package httpapi

import (
	"errors"
	"io"
	"net/http"

	"private/agent_basedon_eino/internal/agent/rag"
)

// maxUploadBytes 限制单次上传大小。
// maxUploadBytes caps a single upload.
const maxUploadBytes = 32 << 20

func (s *Server) registerRAGRoutes() {
	s.mux.HandleFunc("GET /api/rag/documents", s.handleListDocs)
	s.mux.HandleFunc("POST /api/rag/documents", s.handleUploadDoc)
	s.mux.HandleFunc("DELETE /api/rag/documents/{id}", s.handleDeleteDoc)
	s.mux.HandleFunc("POST /api/rag/reindex", s.handleReindex)
	s.mux.HandleFunc("GET /api/rag/status", s.handleRAGStatus)
}

func (s *Server) handleListDocs(w http.ResponseWriter, r *http.Request) {
	docs, err := s.deps.RAG.List(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	if docs == nil {
		docs = []*rag.Document{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"documents": docs,
		"dir":       s.deps.RAG.Dir(),
	})
}

// handleUploadDoc 接收一份上传的文档。
//
// 走 multipart 而非 JSON + base64：base64 会让传输体积涨三分之一，
// 而且要在内存里同时持有编码前后两份数据。
//
// handleUploadDoc receives one uploaded document.
//
// It uses multipart rather than JSON with base64: base64 inflates the payload by a third and
// requires holding both the encoded and decoded copies in memory at once.
func (s *Server) handleUploadDoc(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		WriteError(w, &ValidationError{Reason: "上传解析失败 / cannot parse upload: " + err.Error()})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		WriteError(w, &ValidationError{Field: "file", Reason: "缺少文件字段 / the file field is missing"})
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxUploadBytes))
	if err != nil {
		WriteError(w, err)
		return
	}
	doc, err := s.deps.RAG.Upload(r.Context(), header.Filename, content)
	if err != nil {
		// 即使索引失败也返回那条记录：它带着失败原因，页面要把原因显示出来。
		// The record is returned even when indexing failed: it carries the reason, which the UI
		// needs to display.
		if doc != nil {
			WriteJSON(w, http.StatusOK, doc)
			return
		}
		WriteError(w, &ValidationError{Reason: err.Error()})
		return
	}
	WriteJSON(w, http.StatusCreated, doc)
}

func (s *Server) handleDeleteDoc(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := s.deps.RAG.Delete(r.Context(), id); err != nil {
		if errors.Is(err, rag.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "document", ID: id})
			return
		}
		WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	if err := s.deps.RAG.Reindex(force); err != nil {
		WriteError(w, &ValidationError{Reason: err.Error()})
		return
	}
	WriteJSON(w, http.StatusAccepted, s.deps.RAG.Progress())
}

func (s *Server) handleRAGStatus(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, s.deps.RAG.Progress())
}
