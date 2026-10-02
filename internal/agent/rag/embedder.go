package rag

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	extemb "github.com/cloudwego/eino-ext/components/embedding/openai"
	"github.com/cloudwego/eino/components/embedding"

	"private/agent_basedon_eino/internal/agent/config"
)

const (
	// embedBatch 是单次嵌入请求的文本条数。
	// 太大容易触发服务端的请求体上限，太小则把一次索引拆成上百个往返。
	// embedBatch is how many texts go into one embedding request. Too large trips the server's
	// body limit; too small turns one indexing run into hundreds of round trips.
	embedBatch = 16
)

// Embedder 把文本转成向量。
// Embedder turns text into vectors.
type Embedder struct {
	inner embedding.Embedder
	model string
}

// NewEmbedder 构造嵌入器。API Key 仅从环境变量读取。
// NewEmbedder builds the embedder. The API key comes from the environment only.
func NewEmbedder(ctx context.Context, model string) (*Embedder, error) {
	apiKey, err := config.APIKey()
	if err != nil {
		return nil, err
	}
	inner, err := extemb.NewEmbedder(ctx, &extemb.EmbeddingConfig{
		APIKey:  apiKey,
		BaseURL: config.BaseURL(),
		Model:   model,
	})
	if err != nil {
		return nil, fmt.Errorf("init embedder %s: %w", model, err)
	}
	return &Embedder{inner: inner, model: model}, nil
}

// Embed 批量向量化。
// Embed vectorizes in batches.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatch {
		end := min(start+embedBatch, len(texts))
		vecs, err := e.inner.EmbedStrings(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch [%d,%d): %w", start, end, err)
		}
		if len(vecs) != end-start {
			return nil, fmt.Errorf("嵌入返回数量不匹配：请求 %d 条，返回 %d 条 / "+
				"embedding count mismatch: requested %d, received %d",
				end-start, len(vecs), end-start, len(vecs))
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// EmbedOne 向量化单条文本。
// EmbedOne vectorizes one text.
func (e *Embedder) EmbedOne(ctx context.Context, text string) ([]float64, error) {
	vecs, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("嵌入返回为空 / embedding returned nothing")
	}
	return vecs[0], nil
}

// packVector 把向量打包成 float32 的小端字节串。
//
// 用 float32 而非 float64：嵌入向量的精度远低于 float64 能表达的范围，存 float64
// 是把体积翻倍换取不存在的精度。一万个 1024 维向量，float32 是 40MB，float64 是 80MB，
// 而检索时这些数据要全部读进内存。
//
// packVector packs a vector as little-endian float32 bytes.
//
// float32 rather than float64: embedding vectors carry far less precision than float64 can
// express, so storing float64 doubles the size in exchange for precision that does not exist.
// Ten thousand 1024-dimensional vectors are 40MB as float32 and 80MB as float64 — and retrieval
// reads all of it into memory.
func packVector(v []float64) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(f)))
	}
	return buf
}

// unpackVector 还原打包的向量。
// unpackVector restores a packed vector.
func unpackVector(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// cosine 计算余弦相似度。
//
// 这里不假设向量已归一化。智谱的 embedding-3 实际返回的是归一化向量，点积即余弦，
// 但依赖这一点意味着换一个嵌入模型时会静默算错——分母恒为 1 的假设不成立后，
// 相似度会被向量模长带偏，而结果看起来仍然是个合理的小数，不会报错。
//
// cosine computes cosine similarity.
//
// It does not assume normalized vectors. ZhipuAI's embedding-3 does return normalized ones,
// making the dot product equal to the cosine, but relying on that would silently miscompute if
// the embedding model is ever swapped: once the denominator is no longer 1, magnitudes skew the
// similarity while the result still looks like a plausible decimal and raises no error.
func cosine(a []float32, b []float64) float64 {
	n := min(len(a), len(b))
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		x, y := float64(a[i]), b[i]
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
