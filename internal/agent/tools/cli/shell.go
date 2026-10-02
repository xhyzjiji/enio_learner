package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/adk/filesystem"
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
	// Attempts to dump the environment. The child environment is already whitelisted; this
	// merely makes the refusal earlier and more explicit.
	`(^|[;&|]\s*)(env|printenv|set)\s*($|[;&|])`,
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
	runner   *Runner
	store    *Store
	maxBytes int
}

// NewShell 构造 execute 工具的后端。
//
// 策略在每次执行前从数据库现读，不缓存：用户在页面上加一条拒绝规则，期望的是
// 下一次执行就生效，而不是重启之后。策略表很小，这次读取的代价相对子进程启动
// 可以忽略。
//
// NewShell builds the backend of the execute tool.
//
// The policy is read from the database before every execution rather than cached: a user adding
// a deny rule in the UI expects it to apply to the very next execution, not after a restart.
// The policy table is tiny and the read is negligible next to spawning a subprocess.
func NewShell(runner *Runner, store *Store, maxBytes int) *Shell {
	return &Shell{runner: runner, store: store, maxBytes: maxBytes}
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
	res, err := s.runner.Run(ctx, &ExecRequest{
		Command:        "/bin/sh",
		Args:           []string{"-c", in.Command},
		MaxOutputBytes: s.maxBytes,
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

// 编译期确认 Shell 满足 filesystem.Shell。
// Compile-time assertion that Shell satisfies filesystem.Shell.
var _ filesystem.Shell = (*Shell)(nil)
