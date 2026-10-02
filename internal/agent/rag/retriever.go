package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"private/agent_basedon_eino/internal/agent/store"
)

const (
	// rrfK 是 RRF 的平滑常数。60 是该方法原论文的取值，也是各家实现的事实默认。
	// rrfK is the RRF smoothing constant. 60 comes from the original paper and is the de facto
	// default across implementations.
	rrfK = 60.0

	// recallDepth 是融合前每一路各自召回的条数。
	// 取得比最终 TopK 大是必要的：只有一路命中的好结果，融合后排名会被拉低，
	// 召回太浅它根本进不了融合。
	// recallDepth is how many results each branch retrieves before fusion. It must exceed the
	// final TopK: a strong result found by only one branch ranks lower after fusion, and a
	// shallow recall would keep it out of the fusion entirely.
	recallDepth = 30
)

// Hit 是一条检索结果。
// Hit is one retrieval result.
type Hit struct {
	ChunkID int64   `json:"chunk_id"`
	DocID   string  `json:"doc_id"`
	Title   string  `json:"title"`
	Path    string  `json:"path"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// Retriever 做向量与关键词的混合检索。
// Retriever performs hybrid vector and keyword retrieval.
type Retriever struct {
	db       *store.DB
	embedder *Embedder
}

// NewRetriever 构造检索器。
// NewRetriever builds the retriever.
func NewRetriever(db *store.DB, e *Embedder) *Retriever {
	return &Retriever{db: db, embedder: e}
}

// Retrieve 返回与查询最相关的若干片段。
//
// 两路并行召回后用 RRF（倒数排名融合）合并，而**不是**把两路分数加权求和。
// 原因是量纲：余弦相似度在 [-1,1]，FTS5 的 BM25 是一个没有上界的负值，两者
// 相加在数学上没有意义——BM25 的绝对值随语料规模漂移，加权系数今天调好了，
// 文档一多就又偏了。RRF 只用名次，天然规避了这个问题。
//
// Retrieve returns the chunks most relevant to a query.
//
// The two branches run in parallel and are merged with RRF (reciprocal rank fusion) rather than
// a weighted sum of their scores. The reason is dimensionality: cosine similarity lives in
// [-1,1] while FTS5's BM25 is an unbounded negative number, so adding them is mathematically
// meaningless — BM25's magnitude drifts with corpus size, and a weighting tuned today skews
// again as documents accumulate. RRF uses ranks only and sidesteps this entirely.
func (r *Retriever) Retrieve(ctx context.Context, query string, topK int) ([]Hit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("查询不能为空 / query must not be empty")
	}
	if topK <= 0 {
		topK = 8
	}

	var (
		wg              sync.WaitGroup
		vecHits, kwHits []Hit
		vecErr, kwErr   error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		vecHits, vecErr = r.vectorSearch(ctx, query)
	}()
	go func() {
		defer wg.Done()
		kwHits, kwErr = r.keywordSearch(ctx, query)
	}()
	wg.Wait()

	// 一路失败不放弃另一路：嵌入服务不可用时，关键词检索仍然能给出有用的结果，
	// 总比一个空答案好。两路都失败才真正报错。
	// One failing branch does not abandon the other: with the embedding service down, keyword
	// search still yields something useful, which beats an empty answer. Only a double failure
	// is a real error.
	if vecErr != nil && kwErr != nil {
		return nil, fmt.Errorf("向量检索与关键词检索均失败 / both vector and keyword search failed: %w", vecErr)
	}

	fused := fuseRRF(vecHits, kwHits)
	if len(fused) > topK {
		fused = fused[:topK]
	}
	return reorderForAttention(fused), nil
}

// vectorSearch 全量计算余弦相似度。
//
// 没有近似索引，就是把全部向量读进来逐个算。万级 chunk 下这是几十毫秒的事，
// 而换来的是零依赖和绝对准确的召回——近似索引在这个规模上省下的时间还不够它
// 自己的构建开销。
//
// vectorSearch computes cosine similarity exhaustively.
//
// There is no approximate index: every vector is read in and scored. At ten-thousand chunk scale
// that is tens of milliseconds, bought with zero dependencies and exact recall — an approximate
// index would not save enough time at this size to pay for its own build cost.
func (r *Retriever) vectorSearch(ctx context.Context, query string) ([]Hit, error) {
	qv, err := r.embedder.EmbedOne(ctx, query)
	if err != nil {
		return nil, err
	}

	// 只比较维度相同的向量。换过嵌入模型后，库里会留下上一代模型产生的向量，
	// 而余弦相似度对维度不一致的输入不会报错——截断到较短的那个长度照样能算出
	// 一个像模像样的小数，检索结果于是无声地变成噪声。
	//
	// Only vectors of matching dimensionality are compared. After the embedding model is
	// switched, vectors produced by the previous one remain in the store, and cosine similarity
	// raises no error on mismatched input — truncating to the shorter length still yields a
	// plausible-looking decimal, and retrieval silently turns into noise.
	rows, err := r.db.Read().QueryContext(ctx, `
		SELECT c.id, c.doc_id, d.title, d.path, c.content, v.vector
		FROM rag_vectors v
		JOIN rag_chunks c ON c.id = v.chunk_id
		JOIN rag_documents d ON d.id = c.doc_id
		WHERE d.status = ? AND v.dim = ?`, StatusIndexed, len(qv))
	if err != nil {
		return nil, fmt.Errorf("scan vectors: %w", err)
	}
	defer rows.Close()

	var out []Hit
	for rows.Next() {
		var (
			h   Hit
			raw []byte
		)
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.Title, &h.Path, &h.Content, &raw); err != nil {
			return nil, err
		}
		h.Score = cosine(unpackVector(raw), qv)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// 一条都没匹配上时要查清是"库是空的"还是"库里全是旧维度的向量"。
		// 后者必须说出来：否则换完嵌入模型后检索只剩关键词一路，质量下降但毫无提示。
		//
		// When nothing matches, distinguish an empty store from one holding only stale
		// dimensions. The latter must be surfaced: otherwise retrieval silently falls back to
		// the keyword branch alone after an embedding-model switch, degrading quality with no
		// indication of why.
		var stale int
		_ = r.db.Read().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM rag_vectors WHERE dim != ?`, len(qv)).Scan(&stale)
		if stale > 0 {
			return nil, fmt.Errorf(
				"库中 %d 条向量的维度与当前嵌入模型（%d 维）不一致，说明嵌入模型换过，请重建索引 / "+
					"%d stored vectors do not match the current embedding model's %d dimensions; "+
					"the embedding model was changed, so the index must be rebuilt",
				stale, len(qv), stale, len(qv))
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > recallDepth {
		out = out[:recallDepth]
	}
	return out, nil
}

// keywordSearch 走 FTS5 的 BM25 排序，短查询降级为 LIKE 扫描。
// keywordSearch uses FTS5's BM25 ranking, degrading to a LIKE scan for short queries.
func (r *Retriever) keywordSearch(ctx context.Context, query string) ([]Hit, error) {
	expr, short := ftsQuery(query)

	// 短词走 LIKE 兜底。它与 FTS 是补充关系而非替代：一个查询里同时出现
	// "分词" 和 "关键词检索" 时，两路各自能查到的内容都不该丢。
	// Short terms go through the LIKE fallback. It complements FTS rather than replacing it:
	// when a query contains both "分词" and "关键词检索", neither branch's findings should be
	// thrown away.
	var fallback []Hit
	if len(short) > 0 {
		hits, err := r.likeSearch(ctx, short)
		if err != nil {
			return nil, err
		}
		fallback = hits
	}
	if expr == "" {
		return fallback, nil
	}

	rows, err := r.db.Read().QueryContext(ctx, `
		SELECT c.id, c.doc_id, d.title, d.path, c.content, bm25(rag_chunks_fts)
		FROM rag_chunks_fts f
		JOIN rag_chunks c ON c.id = f.chunk_id
		JOIN rag_documents d ON d.id = c.doc_id
		WHERE rag_chunks_fts MATCH ? AND d.status = ?
		ORDER BY bm25(rag_chunks_fts)
		LIMIT ?`, expr, StatusIndexed, recallDepth)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.Title, &h.Path, &h.Content, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return appendUnseen(out, fallback), nil
}

// appendUnseen 把兜底结果里没出现过的补到末尾。
// appendUnseen appends the fallback results that are not already present.
func appendUnseen(primary, extra []Hit) []Hit {
	if len(extra) == 0 {
		return primary
	}
	seen := make(map[int64]bool, len(primary))
	for _, h := range primary {
		seen[h.ChunkID] = true
	}
	for _, h := range extra {
		if !seen[h.ChunkID] {
			primary = append(primary, h)
		}
	}
	return primary
}

// ftsQuery 把自然语言查询转成 FTS5 表达式，并返回无法用 FTS 表达的短词。
//
// 中文这一路有两个实测踩到的坑（T083）：
//
//  1. 整句加引号当短语查会恒为空。"如何配置定时任务" 作为短语要求文档里原样出现
//     这八个字，而文档里实际写的是 "定时任务通过 cron…"。所以中文串必须拆成
//     重叠三元组再 OR，靠 "定时任" 与 "时任务" 命中。
//  2. 少于三个字的中文词 trigram 根本索引不到。"分词" 这类两字查询在 FTS 里
//     永远查不出东西，只能交给 LIKE 兜底。
//
// ftsQuery converts a natural-language query into an FTS5 expression and returns the short terms
// that FTS cannot express.
//
// The Chinese path has two pitfalls found by measurement (T083):
//
//  1. Quoting a whole sentence as a phrase always returns nothing. As a phrase,
//     "如何配置定时任务" demands those eight characters verbatim, while the document actually
//     reads "定时任务通过 cron…". Chinese runs must therefore be expanded into overlapping
//     trigrams and OR-ed, matching via "定时任" and "时任务".
//  2. Chinese terms shorter than three characters are not indexable by trigram at all. A
//     two-character query such as "分词" can never be found through FTS and has to fall back
//     to LIKE.
func ftsQuery(query string) (expr string, short []string) {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	})

	var terms []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		runes := []rune(f)
		if !hasCJK(runes) {
			// 非中文词本身就是一个 token，整体加引号即可。
			// A non-Chinese term is already one token and just needs quoting.
			terms = append(terms, quoteFTS(f))
			continue
		}
		if len(runes) < 3 {
			short = append(short, f)
			continue
		}
		for i := 0; i+3 <= len(runes); i++ {
			terms = append(terms, quoteFTS(string(runes[i:i+3])))
		}
	}
	if len(terms) == 0 {
		return "", short
	}
	// 用 OR 而非 AND：一个问句里总有几个词在文档里不出现，AND 会让召回恒为空。
	// OR rather than AND: some words of any question never appear in the documents, and AND
	// would make recall permanently empty.
	return strings.Join(terms, " OR "), short
}

func quoteFTS(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func hasCJK(runes []rune) bool {
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

// likeSearch 是短查询的兜底：直接在 chunk 正文上做子串匹配。
//
// 它没有 BM25 那样的相关性排序，只按文档顺序返回。但对于"分词"这种两字查询，
// 有序但不精排的结果远胜于一个空列表——后者会让模型直接断言"文档里没有"。
//
// likeSearch is the fallback for short queries: plain substring matching on the chunk body.
//
// It has no BM25 relevance ordering and simply returns documents in order. For a two-character
// query like "分词", though, unranked results beat an empty list by a wide margin: an empty one
// leads the model to flatly assert that the documents do not mention it.
func (r *Retriever) likeSearch(ctx context.Context, terms []string) ([]Hit, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	var (
		clauses []string
		args    []any
	)
	for _, t := range terms {
		// ESCAPE 必须显式声明：SQLite 的 LIKE 默认没有转义符，
		// escapeLike 加的反斜杠不声明就会被当成要匹配的字面反斜杠。
		// The ESCAPE clause must be explicit: SQLite's LIKE has no default escape character, and
		// without it the backslashes added by escapeLike would be matched literally.
		clauses = append(clauses, `c.content LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(t)+"%")
	}
	args = append(args, StatusIndexed, recallDepth)

	rows, err := r.db.Read().QueryContext(ctx, fmt.Sprintf(`
		SELECT c.id, c.doc_id, d.title, d.path, c.content
		FROM rag_chunks c
		JOIN rag_documents d ON d.id = c.doc_id
		WHERE (%s) AND d.status = ?
		LIMIT ?`, strings.Join(clauses, " OR ")), args...)
	if err != nil {
		return nil, fmt.Errorf("like search: %w", err)
	}
	defer rows.Close()

	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.Title, &h.Path, &h.Content); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// escapeLike 转义 LIKE 的通配符，让用户输入的 % 和 _ 只匹配它们自己。
// escapeLike escapes LIKE wildcards so a user-typed % or _ matches only itself.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// fuseRRF 用倒数排名融合合并两路结果。
// fuseRRF merges the two branches with reciprocal rank fusion.
func fuseRRF(lists ...[]Hit) []Hit {
	scores := make(map[int64]float64)
	hits := make(map[int64]Hit)
	for _, list := range lists {
		for rank, h := range list {
			scores[h.ChunkID] += 1.0 / (rrfK + float64(rank+1))
			if _, ok := hits[h.ChunkID]; !ok {
				hits[h.ChunkID] = h
			}
		}
	}
	out := make([]Hit, 0, len(hits))
	for id, h := range hits {
		h.Score = scores[id]
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	return out
}

// reorderForAttention 把高分片段放到首尾，低分的留在中间。
//
// 这是针对 "lost in the middle" 的排布：模型对长上下文中段内容的注意力明显弱于
// 首尾。按相关性从高到低顺序平铺的话，第二相关的那段恰好落在注意力最弱的位置。
//
// reorderForAttention places the highest-scoring chunks at both ends and the weaker ones in the
// middle.
//
// This targets "lost in the middle": a model attends noticeably less to the middle of a long
// context than to either end. Laying results out in plain descending relevance would drop the
// second-most relevant passage exactly where attention is weakest.
func reorderForAttention(hits []Hit) []Hit {
	if len(hits) < 3 {
		return hits
	}
	head := make([]Hit, 0, len(hits))
	tail := make([]Hit, 0, len(hits))
	for i, h := range hits {
		if i%2 == 0 {
			head = append(head, h)
		} else {
			tail = append(tail, h)
		}
	}
	for i := len(tail) - 1; i >= 0; i-- {
		head = append(head, tail[i])
	}
	return head
}

// KeywordSearchForTest 暴露关键词检索一路，供离线验证中文分词效果时调用。
// 单独开一个导出方法而不是把 keywordSearch 导出，是为了让"这是验证用入口"
// 这件事在名字上就写清楚。
// KeywordSearchForTest exposes the keyword branch for offline verification of Chinese
// tokenization. It is a separate exported method rather than exporting keywordSearch so that
// "this is a verification entry point" is obvious from the name alone.
func (r *Retriever) KeywordSearchForTest(ctx context.Context, query string) ([]Hit, error) {
	return r.keywordSearch(ctx, query)
}
