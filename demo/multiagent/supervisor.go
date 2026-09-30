package multiagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/supervisor"

	"private/agent_basedon_eino/internal/agentio"
	"private/agent_basedon_eino/internal/democli"
	"private/agent_basedon_eino/internal/llm"
)

func init() {
	democli.Register(democli.Demo{
		Name:     "supervisor",
		Desc:     "Supervisor 版多智能体 / multi-agent via supervisor (multi_agent_demo.py)",
		Category: democli.CatMultiAgent,
		Order:    50,
		Needs:    "ZHIPUAI_API_KEY, TAVILY_API_KEY",
		Run:      RunSupervisor,
	})
}

// supervisorInstruction 对应 Python 的 ROUTER_SYSTEM_PROMPT。
// supervisorInstruction mirrors ROUTER_SYSTEM_PROMPT in Python.
//
// 差别在于：Python 版要求路由输出一个 JSON 再由代码解析并跳转；
// ADK 里转交是框架能力，调度器直接调用内置的 transfer 工具即可，不用自己定协议。
// The difference: the Python router emits JSON that the code parses to jump;
// in ADK, transfer is a framework capability invoked through a built-in tool, so no hand-rolled protocol is needed.
const supervisorInstruction = `你是一个多智能体系统的调度器，负责决定下一步由哪个专家来处理用户的问题。
可选的专家：
- weather_expert: 天气专家，只能查询某个城市的实时天气；
- attraction_expert: 景点推荐专家，必须依赖城市 + 天气才能推荐景点。

调度规则：
1. 用户问“某城市有什么推荐景点”时，必须先转交 weather_expert 拿到天气，再转交 attraction_expert 推荐景点；
2. 如果对话中还没有天气结果，且需要推荐景点，则转交 weather_expert；
3. 如果已经有天气结果、但还没有景点推荐，则转交 attraction_expert；
4. 如果用户只是问天气且天气已经查到，或者景点推荐结果已经存在，就不要再转交，
   直接基于已有信息用中文整理出最终答复给用户。`

// RunSupervisor 用 Supervisor 模式跑一次多智能体协作。
// RunSupervisor runs the multi-agent collaboration in supervisor mode.
func RunSupervisor(ctx context.Context) error {
	chatModel, err := llm.NewChatModel(ctx)
	if err != nil {
		return fmt.Errorf("create chat model: %w", err)
	}

	// 调度器自己不带业务工具，它的“工具”就是转交给谁。
	// The supervisor carries no business tools; its only action is choosing whom to transfer to.
	router, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "router",
		Description: "调度器，决定由哪个专家处理 / dispatcher deciding which expert handles the request",
		Instruction: supervisorInstruction,
		Model:       chatModel,
	})
	if err != nil {
		return fmt.Errorf("create router: %w", err)
	}

	weatherExpert, err := newWeatherExpert(ctx, chatModel)
	if err != nil {
		return err
	}
	attractionExpert, err := newAttractionExpert(ctx, chatModel)
	if err != nil {
		return err
	}

	// supervisor.New 会给调度器装上转交能力，并让每个专家在结束后自动转回调度器。
	// 这正是 Python 版那两条 add_edge("xxx_expert", "router") 干的事。
	// supervisor.New equips the router with transfer capability and makes each expert transfer back when done,
	// which is exactly what the two add_edge("xxx_expert", "router") lines do in the Python version.
	root, err := supervisor.New(ctx, &supervisor.Config{
		Supervisor: router,
		SubAgents:  []adk.Agent{weatherExpert, attractionExpert},
	})
	if err != nil {
		return fmt.Errorf("create supervisor: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: root})

	fmt.Println("========== Supervisor 模式 / supervisor mode ==========")
	if err := agentio.Print(runner.Query(ctx, userQuery), true); err != nil {
		return err
	}
	fmt.Println("\n=====================================================")
	return nil
}
