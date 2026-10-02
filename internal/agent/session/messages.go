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
