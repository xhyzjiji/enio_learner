// 独立运行单轮问答机器人：go run ./cmd/chatbot
// Run the single-turn chatbot standalone: go run ./cmd/chatbot
package main

import (
	"private/agent_basedon_eino/demo/chatbot"
	"private/agent_basedon_eino/internal/democli"
)

func main() {
	democli.Main("chatbot", chatbot.Run)
}
