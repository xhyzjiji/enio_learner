// Package agentio 负责消费 ADK 的事件流并打印到终端。
// Package agentio consumes an ADK event stream and prints it to the terminal.
//
// ADK 的交付物是事件流而不是最终状态，所以“怎么读事件”是所有 Agent demo 的公共部分。
// An ADK run delivers an event stream rather than a final state, so "how to read events" is shared by every agent demo.
package agentio

import (
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Print 消费事件流并输出模型回复。
// Print consumes the event stream and writes the model reply out.
//
// verbose 为真时额外打印工具调用与工具返回，用来观察 ReAct 循环的每一步。
// With verbose set, tool calls and tool results are printed too, exposing every step of the ReAct loop.
func Print(iter *adk.AsyncIterator[*adk.AgentEvent], verbose bool) error {
	for {
		event, ok := iter.Next()
		if !ok {
			return nil
		}
		if event.Err != nil {
			return fmt.Errorf("agent run: %w", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		if err := printMessage(event.Output.MessageOutput, verbose); err != nil {
			return err
		}
	}
}

func printMessage(out *adk.MessageVariant, verbose bool) error {
	// 流式模式下工具调用信息要等流收完才完整，这里只做增量输出。
	// In streaming mode tool-call details are only complete once the stream ends, so just echo increments.
	if out.IsStreaming {
		return printStream(out.MessageStream)
	}

	switch out.Role {
	case schema.Assistant:
		if out.Message.Content != "" {
			fmt.Print(out.Message.Content)
		}
		if verbose {
			for _, tc := range out.Message.ToolCalls {
				fmt.Printf("\n[tool call] %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
			}
		}
	case schema.Tool:
		if verbose {
			fmt.Printf("[tool result] %s -> %s\n", out.ToolName, out.Message.Content)
		}
	}
	return nil
}

func printStream(stream *schema.StreamReader[*schema.Message]) error {
	defer stream.Close()
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv stream: %w", err)
		}
		fmt.Print(chunk.Content)
	}
}
