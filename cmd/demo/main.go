// 统一分发器：go run ./cmd/demo <name>，方便一个入口跑遍所有 demo。
// Dispatcher: go run ./cmd/demo <name>, a single entrypoint covering every demo.
//
// 每个 demo 同时也有自己的独立入口，见 cmd/<name>/main.go。
// Each demo also has its own standalone entrypoint, see cmd/<name>/main.go.
package main

import (
	"fmt"
	"os"

	// 空导入触发各 demo 的 init 注册。/ Blank imports trigger each demo's init registration.
	_ "private/agent_basedon_eino/demo/chatbot"
	_ "private/agent_basedon_eino/demo/fruitdag"
	_ "private/agent_basedon_eino/demo/mcpagent"
	_ "private/agent_basedon_eino/demo/multiagent"
	_ "private/agent_basedon_eino/demo/skillagent"
	_ "private/agent_basedon_eino/demo/travelagent"

	"private/agent_basedon_eino/internal/democli"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	name := os.Args[1]
	demo, ok := democli.Lookup(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown demo: %s\n\n", name)
		usage()
		os.Exit(1)
	}

	democli.Main(demo.Name, demo.Run)
}

// usage 按能力分类列出 demo，组的先后就是建议的学习顺序。
// usage lists demos grouped by capability; group order is the suggested learning path.
func usage() {
	width := 0
	for _, d := range democli.All() {
		if len(d.Name) > width {
			width = len(d.Name)
		}
	}

	fmt.Fprintln(os.Stderr, "usage: go run ./cmd/demo <name>")
	fmt.Fprintln(os.Stderr, "\n按能力分类，由浅入深 / grouped by capability, shallow to deep:")

	for _, g := range democli.Grouped() {
		fmt.Fprintf(os.Stderr, "\n  %s\n", g.Category)
		for _, d := range g.Demos {
			fmt.Fprintf(os.Stderr, "    %-*s  %s\n", width, d.Name, d.Desc)
			fmt.Fprintf(os.Stderr, "    %-*s  前置 / needs: %s\n", width, "", d.Needs)
		}
	}

	fmt.Fprintln(os.Stderr, "\n每个 demo 也可以独立运行 / each demo can also run standalone:")
	fmt.Fprintln(os.Stderr, "  go run ./cmd/<name>")
	fmt.Fprintln(os.Stderr, "\n完整说明见 README.md / see README.md for the full guide")
}
