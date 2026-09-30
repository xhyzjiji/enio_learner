package multiagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"private/agent_basedon_eino/internal/agentio"
	"private/agent_basedon_eino/internal/democli"
	"private/agent_basedon_eino/internal/llm"
)

func init() {
	democli.Register(democli.Demo{
		Name:     "agenttool",
		Desc:     "AgentAsTool 版多智能体 / multi-agent via AgentAsTool (multi_agent_demo.py)",
		Category: democli.CatMultiAgent,
		Order:    60,
		Needs:    "ZHIPUAI_API_KEY, TAVILY_API_KEY",
		Run:      RunAgentTool,
	})
}

// coordinatorInstruction 是主 Agent 的提示词。
// coordinatorInstruction is the prompt of the main agent.
//
// 与 Supervisor 版最大的差别：子 Agent 默认拿不到父 Agent 的完整对话历史，
// 只收到父模型生成的那段任务描述，所以天气结果必须由主 Agent 显式转述给景点专家。
// The key difference from the supervisor version: a sub-agent does not inherit the parent's full history,
// it only receives the task description the parent model generates, so the weather must be passed on explicitly.
const coordinatorInstruction = `你是旅游查询助手，你自己不查数据，所有数据都必须通过调用下属专家获得。
可用的专家（以工具形式调用）：
- weather_expert: 查询某个城市的实时天气，入参是自然语言任务描述，需写明城市；
- attraction_expert: 结合城市和天气推荐景点，入参是自然语言任务描述，
  必须在描述里写清城市和天气，因为该专家看不到你和其他专家的对话。

工作流程：
1. 先调用 weather_expert 查询目标城市天气；
2. 拿到天气后调用 attraction_expert，并在任务描述中带上城市与刚查到的天气；
3. 两个专家都返回后，用中文整理成最终答复，包含天气情况、推荐景点和出行建议；
4. 每个专家只调用一次，不要重复调用。`

// RunAgentTool 用 AgentAsTool 模式跑一次多智能体协作。
// RunAgentTool runs the multi-agent collaboration in AgentAsTool mode.
func RunAgentTool(ctx context.Context) error {
	chatModel, err := llm.NewChatModel(ctx)
	if err != nil {
		return fmt.Errorf("create chat model: %w", err)
	}

	weatherExpert, err := newWeatherExpert(ctx, chatModel)
	if err != nil {
		return err
	}
	attractionExpert, err := newAttractionExpert(ctx, chatModel)
	if err != nil {
		return err
	}

	// 把子 Agent 包成 Tool；工具名与描述取自 Agent 的 Name / Description。
	// Wrap sub-agents as tools; the tool name and description come from the agent's Name / Description.
	coordinator, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "travel_coordinator",
		Description: "协调天气与景点专家完成旅游查询 / coordinates the weather and attraction experts",
		Instruction: coordinatorInstruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{
					adk.NewAgentTool(ctx, weatherExpert),
					adk.NewAgentTool(ctx, attractionExpert),
				},
			},
			// 打开后子 Agent 的事件会实时并入主事件流，能看到子 Agent 的中间过程。
			// With this on, sub-agent events flow into the main stream so their intermediate steps are visible.
			EmitInternalEvents: true,
		},
		MaxIterations: 8,
	})
	if err != nil {
		return fmt.Errorf("create coordinator: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: coordinator})

	fmt.Println("========== AgentAsTool 模式 / AgentAsTool mode ==========")
	if err := agentio.Print(runner.Query(ctx, userQuery), true); err != nil {
		return err
	}
	fmt.Println("\n=======================================================")
	return nil
}
