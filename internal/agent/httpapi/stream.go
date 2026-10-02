package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
)

// SSE 事件类型。
// SSE event types.
const (
	// EventMessageDelta 是助手回复的增量片段。
	// EventMessageDelta carries an incremental chunk of the assistant reply.
	EventMessageDelta = "message_delta"
	// EventToolCall 表示模型决定调用某个工具。
	// EventToolCall signals that the model decided to call a tool.
	EventToolCall = "tool_call"
	// EventToolResult 是工具的执行结果。
	// EventToolResult carries a tool execution result.
	EventToolResult = "tool_result"
	// EventCompression 表示上下文被压缩。
	// EventCompression signals that the context was compressed.
	EventCompression = "compression"
	// EventError 是可读的错误说明。
	// EventError carries a human-readable error description.
	EventError = "error"
	// EventDone 标记一轮对话结束。
	// EventDone marks the end of one conversation turn.
	EventDone = "done"
)

// ToolCallPayload 描述一次工具调用。
// ToolCallPayload describes one tool call.
type ToolCallPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolResultPayload 描述一次工具返回。
// ToolResultPayload describes one tool result.
type ToolResultPayload struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// DeltaPayload 是回复增量。
// DeltaPayload is a reply increment.
type DeltaPayload struct {
	Content   string `json:"content"`
	Reasoning string `json:"reasoning,omitempty"`
}

// CompressionPayload 说明压缩发生在哪一步。
// CompressionPayload states which compression step occurred.
type CompressionPayload struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

// DonePayload 是收尾事件。
// DonePayload is the closing event.
type DonePayload struct {
	SessionID   string `json:"session_id"`
	Interrupted bool   `json:"interrupted"`
}

// SSEWriter 把事件写成 text/event-stream 并及时 flush。
//
// 每写一条就 flush 是必需的：不 flush 的话内容会积在 net/http 的缓冲里，
// 用户看到的是"卡很久然后一次性刷出全部文字"，流式输出的意义就没了。
//
// SSEWriter writes events as text/event-stream and flushes promptly.
//
// Flushing after every write is required: without it the content piles up in net/http's buffer
// and the user sees a long pause followed by the whole answer at once, which defeats the point
// of streaming.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter 初始化 SSE 响应头。
// NewSSEWriter initializes the SSE response headers.
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("response writer does not support flushing")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// 关闭 nginx 之类反向代理的缓冲，本地直连时无副作用。
	// Disables buffering in reverse proxies such as nginx; harmless on a direct local connection.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSEWriter{w: w, flusher: flusher}, nil
}

// Send 写出一个事件。
// Send emits one event.
func (s *SSEWriter) Send(event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", event, err)
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// SendError 写出错误事件。
// SendError emits an error event.
func (s *SSEWriter) SendError(err error) {
	_ = s.Send(EventError, map[string]string{"message": err.Error()})
}

// StreamResult 汇总一轮对话产生的完整消息，供调用方落库。
// StreamResult aggregates the complete messages produced by one turn, for the caller to persist.
type StreamResult struct {
	// Messages 是本轮新增的全部消息，顺序与产生顺序一致。
	// Messages holds every message added this turn, in production order.
	Messages []*schema.Message
	// Interrupted 表示本轮被中断。
	// Interrupted reports that the turn was interrupted.
	Interrupted bool
	// Compressed 表示本轮触发过上下文压缩。
	// Compressed reports that context compression fired during this turn.
	Compressed bool
}

// PumpEvents 消费 Agent 事件迭代器并转成 SSE，同时收集完整消息。
//
// 关于工具调用事件的补发：流式模式下 MessageStream 里的每个 chunk 只带有 ToolCalls
// 的一个分片（函数名和参数都是逐字拼出来的），必须等整条流收完再 concat，
// 才能拿到完整的工具名与参数。所以工具调用事件只能在流结束后补发，
// 不能在 chunk 循环里边收边发。
//
// PumpEvents consumes the agent event iterator, converts it to SSE and collects full messages.
//
// On re-emitting tool-call events: in streaming mode each chunk of MessageStream carries only a
// fragment of ToolCalls — function names and arguments arrive character by character — so the
// stream must be fully consumed and concatenated before the complete tool name and arguments
// are available. Tool-call events can therefore only be emitted after the stream ends, never
// from inside the chunk loop.
func PumpEvents(iter *adk.AsyncIterator[*adk.AgentEvent], sse *SSEWriter) (*StreamResult, error) {
	res := &StreamResult{}
	for {
		event, ok := iter.Next()
		if !ok {
			return res, nil
		}
		if event.Err != nil {
			if errors.Is(event.Err, io.EOF) {
				return res, nil
			}
			return res, event.Err
		}
		if err := handleAction(event.Action, sse, res); err != nil {
			return res, err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if err := handleMessage(event.Output.MessageOutput, sse, res); err != nil {
			return res, err
		}
	}
}

// handleAction 处理动作事件，目前只关心中断与压缩。
// handleAction processes action events; only interruption and compression matter today.
func handleAction(action *adk.AgentAction, sse *SSEWriter, res *StreamResult) error {
	if action == nil {
		return nil
	}
	if action.Interrupted != nil {
		res.Interrupted = true
	}
	ca, ok := action.CustomizedAction.(*summarization.CustomizedAction)
	if !ok {
		return nil
	}
	switch ca.Type {
	case summarization.ActionTypeBeforeSummarize:
		res.Compressed = true
		return sse.Send(EventCompression, CompressionPayload{
			Stage:   "before",
			Message: "对话较长，正在压缩历史上下文 / conversation is long, compressing history",
		})
	case summarization.ActionTypeAfterSummarize:
		return sse.Send(EventCompression, CompressionPayload{
			Stage:   "after",
			Message: "历史上下文已压缩 / history has been compressed",
		})
	}
	return nil
}

// handleMessage 把一个消息事件转成 SSE。
// handleMessage converts one message event into SSE.
func handleMessage(mv *adk.MessageVariant, sse *SSEWriter, res *StreamResult) error {
	switch mv.Role {
	case schema.Tool:
		msg, err := mv.GetMessage()
		if err != nil || msg == nil {
			return err
		}
		res.Messages = append(res.Messages, msg)
		return sse.Send(EventToolResult, ToolResultPayload{
			ID: msg.ToolCallID, Name: mv.ToolName, Content: msg.Content,
		})
	case schema.Assistant:
		return handleAssistant(mv, sse, res)
	default:
		return nil
	}
}

func handleAssistant(mv *adk.MessageVariant, sse *SSEWriter, res *StreamResult) error {
	if !mv.IsStreaming {
		if mv.Message == nil {
			return nil
		}
		if mv.Message.Content != "" {
			if err := sse.Send(EventMessageDelta, DeltaPayload{Content: mv.Message.Content}); err != nil {
				return err
			}
		}
		res.Messages = append(res.Messages, mv.Message)
		return emitToolCalls(mv.Message, sse)
	}

	// 流式：先逐片推给前端，再把整条流 concat 成完整消息。
	// Streaming: push each chunk to the frontend first, then concatenate the whole stream into
	// one complete message.
	stream := mv.MessageStream.Copy(2)
	defer func() {
		stream[0].Close()
		stream[1].Close()
	}()

	for {
		chunk, err := stream[0].Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if chunk == nil {
			continue
		}
		if chunk.Content == "" && chunk.ReasoningContent == "" {
			continue
		}
		if err := sse.Send(EventMessageDelta, DeltaPayload{
			Content: chunk.Content, Reasoning: chunk.ReasoningContent,
		}); err != nil {
			return err
		}
	}

	full, err := concatStream(stream[1])
	if err != nil {
		return err
	}
	if full == nil {
		return nil
	}
	res.Messages = append(res.Messages, full)
	return emitToolCalls(full, sse)
}

// concatStream 把一条消息流拼成完整消息。
// concatStream concatenates a message stream into one complete message.
func concatStream(sr *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	var chunks []*schema.Message
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	return schema.ConcatMessages(chunks)
}

func emitToolCalls(msg *schema.Message, sse *SSEWriter) error {
	for _, tc := range msg.ToolCalls {
		if err := sse.Send(EventToolCall, ToolCallPayload{
			ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
		}); err != nil {
			return err
		}
	}
	return nil
}
