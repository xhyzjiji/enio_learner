// Package httpapi 提供通用 Agent 的 HTTP 接口与静态资源服务。
//
// 路由用标准库 net/http.ServeMux。Go 1.22 起它原生支持 "POST /api/sessions/{id}"
// 这种方法与路径参数写法，不再需要第三方路由库——当前工程依赖很干净，不为此破例。
//
// Package httpapi serves the HTTP API and static assets for the general agent runtime.
//
// Routing uses the standard library net/http.ServeMux. Since Go 1.22 it natively supports
// patterns like "POST /api/sessions/{id}" with method and path parameters, so no third-party
// router is needed — the dependency set of this project is clean and stays that way.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"private/agent_basedon_eino/internal/agent/approval"
	"private/agent_basedon_eino/internal/agent/config"
	"private/agent_basedon_eino/internal/agent/kernel"
	"private/agent_basedon_eino/internal/agent/memory"
	"private/agent_basedon_eino/internal/agent/rag"
	"private/agent_basedon_eino/internal/agent/schedule"
	"private/agent_basedon_eino/internal/agent/session"
	"private/agent_basedon_eino/internal/agent/skills"
	"private/agent_basedon_eino/internal/agent/tools"
	"private/agent_basedon_eino/internal/agent/tools/cli"
	"private/agent_basedon_eino/internal/agent/tools/mcp"
)

// Server 聚合所有 handler 所需的依赖。各分区 handler 以方法形式挂在它上面。
// Server aggregates every dependency the handlers need. Each section's handlers hang off it
// as methods.
type Server struct {
	mux  *http.ServeMux
	deps Deps
}

// Deps 是 Server 的依赖集合。分区 handler 在各自文件中按需取用。
// Deps is the dependency set of Server. Section handlers pick what they need in their own files.
type Deps struct {
	Logger     *slog.Logger
	Sessions   *session.Store
	Titles     *session.TitleGenerator
	Engine     *kernel.Engine
	Config     *config.Manager
	Interrupts *InterruptRegistry
	Tools      *tools.Registry
	MCP        *mcp.Manager
	CLI        *cli.Store
	Skills     *skills.Store
	RAG        *rag.Manager
	Memory     *memory.Store
	Tasks      *schedule.Store
	Scheduler  *schedule.Scheduler
	Approvals  *approval.Broker
}

// NewServer 构造服务器并注册全部路由。
// NewServer builds the server and registers all routes.
func NewServer(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Interrupts == nil {
		deps.Interrupts = NewInterruptRegistry()
	}
	s := &Server{mux: http.NewServeMux(), deps: deps}
	s.routes()
	return s
}

// routes 集中登记路由。放在一处是为了让"这个服务到底暴露了哪些口子"一眼可见，
// 这对一个没有鉴权层的本地服务尤其重要。
// routes registers every route in one place, so that "what does this service actually expose"
// is answerable at a glance — which matters especially for a local service with no auth layer.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)

	s.mux.HandleFunc("POST /api/chat", s.handleChat)
	s.mux.HandleFunc("POST /api/chat/interrupt", s.handleInterrupt)
	s.mux.HandleFunc("POST /api/chat/approve", s.handleApprove)

	s.mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	s.mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("PATCH /api/sessions/{id}", s.handleUpdateSession)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	s.mux.HandleFunc("GET /api/sessions/{id}/messages", s.handleListMessages)

	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.HandleFunc("PUT /api/config", s.handleUpdateConfig)

	if s.deps.Tools != nil {
		s.registerToolRoutes()
	}
	if s.deps.Skills != nil {
		s.registerSkillRoutes()
	}
	if s.deps.RAG != nil {
		s.registerRAGRoutes()
	}
	if s.deps.Memory != nil {
		s.registerMemoryRoutes()
	}
	if s.deps.Tasks != nil && s.deps.Scheduler != nil {
		s.registerTaskRoutes()
	}
}

// ServeHTTP 实现 http.Handler。
// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Mux 暴露底层 ServeMux，供其他文件中的分区注册路由。
// Mux exposes the underlying ServeMux so route registration can live in per-section files.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Logger 返回日志器。
// Logger returns the logger.
func (s *Server) Logger() *slog.Logger { return s.deps.Logger }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ───────────────── 统一响应 / unified responses ─────────────────

// errorBody 是所有失败响应的统一结构。
// message 直接面向用户，必须说清楚"哪一项、为什么"，不能是笼统的 "bad request"。
// errorBody is the single shape of every failure response.
// message is user-facing and must state which field failed and why, never a generic
// "bad request".
type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// ValidationError 表示请求参数不合法，携带具体字段名。
// ValidationError signals an invalid request parameter and carries the offending field name.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Reason
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}

// NotFoundError 表示目标资源不存在。
// NotFoundError signals that the target resource does not exist.
type NotFoundError struct {
	Kind string
	ID   string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %q 不存在 / %s %q not found", e.Kind, e.ID, e.Kind, e.ID)
}

// WriteJSON 写出一个 JSON 响应。
// WriteJSON emits a JSON response.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 响应头已经发出，此时无法再改状态码，只能记录。
		// Headers are already flushed, so the status cannot be changed; log and move on.
		slog.Default().Error("encode response body failed", "err", err)
	}
}

// WriteError 把 error 映射为合适的状态码与响应体。
// WriteError maps an error onto an appropriate status code and response body.
func WriteError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		WriteJSON(w, http.StatusBadRequest, errorBody{
			Error: "invalid_request", Message: ve.Reason, Field: ve.Field,
		})
		return
	}
	var nf *NotFoundError
	if errors.As(err, &nf) {
		WriteJSON(w, http.StatusNotFound, errorBody{
			Error: "not_found", Message: nf.Error(),
		})
		return
	}
	WriteJSON(w, http.StatusInternalServerError, errorBody{
		Error: "internal_error", Message: err.Error(),
	})
}

// DecodeJSON 解析请求体。禁止未知字段：页面传了拼错的字段名时应当报错，
// 而不是静默忽略然后让用户以为改生效了。
// DecodeJSON parses the request body. Unknown fields are rejected: if the UI sends a misspelled
// field name it must fail loudly, rather than being silently ignored while the user believes
// the change took effect.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &ValidationError{Reason: fmt.Sprintf("请求体解析失败 / cannot parse request body: %v", err)}
	}
	return nil
}

// RequirePath 取出路径参数并确保非空。
// RequirePath extracts a path parameter and ensures it is non-empty.
func RequirePath(r *http.Request, name string) (string, error) {
	v := strings.TrimSpace(r.PathValue(name))
	if v == "" {
		return "", &ValidationError{Field: name, Reason: "路径参数不能为空 / path parameter must not be empty"}
	}
	return v, nil
}
