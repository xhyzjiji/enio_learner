package kernel

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"

	"private/agent_basedon_eino/internal/agent/approval"
	"private/agent_basedon_eino/internal/agent/config"
	"private/agent_basedon_eino/internal/agent/session"
)

// defaultInstruction 是没有任何增强时的基础系统提示词。
// defaultInstruction is the base system prompt before any augmentation.
const defaultInstruction = `你是一个通用助手，可以调用工具、检索本地文档、记住用户偏好、管理定时任务。
回答时使用与用户相同的语言。需要外部信息时主动使用工具，不要凭空编造。

You are a general-purpose assistant able to call tools, search local documents, remember user
preferences and manage scheduled tasks. Reply in the same language the user writes in. Reach for
tools when you need external information rather than inventing it.`

// workspaceNotice 告诉模型文件操作的边界在哪。
//
// 不说清楚的后果不是「模型小心行事」，而是它编一个看起来合理的绝对路径——
// /Users/username/Documents 这种——然后撞上工作区边界。它不是在试探沙箱，
// 它只是不知道沙箱存在。
//
// workspaceNotice tells the model where the file-operation boundary lies.
//
// Leaving it unsaid does not make the model cautious; it makes the model invent a
// plausible-looking absolute path such as /Users/username/Documents and run straight into the
// workspace boundary. It is not probing the sandbox — it does not know one exists.
const workspaceNotice = `
文件工具的根目录是 %s，所有路径都相对于它，越界访问会被拒绝。
写文件时直接用相对路径（例如 notes/draft.md），不要拼绝对路径，也不要猜测用户的主目录位置。

The root directory for file tools is %s. All paths are relative to it and anything outside is
refused. Use relative paths such as notes/draft.md when writing; never construct absolute paths
and never guess the location of the user's home directory.`

// shellNotice 在 execute 可用时补充说明它与文件工具的区别，并明确声明联网能力。
//
// 声明联网不是多余的。中小参数模型在预训练里被反复灌输"我是语言模型，无法访问
// 互联网"，这条先验强到会盖过工具表——实测 qwen3:14b 拿着 execute 却直接回复
// "无法执行网络安装"，连一次工具调用都没发起。能力必须在提示词里说出来，
// 光挂上工具是不够的。
//
// 单说工作区根目录是不够的：execute 的子进程不受那条边界约束，而技能目录恰好在
// 边界之外。不点明这一点，模型会把 execute 的工作目录当成唯一能去的地方，
// 于是把东西装进 workspace/skills/ 这种看起来对、实际不会被扫描的位置。
//
// shellNotice explains how execute differs from the file tools, when it is available, and
// states the network capability outright.
//
// Stating network access is not redundant. Mid-sized models are drilled during pre-training on
// "I am a language model and cannot access the internet", and that prior is strong enough to
// override the tool list: qwen3:14b, holding execute, replied "cannot perform network installs"
// without ever issuing a tool call. A capability has to be spelled out in the prompt; mounting
// the tool is not enough.
//
// Naming the workspace root alone is not enough: execute's subprocesses are not bound by that
// boundary, and the skills directory happens to sit outside it. Left unsaid, the model treats
// execute's working directory as the only reachable place and installs things into paths like
// workspace/skills/ — plausible-looking locations that are never scanned.
const shellNotice = `
你可以执行 shell 命令，因此**具备联网能力**：curl、wget、git、包管理器都能正常访问网络。
需要下载文件、读取网页或安装工具时直接去做，不要回答"无法访问互联网"。
执行命令时当前目录是 %s，但子进程不受工作区限制，需要时可以用绝对路径访问别处。
本 Agent 的技能目录是 %s——安装技能一律装到这里，装到别处不会被识别。

You can run shell commands, so you DO have network access: curl, wget, git and package managers
all reach the network. When you need to download a file, read a web page or install a tool, just
do it; never reply that you cannot access the internet.
When running commands the current directory is %s, but subprocesses are not confined to the
workspace and may use absolute paths to reach elsewhere when needed.
This agent's skills directory is %s. Always install skills there; anywhere else is not detected.`

// cliDiscipline 是调用陌生命令行工具时的几条硬性纪律，随 shellNotice 一起追加。
//
// 这几条不是泛泛的最佳实践，每一条都对应一次真实的失败。模型用 lark-cli 建飞书文档时：
// 只读了顶层 --help 就猜子命令的参数名，连错两个 flag；没读帮助里指名要读的那份指南，
// 于是漏掉 --doc-format 默认是 xml 而不是 markdown，整篇 markdown 被当纯文本塞进文档；
// 把多行正文直接拼进命令行，经 JSON 与 shell 两层转义后换行变成字面量的反斜杠加 n；
// 第一次失败的真实原因是命令名写错，它却归因到转义上，多加了一层反斜杠并一路带到最后；
// 中间还把同一条失败命令一字不差地重发了两次。
//
// 第 2 条替代了"把某个 CLI 的文档入口硬编码进提示词"的做法。设计良好的 CLI 会在 --help
// 和报错里指明该读什么（lark-cli 指向 `lark-cli skills read lark-doc`），模型只要肯照做就够了，
// 不需要我们为每个工具单独写一条规则。
//
// cliDiscipline states a few hard rules for driving unfamiliar command-line tools, appended
// alongside shellNotice.
//
// These are not generic best practices; each one maps to an observed failure. While creating a
// Lark document with lark-cli the model: read only the top-level --help and then guessed the
// subcommand's flag names, getting two of them wrong in a row; skipped the guide that --help
// explicitly told it to read, and so missed that --doc-format defaults to xml rather than
// markdown, which shoved the whole markdown body in as plain text; inlined the multi-line body
// into the command line, where two layers of escaping (JSON then shell) turned its newlines into
// a literal backslash-n; misattributed its first failure — actually a wrong command name — to
// escaping, added another backslash layer and carried that bug all the way to the end; and along
// the way resent a failed command verbatim.
//
// Rule 2 replaces hard-coding any specific CLI's documentation entry point into the prompt. A
// well-built CLI already names what to read in its --help and error output (lark-cli points at
// `lark-cli skills read lark-doc`); the model only has to follow it, and we avoid writing a
// bespoke rule per tool.
const cliDiscipline = `

使用你不熟悉的命令行工具时，按下面几条做：
1. 先看 <命令> --help，再看 <命令> <子命令> --help。顶层帮助不会列出子命令的参数，不要凭印象猜参数名。
2. 帮助或报错让你去读某份指南、某个 skill 或 doc（例如提示 "run xxx skills read yyy"），就先读完再动手。
   参数的默认值和坑通常只写在那里，不读就动手等于在猜。
3. 报错里带 hint、"did you mean" 或 suggestions 时照着改，一次只改一个地方。
   同时改多处，成功了也分不清刚才到底错在哪，错误的修改会被一路带下去。
4. 同一条命令失败后不要原样重发。
5. 多行文本，或含引号、反斜杠、换行的内容，不要拼进命令行：先用 write_file 写成文件，
   再让命令从文件读（看 --help 支持哪种，常见是 --content @文件、-f 文件 或 < 文件）。
   直接拼接要穿过 JSON 参数和 shell 两层转义，换行很容易变成字面量的反斜杠加 n。
   execute 的当前目录就是文件工具的根目录，所以相对路径两边通用。

When driving a command-line tool you are not already fluent in, follow these rules:
1. Read <command> --help first, then <command> <subcommand> --help. Top-level help does not list a
   subcommand's flags; never guess flag names from memory.
2. When help or an error tells you to read a guide, skill or doc (e.g. "run xxx skills read yyy"),
   read it before acting. Flag defaults and pitfalls are usually documented only there, and acting
   without reading it is guessing.
3. When an error carries a hint, "did you mean" or suggestions, follow it and change one thing at a
   time. Change several at once and even success leaves you unable to tell what was actually wrong,
   so a bogus edit rides along unnoticed.
4. Never resend a command verbatim after it has failed.
5. Never inline multi-line text, or anything containing quotes, backslashes or newlines, into the
   command line: write it to a file with write_file, then have the command read that file (check
   --help for the supported form; --content @file, -f file and < file are common).
   Inlining must survive two layers of escaping, JSON arguments then shell, and newlines readily
   degrade into a literal backslash-n. execute's working directory is the file tools' root
   directory, so relative paths work on both sides.`

// Augmenter 在基础快照之上补充能力。
//
// 工具、技能、长期记忆分别由 US2、US3、US5 实现，它们都需要往快照里加东西。
// 做成接口而非把这些依赖硬塞进 Engine，是为了让每个阶段各自独立落地：
// 没有实现的阶段就是一个空的增强器列表，Engine 一行都不用改。
//
// Augmenter enriches the base snapshot.
//
// Tools, skills and long-term memory are delivered by US2, US3 and US5 respectively, and each
// needs to contribute to the snapshot. Modelling this as an interface rather than wiring those
// dependencies into Engine lets each phase land independently: an undelivered phase is simply
// an absent entry in the augmenter list, and Engine needs no change at all.
type Augmenter interface {
	// Name 用于错误信息定位。
	// Name identifies the augmenter in error messages.
	Name() string
	// Augment 就地修改快照。实现方只应新增内容，不应覆盖已有字段。
	// Augment mutates the snapshot in place. Implementations should only add, never overwrite.
	Augment(ctx context.Context, snap *Snapshot) error
}

// EngineConfig 是引擎的构造参数。
// EngineConfig holds the engine's construction parameters.
type EngineConfig struct {
	Sessions   *session.Store
	Config     *config.Manager
	CheckPoint adk.CheckPointStore
	Files      FileBackend
	Shell      filesystem.Shell
	WorkDir    string
	// SkillsDir 是技能目录。它在工作区之外，文件工具够不到，只有 execute 能写。
	// SkillsDir is the skills directory. It lies outside the workspace, beyond the reach of the
	// file tools, and only execute can write to it.
	SkillsDir  string
	Augmenters []Augmenter
}

// Engine 把一次用户输入变成一条 Agent 事件流。
// Engine turns one user input into a stream of agent events.
type Engine struct {
	cfg      EngineConfig
	modelFac ModelFactory
}

// NewEngine 构造引擎。
// NewEngine builds the engine.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("engine requires a session store")
	}
	if cfg.Config == nil {
		return nil, errors.New("engine requires a config manager")
	}
	if cfg.WorkDir == "" {
		return nil, errors.New("engine requires a work directory")
	}
	abs, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("resolve work dir: %w", err)
	}
	cfg.WorkDir = abs
	return &Engine{cfg: cfg}, nil
}

// TurnOptions 描述一轮对话的可选覆盖项，定时任务用它裁剪能力。
// TurnOptions describes per-turn overrides; scheduled tasks use them to restrict capabilities.
type TurnOptions struct {
	// DisableShell 强制关闭 execute 工具，定时任务恒为真。
	// DisableShell force-disables the execute tool; always true for scheduled tasks.
	DisableShell bool
	// ExtraAugmenters 追加到默认增强器之后。
	// ExtraAugmenters are appended after the default ones.
	ExtraAugmenters []Augmenter
}

// BuildSnapshot 为一次对话构造配置快照。
// BuildSnapshot builds the configuration snapshot for one conversation.
func (e *Engine) BuildSnapshot(ctx context.Context, sessionID string, opts TurnOptions) (*Snapshot, error) {
	rt := e.cfg.Config.Current()

	chatModel, err := e.modelFac.NewChatModel(ctx, rt.ModelName)
	if err != nil {
		return nil, err
	}

	snap := &Snapshot{
		SessionID:    sessionID,
		Model:        chatModel,
		Instruction:  defaultInstruction + fmt.Sprintf(workspaceNotice, e.cfg.WorkDir, e.cfg.WorkDir),
		SummaryModel: chatModel,
		Files:        e.cfg.Files,
		WorkDir:      e.cfg.WorkDir,
		Runtime:      rt,
	}
	// execute 工具要同时满足"全局开启"与"本轮未禁用"才会挂上。
	// The execute tool is mounted only when it is globally enabled AND not disabled this turn.
	if rt.EnableExecute && !opts.DisableShell {
		snap.Shell = e.cfg.Shell
		snap.Instruction += fmt.Sprintf(shellNotice,
			e.cfg.WorkDir, e.cfg.SkillsDir, e.cfg.WorkDir, e.cfg.SkillsDir) + cliDiscipline
	}

	for _, a := range append(e.cfg.Augmenters, opts.ExtraAugmenters...) {
		if err := a.Augment(ctx, snap); err != nil {
			return nil, fmt.Errorf("增强器 %s 失败 / augmenter %s failed: %w", a.Name(), a.Name(), err)
		}
	}
	return snap, nil
}

// Run 执行一轮对话并返回事件迭代器。
//
// 模型输入取自 session_context 视图而非原始消息表：前者是被 summarization 压缩过的
// 版本，后者是给用户看的完整记录。混用会让压缩失效——压缩完了下一轮又把全量历史喂回去。
//
// Run executes one conversation turn and returns the event iterator.
//
// Model input comes from the session_context view rather than the raw message table: the
// former is the summarization-compressed version while the latter is the complete record shown
// to the user. Mixing them defeats compression, since a compressed turn would be followed by
// feeding the full history straight back in.
func (e *Engine) Run(
	ctx context.Context, sessionID, userInput, checkpointID string, opts TurnOptions,
) (*adk.AsyncIterator[*adk.AgentEvent], *Snapshot, error) {
	// 会话 ID 必须随上下文传进工具层：工具层据此判断这次执行是否来自交互对话。
	// 定时任务没有会话 ID，于是 execute 会直接拒绝而不是中断——中断了也没人恢复。
	//
	// The conversation ID must travel with the context into the tool layer, which uses it to
	// tell whether an execution came from an interactive chat. A scheduled task has none, so
	// execute refuses outright instead of interrupting — nobody would ever resume it.
	ctx = approval.WithSession(ctx, sessionID)

	runner, snap, err := e.runner(ctx, sessionID, opts)
	if err != nil {
		return nil, nil, err
	}

	history, err := e.cfg.Sessions.BuildModelInput(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	input := append(history, schema.UserMessage(userInput))

	return runner.Run(ctx, input, adk.WithCheckPointID(checkpointID)), snap, nil
}

// Resume 从断点继续一轮被中断的对话，把 decision 交给指定的中断点。
//
// 重建 Agent 走的是和 Run 完全相同的 BuildSnapshot，这是有意的：恢复时的工具集、
// 提示词、中间件必须与中断时一致，否则 checkpoint 里记录的节点在新图里找不到对应。
// 换句话说，用户在中断期间改了配置，恢复会失败——这比带着两套半配置跑完要好。
//
// Resume continues an interrupted turn from its checkpoint, handing decision to the named
// interrupt point.
//
// The agent is rebuilt through the very same BuildSnapshot that Run uses, deliberately: the
// tool set, instruction and middlewares at resume time must match those at interrupt time, or
// the nodes recorded in the checkpoint will not line up with the new graph. Put differently, if
// the user changed configuration while the turn was paused, the resume fails — which is better
// than completing it under a half-changed configuration.
func (e *Engine) Resume(
	ctx context.Context, sessionID, checkpointID, interruptID string, decision approval.Decision,
) (*adk.AsyncIterator[*adk.AgentEvent], *Snapshot, error) {
	ctx = approval.WithSession(ctx, sessionID)

	runner, snap, err := e.runner(ctx, sessionID, TurnOptions{})
	if err != nil {
		return nil, nil, err
	}
	iter, err := runner.ResumeWithParams(ctx, checkpointID,
		&adk.ResumeParams{Targets: map[string]any{interruptID: decision}},
		adk.WithCheckPointID(checkpointID))
	if err != nil {
		return nil, nil, fmt.Errorf("恢复断点失败 / failed to resume checkpoint %s: %w", checkpointID, err)
	}
	return iter, snap, nil
}

// DropCheckpoint 删除一个断点。一轮正常跑完后必须调用：eino 不会自己清理，
// 不删的话每一轮被中断过的对话都会在库里留下几十 KB 的死数据。
//
// DropCheckpoint deletes one checkpoint. It must be called once a turn finishes normally: eino
// does not clean up after itself, and without this every turn that was ever interrupted leaves
// tens of kilobytes of dead data in the database.
func (e *Engine) DropCheckpoint(ctx context.Context, checkpointID string) error {
	if e.cfg.CheckPoint == nil || checkpointID == "" {
		return nil
	}
	// adk.CheckPointStore 接口只有 Get/Set，删除能力是实现方自带的可选扩展。
	// adk.CheckPointStore declares only Get/Set; deletion is an optional extension of the
	// implementation.
	deleter, ok := e.cfg.CheckPoint.(interface {
		Delete(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return deleter.Delete(ctx, checkpointID)
}

func (e *Engine) runner(ctx context.Context, sessionID string, opts TurnOptions) (
	*adk.Runner, *Snapshot, error) {
	snap, err := e.BuildSnapshot(ctx, sessionID, opts)
	if err != nil {
		return nil, nil, err
	}
	agent, err := Build(ctx, snap)
	if err != nil {
		return nil, nil, err
	}
	return adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: e.cfg.CheckPoint,
	}), snap, nil
}
