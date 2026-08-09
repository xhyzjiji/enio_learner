// 独立运行带工具的旅行助手：go run ./cmd/travel
// Run the tool-using travel assistant standalone: go run ./cmd/travel
package main

import (
	"private/agent_basedon_eino/demo/travelagent"
	"private/agent_basedon_eino/internal/democli"
)

func main() {
	democli.Main("travel", travelagent.Run)
}
