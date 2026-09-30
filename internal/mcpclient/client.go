// Package mcpclient 负责连接 MCP server 并把它的工具转成 Eino Tool。
// Package mcpclient connects to an MCP server and converts its tools into Eino tools.
//
// 这一层只做连接与握手，拿到的 []tool.BaseTool 与本地 InferTool 造出来的工具完全同类，
// 可以直接混在同一个 ToolsConfig 里挂给 Agent。
// This layer only handles connection and handshake; the resulting []tool.BaseTool is the same
// kind of thing InferTool produces locally and can be mixed into one ToolsConfig.
package mcpclient

import (
	"context"
	"fmt"
	"time"

	mcpp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	clientName    = "eino-demo-client"
	clientVersion = "1.0.0"
	dialTimeout   = 30 * time.Second
)

// Config 描述一次 MCP 连接。/ Config describes one MCP connection.
//
// Endpoint 和 Secret 都要求调用方显式传入，本包不读任何环境变量。
// 工程里有多个 MCP server，一旦在这里兜底某个服务的环境变量，
// 另一个服务忘了传密钥时就会静默连到错误的配置上，排查起来很费劲。
// Both Endpoint and Secret must be supplied by the caller; this package reads no environment variables.
// The repo hosts several MCP servers, and defaulting to one service's env vars here would let another
// service silently pick up the wrong credentials — a nasty thing to debug.
type Config struct {
	// Endpoint 是 Streamable HTTP 端点。/ Endpoint is the Streamable HTTP endpoint.
	Endpoint string

	// Secret 是 Bearer token。/ Secret is the bearer token.
	Secret string

	// ToolNames 限定拉取哪些工具，留空表示全部。
	// ToolNames limits which tools to fetch; empty means all of them.
	ToolNames []string

	// MaxResultBytes 限制单次工具返回的长度，0 表示不限制。
	// MaxResultBytes caps the size of a single tool result; 0 means unlimited.
	MaxResultBytes int
}

// Client 持有底层 MCP 客户端，用完需要 Close。
// Client owns the underlying MCP client and must be closed when done.
type Client struct {
	cli *client.Client
}

// Dial 建立连接并完成 initialize 握手。
// Dial establishes the connection and completes the initialize handshake.
//
// mcp-server-mysql 的 remote 模式是无状态的（sessionIdGenerator: undefined），
// 所以这里不需要处理 session id。
// The remote mode of mcp-server-mysql is stateless (sessionIdGenerator: undefined),
// so there is no session id to deal with here.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("mcp endpoint is empty")
	}
	if cfg.Secret == "" {
		return nil, fmt.Errorf("mcp secret is empty")
	}
	endpoint := cfg.Endpoint

	cli, err := client.NewStreamableHttpClient(endpoint,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + cfg.Secret}),
		transport.WithHTTPTimeout(dialTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("create mcp client: %w", err)
	}

	if err := cli.Start(ctx); err != nil {
		return nil, fmt.Errorf("start mcp client: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: clientName, Version: clientVersion}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("initialize mcp session with %s: %w", endpoint, err)
	}

	return &Client{cli: cli}, nil
}

// Close 关闭底层连接。/ Close shuts the underlying connection down.
func (c *Client) Close() error {
	return c.cli.Close()
}

// Raw 暴露底层客户端，供探针直接调 ListTools / CallTool。
// Raw exposes the underlying client so the probe can call ListTools / CallTool directly.
func (c *Client) Raw() *client.Client {
	return c.cli
}

// Tools 拉取工具列表并转成 Eino Tool，对应 MCP 的 tools/list。
// Tools fetches the tool list and converts it into Eino tools, i.e. MCP's tools/list.
//
// 这是一次性的：拿到的 ToolInfo 会缓存在本地，后续 ReAct 循环不再回源。
// This is one-shot: the resulting ToolInfo is cached locally and later ReAct turns never re-fetch it.
func (c *Client) Tools(ctx context.Context, cfg Config) ([]tool.BaseTool, error) {
	conf := &mcpp.Config{
		Cli:          c.cli,
		ToolNameList: cfg.ToolNames,
	}

	// 外部 server 的返回长度不可控，超长结果会直接撑爆上下文。
	// Result size from an external server is unbounded and can blow up the context window.
	if cfg.MaxResultBytes > 0 {
		conf.ToolCallResultHandler = truncateResult(cfg.MaxResultBytes)
	}

	tools, err := mcpp.GetTools(ctx, conf)
	if err != nil {
		return nil, fmt.Errorf("list mcp tools: %w", err)
	}
	return tools, nil
}

func truncateResult(limit int) func(context.Context, string, *mcp.CallToolResult) (*mcp.CallToolResult, error) {
	return func(_ context.Context, name string, result *mcp.CallToolResult) (*mcp.CallToolResult, error) {
		if result == nil {
			return result, nil
		}
		for i, content := range result.Content {
			text, ok := content.(mcp.TextContent)
			if !ok || len(text.Text) <= limit {
				continue
			}
			text.Text = text.Text[:limit] + fmt.Sprintf("\n...[truncated, tool %s returned %d bytes]", name, len(text.Text))
			result.Content[i] = text
		}
		return result, nil
	}
}
