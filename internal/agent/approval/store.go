package approval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"private/agent_basedon_eino/internal/agent/store"
)

// 确认状态。
// Approval statuses.
const (
	// StatusPending 表示还在等人决定。
	// StatusPending means a decision is still awaited.
	StatusPending = "pending"
	// StatusApproved 与 StatusDenied 是人工给出的两种结论。
	// StatusApproved and StatusDenied are the two human verdicts.
	StatusApproved = "approved"
	StatusDenied   = "denied"
	// StatusAbandoned 表示这次确认已经没有意义了——用户没理它，直接在同一会话里
	// 发了新消息。此时旧的 checkpoint 也会被删掉。
	//
	// StatusAbandoned means the confirmation became moot: the user ignored it and sent a new
	// message in the same conversation. The stale checkpoint is deleted along with it.
	StatusAbandoned = "abandoned"
)

// ErrNotFound 表示确认记录不存在。
// ErrNotFound signals a missing approval record.
var ErrNotFound = errors.New("approval not found")

// Record 是一条确认记录。
// Record is one approval record.
type Record struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	// CheckpointID 与 InterruptID 合起来是恢复执行的坐标：前者定位"哪一轮"，
	// 后者定位"那一轮里的哪个中断点"。
	//
	// CheckpointID and InterruptID together locate the resume point: the former says which
	// turn, the latter which interrupt within it.
	CheckpointID string `json:"-"`
	InterruptID  string `json:"-"`
	Command      string `json:"command"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	DecidedAt    int64  `json:"decided_at,omitempty"`
}

// Store 持久化确认记录。
// Store persists approval records.
type Store struct {
	db *store.DB
}

// NewStore 构造确认记录存储。
// NewStore builds the approval record store.
func NewStore(db *store.DB) *Store { return &Store{db: db} }

// Create 登记一条待确认命令。
// Create registers one command awaiting confirmation.
func (s *Store) Create(ctx context.Context, r Record) (Record, error) {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	r.Status = StatusPending
	r.CreatedAt = store.Now()
	_, err := s.db.Write().ExecContext(ctx, `
		INSERT INTO approvals (id, session_id, checkpoint_id, interrupt_id, command, status, reason, created_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, 0)`,
		r.ID, r.SessionID, r.CheckpointID, r.InterruptID, r.Command, r.Status, r.CreatedAt)
	if err != nil {
		return Record{}, fmt.Errorf("create approval: %w", err)
	}
	return r, nil
}

// Get 按 ID 取一条记录。
// Get fetches one record by ID.
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT id, session_id, checkpoint_id, interrupt_id, command, status, reason, created_at, decided_at
		FROM approvals WHERE id = ?`, id)
	r, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return r, err
}

// ListPending 返回一个会话里还在等待的确认。
//
// 页面每次加载会话都调它，待确认的卡片因此能在刷新、重开、甚至后端重启之后重现。
//
// ListPending returns the still-waiting confirmations of one conversation.
//
// The UI calls it on every conversation load, which is how a pending card reappears after a
// refresh, a reopen, or even a backend restart.
func (s *Store) ListPending(ctx context.Context, sessionID string) ([]Record, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT id, session_id, checkpoint_id, interrupt_id, command, status, reason, created_at, decided_at
		FROM approvals WHERE session_id = ? AND status = ? ORDER BY created_at`, sessionID, StatusPending)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()

	out := []Record{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Decide 写入人工结论。只有仍处于 pending 的记录会被改动，返回 false 表示它已被
// 别处处理过——两个标签页同时点，只有第一下算数。
//
// Decide records the human verdict. Only a still-pending record is updated; false means it was
// already handled elsewhere, so that two tabs clicking at once resolve it exactly once.
func (s *Store) Decide(ctx context.Context, id string, d Decision) (bool, error) {
	status := StatusDenied
	if d.Approved {
		status = StatusApproved
	}
	res, err := s.db.Write().ExecContext(ctx, `
		UPDATE approvals SET status = ?, reason = ?, decided_at = ?
		WHERE id = ? AND status = ?`, status, d.Reason, store.Now(), id, StatusPending)
	if err != nil {
		return false, fmt.Errorf("decide approval %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AbandonSession 作废一个会话里所有待确认的记录，返回被作废记录的 checkpoint ID。
//
// 调用方拿到这些 ID 去删对应的 checkpoint。用户没理那张卡片、直接发了新消息，
// 就是在说"这件事不用做了"，留着那一轮的断点既占地方又会让下一次恢复指向过期状态。
//
// AbandonSession voids every pending record of a conversation and returns their checkpoint IDs.
//
// The caller uses those IDs to delete the corresponding checkpoints. A user who ignores the card
// and sends a new message is saying "never mind"; keeping that turn's checkpoint wastes space
// and risks a later resume landing on stale state.
func (s *Store) AbandonSession(ctx context.Context, sessionID string) ([]string, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT checkpoint_id FROM approvals WHERE session_id = ? AND status = ?`,
		sessionID, StatusPending)
	if err != nil {
		return nil, fmt.Errorf("collect abandoned checkpoints: %w", err)
	}
	var cps []string
	for rows.Next() {
		var cp string
		if err := rows.Scan(&cp); err != nil {
			rows.Close()
			return nil, err
		}
		cps = append(cps, cp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cps) == 0 {
		return nil, nil
	}
	if _, err := s.db.Write().ExecContext(ctx,
		`UPDATE approvals SET status = ?, decided_at = ? WHERE session_id = ? AND status = ?`,
		StatusAbandoned, store.Now(), sessionID, StatusPending); err != nil {
		return nil, fmt.Errorf("abandon approvals: %w", err)
	}
	return cps, nil
}

type scanner interface{ Scan(...any) error }

func scan(sc scanner) (Record, error) {
	var r Record
	err := sc.Scan(&r.ID, &r.SessionID, &r.CheckpointID, &r.InterruptID,
		&r.Command, &r.Status, &r.Reason, &r.CreatedAt, &r.DecidedAt)
	return r, err
}
