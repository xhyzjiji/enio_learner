// 独立运行技能驱动的 Git 仓库分析助手：go run ./cmd/skillagent
// 前置条件：另开一个终端跑 go run ./cmd/gitmcp，并设置 MCP_GIT_SECRET。
//
// Run the skill-driven Git repository analyst standalone: go run ./cmd/skillagent
// Prerequisite: run `go run ./cmd/gitmcp` in another terminal and set MCP_GIT_SECRET.
package main

import (
	"private/agent_basedon_eino/demo/skillagent"
	"private/agent_basedon_eino/internal/democli"
)

func main() {
	democli.Main("skillagent", skillagent.Run)
}
