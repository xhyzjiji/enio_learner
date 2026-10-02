// Package schedule 实现定时任务：登记、调度、受限执行与执行记录。
//
// 贯穿本包的一条原则是**数据库为唯一真相源**：任何增删改都先写库再同步内存中的
// cron 调度器。反过来做的话，进程被 kill -9 时内存里的调度状态直接蒸发，
// 而数据库里留下的是一份没人执行的任务清单——而且重启后看起来一切正常。
//
// Package schedule implements scheduled tasks: registration, scheduling, restricted execution
// and run records.
//
// One principle runs through it: THE DATABASE IS THE SINGLE SOURCE OF TRUTH. Every mutation
// writes to the database first and then syncs the in-memory cron scheduler. Done the other way
// round, a kill -9 evaporates the in-memory schedule while the database keeps a task list nobody
// executes — and everything looks fine after the restart.
package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"private/agent_basedon_eino/internal/agent/store"
)

// 执行状态。
// Run statuses.
const (
	RunRunning = "running"
	RunSuccess = "success"
	RunFailed  = "failed"
	// RunMissed 表示进程停机期间错过了该次执行。
	// RunMissed marks an execution missed while the process was down.
	RunMissed = "missed"
)

// maxRunsPerTask 是每个任务保留的执行记录条数。
// 不截断的话，一个每分钟执行的任务一年会留下五十多万条记录。
// maxRunsPerTask is how many run records each task keeps. Without truncation, a task running
// every minute leaves over half a million rows in a year.
const maxRunsPerTask = 50

// Task 是一个定时任务。
// Task is one scheduled task.
type Task struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	// KeepContext 为真时沿用 SessionID 指向的会话上下文，否则每次独立执行。
	// When KeepContext is true the task reuses the conversation named by SessionID; otherwise
	// each run is isolated.
	KeepContext bool     `json:"keep_context"`
	SessionID   string   `json:"session_id"`
	ToolScope   []string `json:"tool_scope"`
	LastRunAt   int64    `json:"last_run_at"`
	NextRunAt   int64    `json:"next_run_at"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// Run 是一次执行记录。
// Run is one execution record.
type Run struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
	Status     string `json:"status"`
	Result     string `json:"result"`
	Error      string `json:"error"`
}

// ErrNotFound 表示任务不存在。
// ErrNotFound signals a missing task.
var ErrNotFound = errors.New("scheduled task not found")

// Store 管理 scheduled_tasks 与 task_runs。
// Store manages scheduled_tasks and task_runs.
type Store struct {
	db *store.DB
}

// NewStore 构造存储层。
// NewStore builds the storage layer.
func NewStore(db *store.DB) *Store { return &Store{db: db} }

// List 返回全部任务。
// List returns every task.
func (s *Store) List(ctx context.Context) ([]*Task, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT id, name, cron, prompt, kind, enabled, keep_context, session_id, tool_scope,
		       last_run_at, next_run_at, created_at, updated_at
		FROM scheduled_tasks ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get 返回单个任务。
// Get returns one task.
func (s *Store) Get(ctx context.Context, id string) (*Task, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT id, name, cron, prompt, kind, enabled, keep_context, session_id, tool_scope,
		       last_run_at, next_run_at, created_at, updated_at
		FROM scheduled_tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func scanTask(sc interface{ Scan(...any) error }) (*Task, error) {
	var (
		t         Task
		scopeJSON string
	)
	if err := sc.Scan(&t.ID, &t.Name, &t.Cron, &t.Prompt, &t.Kind, &t.Enabled, &t.KeepContext,
		&t.SessionID, &scopeJSON, &t.LastRunAt, &t.NextRunAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(scopeJSON), &t.ToolScope); err != nil {
		return nil, fmt.Errorf("任务 %s 的 tool_scope 不是合法 JSON / tool_scope of task %s is not valid JSON: %w",
			t.Name, t.Name, err)
	}
	return &t, nil
}

// Save 新增或覆盖一个任务。
// Save inserts or replaces a task.
func (s *Store) Save(ctx context.Context, t *Task) (*Task, error) {
	if strings.TrimSpace(t.Name) == "" {
		return nil, errors.New("任务名不能为空 / task name must not be empty")
	}
	if strings.TrimSpace(t.Prompt) == "" {
		return nil, errors.New("任务内容不能为空 / task prompt must not be empty")
	}
	now := store.Now()
	t.UpdatedAt = now
	if t.ID == "" {
		t.ID = uuid.NewString()
		t.CreatedAt = now
	}
	if t.Kind == "" {
		t.Kind = "prompt"
	}
	scope, _ := json.Marshal(orEmpty(t.ToolScope))
	_, err := s.db.Write().ExecContext(ctx, `
		INSERT INTO scheduled_tasks (id, name, cron, prompt, kind, enabled, keep_context,
		       session_id, tool_scope, last_run_at, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, cron = excluded.cron, prompt = excluded.prompt,
			kind = excluded.kind, enabled = excluded.enabled, keep_context = excluded.keep_context,
			session_id = excluded.session_id, tool_scope = excluded.tool_scope,
			next_run_at = excluded.next_run_at, updated_at = excluded.updated_at`,
		t.ID, t.Name, t.Cron, t.Prompt, t.Kind, t.Enabled, t.KeepContext,
		t.SessionID, string(scope), t.LastRunAt, t.NextRunAt, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("save task %s: %w", t.Name, err)
	}
	return t, nil
}

// Delete 删除一个任务。
// Delete removes a task.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.Write().ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Count 返回任务总数，供熔断判断。
// Count returns the task total, used by the circuit breaker.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_tasks`).Scan(&n)
	return n, err
}

// MarkRunTimes 更新任务的上次与下次执行时间。
// MarkRunTimes updates a task's last and next run times.
func (s *Store) MarkRunTimes(ctx context.Context, id string, last, next int64) error {
	_, err := s.db.Write().ExecContext(ctx,
		`UPDATE scheduled_tasks SET last_run_at = ?, next_run_at = ? WHERE id = ?`, last, next, id)
	return err
}

// StartRun 登记一次执行开始。
// StartRun records the start of one execution.
func (s *Store) StartRun(ctx context.Context, taskID string) (*Run, error) {
	r := &Run{ID: uuid.NewString(), TaskID: taskID, StartedAt: store.Now(), Status: RunRunning}
	_, err := s.db.Write().ExecContext(ctx,
		`INSERT INTO task_runs (id, task_id, started_at, status) VALUES (?, ?, ?, ?)`,
		r.ID, r.TaskID, r.StartedAt, r.Status)
	if err != nil {
		return nil, fmt.Errorf("start run of task %s: %w", taskID, err)
	}
	return r, nil
}

// FinishRun 登记一次执行结束，并顺手截断超量的历史记录。
// FinishRun records the end of one execution and trims excess history along the way.
func (s *Store) FinishRun(ctx context.Context, r *Run) error {
	_, err := s.db.Write().ExecContext(ctx, `
		UPDATE task_runs SET finished_at = ?, status = ?, result = ?, error = ? WHERE id = ?`,
		store.Now(), r.Status, r.Result, r.Error, r.ID)
	if err != nil {
		return fmt.Errorf("finish run %s: %w", r.ID, err)
	}
	return s.trimRuns(ctx, r.TaskID)
}

// RecordMissed 记录一次因停机而错过的执行。
//
// 记下来而不是无视：用户看到"这个任务昨天该跑但没跑"，才能明白结果为什么缺了一天，
// 而不是怀疑任务配错了。
//
// RecordMissed logs an execution missed because the process was down.
//
// Logging beats ignoring: seeing "this task should have run yesterday but did not" is what lets
// the user understand why a day's result is missing, instead of suspecting a misconfiguration.
func (s *Store) RecordMissed(ctx context.Context, taskID string, at int64) error {
	_, err := s.db.Write().ExecContext(ctx, `
		INSERT INTO task_runs (id, task_id, started_at, finished_at, status, result, error)
		VALUES (?, ?, ?, ?, ?, '', ?)`,
		uuid.NewString(), taskID, at, at, RunMissed,
		"进程未运行，该次执行被跳过 / the process was not running, so this execution was skipped")
	if err != nil {
		return fmt.Errorf("record missed run of %s: %w", taskID, err)
	}
	return nil
}

// ListRuns 返回一个任务的执行记录。
// ListRuns returns the run records of one task.
func (s *Store) ListRuns(ctx context.Context, taskID string) ([]*Run, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT id, task_id, started_at, finished_at, status, result, error
		FROM task_runs WHERE task_id = ? ORDER BY started_at DESC LIMIT ?`, taskID, maxRunsPerTask)
	if err != nil {
		return nil, fmt.Errorf("list runs of %s: %w", taskID, err)
	}
	defer rows.Close()

	var out []*Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.TaskID, &r.StartedAt, &r.FinishedAt,
			&r.Status, &r.Result, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *Store) trimRuns(ctx context.Context, taskID string) error {
	_, err := s.db.Write().ExecContext(ctx, `
		DELETE FROM task_runs WHERE task_id = ? AND id NOT IN (
			SELECT id FROM task_runs WHERE task_id = ? ORDER BY started_at DESC LIMIT ?)`,
		taskID, taskID, maxRunsPerTask)
	return err
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
