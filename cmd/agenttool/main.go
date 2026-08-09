// 独立运行 AgentAsTool 版多智能体：go run ./cmd/agenttool
// Run the AgentAsTool-based multi-agent demo standalone: go run ./cmd/agenttool
package main

import (
	"private/agent_basedon_eino/demo/multiagent"
	"private/agent_basedon_eino/internal/democli"
)

func main() {
	democli.Main("agenttool", multiagent.RunAgentTool)
}
