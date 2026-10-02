package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"private/agent_basedon_eino/internal/agent/store"
)

// ContextSnapshot 是喂给模型的输入视图。
//
// 它与 messages 表的关系是「同一段对话的两个视角」，不是主从：
// Compressed 为真时，Messages 里已经是摘要替换过的历史，与 messages 表的原文不再逐条对应，
// 但两者描述的必须是同一段对话——这条一致性是本 feature 最高风险项，
// 因为一旦破坏，页面显示正常而模型看到的是错乱历史，几乎无法从表象察觉。
//
// ContextSnapshot is the model-input view of a conversation.
//
// Its relationship with the messages table is "two views of the same conversation", not
// master/replica: once Compressed is true, Messages holds summary-replaced history that no
// longer maps one-to-one onto the original rows, yet both must still describe the same
// conversation. That consistency is the highest risk in this feature, because breaking it
// leaves the UI looking correct while the model sees garbled history.
type ContextSnapshot struct {
	// Messages 是模型下一轮的输入历史。
	// Messages is the input history for the model's next turn.
	Messages []*schema.Message `json:"messages"`

	// Compressed 标记该快照是否已经过有损压缩。
	// Compressed marks whether this snapshot has undergone lossy compression.
	Compressed bool `json:"compressed"`

	// SourceSeq 是快照覆盖到的最大原始消息序号。
	// 恢复时用它判断「快照之后还有哪些新消息需要追加」，避免整段历史重放。
	// SourceSeq is the highest original message sequence the snapshot covers. On resume it
	// tells which newer messages must be appended, avoiding a full history replay.
	SourceSeq int `json:"source_seq"`

	// UpdatedAt 是快照写入时间。
	// UpdatedAt is when the snapshot was written.
	UpdatedAt int64 `json:"updated_at"`
}

// SaveContext 写入或覆盖模型输入快照。
// SaveContext writes or replaces the model-input snapshot.
func (s *Store) SaveContext(ctx context.Context, sessionID string, snap *ContextSnapshot) error {
	if snap == nil {
		return errors.New("context snapshot is nil")
	}
	snap.UpdatedAt = store.Now()
	payload, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encode context snapshot of %s: %w", sessionID, err)
	}
	_, err = s.db.Write().ExecContext(ctx,
		`INSERT INTO session_context (session_id, payload, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (session_id) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at`,
		sessionID, string(payload), snap.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save context snapshot of %s: %w", sessionID, err)
	}
	return nil
}

// LoadContext 读取模型输入快照。不存在时返回 nil 而非错误——新会话本来就没有快照。
// LoadContext reads the model-input snapshot. Absence returns nil rather than an error: a new
// conversation simply has none yet.
func (s *Store) LoadContext(ctx context.Context, sessionID string) (*ContextSnapshot, error) {
	var payload string
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT payload FROM session_context WHERE session_id = ?`, sessionID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load context snapshot of %s: %w", sessionID, err)
	}
	var snap ContextSnapshot
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		return nil, fmt.Errorf("decode context snapshot of %s: %w", sessionID, err)
	}
	return &snap, nil
}

// BuildModelInput 组装模型下一轮的输入历史。
//
// 有压缩快照就以快照为基线，再补上快照之后产生的原始消息；没有则直接用全部原文。
// 这样既不会重复压缩，也不会漏掉压缩之后的新对话。
//
// BuildModelInput assembles the input history for the model's next turn.
//
// When a compressed snapshot exists it is used as the baseline, topped up with original
// messages produced after it; otherwise the full original history is used. This neither
// re-compresses nor loses the turns that happened after compression.
func (s *Store) BuildModelInput(ctx context.Context, sessionID string) ([]*schema.Message, error) {
	snap, err := s.LoadContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if snap == nil || len(snap.Messages) == 0 {
		return s.LoadHistory(ctx, sessionID)
	}

	tail, err := s.messagesAfter(ctx, sessionID, snap.SourceSeq)
	if err != nil {
		return nil, err
	}
	out := make([]*schema.Message, 0, len(snap.Messages)+len(tail))
	out = append(out, snap.Messages...)
	out = append(out, tail...)
	return out, nil
}

// messagesAfter 返回序号大于 seq 的原始消息。
// messagesAfter returns original messages with a sequence greater than seq.
func (s *Store) messagesAfter(ctx context.Context, sessionID string, seq int) ([]*schema.Message, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT role, content, tool_calls, tool_call_id, tool_name
		 FROM messages WHERE session_id = ? AND seq > ? ORDER BY seq ASC`, sessionID, seq)
	if err != nil {
		return nil, fmt.Errorf("load messages after seq %d of %s: %w", seq, sessionID, err)
	}
	defer rows.Close()

	out := []*schema.Message{}
	for rows.Next() {
		var role, content, rawToolCalls, toolCallID, toolName string
		if err := rows.Scan(&role, &content, &rawToolCalls, &toolCallID, &toolName); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		tcs, err := decodeToolCalls(rawToolCalls)
		if err != nil {
			return nil, err
		}
		out = append(out, &schema.Message{
			Role:       schema.RoleType(role),
			Content:    content,
			ToolCalls:  tcs,
			ToolCallID: toolCallID,
			ToolName:   toolName,
		})
	}
	return out, rows.Err()
}

// MaxSeq 返回会话当前的最大消息序号，没有消息时返回 -1。
// MaxSeq returns the highest message sequence of a conversation, or -1 when it has none.
func (s *Store) MaxSeq(ctx context.Context, sessionID string) (int, error) {
	var seq int
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), -1) FROM messages WHERE session_id = ?`, sessionID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("read max seq of %s: %w", sessionID, err)
	}
	return seq, nil
}
