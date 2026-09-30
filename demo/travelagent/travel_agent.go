// Package travelagent 对应 LangGraph 的 agent_call_tools.py：带工具的旅行助手。
// Package travelagent mirrors agent_call_tools.py: a travel assistant that calls tools.
//
// LangGraph 版需要手工搭出 ReAct 环：chatbot 节点 → tools_condition 条件边 → ToolNode → 回到 chatbot。
// Eino 的 ChatModelAgent 内建了这个循环，配上 Tools 即可，不需要画图。
// The LangGraph version hand-builds the ReAct loop: chatbot node → tools_condition edge → ToolNode → back to chatbot.
// Eino's ChatModelAgent has that loop built in; you only supply the tools, no graph required.
package travelagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"private/agent_basedon_eino/demo/tools"
	"private/agent_basedon_eino/internal/agentio"
	"private/agent_basedon_eino/internal/democli"
	"private/agent_basedon_eino/internal/llm"
)

func init() {
	democli.Register(democli.Demo{
		Name:     "travel",
		Desc:     "带工具的旅行助手 / travel assistant with tools (agent_call_tools.py)",
		Category: democli.CatAgent,
		Order:    30,
		Needs:    "ZHIPUAI_API_KEY, TAVILY_API_KEY",
		Run:      Run,
	})
}

// travelInstruction 对应 Python 的 TRAVEL_AGENT_DESCRIPTION。
// travelInstruction mirrors TRAVEL_AGENT_DESCRIPTION in Python.
//
// 注意：Python 版定义了这段严格提示词，但节点里实际用的是宽松的 agent_description，
// 这是它反复重复调用 get_weather 的直接原因。这里用回严格版本。
// Note: the Python version defines this strict prompt but the node actually uses the loose agent_description,
// which is why it kept re-calling get_weather. Here the strict version is used.
const travelInstruction = `你是旅游查询助手，**必须通过工具获取数据，禁止凭空回答**。
规则：
1. 用户询问城市游玩景点，第一步必须调用 get_weather 查询目标城市天气；
2. 拿到天气结果后，必须调用 get_attraction(city, weather) 获取景点推荐；
3. 每个工具在一次任务中只调用一次，已经拿到的结果不要重复查询；
4. 只有所有工具执行完毕，拿到完整天气+景点信息后，再整理自然语言回答用户。`

// Run 执行一次旅行查询，对应 Python 的 on_demo3()。
// Run performs one travel query, mirroring on_demo3() in Python.
func Run(ctx context.Context) error {
	agent, err := newAgent(ctx)
	if err != nil {
		return err
	}

	// 这个 demo 关闭流式，好把 ReAct 循环的每一步完整打印出来。
	// Streaming is off here so the whole ReAct trace can be printed step by step.
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})

	fmt.Println("=================================================")
	if err := agentio.Print(runner.Query(ctx, "帮我查珠海今天有什么适合游玩的景点？"), true); err != nil {
		return err
	}
	fmt.Println("\n=================================================")
	return nil
}

// newAgent 组装一个带两个工具的 ChatModelAgent。
// newAgent assembles a ChatModelAgent with the two travel tools.
func newAgent(ctx context.Context) (adk.Agent, error) {
	chatModel, err := llm.NewChatModel(ctx)
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}

	weatherTool, err := tools.NewWeatherTool()
	if err != nil {
		return nil, fmt.Errorf("create weather tool: %w", err)
	}
	attractionTool, err := tools.NewAttractionTool()
	if err != nil {
		return nil, fmt.Errorf("create attraction tool: %w", err)
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "travel_agent",
		Description: "查询天气并推荐景点 / queries weather and recommends attractions",
		Instruction: travelInstruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{weatherTool, attractionTool},
			},
		},
		// 兜底防止模型在工具之间打转；超出后报错退出。
		// A backstop against the model spinning between tools; exceeding it exits with an error.
		MaxIterations: 8,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return agent, nil
}
