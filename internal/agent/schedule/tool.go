package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"private/agent_basedon_eino/internal/agent/config"
	"private/agent_basedon_eino/internal/agent/kernel"
)

// ScheduleToolName 是模型登记定时任务用的工具名。
// ScheduleToolName is the tool the model uses to register scheduled tasks.
const ScheduleToolName = "schedule_task"

// ListTasksToolName 让模型查看已有任务，避免重复登记。
// ListTasksToolName lets the model inspect existing tasks to avoid duplicate registration.
const ListTasksToolName = "list_scheduled_tasks"

// NewScheduleTool 构造登记工具。
//
// 两道熔断写在这里而不是只写在前端：这个工具是模型自己调的，前端的任何限制对它
// 都不存在。
//
//   - 任务总数上限，挡住"任务 A 每次执行都登记一个新任务"的自我繁殖；
//   - 最小执行间隔，挡住模型把"经常检查一下"翻译成 "* * * * *"。
//
// NewScheduleTool builds the registration tool.
//
// Both circuit breakers live here rather than only in the frontend: the model calls this tool
// itself, so no frontend restriction applies to it.
//
//   - A task-count cap stops the self-replication where task A registers a new task on each run.
//   - A minimum interval stops the model from translating "check on this often" into "* * * * *".
func NewScheduleTool(store *Store, sched *Scheduler, rt config.Runtime, sessionID string) tool.BaseTool {
	info := &schema.ToolInfo{
		Name: ScheduleToolName,
		Desc: "登记一个定时任务，到点后自动执行给定的指令。适用于用户说「每天…」「每隔…」这类要求。" +
			"Register a scheduled task that automatically runs the given instruction on a cron " +
			"schedule. Use it when the user asks for something to happen every day, every hour, etc.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {
				Type:     schema.String,
				Desc:     "任务的简短名称 / a short name for the task",
				Required: true,
			},
			"cron": {
				Type: schema.String,
				Desc: "cron 表达式，支持五段式（分 时 日 月 周）与 @daily、@hourly、@every 30m 等描述符。" +
					"A cron expression; the five-field form (minute hour dom month dow) and " +
					"descriptors such as @daily, @hourly and @every 30m are supported.",
				Required: true,
			},
			"prompt": {
				Type: schema.String,
				Desc: "到点后要执行的完整指令。它会作为一条独立的用户消息发给自己，所以要写成自包含的、" +
					"不依赖当前对话上下文的形式。The full instruction to execute. It is sent as a " +
					"standalone user message, so write it self-contained without relying on the " +
					"current conversation.",
				Required: true,
			},
			"keep_context": {
				Type: schema.Boolean,
				Desc: "是否让每次执行共享同一个会话上下文。需要「接着上次的进度继续」时才设为 true。" +
					"Whether every execution shares one conversation. Set true only when the task " +
					"should continue from where the previous run left off.",
			},
			"tool_scope": {
				Type:     schema.Array,
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
				Desc: "允许该任务使用的工具名列表，留空表示使用除自由执行外的全部工具。" +
					"Names of the tools this task may use; empty means every tool except free-form " +
					"execution.",
			},
		}),
	}

	handler := func(ctx context.Context, args map[string]any) (string, error) {
		name, _ := args["name"].(string)
		cronExpr, _ := args["cron"].(string)
		prompt, _ := args["prompt"].(string)
		keepContext, _ := args["keep_context"].(bool)

		count, err := store.Count(ctx)
		if err != nil {
			return "", err
		}
		if count >= rt.MaxScheduledTasks {
			return fmt.Sprintf(
				"登记失败：定时任务数量已达上限 %d，请先让用户删除一些不再需要的任务。/ "+
					"Registration refused: the number of scheduled tasks has reached the limit of %d; "+
					"ask the user to remove obsolete tasks first.",
				rt.MaxScheduledTasks, rt.MaxScheduledTasks), nil
		}

		minInterval := time.Duration(rt.MinScheduleIntervalSec) * time.Second
		next, err := ValidateCron(cronExpr, minInterval)
		if err != nil {
			// 返回错误文本而非 error：让模型看到原因后改表达式重试，比让整轮对话失败有用。
			// Returning text rather than an error lets the model read the reason, fix the
			// expression and retry, which beats failing the whole turn.
			return fmt.Sprintf("登记失败 / registration refused: %v", err), nil
		}

		t := &Task{
			Name:        name,
			Cron:        cronExpr,
			Prompt:      prompt,
			Kind:        "prompt",
			Enabled:     true,
			KeepContext: keepContext,
			ToolScope:   toStrings(args["tool_scope"]),
			NextRunAt:   next.UnixMilli(),
		}
		if keepContext {
			t.SessionID = sessionID
		}

		saved, err := store.Save(ctx, t)
		if err != nil {
			return "", err
		}
		if err := sched.Sync(ctx, saved); err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"已登记定时任务「%s」，下次执行时间 %s。/ Scheduled task %q registered; next run at %s.",
			saved.Name, next.Format("2006-01-02 15:04:05"),
			saved.Name, next.Format("2006-01-02 15:04:05")), nil
	}

	return utils.NewTool[map[string]any, string](info, handler)
}

// NewListTasksTool 构造查询工具。
// NewListTasksTool builds the listing tool.
func NewListTasksTool(store *Store) tool.BaseTool {
	info := &schema.ToolInfo{
		Name: ListTasksToolName,
		Desc: "列出当前已登记的全部定时任务。修改或删除任务前先用它确认任务是否存在。" +
			"List every registered scheduled task. Use it to confirm a task exists before " +
			"changing or removing it.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}

	handler := func(ctx context.Context, _ map[string]any) (string, error) {
		tasks, err := store.List(ctx)
		if err != nil {
			return "", err
		}
		if len(tasks) == 0 {
			return "当前没有定时任务。/ There are no scheduled tasks.", nil
		}
		b, err := json.Marshal(tasks)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}

	return utils.NewTool[map[string]any, string](info, handler)
}

// Augmenter 把定时任务工具挂到每一轮对话上。
// Augmenter mounts the scheduling tools on every turn.
func Augmenter(store *Store, sched *Scheduler) kernel.Augmenter {
	return &scheduleAugmenter{store: store, sched: sched}
}

type scheduleAugmenter struct {
	store *Store
	sched *Scheduler
}

func (a *scheduleAugmenter) Name() string { return "schedule" }

func (a *scheduleAugmenter) Augment(_ context.Context, snap *kernel.Snapshot) error {
	snap.Tools = append(snap.Tools,
		NewScheduleTool(a.store, a.sched, snap.Runtime, snap.SessionID),
		NewListTasksTool(a.store),
	)
	return nil
}

func toStrings(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
