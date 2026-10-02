package httpapi

import (
	"net/http"
	"strings"

	"private/agent_basedon_eino/internal/agent/session"
)

// handleCreateSession 新建会话。
// handleCreateSession creates a session.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	// 允许空请求体：前端点"新建对话"时通常什么都不传。
	// An empty body is allowed: the UI's "new chat" button normally sends nothing.
	if r.ContentLength > 0 {
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
	}
	sess, err := s.deps.Sessions.Create(r.Context(), strings.TrimSpace(req.Title))
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, sess)
}

// handleListSessions 列出会话。
// handleListSessions lists sessions.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Sessions.List(r.Context(), r.URL.Query().Get("archived") == "true")
	if err != nil {
		WriteError(w, err)
		return
	}
	if list == nil {
		list = []*session.Session{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

// handleGetSession 返回单个会话。
// handleGetSession returns a single session.
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	sess, err := s.deps.Sessions.Get(r.Context(), id)
	if err != nil {
		WriteError(w, mapStoreError(err, "session", id))
		return
	}
	WriteJSON(w, http.StatusOK, sess)
}

// handleUpdateSession 重命名或归档会话。
// handleUpdateSession renames or archives a session.
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	// 用指针区分"没传这个字段"与"传了零值"：Archived 传 false 是合法的取消归档操作，
	// 用值类型就没法和"没传"区分开。
	// Pointers distinguish "field absent" from "field set to the zero value": sending
	// Archived=false is a legitimate un-archive request, indistinguishable from absence with a
	// value type.
	var req struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Title == nil && req.Archived == nil {
		WriteError(w, &ValidationError{Reason: "title 与 archived 至少需提供一个 / provide at least one of title or archived"})
		return
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			WriteError(w, &ValidationError{Field: "title", Reason: "标题不能为空 / title must not be empty"})
			return
		}
		if err := s.deps.Sessions.Rename(r.Context(), id, title); err != nil {
			WriteError(w, mapStoreError(err, "session", id))
			return
		}
	}
	if req.Archived != nil {
		if err := s.deps.Sessions.SetArchived(r.Context(), id, *req.Archived); err != nil {
			WriteError(w, mapStoreError(err, "session", id))
			return
		}
	}
	sess, err := s.deps.Sessions.Get(r.Context(), id)
	if err != nil {
		WriteError(w, mapStoreError(err, "session", id))
		return
	}
	WriteJSON(w, http.StatusOK, sess)
}

// handleDeleteSession 删除会话。删除前先中断该会话正在进行的对话，
// 否则那一轮结束时会往已删除的会话里写消息，留下一批无主记录。
// handleDeleteSession deletes a session. Any in-flight turn is interrupted first; otherwise
// that turn would write messages into a deleted session, leaving orphaned rows behind.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	s.deps.Interrupts.Interrupt(id)
	if err := s.deps.Sessions.Delete(r.Context(), id); err != nil {
		WriteError(w, mapStoreError(err, "session", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListMessages 返回会话的完整消息记录。
//
// 这里取的是原始消息表而非 session_context 视图：给用户看的应当是完整历史，
// 压缩只影响喂给模型的那份输入。
//
// handleListMessages returns the complete message record of a session.
//
// It reads the raw message table rather than the session_context view: the user should see the
// full history, while compression only affects the input fed to the model.
func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	if _, err := s.deps.Sessions.Get(r.Context(), id); err != nil {
		WriteError(w, mapStoreError(err, "session", id))
		return
	}
	msgs, err := s.deps.Sessions.ListMessages(r.Context(), id)
	if err != nil {
		WriteError(w, err)
		return
	}
	if msgs == nil {
		msgs = []*session.Message{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}
