// 独立运行 SequentialAgent 版多智能体：go run ./cmd/sequential
// Run the SequentialAgent multi-agent demo standalone: go run ./cmd/sequential
package main

import (
	"private/agent_basedon_eino/demo/multiagent"
	"private/agent_basedon_eino/internal/democli"
)

func main() {
	democli.Main("sequential", multiagent.RunSequential)
}
