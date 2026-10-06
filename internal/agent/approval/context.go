package approval

import "context"

type sessionKey struct{}

// WithSession 把会话 ID 放进上下文。
//
// 工具是在 agent 图内部被调用的，拿不到 HTTP 请求，而确认请求必须推给**发起这一轮
// 对话的那个页面**，否则你会在 A 会话里收到 B 会话的命令确认框。上下文是唯一能把
// 这个信息带穿整条调用链的通道。
//
// WithSession puts the conversation ID into the context.
//
// Tools are invoked inside the agent graph and have no access to the HTTP request, yet a
// confirmation must reach THE PAGE THAT STARTED THIS TURN — otherwise you would get a
// confirmation dialog for conversation B while looking at conversation A. The context is the
// only channel that carries this across the whole call chain.
func WithSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionKey{}, sessionID)
}

// SessionFrom 取出会话 ID。
// SessionFrom extracts the conversation ID.
func SessionFrom(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}
