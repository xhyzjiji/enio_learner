package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/compose"

	"private/agent_basedon_eino/internal/agent/approval"
	"private/agent_basedon_eino/internal/agent/config"
)

// PolicyAction 是策略规则的动作。
// PolicyAction is the action of a policy rule.
type PolicyAction string

const (
	// PolicyAllow 允许匹配的命令执行。
	// PolicyAllow permits a matching command.
	PolicyAllow PolicyAction = "allow"
	// PolicyDeny 拒绝匹配的命令。
	// PolicyDeny rejects a matching command.
	PolicyDeny PolicyAction = "deny"
)

// PolicyRule 是一条策略规则，Pattern 为正则。
// PolicyRule is one policy rule; Pattern is a regular expression.
type PolicyRule struct {
	ID      string       `json:"id"`
	Pattern string       `json:"pattern"`
	Action  PolicyAction `json:"action"`
	Note    string       `json:"note"`
}

// builtinDenyPatterns 是无论页面怎么配都拦下的命令。
//
// 这不是"安全边界"——execute 本质上就是给模型开了一个 shell，真要绕总有办法。
// 它的作用是拦住**模型的误操作**：模型想清理临时文件却写出 rm -rf /，
// 这类事故远比蓄意攻击常见。真正的安全边界是"该工具默认关闭"。
//
// builtinDenyPatterns are commands rejected regardless of UI configuration.
//
// This is not a security boundary — execute fundamentally hands the model a shell, and a
// determined bypass always exists. Its job is to stop MODEL MISTAKES: a model meaning to clear
// temporary files and emitting rm -rf / is far more common than deliberate attack. The real
// security boundary is that the tool is off by default.
var builtinDenyPatterns = []string{
	`(^|[;&|]\s*)rm\s+(-[a-zA-Z]*\s+)*(/|~|\$HOME)(\s|$)`,
	`(^|[;&|]\s*)(shutdown|reboot|halt|poweroff)\b`,
	`\bmkfs(\.|\s)`,
	`\bdd\s+[^|]*\bof=/dev/`,
	`>\s*/dev/(sd|nvme|disk)`,
	`(^|[;&|]\s*)chmod\s+(-[a-zA-Z]*\s+)*777\s+/`,
	// 直接读取环境变量的尝试。子进程环境已经走白名单，这里只是让拒绝更早、更直白。
	//
	// printenv 整条拦掉，不区分有没有参数：它唯一的用途就是打印环境变量，而
	// `printenv SOME_KEY` 恰恰是最省事的那种读法。原先只拦裸命令，带一个参数就绕过去了。
	// env 与 set 不能同样处理——`env VAR=1 cmd` 是合法的运行方式，`set -e` 是脚本惯例，
	// 一刀切会误伤，所以只拦"后面没有命令可跑"的转储形态，重定向也算进去。
	//
	// 这里不去拦 `echo $SOME_KEY`：要覆盖它就得匹配任意 echo 加变量展开，而脚本里
	// `echo "$result"` 太常见，误伤一条能跑的命令比漏掉一次读取更糟。真正的依仗仍然是
	// 白名单——子进程环境里本就没有秘密可读。
	//
	// Attempts to dump the environment. The child environment is already whitelisted; this
	// merely makes the refusal earlier and more explicit.
	//
	// printenv is rejected outright regardless of arguments: printing environment variables is
	// its only purpose, and `printenv SOME_KEY` is the laziest way to read one. The previous
	// pattern caught only the bare command, so a single argument walked straight past it.
	// env and set cannot be treated the same way — `env VAR=1 cmd` is a legitimate way to run
	// something and `set -e` is a scripting staple — so only the dumping form, where no command
	// follows, is rejected; redirection counts as dumping too.
	//
	// `echo $SOME_KEY` is deliberately left alone: covering it would mean matching any echo with
	// a variable expansion, and `echo "$result"` is far too common in scripts. Breaking a working
	// command is worse than missing one read. The real safeguard remains the whitelist — there is
	// nothing secret in the child environment to begin with.
	`(^|[;&|]\s*)printenv\b`,
	`(^|[;&|]\s*)(env|set)\s*($|[;&|>])`,
}

// CommandValidator 按策略校验命令串。
// CommandValidator validates a command string against the policy.
type CommandValidator struct {
	builtin []*regexp.Regexp
	allow   []*regexp.Regexp
	deny    []*regexp.Regexp
	// requireAllow 为真时采用白名单模式：没有任何 allow 规则命中就拒绝。
	// When requireAllow is true the validator runs in allowlist mode: anything not matched by
	// an allow rule is rejected.
	requireAllow bool
}

// NewCommandValidator 按规则集构造校验器。
//
// 只要配置了任意一条 allow 规则，就自动切到白名单模式。这个默认值是刻意的：
// 一个人会去配 allow 规则，说明他心里想的是"只允许这些"，而不是"除了这些都行"。
//
// NewCommandValidator builds a validator from a rule set.
//
// The presence of any allow rule automatically switches to allowlist mode. That default is
// deliberate: someone who bothers to write allow rules means "only these are permitted", not
// "everything except these".
func NewCommandValidator(rules []PolicyRule) (*CommandValidator, error) {
	v := &CommandValidator{}
	for _, p := range builtinDenyPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("compile builtin deny pattern %q: %w", p, err)
		}
		v.builtin = append(v.builtin, re)
	}
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("策略规则 %s 的正则无效 / invalid regexp in policy rule %s: %w",
				r.ID, r.ID, err)
		}
		switch r.Action {
		case PolicyAllow:
			v.allow = append(v.allow, re)
			v.requireAllow = true
		default:
			v.deny = append(v.deny, re)
		}
	}
	return v, nil
}

// Validate 校验一条命令。返回的错误会原样交给模型，所以要说清楚为什么被拒。
// Validate checks one command. The returned error is handed to the model verbatim, so it must
// state why the command was refused.
func (v *CommandValidator) Validate(command string) error {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return fmt.Errorf("命令不能为空 / command must not be empty")
	}
	for _, re := range v.builtin {
		if re.MatchString(cmd) {
			return fmt.Errorf(
				"命令被内置安全规则拒绝（匹配 %q），这类操作不可通过 execute 执行 / "+
					"command rejected by a built-in safety rule (matched %q); such operations cannot run through execute",
				re.String(), re.String())
		}
	}
	for _, re := range v.deny {
		if re.MatchString(cmd) {
			return fmt.Errorf(
				"命令被本地策略拒绝（匹配 %q），如确需执行请先在页面调整策略 / "+
					"command rejected by local policy (matched %q); adjust the policy in the UI if it is genuinely needed",
				re.String(), re.String())
		}
	}
	if v.requireAllow {
		for _, re := range v.allow {
			if re.MatchString(cmd) {
				return nil
			}
		}
		return fmt.Errorf(
			"当前为白名单模式，命令未匹配任何允许规则 / allowlist mode is active and the command matched no allow rule")
	}
	return nil
}

// Shell 实现 filesystem.Shell，为模型提供 execute 自由执行工具。
//
// 注意 filesystem.Shell 的接口签名是 Execute(ctx, *ExecuteRequest{Command string})，
// 收到的是**一整条命令串**而非参数切片——这是该工具的本质：它就是一个 shell。
// 声明式 CLI 工具那套"参数切片传递"的防护在这里不适用，所以这个工具必须默认关闭，
// 并且在定时任务中强制关闭。
//
// Shell implements filesystem.Shell, providing the free-form execute tool to the model.
//
// Note that filesystem.Shell's signature is Execute(ctx, *ExecuteRequest{Command string}): it
// receives ONE COMMAND STRING rather than an argument slice. That is the nature of the tool —
// it is a shell. The "arguments as a slice" protection used by declarative CLI tools does not
// apply here, which is exactly why this tool is off by default and force-disabled inside
// scheduled tasks.
type Shell struct {
	runner  *Runner
	store   *Store
	runtime func() config.Runtime
	// approvals 为 nil 时确认机制整体关闭。
	// A nil approvals disables the confirmation mechanism entirely.
	approvals bool
}

// NewShell 构造 execute 工具的后端。
//
// 策略与限额都在每次执行前现读，不缓存：用户在页面上加一条拒绝规则、或把超时调长，
// 期望的是下一次执行就生效，而不是重启之后。读一次策略表的代价相对子进程启动可以忽略。
//
// runtime 传函数而非 config.Runtime 值，正是为了这一点——传值就等于把启动那一刻的
// 配置永久定住，而页面上的修改会静默无效。
//
// NewShell builds the backend of the execute tool.
//
// Both policy and limits are read fresh before every execution rather than cached: a user adding
// a deny rule or raising the timeout in the UI expects it to apply to the very next execution,
// not after a restart. One read of the tiny policy table is negligible next to spawning a
// subprocess.
//
// runtime is a function rather than a config.Runtime value precisely for this reason: passing a
// value would freeze the configuration as it stood at startup, and edits made in the UI would
// silently have no effect.
func NewShell(runner *Runner, store *Store, runtime func() config.Runtime, approvals bool) *Shell {
	return &Shell{runner: runner, store: store, runtime: runtime, approvals: approvals}
}

// Execute 校验并执行一条命令串。
// Execute validates and runs one command string.
func (s *Shell) Execute(ctx context.Context, in *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	rules, err := s.store.ListPolicy(ctx)
	if err != nil {
		return nil, err
	}
	validator, err := NewCommandValidator(rules)
	if err != nil {
		return nil, err
	}
	if err := validator.Validate(in.Command); err != nil {
		return nil, err
	}
	// 命令串交给 sh -c 解释，但环境变量仍然走白名单、工作目录仍然限制在工作区内。
	// The command string is interpreted by sh -c, yet the environment is still whitelisted and
	// the working directory is still confined to the workspace.
	rt := s.runtime()
	// 这一步必须排在 confirm 前面：一条注定会把 \n 原样写进目标的命令，
	// 没有理由先去打扰用户确认。
	//
	// This must precede confirm: there is no reason to interrupt the user for approval of a
	// command that is guaranteed to write \n verbatim into its target.
	if refusal := inlineTextRefusal(in.Command); refusal != "" {
		code := 126
		return &filesystem.ExecuteResponse{Output: refusal, ExitCode: &code}, nil
	}
	if refusal, err := s.confirm(ctx, rt, in.Command); err != nil {
		return nil, err
	} else if refusal != "" {
		// 被拒绝时返回正常响应而非错误：模型需要把"用户拒绝了"当成一条可读的结果
		// 来调整思路，而不是收到一个看起来像故障的报错。退出码用 126，与 shell 里
		// "命令存在但不可执行"的含义一致。
		//
		// A refusal returns a normal response rather than an error: the model needs to read
		// "the user refused" as an ordinary result and adjust, not receive something that looks
		// like a malfunction. Exit code 126 matches the shell's "found but not executable".
		code := 126
		return &filesystem.ExecuteResponse{Output: refusal, ExitCode: &code}, nil
	}
	res, err := s.runner.Run(ctx, &ExecRequest{
		Command:        "/bin/sh",
		Args:           []string{"-c", in.Command},
		Timeout:        time.Duration(rt.ExecTimeoutSec) * time.Second,
		MaxOutputBytes: rt.MaxToolResultBytes,
	})
	if err != nil {
		return nil, err
	}
	exitCode := res.ExitCode
	return &filesystem.ExecuteResponse{
		Output:    res.Output,
		ExitCode:  &exitCode,
		Truncated: res.Truncated,
	}, nil
}

// confirm 在执行前征求人工同意，返回拒绝说明（空串表示放行）。
//
// 它被同一次工具调用执行**两遍**，分属两个进程时期：
//
//  1. 首次：没有中断状态，于是返回一个中断信号。eino 据此把整条执行链序列化进
//     checkpoint 并结束这一轮。此时没有任何 goroutine 在等待。
//  2. 恢复：用户做出决定后，调用方带着决定恢复执行，同一行代码再跑一次，
//     这次拿到的是决定本身。
//
// 第二步里有个必须守住的分支：若本次恢复的目标不是自己（isResume 为假），必须
// **重新中断**而不是继续。一轮里可能有多个中断点，别人被恢复时自己若放行，
// 就等于一次未经确认的执行。
//
// confirm seeks human consent before execution, returning a refusal message (empty means go).
//
// It runs TWICE for the same tool call, in what may be two different process lifetimes:
//
//  1. First pass: no interrupt state, so it returns an interrupt signal. eino serializes the
//     whole execution chain into a checkpoint and ends the turn. No goroutine is left waiting.
//  2. Resume: once the user decides, the caller resumes with that decision and this same line
//     runs again, this time receiving the verdict.
//
// One branch in step 2 must hold: if this component is not the target of the resume
// (isResume false), it MUST re-interrupt rather than proceed. A turn may hold several interrupt
// points, and proceeding while someone else is resumed would be an unconfirmed execution.
func (s *Shell) confirm(ctx context.Context, rt config.Runtime, command string) (string, error) {
	if !rt.RequireExecApproval || !s.approvals {
		return "", nil
	}

	wasInterrupted, _, _ := compose.GetInterruptState[any](ctx)
	if wasInterrupted {
		isResume, hasData, raw := compose.GetResumeContext[any](ctx)
		if !isResume || !hasData {
			return "", compose.Interrupt(ctx, approval.AskInfo{Kind: approval.KindExec, Command: command})
		}
		d, ok := raw.(approval.Decision)
		if !ok {
			return "", fmt.Errorf("恢复数据类型不是确认决定 / resume data is not an approval decision: %T", raw)
		}
		if d.Approved {
			return "", nil
		}
		reason := d.Reason
		if reason == "" {
			reason = "用户拒绝了这条命令 / the user refused this command"
		}
		return "命令未执行：" + reason + " / command not run: " + reason, nil
	}

	if approval.SessionFrom(ctx) == "" {
		// 没有会话上下文意味着这次执行不来自交互对话（例如定时任务）。无人可确认时
		// 一律拒绝，而且必须在中断**之前**拒绝：定时任务中断了也没人会去恢复它，
		// 那一轮会直接卡死并留下一个永远不会被消费的 checkpoint。
		//
		// An absent conversation context means this execution did not come from an interactive
		// chat (a scheduled task, for instance). With nobody to ask, always refuse — and refuse
		// BEFORE interrupting: nobody would ever resume a scheduled task, so it would simply
		// stall and leave behind a checkpoint that is never consumed.
		return "该执行环境无法进行人工确认，命令未执行 / this execution context cannot request " +
			"human confirmation, so the command was not run", nil
	}
	return "", compose.Interrupt(ctx, approval.AskInfo{Kind: approval.KindExec, Command: command})
}

// 编译期确认 Shell 满足 filesystem.Shell。
// Compile-time assertion that Shell satisfies filesystem.Shell.
var _ filesystem.Shell = (*Shell)(nil)
