package httpapi

import (
	"errors"
	"net/http"

	"private/agent_basedon_eino/internal/agent/memory"
)

func (s *Server) registerMemoryRoutes() {
	s.mux.HandleFunc("GET /api/memories", s.handleListMemories)
	s.mux.HandleFunc("PUT /api/memories/{key}", s.handlePutMemory)
	s.mux.HandleFunc("DELETE /api/memories/{key}", s.handleDeleteMemory)
}

func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Memory.List(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	if list == nil {
		list = []memory.Memory{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"memories": list})
}

func (s *Server) handlePutMemory(w http.ResponseWriter, r *http.Request) {
	key, err := RequirePath(r, "key")
	if err != nil {
		WriteError(w, err)
		return
	}
	var body struct {
		Content  string `json:"content"`
		Category string `json:"category"`
	}
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, err)
		return
	}
	// 保留原有的 created_at：页面上的编辑是"修正内容"，不是"重新认识这件事"。
	// The original created_at is preserved: editing in the UI corrects the content rather than
	// learning the fact anew.
	var createdAt int64
	if existing, err := s.deps.Memory.Get(r.Context(), key); err == nil {
		createdAt = existing.CreatedAt
	}
	saved, err := s.deps.Memory.Put(r.Context(), memory.Memory{
		Key: key, Content: body.Content, Category: body.Category, CreatedAt: createdAt,
	})
	if err != nil {
		WriteError(w, &ValidationError{Reason: err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	key, err := RequirePath(r, "key")
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := s.deps.Memory.Delete(r.Context(), key); err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "memory", ID: key})
			return
		}
		WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
