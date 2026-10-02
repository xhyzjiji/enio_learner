package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	fsmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	skillmw "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/compose"
)

const (
	// agentName 是 Agent 名称，会出现在事件流里。
	// agentName is the agent name, surfaced in the event stream.
	agentName = "general_agent"

	// defaultMaxIterations 兜住模型在工具之间无限打转。
	// defaultMaxIterations guards against the model looping between tools forever.
	defaultMaxIterations = 24

	// offloadDirName 与 taskDirName 是工作目录下的两个保留子目录。
	// offloadDirName and taskDirName are two reserved subdirectories under the work directory.
	offloadDirName = ".agent/offload"
	taskDirName    = ".agent/tasks"
)

// Build 按快照装配一个 Agent 实例。
//
// 中间件的挂载顺序是**固定的，不可调整**：
//
//	skill → filesystem → plantask → reduction → summarization
//
// 每一处约束都有具体原因，错序不会报错但效果会变差且极难归因：
//   - filesystem 必须早于 reduction：reduction 把大工具结果卸载成文件后，是靠
//     ReadFileToolName（默认 read_file）让模型取回的，而这个工具由 filesystem 注册
//   - reduction 必须早于 summarization：先做**可逆**的工具结果卸载，再做**有损**的
//     历史摘要。反过来会在还有大量工具结果可卸载时就把对话历史压没了
//
// Build assembles an agent instance from a snapshot.
//
// The middleware mounting order is FIXED and must not be changed:
//
//	skill -> filesystem -> plantask -> reduction -> summarization
//
// Every constraint has a concrete reason, and getting the order wrong raises no error while
// silently degrading behaviour in ways that are very hard to attribute:
//   - filesystem must precede reduction: after reduction offloads a large tool result to a
//     file, the model retrieves it via ReadFileToolName (default read_file), a tool that the
//     filesystem middleware registers
//   - reduction must precede summarization: do the REVERSIBLE tool-result offloading first and
//     the LOSSY history summarization second. Reversed, conversation history gets squashed
//     while plenty of offloadable tool results are still sitting there
func Build(ctx context.Context, snap *Snapshot) (adk.Agent, error) {
	if snap == nil {
		return nil, errors.New("snapshot is nil")
	}
	if snap.Model == nil {
		return nil, errors.New("snapshot.Model is nil")
	}

	handlers, err := buildHandlers(ctx, snap)
	if err != nil {
		return nil, err
	}

	maxIter := snap.MaxIterations
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        agentName,
		Description: "通用 Agent / general-purpose agent",
		Instruction: snap.Instruction,
		Model:       snap.Model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: snap.Tools,
				ToolCallMiddlewares: []compose.ToolMiddleware{
					recoverableToolErrors(slog.Default()),
				},
				// 模型调用不存在的工具时给一句说明而非直接报错中断。
				// 本地小模型尤其容易凭记忆编出一个听起来合理的工具名。
				// When the model calls a non-existent tool, explain instead of aborting. Local
				// smaller models are especially prone to inventing plausible-sounding tool names.
				UnknownToolsHandler: func(_ context.Context, name, _ string) (string, error) {
					return fmt.Sprintf(
						"工具 %s 不存在。请只使用本轮提供给你的工具，不要凭印象编造工具名。/ "+
							"Tool %s does not exist. Use only the tools offered this turn; "+
							"do not invent tool names from memory.", name, name), nil
				},
			},
		},
		MaxIterations: maxIter,
		// 用 Handlers 而非已废弃的 Middlewares：T005 实测确认六个内置中间件的
		// New() 恰好返回 adk.ChatModelAgentMiddleware，且按注册顺序生效。
		// Handlers rather than the deprecated Middlewares: gate T005 confirmed that all six
		// built-in middlewares' New() returns adk.ChatModelAgentMiddleware and that they take
		// effect in registration order.
		Handlers: handlers,
	})
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}
	return agent, nil
}

// buildHandlers 按固定顺序构造中间件链。各组件缺失时跳过对应中间件而非报错，
// 这样 US1 的 MVP 不必等 US3 的技能后端就能跑起来。
// buildHandlers builds the middleware chain in the fixed order. A missing component skips its
// middleware rather than failing, so the US1 MVP runs without waiting for the US3 skill backend.
func buildHandlers(ctx context.Context, snap *Snapshot) ([]adk.ChatModelAgentMiddleware, error) {
	var handlers []adk.ChatModelAgentMiddleware

	// 1. skill：技能目录渲染进 skill 工具的 description。
	//    Backend.List 在每轮 Info(ctx) 中被重新调用，热重载天然成立。
	// 1. skill: the catalog renders into the skill tool's description. Backend.List is called
	//    afresh in every Info(ctx), so hot reload comes for free.
	if snap.SkillBackend != nil {
		h, err := skillmw.NewMiddleware(ctx, &skillmw.Config{
			Backend:  snap.SkillBackend,
			AgentHub: snap.SkillAgentHub,
		})
		if err != nil {
			return nil, fmt.Errorf("build skill middleware: %w", err)
		}
		handlers = append(handlers, h)
	}

	if snap.Files == nil {
		return handlers, nil
	}

	// 2. filesystem：注册 ls / read_file / write_file / edit_file / glob / grep，
	//    Shell 非空时额外注册 execute。
	// 2. filesystem: registers ls / read_file / write_file / edit_file / glob / grep, plus
	//    execute when Shell is non-nil.
	fsHandler, err := fsmw.New(ctx, &fsmw.MiddlewareConfig{
		Backend: snap.Files,
		Shell:   snap.Shell,
	})
	if err != nil {
		return nil, fmt.Errorf("build filesystem middleware: %w", err)
	}
	handlers = append(handlers, fsHandler)

	// 3. plantask：给模型一组结构化任务管理工具，长任务里显著降低跑偏概率。
	// 3. plantask: gives the model structured task management tools, markedly reducing drift
	//    on long tasks.
	planHandler, err := plantask.New(ctx, &plantask.Config{
		Backend: planBackendAdapter{snap.Files},
		BaseDir: path.Join(snap.WorkDir, taskDirName),
	})
	if err != nil {
		return nil, fmt.Errorf("build plantask middleware: %w", err)
	}
	handlers = append(handlers, planHandler)

	// 4. reduction：可逆的工具结果卸载。必须在 filesystem 之后、summarization 之前。
	// 4. reduction: reversible tool-result offloading. Must sit after filesystem and before
	//    summarization.
	redHandler, err := reduction.New(ctx, &reduction.Config{
		Backend:           snap.Files,
		RootDir:           path.Join(snap.WorkDir, offloadDirName),
		ReadFileToolName:  "read_file",
		MaxLengthForTrunc: snap.Runtime.MaxLengthForTrunc,
		MaxTokensForClear: int64(snap.Runtime.MaxTokensForClear),
	})
	if err != nil {
		return nil, fmt.Errorf("build reduction middleware: %w", err)
	}
	handlers = append(handlers, redHandler)

	// 5. summarization：有损的历史摘要，整条链的最后一道。
	// 5. summarization: lossy history summarization, the last stage of the chain.
	if snap.SummaryModel != nil {
		sumHandler, err := summarization.New(ctx, &summarization.Config{
			Model:   snap.SummaryModel,
			Trigger: &summarization.TriggerCondition{ContextTokens: snap.Runtime.SummarizeTokens},
			// 打开内部事件，压缩发生时才能通过 SSE 的 compression 事件让用户看见。
			// Emitting internal events is what makes compression observable to the user via
			// the SSE compression event.
			EmitInternalEvents: true,
		})
		if err != nil {
			return nil, fmt.Errorf("build summarization middleware: %w", err)
		}
		handlers = append(handlers, sumHandler)
	}

	return handlers, nil
}

// planBackendAdapter 把 FileBackend 适配成 plantask.Backend。
// 两者只差一个 Delete 的参数类型，本包不直接依赖 plantask 的请求结构，故在此转接。
// planBackendAdapter adapts FileBackend to plantask.Backend. They differ only in the parameter
// type of Delete; this package avoids depending on plantask's request struct, hence the bridge.
type planBackendAdapter struct {
	fb FileBackend
}

func (a planBackendAdapter) LsInfo(ctx context.Context, req *plantask.LsInfoRequest) ([]plantask.FileInfo, error) {
	return a.fb.LsInfo(ctx, req)
}

func (a planBackendAdapter) Read(ctx context.Context, req *plantask.ReadRequest) (*filesystem.FileContent, error) {
	return a.fb.Read(ctx, req)
}

func (a planBackendAdapter) Write(ctx context.Context, req *plantask.WriteRequest) error {
	return a.fb.Write(ctx, req)
}

func (a planBackendAdapter) Delete(ctx context.Context, req *plantask.DeleteRequest) error {
	return a.fb.Delete(ctx, &DeleteRequest{FilePath: req.FilePath})
}
