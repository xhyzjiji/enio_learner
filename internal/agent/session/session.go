// Package session 管理会话与消息的持久化。
//
// 一段对话在库里有两个视角，这是本包最重要的设计：
//   - messages 表存**原始完整消息**，供页面展示、导出与审计
//   - session_context 表存**压缩后的模型输入快照**，用于恢复对话时喂给模型
//
// 只存一份必然二选一地牺牲：只存压缩后，页面就看不到原始对话；只存原始，则每次恢复
// 都要重新压缩，既浪费又会因为两次摘要结果不同而让上下文漂移。
//
// Package session persists conversations and their messages.
//
// A conversation has two views in the database, which is the central design of this package:
//   - the messages table holds the ORIGINAL, complete messages for UI, export and audit
//   - the session_context table holds the COMPRESSED model-input snapshot used when resuming
//
// Storing only one of them forces a sacrifice either way: keep only the compressed form and the
// UI can no longer show the real conversation; keep only the original and every resume must
// re-compress, which wastes calls and drifts the context because two summaries never match.
package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"private/agent_basedon_eino/internal/agent/store"
)

// Session 是会话元信息。
// Session is conversation metadata.
type Session struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	Archived     bool   `json:"archived"`
	TokenUsage   int64  `json:"token_usage"`
	CompactCount int    `json:"compact_count"`
}

// Store 是会话与消息的数据访问层。
// Store is the data access layer for conversations and messages.
type Store struct {
	db *store.DB
}

// NewStore 构造会话存储。
// NewStore builds the session store.
func NewStore(db *store.DB) *Store { return &Store{db: db} }

// ErrNotFound 表示会话不存在。
// ErrNotFound signals that the conversation does not exist.
var ErrNotFound = errors.New("session not found")

// Create 新建一个会话。标题留空，由 title.go 异步补齐。
// Create starts a new conversation. The title is left empty and filled asynchronously by
// title.go.
func (s *Store) Create(ctx context.Context, title string) (*Session, error) {
	now := store.Now()
	sess := &Session{
		ID:        uuid.NewString(),
		Title:     strings.TrimSpace(title),
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err := s.db.Write().ExecContext(ctx,
		`INSERT INTO sessions (id, title, created_at, updated_at, archived, token_usage, compact_count)
		 VALUES (?, ?, ?, ?, 0, 0, 0)`,
		sess.ID, sess.Title, sess.CreatedAt, sess.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// Get 按 ID 读取会话。
// Get loads a conversation by ID.
func (s *Store) Get(ctx context.Context, id string) (*Session, error) {
	var sess Session
	var archived int
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT id, title, created_at, updated_at, archived, token_usage, compact_count
		 FROM sessions WHERE id = ?`, id).
		Scan(&sess.ID, &sess.Title, &sess.CreatedAt, &sess.UpdatedAt,
			&archived, &sess.TokenUsage, &sess.CompactCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", id, err)
	}
	sess.Archived = archived != 0
	return &sess, nil
}

// List 按最近更新倒序返回会话。includeArchived 为假时跳过已归档会话。
// List returns conversations ordered by most recent update. Archived ones are skipped unless
// includeArchived is true.
func (s *Store) List(ctx context.Context, includeArchived bool) ([]*Session, error) {
	query := `SELECT id, title, created_at, updated_at, archived, token_usage, compact_count
	          FROM sessions`
	if !includeArchived {
		query += ` WHERE archived = 0`
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := s.db.Read().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	out := []*Session{}
	for rows.Next() {
		var sess Session
		var archived int
		if err := rows.Scan(&sess.ID, &sess.Title, &sess.CreatedAt, &sess.UpdatedAt,
			&archived, &sess.TokenUsage, &sess.CompactCount); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sess.Archived = archived != 0
		out = append(out, &sess)
	}
	return out, rows.Err()
}

// Rename 修改会话标题。
// Rename changes the conversation title.
func (s *Store) Rename(ctx context.Context, id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("标题不能为空 / title must not be empty")
	}
	return s.touchExec(ctx, id,
		`UPDATE sessions SET title = ?, updated_at = ? WHERE id = ?`, title, store.Now(), id)
}

// SetArchived 归档或取消归档。归档是软删除：会话从默认列表消失但数据仍在，
// 对"我上周那段对话呢"这种场景比真删除友好得多。
// SetArchived archives or unarchives a conversation. Archiving is a soft delete: the
// conversation leaves the default list while its data remains, which handles "where did last
// week's conversation go" far better than a hard delete.
func (s *Store) SetArchived(ctx context.Context, id string, archived bool) error {
	v := 0
	if archived {
		v = 1
	}
	return s.touchExec(ctx, id,
		`UPDATE sessions SET archived = ?, updated_at = ? WHERE id = ?`, v, store.Now(), id)
}

// Delete 真删除会话。messages / session_context 靠外键级联删除。
// Delete hard-deletes a conversation. messages and session_context cascade via foreign keys.
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.touchExec(ctx, id, `DELETE FROM sessions WHERE id = ?`, id)
}

// Touch 更新 updated_at，使会话在列表中上浮。
// Touch bumps updated_at so the conversation floats to the top of the list.
func (s *Store) Touch(ctx context.Context, id string) error {
	return s.touchExec(ctx, id,
		`UPDATE sessions SET updated_at = ? WHERE id = ?`, store.Now(), id)
}

// AddTokenUsage 累加 token 用量。
// AddTokenUsage accumulates token usage.
func (s *Store) AddTokenUsage(ctx context.Context, id string, delta int64) error {
	return s.touchExec(ctx, id,
		`UPDATE sessions SET token_usage = token_usage + ?, updated_at = ? WHERE id = ?`,
		delta, store.Now(), id)
}

// IncCompactCount 累加压缩次数，供页面在压缩多次后提示用户新开会话。
// IncCompactCount bumps the compaction counter so the UI can suggest a fresh conversation
// after repeated compaction.
func (s *Store) IncCompactCount(ctx context.Context, id string) (int, error) {
	if err := s.touchExec(ctx, id,
		`UPDATE sessions SET compact_count = compact_count + 1, updated_at = ? WHERE id = ?`,
		store.Now(), id); err != nil {
		return 0, err
	}
	var count int
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT compact_count FROM sessions WHERE id = ?`, id).Scan(&count)
	return count, err
}

// touchExec 执行一条影响单个会话的写语句，并把"没有影响任何行"翻译为 ErrNotFound。
// 不这么做的话，对一个不存在的会话改名会静默成功，页面会以为改生效了。
// touchExec runs a write statement scoped to one conversation and translates "no rows affected"
// into ErrNotFound. Without this, renaming a non-existent conversation succeeds silently and
// the UI believes the change took effect.
func (s *Store) touchExec(ctx context.Context, id, query string, args ...any) error {
	res, err := s.db.Write().ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update session %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update session %s: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeOldCheckpoints 清理超过给定时长的断点，在启动时调用一次即可。
// PurgeOldCheckpoints removes checkpoints older than the given age; calling it once at startup
// is enough.
func PurgeOldCheckpoints(ctx context.Context, db *store.DB, age time.Duration) error {
	cutoff := time.Now().Add(-age).UnixMilli()
	_, err := db.Write().ExecContext(ctx, `DELETE FROM checkpoints WHERE created_at < ?`, cutoff)
	if err != nil {
		return fmt.Errorf("purge checkpoints: %w", err)
	}
	return nil
}
