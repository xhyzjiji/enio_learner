// mcpprobe 是 MCP server 的调试探针，不涉及任何 LLM。
// mcpprobe is a debugging probe for the MCP server; no LLM involved.
//
// 用途是把“MCP 通不通”和“Agent 好不好用”这两件事分开排查。
// Its purpose is to separate "is MCP reachable" from "is the agent behaving".
//
// 工程里有两个 MCP server，探针对两者通用，靠 -endpoint 和 -secret-env 指定。
// The repo hosts two MCP servers; this probe works against both, selected via -endpoint and -secret-env.
//
// 用法 / Usage:
//
//	go run ./cmd/mcpprobe                         # 默认探 mcp-mysql
//	go run ./cmd/mcpprobe -call mysql_query -args '{"sql":"SHOW TABLES"}'
//	go run ./cmd/mcpprobe -endpoint http://127.0.0.1:8080/mcp -secret-env MCP_GIT_SECRET
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/cloudwego/eino/components/tool"

	"private/agent_basedon_eino/internal/mcpclient"
)

func main() {
	var (
		endpoint  = flag.String("endpoint", "http://127.0.0.1:3000/mcp", "MCP 端点")
		secretEnv = flag.String("secret-env", "MCP_MYSQL_SECRET", "存放 Bearer token 的环境变量名")
		callName  = flag.String("call", "", "要调用的工具名，留空则只列出工具")
		callArgs  = flag.String("args", "{}", "工具入参，JSON 字符串")
		verbose   = flag.Bool("v", false, "打印每个工具完整的入参 schema")
		maxBytes  = flag.Int("max", 0, "截断工具返回的字节数，0 表示不截断")
	)
	flag.Parse()

	if err := run(context.Background(), probeConfig{
		endpoint:  *endpoint,
		secret:    os.Getenv(*secretEnv),
		callName:  *callName,
		callArgs:  *callArgs,
		verbose:   *verbose,
		maxBytes:  *maxBytes,
		secretVar: *secretEnv,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "mcpprobe: %v\n", err)
		os.Exit(1)
	}
}

// probeConfig 收敛命令行参数，避免 run 的签名越加越长。
// probeConfig groups the flags so run's signature doesn't keep growing.
type probeConfig struct {
	endpoint  string
	secret    string
	secretVar string
	callName  string
	callArgs  string
	verbose   bool
	maxBytes  int
}

func run(ctx context.Context, pc probeConfig) error {
	if pc.secret == "" {
		return fmt.Errorf("环境变量 %s 未设置 / environment variable %s is not set", pc.secretVar, pc.secretVar)
	}

	cfg := mcpclient.Config{Endpoint: pc.endpoint, Secret: pc.secret, MaxResultBytes: pc.maxBytes}

	cli, err := mcpclient.Dial(ctx, cfg)
	if err != nil {
		return err
	}
	defer cli.Close()
	fmt.Println("✓ 握手成功 / handshake ok")

	tools, err := cli.Tools(ctx, cfg)
	if err != nil {
		return err
	}

	fmt.Printf("✓ 拉到 %d 个工具 / %d tools returned\n\n", len(tools), len(tools))
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			return fmt.Errorf("read tool info: %w", err)
		}
		fmt.Printf("- %s\n  %s\n", info.Name, info.Desc)
		if pc.verbose {
			schema, err := info.ParamsOneOf.ToJSONSchema()
			if err == nil {
				pretty, _ := json.MarshalIndent(schema, "  ", "  ")
				fmt.Printf("  schema: %s\n", pretty)
			}
		}
		fmt.Println()
	}

	if pc.callName == "" {
		return nil
	}

	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			return err
		}
		if info.Name != pc.callName {
			continue
		}

		invokable, ok := t.(tool.InvokableTool)
		if !ok {
			return fmt.Errorf("tool %s is not invokable", pc.callName)
		}

		fmt.Printf("→ 调用 %s(%s)\n\n", pc.callName, pc.callArgs)
		result, err := invokable.InvokableRun(ctx, pc.callArgs)
		if err != nil {
			return fmt.Errorf("call %s: %w", pc.callName, err)
		}
		fmt.Println(result)
		return nil
	}

	return fmt.Errorf("tool %q not found on server", pc.callName)
}
