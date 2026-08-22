// mcpprobe 是 MCP server 的调试探针，不涉及任何 LLM。
// mcpprobe is a debugging probe for the MCP server; no LLM involved.
//
// 用途是把“MCP 通不通”和“Agent 好不好用”这两件事分开排查。
// Its purpose is to separate "is MCP reachable" from "is the agent behaving".
//
// 用法 / Usage:
//
//	go run ./cmd/mcpprobe                         # 握手 + 列出全部工具
//	go run ./cmd/mcpprobe -call mysql_query -args '{"sql":"SHOW TABLES"}'
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
		endpoint = flag.String("endpoint", "", "MCP 端点，默认取 MCP_MYSQL_ENDPOINT 或 http://127.0.0.1:3000/mcp")
		callName = flag.String("call", "", "要调用的工具名，留空则只列出工具")
		callArgs = flag.String("args", "{}", "工具入参，JSON 字符串")
		verbose  = flag.Bool("v", false, "打印每个工具完整的入参 schema")
		maxBytes = flag.Int("max", 0, "截断工具返回的字节数，0 表示不截断")
	)
	flag.Parse()

	if err := run(context.Background(), *endpoint, *callName, *callArgs, *verbose, *maxBytes); err != nil {
		fmt.Fprintf(os.Stderr, "mcpprobe: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, endpoint, callName, callArgs string, verbose bool, maxBytes int) error {
	cfg := mcpclient.Config{Endpoint: endpoint, MaxResultBytes: maxBytes}

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
		if verbose {
			schema, err := info.ParamsOneOf.ToJSONSchema()
			if err == nil {
				pretty, _ := json.MarshalIndent(schema, "  ", "  ")
				fmt.Printf("  schema: %s\n", pretty)
			}
		}
		fmt.Println()
	}

	if callName == "" {
		return nil
	}

	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			return err
		}
		if info.Name != callName {
			continue
		}

		invokable, ok := t.(tool.InvokableTool)
		if !ok {
			return fmt.Errorf("tool %s is not invokable", callName)
		}

		fmt.Printf("→ 调用 %s(%s)\n\n", callName, callArgs)
		result, err := invokable.InvokableRun(ctx, callArgs)
		if err != nil {
			return fmt.Errorf("call %s: %w", callName, err)
		}
		fmt.Println(result)
		return nil
	}

	return fmt.Errorf("tool %q not found on server", callName)
}
