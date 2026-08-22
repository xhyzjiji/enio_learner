// 独立运行 MCP 三级工具链 demo：go run ./cmd/mcp
// 前置条件：先启动 ./deploy/mcp-mysql/run-server.sh，并设置 MCP_MYSQL_SECRET。
//
// Run the MCP three-step tool chain demo standalone: go run ./cmd/mcp
// Prerequisite: start ./deploy/mcp-mysql/run-server.sh and set MCP_MYSQL_SECRET.
package main

import (
	"private/agent_basedon_eino/demo/mcpagent"
	"private/agent_basedon_eino/internal/democli"
)

func main() {
	democli.Main("mcp", mcpagent.Run)
}
