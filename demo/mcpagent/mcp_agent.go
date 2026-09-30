// Package mcpagent 演示把远程 MCP 工具和本地 Go 工具挂在同一个 Agent 上。
// Package mcpagent shows a remote MCP tool and local Go tools mounted on one agent.
//
// 和 travelagent 的区别只有工具来源：mysql_query 来自 mcp-server-mysql（另一个进程、走 HTTP），
// get_weather / get_attraction 是本地 InferTool 造的。对 Agent 来说两者没有任何区别，
// 都是 tool.BaseTool，模型也只看得到名字、描述和入参 schema。
// The only difference from travelagent is where the tools come from: mysql_query comes from
// mcp-server-mysql (a separate process over HTTP), while get_weather / get_attraction are built
// locally with InferTool. The agent can't tell them apart — both are tool.BaseTool, and the model
// only ever sees a name, a description and a parameter schema.
package mcpagent

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"private/agent_basedon_eino/demo/tools"
	"private/agent_basedon_eino/internal/agentio"
	"private/agent_basedon_eino/internal/democli"
	"private/agent_basedon_eino/internal/llm"
	"private/agent_basedon_eino/internal/mcpclient"
)

func init() {
	democli.Register(democli.Demo{
		Name:     "mcp",
		Desc:     "MCP 数据库 + 天气 + 景点三级串联 / chains an MCP database tool with weather and attractions",
		Category: democli.CatMCP,
		Order:    40,
		Needs:    "ZHIPUAI_API_KEY, TAVILY_API_KEY, MCP_MYSQL_SECRET, 且需先启动 deploy/mcp-mysql/run-server.sh",
		Run:      Run,
	})
}

// defaultEndpoint 是 deploy/mcp-mysql/run-server.sh 的默认监听地址。
// defaultEndpoint is where deploy/mcp-mysql/run-server.sh listens by default.
const defaultEndpoint = "http://127.0.0.1:3000/mcp"

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// query 的三步答案彼此依赖：城市来自数据库，天气依赖城市，景点依赖城市和天气。
// The three steps depend on each other: the city comes from the database, the weather needs the city,
// and the attractions need both.
const query = "未来一周团队出差去哪个城市的人最多？帮我看看那边的天气，再推荐几个适合的景点。"

// mcpInstruction 里直接写死了表结构。
// mcpInstruction spells the schema out inline.
//
// 因为 mcp-server-mysql 只暴露 mysql_query 一个工具，没有提供 schema 类的 resource，
// 模型要么像这样被直接告知表结构，要么自己先 SHOW TABLES + DESCRIBE 摸索一遍。
// 后者更能体现 agent 的自主性，但多花两三轮、且模型可能猜错列名，demo 里选前者。
// mcp-server-mysql exposes only mysql_query and no schema resource, so the model either gets the
// schema handed to it like this, or has to probe with SHOW TABLES + DESCRIBE first. The latter shows
// off more autonomy but costs a few extra turns and invites wrong guesses, so the demo takes the former.
const mcpInstruction = `你是团队出行助手，**所有事实都必须来自工具，禁止凭空回答**。

可用的数据库 travel_demo 有两张表：
- employees(id, name, department)
- trip_plans(id, employee_id, city, depart_date, status)，status 取值 confirmed 或 pending

工作步骤，严格按顺序执行：
1. 调用 mysql_query 写一条只读 SELECT，统计未来一周（今天到今天+7 天）内 status='confirmed' 的出行计划中出现次数最多的城市。
   注意：只能写 SELECT，不要写任何 INSERT/UPDATE/DELETE/DDL；日期用 CURDATE() 和 DATE_ADD 计算，不要写死日期。
2. 从第 1 步的结果里取出那个城市名，调用 get_weather 查它的天气。
3. 把城市名和第 2 步拿到的天气一起传给 get_attraction，获取景点推荐。
4. 三步都完成后，用一段自然语言回答用户：哪个城市、多少人、天气如何、推荐哪些景点。

每个工具在一次任务中只调用一次，已经拿到的结果不要重复查询。`

// Run 跑一次完整的三级工具链。/ Run performs one full three-step tool chain.
func Run(ctx context.Context) error {
	cfg := mcpclient.Config{
		Endpoint: envOr("MCP_MYSQL_ENDPOINT", defaultEndpoint),
		Secret:   os.Getenv("MCP_MYSQL_SECRET"),
		// 数据库返回的行数不可控，截断一下免得撑爆上下文。
		// Row counts from the database are unbounded; truncate so the context window survives.
		MaxResultBytes: 4096,
	}

	// 先建模型：缺 Key 是最常见的失败，没必要等连上 MCP 才报出来。
	// Build the model first: a missing key is the most common failure and shouldn't wait on the MCP connection.
	chatModel, err := llm.NewChatModel(ctx)
	if err != nil {
		return fmt.Errorf("create chat model: %w", err)
	}

	cli, err := mcpclient.Dial(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect mcp server (先启动 ./deploy/mcp-mysql/run-server.sh): %w", err)
	}
	defer cli.Close()

	mcpTools, err := cli.Tools(ctx, cfg)
	if err != nil {
		return err
	}

	agent, err := newAgent(ctx, chatModel, mcpTools)
	if err != nil {
		return err
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})

	fmt.Println("=================================================")
	fmt.Printf("用户提问：%s\n\n", query)
	if err := agentio.Print(runner.Query(ctx, query), true); err != nil {
		return err
	}
	fmt.Println("\n=================================================")
	return nil
}

// newAgent 把远程工具和本地工具拼成同一份工具列表。
// newAgent merges the remote and local tools into a single tool list.
func newAgent(ctx context.Context, chatModel model.ToolCallingChatModel, mcpTools []tool.BaseTool) (adk.Agent, error) {
	weatherTool, err := tools.NewWeatherTool()
	if err != nil {
		return nil, fmt.Errorf("create weather tool: %w", err)
	}
	attractionTool, err := tools.NewAttractionTool()
	if err != nil {
		return nil, fmt.Errorf("create attraction tool: %w", err)
	}

	allTools := append([]tool.BaseTool{}, mcpTools...)
	allTools = append(allTools, weatherTool, attractionTool)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "trip_assistant",
		Description: "查询出行计划、天气与景点 / queries trip plans, weather and attractions",
		Instruction: mcpInstruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: allTools,
			},
		},
		// 三次工具调用加上纠错余量；SQL 写错时模型往往要重试一两次。
		// Three tool calls plus slack for retries; a bad SQL usually costs the model a round or two.
		MaxIterations: 12,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return agent, nil
}
