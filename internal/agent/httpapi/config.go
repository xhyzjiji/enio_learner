package httpapi

import (
	"net/http"

	"private/agent_basedon_eino/internal/agent/config"
)

// handleGetConfig 返回当前运行时配置。
//
// 响应里不包含也永远不会包含 API Key：密钥只存在于进程环境变量中，
// 既不入库也不出接口。页面只需要知道"密钥是否已配置"。
//
// handleGetConfig returns the current runtime configuration.
//
// The response does not and never will contain the API key: the secret lives only in the
// process environment, never in the database and never in a response. The UI only needs to know
// whether a key is configured at all.
func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]any{
		"runtime":         s.deps.Config.Current(),
		"api_key_present": config.HasAPIKey(),
		// 端点地址不是密钥，而且必须让页面看得到：填错模型名时，
		// "我连的到底是智谱还是本地 Ollama" 是第一个要确认的事。
		// The endpoint is not a secret and the UI must show it: when a model name is wrong, the
		// first thing to confirm is whether the service is talking to ZhipuAI or a local Ollama.
		"base_url": config.BaseURL(),
		"is_local": config.IsLocalEndpoint(),
	})
}

// handleUpdateConfig 覆盖运行时配置。
// handleUpdateConfig replaces the runtime configuration.
func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	next := s.deps.Config.Current()
	if err := DecodeJSON(r, &next); err != nil {
		WriteError(w, err)
		return
	}
	if err := config.Validate(next); err != nil {
		WriteError(w, &ValidationError{Reason: err.Error()})
		return
	}
	if err := s.deps.Config.Update(r.Context(), next); err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"runtime": s.deps.Config.Current()})
}
