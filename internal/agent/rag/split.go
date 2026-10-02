package rag

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino-ext/components/document/transformer/splitter/markdown"
	"github.com/cloudwego/eino-ext/components/document/transformer/splitter/recursive"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/schema"
)

const (
	// chunkSize 与 chunkOverlap 是切分参数。
	//
	// 重叠是必要的：不重叠的话，一个恰好跨越切分边界的句子会被劈成两半，
	// 两个 chunk 各持半句，语义都不完整，检索时两边都匹配不上。
	//
	// chunkSize and chunkOverlap are the splitting parameters.
	//
	// The overlap matters: without it, a sentence landing exactly on a boundary is cut in two,
	// leaving each chunk with half of it, neither semantically complete, and retrieval matches
	// neither.
	chunkSize    = 800
	chunkOverlap = 120
)

// Splitter 按文档类型选择切分策略。
// Splitter picks a splitting strategy per document type.
type Splitter struct {
	markdown  document.Transformer
	recursive document.Transformer
}

// NewSplitter 构造切分器。
// NewSplitter builds the splitter.
func NewSplitter(ctx context.Context) (*Splitter, error) {
	// Markdown 用 header 切分器：它按标题层级切，并把所在标题链写进 metadata。
	// 用通用切分器的话，"安装步骤"这一段会丢掉它属于哪一章的信息，
	// 检索命中之后模型看到的是一段无头无尾的步骤。
	// Markdown uses the header splitter: it cuts along heading levels and records the heading
	// chain in metadata. A generic splitter would strip "installation steps" of the chapter it
	// belongs to, leaving the model with a headless list of steps once retrieved.
	md, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderConfig{
		Headers: map[string]string{"#": "h1", "##": "h2", "###": "h3"},
	})
	if err != nil {
		return nil, fmt.Errorf("init markdown splitter: %w", err)
	}
	rec, err := recursive.NewSplitter(ctx, &recursive.Config{
		ChunkSize:   chunkSize,
		OverlapSize: chunkOverlap,
	})
	if err != nil {
		return nil, fmt.Errorf("init recursive splitter: %w", err)
	}
	return &Splitter{markdown: md, recursive: rec}, nil
}

// Split 把一份文档切成若干片段。
//
// Markdown 走两道：先按标题切出语义完整的小节，再对超长小节用 recursive 兜底。
// 只用 header 切分器的话，一个没有子标题的长章节会变成一个巨大的 chunk，
// 既超嵌入模型的输入上限，检索粒度也太粗。
//
// Split cuts one document into chunks.
//
// Markdown goes through two passes: headings first, producing semantically whole sections, then
// recursive splitting for any section that is still too long. Using only the header splitter
// would turn a long chapter without subheadings into one enormous chunk, exceeding the embedding
// model's input limit and leaving retrieval far too coarse.
func (s *Splitter) Split(ctx context.Context, l *Loaded) ([]*schema.Document, error) {
	doc := l.toDocument()
	ext := strings.ToLower(filepath.Ext(l.Path))

	var (
		parts []*schema.Document
		err   error
	)
	if ext == ".md" || ext == ".markdown" {
		parts, err = s.markdown.Transform(ctx, []*schema.Document{doc})
		if err != nil {
			return nil, fmt.Errorf("markdown split %s: %w", l.Title, err)
		}
		parts, err = s.refine(ctx, parts)
	} else {
		parts, err = s.recursive.Transform(ctx, []*schema.Document{doc})
	}
	if err != nil {
		return nil, fmt.Errorf("split %s: %w", l.Title, err)
	}

	out := make([]*schema.Document, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p.Content) == "" {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// refine 对仍然超长的片段再切一次。
// refine splits any chunk that is still too long a second time.
func (s *Splitter) refine(ctx context.Context, parts []*schema.Document) ([]*schema.Document, error) {
	var out []*schema.Document
	for _, p := range parts {
		if len(p.Content) <= chunkSize {
			out = append(out, p)
			continue
		}
		sub, err := s.recursive.Transform(ctx, []*schema.Document{p})
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// headingPath 从 metadata 里取出标题链，拼进 chunk 正文。
//
// 把标题写进正文而不是只留在 metadata，是因为关键词检索与向量检索都只看正文。
// 标题里的词往往正是用户会搜的词，留在 metadata 里等于白存。
//
// headingPath pulls the heading chain out of metadata and prefixes it to the chunk body.
//
// Headings go into the body rather than staying in metadata because both keyword and vector
// retrieval only look at the body. The words in a heading are frequently exactly what the user
// searches for, so leaving them in metadata wastes them.
func headingPath(d *schema.Document) string {
	if d.MetaData == nil {
		return ""
	}
	var parts []string
	for _, key := range []string{"h1", "h2", "h3"} {
		if v, ok := d.MetaData[key].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " > ")
}
