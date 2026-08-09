// Package multiagent 对应 LangGraph 的 multi_agent_demo.py：router + 天气专家 + 景点专家。
// Package multiagent mirrors multi_agent_demo.py: a router plus a weather expert and an attraction expert.
//
// 同一个需求这里给了两种 ADK 实现，可以直接对比：
//   - supervisor.go：Supervisor 模式，调度器把任务转交给专家，专家做完转回调度器
//   - agenttool.go： AgentAsTool 模式，专家被包成 Tool，由主 Agent 自主决定何时调用
//
// Two ADK implementations of the same requirement are provided side by side:
//   - supervisor.go: the supervisor transfers work to experts, experts transfer back
//   - agenttool.go:  experts are wrapped as tools, the main agent decides when to call them
package multiagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"private/agent_basedon_eino/demo/tools"
)

// 两个专家的名字，Supervisor 转交和 AgentTool 命名都依赖它们。
// Names of the two experts; both agent transfer and agent-tool naming depend on them.
const (
	weatherExpertName    = "weather_expert"
	attractionExpertName = "attraction_expert"
)

// userQuery 与 Python 版保持一致，方便两边对照输出。
// userQuery matches the Python version so both sides can be compared directly.
const userQuery = "帮我查江门今天有什么适合游玩的三个景点？"

// weatherExpertInstruction 对应 Python 的 WEATHER_EXPERT_PROMPT。
// weatherExpertInstruction mirrors WEATHER_EXPERT_PROMPT in Python.
const weatherExpertInstruction = `你是天气专家。你只有一个工具 get_weather(city)，用于查询城市的实时天气。
规则：
1. 必须先调用 get_weather 工具获取真实天气，禁止凭空编造；
2. 拿到工具结果后，用自然、口语化的中文把天气信息润色成一句话；
3. 不要推荐景点，那不是你的职责；
4. 同一个城市只查一次，不要重复调用工具。`

// attractionExpertInstruction 对应 Python 的 ATTRACTION_EXPERT_PROMPT。
// attractionExpertInstruction mirrors ATTRACTION_EXPERT_PROMPT in Python.
const attractionExpertInstruction = `你是景点推荐专家。你只有一个工具 get_attraction(city, weather)，用于结合城市与天气搜索景点。
规则：
1. 必须调用 get_attraction 工具，参数 city 和 weather 都要传，weather 使用天气专家已经查到的结果；
2. 工具返回的内容可能是英文或比较零散，你需要把它润色成条理清晰的中文推荐；
3. 结合天气给出实用的出行建议（例如下雨优先室内景点）；
4. 只调用一次工具，拿到结果就整理成最终答复。`

// newWeatherExpert 构造只绑定 get_weather 的专家。
// newWeatherExpert builds the expert bound to get_weather only.
//
// Python 版特意让每个专家只 bind 自己那一个工具，避免互相干扰；这里保持同样的做法。
// The Python version deliberately binds one tool per expert to avoid interference; the same holds here.
func newWeatherExpert(ctx context.Context, chatModel model.ToolCallingChatModel) (adk.Agent, error) {
	weatherTool, err := tools.NewWeatherTool()
	if err != nil {
		return nil, fmt.Errorf("create weather tool: %w", err)
	}
	return newExpert(ctx, expertConfig{
		name:        weatherExpertName,
		description: "查询指定城市的实时天气 / queries the live weather of a city",
		instruction: weatherExpertInstruction,
		chatModel:   chatModel,
		tools:       []tool.BaseTool{weatherTool},
	})
}

// newAttractionExpert 构造只绑定 get_attraction 的专家。
// newAttractionExpert builds the expert bound to get_attraction only.
func newAttractionExpert(ctx context.Context, chatModel model.ToolCallingChatModel) (adk.Agent, error) {
	attractionTool, err := tools.NewAttractionTool()
	if err != nil {
		return nil, fmt.Errorf("create attraction tool: %w", err)
	}
	return newExpert(ctx, expertConfig{
		name:        attractionExpertName,
		description: "结合城市与天气推荐旅游景点，调用前必须已知天气 / recommends attractions for a city, requires the weather to be known",
		instruction: attractionExpertInstruction,
		chatModel:   chatModel,
		tools:       []tool.BaseTool{attractionTool},
	})
}

type expertConfig struct {
	name        string
	description string
	instruction string
	chatModel   model.ToolCallingChatModel
	tools       []tool.BaseTool
}

func newExpert(ctx context.Context, cfg expertConfig) (adk.Agent, error) {
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: cfg.name,
		// Description 不是可选的：Supervisor 靠它决定转交给谁，AgentTool 靠它生成工具描述。
		// Description is not optional: the supervisor routes on it, and AgentTool derives the tool description from it.
		Description: cfg.description,
		Instruction: cfg.instruction,
		Model:       cfg.chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: cfg.tools},
		},
		MaxIterations: 6,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent %s: %w", cfg.name, err)
	}
	return agent, nil
}
