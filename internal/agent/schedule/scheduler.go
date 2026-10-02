package schedule

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser 解析 cron 表达式。
//
// 带 Descriptor 支持，因此 "@daily"、"@every 30m" 这类写法直接可用——模型在对话里
// 说"每天早上跑一次"时，让它产出 "@daily" 远比让它拼一个五段式表达式可靠。
//
// cronParser parses cron expressions.
//
// Descriptors are enabled, so "@daily" and "@every 30m" work out of the box. When the model is
// told "run this every morning", having it emit "@daily" is far more reliable than having it
// assemble a five-field expression.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Executor 执行一个任务，由 runner.go 实现。
// Executor runs one task; implemented in runner.go.
type Executor interface {
	Execute(ctx context.Context, t *Task) (string, error)
}

// Scheduler 把数据库里的任务同步到内存中的 cron 调度器。
// Scheduler syncs tasks from the database into the in-memory cron scheduler.
type Scheduler struct {
	store    *Store
	executor Executor
	logger   *slog.Logger

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[string]cron.EntryID
	started bool
}

// NewScheduler 构造调度器。
// NewScheduler builds the scheduler.
func NewScheduler(s *Store, e Executor, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		store:    s,
		executor: e,
		logger:   logger,
		cron:     cron.New(cron.WithParser(cronParser)),
		entries:  make(map[string]cron.EntryID),
	}
}

// SetExecutor 在构造之后注入执行器。
//
// 存在这个方法是因为依赖成环：调度器要执行任务就得有 Engine，而 Engine 要挂
// schedule_task 工具就得先有调度器。二选一地在构造后补上一环，比让两者互相持有
// 对方的构造函数清楚。调用它必须早于 Start。
//
// SetExecutor injects the executor after construction.
//
// It exists because the dependencies form a cycle: the scheduler needs the Engine to execute
// tasks, while the Engine needs the scheduler to mount the schedule_task tool. Closing one link
// after construction is clearer than having the two hold each other's constructors. It must be
// called before Start.
func (s *Scheduler) SetExecutor(e Executor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executor = e
}

func (s *Scheduler) currentExecutor() Executor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executor
}

// ValidateCron 校验一个 cron 表达式，并返回它的下一次执行时间。
// ValidateCron validates a cron expression and returns its next fire time.
func ValidateCron(expr string, minInterval time.Duration) (time.Time, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf(
			"cron 表达式 %q 无法解析（支持五段式与 @daily、@every 30m 这类写法）/ "+
				"cannot parse cron expression %q (five-field form and descriptors such as @daily "+
				"or @every 30m are supported): %w", expr, expr, err)
	}
	now := time.Now()
	first := sched.Next(now)
	second := sched.Next(first)
	if minInterval > 0 && second.Sub(first) < minInterval {
		return time.Time{}, fmt.Errorf(
			"执行间隔 %s 小于允许的最小间隔 %s / the interval %s is below the minimum of %s",
			second.Sub(first), minInterval, second.Sub(first), minInterval)
	}
	return first, nil
}

// Start 从数据库全量恢复任务并启动调度。
//
// 恢复时会检查每个任务的 next_run_at：如果它已经过去，说明进程停机期间错过了
// 一次执行。此处按 skip 策略处理——只记一条 missed 记录，不补跑。
//
// 不补跑是刻意的：停机一周后启动，补跑意味着一个每小时的任务会瞬间连发一百多次。
// 而定时任务的语义通常是"在那个时间点做那件事"，过了时间点再做往往已无意义。
//
// Start rebuilds every task from the database and begins scheduling.
//
// During recovery each task's next_run_at is checked: a time in the past means an execution was
// missed while the process was down. The policy here is skip — a missed record is written and
// nothing is re-run.
//
// Not catching up is deliberate: after a week of downtime, catching up would fire an hourly task
// over a hundred times in an instant. The meaning of a scheduled task is usually "do that thing
// at that moment", and doing it long after the moment has passed is generally pointless.
func (s *Scheduler) Start(ctx context.Context) error {
	tasks, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, t := range tasks {
		if t.NextRunAt > 0 && t.NextRunAt < now {
			if err := s.store.RecordMissed(ctx, t.ID, t.NextRunAt); err != nil {
				s.logger.Warn("record missed run failed", "task", t.Name, "err", err)
			}
		}
		if !t.Enabled {
			continue
		}
		if err := s.Sync(ctx, t); err != nil {
			// 一个任务的表达式坏掉不应该让其余任务全部不调度。
			// A single broken expression must not leave every other task unscheduled.
			s.logger.Warn("schedule task failed, it stays inactive",
				"task", t.Name, "cron", t.Cron, "err", err)
		}
	}

	s.mu.Lock()
	if !s.started {
		s.cron.Start()
		s.started = true
	}
	s.mu.Unlock()
	return nil
}

// Sync 把一个任务的调度状态同步到内存。调用前该任务必须已经写入数据库。
// Sync reflects one task's schedule in memory. The task must already be in the database.
func (s *Scheduler) Sync(ctx context.Context, t *Task) error {
	s.Unschedule(t.ID)
	if !t.Enabled {
		return nil
	}

	next, err := ValidateCron(t.Cron, 0)
	if err != nil {
		return err
	}

	id, err := s.cron.AddFunc(t.Cron, func() { s.fire(t.ID) })
	if err != nil {
		return fmt.Errorf("schedule task %s: %w", t.Name, err)
	}

	s.mu.Lock()
	s.entries[t.ID] = id
	s.mu.Unlock()

	return s.store.MarkRunTimes(ctx, t.ID, t.LastRunAt, next.UnixMilli())
}

// Unschedule 从调度器中移除一个任务。
// Unschedule removes a task from the scheduler.
func (s *Scheduler) Unschedule(taskID string) {
	s.mu.Lock()
	id, ok := s.entries[taskID]
	delete(s.entries, taskID)
	s.mu.Unlock()
	if ok {
		s.cron.Remove(id)
	}
}

// fire 执行一次任务。
//
// 每次执行都重新从数据库读任务定义，而不是用闭包捕获的那一份：用户可能刚在页面上
// 改了 prompt 或工具范围，用旧定义跑就是拿着过期配置在执行。
//
// fire runs a task once.
//
// The definition is re-read from the database on every execution rather than taken from the
// closure: the user may have just changed the prompt or tool scope in the UI, and running the
// stale copy would execute an out-of-date configuration.
func (s *Scheduler) fire(taskID string) {
	// 独立 context：调度是后台行为，不挂在任何请求上。
	// An independent context: scheduling is background work with no request behind it.
	ctx := context.Background()

	t, err := s.store.Get(ctx, taskID)
	if err != nil {
		s.logger.Warn("scheduled task disappeared, unscheduling it", "task", taskID, "err", err)
		s.Unschedule(taskID)
		return
	}
	if !t.Enabled {
		s.Unschedule(taskID)
		return
	}
	s.RunNow(ctx, t)
}

// RunNow 立即执行一个任务并记录结果。页面上的"立即运行"也走这里。
// RunNow executes a task immediately and records the outcome. The UI's "run now" uses it too.
func (s *Scheduler) RunNow(ctx context.Context, t *Task) *Run {
	run, err := s.store.StartRun(ctx, t.ID)
	if err != nil {
		s.logger.Error("start run failed", "task", t.Name, "err", err)
		return nil
	}

	exec := s.currentExecutor()
	if exec == nil {
		run.Status = RunFailed
		run.Error = "执行器未初始化 / the executor is not initialised"
		_ = s.store.FinishRun(ctx, run)
		return run
	}

	result, execErr := exec.Execute(ctx, t)
	if execErr != nil {
		run.Status, run.Error = RunFailed, execErr.Error()
	} else {
		run.Status, run.Result = RunSuccess, result
	}
	if err := s.store.FinishRun(ctx, run); err != nil {
		s.logger.Error("finish run failed", "task", t.Name, "err", err)
	}

	next := int64(0)
	if at, err := ValidateCron(t.Cron, 0); err == nil {
		next = at.UnixMilli()
	}
	if err := s.store.MarkRunTimes(ctx, t.ID, run.StartedAt, next); err != nil {
		s.logger.Warn("update run times failed", "task", t.Name, "err", err)
	}
	return run
}

// Stop 停止调度器，等待正在执行的任务结束。
// Stop halts the scheduler, waiting for in-flight executions to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	started := s.started
	s.started = false
	s.mu.Unlock()
	if started {
		<-s.cron.Stop().Done()
	}
}
