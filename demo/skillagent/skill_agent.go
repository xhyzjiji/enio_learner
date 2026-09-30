// Package skillagent 是一个实战案例：技能驱动的 Git 仓库分析助手。
// Package skillagent is a hands-on case study: a skill-driven Git repository analyst.
//
// 它把三样东西拼在一起：
//   - 自研的 MCP server（internal/gitmcp）提供 6 个只读的 Git 工具，跑在独立进程里；
//   - 技能的渐进披露（internal/skill）决定「什么时候该按什么步骤用这些工具」；
//   - ChatModelAgent 负责把两者串成一次完整的 ReAct 执行。
//
// It combines three things:
//   - a self-hosted MCP server (internal/gitmcp) offering six read-only Git tools in its own process;
//   - progressive disclosure of skills (internal/skill) dictating *when* and *in what order* to use them;
//   - a ChatModelAgent tying both into one ReAct run.
//
// 和 demo/mcpagent 的关键差别：mcpagent 把执行步骤硬编码在 instruction 里，加一个新任务就得改提示词；
// 这里 instruction 只有通用的工作流，具体步骤放在技能文件里，加能力 = 加一个 SKILL.md，不动 Go 代码。
// The key difference from demo/mcpagent: that one hard-codes its steps into the instruction, so a new task
// means editing the prompt. Here the instruction holds only a generic workflow and the steps live in skill
// files — adding a capability means adding a SKILL.md, with no Go code touched.
package skillagent

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"private/agent_basedon_eino/internal/agentio"
	"private/agent_basedon_eino/internal/democli"
	"private/agent_basedon_eino/internal/llm"
	"private/agent_basedon_eino/internal/mcpclient"
	"private/agent_basedon_eino/internal/skill"
)

// 技能编进二进制，这样 go run ./cmd/skillagent 在任何工作目录下都能找到它们。
// Skills are embedded so `go run ./cmd/skillagent` finds them from any working directory.
//
//go:embed skills
var skillFS embed.FS

const (
	defaultEndpoint = "http://127.0.0.1:8080/mcp"
	secretEnv       = "MCP_GIT_SECRET"
)

func init() {
	democli.Register(democli.Demo{
		Name:     "skillagent",
		Desc:     "技能驱动的 Git 仓库分析助手 / skill-driven Git repository analyst",
		Category: democli.CatSkill,
		Order:    45,
		Needs:    "ZHIPUAI_API_KEY, MCP_GIT_SECRET, 且需先启动 go run ./cmd/gitmcp",
		Run:      Run,
	})
}

// query 故意不点名任何技能，就是要看模型能不能只凭描述自己选对。
// The query deliberately names no skill — the whole point is whether the model picks the right one
// from the descriptions alone.
const query = "帮我熟悉一下这个仓库，它是干什么的、代码怎么组织的？"

// baseInstruction 只描述通用工作流，不含任何具体任务的步骤。
// baseInstruction describes only the generic workflow, with no task-specific steps.
//
// 具体步骤全部来自技能文件，这是「加能力不改代码」的前提。
// Every concrete step comes from a skill file; that is what makes "add capability without touching code" work.
const baseInstruction = `你是 Git 仓库分析助手，通过工具读取真实的仓库数据来回答问题。

你具备一组「技能」。每个技能描述了某一类任务该按什么步骤、用哪些工具完成。

工作流程：
1. 读下面的可用技能清单，判断哪一个最匹配用户的问题；
2. 调用 load_skill 加载那个技能，拿到它的完整执行说明；
3. 严格按照技能里写的步骤和工具顺序执行，技能说不要用的工具就不要用；
4. 按技能规定的输出格式组织最终答复。

硬性要求：
- 所有结论必须来自工具返回的真实数据，禁止凭空编造文件名、行号、作者或提交记录；
- 一次任务只加载一个技能，不要把多个技能混着执行；
- 如果没有任何技能匹配，直接用通用工具回答，并说明你没有走技能流程。

可用技能：
%s`

// Run 跑一次技能驱动的仓库分析。/ Run performs one skill-driven repository analysis.
func Run(ctx context.Context) error {
	// 技能加载放最前面：它是纯本地的、瞬时的，而 SKILL.md 的前言格式最容易写错。
	// 排在这里，改技能文件的人不配任何凭证也能立刻看到解析报错。
	// Skill loading comes first: it is local and instant, and SKILL.md front matter is the easiest thing
	// to get wrong. Putting it here surfaces parse errors immediately, with no credentials configured.
	registry, loadSkillTool, err := loadSkills()
	if err != nil {
		return err
	}

	chatModel, err := llm.NewChatModel(ctx)
	if err != nil {
		return fmt.Errorf("create chat model: %w", err)
	}

	cfg := mcpclient.Config{
		Endpoint: envOr("MCP_GIT_ENDPOINT", defaultEndpoint),
		Secret:   os.Getenv(secretEnv),
		// git log / diff 的输出长度不可控，必须设上限。
		// Output from git log / diff is unbounded, so a cap is mandatory.
		MaxResultBytes: 6000,
	}

	cli, err := mcpclient.Dial(ctx, cfg)
	if err != nil {
		return fmt.Errorf("连接 Git MCP server 失败（先运行 go run ./cmd/gitmcp）: %w", err)
	}
	defer cli.Close()

	gitTools, err := cli.Tools(ctx, cfg)
	if err != nil {
		return err
	}

	agent, err := newAgent(ctx, chatModel, registry, append(gitTools, loadSkillTool))
	if err != nil {
		return err
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})

	fmt.Println("=================================================")
	fmt.Printf("已加载技能：%v\n", registry.Names())
	fmt.Printf("用户提问：%s\n\n", query)
	if err := agentio.Print(runner.Query(ctx, query), true); err != nil {
		return err
	}
	fmt.Println("\n=================================================")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadSkills() (*skill.Registry, tool.InvokableTool, error) {
	// embed 的路径带 skills/ 前缀，Sub 一下让技能名直接对应目录名。
	// The embedded paths carry a skills/ prefix; Sub strips it so a skill name maps to its directory name.
	sub, err := fs.Sub(skillFS, "skills")
	if err != nil {
		return nil, nil, fmt.Errorf("open embedded skills: %w", err)
	}

	registry, err := skill.Load(sub)
	if err != nil {
		return nil, nil, fmt.Errorf("load skills: %w", err)
	}

	loadSkillTool, err := registry.NewLoadSkillTool()
	if err != nil {
		return nil, nil, fmt.Errorf("create load_skill tool: %w", err)
	}
	return registry, loadSkillTool, nil
}

func newAgent(ctx context.Context, chatModel model.ToolCallingChatModel, registry *skill.Registry, tools []tool.BaseTool) (adk.Agent, error) {
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "git_analyst",
		Description: "技能驱动的 Git 仓库分析助手 / skill-driven Git repository analyst",
		// 技能清单在这里被拼进系统提示词，这就是渐进披露的第一级。
		// The catalog is spliced into the system prompt here — that is tier one of progressive disclosure.
		Instruction: fmt.Sprintf(baseInstruction, registry.Catalog()),
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
		// 一次 load_skill 加技能里最多五六步工具，再留出纠错余量。
		// One load_skill plus up to half a dozen steps from the skill, with slack for retries.
		MaxIterations: 15,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return agent, nil
}
