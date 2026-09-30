// Package gitmcp 是一个自研的 MCP server：把只读的 Git 仓库分析能力暴露成 MCP 工具。
// Package gitmcp is a self-hosted MCP server exposing read-only Git repository analysis as MCP tools.
//
// 和 deploy/mcp-mysql 里那个第三方 TypeScript server 的区别在于，这个是用 Go 写的、
// 和主工程同一个 module，go run ./cmd/gitmcp 就能起，方便对照“接入别人的 MCP”和“自己实现 MCP”两件事。
// Unlike the third-party TypeScript server in deploy/mcp-mysql, this one is written in Go inside the same
// module and starts with `go run ./cmd/gitmcp`, contrasting "consuming an MCP server" with "implementing one".
//
// 所有工具都通过 git 子命令实现，因此天然只能看到被 Git 跟踪的文件，
// 这既省掉了外部依赖，也顺手划出了一条安全边界。
// Every tool shells out to git, so it can only ever see tracked files — that removes external
// dependencies and draws a security boundary at the same time.
package gitmcp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "git-analysis-mcp"
	serverVersion = "1.0.0"

	// 单次 git 调用的超时与输出上限。仓库大起来 git log 可能很慢很长，
	// 而工具返回最终要进模型上下文，必须有硬上限。
	// Timeout and output cap for one git call. On a big repository git log can be slow and huge,
	// and the result ends up in the model's context, so a hard cap is mandatory.
	gitTimeout     = 20 * time.Second
	maxOutputBytes = 16 * 1024
)

// Config 描述 server 的运行参数。/ Config holds the server's runtime parameters.
type Config struct {
	// RepoRoot 是被分析的仓库根目录，所有工具都被限制在它内部。
	// RepoRoot is the repository under analysis; every tool is confined to it.
	RepoRoot string

	// Secret 是 Bearer token，为空表示不鉴权。
	// Secret is the bearer token; empty disables authentication.
	Secret string
}

// Server 持有仓库路径，是所有工具 handler 的接收者。
// Server owns the repository path and is the receiver of every tool handler.
type Server struct {
	repoRoot string
}

// New 构造 MCP server 并注册全部工具。
// New builds the MCP server and registers every tool.
func New(cfg Config) (*server.MCPServer, error) {
	root, err := resolveRepo(cfg.RepoRoot)
	if err != nil {
		return nil, err
	}

	s := &Server{repoRoot: root}
	mcpServer := server.NewMCPServer(serverName, serverVersion, server.WithToolCapabilities(false))
	s.register(mcpServer)
	return mcpServer, nil
}

// RepoRoot 返回解析后的仓库绝对路径，供启动日志展示。
// RepoRoot returns the resolved absolute repository path for the startup log.
func RepoRoot(cfg Config) (string, error) {
	return resolveRepo(cfg.RepoRoot)
}

// resolveRepo 把配置里的路径解析成仓库根目录，并确认它确实是个 Git 仓库。
// resolveRepo turns the configured path into a repository root and verifies it really is a Git repo.
func resolveRepo(path string) (string, error) {
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		path = cwd
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repo path %q: %w", path, err)
	}

	// 交给 git 自己判断，顺带把子目录归一化到仓库根。
	// Let git decide, which also normalises a subdirectory down to the repository root.
	cmd := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository: %w", abs, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Handler 把 MCP server 包成 http.Handler，并按需加上 Bearer 鉴权。
// Handler wraps the MCP server into an http.Handler, adding bearer authentication when configured.
func Handler(mcpServer *server.MCPServer, secret string) http.Handler {
	var h http.Handler = server.NewStreamableHTTPServer(mcpServer)
	if secret != "" {
		h = requireBearer(secret, h)
	}
	return h
}

func requireBearer(secret string, next http.Handler) http.Handler {
	expected := "Bearer " + secret
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"missing or invalid Authorization header"},"id":null}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// runGit 执行一次 git 子命令，带超时与输出截断。
// runGit executes one git subcommand with a timeout and output truncation.
//
// 注意这里用 exec.CommandContext 传参数数组而不是拼 shell 字符串，
// 所以模型传进来的参数不会被当成 shell 语法执行。
// Note the arguments go through exec.CommandContext as a slice rather than a shell string,
// so whatever the model passes in can never be interpreted as shell syntax.
func (s *Server) runGit(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	full := append([]string{"-C", s.repoRoot}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return truncate(stdout.String()), nil
}

func truncate(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	return s[:maxOutputBytes] + fmt.Sprintf("\n...[输出被截断，原始长度 %d 字节 / truncated, original length %d bytes]", len(s), len(s))
}

// safeRelPath 校验模型传来的路径确实落在仓库内部，返回相对仓库根的路径。
// safeRelPath verifies a model-supplied path stays inside the repository and returns it relative to the root.
//
// 模型完全可能传 ../../etc/passwd，这层检查不能省。
// The model can absolutely pass ../../etc/passwd, so this check is not optional.
func (s *Server) safeRelPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}

	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.repoRoot, candidate)
	}

	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}

	rel, err := filepath.Rel(s.repoRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the repository root", path)
	}
	return rel, nil
}
