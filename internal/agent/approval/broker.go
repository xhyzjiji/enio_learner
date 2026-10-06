// Package approval 实现 shell 命令的人工确认。
//
// 在此之前 execute 只有"全开"和"全关"两种状态：全关则模型碰不到 shell，全开则它在
// 之后的每一轮里都握着一个完整的 shell，而你无从知道它某一次究竟跑了什么——等你
// 在对话里看到结果时，命令早已执行完毕。
//
// 本包提供第三种状态：命令在执行**之前**停下来等你点头。模型仍然可以提议任何命令，
// 但提议和执行之间多了一个你。
//
// Package approval implements human confirmation for shell commands.
//
// Until now execute had only two states: off, and the model holds a full shell in every
// subsequent turn with no way for you to know what any given invocation actually ran — by the
// time you read the result in the conversation, the command has long since executed.
//
// This package adds a third state: the command stops and waits for your approval BEFORE running.
// The model may still propose anything; what changes is that you now sit between the proposal
// and the execution.
package approval

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// waitTimeout 是等待人工决定的上限。
//
// 超时按**拒绝**处理，不是批准。理由是超时最常见的成因是人不在电脑前，
// 而「没人看着」恰恰是最不该放行命令的时刻。
//
// waitTimeout bounds how long a decision is awaited.
//
// A timeout counts as a DENIAL, never an approval. The usual cause of a timeout is nobody being
// at the keyboard, and "nobody is watching" is precisely the moment a command should not run.
const waitTimeout = 5 * time.Minute

// ErrNoReviewer 表示没有任何前端连接在监听该会话。
// ErrNoReviewer signals that no frontend is listening on this conversation.
var ErrNoReviewer = errors.New("no reviewer is attached to this conversation")

// Request 是一次待确认的命令。
// Request is one command awaiting confirmation.
type Request struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	CreatedAt int64  `json:"created_at"`
}

// Decision 是人工给出的结论。
// Decision is the human verdict.
type Decision struct {
	Approved bool
	Reason   string
}

type pending struct {
	req    *Request
	result chan Decision
}

// Broker 在工具执行线程与前端之间传递确认请求。
// Broker relays confirmation requests between the tool execution thread and the frontend.
type Broker struct {
	mu        sync.Mutex
	listeners map[string]chan *Request
	pendings  map[string]*pending
}

// NewBroker 构造一个 broker。
// NewBroker builds a broker.
func NewBroker() *Broker {
	return &Broker{
		listeners: make(map[string]chan *Request),
		pendings:  make(map[string]*pending),
	}
}

// Subscribe 登记一个会话的监听者，返回请求通道与注销函数。
//
// 同一会话同时只允许一个监听者：用户在两个标签页开着同一个会话时，
// 确认请求只推给最新的那个，避免两边各点一次导致命令被执行两遍。
//
// Subscribe registers a listener for one conversation, returning the request channel and an
// unsubscribe function.
//
// Only one listener per conversation is allowed at a time: with the same conversation open in
// two tabs, the request goes only to the most recent one, so that approving in both cannot run
// the command twice.
func (b *Broker) Subscribe(sessionID string) (<-chan *Request, func()) {
	ch := make(chan *Request, 4)

	b.mu.Lock()
	if old, ok := b.listeners[sessionID]; ok {
		close(old)
	}
	b.listeners[sessionID] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if cur, ok := b.listeners[sessionID]; ok && cur == ch {
			delete(b.listeners, sessionID)
			close(ch)
		}
	}
}

// Ask 提交一条命令等待确认，阻塞直到有结论、超时或上下文取消。
//
// 没有监听者时直接返回 ErrNoReviewer 而不是默默放行：定时任务、或者用户关掉页面
// 之后仍在后台跑完的那一轮，都属于"无人可确认"的情形，此时放行等于把确认机制
// 变成一个只在有人看着时才生效的装饰品。
//
// Ask submits a command for confirmation, blocking until a verdict, a timeout or context
// cancellation.
//
// With no listener it returns ErrNoReviewer rather than quietly allowing the command: scheduled
// tasks, and turns that keep running in the background after the user closed the page, are both
// cases of "nobody available to confirm". Allowing them would reduce the whole mechanism to
// decoration that only works while someone happens to be watching.
func (b *Broker) Ask(ctx context.Context, sessionID, command string) (Decision, error) {
	req := &Request{
		ID:        uuid.NewString(),
		SessionID: sessionID,
		Command:   command,
		CreatedAt: time.Now().UnixMilli(),
	}
	p := &pending{req: req, result: make(chan Decision, 1)}

	b.mu.Lock()
	listener, ok := b.listeners[sessionID]
	if !ok {
		b.mu.Unlock()
		return Decision{}, ErrNoReviewer
	}
	b.pendings[req.ID] = p
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.pendings, req.ID)
		b.mu.Unlock()
	}()

	select {
	case listener <- req:
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	case <-time.After(5 * time.Second):
		// 通道满说明前端积压了未处理的请求，继续等没有意义。
		// A full channel means the frontend has a backlog; waiting longer is pointless.
		return Decision{}, ErrNoReviewer
	}

	timer := time.NewTimer(waitTimeout)
	defer timer.Stop()

	select {
	case d := <-p.result:
		return d, nil
	case <-timer.C:
		return Decision{
			Approved: false,
			Reason:   "等待确认超时，命令未执行 / the confirmation timed out and the command was not run",
		}, nil
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
}

// Resolve 提交一个决定。返回 false 表示该请求已不存在（超时或已被处理）。
// Resolve submits a verdict. False means the request no longer exists (timed out or already
// handled).
func (b *Broker) Resolve(requestID string, d Decision) bool {
	b.mu.Lock()
	p, ok := b.pendings[requestID]
	if ok {
		delete(b.pendings, requestID)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	p.result <- d
	return true
}
