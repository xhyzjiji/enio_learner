---
description: "Task list template for feature implementation"
---

# Tasks: 通用 Agent 运行时与 Web 控制台

**Input**: Design documents from `/specs/20260930-general-agent-runtime/`
**Prerequisites**: [plan.md](./plan.md)（必需）、[spec.md](./spec.md)（必需，含 7 个 User Story）

**Tests**: 本 feature 在 plan.md 风险清单中识别出四处必须有测试覆盖的高风险区域（上下文压缩一致性、SQLite 并发写、动态工具构造、FTS5 中文分词静默失效）。这些测试任务已显式列出，全部通过 `bits-unit-test-gen` skill 完成。

**Organization**: 任务按 User Story 分组，每组可独立实现与验证。

## Format: `[ID] [P?] [Story] Description`
- **[P]**: 可并行（不同文件、无依赖）
- **[Story]**: 所属 User Story（US1~US7）
- 描述中包含确切的文件路径

## Path Conventions
- Go 后端：`cmd/agent/`、`internal/agent/`
- 前端：`web/src/`
- 现有 `cmd/*`、`demo/*`、`internal/{llm,mcpclient,skill,gitmcp,agentio,democli}` 一律不改动

---

## Phase 1: Setup（共享基础设施）

**Purpose**: 项目骨架与依赖就位

- [x] T001 创建目录骨架：`cmd/agent/`、`internal/agent/{config,store,session,memory,rag,tools,skills,schedule,kernel,httpapi}/`、`web/`
- [x] T002 引入 Go 依赖并更新 `go.mod`：`modernc.org/sqlite`、`github.com/robfig/cron/v3`、`eino-ext/components/embedding/openai`、`eino-ext/components/document/parser/{pdf,docx,html,xlsx}`、`eino-ext/components/document/transformer/splitter/{markdown,recursive}`、`eino-ext/components/document/transformer/reranker/score`（注意各组件是独立 module，需逐个 `go get`）
- [x] T003 [P] 初始化 `web/` 前端工程：Vite + React 18 + TypeScript + Tailwind + shadcn/ui + zustand，配置 dev server 代理到 `127.0.0.1:8090`
- [x] T004 [P] 在 `.gitignore` 中加入 `web/node_modules/`、`web/dist/`、`*.db`、`*.db-wal`、`*.db-shm`

---

## Phase 2: Foundational（阻塞性前置）

**Purpose**: 所有 User Story 都依赖的核心基础设施

**⚠️ CRITICAL**: 本阶段未完成前，任何 User Story 都无法开始

- [x] T005 **【前置闸门】验证 Eino 六个内置中间件的实际行为**：写一个最小可运行样例，分别验证 `skill`（含 `inline`/`fork`/`fork_with_context` 三种模式与 `UseChinese`）、`reduction`（截断与清理两阶段、与 Backend 的协作）、`summarization`、`filesystem`（工具集与 `Shell` 接口）、`toolsearch`、`plantask`。确认行为与 plan.md 的 D2 设计假设一致。**若 `fork` 模式行为不符预期，必须先回到 plan.md 调整设计再继续**
- [x] T006 在 `internal/agent/store/schema.sql` 中定义全部 16 张表（见 plan.md 数据模型一节）与必要索引。其中 `rag_chunks_fts` 虚拟表**必须显式指定 `tokenize = 'trigram'`**——FTS5 默认的 `unicode61` 分词器会把连续汉字当作单个 token，导致中文关键词召回恒为空且不报错（见 plan.md D6 与风险 7）
- [x] T007 在 `internal/agent/store/db.go` 实现 SQLite 打开：开启 WAL 模式、单一写连接 + 互斥串行化写操作、独立读连接池
- [x] T008 在 `internal/agent/store/migrate.go` 实现表结构迁移，失败时明确报错并指出数据库路径，禁止静默重建
- [x] T009 [P] 在 `internal/agent/config/config.go` 实现配置加载与运行时变更，模型密钥仅从环境变量 `ZHIPUAI_API_KEY` 读取
- [x] T010 [P] 在 `internal/agent/httpapi/server.go` 搭建路由骨架（标准库 `net/http.ServeMux`，使用 Go 1.22 的方法与路径参数语法）与统一错误响应格式
- [ ] T011 使用 `bits-unit-test-gen` skill 完成 `internal/agent/store` 的单元测试生成与执行，重点覆盖 WAL 模式下的并发写串行化（对应 plan.md 风险 3）。该 skill 的 `运行数据回传` 步骤**必须**执行，严禁跳过该步骤

**Checkpoint**: 地基就绪，数据库可用，中间件假设已验证 —— User Story 可以开始

---

## Phase 3: User Story 1 - 对话内核与会话持久化 (Priority: P1) 🎯 MVP

**Goal**: 能对话、能流式输出、能管理会话、重启不丢数据

**Independent Test**: 启动程序 → 打开页面 → 发起一轮对话 → 观察流式输出 → 新建第二个会话 → 切回第一个确认历史完整 → `Ctrl+C` 关闭后重启 → 确认全部恢复

### Implementation for User Story 1

- [x] T012 [P] [US1] 在 `internal/agent/session/session.go` 实现会话 CRUD（新建、列表、重命名、归档、删除）
- [x] T013 [P] [US1] 在 `internal/agent/session/messages.go` 实现消息读写，`tool_calls` 以 JSON 序列化，**恢复时保持 ToolCall ID 原值不变**
- [x] T014 [US1] 在 `internal/agent/session/context.go` 实现 `session_context` 模型输入视图的快照与恢复（依赖 T013）
- [x] T015 [P] [US1] 在 `internal/agent/store/checkpoint.go` 实现 `adk.CheckPointStore` 接口（`Get`/`Set`），Eino 未提供任何实现
- [x] T016 [US1] 在 `internal/agent/kernel/snapshot.go` 定义配置快照结构：模型、工具集、技能集、记忆、限额
- [x] T017 [US1] 在 `internal/agent/kernel/factory.go` 实现 `Build(ctx, Snapshot) (adk.Agent, error)`，每次对话按快照构造新实例（依赖 T016）
- [x] T018 [US1] 在 `internal/agent/httpapi/stream.go` 实现 `adk.AgentEvent` 转 SSE，事件类型 `message_delta`/`tool_call`/`tool_result`/`compression`/`error`/`done`；**工具调用事件须在流结束后补发**，因流式模式下 `ToolCalls` 要等流收完才完整
- [x] T019 [US1] 在 `internal/agent/httpapi/chat.go` 实现 `POST /api/chat`（SSE）与 `POST /api/chat/interrupt`（context 取消）
- [x] T020 [P] [US1] 在 `internal/agent/httpapi/session.go` 实现会话相关 REST 接口
- [x] T021 [P] [US1] 在 `internal/agent/session/title.go` 实现标题异步生成，不阻塞用户输入
- [x] T022 [US1] 在 `cmd/agent/main.go` 实现入口：flag `--addr`（默认 `127.0.0.1:8090`，**不提供 `0.0.0.0` 选项**）、`--db`、`--rag-dir`、`--skills-dir`；启动链路为打开 SQLite → 迁移 → 加载配置 → 挂载静态资源 → 监听；缺少 `ZHIPUAI_API_KEY` 时启动即失败并明确提示
- [x] T023 [US1] 在 `internal/agent/httpapi/static.go` 实现 `go:embed web/dist` 与 SPA fallback
- [x] T024 [P] [US1] 在 `web/src/api/` 实现 REST 封装与 SSE 解析，**必须用 `fetch` + `ReadableStream`，不得用仅支持 GET 的 `EventSource`**
- [x] T025 [P] [US1] 在 `web/src/store/` 用 zustand 建立会话列表与当前消息流的状态管理
- [x] T026 [US1] 在 `web/src/components/Chat/` 实现 `MessageList`、`Composer`、`EmptyState`（依赖 T024、T025）
- [x] T027 [US1] 在 `web/src/components/Sidebar/SessionList.tsx` 实现会话列表与新建按钮
- [ ] T028 [US1] 使用 `bits-unit-test-gen` skill 完成 `internal/agent/session` 的单元测试生成与执行，重点覆盖 `messages` 与 `session_context` 双视图的一致性、ToolCall ID 的原值保留（对应 plan.md 风险 1）。该 skill 的 `运行数据回传` 步骤**必须**执行，严禁跳过该步骤

**Checkpoint**: User Story 1 完整可用，已是一个可独立交付的本地 AI 对话工具（MVP）

---

## Phase 4: User Story 2 - 工具系统：MCP 与本地 CLI (Priority: P2)

**Goal**: 页面配置 MCP server 与本地 CLI 工具，对话中即时可用，禁用后不重启即失效

**Independent Test**: 页面添加一个 MCP server → 测试连通性看到工具清单 → 对话中让 Agent 用该工具 → 页面禁用该 server → 新对话中确认工具已消失；添加一个声明式 CLI 工具重复验证

### Implementation for User Story 2

- [x] T029 [US2] 在 `internal/agent/tools/mcp/manager.go` 实现多 MCP server 管理：连接池、按需重连、健康状态、连通性测试返回工具清单。复用 `internal/mcpclient` 但**不修改**它，在其上加管理层
- [x] T030 [US2] **动态工具构造技术验证**：写最小样例确认 `utils.NewTool[map[string]any, string]` 配 `schema.NewParamsOneOfByParams` 可在运行时构造工具。**严禁使用 `utils.InferTool`**，它依赖编译期泛型推导，无法用于运行时定义的工具（对应 plan.md 风险 6）
- [x] T031 [US2] 在 `internal/agent/tools/cli/declarative.go` 基于 T030 的结论，从 `cli_tools` 表记录动态构造 `tool.BaseTool`
- [x] T032 [US2] 在 `internal/agent/tools/cli/exec.go` 实现子进程执行：`exec.CommandContext` + **参数以切片传递，严禁拼接 shell 字符串**；`cmd.Env` **必须使用白名单**（仅 `PATH`、`HOME`、`LANG`），严禁继承父进程全部环境变量，否则模型可通过 `env` 读到 `ZHIPUAI_API_KEY`；超时终止；输出按 `MaxResultBytes` 截断并标注
- [x] T033 [US2] 在 `internal/agent/tools/cli/shell.go` 接入 `filesystem` 中间件的 `Shell` 接口提供 `execute` 自由执行工具，实现 `CommandValidator` 按 `cli_policy` 表校验。**该工具默认关闭**，需页面显式开启
- [x] T034 [US2] 在 `internal/agent/tools/registry.go` 实现统一装配：合并 MCP 工具、声明式 CLI 工具、`filesystem` 文件工具、内置工具；**同名冲突时以来源前缀消歧并在页面标注**；支持按工具范围过滤（供定时任务使用）
- [x] T035 [US2] 将 T034 的工具装配接入 `internal/agent/kernel/factory.go`，使配置变更在下一轮对话即生效（依赖 T017、T034）
- [x] T036 [P] [US2] 在 `internal/agent/httpapi/tools.go` 实现 MCP 与 CLI 的配置 REST 接口，含 `POST /api/tools/mcp/{id}/test` 与 `GET /api/tools`。**密钥字段只接受环境变量名（`secret_ref`），明文密钥一律拒绝写入数据库**
- [x] T037 [P] [US2] 在 `web/src/components/Chat/ToolCallCard.tsx` 实现工具调用卡片：工具名、参数、结果、耗时，可折叠
- [x] T038 [US2] 在 `web/src/components/Sidebar/ConfigPanel.tsx` 实现 MCP 与本地 CLI 两个配置分区
- [ ] T039 [US2] 使用 `bits-unit-test-gen` skill 完成 `internal/agent/tools` 的单元测试生成与执行，重点覆盖参数切片传递、环境变量白名单、路径穿越防护、输出截断、`cli_policy` 校验。该 skill 的 `运行数据回传` 步骤**必须**执行，严禁跳过该步骤

**Checkpoint**: User Story 1 + 2 均可独立工作，Agent 具备外部行动能力

---

## Phase 5: User Story 3 - 技能驱动与上下文治理 (Priority: P3)

**Goal**: 技能按任务自动匹配执行，长会话自动压缩不爆上下文

**Independent Test**: 放入一个 SKILL.md → 页面看到它 → 对话中提相关任务观察技能被加载执行 → 持续对话至超过阈值 → 观察压缩提示出现且对话继续正常

### Implementation for User Story 3

- [x] T040 [P] [US3] 在 `internal/agent/skills/store.go` 实现技能 CRUD 与目录扫描缓存，合并文件系统目录与数据库两个来源，同名时数据库优先
- [x] T041 [US3] 在 `internal/agent/skills/backend.go` 实现 `skill.Backend` 接口（`List`/`Get`）。因 `List` 在 `skillTool.Info(ctx)` 内被调用，热重载天然成立，无需额外机制（依赖 T040）
- [x] T042 [US3] ~~在 `internal/agent/store/offload.go` 实现 `reduction` 中间件的 Backend：大工具结果卸载到数据库~~ → **原设计被 T005 实测结论推翻**：`reduction.Backend` 的签名是 `Write(ctx, *filesystem.WriteRequest) error`，是文件形状而非 KV 形状；模型取回卸载内容走的是 `ReadFileToolName`（默认 `read_file`），而该工具由 `filesystem` 中间件注册。因此 `reduction` 与 `filesystem` **必须共用同一个 Backend 实例**，独立的数据库 offload 后端取不回内容。实际实现为 `internal/agent/tools/cli/workspace.go` 的路径受限 `Workspace`，同时服务 `filesystem`、`reduction`、`plantask` 三个中间件
- [x] T043 [US3] 按 plan.md D2 的固定顺序挂载中间件：`skill` → `filesystem` → `plantask` → `reduction` → `summarization`。**顺序不可调整**（依赖 T041、T042）。实现落在 `internal/agent/kernel/factory.go` 的 `buildHandlers` 而非单独的 `context.go`：挂载逻辑与 `Build` 共用同一份 `Snapshot`，拆成两个文件只会让固定顺序这条约束离它的使用点更远。`toolsearch` 未挂载——当前工具总数为个位数，该中间件是为工具过多时的检索而设，此时挂上只会多一层间接
- [x] T044 [US3] 实现三层上下文治理的触发与提示：工具结果卸载（可逆）→ 历史摘要（有损）→ 压缩多次后在页面提示新开会话；压缩事件须通过 SSE 的 `compression` 事件可观测（依赖 T043、T018）
- [x] T045 [P] [US3] 在 `internal/agent/httpapi/skills.go` 实现技能 REST 接口与 `POST /api/skills/reload`
- [x] T046 [P] [US3] 在 `web/src/components/Sidebar/ConfigPanel.tsx` 增加技能管理分区，支持编辑 `context_mode`
- [ ] T047 [US3] 使用 `bits-unit-test-gen` skill 完成 `internal/agent/kernel` 与 `internal/agent/store/offload.go` 的单元测试生成与执行，重点覆盖压缩前后 `messages` 与 `session_context` 的语义对应关系（对应 plan.md 风险 1，本 feature 最高风险项）。该 skill 的 `运行数据回传` 步骤**必须**执行，严禁跳过该步骤

**Checkpoint**: User Story 1~3 均可独立工作，长时间使用不再受上下文窗口限制

---

## Phase 6: User Story 4 - 本地文档 RAG (Priority: P4)

**Goal**: 本地文档可被自动切分、向量化、混合检索

**Independent Test**: 指定含 Markdown/PDF/Word 的目录 → 触发索引 → 观察进度与每份文档状态 → 提问文档内特定内容 → 确认答案带出处 → 修改一份文档后重新索引 → 确认只重建该份

### Implementation for User Story 4

- [x] T048 [P] [US4] 在 `internal/agent/rag/loader.go` 实现目录扫描与上传，按扩展名派发 `document/parser`（pdf/docx/html/xlsx，纯文本直读），单份失败不中断整体，失败原因写入 `rag_documents.error`
- [x] T049 [P] [US4] 在 `internal/agent/rag/split.go` 实现切分器选择：Markdown 用 header 切分器保留标题层级，其余用 recursive 切分器，重叠区间可配置
- [x] T050 [P] [US4] 在 `internal/agent/rag/embedder.go` 接入 `eino-ext/components/embedding/openai` 指向智谱 `embedding-3` 端点，API Key 从环境变量读取
- [x] T051 [US4] 在 `internal/agent/rag/indexer.go` 实现 `indexer.Indexer` 接口：写 `rag_chunks`、`rag_vectors`（`float32` 打包为 BLOB，非 JSON）、同步 FTS5 表；按 `hash` 做增量跳过（依赖 T048~T050）
- [x] T052 [US4] 在 `internal/agent/rag/retriever.go` 实现 `retriever.Retriever` 接口：内存全量余弦 + FTS5 BM25 并行召回，**用 RRF 融合而非加权求和**（两者量纲不同，相加无意义），再经 `reranker/score` 重排（该组件把高分文档同时放在开头与结尾，规避 "lost in the middle"）
- [x] T053 [US4] 在 `internal/agent/rag/tool.go` 实现 `search_docs` 工具，返回内容片段与文档出处（依赖 T052）
- [x] T054 [US4] 在 `internal/agent/rag/manager.go` 实现文档管理与索引进度上报，索引作为后台任务运行且不阻塞对话
- [x] T055 [P] [US4] 在 `internal/agent/httpapi/rag.go` 实现文档 REST 接口与 `POST /api/rag/reindex`、`GET /api/rag/status`
- [x] T056 [P] [US4] 在 `web/src/components/Sidebar/ConfigPanel.tsx` 增加 RAG 文档管理分区：列表、上传、删除、重建索引、进度与失败原因展示
- [x] T083 [US4] 中文关键词召回质量实测：以真实中文文档评估 `trigram` 分词的召回效果。若质量不足，升级为纯 Go 分词器（如 `gse`）预切分后写入空格分隔 token 列并改回 `unicode61`——仅需改 `rag/indexer.go` 的写入与 `rag/retriever.go` 的查询构造，表结构与 RRF 融合逻辑不动（依赖 T052）
- [ ] T057 [US4] 使用 `bits-unit-test-gen` skill 完成 `internal/agent/rag` 的单元测试生成与执行，重点覆盖增量索引的 hash 跳过、RRF 融合的排名正确性、单文档解析失败不中断整体、**以纯中文文档与中文词语查询验证 FTS5 关键词一路非空**。该 skill 的 `运行数据回传` 步骤**必须**执行，严禁跳过该步骤

**Checkpoint**: User Story 1~4 均可独立工作，Agent 能基于本地知识回答

---

## Phase 7: User Story 5 - 跨会话长期记忆 (Priority: P5)

**Goal**: 用户偏好与事实跨会话生效，且页面可查可改

**Independent Test**: 会话 A 中告知一项偏好 → 新建会话 B 提相关问题 → 确认偏好被遵守 → 页面查看该条记忆 → 修改后在会话 C 验证新值生效

### Implementation for User Story 5

- [x] T058 [P] [US5] 在 `internal/agent/memory/store.go` 实现 `memories` 表 CRUD，**`key` 作主键，同 key 覆盖而非新增**，避免同一事实的不同措辞堆积成矛盾记录
- [x] T059 [US5] 在 `internal/agent/memory/tool.go` 实现 `remember` 工具，参数为 `key`/`content`/`category`，记录来源会话（依赖 T058）
- [x] T060 [US5] 在 `internal/agent/memory/inject.go` 实现分档召回：记忆总量小于阈值时全量注入 system prompt 且**不提供 `recall` 工具**；超过阈值转为向量检索按需召回（依赖 T058）
- [x] T061 [US5] 将记忆注入接入 `internal/agent/kernel/factory.go` 的 system prompt 组装（依赖 T060、T017）
- [x] T062 [P] [US5] 在 `internal/agent/httpapi/memory.go` 实现记忆 REST 接口
- [x] T063 [P] [US5] 在 `web/src/components/Sidebar/ConfigPanel.tsx` 增加记忆管理分区，支持查看、编辑、删除
- [ ] T064 [US5] 使用 `bits-unit-test-gen` skill 完成 `internal/agent/memory` 的单元测试生成与执行，重点覆盖同 key 覆盖语义与分档注入的阈值切换。该 skill 的 `运行数据回传` 步骤**必须**执行，严禁跳过该步骤

**Checkpoint**: User Story 1~5 均可独立工作，Agent 具备跨会话连续性

---

## Phase 8: User Story 6 - 定时任务 (Priority: P6)

**Goal**: 对话中口述即可登记定时任务，到点自动执行，重启不丢

**Independent Test**: 对话中说"每天早上九点帮我重建一次文档索引" → 页面定时任务分区出现该任务 → 修改为每分钟执行 → 观察自动执行与执行记录 → 重启程序确认任务仍在

### Implementation for User Story 6

- [x] T065 [P] [US6] 在 `internal/agent/schedule/store.go` 实现 `scheduled_tasks` 与 `task_runs` 的 CRUD，含执行记录保留策略（超量截断）
- [x] T066 [US6] 在 `internal/agent/schedule/scheduler.go` 接入 `robfig/cron/v3`：**数据库为唯一真相源**，增删改一律先写库再同步调度器；内存维护 `任务 ID → cron.EntryID` 映射；启动时从数据库全量恢复；错过的执行按 `skip` 策略处理并记为 `missed`（依赖 T065）
- [x] T067 [US6] 在 `internal/agent/schedule/tool.go` 实现 `schedule_task` 工具，供 Agent 在对话中登记任务；含**任务总数上限与最小执行间隔熔断**，防止 Agent 登记自我繁殖的任务（对应 plan.md 风险 5）
- [x] T068 [US6] 在 `internal/agent/schedule/runner.go` 实现受限执行：基于 `tool_scope` 构造裁剪后的工具集快照，**强制关闭 `execute` 自由执行工具**；支持 `keep_context`（沿用指定会话上下文）与独立上下文两种模式（依赖 T034、T017）
- [x] T069 [P] [US6] 在 `internal/agent/httpapi/task.go` 实现任务 REST 接口，含 `POST /api/tasks/{id}/run` 与 `GET /api/tasks/{id}/runs`
- [x] T070 [P] [US6] 在 `web/src/components/Sidebar/ConfigPanel.tsx` 增加定时任务分区：列表、启停、编辑、立即执行、执行历史
- [ ] T071 [US6] 使用 `bits-unit-test-gen` skill 完成 `internal/agent/schedule` 的单元测试生成与执行，重点覆盖重启恢复、熔断规则、受限工具集中 `execute` 确实被移除。该 skill 的 `运行数据回传` 步骤**必须**执行，严禁跳过该步骤

**Checkpoint**: User Story 1~6 均可独立工作，Agent 具备自主定时行动能力

---

## Phase 9: User Story 7 - 配置管理控制台 (Priority: P7)

**Goal**: 六组配置在统一界面中可视化管理，布局与需求中的参考截图一致

**Independent Test**: 打开页面 → 左侧栏上半部依次展开 RAG 文档、MCP、本地 CLI、技能、定时任务、记忆六个分区 → 每个分区完成一次增删改 → 确认无需重启即生效 → 左侧栏下半部会话列表可切换

### Implementation for User Story 7

- [x] T072 [US7] 在 `web/src/components/Sidebar/ConfigPanel.tsx` 整合六个配置分区为统一的可折叠手风琴布局，与需求参考截图的左侧栏上半部对应（依赖 T038、T046、T056、T063、T070）
- [ ] T073 [US7] 完成左侧栏整体布局：上半部配置管理、下半部会话管理，二者比例可拖拽调整
- [x] T074 [P] [US7] 统一配置项的校验与错误提示：保存失败时明确指出哪一项不合法及原因，不做静默失败
- [ ] T075 [P] [US7] 统一空状态与加载状态呈现，六个分区风格一致

**Checkpoint**: 全部 7 个 User Story 完成，功能闭环

---

## Phase 10: Polish & 跨领域收尾

- [ ] T076 [P] 补全全部导出函数与关键逻辑的注释，**中英文注释同时提供**（用户规则强制要求）
- [ ] T077 [P] 编写 `cmd/agent` 的 README：启动方式、环境变量要求、目录约定、与 `demo/` 的边界说明
- [ ] T078 [P] 更新仓库根 README，在 demo 分类之外单列本 Agent，说明它不是 demo
- [ ] T079 配置前端构建产物嵌入流程：`web/dist` 经 `go:embed` 进入二进制，并在 Makefile 或脚本中串联 `npm run build` 与 `go build`
- [ ] T080 端到端验证 spec.md 的 17 条 Success Criteria，逐条记录实测结果
- [ ] T081 `go vet ./...` 与 `gofmt -l .` 全绿；确认现有 `cmd/*`、`demo/*`、`internal/{llm,mcpclient,skill,gitmcp,agentio,democli}` 的 `git diff` 为空

---

## Phase 11: 合规与安全扫描（最终任务，不可跳过）

- [ ] T082 使用 `code-compliance-checker` 与 `security-scanner` skill 对本 feature 变更的代码进行代码合规与安全问题扫描，并产出最终扫描报告。`code-compliance-checker` 与 `security-scanner` **两者的 `运行数据回传` 步骤都必须执行，严禁跳过其中任何一个**

---

## Dependencies & Execution Order

### Phase 依赖

- **Setup (Phase 1)** → 无依赖，最先执行
- **Foundational (Phase 2)** → 依赖 Setup，**阻塞所有 User Story**
- **User Stories (Phase 3~9)** → 全部依赖 Foundational
- **Polish (Phase 10)** → 依赖所有 User Story
- **合规扫描 (Phase 11)** → 依赖 Polish，最后执行

### User Story 之间的依赖

按优先级顺序推荐实现。其中存在的真实依赖：

- US3 的中间件挂载（T043）需要 US2 的工具装配（T034）已就位
- US6 的受限执行（T068）需要 US2 的工具范围过滤（T034）与 US1 的 Agent 工厂（T017）
- US7 是对 US2~US6 各自前端分区的整合，必须最后做
- US4、US5 相互独立，与 US2、US3 也无强依赖，可并行推进

### 关键路径

```
T005（中间件验证）→ T006~T008（存储）→ T016/T017（Agent 工厂）→ T034（工具装配）→ T043（中间件挂载）
```

T005 是整条链路的前置闸门。六个内置中间件的行为来自源码阅读推断而非实测，若 `fork` 模式或 `reduction` 与 `filesystem` 的协作与假设不符，后续设计需要返工。**必须在写大量代码之前完成验证**。

T030 是 US2 的小型前置闸门，同理。

### Parallel Example

Foundational 完成后，若有多人协作可如下切分：

```
开发者 A：US1（T012~T028）—— 必须最先完成，其余 Story 依赖其 Agent 工厂
US1 完成后：
开发者 A：US2（T029~T039）
开发者 B：US4（T048~T057、T083）—— 与 US2 无依赖
开发者 C：US5（T058~T064）—— 与 US2、US4 无依赖
US2 完成后：
开发者 A：US3（T040~T047）→ US6（T065~T071）
最后由一人统一做 US7（T072~T075）
```

单人开发时按 T001→T082 顺序执行即可，`[P]` 标记仅表示"这些任务之间改动不同文件，打断重排不会冲突"。

## Implementation Strategy

### MVP 优先

Phase 1 + Phase 2 + Phase 3（US1）完成即是一个**可独立交付**的本地 AI 对话工具：能对话、能流式输出、能管理会话、重启不丢。建议在此处停下来实际用几天，再决定后续 Story 的优先级是否需要调整。

### 增量交付

每个 Phase 的 Checkpoint 都是可运行、可验证的状态。不要把多个 Story 的代码混在一次大提交里——出问题时无法定位是哪个 Story 引入的。

### 风险前置

三个技术验证任务（T005 中间件行为、T030 动态工具构造、T011 并发写）刻意放在各自阶段的最前面。它们验证的都是"地基假设"，晚发现的代价远大于早验证的成本。

## Notes

- 所有新增 Go 代码收敛在 `internal/agent/` 与 `cmd/agent/`，前端收敛在 `web/`。现有 `cmd/*`、`demo/*` 零改动，`internal/{llm,mcpclient}` 仅被只读复用（用户明确要求"不要跟现有的代码混淆"）
- 密钥仅从环境变量读取：不硬编码、不落库、不写配置文件、**不作为命令行参数**（`ps` 可见）。MCP server 密钥以 `secret_ref` 存环境变量名
- 服务仅监听 `127.0.0.1`，不提供 `0.0.0.0` 选项——当前设计没有鉴权层
- 注释必须中英文同时提供（用户规则）
- 单元测试一律通过 `bits-unit-test-gen` skill 完成，**严禁手工编写测试代码**，且该 skill 的 `运行数据回传` 步骤严禁跳过
- 任务总数 83 项，其中标记 `[P]` 的可并行任务 32 项
- 存储确定为单文件 SQLite，不使用本地 MySQL（理由见 plan.md D9）。`rag_chunks_fts` 建表必须显式指定 `tokenize = 'trigram'`，否则中文关键词召回会静默失效
- T083 由 `/adk-sdd-clarify` 追加，编号接续全局序列，故其编号大于文档中位于其后的 T082；执行顺序以文档中的出现位置为准，T082 仍是最后一个任务
