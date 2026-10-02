package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CheckPointStore 是 adk.CheckPointStore 的 SQLite 实现。
//
// Eino 只定义了这个接口（Get/Set 两个方法存取 []byte），**没有提供任何实现**，
// 所以必须自备。它与 session 持久化解决的是完全不同的问题：
//   - CheckPointStore：单次 run 被中断后从断点继续，生命周期临时，用完即删
//   - session 持久化：跨进程重启的对话历史，生命周期长期
//
// 把两者混为一谈会导致要么断点数据无限膨胀，要么对话历史被当成临时数据清掉。
//
// CheckPointStore is the SQLite implementation of adk.CheckPointStore.
//
// Eino only declares the interface (Get/Set over []byte) and ships no implementation, so one
// must be supplied. It solves a different problem from session persistence:
//   - CheckPointStore: resume a single interrupted run; transient, deleted once consumed
//   - session persistence: conversation history across process restarts; long-lived
//
// Conflating the two leads either to unbounded checkpoint growth or to conversation history
// being purged as if it were scratch data.
type CheckPointStore struct {
	db *DB
}

// NewCheckPointStore 基于给定数据库构造断点存储。
// NewCheckPointStore builds a checkpoint store on the given database.
func NewCheckPointStore(db *DB) *CheckPointStore {
	return &CheckPointStore{db: db}
}

// Get 读取断点数据。第二个返回值表示是否存在，不存在不算错误。
// Get reads checkpoint data. The second return value reports existence; absence is not an error.
func (s *CheckPointStore) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	var data []byte
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT data FROM checkpoints WHERE id = ?`, checkPointID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get checkpoint %s: %w", checkPointID, err)
	}
	return data, true, nil
}

// Set 写入或覆盖断点数据。
// Set writes or overwrites checkpoint data.
func (s *CheckPointStore) Set(ctx context.Context, checkPointID string, checkPoint []byte) error {
	_, err := s.db.Write().ExecContext(ctx,
		`INSERT INTO checkpoints (id, data, created_at) VALUES (?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET data = excluded.data, created_at = excluded.created_at`,
		checkPointID, checkPoint, Now())
	if err != nil {
		return fmt.Errorf("set checkpoint %s: %w", checkPointID, err)
	}
	return nil
}

// Delete 实现 core.CheckPointDeleter。Eino 在断点被消费后会调用它；
// 不实现该接口则断点永不清理，数据库会随运行次数单调增长。
// Delete implements core.CheckPointDeleter. Eino calls it once a checkpoint is consumed;
// without it stale checkpoints are never reclaimed and the database grows monotonically.
func (s *CheckPointStore) Delete(ctx context.Context, checkPointID string) error {
	_, err := s.db.Write().ExecContext(ctx, `DELETE FROM checkpoints WHERE id = ?`, checkPointID)
	if err != nil {
		return fmt.Errorf("delete checkpoint %s: %w", checkPointID, err)
	}
	return nil
}

// PruneBefore 清理早于给定时间戳的断点，兜住进程异常退出遗留的孤儿记录。
// PruneBefore removes checkpoints older than the given timestamp, cleaning up orphans left
// behind by abnormal process exits.
func (s *CheckPointStore) PruneBefore(ctx context.Context, cutoffMillis int64) (int64, error) {
	res, err := s.db.Write().ExecContext(ctx,
		`DELETE FROM checkpoints WHERE created_at < ?`, cutoffMillis)
	if err != nil {
		return 0, fmt.Errorf("prune checkpoints: %w", err)
	}
	return res.RowsAffected()
}
