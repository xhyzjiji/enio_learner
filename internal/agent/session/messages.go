package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"private/agent_basedon_eino/internal/agent/store"
)

// Message 是持久化后的一条消息。它是 schema.Message 的库内表示，
// 两者之间的转换必须无损，尤其是 ToolCall 的 ID。
// Message is one persisted message: the in-database representation of schema.Message.
// The conversion between the two must be lossless, most importantly for ToolCall IDs.
type Message struct {
	ID         string            `json:"id"`
	SessionID  string            `json:"session_id"`
	Seq        int               `json:"seq"`
	Role       string            `json:"role"`
	Content    string            `json:"content"`
	ToolCalls  []schema.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolName   string            `json:"tool_name,omitempty"`
	CreatedAt  int64             `json:"created_at"`
}

// AppendMessages 把一批消息追加到会话尾部，序号在事务内连续分配。
//
// 整批放在一个事务里，是为了避免"assistant 的工具调用写进去了，但对应的 tool 结果
// 没写进去"这种半截状态——恢复会话时模型会看到一个悬空的工具调用，行为不可预测。
//
// AppendMessages appends a batch of messages, allocating consecutive sequence numbers inside a
// transaction.
//
// The whole batch shares one transaction to rule out the half-written state where an
// assistant tool call is persisted but its tool result is not: on resume the model would see a
// dangling tool call and behave unpredictably.
func (s *Store) AppendMessages(ctx context.Context, sessionID string, msgs []*schema.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		var next int
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), -1) + 1 FROM messages WHERE session_id = ?`, sessionID).Scan(&next)
		if err != nil {
			return fmt.Errorf("allocate seq for session %s: %w", sessionID, err)
		}

		now := store.Now()
		for _, m := range msgs {
			if m == nil {
				continue
			}
			encoded, err := encodeToolCalls(m.ToolCalls)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx,
				`INSERT INTO messages (id, session_id, seq, role, content, tool_calls, tool_call_id, tool_name, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				uuid.NewString(), sessionID, next, string(m.Role), m.Content,
				encoded, m.ToolCallID, m.ToolName, now)
			if err != nil {
				return fmt.Errorf("insert message seq %d: %w", next, err)
			}
			next++
		}

		_, err = tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, now, sessionID)
		return err
	})
}

// ListMessages 按序号升序返回一个会话的全部原始消息。
// ListMessages returns all original messages of a conversation in sequence order.
func (s *Store) ListMessages(ctx context.Context, sessionID string) ([]*Message, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, session_id, seq, role, content, tool_calls, tool_call_id, tool_name, created_at
		 FROM messages WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list messages of %s: %w", sessionID, err)
	}
	defer rows.Close()

	out := []*Message{}
	for rows.Next() {
		var m Message
		var rawToolCalls string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Seq, &m.Role, &m.Content,
			&rawToolCalls, &m.ToolCallID, &m.ToolName, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.ToolCalls, err = decodeToolCalls(rawToolCalls)
		if err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// LoadHistory 读取原始消息并还原为 schema.Message，用于在没有压缩快照时重建上下文。
// LoadHistory reads the original messages back into schema.Message, used to rebuild context
// when no compressed snapshot exists.
func (s *Store) LoadHistory(ctx context.Context, sessionID string) ([]*schema.Message, error) {
	stored, err := s.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]*schema.Message, 0, len(stored))
	for _, m := range stored {
		out = append(out, ToSchema(m))
	}
	return out, nil
}

// LastUserMessage 返回会话中最后一条用户消息，供标题生成使用。
// LastUserMessage returns the last user message of a conversation, used for title generation.
func (s *Store) LastUserMessage(ctx context.Context, sessionID string) (string, error) {
	var content string
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT content FROM messages WHERE session_id = ? AND role = 'user'
		 ORDER BY seq DESC LIMIT 1`, sessionID).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read last user message of %s: %w", sessionID, err)
	}
	return content, nil
}

// CountMessages 返回会话的消息条数。
// CountMessages returns how many messages a conversation has.
func (s *Store) CountMessages(ctx context.Context, sessionID string) (int, error) {
	var n int
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count messages of %s: %w", sessionID, err)
	}
	return n, nil
}

// ToSchema 把库内消息还原为 schema.Message。
//
// ToolCallID 与 ToolCalls[i].ID 必须原样带回：assistant 消息里的工具调用与后续
// tool 消息就是靠这个 ID 配对的，一旦重新生成，模型会看到一堆对不上号的工具结果。
//
// ToSchema converts a stored message back into schema.Message.
//
// ToolCallID and ToolCalls[i].ID must come back verbatim: that ID is the only thing pairing an
// assistant tool call with its subsequent tool message, and regenerating it would leave the
// model staring at tool results that match nothing.
func ToSchema(m *Message) *schema.Message {
	return &schema.Message{
		Role:       schema.RoleType(m.Role),
		Content:    m.Content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
		ToolName:   m.ToolName,
	}
}

// emptyToolResult 是空工具结果的占位文字。
// emptyToolResult stands in for a tool result that came back empty.
const emptyToolResult = "[工具执行完成，没有返回内容 / the tool completed and returned nothing]"

// NormalizeForModel 处理掉会让模型服务拒收整轮请求的空消息。
//
// 起因是一个很难从报错反推回来的失败：Ollama 的 OpenAI 兼容层按类型分支解析每条
// 消息的 content，拿到 nil 就回 400 "invalid message content type: <nil>"。而
// go-openai 的 content 字段带 omitempty——内容是空串时整个字段不会出现在 JSON 里，
// 服务端读到的就是 nil。于是**一条内容为空的历史消息会让整轮对话报一个和内容毫无
// 关系的错**，而且报错点落在 ChatModel 节点上，看不出是历史里的哪一条惹的祸。
//
// 两种空消息必须分开处理：
//   - tool 消息不能丢。每个 tool_call 都要有一条对应的 tool 消息，丢掉会让
//     assistant 那边的工具调用悬空，换来另一个 400。所以补占位文字。这对模型也更好：
//     空结果没法区分"跑完了没输出"和"根本没跑"。
//   - 其余角色的空消息直接丢弃。既无正文又无工具调用的 assistant 消息不携带任何信息
//     （常见来源是只产出思维链、正文为空的那一次回复），塞一句假正文反而会被模型
//     当成自己说过的话。
//
// 修在读路径而不是写路径，是因为库里已经存在的坏消息也必须被绕开——只拦新写入的话，
// 出过问题的会话会永远卡在那个 400 上，除非删掉重开。
//
// NormalizeForModel removes the empty messages that make a model service reject an entire turn.
//
// The cause is a failure that is hard to reason backwards from: Ollama's OpenAI-compatibility
// layer type-switches on each message's content and answers 400 "invalid message content type:
// <nil>" when it gets nil. go-openai's content field carries omitempty, so an empty string makes
// the field vanish from the JSON and the server reads nil. One empty history message therefore
// fails the whole turn with an error unrelated to content, reported against the ChatModel node,
// giving no clue which history entry is at fault.
//
// The two kinds of empty message need opposite treatment:
//   - Tool messages must not be dropped. Every tool_call requires a matching tool message, and
//     removing one leaves the assistant's call dangling — another 400. A placeholder goes in
//     instead, which also helps the model: an empty result cannot be told apart from "never ran".
//   - Empty messages of any other role are dropped. An assistant message with neither content nor
//     tool calls carries nothing (it typically comes from a reply that produced only reasoning),
//     and inventing body text would have the model treat it as something it had said.
//
// This lives on the read path rather than the write path because messages already stored must be
// worked around too: guarding only new writes would leave an affected conversation permanently
// stuck on that 400, short of deleting it and starting over.
func NormalizeForModel(msgs []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		if m.Content != "" || len(m.ToolCalls) > 0 {
			out = append(out, m)
			continue
		}
		if m.Role == schema.Tool {
			// 复制一份再改。这些消息可能来自压缩快照的缓存切片，就地修改会把缓存里的
			// 内容也改掉，而那份数据下一轮还要用。
			// Copy before editing. These may come from a compressed snapshot's cached slice, and
			// mutating in place would alter data that the next turn still relies on.
			fixed := *m
			fixed.Content = emptyToolResult
			out = append(out, &fixed)
		}
	}
	return out
}

func encodeToolCalls(tcs []schema.ToolCall) (string, error) {
	if len(tcs) == 0 {
		return "", nil
	}
	b, err := json.Marshal(tcs)
	if err != nil {
		return "", fmt.Errorf("encode tool calls: %w", err)
	}
	return string(b), nil
}

func decodeToolCalls(raw string) ([]schema.ToolCall, error) {
	if raw == "" {
		return nil, nil
	}
	var tcs []schema.ToolCall
	if err := json.Unmarshal([]byte(raw), &tcs); err != nil {
		return nil, fmt.Errorf("decode tool calls: %w", err)
	}
	return tcs, nil
}
