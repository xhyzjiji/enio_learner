-- 通用 Agent 的全部表结构。单文件 SQLite，开启 WAL。
-- All tables for the general agent runtime. Single-file SQLite with WAL enabled.
--
-- 表结构变更请走 migrate.go 的版本号机制，不要直接改本文件的既有语句。
-- Schema changes must go through the version mechanism in migrate.go; do not edit existing statements here.

-- ───────────────── 会话与消息 / sessions and messages ─────────────────

-- sessions 保存会话元信息。compact_count 记录压缩次数，用于在压缩多次后提示用户新开会话。
-- sessions holds conversation metadata. compact_count tracks compression rounds so the UI can
-- suggest starting a fresh session after repeated compaction.
CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT    PRIMARY KEY,
    title       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    archived    INTEGER NOT NULL DEFAULT 0,
    token_usage INTEGER NOT NULL DEFAULT 0,
    compact_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions (archived, updated_at DESC);

-- messages 保存原始完整消息，供页面展示、导出与审计。
-- 它与 session_context 是同一段对话的两个视角：这里永远是未压缩的原文。
-- messages stores the original, complete messages for UI display, export and audit.
-- It is one of two views of the same conversation; this one is never compressed.
CREATE TABLE IF NOT EXISTS messages (
    id           TEXT    PRIMARY KEY,
    session_id   TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    role         TEXT    NOT NULL,
    content      TEXT    NOT NULL DEFAULT '',
    -- tool_calls 为 JSON 数组。ToolCall.ID 必须原值保存，否则 assistant 消息与后续
    -- tool 消息无法配对。
    -- tool_calls is a JSON array. ToolCall.ID must be preserved verbatim, otherwise the
    -- assistant message cannot be paired with its subsequent tool messages.
    tool_calls   TEXT    NOT NULL DEFAULT '',
    tool_call_id TEXT    NOT NULL DEFAULT '',
    tool_name    TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_seq ON messages (session_id, seq);

-- session_context 保存压缩后的模型输入快照，与会话一对一，用于恢复对话时喂给模型。
-- session_context holds the compressed model-input snapshot, one row per session,
-- used to feed the model when resuming a conversation.
CREATE TABLE IF NOT EXISTS session_context (
    session_id TEXT    PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    payload    TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

-- checkpoints 落 adk.CheckPointStore 的 Get/Set。生命周期临时，用完即删，
-- 与 messages 的长期持久化是两回事。
-- checkpoints backs adk.CheckPointStore Get/Set. Its lifetime is transient and entries are
-- deleted once consumed; this is distinct from the long-lived persistence in messages.
CREATE TABLE IF NOT EXISTS checkpoints (
    id         TEXT    PRIMARY KEY,
    data       BLOB    NOT NULL,
    created_at INTEGER NOT NULL
);

-- ───────────────── 长期记忆 / long-term memory ─────────────────

-- memories 以 key 为主键，同 key 覆盖而非新增，避免同一事实的不同措辞堆积成矛盾记录。
-- memories is keyed by `key`; writing the same key overwrites rather than appends, preventing
-- different phrasings of the same fact from piling up into contradictions.
CREATE TABLE IF NOT EXISTS memories (
    key        TEXT    PRIMARY KEY,
    content    TEXT    NOT NULL,
    category   TEXT    NOT NULL DEFAULT '',
    source_sid TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memories_category ON memories (category);

-- ───────────────── RAG ─────────────────

-- rag_documents 的 hash 支撑增量索引；error 让失败原因在页面可见，而不是静默丢失。
-- The hash column in rag_documents drives incremental indexing; error surfaces the failure
-- reason in the UI instead of silently dropping it.
CREATE TABLE IF NOT EXISTS rag_documents (
    id         TEXT    PRIMARY KEY,
    path       TEXT    NOT NULL UNIQUE,
    title      TEXT    NOT NULL DEFAULT '',
    hash       TEXT    NOT NULL DEFAULT '',
    size       INTEGER NOT NULL DEFAULT 0,
    status     TEXT    NOT NULL DEFAULT 'pending',
    error      TEXT    NOT NULL DEFAULT '',
    chunk_count INTEGER NOT NULL DEFAULT 0,
    indexed_at INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rag_chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_id      TEXT    NOT NULL REFERENCES rag_documents (id) ON DELETE CASCADE,
    ordinal     INTEGER NOT NULL,
    content     TEXT    NOT NULL,
    token_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_rag_chunks_doc ON rag_chunks (doc_id, ordinal);

-- rag_vectors 以 float32 打包为 BLOB 存储，禁止文本序列化：万级 chunk 下 JSON 的
-- 解析开销与体积都不可接受。
-- rag_vectors stores float32 packed as BLOB. Text serialization is forbidden: at ten-thousand
-- chunk scale the parsing cost and size of JSON are both unacceptable.
CREATE TABLE IF NOT EXISTS rag_vectors (
    chunk_id INTEGER PRIMARY KEY REFERENCES rag_chunks (id) ON DELETE CASCADE,
    dim      INTEGER NOT NULL,
    vector   BLOB    NOT NULL
);

-- rag_chunks_fts 是混合检索的关键词一侧。
-- tokenize 必须显式指定为 trigram：FTS5 默认的 unicode61 分词器把连续汉字判定为单个
-- token，中文查询会恒返回空结果，且不报任何错误。
-- rag_chunks_fts is the keyword half of hybrid retrieval.
-- The tokenizer MUST be set to trigram explicitly: the FTS5 default unicode61 treats a run of
-- CJK characters as a single token, so Chinese queries silently return nothing.
CREATE VIRTUAL TABLE IF NOT EXISTS rag_chunks_fts USING fts5 (
    content,
    chunk_id UNINDEXED,
    tokenize = 'trigram'
);

-- ───────────────── 工具配置 / tool configuration ─────────────────

-- mcp_servers 的 secret_ref 存的是环境变量名，不是密钥明文。
-- 明文密钥一律不入库，写入层会拒绝。
-- secret_ref in mcp_servers stores an environment variable NAME, never the secret itself.
-- Plaintext secrets are rejected at the write layer and never persisted.
CREATE TABLE IF NOT EXISTS mcp_servers (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    endpoint    TEXT    NOT NULL,
    secret_ref  TEXT    NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    health      TEXT    NOT NULL DEFAULT 'unknown',
    last_error  TEXT    NOT NULL DEFAULT '',
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

-- cli_tools 是页面定义的声明式工具：固定命令 + 参数模板，模型只能填模板里的槽位。
-- cli_tools holds declarative tools defined in the UI: a fixed command plus an argument
-- template; the model can only fill the declared slots.
CREATE TABLE IF NOT EXISTS cli_tools (
    id            TEXT    PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE,
    description   TEXT    NOT NULL DEFAULT '',
    command       TEXT    NOT NULL,
    args_template TEXT    NOT NULL DEFAULT '[]',
    params        TEXT    NOT NULL DEFAULT '{}',
    work_dir      TEXT    NOT NULL DEFAULT '',
    timeout_sec   INTEGER NOT NULL DEFAULT 30,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL
);

-- cli_policy 是 execute 自由执行工具的允许/拒绝规则。该工具默认关闭。
-- cli_policy holds allow/deny rules for the free-form execute tool, which is off by default.
CREATE TABLE IF NOT EXISTS cli_policy (
    id         TEXT    PRIMARY KEY,
    pattern    TEXT    NOT NULL,
    action     TEXT    NOT NULL DEFAULT 'deny',
    note       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

-- ───────────────── 技能 / skills ─────────────────

-- skills 是页面创建的技能。与 --skills-dir 目录扫描结果合并时，同名以本表为准。
-- skills holds UI-created skills. When merged with the --skills-dir scan, this table wins on
-- name collisions.
CREATE TABLE IF NOT EXISTS skills (
    name         TEXT    PRIMARY KEY,
    description  TEXT    NOT NULL DEFAULT '',
    body         TEXT    NOT NULL DEFAULT '',
    context_mode TEXT    NOT NULL DEFAULT '',
    agent        TEXT    NOT NULL DEFAULT '',
    model        TEXT    NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- ───────────────── 定时任务 / scheduled tasks ─────────────────

-- scheduled_tasks 是调度的唯一真相源：增删改一律先写本表，再同步内存中的 cron 调度器。
-- 进程被 kill -9 后靠本表全量恢复。
-- scheduled_tasks is the single source of truth for scheduling: every mutation writes here
-- first and then syncs the in-memory cron scheduler. After a kill -9 the scheduler is fully
-- rebuilt from this table.
CREATE TABLE IF NOT EXISTS scheduled_tasks (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL,
    cron         TEXT    NOT NULL,
    prompt       TEXT    NOT NULL DEFAULT '',
    kind         TEXT    NOT NULL DEFAULT 'prompt',
    enabled      INTEGER NOT NULL DEFAULT 1,
    keep_context INTEGER NOT NULL DEFAULT 0,
    session_id   TEXT    NOT NULL DEFAULT '',
    tool_scope   TEXT    NOT NULL DEFAULT '[]',
    last_run_at  INTEGER NOT NULL DEFAULT 0,
    next_run_at  INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_enabled ON scheduled_tasks (enabled, next_run_at);

CREATE TABLE IF NOT EXISTS task_runs (
    id          TEXT    PRIMARY KEY,
    task_id     TEXT    NOT NULL REFERENCES scheduled_tasks (id) ON DELETE CASCADE,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0,
    status      TEXT    NOT NULL DEFAULT 'running',
    result      TEXT    NOT NULL DEFAULT '',
    error       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs (task_id, started_at DESC);

-- ───────────────── 杂项配置 / misc configuration ─────────────────

-- config_kv 存模型名、各类阈值等零散配置。密钥永远不进这里。
-- config_kv stores scattered settings such as model name and thresholds. Secrets never land here.
CREATE TABLE IF NOT EXISTS config_kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- ───────────────── 命令确认 / command approvals ─────────────────

-- approvals 记录每一次等待人工确认的 execute 命令。
--
-- 它必须落库而不是只存内存：确认的本质是"等一个人做决定"，而人可能明天才回来。
-- checkpoint_id 与 interrupt_id 是恢复执行所需的全部坐标，缺一条都接不回去。
--
-- approvals records every execute command awaiting human confirmation.
--
-- It must be persisted rather than kept in memory: confirmation means waiting on a person, and
-- a person may come back tomorrow. checkpoint_id and interrupt_id are the complete coordinates
-- needed to resume; without either, execution cannot be rejoined.
CREATE TABLE IF NOT EXISTS approvals (
    id            TEXT    PRIMARY KEY,
    session_id    TEXT    NOT NULL,
    checkpoint_id TEXT    NOT NULL,
    interrupt_id  TEXT    NOT NULL,
    command       TEXT    NOT NULL,
    status        TEXT    NOT NULL DEFAULT 'pending',
    reason        TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    decided_at    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_approvals_session ON approvals (session_id, status, created_at);
