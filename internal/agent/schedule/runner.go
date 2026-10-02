package schedule

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"private/agent_basedon_eino/internal/agent/kernel"
	"private/agent_basedon_eino/internal/agent/session"
	"private/agent_basedon_eino/internal/agent/tools"
)

// taskTimeout 限制单次任务执行时长。
// 没有人守在屏幕前，一个跑飞的任务会一直占着模型配额直到被发现。
// taskTimeout bounds one execution. Nobody is watching the screen, and a runaway task would keep
// consuming the model quota until someone notices.
const taskTimeout = 10 * time.Minute

// maxResultChars 限制写入执行记录的结果长度。
// maxResultChars caps the result length stored in a run record.
const maxResultChars = 8000

// Runner 在受限工具集下执行定时任务。
// Runner executes scheduled tasks under a restricted tool set.
type Runner struct {
	engine   *kernel.Engine
	sessions *session.Store
	registry *tools.Registry
}

// NewRunner 构造执行器。
// NewRunner builds the executor.
func NewRunner(e *kernel.Engine, s *session.Store, r *tools.Registry) *Runner {
	return &Runner{engine: e, sessions: s, registry: r}
}

// Execute 执行一个任务。
//
// 两条硬约束：
//
//  1. **强制关闭 execute 自由执行工具**，无论全局配置是否打开。理由是定时任务
//     跑在无人值守的时刻，出了事没有人能当场叫停；而 execute 的攻击面是整个 shell。
//     交互对话里用户至少能看着它执行、随时点停止。
//  2. 工具集按 ToolScope 裁剪。一个"每天汇总昨天的日志"的任务不需要发邮件的能力，
//     给了它就是在扩大出错时的影响范围。
//
// Execute runs one task.
//
// Two hard constraints:
//
//  1. THE FREE-FORM EXECUTE TOOL IS FORCE-DISABLED regardless of the global setting. Scheduled
//     tasks run unattended, so nobody can intervene when something goes wrong, and execute's
//     attack surface is an entire shell. In an interactive conversation the user at least
//     watches it happen and can hit stop.
//  2. The tool set is trimmed by ToolScope. A task that summarizes yesterday's logs has no need
//     to send email, and granting it only widens the blast radius when something misfires.
func (r *Runner) Execute(ctx context.Context, t *Task) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()

	sessionID, err := r.resolveSession(runCtx, t)
	if err != nil {
		return "", err
	}

	opts := kernel.TurnOptions{
		DisableShell: true,
		ExtraAugmenters: []kernel.Augmenter{
			r.registry.Augmenter(tools.Scope{Allow: t.ToolScope}),
		},
	}

	prompt := t.Prompt
	if err := r.sessions.AppendMessages(runCtx, sessionID,
		[]*schema.Message{schema.UserMessage(prompt)}); err != nil {
		return "", err
	}

	iter, _, err := r.engine.Run(runCtx, sessionID, prompt, opts)
	if err != nil {
		return "", err
	}

	produced, err := drain(iter)
	if len(produced) > 0 {
		if appendErr := r.sessions.AppendMessages(runCtx, sessionID, produced); appendErr != nil {
			return "", appendErr
		}
	}
	if err != nil {
		return "", err
	}
	return summarize(produced), nil
}

// resolveSession 决定这次执行落在哪个会话里。
//
// keep_context 为真时沿用指定会话：适合"每天接着昨天的进度继续"这类任务。
// 否则每次新建一个会话：任务之间互不影响，一次跑偏不会污染后续所有执行。
//
// resolveSession decides which conversation this execution belongs to.
//
// With keep_context the named conversation is reused, suiting tasks that pick up where yesterday
// left off. Otherwise a fresh conversation is created per run, so executions stay independent and
// one derailed run cannot contaminate every later one.
func (r *Runner) resolveSession(ctx context.Context, t *Task) (string, error) {
	if t.KeepContext && t.SessionID != "" {
		if _, err := r.sessions.Get(ctx, t.SessionID); err == nil {
			return t.SessionID, nil
		}
		// 指定的会话已被删除时退回新建，而不是让任务从此永远失败。
		// If the named conversation was deleted, fall back to a new one rather than letting the
		// task fail forever after.
	}
	s, err := r.sessions.Create(ctx, fmt.Sprintf("定时任务：%s", t.Name))
	if err != nil {
		return "", err
	}
	if t.KeepContext {
		t.SessionID = s.ID
	}
	return s.ID, nil
}

// drain 消费事件流并收集完整消息。
// drain consumes the event stream and collects the complete messages.
func drain(iter *adk.AsyncIterator[*adk.AgentEvent]) ([]*schema.Message, error) {
	var out []*schema.Message
	for {
		event, ok := iter.Next()
		if !ok {
			return out, nil
		}
		if event.Err != nil {
			if errors.Is(event.Err, io.EOF) {
				return out, nil
			}
			return out, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil || msg == nil {
			continue
		}
		out = append(out, msg)
	}
}

// summarize 取最后一条助手回复作为执行结果。
// summarize takes the final assistant reply as the execution result.
func summarize(msgs []*schema.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == schema.Assistant && strings.TrimSpace(msgs[i].Content) != "" {
			return truncate(msgs[i].Content, maxResultChars)
		}
	}
	return "任务已执行，但模型没有产生文本回复 / the task ran but the model produced no text reply"
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "\n…[结果已截断 / result truncated]"
}
