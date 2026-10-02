package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"private/agent_basedon_eino/internal/agent/schedule"
)

func (s *Server) registerTaskRoutes() {
	s.mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	s.mux.HandleFunc("POST /api/tasks", s.handleSaveTask)
	s.mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)
	s.mux.HandleFunc("POST /api/tasks/{id}/run", s.handleRunTask)
	s.mux.HandleFunc("GET /api/tasks/{id}/runs", s.handleListRuns)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.deps.Tasks.List(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	if tasks == nil {
		tasks = []*schedule.Task{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleSaveTask(w http.ResponseWriter, r *http.Request) {
	var t schedule.Task
	if err := DecodeJSON(r, &t); err != nil {
		WriteError(w, err)
		return
	}

	// 页面上改的表达式同样要过最小间隔检查。放行的话，用户手填一个每秒执行
	// 就绕开了给模型设的那道闸。
	// Expressions edited in the UI go through the same minimum-interval check. Letting them
	// through would let a hand-typed per-second schedule bypass the gate meant for the model.
	rt := s.deps.Config.Current()
	next, err := schedule.ValidateCron(t.Cron, time.Duration(rt.MinScheduleIntervalSec)*time.Second)
	if err != nil {
		WriteError(w, &ValidationError{Field: "cron", Reason: err.Error()})
		return
	}
	t.NextRunAt = next.UnixMilli()

	saved, err := s.deps.Tasks.Save(r.Context(), &t)
	if err != nil {
		WriteError(w, &ValidationError{Reason: err.Error()})
		return
	}
	if err := s.deps.Scheduler.Sync(r.Context(), saved); err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}

	// 先摘掉内存里的调度再删库：反过来的话，删库到摘调度之间恰好触发一次执行，
	// fire 会读到一个已不存在的任务。
	// Unschedule before deleting the row: the other order leaves a window where a fire between
	// the delete and the unschedule reads a task that no longer exists.
	s.deps.Scheduler.Unschedule(id)
	if err := s.deps.Tasks.Delete(r.Context(), id); err != nil {
		if errors.Is(err, schedule.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "scheduled task", ID: id})
			return
		}
		WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunTask(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	t, err := s.deps.Tasks.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, schedule.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "scheduled task", ID: id})
			return
		}
		WriteError(w, err)
		return
	}

	// 手动触发也可能跑满 taskTimeout，不能挂在 HTTP 请求的生命周期上——
	// 浏览器一刷新 context 就取消，任务半路夭折。
	// A manual trigger can also run for the full task timeout, so it must not hang off the HTTP
	// request lifetime: one browser refresh cancels the context and kills the run halfway.
	runCtx := context.WithoutCancel(r.Context())
	go s.deps.Scheduler.RunNow(runCtx, t)

	WriteJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	id, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	runs, err := s.deps.Tasks.ListRuns(r.Context(), id)
	if err != nil {
		WriteError(w, err)
		return
	}
	if runs == nil {
		runs = []*schedule.Run{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"runs": runs})
}
