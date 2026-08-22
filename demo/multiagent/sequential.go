package multiagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"

	"private/agent_basedon_eino/internal/agentio"
	"private/agent_basedon_eino/internal/democli"
	"private/agent_basedon_eino/internal/llm"
)

func init() {
	democli.Register(democli.Demo{
		Name: "sequential",
		Desc: "SequentialAgent 版多智能体 / multi-agent via SequentialAgent (multi_agent_demo.py)",
		Run:  RunSequential,
	})
}

// RunSequential 用 SequentialAgent 模式跑一次多智能体协作。
// RunSequential runs the multi-agent collaboration in SequentialAgent mode.
//
// 和另外两版最直观的差别：这里没有协调者，也没有任何一句提示词在描述执行顺序。
// 顺序由 SubAgents 的切片下标决定，是代码写死的，模型无权更改。
// The most visible difference from the other two: there is no coordinator and not one line of prompt
// describing the order. The order is the SubAgents slice index — written in code, not up to the model.
func RunSequential(ctx context.Context) error {
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

	// 专家复用另外两版的定义，一个字都没改，说明差异纯粹来自编排方式。
	// The experts are reused verbatim from the other two versions, so the difference is purely orchestration.
	sequential, err := adk.NewSequentialAgent(ctx, &adk.SequentialAgentConfig{
		Name:        "travel_pipeline",
		Description: "先查天气再推荐景点的固定流程 / a fixed pipeline: weather first, attractions second",
		SubAgents:   []adk.Agent{weatherExpert, attractionExpert},
	})
	if err != nil {
		return fmt.Errorf("create sequential agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: sequential})

	fmt.Println("========== SequentialAgent 模式 / SequentialAgent mode ==========")
	if err := agentio.Print(runner.Query(ctx, userQuery), true); err != nil {
		return err
	}
	fmt.Println("\n===============================================================")
	return nil
}
