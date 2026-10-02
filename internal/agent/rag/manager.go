package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Progress 是索引进度快照。
// Progress is a snapshot of indexing progress.
type Progress struct {
	Running  bool   `json:"running"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Failed   int    `json:"failed"`
	Current  string `json:"current"`
	LastRun  int64  `json:"last_run"`
	LastErr  string `json:"last_error"`
	Duration int64  `json:"duration_ms"`
}

// ErrNotFound 表示文档不存在。
// ErrNotFound signals a missing document.
var ErrNotFound = errors.New("document not found")

// Manager 管理文档与索引任务。
// Manager owns documents and indexing jobs.
type Manager struct {
	indexer *Indexer
	dir     string
	logger  *slog.Logger

	mu       sync.Mutex
	progress Progress
}

// NewManager 构造管理器。
// NewManager builds the manager.
func NewManager(ix *Indexer, dir string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{indexer: ix, dir: dir, logger: logger}
}

// Dir 返回文档目录。
// Dir returns the documents directory.
func (m *Manager) Dir() string { return m.dir }

// Progress 返回当前进度。
// Progress returns the current progress.
func (m *Manager) Progress() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress
}

// List 返回全部文档记录。
// List returns every document record.
func (m *Manager) List(ctx context.Context) ([]*Document, error) {
	rows, err := m.indexer.db.Read().QueryContext(ctx, `
		SELECT id, path, title, hash, size, status, error, chunk_count, indexed_at, created_at
		FROM rag_documents ORDER BY title`)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	var out []*Document
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Delete 删除一份文档及其全部索引数据。
// Delete removes a document and all of its index data.
func (m *Manager) Delete(ctx context.Context, id string) error {
	return m.indexer.db.Tx(ctx, func(tx *sql.Tx) error {
		// FTS5 表不受外键级联影响，必须先按 chunk id 手动清掉。
		// The FTS5 table is outside foreign-key cascades and must be cleared by chunk id first.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM rag_chunks_fts WHERE chunk_id IN (SELECT id FROM rag_chunks WHERE doc_id = ?)`,
			id); err != nil {
			return fmt.Errorf("clear fts rows: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM rag_documents WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete document %s: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Upload 把上传的内容写进文档目录并索引它。
// Upload writes uploaded content into the documents directory and indexes it.
func (m *Manager) Upload(ctx context.Context, name string, content []byte) (*Document, error) {
	// 只取文件名，丢掉任何目录部分：上传的文件名来自浏览器，
	// 一个 "../../.ssh/authorized_keys" 就能把文件写到该去的地方之外。
	// Only the base name survives, with any directory component discarded: the upload name comes
	// from the browser, and a single "../../.ssh/authorized_keys" would write outside the
	// intended location.
	base := filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if base == "." || base == ".." || base == "/" || base == "" {
		return nil, fmt.Errorf("文件名 %q 不合法 / invalid file name %q", name, name)
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create documents dir: %w", err)
	}
	target := filepath.Join(m.dir, base)
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", base, err)
	}
	return m.indexer.IndexFile(ctx, target, true)
}

// Reindex 扫描目录并在后台重建索引。
//
// 后台跑而不是同步等：索引几十份文档要调用嵌入服务几十次，同步等意味着 HTTP
// 请求挂几分钟，页面上除了转圈什么也做不了。进度通过 GET /api/rag/status 查。
//
// Reindex scans the directory and rebuilds the index in the background.
//
// It runs in the background rather than synchronously: indexing dozens of documents means dozens
// of embedding calls, and waiting inline would hang the HTTP request for minutes with nothing to
// show but a spinner. Progress is polled via GET /api/rag/status.
func (m *Manager) Reindex(force bool) error {
	m.mu.Lock()
	if m.progress.Running {
		m.mu.Unlock()
		return errors.New("索引正在进行中 / indexing is already running")
	}
	m.progress = Progress{Running: true}
	m.mu.Unlock()

	// 用独立的 context：这个任务比触发它的 HTTP 请求活得久，
	// 沿用请求 context 的话，请求一返回索引就被取消了。
	// An independent context: this job outlives the HTTP request that started it, and inheriting
	// the request context would cancel the indexing the moment the response is written.
	go m.run(context.Background(), force)
	return nil
}

func (m *Manager) run(ctx context.Context, force bool) {
	started := time.Now()
	paths, err := m.indexer.loader.Scan(m.dir)
	if err != nil {
		m.finish(started, err)
		return
	}

	m.mu.Lock()
	m.progress.Total = len(paths)
	m.mu.Unlock()

	for _, p := range paths {
		m.mu.Lock()
		m.progress.Current = filepath.Base(p)
		m.mu.Unlock()

		// 单份失败不中断整体：一份坏掉的 PDF 不应该让其余几十份文档也索引不上。
		// 失败原因已经由 IndexFile 写进了那条记录，页面上看得见。
		// One failure does not abort the run: a broken PDF must not keep dozens of other
		// documents out of the index. IndexFile has already recorded the reason on that row,
		// where the UI can show it.
		if _, err := m.indexer.IndexFile(ctx, p, force); err != nil {
			m.logger.Warn("index document failed", "path", p, "err", err)
			m.mu.Lock()
			m.progress.Failed++
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		m.progress.Done++
		m.mu.Unlock()
	}
	m.finish(started, nil)
}

func (m *Manager) finish(started time.Time, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.progress.Running = false
	m.progress.Current = ""
	m.progress.LastRun = time.Now().UnixMilli()
	m.progress.Duration = time.Since(started).Milliseconds()
	if err != nil {
		m.progress.LastErr = err.Error()
	} else {
		m.progress.LastErr = ""
	}
}
