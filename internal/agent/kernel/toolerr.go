package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// recoverableToolErrors 捕获那些「模型把参数填错了」的失败，把它们变成普通的工具结果。
//
// 默认行为是工具一返回 error，整轮对话就以 NodeRunError 中断。这对基础设施故障
// 是对的，但对参数错误是灾难性的——模型写了个越界路径，用户看到的是一串英文堆栈，
// 而模型根本没有机会改正。它甚至不知道自己错在哪，因为那条错误信息从来没回到它面前。
//
// 转成工具结果后，模型会在下一次迭代里读到失败原因，自己换个路径重试，整个过程
// 对用户是透明的。
//
// 注意这不会放松任何安全边界：越界访问依然被拒绝，被改变的只是「拒绝之后发生什么」。
//
// recoverableToolErrors catches failures where the model simply filled in a bad argument and
// turns them into ordinary tool results.
//
// By default a tool returning an error aborts the entire turn with a NodeRunError. That is right
// for infrastructure faults but disastrous for argument mistakes: the model writes an
// out-of-bounds path, the user is shown an English stack trace, and the model never gets a chance
// to correct itself. It does not even learn what went wrong, because the error never reaches it.
//
// Converted into a tool result, the model reads the reason on its next iteration, retries with a
// different path, and the whole exchange stays invisible to the user.
//
// Note that this relaxes no security boundary: out-of-bounds access is still refused. What
// changes is only what happens after the refusal.
func recoverableToolErrors(logger *slog.Logger) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, in *compose.ToolInput) (*compose.ToolOutput, error) {
				out, err := next(ctx, in)
				if err == nil {
					return out, nil
				}
				hint, ok := recoverHint(in.Name, err)
				if !ok {
					return nil, err
				}
				logger.Info("工具调用参数有误，已交还模型重试 / tool argument rejected, handed back to the model",
					"tool", in.Name, "err", err)
				return &compose.ToolOutput{Result: hint}, nil
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, in *compose.ToolInput) (*compose.StreamToolOutput, error) {
				out, err := next(ctx, in)
				if err == nil {
					return out, nil
				}
				hint, ok := recoverHint(in.Name, err)
				if !ok {
					return nil, err
				}
				logger.Info("工具调用参数有误，已交还模型重试 / tool argument rejected, handed back to the model",
					"tool", in.Name, "err", err)
				return &compose.StreamToolOutput{
					Result: schema.StreamReaderFromArray([]string{hint}),
				}, nil
			}
		},
	}
}

// recoverableMarkers 是可恢复失败的识别特征。
//
// 按错误文本匹配而非错误类型，是因为这些错误来自 eino 内置中间件和各路 MCP server，
// 它们既不导出错误类型也不用 %w 包装，没有别的抓手。匹配范围刻意收得很窄：
// 宁可漏掉一个该恢复的，也不要把真正的基础设施故障伪装成工具结果——那会让模型
// 对着一个根本连不上的服务反复重试。
//
// recoverableMarkers identify recoverable failures.
//
// Matching on error text rather than type is forced: these errors come from eino's built-in
// middlewares and from assorted MCP servers, none of which export error types or wrap with %w.
// The patterns are kept deliberately narrow — better to miss a recoverable case than to disguise
// a genuine infrastructure fault as a tool result, which would have the model retry endlessly
// against a service that is simply unreachable.
var recoverableMarkers = []string{
	"escapes the workspace root",
	"path escapes",
	"no such file or directory",
	"is a directory",
	"file already exists",
	"invalid character", // 参数 JSON 格式错 / malformed argument JSON
	"cannot unmarshal",  // 参数类型对不上 / argument type mismatch
	"required",          // 缺必填参数 / missing required argument
}

func recoverHint(toolName string, err error) (string, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	for _, marker := range recoverableMarkers {
		if strings.Contains(lower, marker) {
			return fmt.Sprintf(
				"工具 %s 调用失败：%s\n请检查参数后重试，不要向用户报告这条内部错误。/ "+
					"Tool %s failed: %s\nCheck the arguments and retry; do not report this "+
					"internal error to the user.",
				toolName, msg, toolName, msg), true
		}
	}
	return "", false
}

// RecoverableToolErrorsForTest 暴露中间件供验证使用。
// RecoverableToolErrorsForTest exposes the middleware for verification.
func RecoverableToolErrorsForTest() compose.ToolMiddleware {
	return recoverableToolErrors(slog.Default())
}
