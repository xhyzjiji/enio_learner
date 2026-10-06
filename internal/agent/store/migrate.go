package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion 是当前期望的表结构版本。新增迁移时递增，并在 migrations 中补一条。
// schemaVersion is the schema version this build expects. Bump it when adding a migration and
// append the corresponding entry to migrations.
const schemaVersion = 2

// migration 是一次结构变更。version 为该迁移完成后的目标版本号。
// migration is a single schema change. version is the target version after it is applied.
type migration struct {
	version int
	stmts   []string
}

// migrations 保存 v1 之后的增量变更。v1 由 schema.sql 一次建全。
// migrations holds incremental changes after v1. v1 itself is created wholesale by schema.sql.
var migrations = []migration{
	{
		// v2：命令确认落库，支持跨进程恢复。
		// v2: persist command approvals so execution can resume across process restarts.
		version: 2,
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS approvals (
				id            TEXT    PRIMARY KEY,
				session_id    TEXT    NOT NULL,
				checkpoint_id TEXT    NOT NULL,
				interrupt_id  TEXT    NOT NULL,
				command       TEXT    NOT NULL,
				status        TEXT    NOT NULL DEFAULT 'pending',
				reason        TEXT    NOT NULL DEFAULT '',
				created_at    INTEGER NOT NULL,
				decided_at    INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX IF NOT EXISTS idx_approvals_session ON approvals (session_id, status, created_at)`,
		},
	},
}

// Migrate 建表并把结构推进到 schemaVersion。
//
// 失败时明确报出数据库路径与失败原因，绝不静默重建：数据库里是用户全部的对话历史、
// 记忆与配置，"修不好就重来一个"是最不能接受的处理方式。
//
// Migrate creates tables and advances the schema to schemaVersion.
//
// On failure it reports the database path and the reason explicitly, and never silently
// recreates the file: the database holds all of the user's conversation history, memories and
// configuration, so "if it's broken, start over" is the one behaviour that must not happen.
func Migrate(ctx context.Context, db *DB) error {
	current, err := readUserVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("read schema version of %s: %w", db.Path(), err)
	}

	if current == 0 {
		if err := applyBaseSchema(ctx, db); err != nil {
			return fmt.Errorf("create schema in %s: %w", db.Path(), err)
		}
		current = 1
		if err := writeUserVersion(ctx, db, current); err != nil {
			return fmt.Errorf("record schema version in %s: %w", db.Path(), err)
		}
	}

	if current > schemaVersion {
		return fmt.Errorf(
			"database %s has schema version %d, newer than the %d this build understands; "+
				"use a matching build instead of deleting the file",
			db.Path(), current, schemaVersion,
		)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("apply migration v%d to %s: %w", m.version, db.Path(), err)
		}
		current = m.version
	}
	return nil
}

// applyBaseSchema 执行 schema.sql。其中含 CREATE VIRTUAL TABLE，需要驱动支持 FTS5。
// applyBaseSchema runs schema.sql, which contains CREATE VIRTUAL TABLE and therefore requires
// FTS5 support in the driver.
func applyBaseSchema(ctx context.Context, db *DB) error {
	if _, err := db.Write().ExecContext(ctx, schemaSQL); err != nil {
		return err
	}
	return nil
}

func applyMigration(ctx context.Context, db *DB, m migration) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		for _, stmt := range m.stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("statement %q: %w", stmt, err)
			}
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version))
		return err
	})
}

func readUserVersion(ctx context.Context, db *DB) (int, error) {
	var v int
	err := db.Read().QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

func writeUserVersion(ctx context.Context, db *DB, v int) error {
	_, err := db.Write().ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v))
	return err
}
