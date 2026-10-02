// Package config 管理通用 Agent 的运行时配置。
//
// 配置分两类，边界必须清楚：
//   - 启动参数（监听地址、数据库路径、各目录）：进程生命周期内不变，来自命令行 flag
//   - 运行时配置（模型名、各类阈值、execute 开关）：页面可改，落在 config_kv 表
//
// 密钥不属于以上任何一类：它只从环境变量读取，既不落库、不写配置文件，
// 也不作为命令行参数——因为 ps 能看到任何进程的命令行。
//
// Package config manages runtime configuration for the general agent runtime.
//
// Configuration falls into two clearly separated kinds:
//   - startup flags (listen address, database path, directories): fixed for the process
//     lifetime, supplied via command-line flags
//   - runtime settings (model name, thresholds, the execute switch): editable from the UI and
//     persisted in the config_kv table
//
// Secrets belong to neither: they are read from environment variables only, never persisted,
// never written to a config file, and never passed as command-line arguments — because ps can
// read the command line of any process.
package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"private/agent_basedon_eino/internal/agent/store"
)

// APIKeyEnv 是模型密钥的环境变量名。值本身永远不会离开进程内存。
// APIKeyEnv is the environment variable name holding the model API key. The value itself never
// leaves process memory.
const APIKeyEnv = "ZHIPUAI_API_KEY"

// BaseURLEnv 覆盖模型服务地址，对话模型与嵌入模型共用一个值。
//
// 共用是故意的：两者分别指向不同厂商时，对话用 A 家、索引用 B 家，RAG 库里就会
// 混进两种维度的向量，而这种错配不会报错，只会让检索结果悄悄变差。
//
// BaseURLEnv overrides the model service address, shared by the chat and embedding models.
//
// Sharing is deliberate: pointing them at different vendors would mix vectors of two different
// dimensionalities into the RAG store, and that mismatch raises no error — it just quietly
// degrades retrieval.
const BaseURLEnv = "AGENT_BASE_URL"

// defaultBaseURL 是智谱开放平台的 OpenAI 兼容端点。
// defaultBaseURL is ZhipuAI's OpenAI-compatible endpoint.
const defaultBaseURL = "https://open.bigmodel.cn/api/paas/v4"

// BaseURL 返回模型服务地址。
//
// 默认指向智谱，设置 AGENT_BASE_URL 即可换到任何 OpenAI 兼容服务——本地 Ollama
// 填 http://localhost:11434/v1 就能完全离线跑。
//
// BaseURL returns the model service address.
//
// It points at ZhipuAI by default; setting AGENT_BASE_URL switches to any OpenAI-compatible
// service — http://localhost:11434/v1 for a local Ollama runs everything offline.
func BaseURL() string {
	if v := strings.TrimSpace(os.Getenv(BaseURLEnv)); v != "" {
		return v
	}
	return defaultBaseURL
}

// HasAPIKey 报告用户是否真的设置了密钥。
//
// 与 APIKey 的区别在于本地端点那条分支：APIKey 会返回占位值让调用方跑下去，
// 而这里必须如实回答「没设」，否则页面会显示密钥已配置，用户换回远程服务时
// 才发现根本没有。
//
// HasAPIKey reports whether the user actually set a key.
//
// It differs from APIKey on the local-endpoint path: APIKey returns a placeholder so callers can
// proceed, whereas this must honestly answer "not set" — otherwise the UI would claim a key is
// configured, and the user would only discover otherwise after switching back to a remote service.
func HasAPIKey() bool {
	return strings.TrimSpace(os.Getenv(APIKeyEnv)) != ""
}

// IsLocalEndpoint 判断当前模型服务是否在本机。
// IsLocalEndpoint reports whether the model service runs on this machine.
func IsLocalEndpoint() bool {
	u := BaseURL()
	return strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost") ||
		strings.Contains(u, "[::1]")
}

// Startup 保存启动参数。
// Startup holds startup flags.
type Startup struct {
	// Addr 只允许回环地址。当前设计没有鉴权层，监听 0.0.0.0 等于把一个能读写本地
	// 文件、能执行命令的 Agent 暴露给整个局域网。
	// Addr is restricted to loopback. There is no authentication layer, so binding 0.0.0.0
	// would expose an agent that can read/write local files and run commands to the whole LAN.
	Addr string

	DBPath     string
	RAGDir     string
	SkillsDir  string
	WorkingDir string
}

// Runtime 是页面可改的运行时配置。零值不可用，必须经 Load 填充默认值。
// Runtime holds UI-editable settings. The zero value is not usable; Load fills in defaults.
type Runtime struct {
	// ModelName 是对话使用的模型。
	// ModelName is the model used for conversation.
	ModelName string `json:"model_name"`

	// TitleModelName 用于生成会话标题，可以选一个更便宜的模型。
	// TitleModelName generates session titles and may point at a cheaper model.
	TitleModelName string `json:"title_model_name"`

	// EmbeddingModel 是 RAG 向量化使用的模型。
	// EmbeddingModel is used for RAG embedding.
	EmbeddingModel string `json:"embedding_model"`

	// MaxTokensForClear 是 reduction 清理阶段的触发阈值。
	// MaxTokensForClear triggers the reduction clear phase.
	MaxTokensForClear int `json:"max_tokens_for_clear"`

	// MaxLengthForTrunc 是单个工具结果触发截断卸载的字符阈值。
	// MaxLengthForTrunc is the per-tool-result character threshold for truncation offloading.
	MaxLengthForTrunc int `json:"max_length_for_trunc"`

	// SummarizeTokens 是 summarization 的触发阈值。必须显著大于 MaxTokensForClear，
	// 因为要先做可逆的工具结果卸载，再做有损的历史摘要。
	// SummarizeTokens triggers summarization. It must be clearly larger than
	// MaxTokensForClear, because reversible tool-result offloading must happen before lossy
	// history summarization.
	SummarizeTokens int `json:"summarize_tokens"`

	// MemoryInjectLimit 是长期记忆全量注入的条数上限。低于它就整批塞进 system prompt
	// 且不提供 recall 工具——recall 最大的失败模式是模型压根不调它，全量注入根本
	// 不存在这个失败模式。
	// MemoryInjectLimit caps how many memories are injected wholesale. Below it, everything
	// goes into the system prompt and no recall tool is offered — the dominant failure mode of
	// recall is the model simply not calling it, which wholesale injection cannot suffer from.
	MemoryInjectLimit int `json:"memory_inject_limit"`

	// EnableExecute 控制是否提供 execute 自由执行工具。默认关闭。
	// EnableExecute controls whether the free-form execute tool is offered. Off by default.
	EnableExecute bool `json:"enable_execute"`

	// MaxToolResultBytes 是工具返回值的字节上限，超出后截断并标注。
	// MaxToolResultBytes caps tool result size; anything beyond is truncated and marked.
	MaxToolResultBytes int `json:"max_tool_result_bytes"`

	// MaxScheduledTasks 是定时任务总数上限，用来熔断"任务登记新任务"的自我繁殖。
	// MaxScheduledTasks caps the number of scheduled tasks, breaking the self-replication loop
	// where a task registers further tasks.
	MaxScheduledTasks int `json:"max_scheduled_tasks"`

	// MinScheduleIntervalSec 是定时任务允许的最小执行间隔。
	// MinScheduleIntervalSec is the minimum allowed interval between task executions.
	MinScheduleIntervalSec int `json:"min_schedule_interval_sec"`

	// RetrievalTopK 是混合检索最终返回的片段数。
	// RetrievalTopK is the number of chunks hybrid retrieval finally returns.
	RetrievalTopK int `json:"retrieval_top_k"`
}

func defaults() Runtime {
	return Runtime{
		ModelName:              "glm-4.5-air",
		TitleModelName:         "glm-4.5-air",
		EmbeddingModel:         "embedding-3",
		MaxTokensForClear:      160000,
		MaxLengthForTrunc:      50000,
		SummarizeTokens:        200000,
		MemoryInjectLimit:      50,
		EnableExecute:          false,
		MaxToolResultBytes:     64 * 1024,
		MaxScheduledTasks:      50,
		MinScheduleIntervalSec: 60,
		RetrievalTopK:          8,
	}
}

// Manager 持有当前运行时配置并负责持久化。读多写少，用 RWMutex。
// Manager owns the current runtime configuration and persists it. Reads dominate writes, so an
// RWMutex is used.
type Manager struct {
	db *store.DB

	mu      sync.RWMutex
	current Runtime
	startup Startup
}

// NewManager 从数据库载入运行时配置，缺失项用默认值补齐并回写。
// NewManager loads runtime settings from the database, filling and persisting any missing
// entries with defaults.
func NewManager(ctx context.Context, db *store.DB, startup Startup) (*Manager, error) {
	m := &Manager{db: db, startup: startup, current: defaults()}
	if err := m.load(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load(ctx context.Context) error {
	rows, err := m.db.Read().QueryContext(ctx, `SELECT key, value FROM config_kv`)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	defer rows.Close()

	stored := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return fmt.Errorf("scan config row: %w", err)
		}
		stored[k] = v
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate config rows: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	c := defaults()
	applyStored(&c, stored)
	m.current = c
	return nil
}

// applyStored 把数据库中的字符串值覆盖到结构体上。无法解析的值直接忽略并保留默认值，
// 因为一个坏掉的配置项不应该让整个进程起不来。
// applyStored overlays stored string values onto the struct. Unparsable values are ignored in
// favour of the default, because one corrupt setting should not prevent the process from
// starting.
func applyStored(c *Runtime, stored map[string]string) {
	setStr(stored, "model_name", &c.ModelName)
	setStr(stored, "title_model_name", &c.TitleModelName)
	setStr(stored, "embedding_model", &c.EmbeddingModel)
	setInt(stored, "max_tokens_for_clear", &c.MaxTokensForClear)
	setInt(stored, "max_length_for_trunc", &c.MaxLengthForTrunc)
	setInt(stored, "summarize_tokens", &c.SummarizeTokens)
	setInt(stored, "memory_inject_limit", &c.MemoryInjectLimit)
	setBool(stored, "enable_execute", &c.EnableExecute)
	setInt(stored, "max_tool_result_bytes", &c.MaxToolResultBytes)
	setInt(stored, "max_scheduled_tasks", &c.MaxScheduledTasks)
	setInt(stored, "min_schedule_interval_sec", &c.MinScheduleIntervalSec)
	setInt(stored, "retrieval_top_k", &c.RetrievalTopK)
}

func setStr(m map[string]string, k string, dst *string) {
	if v, ok := m[k]; ok && v != "" {
		*dst = v
	}
}

func setInt(m map[string]string, k string, dst *int) {
	if v, ok := m[k]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			*dst = n
		}
	}
}

func setBool(m map[string]string, k string, dst *bool) {
	if v, ok := m[k]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}

// Current 返回当前运行时配置的副本。返回副本而非指针，避免调用方无意中改到共享状态。
// Current returns a copy of the current runtime configuration. A copy rather than a pointer,
// so callers cannot accidentally mutate shared state.
func (m *Manager) Current() Runtime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Startup 返回启动参数。
// Startup returns the startup flags.
func (m *Manager) Startup() Startup {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.startup
}

// Update 覆盖运行时配置并落库。校验失败时不做任何改动。
// Update replaces the runtime configuration and persists it. On validation failure nothing
// is changed.
func (m *Manager) Update(ctx context.Context, next Runtime) error {
	if err := Validate(next); err != nil {
		return err
	}
	pairs := map[string]string{
		"model_name":                next.ModelName,
		"title_model_name":          next.TitleModelName,
		"embedding_model":           next.EmbeddingModel,
		"max_tokens_for_clear":      strconv.Itoa(next.MaxTokensForClear),
		"max_length_for_trunc":      strconv.Itoa(next.MaxLengthForTrunc),
		"summarize_tokens":          strconv.Itoa(next.SummarizeTokens),
		"memory_inject_limit":       strconv.Itoa(next.MemoryInjectLimit),
		"enable_execute":            strconv.FormatBool(next.EnableExecute),
		"max_tool_result_bytes":     strconv.Itoa(next.MaxToolResultBytes),
		"max_scheduled_tasks":       strconv.Itoa(next.MaxScheduledTasks),
		"min_schedule_interval_sec": strconv.Itoa(next.MinScheduleIntervalSec),
		"retrieval_top_k":           strconv.Itoa(next.RetrievalTopK),
	}

	err := m.db.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range pairs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO config_kv (key, value) VALUES (?, ?)
				 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return fmt.Errorf("persist %s: %w", k, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.current = next
	m.mu.Unlock()
	return nil
}

// Validate 校验运行时配置。错误信息必须指明是哪一项、为什么不合法，
// 页面才能把原因原样显示给用户，而不是一句笼统的"保存失败"。
// Validate checks a runtime configuration. Error messages must name the offending field and
// the reason, so the UI can surface it verbatim instead of a generic "save failed".
func Validate(c Runtime) error {
	if c.ModelName == "" {
		return errors.New("model_name 不能为空 / model_name must not be empty")
	}
	if c.MaxTokensForClear <= 0 {
		return errors.New("max_tokens_for_clear 必须为正数 / max_tokens_for_clear must be positive")
	}
	if c.SummarizeTokens <= c.MaxTokensForClear {
		return fmt.Errorf(
			"summarize_tokens(%d) 必须大于 max_tokens_for_clear(%d)：须先做可逆的工具结果卸载，再做有损的历史摘要 / "+
				"summarize_tokens(%d) must exceed max_tokens_for_clear(%d): reversible offloading must precede lossy summarization",
			c.SummarizeTokens, c.MaxTokensForClear, c.SummarizeTokens, c.MaxTokensForClear)
	}
	if c.MaxToolResultBytes <= 0 {
		return errors.New("max_tool_result_bytes 必须为正数 / max_tool_result_bytes must be positive")
	}
	if c.MinScheduleIntervalSec <= 0 {
		return errors.New("min_schedule_interval_sec 必须为正数 / min_schedule_interval_sec must be positive")
	}
	if c.MaxScheduledTasks <= 0 {
		return errors.New("max_scheduled_tasks 必须为正数 / max_scheduled_tasks must be positive")
	}
	if c.RetrievalTopK <= 0 {
		return errors.New("retrieval_top_k 必须为正数 / retrieval_top_k must be positive")
	}
	return nil
}

// APIKey 从环境变量读取模型密钥。
// 刻意不缓存到结构体字段里，也不提供任何"导出配置"的路径能带出它。
// APIKey reads the model API key from the environment. It is deliberately not cached in a
// struct field, and no configuration-export path can carry it out.
func APIKey() (string, error) {
	key := os.Getenv(APIKeyEnv)
	if key == "" {
		// 本地服务（Ollama 等）不校验密钥，这里补一个占位值，省得用户为了跑通
		// 本地模型还要先 export 一个假密钥。
		// Local services such as Ollama do not check the key; a placeholder is supplied here so
		// that running a local model does not require exporting a fake key first.
		if IsLocalEndpoint() {
			return "local", nil
		}
		return "", fmt.Errorf(
			"环境变量 %s 未设置，请先 export 后再启动；若要用本地模型，设置 %s=http://localhost:11434/v1 即可免密钥 / "+
				"environment variable %s is not set; export it before starting, or set "+
				"%s=http://localhost:11434/v1 to use a local model without a key",
			APIKeyEnv, BaseURLEnv, APIKeyEnv, BaseURLEnv)
	}
	return key, nil
}
