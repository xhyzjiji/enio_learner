// Package memory 实现跨会话的长期记忆。
//
// 核心设计是 **key 作主键、同 key 覆盖**。这不是图省事：如果按条追加，模型会把
// 同一件事在不同对话里反复记下来——"用户喜欢简洁的回答"、"用户不喜欢长篇大论"、
// "回答要短" 会变成三条独立记忆。它们不冲突时只是冗余，一旦用户改了主意，
// 新旧记忆就同时存在且互相矛盾，而注入时无从判断该信哪条。
//
// Package memory implements long-term memory across conversations.
//
// The central design is that the KEY IS THE PRIMARY KEY and writing the same key overwrites.
// This is not laziness: with append-only records the model re-records the same fact across
// conversations — "the user likes concise answers", "the user dislikes long explanations" and
// "keep replies short" become three separate memories. While consistent they are merely
// redundant, but the moment the user changes their mind the old and new coexist and contradict
// each other, with no way to tell which one to trust at injection time.
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"private/agent_basedon_eino/internal/agent/store"
)

// Memory 是一条长期记忆。
// Memory is one long-term memory.
type Memory struct {
	Key       string `json:"key"`
	Content   string `json:"content"`
	Category  string `json:"category"`
	SourceSID string `json:"source_sid"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// ErrNotFound 表示记忆不存在。
// ErrNotFound signals a missing memory.
var ErrNotFound = errors.New("memory not found")

// Store 管理 memories 表。
// Store manages the memories table.
type Store struct {
	db *store.DB
}

// NewStore 构造记忆存储。
// NewStore builds the memory store.
func NewStore(db *store.DB) *Store { return &Store{db: db} }

// Put 写入一条记忆，同 key 覆盖内容但保留首次记录时间。
//
// 保留 created_at 是有意的：它记录的是"这件事第一次被知道"的时间，而 updated_at
// 记录"最后一次确认"。页面上按这两个时间能看出哪些偏好是长期稳定的，
// 哪些是刚改的。覆盖时一并重置 created_at 就把这个信息抹掉了。
//
// Put writes one memory, overwriting the content on an existing key while preserving the
// original creation time.
//
// Preserving created_at is deliberate: it records when the fact was first learned, while
// updated_at records when it was last confirmed. Together they show in the UI which preferences
// have been stable for a long time and which just changed. Resetting created_at on overwrite
// would erase that.
func (s *Store) Put(ctx context.Context, m Memory) (Memory, error) {
	m.Key = strings.TrimSpace(m.Key)
	m.Content = strings.TrimSpace(m.Content)
	if m.Key == "" {
		return m, errors.New("记忆的 key 不能为空 / memory key must not be empty")
	}
	if m.Content == "" {
		return m, errors.New("记忆内容不能为空 / memory content must not be empty")
	}

	now := store.Now()
	m.UpdatedAt = now
	if m.CreatedAt == 0 {
		m.CreatedAt = now
	}
	_, err := s.db.Write().ExecContext(ctx, `
		INSERT INTO memories (key, content, category, source_sid, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			content = excluded.content, category = excluded.category,
			source_sid = excluded.source_sid, updated_at = excluded.updated_at`,
		m.Key, m.Content, m.Category, m.SourceSID, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return m, fmt.Errorf("put memory %s: %w", m.Key, err)
	}
	return s.Get(ctx, m.Key)
}

// Get 返回一条记忆。
// Get returns one memory.
func (s *Store) Get(ctx context.Context, key string) (Memory, error) {
	row := s.db.Read().QueryRowContext(ctx,
		`SELECT key, content, category, source_sid, created_at, updated_at FROM memories WHERE key = ?`, key)
	var m Memory
	err := row.Scan(&m.Key, &m.Content, &m.Category, &m.SourceSID, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// List 返回全部记忆，按最近更新排序。
// List returns every memory, most recently updated first.
func (s *Store) List(ctx context.Context) ([]Memory, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT key, content, category, source_sid, created_at, updated_at
		FROM memories ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.Key, &m.Content, &m.Category, &m.SourceSID,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Delete 删除一条记忆。
// Delete removes one memory.
func (s *Store) Delete(ctx context.Context, key string) error {
	res, err := s.db.Write().ExecContext(ctx, `DELETE FROM memories WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete memory %s: %w", key, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Count 返回记忆总数，供分档注入判断走哪条路径。
// Count returns the total, which decides which injection strategy applies.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&n)
	return n, err
}
