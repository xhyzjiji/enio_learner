// 独立运行 Supervisor 版多智能体：go run ./cmd/supervisor
// Run the supervisor-based multi-agent demo standalone: go run ./cmd/supervisor
package main

import (
	"private/agent_basedon_eino/demo/multiagent"
	"private/agent_basedon_eino/internal/democli"
)

func main() {
	democli.Main("supervisor", multiagent.RunSupervisor)
}
