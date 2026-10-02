package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// 工具名。
// Tool names.
const (
	RememberToolName = "remember"
	RecallToolName   = "recall"
)

// NewRememberTool 构造记忆写入工具。
// NewRememberTool builds the memory writing tool.
func NewRememberTool(s *Store, sessionID string) tool.BaseTool {
	info := &schema.ToolInfo{
		Name: RememberToolName,
		Desc: "记住一件关于用户的长期事实，供以后的对话使用。适合记录偏好、背景、习惯、约定。" +
			"key 要用稳定的短标识（如 coding_style、timezone），同一件事请复用同一个 key 来更新，" +
			"不要每次换新 key。不要记录一次性的临时信息。\n" +
			"Records one long-term fact about the user for future conversations: preferences, " +
			"background, habits, agreements. Use a stable short key such as coding_style or " +
			"timezone, and reuse the same key to update a fact rather than inventing a new one " +
			"each time. Do not record one-off transient details.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"key": {
				Type:     schema.String,
				Desc:     "稳定的短标识，同一事实复用同一个 key / a stable short identifier, reused for the same fact",
				Required: true,
			},
			"content": {
				Type:     schema.String,
				Desc:     "要记住的内容，一句话说清 / the fact to remember, stated in one sentence",
				Required: true,
			},
			"category": {
				Type: schema.String,
				Desc: "分类，如 preference / background / project；可省略 / a category such as " +
					"preference, background or project; optional",
			},
		}),
	}

	handler := func(ctx context.Context, args map[string]any) (string, error) {
		key, _ := args["key"].(string)
		content, _ := args["content"].(string)
		category, _ := args["category"].(string)

		existing, err := s.Get(ctx, key)
		isUpdate := err == nil

		m := Memory{Key: key, Content: content, Category: category, SourceSID: sessionID}
		if isUpdate {
			m.CreatedAt = existing.CreatedAt
		}
		if _, err := s.Put(ctx, m); err != nil {
			return "", err
		}
		if isUpdate {
			return fmt.Sprintf("已更新记忆 %q（原内容：%s）/ memory %q updated (was: %s)",
				key, existing.Content, key, existing.Content), nil
		}
		return fmt.Sprintf("已记住 %q / remembered %q", key, key), nil
	}
	return utils.NewTool[map[string]any, string](info, handler)
}

// NewRecallTool 构造记忆召回工具，仅在记忆数量超过阈值时挂载。
// NewRecallTool builds the memory recall tool, mounted only when the count exceeds the threshold.
func NewRecallTool(s *Store) tool.BaseTool {
	info := &schema.ToolInfo{
		Name: RecallToolName,
		Desc: "按关键词查询关于用户的长期记忆。/ Looks up long-term memories about the user by keyword.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "要查什么，可以是关键词或一句话 / what to look for, a keyword or a sentence",
				Required: true,
			},
		}),
	}

	handler := func(ctx context.Context, args map[string]any) (string, error) {
		query, _ := args["query"].(string)
		all, err := s.List(ctx)
		if err != nil {
			return "", err
		}
		matched := filterMemories(all, query)
		if len(matched) == 0 {
			return "没有找到相关的长期记忆。/ No matching long-term memory.", nil
		}
		var sb strings.Builder
		for _, m := range matched {
			fmt.Fprintf(&sb, "- %s：%s\n", m.Key, m.Content)
		}
		return sb.String(), nil
	}
	return utils.NewTool[map[string]any, string](info, handler)
}

// filterMemories 在内存里做子串匹配。
//
// 记忆超过阈值才用到这个工具，而"阈值"本身也就几十到几百条——这个量级做全表
// 子串匹配是微秒级的事，为它建一套向量索引纯属自找麻烦。真到了记忆上万条那天
// 再说，而那一天大概率不会来：长期记忆的本质就是少而精，多到上万条说明记的
// 全是不该记的东西。
//
// filterMemories does substring matching in memory.
//
// This tool only comes into play above the threshold, and the threshold itself is in the tens to
// low hundreds — scanning that many rows for a substring takes microseconds, and building a
// vector index for it would be self-inflicted complexity. If memories ever reach ten thousand,
// revisit it; that day probably never comes, because long-term memory is meant to be few and
// meaningful, and ten thousand entries would mean recording things that should not be recorded.
func filterMemories(all []Memory, query string) []Memory {
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '，' || r == ',' || r == '。' || r == '？'
	})
	if len(terms) == 0 {
		return all
	}
	var out []Memory
	for _, m := range all {
		hay := strings.ToLower(m.Key + " " + m.Content + " " + m.Category)
		for _, t := range terms {
			if strings.Contains(hay, t) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}
