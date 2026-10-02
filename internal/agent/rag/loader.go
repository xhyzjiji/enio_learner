// Package rag 实现本地文档的加载、切分、向量化、索引与混合检索。
//
// 整体不引入向量数据库：本地场景的文档量在万级 chunk 以内，全量余弦计算在几十毫秒
// 量级，而引入一个外部向量库意味着多一个要装、要起、要备份的进程。这个取舍在
// plan.md 的 D5 有完整论证。
//
// Package rag implements loading, splitting, embedding, indexing and hybrid retrieval of local
// documents.
//
// No vector database is involved: a local corpus stays within ten thousand chunks, a full cosine
// pass takes tens of milliseconds, and adding an external vector store means one more process to
// install, run and back up. Decision D5 in plan.md argues the trade-off in full.
package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino-ext/components/document/parser/docx"
	"github.com/cloudwego/eino-ext/components/document/parser/html"
	"github.com/cloudwego/eino-ext/components/document/parser/pdf"
	"github.com/cloudwego/eino-ext/components/document/parser/xlsx"
	"github.com/cloudwego/eino/components/document/parser"
	"github.com/cloudwego/eino/schema"
)

// maxDocBytes 限制单份文档大小。
// 超大文件切出来的 chunk 会淹没检索结果，而且嵌入调用的费用与耗时都不成比例。
// maxDocBytes caps a single document. Chunks from an oversized file drown the retrieval results,
// and the cost and latency of embedding them are out of proportion.
const maxDocBytes = 32 << 20

// Loaded 是一份成功读入的文档。
// Loaded is one successfully ingested document.
type Loaded struct {
	Path    string
	Title   string
	Content string
	Hash    string
	Size    int64
}

// Loader 按扩展名派发解析器把文件读成纯文本。
// Loader dispatches a parser by file extension to turn a file into plain text.
type Loader struct {
	parsers map[string]parser.Parser
	text    parser.Parser
}

// NewLoader 构造加载器。
// NewLoader builds the loader.
func NewLoader(ctx context.Context) (*Loader, error) {
	text := parser.TextParser{}

	pdfParser, err := pdf.NewPDFParser(ctx, &pdf.Config{ToPages: false})
	if err != nil {
		return nil, fmt.Errorf("init pdf parser: %w", err)
	}
	docxParser, err := docx.NewDocxParser(ctx, &docx.Config{})
	if err != nil {
		return nil, fmt.Errorf("init docx parser: %w", err)
	}
	htmlParser, err := html.NewParser(ctx, &html.Config{})
	if err != nil {
		return nil, fmt.Errorf("init html parser: %w", err)
	}
	xlsxParser, err := xlsx.NewXlsxParser(ctx, &xlsx.Config{})
	if err != nil {
		return nil, fmt.Errorf("init xlsx parser: %w", err)
	}

	return &Loader{
		text: text,
		parsers: map[string]parser.Parser{
			".pdf":  pdfParser,
			".docx": docxParser,
			".html": htmlParser,
			".htm":  htmlParser,
			".xlsx": xlsxParser,
		},
	}, nil
}

// textExtensions 是直读的纯文本扩展名。
// textExtensions lists extensions read as plain text directly.
var textExtensions = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".json": true, ".yaml": true,
	".yml": true, ".csv": true, ".log": true, ".go": true, ".py": true,
	".ts": true, ".tsx": true, ".js": true, ".java": true, ".sql": true, ".sh": true,
}

// Supported 判断一个路径是否可被索引。
// Supported reports whether a path can be indexed.
func (l *Loader) Supported(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return textExtensions[ext] || l.parsers[ext] != nil
}

// Load 读取并解析单份文档。
// Load reads and parses a single document.
func (l *Loader) Load(ctx context.Context, path string) (*Loaded, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() > maxDocBytes {
		return nil, fmt.Errorf("文档 %s 有 %d 字节，超过上限 %d / document %s is %d bytes, above the %d cap",
			filepath.Base(path), info.Size(), maxDocBytes, filepath.Base(path), info.Size(), maxDocBytes)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)

	ext := strings.ToLower(filepath.Ext(path))
	p := l.parsers[ext]
	if p == nil {
		p = l.text
	}
	docs, err := p.Parse(ctx, strings.NewReader(string(raw)), parser.WithURI(path))
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败 / cannot parse %s: %w", filepath.Base(path), filepath.Base(path), err)
	}

	var sb strings.Builder
	for _, d := range docs {
		sb.WriteString(d.Content)
		sb.WriteString("\n")
	}
	content := strings.TrimSpace(sb.String())
	if content == "" {
		return nil, fmt.Errorf("文档 %s 解析后内容为空 / document %s yielded no text after parsing",
			filepath.Base(path), filepath.Base(path))
	}

	return &Loaded{
		Path:    path,
		Title:   filepath.Base(path),
		Content: content,
		Hash:    hex.EncodeToString(sum[:]),
		Size:    info.Size(),
	}, nil
}

// Scan 遍历目录，返回全部可索引文件的路径。
// Scan walks a directory and returns every indexable file path.
func (l *Loader) Scan(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // 单个条目不可读不应中断整次扫描 / one unreadable entry must not abort the scan
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if l.Supported(p) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	return out, nil
}

// toDocument 把加载结果转成 Eino 的 Document，供切分器消费。
// toDocument converts a load result into an Eino Document for the splitter.
func (l *Loaded) toDocument() *schema.Document {
	return &schema.Document{
		ID:       l.Path,
		Content:  l.Content,
		MetaData: map[string]any{"path": l.Path, "title": l.Title},
	}
}
