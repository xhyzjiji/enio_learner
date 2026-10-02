package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"private/agent_basedon_eino/internal/agent/kernel"
)

// SearchToolName 是文档检索工具的名字。
// SearchToolName is the name of the document search tool.
const SearchToolName = "search_docs"

// NewSearchTool 构造 search_docs 工具。
// NewSearchTool builds the search_docs tool.
func NewSearchTool(r *Retriever, topK int) tool.BaseTool {
	info := &schema.ToolInfo{
		Name: SearchToolName,
		Desc: "在用户的本地文档库中检索相关内容。当问题涉及用户自己的资料、项目文档、" +
			"笔记等本地信息时使用它，不要凭记忆作答。" +
			"Searches the user's local document library. Use it whenever a question touches the " +
			"user's own material — project docs, notes and the like — instead of answering from memory.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "检索词，用自然语言描述要找的内容 / what to look for, in natural language",
				Required: true,
			},
		}),
	}

	handler := func(ctx context.Context, args map[string]any) (string, error) {
		query, _ := args["query"].(string)
		hits, err := r.Retrieve(ctx, query, topK)
		if err != nil {
			return "", err
		}
		return formatHits(hits), nil
	}
	return utils.NewTool[map[string]any, string](info, handler)
}

// formatHits 把检索结果排版成模型易读的文本。
//
// 每段都带上出处。没有出处的话，模型没法在回答里说"这出自哪份文档"，
// 用户就无从核对——而本地文档检索的结果恰恰是最需要能核对的那类信息。
//
// formatHits lays retrieval results out in a form the model reads easily.
//
// Every passage carries its source. Without one the model cannot say which document an answer
// came from, leaving the user unable to verify it — and local document results are precisely the
// kind of information that most needs verifying.
func formatHits(hits []Hit) string {
	if len(hits) == 0 {
		return "没有检索到相关内容。可能是文档库中确实没有，也可能是索引尚未建立。\n" +
			"No relevant content found. The library may genuinely lack it, or the index may not be built yet."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "检索到 %d 条相关内容 / %d relevant passages:\n\n", len(hits), len(hits))
	for i, h := range hits {
		fmt.Fprintf(&sb, "【%d】出处 / source: %s\n%s\n\n", i+1, h.Title, strings.TrimSpace(h.Content))
	}
	return sb.String()
}

// Augmenter 返回把 search_docs 写进快照的 kernel.Augmenter。
// Augmenter returns a kernel.Augmenter that adds search_docs to the snapshot.
func (r *Retriever) Augmenter() kernel.Augmenter { return &ragAugmenter{r: r} }

type ragAugmenter struct{ r *Retriever }

func (a *ragAugmenter) Name() string { return "rag" }

func (a *ragAugmenter) Augment(_ context.Context, snap *kernel.Snapshot) error {
	snap.Tools = append(snap.Tools, NewSearchTool(a.r, snap.Runtime.RetrievalTopK))
	return nil
}
