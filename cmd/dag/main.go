// 独立运行水果结账流水线：go run ./cmd/dag
// Run the fruit checkout pipeline standalone: go run ./cmd/dag
package main

import (
	"private/agent_basedon_eino/demo/fruitdag"
	"private/agent_basedon_eino/internal/democli"
)

func main() {
	democli.Main("dag", fruitdag.Run)
}
