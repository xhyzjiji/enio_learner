package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"private/agent_basedon_eino/internal/agent/tools"
	"private/agent_basedon_eino/internal/agent/tools/cli"
	"private/agent_basedon_eino/internal/agent/tools/mcp"
)

// registerToolRoutes 登记工具相关路由。它单独一个函数，是因为工具能力在没有
// Registry 的部署下（例如未来的精简模式）可以整体不挂。
// registerToolRoutes registers the tool routes. It is a separate function so the whole tool
// surface can be omitted in a deployment without a Registry, such as a future slim mode.
func (s *Server) registerToolRoutes() {
	s.mux.HandleFunc("GET /api/tools", s.handleListTools)

	s.mux.HandleFunc("GET /api/tools/mcp", s.handleListMCP)
	s.mux.HandleFunc("POST /api/tools/mcp", s.handleCreateMCP)
	s.mux.HandleFunc("PUT /api/tools/mcp/{id}", s.handleUpdateMCP)
	s.mux.HandleFunc("DELETE /api/tools/mcp/{id}", s.handleDeleteMCP)
	s.mux.HandleFunc("POST /api/tools/mcp/{id}/test", s.handleTestMCP)

	s.mux.HandleFunc("GET /api/tools/cli", s.handleListCLI)
	s.mux.HandleFunc("POST /api/tools/cli", s.handleSaveCLI)
	s.mux.HandleFunc("PUT /api/tools/cli/{id}", s.handleSaveCLIByID)
	s.mux.HandleFunc("DELETE /api/tools/cli/{id}", s.handleDeleteCLI)

	s.mux.HandleFunc("GET /api/tools/policy", s.handleListPolicy)
	s.mux.HandleFunc("POST /api/tools/policy", s.handleSavePolicy)
	s.mux.HandleFunc("DELETE /api/tools/policy/{id}", s.handleDeletePolicy)
}

// handleListTools 返回当前生效的完整工具清单，含来源与是否被改名。
// handleListTools returns the full inventory of currently effective tools, with source and
// rename flags.
func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	_, entries, err := s.deps.Tools.Assemble(r.Context(), s.deps.Config.Current(), tools.Scope{})
	if err != nil {
		WriteError(w, err)
		return
	}
	if entries == nil {
		entries = []tools.Entry{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tools": entries})
}

// ───────────────── MCP ─────────────────

func (s *Server) handleListMCP(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.MCP.List(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	if list == nil {
		list = []*mcp.Server{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"servers": list})
}

func (s *Server) handleCreateMCP(w http.ResponseWriter, r *http.Request) {
	srv, err := decodeMCP(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	created, err := s.deps.MCP.Create(r.Context(), srv)
	if err != nil {
		WriteError(w, wrapMCPError(err, ""))
		return
	}
	WriteJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateMCP(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	srv, err := decodeMCP(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	updated, err := s.deps.MCP.Update(r.Context(), id, srv)
	if err != nil {
		WriteError(w, wrapMCPError(err, id))
		return
	}
	WriteJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteMCP(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := s.deps.MCP.Delete(r.Context(), id); err != nil {
		WriteError(w, wrapMCPError(err, id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestMCP(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	names, err := s.deps.MCP.Test(r.Context(), id, s.deps.Config.Current().MaxToolResultBytes)
	if err != nil {
		if errors.Is(err, mcp.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "mcp server", ID: id})
			return
		}
		// 连不上不是服务端故障，是用户的配置问题，所以返回 200 加失败详情，
		// 让页面能把原因原样显示在那一行旁边，而不是弹一个笼统的报错。
		// A failed connection is a configuration problem rather than a server fault, so it
		// returns 200 with the details, letting the UI show the reason next to that row instead
		// of raising a generic error.
		WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tools": names})
}

// decodeMCP 解析并校验一条 MCP server 配置。
//
// 这里是"密钥不入库"这条规则的执行点：secret_ref 必须长得像环境变量名，
// 任何看起来像密钥本身的值都拒绝。不这么拦的话，用户很自然地会直接把 token
// 粘进那个输入框，而页面文案再怎么写也挡不住。
//
// decodeMCP parses and validates one MCP server configuration.
//
// This is where the "no secrets in the database" rule is enforced: secret_ref must look like an
// environment variable name, and anything resembling an actual secret is rejected. Without this
// check users will naturally paste a token straight into the field, and no amount of UI copy
// prevents it.
func decodeMCP(r *http.Request) (*mcp.Server, error) {
	var body struct {
		Name      string `json:"name"`
		Endpoint  string `json:"endpoint"`
		SecretRef string `json:"secret_ref"`
		Enabled   bool   `json:"enabled"`
	}
	if err := DecodeJSON(r, &body); err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(body.SecretRef)
	if ref != "" {
		if !isEnvVarName(ref) {
			return nil, &ValidationError{
				Field: "secret_ref",
				Reason: "该字段只接受环境变量名（大写字母、数字和下划线），不接受密钥明文。" +
					"请先 export 一个环境变量再在这里填它的名字 / this field accepts an environment " +
					"variable NAME (uppercase letters, digits and underscores), never a literal secret. " +
					"Export the variable first and put its name here",
			}
		}
		if _, ok := os.LookupEnv(ref); !ok {
			return nil, &ValidationError{
				Field:  "secret_ref",
				Reason: "环境变量 " + ref + " 当前未设置 / environment variable " + ref + " is not currently set",
			}
		}
	}
	return &mcp.Server{
		Name: strings.TrimSpace(body.Name), Endpoint: strings.TrimSpace(body.Endpoint),
		SecretRef: ref, Enabled: body.Enabled,
	}, nil
}

func isEnvVarName(s string) bool {
	for i, c := range s {
		switch {
		case c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return s != ""
}

func wrapMCPError(err error, id string) error {
	if errors.Is(err, mcp.ErrNotFound) {
		return &NotFoundError{Kind: "mcp server", ID: id}
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return err
	}
	return &ValidationError{Reason: err.Error()}
}

// ───────────────── 声明式 CLI 工具 / declarative CLI tools ─────────────────

func (s *Server) handleListCLI(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.CLI.ListTools(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	if list == nil {
		list = []*cli.ToolDef{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tools": list})
}

func (s *Server) handleSaveCLI(w http.ResponseWriter, r *http.Request) {
	s.saveCLI(w, r, "")
}

func (s *Server) handleSaveCLIByID(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	s.saveCLI(w, r, id)
}

func (s *Server) saveCLI(w http.ResponseWriter, r *http.Request, id string) {
	var def cli.ToolDef
	if err := DecodeJSON(r, &def); err != nil {
		WriteError(w, err)
		return
	}
	def.ID = id
	saved, err := s.deps.CLI.SaveTool(r.Context(), &def)
	if err != nil {
		WriteError(w, &ValidationError{Reason: err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteCLI(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := s.deps.CLI.DeleteTool(r.Context(), id); err != nil {
		if errors.Is(err, cli.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "cli tool", ID: id})
			return
		}
		WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ───────────────── execute 策略 / execute policy ─────────────────

func (s *Server) handleListPolicy(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.CLI.ListPolicy(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	if list == nil {
		list = []cli.PolicyRule{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"rules": list})
}

func (s *Server) handleSavePolicy(w http.ResponseWriter, r *http.Request) {
	var rule cli.PolicyRule
	if err := DecodeJSON(r, &rule); err != nil {
		WriteError(w, err)
		return
	}
	saved, err := s.deps.CLI.SavePolicy(r.Context(), rule)
	if err != nil {
		WriteError(w, &ValidationError{Reason: err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := s.deps.CLI.DeletePolicy(r.Context(), id); err != nil {
		if errors.Is(err, cli.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "policy rule", ID: id})
			return
		}
		WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
