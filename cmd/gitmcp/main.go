// gitmcp 启动自研的 Git 分析 MCP server：go run ./cmd/gitmcp
//
// 默认分析当前工程自己，也就是说 skillagent demo 里模型读到的提交、代码和作者都是真的，
// 结论可以直接用 git 命令核对。
//
// gitmcp starts the self-hosted Git analysis MCP server: go run ./cmd/gitmcp
// It analyses this very repository by default, so everything the skillagent demo reads —
// commits, code, authors — is real and can be verified with plain git commands.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"private/agent_basedon_eino/internal/gitmcp"
)

func main() {
	var (
		addr = flag.String("addr", envOr("MCP_GIT_ADDR", "127.0.0.1:8080"), "监听地址")
		repo = flag.String("repo", os.Getenv("MCP_GIT_REPO"), "被分析的仓库路径，默认当前目录")
	)
	flag.Parse()

	// 与 mcp-mysql 保持一致：密钥来自环境变量，不进命令行参数（会被 ps 看到）。
	// Consistent with mcp-mysql: the secret comes from the environment, never a flag (ps would show it).
	secret := os.Getenv("MCP_GIT_SECRET")

	cfg := gitmcp.Config{RepoRoot: *repo, Secret: secret}

	root, err := gitmcp.RepoRoot(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gitmcp: %v\n", err)
		os.Exit(1)
	}

	mcpServer, err := gitmcp.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gitmcp: %v\n", err)
		os.Exit(1)
	}

	auth := "已启用 Bearer 鉴权"
	if secret == "" {
		auth = "未鉴权（设置 MCP_GIT_SECRET 可开启）"
	}

	fmt.Printf("[gitmcp] 仓库: %s\n", root)
	fmt.Printf("[gitmcp] 监听: http://%s/mcp  (%s)\n", *addr, auth)
	fmt.Printf("[gitmcp] 退出: Ctrl-C\n\n")

	mux := http.NewServeMux()
	mux.Handle("/mcp", gitmcp.Handler(mcpServer, secret))

	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "gitmcp: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
