# Feature Specification: 通用 Agent 运行时与 Web 控制台

**Feature**: `20260930-general-agent-runtime`
**Created**: 2026-09-30
**Status**: Draft
**Input**: 用户描述："完整设计一个通用agent，入口是cmd，这个agent支持mcp和本地cli的工具调用，支持skills，支持rag调用（这个不太确定，假设本地有一些文档怎么做rag？），支持短期和长期记忆和session管理。然后设计一个页面（类似上面的），页面左侧栏上半部分用于配置管理，如rag文档管理和重新加载，mcp和本地cli工具，定时任务和skill管理；左侧下半部是session管理。其他部分则为人机交互的输入和输出页。"（随附 Codex UI 截图一张作为页面参照）
**设计依据**: [general_agent_brainstorm.md](../brainstorm/general_agent_brainstorm.md) ｜ [飞书文档](https://bytedance.my.larkoffice.com/docx/O5hedc8IQonfgBxtCl3m8TuRy6f)
**Meego**:

## Clarifications

### Session 2026-09-30

- Q: 本地存储是否由 SQLite 改为本地 MySQL？ → A: 保持 SQLite。MySQL 唯一实打实的收益是免去写串行化，但该问题在单人本地场景下代价接近于零；换库则需付出独立服务运维、约 400MB 常驻内存、新增 DSN 密钥面、16 张表 DDL 重写的代价。MySQL 仅在多人共用实例、千万行级数据量、或需第三方工具直连查库时才划算，当前三者均不成立。
- 追加发现：SQLite FTS5 的默认 `unicode61` 分词器同样无法切分中文——连续汉字会被当作单个 token。中文全文检索必须显式处理分词，不能依赖默认行为。

## User Scenarios & Testing *(mandatory)*

本功能在现有 Eino demo 仓库之上新建一个可长期使用的通用 Agent。七个 User Story 按 brainstorm 确定的实施顺序拆分，每个都是独立可交付、可验证的切片。

### User Story 1 - 对话内核与会话持久化 (Priority: P1)

用户在命令行执行 `go run ./cmd/agent` 启动程序，浏览器打开本地页面后即可与 Agent 对话。回复以流式方式逐字呈现。用户可以新建会话、在会话列表中切换、重命名和删除会话。关闭程序再次启动后，所有会话与完整的消息历史都还在。

会话标题由模型在首轮对话后异步生成，不阻塞用户输入。

本 Story 同时承担一项**前置验证职责**：确认 Eino v0.9.13 的六个内置中间件（`skill`、`reduction`、`summarization`、`filesystem`、`toolsearch`、`plantask`）的实际行为与设计假设一致。这些能力是整个方案的地基，假设不成立则后续设计需要返工。

**Why this priority**: 没有对话与持久化，其余六个 Story 全部无处依附。这是唯一的 MVP 候选——即使只交付这一个 Story，用户也已获得一个可用的本地 AI 对话工具。中间件验证置于此处，是为了用最小代价证伪地基假设，而非在完成一半后才发现。

**Technical Implementation**:

采用**会话级 Agent 工厂**架构。Eino 的 `ChatModelAgent` 在构造时固定工具集（`ToolsConfig` 传入后不可变），与"页面上热更新配置"的需求直接冲突。化解方式是每次对话开始时按当前配置快照构造一个新的 Agent 实例，而非维护全局单例并热重建。

选择理由：构造 Agent 是纯内存操作（拼提示词、组装工具切片、挂中间件），相对于一次数百毫秒的模型网络调用可忽略不计；而全局单例热重建会引入经典竞态——配置变更时若有多个对话正在跑，旧 agent 与新配置的归属关系难以理清，此类 bug 只在边用边改配置时偶发，复现极难。快照法用"不可变快照"这一概念直接绕开整个问题类别，并额外白送"不同 session 用不同模型与工具子集"的能力。

核心文件：

| 文件 | 职责 |
| --- | --- |
| `cmd/agent/main.go` | 入口。flag：`--addr`（默认 `127.0.0.1:8090`）、`--db`、`--rag-dir`、`--skills-dir` |
| `internal/agent/kernel/snapshot.go` | 配置快照结构：模型、工具集、技能集、记忆、限额 |
| `internal/agent/kernel/factory.go` | `Build(ctx, Snapshot) (adk.Agent, error)` |
| `internal/agent/store/db.go` | SQLite 打开、WAL 模式、写串行化 |
| `internal/agent/store/migrate.go` | 表结构迁移 |
| `internal/agent/session/session.go` | 会话增删改查 |
| `internal/agent/session/messages.go` | 消息读写，`tool_calls` JSON 序列化 |
| `internal/agent/session/context.go` | 模型输入视图的快照与恢复 |
| `internal/agent/session/title.go` | 标题异步生成 |
| `internal/agent/httpapi/server.go` | 路由（标准库 `net/http.ServeMux`） |
| `internal/agent/httpapi/stream.go` | `adk.AgentEvent` 转 SSE |
| `internal/agent/httpapi/static.go` | `go:embed web/dist` + SPA fallback |
| `web/` | React + Vite + TypeScript + Tailwind + shadcn/ui |

**双视图持久化**是本 Story 最关键的决策。`summarization` 中间件压缩后历史消息被摘要替换，若只存一份：只存压缩后则用户在页面上看不到自己此前说过什么；只存原始则每次恢复会话都要重新压缩一遍，既浪费又因两次摘要结果不同而不稳定。因此：

| 表 | 内容 | 用途 |
| --- | --- | --- |
| `messages` | 原始完整消息 | 页面展示、导出、审计 |
| `session_context` | 压缩后的模型输入快照 | 恢复会话时喂给模型 |

`tool_calls` 的 ID 必须原值保留——修改会让 assistant 消息中的工具调用与后续 tool 消息对不上，导致模型困惑甚至报错。

`CheckPointStore` 与 session 持久化是两回事，不可混淆：前者解决"单次 run 中途被打断后继续"，生命周期临时，用完即删；后者解决"跨进程重启的对话历史"，生命周期长期。Eino 只定义了 `CheckPointStore` 接口（`Get`/`Set` 两个方法存 `[]byte`），**未提供任何实现**，需自行用 SQLite 实现。

SSE 事件类型：`message_delta`、`tool_call`、`tool_result`、`compression`、`error`、`done`。

流式模式下 `ToolCalls` 需等流收完才完整（现有 `internal/agentio/print.go` 已注明此约束），因此工具调用事件必须在流结束后补发，不能边收边发。

前端**不能使用 `EventSource`**：浏览器的 `EventSource` 仅支持 GET 且不能携带请求体，而发送消息必须是 POST。须用 `fetch` + `ReadableStream` 手动解析 SSE 帧。

只监听回环地址，不提供 `0.0.0.0` 选项——该程序可读本地文件、执行命令、访问密钥，且无任何鉴权。密钥仅从环境变量 `ZHIPUAI_API_KEY` 读取（沿用 `internal/llm/model.go` 既有约定），不进数据库、不进配置文件、不作为 flag（`ps` 可见）。

相关 HTTP 接口：

- `POST /api/chat`（SSE）、`POST /api/chat/interrupt`
- `GET/POST/PATCH/DELETE /api/sessions`、`GET /api/sessions/{id}/messages`

**Independent Test**: 启动程序、打开页面、发起一轮对话、观察流式输出、新建第二个会话、切换回第一个会话确认历史完整、`Ctrl+C` 关闭程序后重启、确认两个会话与全部消息均已恢复。

**Acceptance Scenarios**:

1. **Given** 已设置 `ZHIPUAI_API_KEY` 且程序未启动，**When** 执行 `go run ./cmd/agent`，**Then** 程序监听 `127.0.0.1:8090`，浏览器访问后展示空态页面
2. **Given** 页面处于空态，**When** 用户输入问题并发送，**Then** 回复以流式方式逐字呈现，结束后会话出现在左栏列表中
3. **Given** 首轮对话已完成，**When** 等待数秒，**Then** 会话标题从默认值更新为模型生成的短标题，且期间用户可继续输入
4. **Given** 存在两个会话，**When** 用户点击切换，**Then** 主区域展示对应会话的完整历史消息
5. **Given** 程序已关闭，**When** 重新启动并打开页面，**Then** 全部会话与消息历史完整恢复
6. **Given** 模型正在流式输出，**When** 用户点击中断，**Then** 输出立即停止，已产生的部分内容被保留并落库
7. **Given** 未设置 `ZHIPUAI_API_KEY`，**When** 启动程序，**Then** 启动失败并给出明确的环境变量缺失提示

---

### User Story 2 - 工具系统：MCP 与本地 CLI (Priority: P2)

用户在页面左栏配置区添加 MCP server（填写端点与密钥所在的环境变量名），保存后可点击"测试连接"验证连通性。连接成功的 server 所提供的工具会出现在工具清单中，下一次对话即可被 Agent 调用，无需重启程序。

用户同样可以在页面上定义本地 CLI 工具：给定工具名、描述、要执行的命令和参数模板（如 `rg -n {pattern} {path}`）。Agent 只能填充占位符，不能改动命令本身或参数结构。

**Why this priority**: 工具是 Agent 从"聊天机器人"变成"能干活的助手"的分界线。这是仅次于对话本身的核心价值。

**Technical Implementation**:

两类工具来源统一由 `internal/agent/tools/registry.go` 装配。

| 文件 | 职责 |
| --- | --- |
| `internal/agent/tools/mcp/manager.go` | 多连接管理、懒连接、健康检查、连通性测试 |
| `internal/agent/tools/cli/declarative.go` | 读 `cli_tools` 表动态构造工具 |
| `internal/agent/tools/cli/exec.go` | 执行、环境变量白名单、超时、输出截断 |
| `internal/agent/tools/cli/shell.go` | `filesystem.Shell` 实现 + `CommandValidator` |
| `internal/agent/tools/registry.go` | 装配、命名冲突检测、按会话过滤工具范围 |
| `internal/mcpclient`（复用，不改动） | 底层 MCP 连接与握手 |

**动态工具不能使用 `utils.InferTool`**。它依赖 Go 泛型在编译期推导 JSON Schema，而 CLI 工具是运行时在页面上定义的。正确走法是 `utils.NewTool[map[string]any, string](toolInfo, handler)` 配合 `schema.NewParamsOneOfByParams` 手工构造参数描述。这是实现上的关键分叉，走错需整体返工。

**工具名冲突必须处理**：两个 MCP server 都提供 `read_file` 会互相覆盖。统一加前缀 `<server>__<tool>`，装配时检测冲突并在页面标注。

**连接失败不阻塞启动**：某个 MCP server 不可达时记录状态、页面标红，其余工具照常可用。

**安全约束**（本 Story 必须落实）：

- 子进程的 `cmd.Env` 走白名单（`PATH`、`HOME`、`LANG` 等）。Go 的 `exec.Cmd` 在 `Env` 为 nil 时继承父进程全部环境变量，显式列举反而更省事，且避免 `ZHIPUAI_API_KEY` 等密钥被模型通过 `env` 命令读出
- 参数一律以切片传给 `exec.CommandContext`，不拼接 shell 字符串（沿用 `internal/gitmcp` 既有约定）
- MCP server 的密钥不落库：`mcp_servers` 表存 `secret_ref`（环境变量名）而非明文
- `execute` 自由执行工具默认关闭，由 `cli_policy` 规则经 `CommandValidator` 拦截；定时任务场景下强制禁用

声明式模板作为默认路径（命令是用户预先审过的，无需沙箱），框架 `filesystem` 中间件的 `execute` 作为可选开关——这一取舍的依据是：通用 Agent 的通用性应来自"换 MCP server、换技能、换文档"，而非"让模型随便敲命令"。

相关 HTTP 接口：

- `GET/POST/PATCH/DELETE /api/tools/mcp`、`POST /api/tools/mcp/{id}/test`
- `GET/POST/PATCH/DELETE /api/tools/cli`、`GET/PUT /api/tools/cli/policy`
- `GET /api/tools`（当前生效的工具全集）

可用现有的 `cmd/gitmcp`（6 个只读 Git 工具）作为联调用的 MCP server，无需额外搭建。

**Independent Test**: 启动 `go run ./cmd/gitmcp`，在页面上添加该 MCP server 并测试连接，确认工具清单出现 6 个 git 工具；再定义一个声明式 CLI 工具；新开一轮对话让 Agent 使用这两类工具完成一个任务。

**Acceptance Scenarios**:

1. **Given** `cmd/gitmcp` 已启动，**When** 用户在页面添加该 server 并点击测试连接，**Then** 返回成功并列出其提供的工具名
2. **Given** MCP server 已添加且启用，**When** 用户新开一轮对话并提出需要该工具的问题，**Then** Agent 成功调用工具，页面以折叠卡片展示调用与返回
3. **Given** MCP server 配置已保存，**When** 用户禁用它并再开一轮对话，**Then** 该 server 的工具不再出现在可用工具中，且**程序未重启**
4. **Given** 配置了一个不可达的 MCP server，**When** 启动程序，**Then** 程序正常启动，该 server 在页面标记为异常，其余工具可用
5. **Given** 两个 MCP server 提供同名工具，**When** 装配工具集，**Then** 工具名自动加 server 前缀，页面提示存在冲突
6. **Given** 已定义声明式 CLI 工具 `rg -n {pattern} {path}`，**When** Agent 调用它，**Then** 命令以参数切片方式执行，且子进程环境变量中不含 `ZHIPUAI_API_KEY`
7. **Given** CLI 工具执行超时，**When** 超过配置的超时时间，**Then** 进程被终止，工具返回超时错误而非挂起

---

### User Story 3 - 技能驱动与上下文治理 (Priority: P3)

用户把技能写成 `SKILL.md` 文件放进 `--skills-dir` 指定的目录，或直接在页面上创建。每个技能描述"某一类任务该按什么步骤、用哪些工具完成"。Agent 在收到问题后自行判断哪个技能匹配，加载其完整说明并按步骤执行。新增一项能力等于新增一个 `SKILL.md`，不需要改 Go 代码、不需要重启程序。

技能可指定执行模式：`inline`（说明直接返回到当前对话）、`fork`（在不带父历史的子 agent 中执行）、`fork_with_context`（子 agent 携带父历史）。

长会话中，用户可在输入框旁看到当前 token 占用与已压缩次数；压缩多次后系统会提示开启新会话。

**Why this priority**: 技能让 Agent 的能力边界可由用户扩展而无需改代码；上下文治理让长会话真正可用。两者都依赖 Eino 中间件，实现成本低但价值高。

**Technical Implementation**:

本 Story 几乎全部由 Eino 内置中间件承担，自研量很小。

| 文件 | 职责 |
| --- | --- |
| `internal/agent/skills/backend.go` | 实现 `skill.Backend` 的 `List` / `Get` |
| `internal/agent/skills/store.go` | 技能 CRUD 与目录扫描缓存 |
| `internal/agent/kernel/context.go` | 配置并挂载 `reduction` 与 `summarization` |
| `internal/agent/store/offload.go` | `reduction.Backend` 实现，卸载内容落盘 |

**不使用现成的 `NewBackendFromFilesystem`**，因为它只读单一目录。本方案需合并两个来源：`--skills-dir` 文件系统目录（便于编辑器与 git 管理）与 SQLite 中页面创建的技能，同名时以数据库为准并标注冲突。`Config.UseChinese` 置真以启用中文提示词。

**热重载几乎零成本**：`Backend.List(ctx)` 是在 `skillTool.Info(ctx)` 中调用的，每次取工具描述都会重读；叠加 US1 的会话级工厂每次重建 Agent，形成双重保险。`POST /api/skills/reload` 实际只需让目录扫描缓存失效。

注意 Eino 把技能清单渲染进 **`skill` 工具的 description** 而非 system prompt——清单与工具绑定，模型看到工具即看到清单。

**中间件挂载顺序不是任意的**：

| 顺序 | 中间件 | 为什么在此位置 |
| --- | --- | --- |
| 1 | `skill` | 最先注入 Skills System 提示词与 `skill` 工具 |
| 2 | `filesystem` | 提供 `read_file`/`glob`/`grep`，**必须早于 `reduction`**——卸载的内容要靠它取回 |
| 3 | `plantask` | 任务清单工具，与上下文改写无关 |
| 4 | `toolsearch` | 工具总数超阈值时启用，**必须在全部工具注册完之后** |
| 5 | `reduction` | 先做**可逆**的工具结果卸载 |
| 6 | `summarization` | 最后才做**有损**的对话历史摘要 |

5 与 6 的先后是关键：先可逆卸载、再有损摘要。反过来会在还有大量工具结果可卸载时就把对话历史压没了。顺序错误不报错，只是效果变差且极难归因。

**三层上下文防御**（从优到劣）：

1. `skill` 的 `fork` 模式——技能在子 agent 中执行，中间过程不进主上下文，是"不产生垃圾"而非"产生后清理"
2. `reduction` 卸载——工具结果超长时存盘并替换为提示，**内容可经 `read_file` 取回，是可逆的**
3. `summarization` 摘要——有损且不可逆，压到第三轮时上下文已是"摘要的摘要"，质量断崖下跌

上下文膨胀的两个来源量级相差一个数量级：对话轮次每轮数百 token，而单次 `grep` 或 `git diff` 可达上万 token。这正是 Eino 将其拆为两个中间件的原因。

阈值默认值（`glm-4.5-air` 上下文窗口 128K）：`reduction` 约 60K 触发，`summarization` 约 80K 触发，留足余量因 token 估算不精确。

`reduction` 的卸载目标用本地文件目录而非数据库，以便模型通过 `filesystem` 中间件的 `read_file` 取回。

页面需展示 token 占用与压缩次数，压缩超过 3 次时提示"本会话已压缩 N 次，建议新开会话，可先将结论 `remember` 下来"——摘要有损，此时诚实提示优于无限压缩。

相关 HTTP 接口：`GET/POST/PATCH/DELETE /api/skills`、`POST /api/skills/reload`

**Independent Test**: 在技能目录放入两个 `SKILL.md`（工具集刻意不重叠），提出一个不点名技能的问题，确认 Agent 自行选对技能并只用该技能允许的工具；随后在页面上修改技能正文，不重启程序再问一次，确认新内容生效。

**Acceptance Scenarios**:

1. **Given** 技能目录含两个 `SKILL.md`，**When** 用户提出匹配其中之一的问题，**Then** Agent 加载该技能并按其规定的工具与步骤执行
2. **Given** 技能已加载，**When** 用户在页面修改其正文并保存，**Then** 下一轮对话即使用新内容，**程序未重启**
3. **Given** 某技能 `context` 设为 `fork`，**When** Agent 执行该技能，**Then** 技能内部的工具调用不出现在主会话消息流中，仅最终结果返回
4. **Given** `SKILL.md` 的 YAML 前言格式错误，**When** 加载技能，**Then** 页面展示明确的解析错误与文件路径，其余技能不受影响
5. **Given** 一次工具调用返回超过 `MaxLengthForTrunc` 的内容，**When** 该结果进入上下文，**Then** 内容被卸载到磁盘、上下文中留下提示，且 Agent 可用 `read_file` 取回
6. **Given** 会话 token 占用超过 `summarization` 阈值，**When** 用户继续发送消息，**Then** 历史被摘要压缩，前端收到 `compression` 事件并更新指示器，而 `messages` 表中的原始消息不受影响
7. **Given** 会话已压缩 3 次，**When** 用户继续对话，**Then** 页面提示建议新开会话

---

### User Story 4 - 本地文档 RAG (Priority: P4)

用户把本地文档（Markdown、纯文本、PDF、DOCX、HTML、XLSX）放进 `--rag-dir` 指定的目录，或在页面上传。点击"重新加载"后，系统自动完成解析、切分、向量化与入库，全程无需人工标注或训练。索引期间页面展示进度。

索引完成后，Agent 在对话中可自行判断何时调用 `search_docs` 检索文档内容来回答问题。

页面上可看到每个文档的路径、大小、chunk 数、索引时间与状态（已索引／待索引／失败及原因）。

**Why this priority**: 让 Agent 能回答"我本地资料里写了什么"这类问题，是通用 Agent 的关键能力之一。但它依赖对话与工具体系已就绪，故排在其后。

**Technical Implementation**:

澄清一个概念：**RAG 没有"训练"环节，模型权重完全不变**。链路是：文档 → Loader 读取 → Transformer 切分 → Embedder 转向量 → Indexer 入库；提问时 query 转向量 → Retriever 检索 top-K → 拼进 prompt。

绝大部分环节 Eino 生态已有现成实现，唯一自研的是索引与检索：

| 环节 | 实现来源 |
| --- | --- |
| 解析 | `eino-ext/components/document/parser/{pdf,docx,html,xlsx}` + eino core `text_parser` |
| 切分 | `eino-ext/components/document/transformer/splitter/{markdown,recursive}` |
| 向量化 | `eino-ext/components/embedding/openai` 指向智谱端点 |
| **索引与检索** | **自研**：SQLite + FTS5 + 内存余弦 |
| 重排 | `eino-ext/components/document/transformer/reranker/score` |

| 文件 | 职责 |
| --- | --- |
| `internal/agent/rag/loader.go` | 目录扫描与上传，按扩展名派发解析器 |
| `internal/agent/rag/split.go` | Markdown 用 `splitter/markdown`，其余用 `recursive` |
| `internal/agent/rag/embedder.go` | `embedding/openai` 指向智谱，复用 `internal/llm` 的 BaseURL 约定 |
| `internal/agent/rag/indexer.go` | 实现 `indexer.Indexer` |
| `internal/agent/rag/retriever.go` | 实现 `retriever.Retriever`，混合检索 |
| `internal/agent/rag/tool.go` | 包装为 `search_docs` 工具 |
| `internal/agent/rag/manager.go` | 文档增删、重建触发、进度跟踪 |

**不引入向量数据库**。万级 chunk 以内，1024 维 float32 约 40MB 内存，全量余弦相似度在 10ms 量级。本地文档库远达不到需要 Milvus／Qdrant 的规模——Milvus standalone 空跑即占 1.5~2.5GB 内存，且 Milvus Lite 仅有 Python SDK，Go 必须起 Docker。为本场景引入向量数据库是典型的过度设计。

**混合检索而非纯向量**。本地文档与代码含大量专有标识符（如 `MaxResultBytes`、错误码），纯向量检索在"精确匹配某个词"上往往输给关键词检索。SQLite 自带 FTS5 全文索引，零额外依赖。

**FTS5 的中文分词必须显式处理**。FTS5 默认的 `unicode61` 分词器按 Unicode 字符类别切词，而汉字属于"字母类"，于是一整句连续汉字会被切成**一个 token**——中文查询几乎恒返回空结果。该问题不会报错，只会静默地让关键词那一路失效，若开发期只用英文标识符测试则完全察觉不到。解决方式见 plan.md 的 D6。

融合用 **RRF（Reciprocal Rank Fusion）**：对两路结果各自的排名取倒数相加。选它的原因是余弦相似度与 BM25 分数**量纲完全不同**，直接加权相加没有意义，而 RRF 只用排名不用分数，天然规避该问题。

`reranker/score` 并非按分数简单降序，而是把高分文档置于数组**首尾**、低分置于中间，针对 LLM 的 "lost in the middle" 效应——模型对上下文首尾的注意力显著高于中部。

**增量索引是必需品而非优化**：`rag_documents` 存文件 hash，重新加载时只处理新增与变更文件。否则每次点"重新加载"都要全量重新向量化，既慢又消耗智谱配额。

**向量以 float32 打包为 BLOB**，不用 JSON。1024 维 float32 是 4KB，JSON 文本会膨胀三倍以上且每次检索都需反序列化。

**索引是长耗时操作**（几千 chunk 需数分钟），必须做成后台任务并上报进度，不能阻塞 HTTP 请求。

**失败原因必须在页面显示**：PDF 解析失败、文件过大、embedding 超限都会发生，静默跳过会让用户误以为索引成功但检索不到。

Embedding 选用智谱 `embedding-3`，复用现有 `ZHIPUAI_API_KEY` 与 OpenAI 兼容端点，零额外部署。**代价是文档全文会发送给智谱**——若后续需索引敏感文档，可切换至 Ollama 本地 Embedding，`Embedder` 本身是接口，切换成本很低。

相关 HTTP 接口：`GET/POST/DELETE /api/rag/documents`、`POST /api/rag/reindex`、`GET /api/rag/status`

**Independent Test**: 放入若干本地文档，点击重新加载并观察进度，索引完成后提出一个答案只存在于文档中的问题，确认 Agent 调用 `search_docs` 并给出基于文档的回答；再修改其中一个文档后重新加载，确认只有该文档被重新索引。

**Acceptance Scenarios**:

1. **Given** `--rag-dir` 中有若干 Markdown 与 PDF，**When** 用户点击重新加载，**Then** 页面展示索引进度，完成后列出每个文档的 chunk 数与状态
2. **Given** 索引已完成，**When** 用户提出答案仅存在于文档中的问题，**Then** Agent 调用 `search_docs` 并基于检索结果作答
3. **Given** 索引已完成且文档未变更，**When** 用户再次点击重新加载，**Then** 所有文档被跳过，不产生新的 embedding 调用
4. **Given** 某文档已被修改，**When** 重新加载，**Then** 仅该文档被重新解析与向量化，其余跳过
5. **Given** 某 PDF 无法解析，**When** 索引执行，**Then** 该文档标记为失败并展示原因，其余文档正常索引完成
6. **Given** 用户检索一个专有标识符，**When** `search_docs` 执行，**Then** FTS5 关键词一路能精确命中包含该标识符的片段
7. **Given** 正在进行大批量索引，**When** 用户同时发起对话，**Then** 对话不被阻塞，SQLite 未出现 `database is locked`
8. **Given** 一份纯中文文档已入库，**When** 用户以中文词语检索，**Then** FTS5 关键词一路能召回相关片段，而非返回空结果

---

### User Story 5 - 跨会话长期记忆 (Priority: P5)

当用户表达稳定的偏好、约定或纠正时，Agent 可调用 `remember` 工具把这条事实记下来。这些事实在**所有后续会话**中都对 Agent 可见，不随会话结束而丢失。

用户可在页面左栏配置区看到一个"长期记忆"列表，按分类分组，每条可查看来源会话、编辑正文、直接删除，也可**手动新增**一条。

**Why this priority**: 让 Agent 跨会话"认识"用户，是从工具变成助手的一步。但它是增量价值而非基础能力，故优先级低于前四项。

**Technical Implementation**:

Eino **没有提供任何长期记忆能力**，本 Story 全部自研。框架中与"记忆"沾边的只有两样，都不是长期记忆：`AddSessionValue`／`GetSessionValue` 是单次 run 内的 KV 传递通道（run 结束即失效，用于父子 agent 传参）；`CheckPointStore` 是中断恢复机制。

| 文件 | 职责 |
| --- | --- |
| `internal/agent/memory/store.go` | `memories` 表 CRUD，同 `key` 覆盖 |
| `internal/agent/memory/tool.go` | `remember(key, content, category)` 工具 |
| `internal/agent/memory/inject.go` | 分档注入策略 |

**`key` 是去重的关键**。工具签名为 `remember(key, content, category)`，如 `remember("用户偏好-代码注释", "要求中英文双语注释同时存在")`，同 key 直接覆盖。

没有 key 的话，模型在长期使用中会**反复记录同一件事的不同措辞**，记忆表不断膨胀且互相矛盾，最终变成噪音。有了 key，"更新"与"新增"被自然区分，页面列表始终是一条条独立事实而非流水账。

**召回分档，而非无脑上检索**：

| 记忆规模 | 召回方式 | 是否提供 `recall` 工具 |
| --- | --- | --- |
| 小（约 50 条 / 2000 token 以内） | **全量注入 system prompt** | 否 |
| 大 | 向量检索 top-K 注入 | 是 |

理由是 `recall` 工具最大的失败模式是"模型压根不调它"——模型没觉得需要回忆就不会调，长期记忆等于不存在。小规模全量注入**严格优于**检索：零漏召回、零额外延迟、且根本不存在该失败模式。个人 Agent 绝大多数时候就在这一档。大规模档复用 US4 已建好的 embedding 链路，不是额外成本。

**注入文案必须带限定语**：注入到 system prompt 尾部，标题写为"关于用户的已知事实（可能已过时，与当前对话冲突时以当前对话为准）"。缺了这句括号，模型会把数月前的旧偏好当成硬约束，与用户当下的指令打架。

`remember` 的调用时机须在 system prompt 中写明（如"当用户表达了稳定的偏好、约定或对你的纠正时"），否则模型要么从不调用，要么把每句闲聊都记下来。

**人工录入是地基，模型自动记忆是锦上添花**。`remember` 的调用时机完全由模型判断，不可靠；页面手动新增才是可靠路径——用户想让 Agent 永远记住某条约定，直接在页面敲一条即可，无需指望模型某次灵光一闪。基于此，**不做会话结束时的自动抽取**：它想解决的问题已被手动录入解决大半，却要在每次会话结束时多烧一次模型调用。

记忆必须**可见、可编辑、可删除**。长期记忆的失败模式不是"记不住"，而是"记错了还一直用"。结构化的人类可读事实让用户能随手删掉错的；若记的是向量，用户什么也看不见，出问题只能清库重来。

相关 HTTP 接口：`GET/POST/PATCH/DELETE /api/memories`

**Independent Test**: 在一个会话中告知 Agent 一条稳定偏好并确认其调用了 `remember`；新开一个会话，提出与该偏好相关的问题，确认 Agent 的回答遵循了它；在页面上编辑该条记忆后再新开会话，确认行为随之改变。

**Acceptance Scenarios**:

1. **Given** 用户在对话中表达了一条稳定偏好，**When** Agent 判断值得记录，**Then** 调用 `remember` 并在页面记忆列表中出现该条
2. **Given** 已存在若干记忆，**When** 用户新开一个会话并提问，**Then** 这些事实已在 system prompt 中，Agent 无需调用任何工具即可遵循
3. **Given** Agent 用相同 `key` 再次调用 `remember`，**When** 写入执行，**Then** 原记录被覆盖而非新增一条
4. **Given** 用户在页面手动新增一条记忆，**When** 新开会话，**Then** Agent 的行为遵循该条，路径与模型自动记录的完全一致
5. **Given** 某条记忆已过时且与用户当前指令冲突，**When** 用户在对话中明确指示，**Then** Agent 以当前对话为准而非旧记忆
6. **Given** 用户在页面删除某条记忆，**When** 新开会话，**Then** 该事实不再出现在 system prompt 中
7. **Given** 记忆条数超过全量注入阈值，**When** 构造 Agent，**Then** 自动切换为向量检索模式并提供 `recall` 工具

---

### User Story 6 - 定时任务 (Priority: P6)

用户可以在对话中直接告诉 Agent "每天早上九点帮我汇总一次昨天的进展"，Agent 调用 `schedule_task` 工具把它登记为定时任务，并回复下次执行时间。用户也可在页面左栏配置区手动创建、编辑、启用／停用、立即执行任务。

任务到点自动运行，结果写入任务执行历史，用户可在页面查看每次执行的时间、状态与结果正文。

定时任务的典型用途既包括自定义业务任务，也包括定时重建 RAG 索引。

**Why this priority**: 让 Agent 能在用户不在场时工作，价值高但依赖前面全部能力就绪。

**Technical Implementation**:

| 文件 | 职责 |
| --- | --- |
| `internal/agent/schedule/scheduler.go` | cron 调度（`robfig/cron/v3`），启动时全量恢复 |
| `internal/agent/schedule/store.go` | 任务与执行记录读写 |
| `internal/agent/schedule/tool.go` | `schedule_task` 工具 |
| `internal/agent/schedule/runner.go` | 构造受限 Snapshot、执行、结果落库 |

**数据库是唯一真相源，内存调度器只是其运行时投影**。增删改一律"先写库、再同步调度器"，绝不反向。进程崩溃或 `kill -9` 后从 `scheduled_tasks` 全量恢复即可，不丢任何东西。内存中需维护 `任务 ID → cron EntryID` 映射，因为 `robfig/cron` 用自己的 `EntryID` 标识调度项，删改时需精确移除旧项。

**执行上下文默认无状态**：结果只写入 `task_runs`，不进会话列表。定时任务的输出是**报告而非对话**，硬塞进会话列表会把它冲垮——一个每天跑的任务一个月就是 30 条 session。需要跨次对比的任务（如"今天和昨天有什么不同"）可在任务级开启"保留上下文"，绑定一个固定 session 累积历史。

**错过不补跑**：启动时对 `next_run_at` 已过期的任务写一条 `missed` 记录并重算下次时间。记录这一事实很重要——否则用户会以为任务在跑，实际上它上周就因关机而静默失效。

**同一任务禁止重叠执行**：Agent 任务动辄数十秒至数分钟，一个每分钟触发的任务能在十分钟内堆起十个并发实例，把模型配额和机器一起拖垮。上次未完成则跳过并记录。

**`schedule_task` 必须回显下次执行时间**：模型书写 cron 表达式出错率不低（`0 9 * * *` 与 `9 0 * * *` 分不清），且写错不报错，只是在意想不到的时间执行。工具返回"已登记任务 X，下次执行：2026-10-01 09:00:00"可让模型自检，用户在对话中也能一眼发现异常。这比任何参数校验都有效。

**失败不自动重试**：Agent 任务失败多为配额耗尽、服务不可用等系统性原因，重试只会加剧问题。记录原因、页面展示，由用户决定是否手动重跑。单次执行设硬超时。

**受限工具集**：定时任务执行时禁用 `execute` 自由执行与写操作——此时无人可做确认。这由 US1 的会话级快照天然支持。

**熔断**：Agent 能自行登记定时任务，而定时任务执行时又是一个 Agent，理论上可登记"每分钟执行且每次登记新任务"的任务。需设置任务总数上限与最小执行间隔。

**`task_runs` 需保留策略**（如每任务保留最近 100 次或 90 天）：一个每分钟执行的任务一年将产生五十余万行。

相关 HTTP 接口：`GET/POST/PATCH/DELETE /api/tasks`、`POST /api/tasks/{id}/run`、`GET /api/tasks/{id}/runs`

**Independent Test**: 在对话中口述一个定时任务，确认 Agent 登记成功并回显了正确的下次执行时间；在页面上把它改为一分钟后执行，等待其自动运行，查看执行历史中的结果；重启程序确认任务仍在。

**Acceptance Scenarios**:

1. **Given** 用户在对话中描述一个定时需求，**When** Agent 调用 `schedule_task`，**Then** 任务落库且工具返回值中包含人类可读的下次执行时间
2. **Given** 任务已登记且到达执行时间，**When** 调度器触发，**Then** Agent 以受限工具集执行，结果写入 `task_runs`，会话列表不新增条目
3. **Given** 任务开启了"保留上下文"，**When** 连续执行两次，**Then** 第二次执行能看到第一次的结果
4. **Given** 上一次执行尚未结束，**When** 到达下一个触发时间，**Then** 本次被跳过并在 `task_runs` 中记录
5. **Given** 程序在任务触发时间点处于关闭状态，**When** 重新启动，**Then** 写入一条 `missed` 记录并重算下次执行时间，**不补跑**
6. **Given** 定时任务执行中，**When** Agent 试图调用 `execute` 自由执行工具，**Then** 该工具不在其可用工具集中
7. **Given** 程序被 `kill -9`，**When** 重新启动，**Then** 全部任务从数据库恢复，调度继续

---

### User Story 7 - 配置管理控制台 (Priority: P7)

用户在页面左栏上半部分完成全部配置管理：RAG 文档管理与重新加载、MCP 工具、本地 CLI 工具、技能管理、定时任务、长期记忆。左栏下半部分是会话管理。右侧为人机交互的输入与输出区。

界面风格参照用户提供的 Codex UI 截图：极简、大量留白、侧栏分组。

**Why this priority**: 前六个 Story 的接口能力已具备，本 Story 把它们统一收拢为可视化操作界面并做整体打磨。功能上非阻塞，故排在最后。

**Technical Implementation**:

| 文件 | 职责 |
| --- | --- |
| `web/src/components/Sidebar/ConfigPanel.tsx` | 六个配置分组，点击展开抽屉 |
| `web/src/components/Sidebar/SessionList.tsx` | 会话列表与新建按钮 |
| `web/src/components/Chat/MessageList.tsx` | 消息流 |
| `web/src/components/Chat/ToolCallCard.tsx` | 工具调用卡片，默认折叠 |
| `web/src/components/Chat/Composer.tsx` | 输入框、模型选择、token 指示器 |
| `web/src/components/Chat/EmptyState.tsx` | 空态欢迎页 |
| `web/src/api/` | REST 封装与 SSE 解析 |
| `web/src/store/` | zustand 状态管理 |

页面区域与截图的对应关系：

| 区域 | 组件 | 对应 Codex 截图 |
| --- | --- | --- |
| 左栏上半 | `ConfigPanel`：RAG 文档、MCP 工具、本地 CLI、技能、定时任务、长期记忆 | 取代 `Projects` 区 |
| 左栏下半 | `SessionList` 与新建按钮 | `Recents` 与 `New chat` |
| 主区空态 | `EmptyState`：欢迎语与快捷入口 | "What should we build?" 屏 |
| 主区对话 | `MessageList` 与 `ToolCallCard` | 对话区 |
| 底部 | `Composer`：输入框、模型选择、token 指示器 | 输入框与模型选择器 |

**发布形态**：前端在 `web/` 独立目录维护、独立构建。开发时 `npm run dev` 热更新并由 Vite 代理到 Go 后端；发布时 `npm run build` 的产物经 `go:embed` 打进二进制。最终仍是**单进程、单文件**，`go run ./cmd/agent` 即可使用，node 只在修改前端时才需要。

**工具调用卡片默认折叠**：一次任务可能调用十余次工具，全部展开会淹没模型的实际回答。折叠后仅显示"调用了 `search_docs`"，点击展开看细节。

**状态管理用 zustand 而非 Redux**：本应用的状态复杂度（会话列表、当前消息流、六组配置）不足以支撑 Redux 的样板代码开销。

**UI 库选用 Tailwind + shadcn/ui**：与 Codex 截图同源（均为 Radix + Tailwind 一脉），视觉风格最接近。Ant Design／Element Plus 这类组件库偏"后台管理系统"观感，与目标的极简风格差距较大。

技能编辑界面需暴露 `context` 字段（`inline`／`fork`／`fork_with_context`）并配说明——它是上下文防御的第一层，用户需要知道它存在。

**Independent Test**: 不接触任何配置文件与命令行，仅通过页面完成：添加一个 MCP server、定义一个 CLI 工具、创建一个技能、上传并索引一个文档、新增一条记忆、创建一个定时任务，随后在对话中验证这六项配置均已生效。

**Acceptance Scenarios**:

1. **Given** 程序已启动，**When** 用户打开页面，**Then** 左栏上半展示六个配置分组、下半展示会话列表，右侧为交互区
2. **Given** 用户从未使用过任何配置文件，**When** 仅通过页面完成六类配置，**Then** 全部配置生效且无需重启程序
3. **Given** Agent 在一轮对话中调用了多次工具，**When** 消息流渲染，**Then** 工具调用以折叠卡片呈现，不淹没最终回答
4. **Given** 页面处于空态，**When** 用户查看，**Then** 展示欢迎语与快捷入口，风格与 Codex 截图一致
5. **Given** 用户在编辑技能，**When** 打开执行模式下拉，**Then** 可选 `inline`／`fork`／`fork_with_context` 并附有说明文字
6. **Given** 前端已构建，**When** 执行 `go run ./cmd/agent`，**Then** 页面由二进制内嵌资源提供，无需单独启动前端进程
7. **Given** 用户访问前端路由的深层路径并刷新，**When** 服务端处理请求，**Then** SPA fallback 正确返回 `index.html`

---

### Edge Cases

**启动与环境**

- 未设置 `ZHIPUAI_API_KEY` 时如何处理？启动即失败并明确提示缺失的环境变量名，不延迟到首次对话才暴露
- SQLite 文件损坏或版本不兼容时如何处理？迁移失败需明确报错并指出数据库路径，不静默创建新库
- `--rag-dir` 或 `--skills-dir` 指向不存在的目录时如何处理？视为空集合并在页面提示，不阻塞启动
- 端口被占用时如何处理？明确报错并提示更换 `--addr`

**并发与一致性**

- 定时任务、交互对话、RAG 索引三方同时写库时如何处理？SQLite 必须开启 WAL 模式并串行化写入，否则出现 `database is locked`。此类问题在单人单会话开发期完全不出现，投入使用后才暴露
- 用户在对话进行中修改配置时如何处理？进行中的对话使用其启动时的快照，不受影响；变更对下一轮生效
- 同一会话被两个浏览器标签页同时操作时如何处理？以消息序号做乐观并发控制，冲突时后写入者收到提示

**上下文与压缩**

- 单条工具返回超过整个上下文窗口时如何处理？`reduction` 的截断阶段先行卸载，不进入模型输入
- `summarization` 调用本身失败时如何处理？降级为截断最旧消息并在事件流中告知，不能让对话彻底卡死
- `messages` 与 `session_context` 不一致时如何发现？需要一致性校验——这是最隐蔽的故障，页面显示正常但模型看到错乱历史

**工具**

- MCP server 中途断连时如何处理？下一轮对话装配时标记不可用，其余工具正常
- 两个 MCP server 提供同名工具时如何处理？加 `<server>__<tool>` 前缀并在页面提示冲突
- CLI 工具输出二进制或超长内容时如何处理？按 `MaxResultBytes` 截断，非 UTF-8 内容做安全转义
- 模型给出的参数无法填入参数模板时如何处理？返回工具错误而非 Go 错误，让模型自行纠正
- 工具总数超过模型可承载的数量时如何处理？启用 `toolsearch` 中间件动态控制可见性

**RAG**

- 文档中途被删除但索引仍在时如何处理？检索结果标注来源文件已失效
- 同一文档被重复上传时如何处理？按 hash 判重，提示已存在
- Embedding 调用超配额或限流时如何处理？索引任务标记为失败并记录原因，已完成部分不回滚，支持续跑
- 文档为空或切分后无有效 chunk 时如何处理？标记为"已索引，0 chunk"而非失败

**记忆与定时任务**

- 模型写入超长或明显无意义的记忆内容时如何处理？长度上限 + 页面可编辑删除作为兜底
- 模型给出非法 cron 表达式时如何处理？工具返回校验错误与格式示例，不落库
- 定时任务登记数量失控时如何处理？总数上限与最小执行间隔熔断
- 定时任务执行时模型服务不可用时如何处理？记录失败原因，不自动重试

## Requirements *(mandatory)*

### Functional Requirements

**入口与运行时**

- **FR-001**: 系统 MUST 提供 `cmd/agent` 命令行入口，支持 `--addr`、`--db`、`--rag-dir`、`--skills-dir` 参数
- **FR-002**: 系统 MUST 仅监听回环地址，不提供监听全部网卡的选项
- **FR-003**: 系统 MUST 仅从环境变量读取模型密钥，禁止将其持久化到数据库、配置文件或命令行参数
- **FR-004**: 系统 MUST 在每次对话开始时按当前配置快照构造 Agent 实例，使配置变更无需重启即对新对话生效
- **FR-005**: 系统 MUST 将前端构建产物通过 `go:embed` 内嵌，使运行时无需独立的前端进程

**对话与会话**

- **FR-006**: 系统 MUST 以 SSE 流式返回模型输出，事件类型涵盖增量文本、工具调用、工具结果、压缩通知、错误与结束
- **FR-007**: 系统 MUST 支持中断进行中的对话，并保留已产生的部分输出
- **FR-008**: 用户 MUST 能够新建、切换、重命名、删除会话
- **FR-009**: 系统 MUST 持久化原始完整消息与压缩后的模型输入视图两份数据
- **FR-010**: 系统 MUST 在消息持久化与恢复过程中保持 `tool_calls` 的 ID 原值不变
- **FR-011**: 系统 MUST 异步生成会话标题，不阻塞用户输入
- **FR-012**: 系统 MUST 实现 `adk.CheckPointStore` 接口以支撑中断与恢复

**工具系统**

- **FR-013**: 系统 MUST 支持配置多个 MCP server，并提供连通性测试
- **FR-014**: 系统 MUST 在某个 MCP server 不可达时仍正常启动，其余工具照常可用
- **FR-015**: 系统 MUST 对同名工具自动加 server 前缀并在页面提示冲突
- **FR-016**: 用户 MUST 能够以声明式模板定义本地 CLI 工具，模型仅可填充占位符而不能改动命令与参数结构
- **FR-017**: 系统 MUST 以参数切片方式执行外部命令，禁止拼接 shell 字符串
- **FR-018**: 系统 MUST 为子进程设置环境变量白名单，禁止透传父进程全部环境变量
- **FR-019**: 系统 MUST 对工具返回结果设置大小上限
- **FR-020**: 系统 MUST 默认关闭自由执行工具，并在定时任务场景下强制禁用
- **FR-021**: 系统 MUST 将 MCP 密钥以环境变量名引用的方式存储，禁止明文落库

**技能**

- **FR-022**: 系统 MUST 从文件系统目录与数据库两个来源合并加载技能，同名时以数据库为准并提示冲突
- **FR-023**: 系统 MUST 支持技能的 `inline`、`fork`、`fork_with_context` 三种执行模式
- **FR-024**: 系统 MUST 在技能变更后无需重启即生效
- **FR-025**: 系统 MUST 在技能文件格式错误时给出明确的文件路径与错误原因，且不影响其余技能加载

**上下文治理**

- **FR-026**: 系统 MUST 按 `skill` → `filesystem` → `plantask` → `toolsearch` → `reduction` → `summarization` 的顺序挂载中间件
- **FR-027**: 系统 MUST 在工具结果超长时将其卸载到可由模型取回的存储，而非直接丢弃
- **FR-028**: 系统 MUST 在压缩发生时通过事件流通知前端
- **FR-029**: 系统 MUST 在压缩后保持 `messages` 表中的原始消息不被改写
- **FR-030**: 系统 MUST 在页面展示当前会话的 token 占用与压缩次数，并在压缩多次后提示新开会话

**RAG**

- **FR-031**: 系统 MUST 支持 Markdown、纯文本、PDF、DOCX、HTML、XLSX 六类文档的解析
- **FR-032**: 系统 MUST 按文件 hash 做增量索引，未变更的文档不重复向量化
- **FR-033**: 系统 MUST 以后台任务方式执行索引并上报进度，不阻塞 HTTP 请求
- **FR-034**: 系统 MUST 在页面展示每个文档的索引状态，失败时展示具体原因
- **FR-035**: 系统 MUST 采用 FTS5 关键词与向量双路检索，并以 RRF 融合排名
- **FR-036**: 系统 MUST 将向量以二进制形式存储，禁止使用文本序列化
- **FR-037**: 系统 MUST 将检索能力以工具形式暴露，由模型自行判断调用时机
- **FR-061**: 系统 MUST 显式处理 FTS5 的中文分词，禁止依赖默认 `unicode61` 分词器——它会把连续汉字当作单个 token，导致中文关键词召回恒为空

**记忆**

- **FR-038**: 系统 MUST 提供以 `key` 为主键的长期记忆写入工具，同 `key` 覆盖而非新增
- **FR-039**: 系统 MUST 在记忆规模较小时全量注入系统提示词，不依赖模型主动召回
- **FR-040**: 系统 MUST 在注入记忆时附带"可能已过时、以当前对话为准"的限定说明
- **FR-041**: 用户 MUST 能够在页面查看、手动新增、编辑与删除长期记忆
- **FR-042**: 系统 MUST 记录每条记忆的来源会话以便追溯

**定时任务**

- **FR-043**: 系统 MUST 提供工具使 Agent 能在对话中登记定时任务
- **FR-044**: 系统 MUST 在登记任务时回显人类可读的下次执行时间
- **FR-045**: 系统 MUST 以数据库为唯一真相源，进程重启后从数据库全量恢复调度
- **FR-046**: 系统 MUST 在错过执行时间时记录 `missed` 而不补跑
- **FR-047**: 系统 MUST 禁止同一任务的重叠执行
- **FR-048**: 系统 MUST 以受限工具集执行定时任务
- **FR-049**: 系统 MUST NOT 在任务失败后自动重试
- **FR-050**: 系统 MUST 设置任务总数上限与最小执行间隔作为熔断
- **FR-051**: 系统 MUST 对任务执行历史设置保留策略

**Web 控制台**

- **FR-052**: 页面 MUST 在左栏上半提供 RAG 文档、MCP 工具、本地 CLI 工具、技能、定时任务、长期记忆六类配置管理
- **FR-053**: 页面 MUST 在左栏下半提供会话管理
- **FR-054**: 页面 MUST 在主区域提供人机交互的输入与流式输出
- **FR-055**: 前端 MUST 以 `fetch` 加 `ReadableStream` 方式消费 SSE，不得使用仅支持 GET 的 `EventSource`
- **FR-056**: 页面 MUST 默认折叠工具调用详情
- **FR-057**: 用户 MUST 能够仅通过页面完成全部六类配置而无需编辑配置文件

**存储与并发**

- **FR-058**: 系统 MUST 使用单文件 SQLite 作为唯一存储，不依赖任何外部数据库或容器
- **FR-059**: 系统 MUST 开启 WAL 模式并串行化写入，以支撑对话、索引、定时任务的并发写
- **FR-060**: 系统 MUST 在启动时执行表结构迁移，失败时明确报错而非静默重建

### Key Entities

- **Session（会话）**：一次连续对话的容器。包含标题、创建与更新时间、归档标记、当前 token 占用、已压缩次数。与 Message 为一对多
- **Message（消息）**：会话中的一条原始消息。包含角色、正文、工具调用列表、序号。是用户可见、可导出、可审计的真相
- **SessionContext（会话上下文）**：会话的模型输入视图快照，即压缩后的消息数组。与 Message 是同一段对话的两个视角，用于恢复会话时喂给模型
- **Checkpoint（检查点）**：单次运行被中断时的状态快照，生命周期临时，与 SessionContext 无关
- **Snapshot（配置快照）**：构造一个 Agent 实例所需的全部配置的不可变副本。包含模型、工具集、技能集、记忆、限额。是"配置变更不影响进行中对话"的载体
- **McpServer（MCP 服务器配置）**：一个外部 MCP server 的连接信息。包含名称、端点、密钥环境变量名、启用状态、健康状态
- **CliTool（本地 CLI 工具）**：一条声明式工具定义。包含名称、描述、可执行文件、参数模板、超时、启用状态
- **CliPolicy（执行策略）**：自由执行工具的允许／拒绝规则集合
- **Skill（技能）**：一段描述"某类任务如何完成"的说明。包含名称、描述、正文、执行模式、可选的指定 agent 与 model。来源可以是文件系统目录或数据库
- **Document（RAG 文档）**：一个被索引的本地文件。包含路径、标题、内容 hash、大小、索引时间、状态与失败原因。与 Chunk 为一对多
- **Chunk（文档片段）**：文档切分后的最小检索单元。包含所属文档、序号、正文、token 数
- **Vector（向量）**：某个 Chunk 的稠密向量表示，以二进制形式存储。与 Chunk 为一对一
- **Memory（长期记忆）**：一条跨会话的结构化事实。以语义化 `key` 为主键，包含正文、分类、来源会话、创建与更新时间
- **ScheduledTask（定时任务）**：一条定时执行的 Agent 任务。包含名称、cron 表达式、要执行的提示词、启用状态、是否保留上下文、绑定会话、工具范围、上次与下次执行时间
- **TaskRun（任务执行记录）**：定时任务的一次执行。包含开始与结束时间、状态（成功／失败／跳过／错过）、结果正文、错误信息

## Success Criteria *(mandatory)*

### Measurable Outcomes

**可用性**

- **SC-001**: 在已设置模型密钥的机器上，从 `git clone` 到发起第一轮对话，用户无需安装 Docker、数据库或任何外部服务，全部操作不超过两条命令
- **SC-002**: 用户无需编辑任何配置文件，仅通过页面即可完成 RAG 文档、MCP 工具、本地 CLI 工具、技能、定时任务、长期记忆六类配置的全部管理
- **SC-003**: 任意一类配置变更后，无需重启程序即可在下一轮对话中生效
- **SC-004**: 新增一项 Agent 能力（新技能或新 MCP server）不需要修改任何 Go 代码

**正确性**

- **SC-005**: 程序重启后，全部会话、消息历史、技能、工具配置、长期记忆、定时任务均完整恢复，无数据丢失
- **SC-006**: 一个经过多轮压缩的长会话，其页面展示的历史始终是完整原始内容，不因压缩而缺失
- **SC-007**: 在对话、RAG 索引、定时任务三方并发写入的场景下，不出现 `database is locked` 或数据损坏
- **SC-008**: 定时任务在进程被强制终止后重启，调度状态与下次执行时间正确恢复

**性能**

- **SC-009**: 万级 chunk 规模下，单次混合检索的端到端耗时在 100 毫秒以内
- **SC-010**: 构造一个 Agent 实例的耗时相对单次模型调用可忽略（毫秒级以内）
- **SC-011**: 重新加载 RAG 索引时，未变更文档产生的 embedding 调用次数为零

**安全**

- **SC-012**: 模型通过任何工具路径均无法读取到 `ZHIPUAI_API_KEY` 等父进程环境变量
- **SC-013**: 服务不在回环地址以外的网络接口上可达
- **SC-014**: 定时任务执行时，自由执行工具与写操作类工具不在其可用工具集中

**可维护性**

- **SC-015**: 现有 `cmd/*`、`demo/*` 目录下的全部代码零改动，既有 demo 仍可正常运行
- **SC-016**: 长期记忆中的任何一条错误事实，用户都能在页面上定位并删除
- **SC-017**: RAG 索引失败的文档，用户能在页面上看到具体失败原因而非静默跳过
