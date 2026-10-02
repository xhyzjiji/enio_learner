---
description: "Implementation plan template for feature development"
---

# Implementation Plan: 通用 Agent 运行时与 Web 控制台

**Feature**: `20260930-general-agent-runtime` | **Date**: 2026-09-30 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/20260930-general-agent-runtime/spec.md`

**Note**: 本文档由 `/adk-sdd-ff` 快速通道生成，设计依据为 [general_agent_brainstorm.md](../brainstorm/general_agent_brainstorm.md)。

## Summary

在现有 Eino 学习 demo 仓库之上新建一个可长期使用的通用 Agent：命令行启动、Web 界面交互，具备 MCP 与本地 CLI 双通道工具调用、技能驱动、本地文档 RAG、短期与长期记忆、会话管理与定时任务。

技术路线的两条主线：

**一、会话级 Agent 工厂。** Eino 的 `ChatModelAgent` 在构造时固定工具集，与"页面热更新配置"直接冲突。化解方式是每次对话按配置快照构造新实例，而非维护全局单例热重建——构造 Agent 是纯内存操作，代价远低于全局单例带来的并发竞态。

**二、最大化复用 Eino 内置中间件。** `adk/middlewares/` 下的 `skill`、`reduction`、`summarization`、`filesystem`、`toolsearch`、`plantask` 六件套覆盖了技能、上下文治理、文件与命令工具等大半需求，配合 `eino-ext` 的 parser／splitter／reranker／embedding 组件，把自研范围压缩到六项：向量索引与检索、Session 持久化、长期记忆、`CheckPointStore` 实现、MCP 多 server 管理、cron 调度，外加 HTTP 层与前端。

## Technical Context

**Language/Version**: Go 1.25（后端）、TypeScript 5.x（前端）
**Primary Dependencies**:
- 已有：`github.com/cloudwego/eino v0.9.13`、`eino-ext/components/model/openai`、`eino-ext/components/tool/mcp v0.0.8`、`github.com/mark3labs/mcp-go v0.43.0`
- 新增（Go）：`modernc.org/sqlite`（纯 Go 无 CGO）、`github.com/robfig/cron/v3`、`eino-ext/components/embedding/openai`、`eino-ext/components/document/parser/{pdf,docx,html,xlsx}`、`eino-ext/components/document/transformer/splitter/{markdown,recursive}`、`eino-ext/components/document/transformer/reranker/score`
- 新增（前端）：React 18、Vite、Tailwind CSS、shadcn/ui、zustand

**Storage**: SQLite 单文件（WAL 模式）。向量以 `float32` 打包为 BLOB 存于同库，检索时载入内存做余弦；关键词一侧用 SQLite 内置 FTS5 虚拟表。不引入任何外部数据库或向量数据库。

**Testing**: `go test`（标准库 + `testify`，仓库已有该依赖）。单元测试一律通过 `bits-unit-test-gen` skill 生成与执行。

**Target Platform**: macOS / Linux 本地单机。仅监听 `127.0.0.1`。

**Project Type**: web（Go 后端 + React 前端，但前端构建产物 `go:embed` 进二进制，最终为单进程单文件）

**Performance Goals**:
- 单次混合检索端到端 < 100ms（万级 chunk 规模）
- 构造一个 Agent 实例 < 1ms（纯内存操作）
- 流式输出首 token 延迟不引入除模型本身外的额外开销

**Constraints**:
- 零外部服务依赖：不需要 Docker、不需要独立数据库、不需要单独的前端进程
- 仅回环地址可达，无鉴权层
- 模型密钥仅来自环境变量，禁止持久化
- 上下文窗口 128K（`glm-4.5-air`），`reduction` 约 60K 触发、`summarization` 约 80K 触发

**Scale/Scope**:
- RAG：万级 chunk 以内
- 会话：数百个会话、单会话数百轮消息
- 工具：数十个（超阈值时由 `toolsearch` 接管可见性）
- 长期记忆：约 50 条以内走全量注入，之上转向量检索
- 代码规模：Go 侧约 45 个新文件，前端约 25 个

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

本仓库不存在 `docs/CONSTITUTION.md` 或 `.ttadk/memory/constitution.md`，无项目宪章约束。适用的是用户级规则与本仓库既有约定：

| 约束来源 | 内容 | 本方案的遵循方式 |
| --- | --- | --- |
| 用户规则 | 注释须中英文双语同时存在 | 所有新增 Go 代码的包注释、导出标识符注释、关键决策注释均双语书写，与 `internal/skill`、`internal/mcpclient` 现有风格一致 |
| 仓库既有约定 | 密钥只走环境变量，不硬编码 | `ZHIPUAI_API_KEY` 沿用 `internal/llm/model.go` 的读取方式；MCP 密钥以环境变量名引用形式落库 |
| 仓库既有约定 | 密钥不作为命令行参数（`ps` 可见） | 沿用 `cmd/gitmcp` 的做法 |
| 仓库既有约定 | 外部命令参数以切片传递，不拼 shell 字符串 | 沿用 `internal/gitmcp` 的做法 |
| 仓库既有约定 | 工具返回结果必须设上限 | `MaxResultBytes` 与 `reduction` 中间件双重保障 |
| 用户明确要求 | 新 Agent 代码不与现有 demo 混淆 | 新增代码全部收在 `cmd/agent/`、`internal/agent/`、`web/` 三棵子树下；`cmd/*`、`demo/*` 零改动 |
| 用户明确决定 | 暂不处理密钥泄露的深度防护 | 仅保留零成本项（子进程环境变量白名单），不做日志脱敏与工具返回值过滤 |

**Gate 结论**：通过。无违反项，`Complexity Tracking` 一节留空。

## Project Structure

### Documentation (this feature)

```
specs/20260930-general-agent-runtime/
├── plan.md              # 本文件
├── spec.md              # 功能规格（7 个 User Story、60 条 FR、17 条 SC）
└── tasks.md             # 任务拆解
```

设计依据在 `specs/brainstorm/general_agent_brainstorm.md`。本 feature 走 ff 快速通道，不单独产出 `research.md`、`data-model.md`、`quickstart.md`、`contracts/`——数据模型与接口契约直接写入本文件。

### Source Code (repository root)

```
cmd/
└── agent/
    └── main.go                    # 唯一新增入口；现有 11 个 cmd 子目录零改动

internal/
├── agent/                         # 新 Agent 的全部内部实现，与现有 internal 包平级但互不干扰
│   ├── config/
│   │   └── config.go              # 运行时配置加载与变更
│   ├── store/
│   │   ├── db.go                  # SQLite 打开、WAL、写串行化
│   │   ├── migrate.go             # 表结构迁移
│   │   ├── schema.sql             # 建表语句
│   │   ├── checkpoint.go          # adk.CheckPointStore 实现
│   │   └── offload.go             # reduction.Backend 实现
│   ├── session/
│   │   ├── session.go             # 会话 CRUD
│   │   ├── messages.go            # 消息读写、tool_calls 序列化
│   │   ├── context.go             # 模型输入视图快照与恢复
│   │   └── title.go               # 标题异步生成
│   ├── memory/
│   │   ├── store.go               # memories 表 CRUD
│   │   ├── tool.go                # remember 工具
│   │   └── inject.go              # 分档注入
│   ├── rag/
│   │   ├── loader.go              # 目录扫描与上传，按扩展名派发解析器
│   │   ├── split.go               # 切分器选择
│   │   ├── embedder.go            # embedding/openai 指向智谱
│   │   ├── indexer.go             # indexer.Indexer 实现
│   │   ├── retriever.go           # retriever.Retriever 实现，FTS5 + 向量 + RRF
│   │   ├── tool.go                # search_docs 工具
│   │   └── manager.go             # 文档管理与索引进度
│   ├── tools/
│   │   ├── registry.go            # 统一装配、冲突检测、范围过滤
│   │   ├── mcp/
│   │   │   └── manager.go         # 多 server 管理
│   │   └── cli/
│   │       ├── declarative.go     # 动态构造声明式工具
│   │       ├── exec.go            # 执行、环境白名单、超时、截断
│   │       └── shell.go           # filesystem.Shell + CommandValidator
│   ├── skills/
│   │   ├── backend.go             # skill.Backend 实现
│   │   └── store.go               # 技能 CRUD 与目录扫描缓存
│   ├── schedule/
│   │   ├── scheduler.go           # cron 调度
│   │   ├── store.go               # 任务与执行记录
│   │   ├── tool.go                # schedule_task 工具
│   │   └── runner.go              # 受限执行
│   ├── kernel/
│   │   ├── snapshot.go            # 配置快照
│   │   ├── factory.go             # Build(ctx, Snapshot) → adk.Agent
│   │   └── context.go             # reduction / summarization 挂载
│   └── httpapi/
│       ├── server.go              # 路由（net/http.ServeMux）
│       ├── stream.go              # AgentEvent → SSE
│       ├── static.go              # go:embed web/dist + SPA fallback
│       ├── chat.go
│       ├── session.go
│       ├── rag.go
│       ├── tools.go
│       ├── skills.go
│       ├── memory.go
│       └── task.go
├── llm/                           # 复用，不改动
├── mcpclient/                     # 复用，不改动
├── skill/                         # 保留供现有 demo 使用，新 Agent 不引用
├── gitmcp/                        # 保留，可作为联调用 MCP server
├── agentio/                       # 保留
└── democli/                       # 保留

web/                               # 前端独立目录，独立构建
├── src/
│   ├── components/
│   │   ├── Sidebar/{ConfigPanel,SessionList}.tsx
│   │   └── Chat/{MessageList,ToolCallCard,Composer,EmptyState}.tsx
│   ├── api/                       # REST 封装 + SSE 解析
│   └── store/                     # zustand
├── package.json
└── vite.config.ts
```

**Structure Decision**: 采用"新 Agent 自成子树"的布局。所有新增 Go 代码收敛在 `internal/agent/` 这一棵子树下，入口收敛在 `cmd/agent/`，前端收敛在 `web/`。现有的 `cmd/*`（11 个 demo 入口）与 `demo/*`（7 个 demo 实现）零改动，`internal/` 下的既有包中仅 `llm` 与 `mcpclient` 被新 Agent 以只读方式复用，其余保持原样供 demo 继续使用。

这一布局直接回应用户"新增一套 `cmd/agent`，不要跟现有的代码混淆"的要求：任何人看 `internal/agent/` 就知道这是生产级 Agent，看 `demo/` 就知道这是学习示例，两者不会互相污染，也不存在"改 demo 把 Agent 改坏"的风险。

`internal/skill` 被内置的 `adk/middlewares/skill` 取代，但**不删除**——现有 `demo/skillagent` 仍依赖它，且它作为"自己实现渐进披露"的教学示例仍有价值。

## Complexity Tracking

*Fill ONLY if Constitution Check has violations that must be justified*

无。Constitution Check 全部通过，不存在需要论证的复杂度引入。

## 关键技术决策

### D1：会话级 Agent 工厂，而非全局单例热重建

`ChatModelAgent` 的 `ToolsConfig` 在构造后不可变，而需求要求页面上增删 MCP server／CLI 工具／技能后即时生效。两条路：

| | 做法 | 风险 |
| --- | --- | --- |
| 采纳 | 每次对话按配置快照构造新实例 | 每次对话多一次构造（纯内存，微秒级） |
| 否决 | 维护全局单例，配置变更时加写锁重建 | 重建时若有多个对话正在跑，旧 agent 与新配置的归属混乱；事件流已建立却换掉工具集。此类 bug 只在边用边改配置时偶发，复现极难 |

构造开销相对一次数百毫秒的模型网络调用可忽略，为省掉微秒级操作而引入竞态是典型的错误优化。快照法还白送"不同 session 用不同模型与工具子集"的能力，定时任务的受限工具集正是靠它实现。

### D2：中间件挂载顺序

顺序不可调整，错序不报错但效果变差且难归因：

`skill` → `filesystem` → `plantask` → `toolsearch` → `reduction` → `summarization`

- `filesystem` 必须早于 `reduction`：卸载的内容要靠 `read_file` 取回
- `toolsearch` 必须在全部工具注册完之后：它控制的是已注册工具的可见性
- `reduction` 必须早于 `summarization`：先做**可逆**的工具结果卸载，再做**有损**的历史摘要。反序会在还有大量工具结果可卸载时就把对话历史压没

### D3：双视图消息持久化

`summarization` 压缩后历史被摘要替换。只存一份必然二选一地牺牲：只存压缩后则页面看不到原始对话；只存原始则每次恢复会话都要重新压缩（浪费且两次摘要结果不同）。

故 `messages` 存原始完整消息（页面展示、导出、审计），`session_context` 存压缩后的模型输入快照（恢复会话时喂给模型）。两者是同一段对话的两个视角。

`tool_calls` 的 ID 必须原值保留，否则 assistant 消息中的工具调用与后续 tool 消息对不上。

### D4：动态工具不能用 `utils.InferTool`

`InferTool[T, D]` 依赖 Go 泛型在**编译期**推导 JSON Schema，而 CLI 工具是运行时在页面定义的。必须改走 `utils.NewTool[map[string]any, string](toolInfo, handler)` 配 `schema.NewParamsOneOfByParams` 手工构造参数描述。这是实现上的关键分叉，走错需整体返工。

### D5：不引入向量数据库

万级 chunk、1024 维 float32 约 40MB 内存，全量余弦在 10ms 量级。Milvus standalone 空跑即占 1.5~2.5GB，且 Milvus Lite 仅有 Python SDK（Go 必须起 Docker）；Qdrant 虽轻（单容器约 100MB）但仍是额外依赖。本地文档库规模远达不到需要它们的程度，引入即过度设计。

`indexer.Indexer` 与 `retriever.Retriever` 各只有一个方法，自研成本很低。

### D6：混合检索 + RRF 融合，并显式处理中文分词

本地文档与代码含大量专有标识符，纯向量在"精确匹配某个词"上劣于关键词检索。SQLite 自带 FTS5，零额外依赖。

融合用 RRF 而非加权求和：余弦相似度与 BM25 **量纲完全不同**，加权相加没有意义；RRF 只用排名不用分数，天然规避。

**分词是本项的隐藏陷阱**。FTS5 默认的 `unicode61` 分词器按 Unicode 字符类别判定 token 边界，汉字属于 `Lo`（字母类），于是一整句连续汉字被切成单个 token，中文查询几乎恒返回空结果。它**不报错**，只是静默失效——开发期若用英文标识符测试则完全察觉不到，直到真实中文文档入库才暴露。两条可行路径：

| | 做法 | 代价 | 质量 |
| --- | --- | --- | --- |
| 默认采用 | 建表时指定 `tokenize = 'trigram'` | 零依赖；索引体积增大 | 三字滑窗，中文召回可用，BM25 相关性偏粗 |
| 备选 | 用纯 Go 分词器（如 `gse`）预切分，写入以空格分隔的 token 列，仍用 `unicode61` | 新增一个依赖及其内置词典 | 词级切分，相关性明显更好 |

先走 `trigram`：零依赖、一行 DDL、召回可用，符合"不为尚未证实的需求引入依赖"的原则。若实测中文召回质量不足，再升级到预切分方案——届时只需改 `indexer.go` 的写入与 `retriever.go` 的查询构造，表结构与检索融合逻辑都不动。

#### T083 实测结论（2026-09-30）：`trigram` 可用，但查询构造必须改

以纯中文文档实测后确认 **不需要**升级到 `gse`，但**必须**改查询侧的构造方式。直接把查询词加引号丢给 `MATCH` 时，出现了两类恒空：

| 查询 | 原构造（整词加引号作短语） | 原因 | 修正后 |
| --- | --- | --- | --- |
| `分词` | 0 条 | 两字词短于 trigram 的三字窗口，索引里根本没有对应 token | 1 条（LIKE 兜底） |
| `如何配置定时任务？` | 0 条 | 作为短语要求文档原样出现这八个字，文档实际写的是"定时任务通过 cron…" | 1 条（三元组 OR） |
| `分词器`、`余弦相似度` | 1 条 | —— | 1 条 |

修正落在 `retriever.go` 的 `ftsQuery`：

- 中文串按**重叠三元组**展开再 OR（`如何配置定时任务` → `"如何配" OR "何配置" OR … OR "时任务"`），靠 `定时任`/`时任务` 命中
- 短于三字的中文词无法经 FTS 表达，收集起来走 `likeSearch` 子串兜底，与 FTS 结果合并去重
- 非中文词仍整体加引号作单 token

这条结论的价值在于：`tokenize = 'trigram'` 只解决了**写入侧**的中文切分，查询侧若沿用"整词作短语"的惯常写法，中文召回仍然恒空且不报错——与用默认 `unicode61` 的失败表现完全一致。

### D7：长期记忆的 key 覆盖与分档召回

`key` 作主键使"更新"与"新增"自然区分，避免模型反复记录同一事实的不同措辞导致记忆表膨胀矛盾。

召回分档：小规模全量注入 system prompt（不提供 `recall` 工具），大规模才转向量检索。理由是 `recall` 最大的失败模式是"模型压根不调它"，而全量注入根本不存在该失败模式。

### D8：定时任务以数据库为唯一真相源

增删改一律"先写库、再同步调度器"。内存维护 `任务 ID → cron EntryID` 映射（`robfig/cron` 用自己的 `EntryID` 标识调度项）。进程被 `kill -9` 后全量恢复，不丢数据。

### D9：存储用 SQLite，不用本地 MySQL

MySQL 唯一实打实的收益是行级锁免去写串行化，即风险 3 可以直接删掉。但该问题的实际体量是：单人本地工具，写并发峰值不过"后台索引任务 + 一个对话"，WAL 模式下读不阻塞写，串行化排队在微秒量级。为此付出的代价：

| 维度 | SQLite | 本地 MySQL |
| --- | --- | --- |
| 启动 | `go run ./cmd/agent` | 需先确保 MySQL 服务在跑，建库建用户配 DSN |
| 常驻内存 | 进程内，近乎为零 | 约 400MB 量级 |
| 备份 | 拷一个文件 | `mysqldump` |
| 密钥面 | 无 | DSN 密码为新增密钥，同样不能落配置文件 |
| 全文检索 | FTS5 内建 | `FULLTEXT` 需 `WITH PARSER ngram`，固定 N 元切分，质量弱于词级切分 |
| Go 驱动 | `modernc.org/sqlite` 纯 Go | `go-sql-driver/mysql` 亦纯 Go，此项持平 |
| schema | 已定稿 | 16 张表 DDL 全部重写方言 |

MySQL 开始划算的条件是多人共用同一实例、数据量至千万行级、或需第三方工具直连查库做分析。当前三者均不成立，换库是纯粹的成本增加。

## T005 闸门实测结论（2026-09-30）

用一个脚本化的假 ChatModel 离线驱动完整 Agent 循环验证（无需网络与密钥）。六个中间件的可用性成立，但有五处与原假设不符，已按实测修正：

| # | 原假设 | 实测 | 影响 |
| --- | --- | --- | --- |
| 1 | 用 `ChatModelAgentConfig.Middlewares` 挂载 | 该字段**已废弃**；应使用 `Handlers []adk.ChatModelAgentMiddleware`，六个中间件的 `New()` 恰好返回该类型，按注册顺序生效 | D2 顺序结论不变，挂载字段改名 |
| 2 | `skill.Config.UseChinese` 开启中文 | 该字段**已废弃**；改用全局 `adk.SetLanguage(adk.LanguageChinese)` | 在 `cmd/agent` 启动时设置一次 |
| 3 | `reduction.Backend` 是通用 KV，可落库 | 实为 `Write(ctx, *filesystem.WriteRequest) error`，即**文件形状**；卸载后模型靠 `ReadFileToolName`（默认 `read_file`）取回 | **`filesystem`、`reduction`、`plantask` 必须共用同一个 Backend 实例**，否则 `read_file` 取不回卸载内容。D2 的顺序约束由此从"建议"变成"硬约束" |
| 4 | `plantask` 无需额外依赖 | `Config.Backend` **必填**，接口为 `LsInfo`/`Read`/`Write`/`Delete`，同样是文件形状 | 同上，复用同一后端并额外实现 `Delete` |
| 5 | 工具经 `model.WithTools` 方法绑定 | 实际通过 `model.WithTools` **调用选项**在每次请求时传入 | 自研模型包装器需从 `model.GetCommonOptions` 读取，而非实现 `WithTools` 方法 |

已确认成立的假设：技能目录渲染进 **skill 工具的 description**（非 system prompt），且 `Backend.List` 在每轮 `Info(ctx)` 中被重新调用，**热重载天然成立无需额外机制**；`ToolCall.ID` 在事件流中保持原值；`utils.NewTool[map[string]any, string]` 配 `schema.NewParamsOneOfByParams` 可在运行时构造工具（T030 闸门同步通过）。

实测注册到模型的工具全集为：`ls`、`read_file`、`write_file`、`edit_file`、`glob`、`grep`、`execute`（随 `Shell` 出现）、`TaskCreate`、`TaskGet`、`TaskUpdate`、`TaskList`、`skill`，加上自行注册的业务工具。注意 `filesystem` 提供的是**七**件套（含 `ls`），非原先记的六件。

### 关于 `eino-ext/adk/backend/local`

原计划复用它以获得 PDF 多模态读取与 `Execute`。阅读源码后**不予采用**，两处硬伤：

1. `Execute` 走 `exec.CommandContext(ctx, "/bin/sh", "-c", cmd)` 且**不设置 `cmd.Env`**，子进程继承父进程全部环境变量——模型只要执行 `env` 就能读到 `ZHIPUAI_API_KEY`。
2. 所有路径方法只做 `filepath.Clean`，**没有任何根目录限制**，模型给什么路径就读写什么路径。

故自行实现一个路径受限、环境变量白名单的 `filesystem.Backend`，同时服务 `filesystem`/`reduction`/`plantask` 三个中间件。

## 数据模型

全部表位于单个 SQLite 文件，开启 WAL 模式。

| 表 | 关键字段 | 说明 |
| --- | --- | --- |
| `sessions` | `id`、`title`、`created_at`、`updated_at`、`archived`、`token_usage`、`compact_count` | 会话元信息。`compact_count` 支撑"压缩多次后提示新开会话" |
| `messages` | `id`、`session_id`、`seq`、`role`、`content`、`tool_calls`(JSON)、`created_at` | **原始完整消息**。`(session_id, seq)` 唯一索引 |
| `session_context` | `session_id`、`payload`(JSON)、`updated_at` | **压缩后的模型输入快照**，与会话一对一 |
| `checkpoints` | `id`、`data`(BLOB)、`created_at` | `adk.CheckPointStore` 的 `Get`/`Set` 落点，用完即删 |
| `memories` | `key`(PK)、`content`、`category`、`source_sid`、`created_at`、`updated_at` | 长期记忆，同 `key` 覆盖 |
| `rag_documents` | `id`、`path`、`title`、`hash`、`size`、`indexed_at`、`status`、`error` | `hash` 支撑增量索引；`error` 支撑失败原因可见 |
| `rag_chunks` | `id`、`doc_id`、`ordinal`、`content`、`token_count` | 切分片段 |
| `rag_vectors` | `chunk_id`(PK)、`dim`、`vector`(BLOB) | `float32` 打包，非 JSON |
| `rag_chunks_fts` | FTS5 虚拟表，索引 `rag_chunks.content`，**建表须显式 `tokenize = 'trigram'`** | 混合检索的关键词一侧；用默认 `unicode61` 会导致中文召回恒空，见 D6 |
| `mcp_servers` | `id`、`name`、`endpoint`、`secret_ref`、`enabled`、`health`、`last_checked_at` | `secret_ref` 存环境变量名，非明文密钥 |
| `cli_tools` | `id`、`name`、`description`、`command`、`args_template`(JSON)、`timeout_sec`、`enabled` | 声明式工具定义 |
| `cli_policy` | `id`、`pattern`、`action` | `execute` 工具的允许／拒绝规则 |
| `skills` | `name`(PK)、`description`、`body`、`context_mode`、`agent`、`model`、`updated_at` | 页面创建的技能；与文件系统目录合并时优先级更高 |
| `scheduled_tasks` | `id`、`name`、`cron`、`prompt`、`enabled`、`keep_context`、`session_id`、`tool_scope`、`last_run_at`、`next_run_at` | 调度的唯一真相源 |
| `task_runs` | `id`、`task_id`、`started_at`、`finished_at`、`status`、`result`、`error` | `status` ∈ {success, failed, skipped, missed}；需保留策略 |
| `config_kv` | `key`(PK)、`value` | 模型名、阈值等杂项配置 |

**并发写策略**：对话、RAG 索引、定时任务三方并发写。开启 WAL 后读不阻塞写，但 SQLite 仍是单写者模型，故在 `store/db.go` 层用单一写连接 + 互斥串行化所有写操作，读连接池独立。

## 接口契约

| 分区 | 方法与路径 | 说明 |
| --- | --- | --- |
| 对话 | `POST /api/chat` | 请求体含 `session_id` 与消息内容；响应为 SSE 流 |
| | `POST /api/chat/interrupt` | 通过 context 取消对应 run |
| 会话 | `GET /api/sessions` | 列表 |
| | `POST /api/sessions` | 新建 |
| | `PATCH /api/sessions/{id}` | 重命名、归档 |
| | `DELETE /api/sessions/{id}` | 删除 |
| | `GET /api/sessions/{id}/messages` | 原始消息历史 |
| RAG | `GET /api/rag/documents` | 文档列表含状态与失败原因 |
| | `POST /api/rag/documents` | 上传 |
| | `DELETE /api/rag/documents/{id}` | 删除并清理 chunk 与向量 |
| | `POST /api/rag/reindex` | 触发增量重建（后台任务） |
| | `GET /api/rag/status` | 索引进度 |
| MCP | `GET/POST/PATCH/DELETE /api/tools/mcp` | 配置增删改查 |
| | `POST /api/tools/mcp/{id}/test` | 连通性测试，返回工具清单 |
| CLI | `GET/POST/PATCH/DELETE /api/tools/cli` | 声明式工具增删改查 |
| | `GET/PUT /api/tools/cli/policy` | `execute` 策略 |
| | `GET /api/tools` | 当前生效的工具全集（含冲突标注） |
| 技能 | `GET/POST/PATCH/DELETE /api/skills` | 技能增删改查 |
| | `POST /api/skills/reload` | 目录扫描缓存失效 |
| 记忆 | `GET/POST/PATCH/DELETE /api/memories` | 长期记忆增删改查 |
| 定时任务 | `GET/POST/PATCH/DELETE /api/tasks` | 任务增删改查 |
| | `POST /api/tasks/{id}/run` | 立即执行一次 |
| | `GET /api/tasks/{id}/runs` | 执行历史 |

SSE 事件格式：`event: <type>\ndata: <json>\n\n`，`type` ∈ {`message_delta`, `tool_call`, `tool_result`, `compression`, `error`, `done`}。

路由使用标准库 `net/http.ServeMux`——Go 1.22 起原生支持 `POST /api/sessions/{id}` 形式的方法与路径参数，无需第三方路由库。当前工程依赖干净，不为此破例。

## 交付顺序与 Story 映射

七个 User Story 对应七个交付阶段，每阶段结束均为可运行、可验证状态。

| 阶段 | User Story | 完成标志 |
| --- | --- | --- |
| 1 | US1 对话内核与会话持久化 | 能对话、能存历史、重启不丢；**中间件行为验证通过** |
| 2 | US2 工具系统 | 页面配工具，对话中可用，禁用后不重启即失效 |
| 3 | US3 技能与上下文治理 | 技能自动匹配执行，长会话不爆上下文 |
| 4 | US4 本地文档 RAG | 本地文档可检索，增量索引生效 |
| 5 | US5 长期记忆 | 跨会话记忆生效，页面可编辑 |
| 6 | US6 定时任务 | 对话中可登记，到点自动执行，重启不丢 |
| 7 | US7 配置管理控制台 | 六组配置全部可视化管理 |

US1 承担的**中间件行为验证**是整个计划的前置闸门。六个内置中间件的能力来自源码阅读推断，`fork` 模式的实际表现、`reduction` 与 `filesystem` Backend 的协作、`UseChinese` 的效果均未实测。这是地基假设，须在写大量代码之前用最小样例证伪或证实。

## 风险与缓解

| # | 风险 | 影响 | 缓解方式 |
| --- | --- | --- | --- |
| 1 | 上下文压缩链路的正确性 | **最高**。`messages` 与 `session_context` 不一致时，页面显示正常但模型看到错乱历史，极难察觉 | 专门的一致性测试：构造超阈值会话，压缩后校验两份视图的语义对应关系；压缩事件须在 SSE 中可观测 |
| 2 | Eino 中间件实际行为与推断不符 | **高**。地基假设塌陷需重做设计 | 置于 US1 最前，用最小样例验证六个中间件；若 `fork` 行为不符预期，退化为 `inline` 并接受上下文膨胀 |
| 3 | SQLite 并发写 `database is locked` | **中**。开发期单人单会话完全不出现，投入使用后才暴露 | WAL 模式 + 单写连接串行化；专门构造"索引进行中同时对话"的并发测试 |
| 4 | Embedding 成本与隐私 | **中**。文档全文出本机；大批量索引消耗配额 | 增量索引按 hash 跳过；`Embedder` 保持接口抽象，需要时可切 Ollama 本地模型 |
| 5 | 定时任务自我繁殖 | **中**。Agent 可登记"每分钟执行且每次登记新任务"的任务 | 任务总数上限 + 最小执行间隔熔断；定时任务用受限工具集 |
| 6 | 动态工具构造走错技术路线 | **中**。误用 `InferTool` 需整体返工 | 已在 D4 明确；US2 开工前先写一个最小的 `NewTool` + `NewParamsOneOfByParams` 样例验证 |
| 7 | FTS5 中文分词静默失效 | **中**。不报错，只是关键词召回恒空，混合检索退化为纯向量；用英文标识符测试完全察觉不到 | 建表显式指定 `tokenize = 'trigram'`（D6）；验收场景中强制包含"纯中文文档 + 中文词语查询"一例 |

用户已明确将"密钥泄露深度防护"降级：仅保留零成本的子进程环境变量白名单，不做日志脱敏与工具返回值过滤。

## 依赖引入清单

需要新增到 `go.mod` 的模块：

```
modernc.org/sqlite                                              # 纯 Go SQLite，无 CGO
github.com/robfig/cron/v3                                       # cron 调度
github.com/cloudwego/eino-ext/components/embedding/openai       # Embedding
github.com/cloudwego/eino-ext/components/document/parser/pdf    # PDF 解析
github.com/cloudwego/eino-ext/components/document/parser/docx
github.com/cloudwego/eino-ext/components/document/parser/html
github.com/cloudwego/eino-ext/components/document/parser/xlsx
github.com/cloudwego/eino-ext/components/document/transformer/splitter/markdown
github.com/cloudwego/eino-ext/components/document/transformer/splitter/recursive
github.com/cloudwego/eino-ext/components/document/transformer/reranker/score
```

`eino-ext` 的各组件是**独立的 Go module**（各自带 `go.mod`），需逐个 `go get`，不能只引根模块。

前端依赖由 `web/package.json` 独立管理，与 Go 侧无耦合。
