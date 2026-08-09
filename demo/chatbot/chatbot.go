// Package chatbot 对应 LangGraph 的 chatbot_demo.py：一个只有系统提示词、没有工具的单轮问答机器人。
// Package chatbot mirrors chatbot_demo.py: a single-turn Q&A bot with a system prompt and no tools.
//
// LangGraph 版需要显式建图：StateGraph → add_node("chatbot") → add_edge(chatbot, END) → compile。
// Eino 这边不需要图，因为“系统提示词 + 一次模型调用”本身就是 ChatModelAgent 不配工具时的形态。
// The LangGraph version builds an explicit graph; Eino needs none, because a ChatModelAgent without
// tools degrades to exactly "system prompt + one model call".
package chatbot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cloudwego/eino/adk"

	"private/agent_basedon_eino/internal/agentio"
	"private/agent_basedon_eino/internal/democli"
	"private/agent_basedon_eino/internal/llm"
)

func init() {
	democli.Register(democli.Demo{
		Name: "chatbot",
		Desc: "无工具的单轮问答 / single-turn Q&A without tools (chatbot_demo.py)",
		Run:  Run,
	})
}

// generalPrompt 对应 Python 的 general_prompt。/ generalPrompt mirrors general_prompt in Python.
const generalPrompt = `You are a Client Stability Assistant.
你负责解答客户端稳定性相关的问题，包括崩溃、卡死、ANR、内存与耗电等方向。
回答要具体、可执行，必要时给出排查步骤。`

// Run 启动一次问答，对应 Python 的 on_demo2()。
// Run performs one Q&A round, mirroring on_demo2() in Python.
func Run(ctx context.Context) error {
	chatModel, err := llm.NewChatModel(ctx)
	if err != nil {
		return fmt.Errorf("create chat model: %w", err)
	}

	// 不配置 ToolsConfig，ChatModelAgent 就退化成单次 ChatModel 调用。
	// Without ToolsConfig, ChatModelAgent degrades into a single ChatModel invocation.
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "client_stability_assistant",
		Description: "解答客户端稳定性问题 / answers client stability questions",
		Instruction: generalPrompt,
		Model:       chatModel,
	})
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	// Runner 是 Agent 的统一执行入口，事件流、中断恢复都由它提供。
	// Runner is the single execution entry for agents, providing the event stream and interrupt recovery.
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent: agent,
		// 置为 false 就是 Python 版 graph.invoke() 那种一次性返回。
		// Setting this to false gives the one-shot behaviour of graph.invoke() in the Python version.
		EnableStreaming: true,
	})

	query, err := readQuery()
	if err != nil {
		return err
	}

	fmt.Println("=================================================")
	if err := agentio.Print(runner.Query(ctx, query), false); err != nil {
		return err
	}
	fmt.Println("\n=================================================")
	return nil
}

// readQuery 从标准输入读取用户提问。/ readQuery reads the user question from stdin.
func readQuery() (string, error) {
	fmt.Print("请输入提问内容：")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read query: %w", err)
	}
	query := strings.TrimSpace(line)
	if query == "" {
		return "", errors.New("query is empty")
	}
	return query, nil
}
