package kernel

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"

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
	ctx context.Context, sessionID, userInput string, opts TurnOptions,
) (*adk.AsyncIterator[*adk.AgentEvent], *Snapshot, error) {
	snap, err := e.BuildSnapshot(ctx, sessionID, opts)
	if err != nil {
		return nil, nil, err
	}
	agent, err := Build(ctx, snap)
	if err != nil {
		return nil, nil, err
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: e.cfg.CheckPoint,
	})

	history, err := e.cfg.Sessions.BuildModelInput(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	input := append(history, schema.UserMessage(userInput))

	return runner.Run(ctx, input), snap, nil
}
