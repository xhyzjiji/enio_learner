# 通用 Agent 运行时 —— 本地运行说明

`cmd/agent` 是一个可以长期使用的通用 Agent：带 Web 界面、SQLite 持久化、本地文档 RAG、MCP 与 CLI 工具、技能系统、定时任务和长期记忆。

**它不是 demo。** 仓库根目录的 `README.md` 讲的是一组 Eino 学习 demo（`cmd/demo`、`demo/`、`internal/{llm,mcpclient,skill,gitmcp,agentio,democli}`），那些是为了对照 LangGraph 而写的教学代码，跑完即止。本运行时在 `cmd/agent` 与 `internal/agent/**` 下自成一套，不与 demo 共享任何代码，两边的环境变量也不通用——**最常见的误配就是把 demo 的 `ZHIPUAI_BASE_URL` 当成本运行时的配置**，本运行时读的是 `AGENT_BASE_URL`。

---

## 一、选一种模型方案

| | 本地（Ollama） | 云端（智谱） |
|---|---|---|
| 环境变量 | `AGENT_BASE_URL=http://localhost:11434/v1` | 不设，走默认端点 |
| 密钥 | **不需要** | `ZHIPUAI_API_KEY` 必须设置，否则启动即失败 |
| 默认模型名 | 需改成本地模型 | `glm-4.5-air` / `embedding-3`，开箱即用 |
| 数据出网 | 否 | 是 |

对话模型与嵌入模型**共用同一个 `AGENT_BASE_URL`**。这是故意的：两者指向不同厂商时，RAG 库里会混进两种维度的向量，而这种错配不报错，只会让检索结果悄悄变差。

下面以本地方案为主线，云端方案见[第五节](#五云端模型方案)。

---

## 二、准备本地模型

### 1. 安装并启动 Ollama

```bash
brew install ollama
brew services start ollama
curl -s http://localhost:11434/api/tags          # 确认服务起来了
```

### 2. 拉模型

对话模型和嵌入模型要分别拉，Ollama 的对话模型不提供 embedding 接口。

```bash
ollama pull qwen3:14b      # 对话，约 9GB
ollama pull bge-m3         # 嵌入，约 1.2GB，1024 维
```

### 3. 把上下文窗口调大（这一步不能跳）

Ollama 默认上下文只有 4096 token，而本运行时的系统提示词加上工具定义就可能接近这个数。直接用会出现模型"忘记"自己有哪些工具、或者 tool call 被截断的怪现象，**且不会报任何错**。建一个覆盖版本：

```bash
cat > /tmp/Modelfile <<'EOF'
FROM qwen3:14b
PARAMETER num_ctx 32768
EOF
ollama create qwen3-agent -f /tmp/Modelfile
ollama list
```

---

## 三、启动后端

```bash
cd <仓库根目录>
export AGENT_BASE_URL=http://localhost:11434/v1
unset ZHIPUAI_API_KEY                            # 本地端点免密钥
go run ./cmd/agent
```

起来后监听 `http://127.0.0.1:8090`。

**工作目录决定数据落在哪。** 下面几个路径都是相对当前目录解析的，所以请固定在仓库根目录启动，否则会在别处生成一套空的数据：

| 参数 | 默认值 | 用途 |
|---|---|---|
| `-addr` | `127.0.0.1:8090` | 监听地址，**仅限回环**，填非回环地址会被拒绝启动 |
| `-db` | `agent.db` | SQLite 数据库，所有会话、配置、索引都在这一个文件里 |
| `-rag-dir` | `documents` | RAG 文档目录 |
| `-skills-dir` | `skills` | 技能目录，装技能一律装到这里 |
| `-work-dir` | `workspace` | 文件工具的根目录，模型读写文件的边界 |
| `-secrets-dir` | `~/.config/agent/secrets` | 第三方工具凭证目录，**默认不在仓库里**，见第六节 |
| `-verbose` | `false` | 调试日志 |

### 切换模型名

默认配置指着智谱的 `glm-4.5-air` 和 `embedding-3`，要改成刚拉的本地模型。页面左侧配置区可以改，也可以直接打接口：

```bash
curl -s -X PUT http://127.0.0.1:8090/api/config \
  -H 'Content-Type: application/json' \
  -d '{"model_name":"qwen3-agent","title_model_name":"qwen3-agent","embedding_model":"bge-m3"}' \
  | python3 -m json.tool
```

请求体是**扁平的运行时配置结构，支持只传要改的字段**，服务端会在当前配置上覆盖。注意不要套一层 `{"runtime": {...}}`，那样所有字段都不会生效。

### 已有 RAG 索引时必须重建

`bge-m3` 是 1024 维，智谱 `embedding-3` 是 2048 维。维度对不上的向量会直接报错而不是静默算错，但索引本身仍需重建：

```bash
curl -s -X POST 'http://127.0.0.1:8090/api/rag/reindex?force=true'
```

---

## 四、启动前端

前端产物已经通过 `go:embed` 嵌进二进制，**所以日常使用不需要单独起前端**：后端跑起来后直接访问 <http://127.0.0.1:8090> 就是完整界面。

只有改前端代码时才需要下面两种模式。

### 开发模式（改代码时用）

```bash
cd web
npm install                                      # 首次
npm run dev
```

访问 <http://localhost:5173>。Vite 把 `/api` 代理到 `127.0.0.1:8090`，所以**后端要同时开着**。代理层关掉了响应缓冲，否则 SSE 会被攒在代理里，页面上看不到逐字输出。

### 构建进二进制（改完前端后用）

```bash
cd web && npm run build                          # 产出 web/dist
cd .. && go run ./cmd/agent                      # 或 go build -o agent ./cmd/agent
```

**前端改动不会自动进二进制**，必须先 `npm run build` 再重新编译 Go。如果页面上看不到刚改的东西，先确认这一步做了没有。

其他前端命令：

```bash
npm run typecheck                                # tsc -b --noEmit
npm run preview                                  # 预览构建产物
```

---

## 五、云端模型方案

```bash
cd <仓库根目录>
export ZHIPUAI_API_KEY=<智谱开放平台 Key>
unset AGENT_BASE_URL                             # 不设则走智谱默认端点
go run ./cmd/agent
```

默认模型名就是智谱的，不用再改配置。密钥只从环境变量读，不落库、不写配置文件、也不作为命令行参数——`ps` 能看到任何进程的命令行。

---

## 六、首次使用要知道的几件事

**`execute` 工具默认关闭。** 它让模型能执行任意 shell 命令，所以默认不挂载。要用的话在页面配置区打开 `enable_execute`。

**打开之后，每条命令都会停下来等你确认。** `require_exec_approval` 默认开启，模型要执行命令时页面会弹出确认卡片。这个等待**没有超时**：它走的是 eino 的中断/恢复机制，等待期间不占用任何资源，所以关掉页面、关掉浏览器、甚至重启后端，回来之后那张卡片还在，点允许就接着执行。

不需要确认的话可以关掉 `require_exec_approval`，但先想清楚——模型在探索陌生 CLI 时发出 `rm -rf` 这类命令并不罕见。

**定时任务里的 `execute` 一律拒绝执行。** 定时任务没有人在旁边，中断了也没人恢复，所以直接拒绝而不是挂起。

**子进程的环境变量走白名单**，只传 `PATH`、`HOME`、`LANG`、`LC_ALL`、`TZ`、`TMPDIR`。所以模型跑 `env` 也看不到 `ZHIPUAI_API_KEY`，同时也意味着**它继承的 `PATH` 是后端进程启动时的那一份**：你在别的终端里新装的命令行工具，要重启后端才能被用上。

---

## 七、第三方工具的凭证

技能要调 Tavily、各类 OpenAPI 这些需要密钥的服务时，**不能靠环境变量**。上一节那条白名单就是原因：你 `export TAVILY_API_KEY` 再让技能去读 `$TAVILY_API_KEY`，拿到的永远是空串，而且不报错——`curl` 照常发得出去，只是换回一个 401，排查时很难想到是这里。

正确做法是在页面左侧的「凭证」分区添加。名称只允许大写字母、数字和下划线（例如 `TAVILY_API_KEY`），值写进 `~/.config/agent/secrets/<名称>`，权限 `0600`，目录 `0700`。也可以打接口：

```bash
curl -s -X PUT http://127.0.0.1:8090/api/secrets/TAVILY_API_KEY \
  -H 'Content-Type: application/json' -d '{"value":"tvly-你的key"}'
```

配好之后，**模型会在系统提示词里看到凭证名和文件路径**（仅在 `execute` 开启时注入，关着的话子进程都起不来，讲了也没用），命令里直接这样读：

```bash
curl -H "Authorization: Bearer $(cat ~/.config/agent/secrets/TAVILY_API_KEY)" https://api.tavily.com/search
```

几点要明确：

**没有任何接口会返回凭证的值**，页面上也只显示「已配置」。想核对只能重新填一遍。这个不方便是故意的——本服务没有鉴权层，多一条读取凭证的路就是多一个口子。

**这不防模型读取。** `execute` 开着时模型随时可以 `cat` 那个文件，这是 `execute` 的本质。它防的是另一件事：凭证**进入对话历史**。直接粘进聊天框的密钥会落进 `messages` 表，此后每一轮都被重新送回模型上下文，还会随会话导出和日志扩散；而文件里的凭证只在模型主动去读的那一次才暴露，那一次会留在工具调用记录里，你看得见。

**默认目录不在仓库里。** 仓库会被 clone、打包、复制到别的机器，凭证跟着走一圈就等于泄露，所以默认放在 `~/.config/agent/secrets`，顺带免掉误 commit 的可能。要换位置用 `-secrets-dir`。

---

## 八、接口速查

| 分类 | 端点 |
|---|---|
| 健康检查 | `GET /api/health` |
| 对话 | `POST /api/chat`（SSE）、`POST /api/chat/interrupt`、`POST /api/chat/resume` |
| 命令确认 | `GET /api/sessions/{id}/approvals` |
| 会话 | `GET/POST /api/sessions`、`GET/PATCH/DELETE /api/sessions/{id}`、`GET /api/sessions/{id}/messages` |
| 配置 | `GET/PUT /api/config` |
| RAG | `GET/POST /api/rag/documents`、`DELETE /api/rag/documents/{id}`、`POST /api/rag/reindex`、`GET /api/rag/status` |
| 技能 | `GET/POST /api/skills`、`PUT/DELETE /api/skills/{name}`、`POST /api/skills/reload` |
| 工具 | `GET /api/tools`、`/api/tools/mcp`、`/api/tools/cli`、`/api/tools/policy` 各自的增删改 |
| 记忆 | `GET /api/memories`、`PUT/DELETE /api/memories/{key}` |
| 定时任务 | `GET/POST /api/tasks`、`DELETE /api/tasks/{id}`、`POST /api/tasks/{id}/run`、`GET /api/tasks/{id}/runs` |
| 凭证 | `GET /api/secrets`、`PUT/DELETE /api/secrets/{name}`（只进不出，没有读取值的端点） |

---

## 九、排查

**启动就报缺密钥。** 用本地模型时 `AGENT_BASE_URL` 没设对。判定本地端点看的是地址里有没有 `127.0.0.1`、`localhost` 或 `[::1]`，写成机器名或局域网 IP 都会被当成远程服务并要求密钥。

**模型答非所问、工具调不对、或者说自己没有某个工具。** 八成是 `num_ctx` 没调，直接用了 `qwen3:14b` 而不是 `qwen3-agent`。这种情况不会报错，只会表现为模型"变笨"。

**页面能开但一说话就 500。** 看后端日志里的 `dial tcp ... connect: connection refused`——Ollama 没起，或者 `AGENT_BASE_URL` 指到了别的端口。

**改了配置重启就丢。** 确认改的是运行时配置而不是启动参数。启动参数（监听地址、各目录）只能靠命令行 flag，不落库。

**中文检索召回为空。** `rag_chunks_fts` 必须用 `tokenize = 'trigram'`。FTS5 默认的 `unicode61` 会把连续汉字当成单个 token，中文查询恒返回空结果且不报错。

**技能调外部接口一直 401。** 密钥是不是用 `export` 给的？子进程环境走白名单，`$XXX_API_KEY` 在命令里恒为空串。改用第七节的「凭证」。
