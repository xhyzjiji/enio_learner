// Package mcp 在 internal/mcpclient 之上提供多 server 管理。
//
// 下层的 mcpclient 只负责"连一个 server 并把它的工具转成 Eino Tool"，它是干净的、
// 不读任何环境变量的一层，本包不修改它，只在其上加：多实例、健康状态、按需重连、
// 密钥从环境变量解引用。
//
// Package mcp adds multi-server management on top of internal/mcpclient.
//
// The lower layer only knows how to connect to one server and convert its tools; it is clean
// and reads no environment variables. This package leaves it untouched and adds multiple
// instances, health tracking, reconnection on demand, and secret dereferencing from the
// environment.
package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"

	"private/agent_basedon_eino/internal/agent/store"
	"private/agent_basedon_eino/internal/mcpclient"
)

// 健康状态取值。
// Health status values.
const (
	HealthUnknown = "unknown"
	HealthOK      = "ok"
	HealthError   = "error"
)

// Server 是一条 MCP server 配置记录。
// Server is one MCP server configuration record.
type Server struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	// SecretRef 存的是**环境变量名**，不是密钥本身。
	// 页面上填 "MY_MCP_TOKEN"，运行时才去 os.Getenv 取值。数据库里放明文密钥意味着
	// 任何能读到 agent.db 的人都拿到了全部凭据，而这个文件既不加密也会被随手备份。
	// SecretRef stores an ENVIRONMENT VARIABLE NAME, not the secret itself. The UI takes
	// "MY_MCP_TOKEN" and the value is fetched via os.Getenv at run time. Plaintext secrets in
	// the database would hand every credential to anyone who can read agent.db — a file that is
	// neither encrypted nor unlikely to be casually backed up.
	SecretRef     string `json:"secret_ref"`
	Enabled       bool   `json:"enabled"`
	Health        string `json:"health"`
	LastError     string `json:"last_error"`
	LastCheckedAt int64  `json:"last_checked_at"`
	CreatedAt     int64  `json:"created_at"`
}

// Manager 管理全部 MCP server 的配置与连接。
// Manager owns the configuration and connections of every MCP server.
type Manager struct {
	db     *store.DB
	logger *slog.Logger

	mu    sync.Mutex
	conns map[string]*conn
}

type conn struct {
	client   *mcpclient.Client
	tools    []tool.BaseTool
	endpoint string
	secret   string
}

// NewManager 构造管理器。
// NewManager builds the manager.
func NewManager(db *store.DB, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{db: db, logger: logger, conns: make(map[string]*conn)}
}

// ErrNotFound 表示 server 不存在。
// ErrNotFound signals that the server does not exist.
var ErrNotFound = errors.New("mcp server not found")

// List 返回全部 server 配置。
// List returns every server configuration.
func (m *Manager) List(ctx context.Context) ([]*Server, error) {
	rows, err := m.db.Read().QueryContext(ctx, `
		SELECT id, name, endpoint, secret_ref, enabled, health, last_error, last_checked_at, created_at
		FROM mcp_servers ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list mcp servers: %w", err)
	}
	defer rows.Close()

	var out []*Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Get 返回单个 server 配置。
// Get returns one server configuration.
func (m *Manager) Get(ctx context.Context, id string) (*Server, error) {
	row := m.db.Read().QueryRowContext(ctx, `
		SELECT id, name, endpoint, secret_ref, enabled, health, last_error, last_checked_at, created_at
		FROM mcp_servers WHERE id = ?`, id)
	s, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanServer(sc scanner) (*Server, error) {
	var s Server
	if err := sc.Scan(&s.ID, &s.Name, &s.Endpoint, &s.SecretRef, &s.Enabled,
		&s.Health, &s.LastError, &s.LastCheckedAt, &s.CreatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// Create 新增一个 server。
// Create adds a server.
func (m *Manager) Create(ctx context.Context, s *Server) (*Server, error) {
	if err := validate(s); err != nil {
		return nil, err
	}
	s.ID = uuid.NewString()
	s.CreatedAt = store.Now()
	s.Health = HealthUnknown
	_, err := m.db.Write().ExecContext(ctx, `
		INSERT INTO mcp_servers (id, name, endpoint, secret_ref, enabled, health, last_error, last_checked_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, '', 0, ?)`,
		s.ID, s.Name, s.Endpoint, s.SecretRef, s.Enabled, s.Health, s.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create mcp server %s: %w", s.Name, err)
	}
	return s, nil
}

// Update 覆盖一个 server 的配置，并断开它已有的连接。
//
// 断开是必须的：端点或密钥变了而连接还挂着，下一轮对话仍然会用旧连接，
// 用户改完配置看不到效果，只会怀疑是不是没保存上。
//
// Update replaces a server's configuration and drops its existing connection.
//
// Dropping is mandatory: with the endpoint or secret changed but the connection still alive,
// the next turn would keep using the old one, and a user who just edited the configuration
// would see no effect and assume the save failed.
func (m *Manager) Update(ctx context.Context, id string, s *Server) (*Server, error) {
	if err := validate(s); err != nil {
		return nil, err
	}
	res, err := m.db.Write().ExecContext(ctx, `
		UPDATE mcp_servers SET name = ?, endpoint = ?, secret_ref = ?, enabled = ?,
		       health = 'unknown', last_error = '', last_checked_at = 0
		WHERE id = ?`, s.Name, s.Endpoint, s.SecretRef, s.Enabled, id)
	if err != nil {
		return nil, fmt.Errorf("update mcp server %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	m.disconnect(id)
	return m.Get(ctx, id)
}

// Delete 删除一个 server。
// Delete removes a server.
func (m *Manager) Delete(ctx context.Context, id string) error {
	res, err := m.db.Write().ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete mcp server %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	m.disconnect(id)
	return nil
}

func validate(s *Server) error {
	if s.Name == "" {
		return errors.New("名称不能为空 / name must not be empty")
	}
	if s.Endpoint == "" {
		return errors.New("端点不能为空 / endpoint must not be empty")
	}
	return nil
}

// resolveSecret 把环境变量名解引用成密钥值。
// resolveSecret dereferences an environment variable name into a secret value.
func resolveSecret(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	v, ok := os.LookupEnv(ref)
	if !ok {
		return "", fmt.Errorf(
			"环境变量 %s 未设置，无法取得该 MCP server 的密钥 / environment variable %s is not set, "+
				"so this MCP server's secret cannot be resolved", ref, ref)
	}
	return v, nil
}

// Test 测试连通性并返回工具名清单，同时把结果写回健康状态。
// Test probes connectivity, returns the tool name list and records the outcome as health.
func (m *Manager) Test(ctx context.Context, id string, maxResultBytes int) ([]string, error) {
	s, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	tools, err := m.connect(ctx, s, maxResultBytes)
	m.recordHealth(ctx, id, err)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			continue
		}
		names = append(names, info.Name)
	}
	return names, nil
}

func (m *Manager) recordHealth(ctx context.Context, id string, err error) {
	health, msg := HealthOK, ""
	if err != nil {
		health, msg = HealthError, err.Error()
	}
	if _, dbErr := m.db.Write().ExecContext(ctx,
		`UPDATE mcp_servers SET health = ?, last_error = ?, last_checked_at = ? WHERE id = ?`,
		health, msg, store.Now(), id); dbErr != nil {
		m.logger.Warn("record mcp health failed", "server", id, "err", dbErr)
	}
}

// Tools 返回全部已启用 server 的工具，供每轮对话装配工具集。
//
// 单个 server 连不上时只记日志并跳过，不让整轮对话失败：一个挂掉的外部服务
// 不应该把本来能正常工作的其他工具一起拖下水。
//
// Tools returns the tools of every enabled server for per-turn assembly.
//
// A server that fails to connect is logged and skipped rather than failing the whole turn: one
// broken external service must not drag down the other tools that would work fine.
func (m *Manager) Tools(ctx context.Context, maxResultBytes int) ([]tool.BaseTool, error) {
	servers, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []tool.BaseTool
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		tools, err := m.connect(ctx, s, maxResultBytes)
		if err != nil {
			m.logger.Warn("mcp server unavailable, its tools are skipped this turn",
				"server", s.Name, "err", err)
			m.recordHealth(ctx, s.ID, err)
			continue
		}
		out = append(out, tools...)
	}
	return out, nil
}

// connect 复用已有连接，配置变化时重连。
// connect reuses an existing connection and reconnects when the configuration changed.
func (m *Manager) connect(ctx context.Context, s *Server, maxResultBytes int) ([]tool.BaseTool, error) {
	secret, err := resolveSecret(s.SecretRef)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if c, ok := m.conns[s.ID]; ok {
		if c.endpoint == s.Endpoint && c.secret == secret {
			tools := c.tools
			m.mu.Unlock()
			return tools, nil
		}
		_ = c.client.Close()
		delete(m.conns, s.ID)
	}
	m.mu.Unlock()

	// 拨号放在锁外：连一个不响应的外部服务可能要等满超时，
	// 持锁等待会让这段时间里所有 server 的工具装配全部排队。
	// Dialling happens outside the lock: reaching an unresponsive external service can burn the
	// full timeout, and holding the lock would queue up tool assembly for every other server.
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cfg := mcpclient.Config{Endpoint: s.Endpoint, Secret: secret, MaxResultBytes: maxResultBytes}
	cli, err := mcpclient.Dial(dialCtx, cfg)
	if err != nil {
		return nil, err
	}
	tools, err := cli.Tools(dialCtx, cfg)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}

	m.mu.Lock()
	// 并发拨号时后到者复用先到者的连接，避免同一个 server 留下两条连接。
	// On concurrent dials the later one reuses the earlier connection, so a server never ends up
	// with two of them.
	if existing, ok := m.conns[s.ID]; ok && existing.endpoint == s.Endpoint && existing.secret == secret {
		m.mu.Unlock()
		_ = cli.Close()
		return existing.tools, nil
	}
	m.conns[s.ID] = &conn{client: cli, tools: tools, endpoint: s.Endpoint, secret: secret}
	m.mu.Unlock()
	return tools, nil
}

func (m *Manager) disconnect(id string) {
	m.mu.Lock()
	c, ok := m.conns[id]
	delete(m.conns, id)
	m.mu.Unlock()
	if ok {
		_ = c.client.Close()
	}
}

// Close 关闭全部连接。
// Close shuts every connection down.
func (m *Manager) Close() {
	m.mu.Lock()
	conns := m.conns
	m.conns = make(map[string]*conn)
	m.mu.Unlock()
	for _, c := range conns {
		_ = c.client.Close()
	}
}
