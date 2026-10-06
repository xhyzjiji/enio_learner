package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/schema"

	"private/agent_basedon_eino/internal/agent/approval"
	"private/agent_basedon_eino/internal/agent/kernel"
	"private/agent_basedon_eino/internal/agent/session"
)

// ChatRequest 是发起一轮对话的请求体。
// ChatRequest is the body of one conversation turn.
type ChatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// InterruptRegistry 记录正在进行的对话，供中断接口取消。
//
// 中断的实现方式是取消 context：Agent 的整条调用链（模型 HTTP 请求、工具子进程、
// MCP 调用）都挂在同一个 context 上，取消它就是一次性把这一轮彻底停掉。
// 如果只在事件循环里加个 break，模型那边的请求还会继续跑完并计费。
//
// InterruptRegistry tracks in-flight conversations so the interrupt endpoint can cancel them.
//
// Interruption works by cancelling a context: the agent's entire call chain — the model HTTP
// request, tool subprocesses, MCP calls — hangs off one context, so cancelling it stops the
// whole turn at once. Merely breaking out of the event loop would leave the model request
// running to completion, and billed.
type InterruptRegistry struct {
	mu      sync.Mutex
	seq     uint64
	running map[string]*inflight
}

// inflight 是一轮正在进行的对话。token 用于区分同一会话的前后两轮：
// 没有它的话，新一轮结束时会把旧一轮残留的登记删掉，或者反过来。
// inflight is one running turn. The token distinguishes successive turns of the same session:
// without it, a finishing turn could delete the registration belonging to another one.
type inflight struct {
	token  uint64
	cancel context.CancelFunc
}

// NewInterruptRegistry 构造注册表。
// NewInterruptRegistry builds the registry.
func NewInterruptRegistry() *InterruptRegistry {
	return &InterruptRegistry{running: make(map[string]*inflight)}
}

func (r *InterruptRegistry) register(sessionID string, cancel context.CancelFunc) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 同一会话已有对话在跑时先取消旧的，避免两轮同时往一个会话里写消息。
	// If a turn is already running for this session, cancel it first so two turns cannot write
	// messages into the same session concurrently.
	if prev, ok := r.running[sessionID]; ok {
		prev.cancel()
	}
	r.seq++
	r.running[sessionID] = &inflight{token: r.seq, cancel: cancel}
	return r.seq
}

func (r *InterruptRegistry) release(sessionID string, token uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.running[sessionID]; ok && cur.token == token {
		delete(r.running, sessionID)
	}
}

// Interrupt 取消指定会话正在进行的对话，返回是否确实取消了一个。
// Interrupt cancels the in-flight turn of a session and reports whether one was cancelled.
func (r *InterruptRegistry) Interrupt(sessionID string) bool {
	r.mu.Lock()
	cur, ok := r.running[sessionID]
	if ok {
		delete(r.running, sessionID)
	}
	r.mu.Unlock()
	if ok {
		cur.cancel()
	}
	return ok
}

// handleChat 处理一轮对话，以 SSE 推送过程与结果。
// handleChat runs one conversation turn, streaming progress and results over SSE.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		WriteError(w, &ValidationError{Field: "message", Reason: "消息不能为空 / message must not be empty"})
		return
	}
	if req.SessionID == "" {
		WriteError(w, &ValidationError{Field: "session_id", Reason: "会话 ID 不能为空 / session_id must not be empty"})
		return
	}
	if _, err := s.deps.Sessions.Get(r.Context(), req.SessionID); err != nil {
		WriteError(w, mapStoreError(err, "session", req.SessionID))
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		WriteError(w, err)
		return
	}

	// 用 WithoutCancel 派生：HTTP 请求结束不应自动杀掉这一轮，只有显式中断才应该。
	// 浏览器刷新页面就断连，若沿用请求 context，用户刷新一下正在生成的回复就没了。
	// Derived with WithoutCancel: the end of the HTTP request must not kill the turn; only an
	// explicit interrupt should. A browser refresh drops the connection, and inheriting the
	// request context would throw away a reply that is still being generated.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	token := s.deps.Interrupts.register(req.SessionID, cancel)
	defer s.deps.Interrupts.release(req.SessionID, token)

	s.runTurn(runCtx, sse, req)
}

func (s *Server) runTurn(ctx context.Context, sse *SSEWriter, req ChatRequest) {
	userMsg := schema.UserMessage(req.Message)
	if err := s.deps.Sessions.AppendMessages(ctx, req.SessionID, []*schema.Message{userMsg}); err != nil {
		sse.SendError(err)
		return
	}
	// 首条用户消息落库之后立刻触发标题生成，它在自己的 goroutine 里跑，不阻塞这一轮。
	// Title generation fires right after the first user message is persisted; it runs in its
	// own goroutine and never blocks this turn.
	s.deps.Titles.EnsureAsync(req.SessionID)

	// 用户发了新消息，就意味着上一轮悬着的确认已经不作数了。
	// 不作废的话，那张旧卡片会和新一轮并存，点下去恢复的是一段早已过时的执行。
	//
	// A new user message means any confirmation still pending from the previous turn is moot.
	// Left alone, the stale card would coexist with the new turn, and clicking it would resume
	// an execution that has long been superseded.
	s.abandonPending(ctx, req.SessionID)

	checkpointID := uuid.NewString()
	iter, _, err := s.deps.Engine.Run(ctx, req.SessionID, req.Message, checkpointID, kernel.TurnOptions{})
	if err != nil {
		sse.SendError(err)
		return
	}

	result, pumpErr := PumpEvents(iter, sse)
	s.finishTurn(ctx, sse, req.SessionID, checkpointID, result)
	if len(result.Messages) > 0 {
		if err := s.deps.Sessions.AppendMessages(ctx, req.SessionID, result.Messages); err != nil {
			s.Logger().Error("persist turn messages failed", "session", req.SessionID, "err", err)
		}
	}
	if result.Compressed {
		s.afterCompression(ctx, sse, req.SessionID)
	}
	if pumpErr != nil {
		// 被中断时不算错误：用户主动停的，已生成的部分照常落库。
		// Interruption is not an error: the user asked for it, and whatever was generated is
		// persisted as usual.
		if errors.Is(pumpErr, context.Canceled) {
			result.Interrupted = true
		} else {
			sse.SendError(pumpErr)
			return
		}
	}
	_ = sse.Send(EventDone, DonePayload{SessionID: req.SessionID, Interrupted: result.Interrupted})
}

// compactHintThreshold 是提示用户新开会话的压缩次数阈值。
//
// 摘要是有损的：压过一次尚可，压到第三次时最早那部分对话已经是"摘要的摘要的摘要"，
// 模型对它的把握非常弱，而用户往往察觉不到——表现为"它怎么把前面说好的事忘了"。
// 与其让人困惑，不如明说一句。
//
// compactHintThreshold is the number of compressions after which the user is advised to start a
// new conversation.
//
// Summarization is lossy: once is fine, but by the third pass the earliest part of the
// conversation is a summary of a summary of a summary. The model's grip on it is weak while the
// user usually cannot tell, experiencing it only as "why did it forget what we agreed earlier".
// Saying so plainly beats leaving people puzzled.
const compactHintThreshold = 3

// afterCompression 记录压缩次数，必要时提示用户新开会话。
// afterCompression records the compression and advises a new conversation when warranted.
func (s *Server) afterCompression(ctx context.Context, sse *SSEWriter, sessionID string) {
	count, err := s.deps.Sessions.IncCompactCount(ctx, sessionID)
	if err != nil {
		s.Logger().Warn("record compaction failed", "session", sessionID, "err", err)
		return
	}
	if count < compactHintThreshold {
		return
	}
	_ = sse.Send(EventCompression, CompressionPayload{
		Stage: "advice",
		Message: fmt.Sprintf(
			"本会话已压缩 %d 次，较早的内容保真度已明显下降，建议新开一个会话 / "+
				"this conversation has been compressed %d times and early content has noticeably "+
				"degraded; consider starting a new one", count, count),
	})
}

// handleInterrupt 中断指定会话正在进行的对话。
// handleInterrupt interrupts the in-flight turn of a session.
func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.SessionID == "" {
		WriteError(w, &ValidationError{Field: "session_id", Reason: "会话 ID 不能为空 / session_id must not be empty"})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"interrupted": s.deps.Interrupts.Interrupt(req.SessionID)})
}

// mapStoreError 把存储层错误翻译成 HTTP 层错误。
// mapStoreError translates storage errors into HTTP-layer errors.
func mapStoreError(err error, kind, id string) error {
	if errors.Is(err, session.ErrNotFound) {
		return &NotFoundError{Kind: kind, ID: id}
	}
	return err
}

// finishTurn 处理一轮结束后的善后：要么登记待确认，要么清掉断点。
//
// 这两件事必须二选一且都不能漏。漏了登记，中断的那一轮就永远没人能恢复，断点成了
// 库里的孤儿；漏了清理，每一轮跑完都留下几十 KB 死数据，因为 eino 自己不删。
//
// finishTurn handles the aftermath of a turn: either register pending confirmations, or drop
// the checkpoint.
//
// Exactly one of the two must happen, and neither may be skipped. Skipping registration leaves
// an interrupted turn that nobody can ever resume, its checkpoint orphaned in the database;
// skipping cleanup leaves tens of kilobytes of dead data behind every turn, because eino does
// not delete checkpoints itself.
func (s *Server) finishTurn(
	ctx context.Context, sse *SSEWriter, sessionID, checkpointID string, result *StreamResult,
) {
	if len(result.Pending) == 0 {
		if err := s.deps.Engine.DropCheckpoint(ctx, checkpointID); err != nil {
			s.Logger().Warn("drop checkpoint failed", "checkpoint", checkpointID, "err", err)
		}
		return
	}
	for _, p := range result.Pending {
		rec, err := s.deps.Approvals.Create(ctx, approval.Record{
			SessionID:    sessionID,
			CheckpointID: checkpointID,
			InterruptID:  p.InterruptID,
			Command:      p.Command,
		})
		if err != nil {
			sse.SendError(err)
			return
		}
		if err := sse.Send(EventApproval, rec); err != nil {
			s.Logger().Warn("push approval request failed", "session", sessionID, "err", err)
		}
	}
}

// abandonPending 作废一个会话里悬而未决的确认，并删掉它们的断点。
// abandonPending voids a conversation's outstanding confirmations and drops their checkpoints.
func (s *Server) abandonPending(ctx context.Context, sessionID string) {
	cps, err := s.deps.Approvals.AbandonSession(ctx, sessionID)
	if err != nil {
		s.Logger().Warn("abandon pending approvals failed", "session", sessionID, "err", err)
		return
	}
	for _, cp := range cps {
		if err := s.deps.Engine.DropCheckpoint(ctx, cp); err != nil {
			s.Logger().Warn("drop abandoned checkpoint failed", "checkpoint", cp, "err", err)
		}
	}
}

// ResumeRequest 是前端提交的确认结论。
// ResumeRequest is the verdict submitted by the frontend.
type ResumeRequest struct {
	ApprovalID string `json:"approval_id"`
	Approved   bool   `json:"approved"`
	Reason     string `json:"reason"`
}

// handleResume 接收用户的确认结论，并把被中断的那一轮接着跑完。
//
// 它和 /api/chat 一样是 SSE 接口，而不是一个返回 ok 的普通 POST：恢复之后模型还要
// 继续生成，那些输出得有地方去。做成普通 POST 的话，前端得先确认再另开一条流，
// 中间那段时间里产生的事件就丢了。
//
// handleResume receives the user's verdict and runs the interrupted turn to completion.
//
// Like /api/chat it is an SSE endpoint rather than a plain POST returning ok: the model keeps
// generating after the resume, and that output needs somewhere to go. As a plain POST the
// frontend would have to confirm first and open a stream second, losing every event produced in
// between.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	var req ResumeRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.ApprovalID == "" {
		WriteError(w, &ValidationError{Field: "approval_id", Reason: "approval_id 不能为空 / approval_id must not be empty"})
		return
	}
	rec, err := s.deps.Approvals.Get(r.Context(), req.ApprovalID)
	if err != nil {
		if errors.Is(err, approval.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "approval", ID: req.ApprovalID})
			return
		}
		WriteError(w, err)
		return
	}

	reason := req.Reason
	if !req.Approved && reason == "" {
		reason = "用户拒绝了这条命令 / the user refused this command"
	}
	decision := approval.Decision{Approved: req.Approved, Reason: reason}

	// 先落决定再恢复。两个标签页同时点时只有第一下拿到 true，第二下看到记录已不是
	// pending 便直接返回——否则同一条命令会被恢复两次，也就执行两次。
	//
	// The verdict is recorded before resuming. With two tabs clicking at once only the first
	// gets true; the second sees the record is no longer pending and returns — otherwise the
	// same command would be resumed, and therefore executed, twice.
	ok, err := s.deps.Approvals.Decide(r.Context(), rec.ID, decision)
	if err != nil {
		WriteError(w, err)
		return
	}
	if !ok {
		WriteError(w, &ValidationError{
			Field:  "approval_id",
			Reason: "该确认已被处理过 / this confirmation was already handled",
		})
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		WriteError(w, err)
		return
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	token := s.deps.Interrupts.register(rec.SessionID, cancel)
	defer s.deps.Interrupts.release(rec.SessionID, token)

	iter, _, err := s.deps.Engine.Resume(
		runCtx, rec.SessionID, rec.CheckpointID, rec.InterruptID, decision)
	if err != nil {
		sse.SendError(err)
		return
	}

	result, pumpErr := PumpEvents(iter, sse)
	if len(result.Messages) > 0 {
		if err := s.deps.Sessions.AppendMessages(runCtx, rec.SessionID, result.Messages); err != nil {
			s.Logger().Error("persist resumed messages failed", "session", rec.SessionID, "err", err)
		}
	}
	s.finishTurn(runCtx, sse, rec.SessionID, rec.CheckpointID, result)
	if result.Compressed {
		s.afterCompression(runCtx, sse, rec.SessionID)
	}
	if pumpErr != nil {
		if errors.Is(pumpErr, context.Canceled) {
			result.Interrupted = true
		} else {
			sse.SendError(pumpErr)
			return
		}
	}
	_ = sse.Send(EventDone, DonePayload{SessionID: rec.SessionID, Interrupted: result.Interrupted})
}

// handleListApprovals 返回一个会话里还在等待的确认。
// 页面加载会话时调它，待确认卡片因此能在刷新或后端重启之后重现。
//
// handleListApprovals returns a conversation's still-pending confirmations. The UI calls it when
// loading a conversation, which is how a pending card survives a refresh or a backend restart.
func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	sessionID, err := RequirePath(r, "id")
	if err != nil {
		WriteError(w, err)
		return
	}
	list, err := s.deps.Approvals.ListPending(r.Context(), sessionID)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"approvals": list})
}
