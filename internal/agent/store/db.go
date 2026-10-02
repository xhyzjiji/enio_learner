// Package store 提供通用 Agent 的 SQLite 存储层：连接管理、表结构迁移，
// 以及 Eino 所需的 CheckPointStore 实现。
// Package store provides the SQLite storage layer for the general agent runtime: connection
// management, schema migration, and the CheckPointStore implementation required by Eino.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动，无需 CGO / pure-Go driver, no CGO required
)

const (
	// busyTimeoutMS 是 SQLite 在锁冲突时的等待上限。写操作已在连接层串行化，
	// 这个超时只用来兜住读连接偶发的等待。
	// busyTimeoutMS caps how long SQLite waits on a lock. Writes are already serialized at the
	// connection layer; this timeout only covers occasional waits on read connections.
	busyTimeoutMS = 5000

	// maxReadConns 是读连接池上限。WAL 模式下读不阻塞写，可以放开并发。
	// maxReadConns caps the read pool. Under WAL, readers do not block the writer, so
	// concurrency here is safe.
	maxReadConns = 8
)

// DB 把写连接与读连接分开管理。
//
// 写连接刻意限制为 1：SQLite 本身就是单写者模型，与其让多个连接去抢锁然后
// 撞上 "database is locked"，不如在连接池层面就排好队。对话、RAG 索引、定时任务
// 三方并发写时，这个约束是必需的——该问题在单人单会话的开发期完全不出现，
// 投入使用后才暴露。
//
// DB manages the write and read connections separately.
//
// The write pool is deliberately capped at 1: SQLite is a single-writer engine, and queueing
// at the pool layer is better than letting several connections contend for the lock and hit
// "database is locked". This matters once conversation, RAG indexing and scheduled tasks write
// concurrently — a failure mode that never appears during single-user development.
type DB struct {
	write *sql.DB
	read  *sql.DB
	path  string
}

// Open 打开（必要时创建）数据库文件并完成 PRAGMA 设置。
// Open opens (creating if needed) the database file and applies the required PRAGMAs.
func Open(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory %s: %w", dir, err)
		}
	}

	write, err := openPool(path, 1)
	if err != nil {
		return nil, fmt.Errorf("open write connection on %s: %w", path, err)
	}
	read, err := openPool(path, maxReadConns)
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("open read connection on %s: %w", path, err)
	}

	db := &DB{write: write, read: read, path: path}
	if err := db.verify(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// openPool 构造一条连接串并按给定并发上限建池。
// openPool builds a DSN and creates a pool with the given concurrency cap.
func openPool(path string, maxConns int) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
		path, busyTimeoutMS,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(0)
	return db, nil
}

// verify 确认 WAL 已经生效。若 journal_mode 不是 WAL，说明文件被其他进程以
// 非 WAL 模式占用，此时继续运行只会在并发写时才炸，不如立刻失败。
// verify confirms WAL is actually in effect. If journal_mode is not WAL, the file is held by
// another process in a different mode; continuing would only blow up later under concurrent
// writes, so fail immediately instead.
func (d *DB) verify(ctx context.Context) error {
	var mode string
	if err := d.read.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("read journal_mode of %s: %w", d.path, err)
	}
	if mode != "wal" {
		return fmt.Errorf("database %s is in %q journal mode, expected wal", d.path, mode)
	}
	return nil
}

// Write 返回写连接。所有 INSERT/UPDATE/DELETE/DDL 必须走这里。
// Write returns the write connection. All INSERT/UPDATE/DELETE/DDL must go through it.
func (d *DB) Write() *sql.DB { return d.write }

// Read 返回读连接池。只读查询走这里，以免占用唯一的写连接。
// Read returns the read pool. Read-only queries go here so they never occupy the single
// write connection.
func (d *DB) Read() *sql.DB { return d.read }

// Path 返回数据库文件路径，出错时用于在提示中指明具体文件。
// Path returns the database file path, used to name the exact file in error messages.
func (d *DB) Path() string { return d.path }

// Tx 在写连接上执行一个事务。因写池上限为 1，事务期间其他写操作自然排队。
// Tx runs a transaction on the write connection. Since the write pool is capped at 1, other
// writes naturally queue for the duration.
func (d *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
		}
		return err
	}
	return tx.Commit()
}

// Close 关闭两个连接池。
// Close shuts down both pools.
func (d *DB) Close() error {
	var first error
	if d.read != nil {
		if err := d.read.Close(); err != nil {
			first = err
		}
	}
	if d.write != nil {
		if err := d.write.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Now 返回统一的时间戳口径（毫秒）。全库所有时间列都用它，避免秒与毫秒混用。
// Now returns the single timestamp convention (milliseconds). Every time column uses it so
// seconds and milliseconds never get mixed.
func Now() int64 { return time.Now().UnixMilli() }
