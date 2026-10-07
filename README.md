# Eino ADK 学习工程

把一组 LangGraph(Python)demo 逐个改写成 [Eino](https://github.com/cloudwego/eino) ADK(Go)实现,用来对照两个框架的差异、并系统性地过一遍 Eino 的核心能力。

每个 demo 都能独立运行,也能通过统一分发器运行。

```bash
go run ./cmd/demo            # 按分类列出全部 demo
go run ./cmd/demo dag        # 运行指定 demo
go run ./cmd/dag             # 等价的独立入口
```

## 这里还有一个不是 demo 的东西

`cmd/agent` 是一个可以长期使用的**通用 Agent 运行时**,带 Web 界面、SQLite 持久化、本地文档 RAG、MCP 与 CLI 工具、技能系统、定时任务和长期记忆。它和下面的 demo 没有任何代码共享,环境变量也不通用——demo 读 `ZHIPUAI_BASE_URL`,它读 `AGENT_BASE_URL`。

启动方式、本地模型配置、前端开发与构建,见 **[cmd/agent/README.md](cmd/agent/README.md)**。

```bash
export AGENT_BASE_URL=http://localhost:11434/v1   # 用本地 Ollama，免密钥
go run ./cmd/agent                                # 打开 http://127.0.0.1:8090
```

本文档余下部分都是在讲 demo。

## 快速开始

`dag` 不需要任何配置,可以直接跑,建议从它开始。其余 demo 需要模型和搜索的凭证:

```bash
export ZHIPUAI_API_KEY=<智谱开放平台 Key>
export TAVILY_API_KEY=<Tavily 搜索 Key>
```

模型默认走智谱 GLM 的 OpenAI 兼容端点,可用 `ZHIPUAI_BASE_URL` 和 `ZHIPUAI_MODEL` 覆盖(见 `internal/llm/model.go`)。

`mcp` demo 额外依赖一个本地 MCP server,见下方[MCP server 部署](#mcp-server-部署)。

## Demo 分类与学习路径

分类由代码驱动(`internal/democli` 的 `Category` / `Order` 字段),`go run ./cmd/demo` 的输出与下表始终一致。建议按顺序学。

### 0. 编排底座 —— 不涉及 LLM

| demo | 代码 | 前置 |
|---|---|---|
| `dag` | `demo/fruitdag/` | 无 |

一条纯确定性的水果结账流水线,用 `compose.Chain` 把三个 Lambda 节点串起来,全程没有模型参与。

放在第一个是因为它能让你在没有模型不确定性干扰的情况下,先看清 Eino 的编排模型。**和 LangGraph 最本质的差异也在这里**:LangGraph 用一个可变的 State 字典贯穿所有节点,类型错误要到运行时才炸;Eino 让每个节点声明自己的输入输出类型,相邻节点能不能接上由编译器校验。

### 1. 单 Agent

| demo | 代码 | 前置 |
|---|---|---|
| `chatbot` | `demo/chatbot/` | `ZHIPUAI_API_KEY` |
| `travel` | `demo/travelagent/` | `ZHIPUAI_API_KEY`, `TAVILY_API_KEY` |

`chatbot` 是最小的 Agent:`ChatModelAgent` 不配 `ToolsConfig` 时会退化成一次普通的模型调用。它顺带演示了 `Runner` 这个统一执行入口和 `EnableStreaming` 流式输出。对照 LangGraph 需要 `StateGraph` → `add_node` → `add_edge` → `compile` 四步建图,这里一个结构体就够了。

`travel` 在此基础上挂上两个本地工具,ReAct 循环才真正跑起来。重点看两处:`utils.InferTool` 如何从一个普通 Go 函数生成 LLM 需要的 JSON Schema,以及 `MaxIterations` 作为防止模型在工具间打转的护栏。

### 2. 外部工具集成

| demo | 代码 | 前置 |
|---|---|---|
| `mcp` | `demo/mcpagent/` | 上面两个 Key + `MCP_MYSQL_SECRET` + 运行中的 MCP server |

把远程 MCP 工具(`mysql_query`,来自另一个进程)和本地工具(`get_weather` / `get_attraction`)挂在同一个 Agent 上。

核心认知是:**两者拼进同一个 `[]tool.BaseTool` 之后,Agent 完全分辨不出区别**,模型看到的永远只是名字、描述和入参 schema。demo 的问题设计成三级数据依赖(查库拿城市 → 查天气 → 查景点),后一步的入参必须来自前一步的输出,模型没法跳步。

### 3. 技能驱动 —— 实战案例

| demo | 代码 | 前置 |
|---|---|---|
| `skillagent` | `demo/skillagent/` | `ZHIPUAI_API_KEY` + `MCP_GIT_SECRET` + 运行中的 `cmd/gitmcp` |

一个技能驱动的 Git 仓库分析助手,是工程里唯一一个**自研 MCP server**(而不是接入别人的)的案例。三块拼在一起:

- `internal/gitmcp` 用 Go 实现 MCP server,暴露 6 个只读 Git 工具(`git_log` / `git_diff` / `git_blame` / `search_code` / `read_file` / `list_files`),跑在独立进程里;
- `internal/skill` 实现技能的**渐进披露**;
- `demo/skillagent` 用 `ChatModelAgent` 把两者串成一次 ReAct 执行。

它分析的就是这个工程本身,所以模型读到的提交、代码、作者都是真的,结论可以直接用 `git` 命令核对。

```bash
# 终端 1
export MCP_GIT_SECRET=$(openssl rand -hex 24)
go run ./cmd/gitmcp

# 终端 2
export ZHIPUAI_API_KEY=... MCP_GIT_SECRET=<和上面一致>
go run ./cmd/demo skillagent
```

#### 渐进披露:为什么不把技能全塞进提示词

如果把所有技能的完整说明都拼进 system prompt,技能一多就会撑爆上下文,而且绝大部分内容和当前提问无关,反而干扰模型。所以分成两级:

- **第一级**:只把「名字 + 一句话描述」常驻 system prompt,便宜,够模型做选择;
- **第二级**:模型判断某个技能匹配后,调用 `load_skill` 工具把完整正文取回来。

这决定了技能文件的写法:**`description` 要写清楚「什么时候该用它」**,因为那是模型选择时唯一能看到的信息;正文才写「具体怎么做」。

#### 和 `mcp` demo 的关键差别

`mcp` demo 把执行步骤硬编码在 `Instruction` 里,加一个新任务就得改提示词、重新编译。`skillagent` 的 `Instruction` 只有通用工作流(选技能 → 加载 → 按步骤执行),具体步骤全在技能文件里,**加能力 = 加一个 `SKILL.md`,不动任何 Go 代码**。

现有三个技能刻意设计成工具组合互不相同,用来验证描述真的在驱动工具选择:

| 技能 | 主要工具 | 明确禁止的工具 |
|---|---|---|
| `repo-overview` | `list_files` → `read_file` → `git_log` | 不做地毯式 `search_code` |
| `locate-feature` | `search_code` → `read_file` →(可选)`git_blame` | 不用 `list_files` 通读全仓库 |
| `release-notes` | `git_log` → `git_diff` | 不用 `read_file` / `search_code` |

demo 的提问(「帮我熟悉一下这个仓库」)**故意不点名任何技能**,就是要看模型能否只凭描述选对。跑的时候开了 verbose,可以从 `[tool call]` 轨迹里看到它先调 `load_skill`、再按技能规定的顺序调工具。

#### 自研 MCP server 值得注意的几处

- **全部走 `git` 子命令**,因此只能看到被 Git 跟踪的文件。这既省掉外部依赖,也顺手划出一条安全边界——`.git`、构建产物、被忽略的目录天然不可见。
- **路径穿越防护不是可选的**。模型完全可能传 `../../../../etc/passwd`,`safeRelPath` 会把它挡掉。这条已经实测过。
- **参数以数组形式传给 `exec.CommandContext`**,不拼 shell 字符串,所以模型传进来的内容不会被当成 shell 语法执行。
- **工具失败返回 `mcp.NewToolResultError` 而不是 Go error**,让模型看到失败原因、自己改参数重试,而不是把整条 Agent 链路打断。

### 4. 多智能体编排

| demo | 代码 | 前置 |
|---|---|---|
| `supervisor` | `demo/multiagent/supervisor.go` | `ZHIPUAI_API_KEY`, `TAVILY_API_KEY` |
| `agenttool` | `demo/multiagent/agenttool.go` | 同上 |
| `sequential` | `demo/multiagent/sequential.go` | 同上 |

这三个是**唯一一组设计成横向对照的 demo**:同一个需求、三种编排方式,专家定义(`demo/multiagent/experts.go`)三者原封不动复用,所以一切差异都纯粹来自编排本身。

|  | 顺序由谁决定 | 专家能否看到彼此输出 | 额外模型开销 |
|---|---|---|---|
| `supervisor` | 模型 | 能(共享上下文) | 每次转交都要决策一次 |
| `agenttool` | 模型 | **不能**(仅入参与返回值) | 协调者要复述数据 |
| `sequential` | 代码 | 能(共享上下文) | 无 |

景点专家依赖天气专家的结果,这个依赖在三版里的落地方式完全不同:`supervisor` 和 `sequential` 靠共享上下文自然可见,`agenttool` 则必须由协调者把天气写进任务描述转述过去——这就是它的提示词比另外两版啰嗦得多的原因。

两个容易被忽略的点:

- adk 给 `supervisor` 和 workflow agent(含 `SequentialAgent`)都标了 `NOT RECOMMENDED`,理由是二者都建立在 agent transfer + 全量上下文共享之上,实测效果未必更好。需要真正确定性的编排时,`compose` 层的 `Graph` / `Chain` 更干净,也就是 `dag` 那种写法。
- `AgentAsTool` 隔离的只是**对话历史**,不隔离 session values。子 agent 和父 agent 共享同一份 `Values` map,所以 `adk.AddSessionValue` / `adk.GetSessionValue` 是一条绕过所有模型上下文的数据旁路,适合传递大 payload。

## 目录结构

```
cmd/                每个子目录一个可执行入口,内容都只有十几行壳
  demo/             统一分发器,go run ./cmd/demo <name>
  gitmcp/           自研 Git 分析 MCP server 的启动入口(不是 demo)
  mcpprobe/         MCP 调试探针(不是 demo,见下)
demo/               demo 的实际实现
  tools/            共享的本地工具:get_weather / get_attraction
  skillagent/skills/  技能文件,每个目录一个 SKILL.md
internal/
  llm/              ChatModel 工厂,统一读环境变量
  agentio/          消费 ADK 事件流并打印,verbose 模式可看完整 ReAct 轨迹
  democli/          demo 注册表与分类,分发器的输出由它驱动
  mcpclient/        MCP 客户端:连接、握手,把远程工具转成 Eino Tool
  gitmcp/           MCP 服务端:6 个只读 Git 工具的实现
  skill/            技能的加载与渐进披露
deploy/mcp-mysql/   第三方 MySQL MCP server 的部署脚本与演示数据
```

工程里有**两个 MCP server,方向相反**,可以对照着看:`deploy/mcp-mysql` 是接入别人写的(TypeScript,需要单独克隆和编译),`internal/gitmcp` 是自己实现的(Go,和主工程同一个 module,`go run ./cmd/gitmcp` 直接起)。

`cmd/` 下平铺是 Go 的标准做法(一个目录 = 一个二进制),分类信息不体现在目录层级上,而是在 `internal/democli` 里,通过 `go run ./cmd/demo` 呈现。

### mcpprobe 不是 demo

`cmd/mcpprobe` 是调试工具,不碰 LLM。它的用途是把「MCP 通不通」和「Agent 好不好用」分成两件事排查——demo 行为异常时,先跑它就能知道问题在哪一层。两个 MCP server 都能探,靠 `-endpoint` 和 `-secret-env` 指定。

```bash
# 探 mcp-mysql（默认端点）
go run ./cmd/mcpprobe
go run ./cmd/mcpprobe -v                 # 额外打印每个工具的入参 schema
go run ./cmd/mcpprobe -call mysql_query -args '{"sql":"SHOW TABLES"}'
go run ./cmd/mcpprobe -call mysql_query -max 200 -args '...'   # 验证截断逻辑

# 探自研的 gitmcp
export MCP_GIT_SECRET=...
go run ./cmd/mcpprobe -endpoint http://127.0.0.1:8080/mcp -secret-env MCP_GIT_SECRET
go run ./cmd/mcpprobe -endpoint http://127.0.0.1:8080/mcp -secret-env MCP_GIT_SECRET \
  -call git_log -args '{"limit":5}'
```

## MCP server 部署

### 自研的 gitmcp(`skillagent` 用)

不需要任何外部依赖,和主工程同一个 module:

```bash
export MCP_GIT_SECRET=$(openssl rand -hex 24)
go run ./cmd/gitmcp                             # 默认分析当前工程,监听 127.0.0.1:8080
go run ./cmd/gitmcp -repo /path/to/other/repo   # 换一个仓库分析
go run ./cmd/gitmcp -addr 127.0.0.1:9090        # 换端口
```

不设 `MCP_GIT_SECRET` 也能起,只是不鉴权。密钥只从环境变量读、不做成命令行参数,因为 `ps` 能看到命令行。

### 第三方的 mcp-server-mysql(`mcp` demo 用)

依赖 [mcp-server-mysql](https://github.com/benborla/mcp-server-mysql) 以 remote 模式运行(Streamable HTTP,`POST /mcp`,Bearer 鉴权,无状态)。

```bash
cp deploy/mcp-mysql/.env.example deploy/mcp-mysql/.env
# 生成密钥填入 REMOTE_SECRET_KEY
openssl rand -hex 24

./deploy/mcp-mysql/run-server.sh
```

脚本会重建演示库、按需编译(`dist` 比源码新就跳过 `tsc`)、校验配置后启动。源码路径默认指向本机的 `mcp-server-mysql` 克隆,可用 `MCP_MYSQL_SRC` 覆盖;不想每次重建数据加 `--no-seed`,强制重编译加 `--rebuild`。

跑 demo 前把密钥导出给 Go 侧:

```bash
export MCP_MYSQL_SECRET=$(grep '^REMOTE_SECRET_KEY=' deploy/mcp-mysql/.env | cut -d= -f2)
```

### 两个已经踩过的坑

**演示数据有保质期。** `travel_demo.sql` 里的出行日期是 `INSERT` 时按 `CURDATE()` 求值后存成绝对日期的,不是查询时动态计算。种下去超过一周,「未来 7 天」的查询就会静默返回空集,Agent 会一本正经地说「本周没有出行计划」。所以脚本默认每次启动都重建演示库。

**中文必须显式声明字符集。** Homebrew 的 `mysql` CLI 默认 `character_set_client=latin1`,导入时会把中文双重编码写进表里。阴险之处在于 CLI 自己读回来显示是正常的(latin1 在写和读两个方向互相抵消),只有 mysql2 这类走 utf8mb4 的客户端才会暴露成乱码。`travel_demo.sql` 开头的 `SET NAMES utf8mb4;` 就是为此。反过来,修好之后用不带参数的 CLI 查这个库,中文会显示成 `?`,那是正常的——加 `--default-character-set=utf8mb4` 即可。

## 与 LangGraph 原版的对应关系

| Python | Go |
|---|---|
| `dag_demo.py` | `demo/fruitdag/` |
| `chatbot_demo.py` | `demo/chatbot/` |
| `agent_call_tools.py` | `demo/travelagent/` |
| `multi_agent_demo.py` | `demo/multiagent/`(三种实现) |

`mcp` 和 `skillagent` 没有 Python 对应版本,是在 Go 侧新增的。

各个包的头部注释里都写了与 LangGraph 对应实现的具体差异,想深入对照可以直接读源码。
