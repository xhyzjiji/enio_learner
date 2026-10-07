package httpapi

import (
	"errors"
	"net/http"

	"private/agent_basedon_eino/internal/agent/secrets"
)

// 这组接口是单向的：可以写入、可以删除、可以列出名字，但**没有任何一个会返回凭证的值**。
//
// "回显一下方便核对"看起来很友好，但它等于给任何能访问到这个本地端口的东西开一条
// 读取凭证的路，而本服务没有鉴权层。核对的正确做法是重新填一遍。
//
// These endpoints are one-way: values can be written, deleted and enumerated by name, but NO
// endpoint ever returns a credential's value.
//
// "Echo it back so I can check it" sounds friendly, yet it opens a read path to anything that can
// reach this local port — and this service has no auth layer. The right way to verify is to type
// it again.
func (s *Server) registerSecretRoutes() {
	s.mux.HandleFunc("GET /api/secrets", s.handleListSecrets)
	s.mux.HandleFunc("PUT /api/secrets/{name}", s.handleSetSecret)
	s.mux.HandleFunc("DELETE /api/secrets/{name}", s.handleDeleteSecret)
}

// secretValue 是写入凭证的请求体。它只在这一个方向上存在。
// secretValue is the request body for writing a credential. It exists in this direction only.
type secretValue struct {
	Value string `json:"value"`
}

func (s *Server) handleListSecrets(w http.ResponseWriter, _ *http.Request) {
	list, err := s.deps.Secrets.List()
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"secrets": list,
		// 目录路径要告诉页面：用户想手工放一个文件、或想确认凭证到底落在哪里时，
		// 这是唯一的线索。它不是秘密。
		// The directory is reported because it is the only clue a user has when placing a file by
		// hand or checking where credentials actually live. It is not a secret.
		"dir": s.deps.Secrets.Dir(),
	})
}

func (s *Server) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	name, err := RequirePath(r, "name")
	if err != nil {
		WriteError(w, err)
		return
	}
	var body secretValue
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, err)
		return
	}
	if err := s.deps.Secrets.Set(name, body.Value); err != nil {
		// 名字不合法和值为空都是用户输入问题，按 400 回，原因原样显示。
		// Both an invalid name and an empty value are input problems: answer 400 and surface the
		// reason verbatim.
		WriteError(w, &ValidationError{Field: "name", Reason: err.Error()})
		return
	}
	// 回列表而不是回单条，省掉页面再发一次 GET。
	// Return the list rather than the single entry, sparing the UI a follow-up GET.
	s.handleListSecrets(w, r)
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name, err := RequirePath(r, "name")
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := s.deps.Secrets.Delete(name); err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "secret", ID: name})
			return
		}
		WriteError(w, &ValidationError{Field: "name", Reason: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
