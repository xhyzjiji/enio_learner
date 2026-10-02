// Package kernel 负责把一份配置快照装配成一个可运行的 Agent 实例。
//
// 本包的核心决策是**会话级 Agent 工厂**：每次对话按当前配置构造一个新实例，
// 而不是维护一个全局单例在配置变更时加写锁重建。
//
// 理由是 ChatModelAgent 的工具集在构造后不可变，而页面要求增删 MCP server、CLI 工具、
// 技能之后即时生效。走全局单例重建的话，重建那一刻若有多个对话正在跑，旧实例与新配置
// 的归属就乱了——事件流已经建立却被换掉工具集。这类 bug 只在"边用边改配置"时偶发，
// 复现极难。而构造一个实例是纯内存操作，在微秒量级，相对一次数百毫秒的模型网络调用
// 完全可以忽略，为省掉它而引入竞态是典型的错误优化。
//
// 快照法还白送一个能力：不同会话可以用不同的模型与工具子集。定时任务的受限工具集
// 正是靠这一点实现的。
//
// Package kernel assembles a runnable agent instance from a configuration snapshot.
//
// Its central decision is the SESSION-LEVEL AGENT FACTORY: build a fresh instance per
// conversation from the current configuration, rather than keeping a global singleton and
// rebuilding it under a write lock whenever configuration changes.
//
// The reason is that a ChatModelAgent's tool set is fixed at construction, while the UI
// requires added/removed MCP servers, CLI tools and skills to take effect immediately. With a
// rebuilt singleton, any conversations in flight at rebuild time end up with confused ownership
// between the old instance and the new configuration — an event stream already established yet
// its tool set swapped underneath. Such bugs only appear when configuration is edited during
// use and are extremely hard to reproduce. Constructing an instance, by contrast, is pure
// in-memory work in the microsecond range, negligible against a model round trip of hundreds of
// milliseconds; trading a race for that saving is a textbook false optimization.
//
// The snapshot approach also yields a capability for free: different conversations can use
// different models and tool subsets. The restricted tool set for scheduled tasks relies on it.
package kernel

import (
	"context"

	"github.com/cloudwego/eino/adk/filesystem"
	skillmw "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"

	"private/agent_basedon_eino/internal/agent/config"
)

// FileBackend 是文件后端需要满足的完整能力集。
//
// T005 实测发现 filesystem、reduction、plantask 三个中间件都需要一个"文件形状"的后端，
// 而且**必须是同一个实例**：reduction 把大工具结果卸载成一个文件路径后，模型是靠
// filesystem 提供的 read_file 工具去取回的，两者用不同后端就会取不到。
//
// FileBackend is the full capability set a file backend must provide.
//
// Gate T005 established that the filesystem, reduction and plantask middlewares all need a
// file-shaped backend, and that it MUST be the same instance: reduction offloads a large tool
// result to a file path, and the model retrieves it through the read_file tool provided by the
// filesystem middleware, so different backends would make retrieval impossible.
type FileBackend interface {
	filesystem.Backend
	// Delete 仅 plantask 需要，filesystem.Backend 本身不含它。
	// Delete is required only by plantask; filesystem.Backend itself does not include it.
	Delete(ctx context.Context, req *DeleteRequest) error
}

// DeleteRequest 与 plantask.DeleteRequest 结构一致，在此重声明以免本包直接依赖该中间件。
// DeleteRequest mirrors plantask.DeleteRequest, redeclared here so this package need not
// depend on that middleware directly.
type DeleteRequest struct {
	FilePath string
}

// Snapshot 是构建一个 Agent 实例所需配置的不可变快照。
//
// 一旦交给 Build，就不应再被修改——它的全部意义就在于"这次对话看到的配置是定格的"。
//
// Snapshot is an immutable snapshot of everything needed to build one agent instance.
//
// Once handed to Build it must not be mutated: its entire purpose is that the configuration
// seen by one conversation is frozen.
type Snapshot struct {
	// SessionID 标识这次对话。定时任务的独立上下文模式下可以为空。
	// SessionID identifies the conversation. It may be empty for scheduled tasks running in
	// an isolated context.
	SessionID string

	// Model 是本次对话使用的模型实例。
	// Model is the model instance used by this conversation.
	Model model.ToolCallingChatModel

	// Instruction 是组装完成的系统提示词，已包含长期记忆注入部分。
	// Instruction is the fully assembled system prompt, memory injection included.
	Instruction string

	// Tools 是业务工具集合：MCP 工具、声明式 CLI 工具、search_docs、remember、
	// schedule_task 等。filesystem / plantask / skill 提供的工具由中间件自行注册，
	// 不在这里。
	// Tools holds the business tools: MCP tools, declarative CLI tools, search_docs, remember,
	// schedule_task and so on. Tools contributed by the filesystem, plantask and skill
	// middlewares are registered by those middlewares and do not belong here.
	Tools []tool.BaseTool

	// SkillBackend 为 skill 中间件提供技能目录。为 nil 时不挂载该中间件。
	// SkillBackend supplies the skill catalog. When nil the middleware is not mounted.
	SkillBackend skillmw.Backend

	// SkillAgentHub 为 fork / fork_with_context 模式提供子 Agent。
	// SkillAgentHub supplies sub-agents for fork / fork_with_context modes.
	SkillAgentHub skillmw.AgentHub

	// Files 是三个中间件共用的文件后端。为 nil 时不挂载 filesystem / reduction / plantask。
	// Files is the file backend shared by three middlewares. When nil, the filesystem,
	// reduction and plantask middlewares are all skipped.
	Files FileBackend

	// Shell 提供 execute 自由执行工具。为 nil 时该工具不会出现在工具清单里。
	// 这是"默认关闭"的实现方式：不是注册后再拒绝，而是压根不注册。
	// Shell backs the free-form execute tool. When nil the tool never appears in the tool list.
	// This is how "off by default" is implemented: not registered and then refused, but never
	// registered at all.
	Shell filesystem.Shell

	// SummaryModel 是 summarization 中间件使用的模型，通常与 Model 相同。
	// SummaryModel is used by the summarization middleware, usually the same as Model.
	SummaryModel model.BaseChatModel

	// WorkDir 是文件工具的工作根目录，也是卸载内容与任务文件的落点前缀。
	// WorkDir is the root directory for file tools and the prefix for offloaded content and
	// task files.
	WorkDir string

	// Runtime 是本次对话定格的运行时配置。
	// Runtime is the runtime configuration frozen for this conversation.
	Runtime config.Runtime

	// MaxIterations 限制模型生成循环次数，兜住模型在工具之间打转。
	// MaxIterations bounds the model generation loop, guarding against the model spinning
	// between tools.
	MaxIterations int
}
