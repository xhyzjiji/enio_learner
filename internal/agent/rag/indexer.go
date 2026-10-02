package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"private/agent_basedon_eino/internal/agent/store"
)

// 文档索引状态。
// Document indexing states.
const (
	StatusPending = "pending"
	StatusIndexed = "indexed"
	StatusFailed  = "failed"
)

// Document 是一条文档记录。
// Document is one document record.
type Document struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Title      string `json:"title"`
	Hash       string `json:"hash"`
	Size       int64  `json:"size"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	ChunkCount int    `json:"chunk_count"`
	IndexedAt  int64  `json:"indexed_at"`
	CreatedAt  int64  `json:"created_at"`
}

// Indexer 把文档写入索引。
// Indexer writes documents into the index.
type Indexer struct {
	db       *store.DB
	loader   *Loader
	splitter *Splitter
	embedder *Embedder
}

// NewIndexer 构造索引器。
// NewIndexer builds the indexer.
func NewIndexer(db *store.DB, l *Loader, s *Splitter, e *Embedder) *Indexer {
	return &Indexer{db: db, loader: l, splitter: s, embedder: e}
}

// IndexFile 索引单份文档，内容未变时跳过。
//
// 增量跳过靠内容 hash 而不是修改时间：`touch` 一下文件、或者把文件拷来拷去，
// 修改时间就变了，但内容一个字没改。按时间判断会让每次扫描都重新嵌入一遍全部文档，
// 那是实打实的 API 费用。
//
// IndexFile indexes one document, skipping it when the content is unchanged.
//
// Incremental skipping keys on a content hash rather than the modification time: a `touch`, or
// simply copying files around, changes the timestamp while not a single byte of content differs.
// Keying on time would re-embed every document on every scan, at real API cost.
func (ix *Indexer) IndexFile(ctx context.Context, path string, force bool) (*Document, error) {
	loaded, loadErr := ix.loader.Load(ctx, path)
	if loadErr != nil {
		// 解析失败也要落一条记录：失败原因必须在页面上看得见。
		// 只记日志的话，用户只会发现"这份文档搜不到"，却无从知道为什么。
		// A parse failure still gets a row: the reason must be visible in the UI. Logging only
		// would leave the user noticing that a document is unsearchable with no way to find out
		// why.
		doc := &Document{
			ID: uuid.NewString(), Path: path, Title: baseName(path),
			Status: StatusFailed, Error: loadErr.Error(), CreatedAt: store.Now(),
		}
		if err := ix.upsertDoc(ctx, doc); err != nil {
			return nil, err
		}
		return doc, loadErr
	}

	existing, err := ix.getByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if existing != nil && !force && existing.Hash == loaded.Hash && existing.Status == StatusIndexed {
		return existing, nil
	}

	chunks, err := ix.splitter.Split(ctx, loaded)
	if err != nil {
		return nil, err
	}
	texts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		text := c.Content
		if path := headingPath(c); path != "" {
			text = path + "\n" + text
		}
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return nil, fmt.Errorf("文档 %s 切分后没有内容 / document %s produced no chunks after splitting",
			loaded.Title, loaded.Title)
	}

	vectors, err := ix.embedder.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}

	doc := &Document{
		ID: uuid.NewString(), Path: path, Title: loaded.Title, Hash: loaded.Hash,
		Size: loaded.Size, Status: StatusIndexed, ChunkCount: len(texts),
		IndexedAt: store.Now(), CreatedAt: store.Now(),
	}
	if existing != nil {
		doc.ID, doc.CreatedAt = existing.ID, existing.CreatedAt
	}

	if err := ix.write(ctx, doc, texts, vectors); err != nil {
		return nil, err
	}
	return doc, nil
}

// write 在一个事务里替换该文档的全部索引数据。
//
// 必须是事务：chunks、vectors、FTS5 三处数据要么一起换掉，要么一处都不动。
// 中途失败留下半套索引的后果是，检索会命中已经不存在的 chunk，或者同一段内容
// 出现新旧两份。
//
// write replaces all index data of one document inside a transaction.
//
// A transaction is required: chunks, vectors and the FTS5 table must all be replaced together or
// not at all. A half-written index means retrieval hits chunks that no longer exist, or the same
// passage appearing in both its old and new form.
func (ix *Indexer) write(ctx context.Context, doc *Document, texts []string, vectors [][]float64) error {
	return ix.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rag_documents (id, path, title, hash, size, status, error, chunk_count, indexed_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET
				title = excluded.title, hash = excluded.hash, size = excluded.size,
				status = excluded.status, error = '', chunk_count = excluded.chunk_count,
				indexed_at = excluded.indexed_at`,
			doc.ID, doc.Path, doc.Title, doc.Hash, doc.Size, doc.Status,
			doc.ChunkCount, doc.IndexedAt, doc.CreatedAt); err != nil {
			return fmt.Errorf("upsert document %s: %w", doc.Title, err)
		}

		// FTS5 是外部内容无关的独立表，级联删除管不到它，必须手动清。
		// 漏了这一步，旧 chunk 会永远留在关键词索引里，检索结果指向已经不存在的 id。
		// The FTS5 table is standalone and unaffected by cascading deletes, so it must be cleared
		// by hand. Skipping this leaves stale chunks in the keyword index forever, with results
		// pointing at ids that no longer exist.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM rag_chunks_fts WHERE chunk_id IN (SELECT id FROM rag_chunks WHERE doc_id = ?)`,
			doc.ID); err != nil {
			return fmt.Errorf("clear fts rows of %s: %w", doc.Title, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM rag_chunks WHERE doc_id = ?`, doc.ID); err != nil {
			return fmt.Errorf("clear chunks of %s: %w", doc.Title, err)
		}

		for i, text := range texts {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO rag_chunks (doc_id, ordinal, content, token_count) VALUES (?, ?, ?, ?)`,
				doc.ID, i, text, len(text)/4)
			if err != nil {
				return fmt.Errorf("insert chunk %d of %s: %w", i, doc.Title, err)
			}
			chunkID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO rag_vectors (chunk_id, dim, vector) VALUES (?, ?, ?)`,
				chunkID, len(vectors[i]), packVector(vectors[i])); err != nil {
				return fmt.Errorf("insert vector %d of %s: %w", i, doc.Title, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO rag_chunks_fts (content, chunk_id) VALUES (?, ?)`,
				text, chunkID); err != nil {
				return fmt.Errorf("insert fts row %d of %s: %w", i, doc.Title, err)
			}
		}
		return nil
	})
}

func (ix *Indexer) upsertDoc(ctx context.Context, doc *Document) error {
	_, err := ix.db.Write().ExecContext(ctx, `
		INSERT INTO rag_documents (id, path, title, hash, size, status, error, chunk_count, indexed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			title = excluded.title, status = excluded.status, error = excluded.error`,
		doc.ID, doc.Path, doc.Title, doc.Hash, doc.Size, doc.Status, doc.Error,
		doc.ChunkCount, doc.IndexedAt, doc.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert document %s: %w", doc.Title, err)
	}
	return nil
}

func (ix *Indexer) getByPath(ctx context.Context, path string) (*Document, error) {
	row := ix.db.Read().QueryRowContext(ctx, `
		SELECT id, path, title, hash, size, status, error, chunk_count, indexed_at, created_at
		FROM rag_documents WHERE path = ?`, path)
	doc, err := scanDoc(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return doc, err
}

func scanDoc(sc interface{ Scan(...any) error }) (*Document, error) {
	var d Document
	if err := sc.Scan(&d.ID, &d.Path, &d.Title, &d.Hash, &d.Size, &d.Status,
		&d.Error, &d.ChunkCount, &d.IndexedAt, &d.CreatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
