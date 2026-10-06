package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

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

	// 在这一轮开始前登记确认监听，并在结束时注销。
	// 顺序要紧：必须早于 Engine.Run，否则模型在首个工具调用里就要确认时，
	// broker 还找不到监听者，命令会以「没有页面在监听」被拒。
	//
	// Register the confirmation listener before this turn starts and unsubscribe at the end.
	// Order matters: it must precede Engine.Run, otherwise a model that needs confirmation on
	// its very first tool call would find no listener registered and the command would be
	// refused as "no page is listening".
	stopForward := s.forwardApprovals(ctx, sse, req.SessionID)
	defer stopForward()

	iter, _, err := s.deps.Engine.Run(ctx, req.SessionID, req.Message, kernel.TurnOptions{})
	if err != nil {
		sse.SendError(err)
		return
	}

	result, pumpErr := PumpEvents(iter, sse)
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

// forwardApprovals 把确认请求推送到 SSE 流，返回注销函数。
// forwardApprovals pushes confirmation requests onto the SSE stream and returns an unsubscribe
// function.
func (s *Server) forwardApprovals(ctx context.Context, sse *SSEWriter, sessionID string) func() {
	if s.deps.Approvals == nil {
		return func() {}
	}
	requests, unsubscribe := s.deps.Approvals.Subscribe(sessionID)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			select {
			case req, ok := <-requests:
				if !ok {
					return
				}
				if err := sse.Send(EventApproval, req); err != nil {
					s.Logger().Warn("push approval request failed",
						"session", sessionID, "err", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return func() {
		unsubscribe()
		<-done
	}
}

// ApproveRequest 是前端提交的确认结论。
// ApproveRequest is the verdict submitted by the frontend.
type ApproveRequest struct {
	RequestID string `json:"request_id"`
	Approved  bool   `json:"approved"`
	Reason    string `json:"reason"`
}

// handleApprove 接收用户对一条命令的确认或拒绝。
// handleApprove receives the user's approval or refusal of one command.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	var req ApproveRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.RequestID == "" {
		WriteError(w, &ValidationError{Field: "request_id", Reason: "request_id 不能为空 / request_id must not be empty"})
		return
	}
	if s.deps.Approvals == nil {
		WriteError(w, errors.New("确认机制未启用 / the confirmation mechanism is not enabled"))
		return
	}
	reason := req.Reason
	if !req.Approved && reason == "" {
		reason = "用户拒绝了这条命令 / the user refused this command"
	}
	ok := s.deps.Approvals.Resolve(req.RequestID, approval.Decision{Approved: req.Approved, Reason: reason})
	// 请求已消失说明等待方已经超时放弃。如实告诉前端，让它把卡片标成失效，
	// 而不是显示「已批准」却什么都没发生。
	//
	// A vanished request means the waiting side already timed out. Say so plainly, so the
	// frontend can mark the card as stale rather than showing "approved" while nothing happens.
	WriteJSON(w, http.StatusOK, map[string]bool{"resolved": ok})
}
