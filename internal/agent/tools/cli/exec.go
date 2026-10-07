package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 默认超时与硬上限。超时不是可选项：一个卡住的子进程会一直占着对话不放。
// Default and hard-cap timeouts. A timeout is not optional: a hung child process would hold the
// conversation hostage indefinitely.
const (
	defaultTimeout = 30 * time.Second
	maxTimeout     = 10 * time.Minute
)

// allowedEnvKeys 是传给子进程的环境变量白名单。
//
// 这里必须是**白名单**而非黑名单，原因很直接：本进程的环境里有 ZHIPUAI_API_KEY。
// 若用 cmd.Env = os.Environ() 继承全部变量，模型只要让 execute 跑一次 env 或
// printenv，密钥就原样出现在工具返回值里，进而进入对话历史、进入数据库、进入日志。
// 黑名单则永远漏得掉——今天漏掉 ZHIPUAI_API_KEY，明天漏掉新加的某个 TOKEN。
//
// allowedEnvKeys is the whitelist of environment variables passed to child processes.
//
// It must be a WHITELIST rather than a blacklist for a direct reason: this process's
// environment contains ZHIPUAI_API_KEY. With cmd.Env = os.Environ() the child inherits
// everything, and a single `env` or `printenv` through the execute tool would place the key
// verbatim into a tool result, and from there into conversation history, the database and the
// logs. A blacklist always leaks eventually — miss ZHIPUAI_API_KEY today, miss some newly added
// TOKEN tomorrow.
var allowedEnvKeys = []string{"PATH", "HOME", "LANG", "LC_ALL", "TZ", "TMPDIR"}

// ExecRequest 描述一次子进程执行。
// ExecRequest describes one child-process execution.
type ExecRequest struct {
	// Command 是可执行文件名或路径。它来自配置而非模型输入。
	// Command is the executable name or path. It comes from configuration, not model input.
	Command string

	// Args 是参数列表。**必须是切片**：一旦拼成 shell 字符串，模型在参数里塞一个
	// `; rm -rf ~` 就直接变成了两条命令。切片传参时参数永远只是参数。
	// Args is the argument list. It MUST be a slice: concatenated into a shell string, a
	// model-supplied `; rm -rf ~` inside an argument silently becomes a second command. Passed
	// as a slice, an argument can only ever be an argument.
	Args []string

	// WorkDir 是工作目录，为空时用工作区根目录。
	// WorkDir is the working directory, defaulting to the workspace root.
	WorkDir string

	// Timeout 是执行超时，为零时用默认值。
	// Timeout bounds execution, defaulting when zero.
	Timeout time.Duration

	// MaxOutputBytes 是输出字节上限，超出截断并标注。
	// MaxOutputBytes caps output size; anything beyond is truncated and marked.
	MaxOutputBytes int
}

// ExecResult 是一次执行的结果。
// ExecResult is the outcome of one execution.
type ExecResult struct {
	Output    string
	ExitCode  int
	Truncated bool
	TimedOut  bool
}

// Runner 在工作区内执行子进程。
// Runner executes child processes inside the workspace.
type Runner struct {
	ws *Workspace
}

// NewRunner 构造执行器。
// NewRunner builds a runner.
func NewRunner(ws *Workspace) *Runner { return &Runner{ws: ws} }

// Run 执行一条命令。
//
// 三条约束同时成立才算安全：参数以切片传递、环境变量走白名单、工作目录限制在工作区内。
// 少任何一条，另外两条提供的防护都会被绕过。
//
// Run executes one command.
//
// Safety requires all three constraints at once: arguments passed as a slice, environment
// variables from the whitelist, and the working directory confined to the workspace. Drop any
// one of them and the protection offered by the other two can be bypassed.
func (r *Runner) Run(ctx context.Context, req *ExecRequest) (*ExecResult, error) {
	if strings.TrimSpace(req.Command) == "" {
		return nil, errors.New("命令不能为空 / command must not be empty")
	}

	workDir := r.ws.Root()
	if req.WorkDir != "" {
		resolved, err := r.ws.resolve(req.WorkDir)
		if err != nil {
			return nil, err
		}
		workDir = resolved
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 参数作为独立切片元素传入，绝不拼接成 shell 字符串。
	// Arguments go in as separate slice elements and are never concatenated into a shell string.
	cmd := exec.CommandContext(runCtx, req.Command, req.Args...)
	cmd.Dir = workDir
	cmd.Env = safeEnv()
	setProcessGroup(cmd)
	// 超时后由 Cancel 杀掉整个进程组，而不是只杀直接子进程。
	// On timeout Cancel kills the whole process group rather than just the direct child.
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return nil
	}
	// WaitDelay 是最后一道保险：万一仍有进程握着输出管道不放，到点强制关闭管道，
	// 让 Wait 返回。没有它的话，一个绕过了进程组的守护进程能把这一轮对话永久挂住。
	//
	// WaitDelay is the last line of defence: should some process still cling to the output pipe,
	// the pipe is force-closed when it expires so that Wait can return. Without it, a daemon
	// that escaped the process group could hang this conversation turn forever.
	cmd.WaitDelay = 2 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	res := &ExecResult{}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		res.ExitCode = 0
	case errors.As(runErr, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case res.TimedOut:
		res.ExitCode = -1
	default:
		return nil, fmt.Errorf("执行 %s 失败 / failed to run %s: %w", req.Command, req.Command, runErr)
	}

	res.Output, res.Truncated = composeOutput(&stdout, &stderr, res, req.MaxOutputBytes)
	return res, nil
}

// safeEnv 按白名单构造子进程环境。
// safeEnv builds the child environment from the whitelist.
func safeEnv() []string {
	env := make([]string, 0, len(allowedEnvKeys))
	for _, k := range allowedEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// composeOutput 合并 stdout 与 stderr 并按上限截断。
//
// 截断时明确标注而不是静默砍掉：模型看到"内容已截断"才知道结果不完整，
// 否则它会把半截输出当成全部事实继续推理。
//
// composeOutput merges stdout and stderr and truncates to the cap.
//
// Truncation is marked explicitly rather than silently applied: only when the model sees
// "content truncated" does it know the result is partial; otherwise it treats half an output as
// the whole truth and reasons on from there.
func composeOutput(stdout, stderr *bytes.Buffer, res *ExecResult, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	var sb strings.Builder
	if res.TimedOut {
		sb.WriteString("[命令超时被终止 / command timed out and was killed]\n")
	}
	if res.ExitCode != 0 && !res.TimedOut {
		fmt.Fprintf(&sb, "[退出码 / exit code: %d]\n", res.ExitCode)
	}
	if stdout.Len() > 0 {
		sb.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if stdout.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[stderr]\n")
		sb.WriteString(stderr.String())
	}

	out := sb.String()
	if out == "" {
		// 命令成功但一个字也没打印——mkdir、cp、chmod、mv 全是这样。
		//
		// 返回空串有两个后果，后一个尤其隐蔽：模型分不清"跑完了没输出"和"根本没跑"，
		// 于是常常把同一条命令再发一遍；而空内容的工具消息经 go-openai 序列化时，
		// content 字段带 omitempty 会被整个省掉，Ollama 的兼容层读到 nil 直接回
		// 400 "invalid message content type: <nil>"——报错落在 ChatModel 节点上，
		// 完全看不出是哪一条历史消息惹的祸。
		//
		// The command succeeded without printing a thing — mkdir, cp, chmod and mv all do this.
		//
		// Returning an empty string has two consequences, the second far more insidious: the
		// model cannot tell "ran fine, no output" from "never ran" and tends to resend the same
		// command; and an empty tool message, once serialized by go-openai, loses its content
		// field entirely to omitempty, so Ollama's compatibility layer reads nil and answers 400
		// "invalid message content type: <nil>" — an error attributed to the ChatModel node,
		// with no hint as to which history message caused it.
		out = "[命令执行成功，没有输出 / command succeeded with no output]"
	}
	if len(out) <= maxBytes {
		return out, false
	}
	return out[:maxBytes] + fmt.Sprintf(
		"\n\n[输出超过 %d 字节，已截断 / output exceeded %d bytes and was truncated]", maxBytes, maxBytes), true
}
