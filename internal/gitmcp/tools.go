package gitmcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// register 把六个只读工具挂到 MCP server 上。
// register mounts the six read-only tools onto the MCP server.
//
// 工具的 Description 是模型唯一的选择依据，所以每条都写清楚“什么时候用它”而不只是“它做什么”。
// A tool's Description is the model's only basis for choosing it, so each one states *when* to use it,
// not merely what it does.
func (s *Server) register(m *server.MCPServer) {
	m.AddTool(mcp.NewTool("git_log",
		mcp.WithDescription("查看提交历史。用于了解一段时间内做了哪些改动、谁提交的。返回每行一条：短哈希、作者、日期、标题。"),
		mcp.WithNumber("limit", mcp.Description("返回多少条提交，默认 20，最大 200")),
		mcp.WithString("since", mcp.Description("只看这个时间之后的提交，git 时间语法，例如 '2 weeks ago'、'2026-08-01'")),
		mcp.WithString("range", mcp.Description("提交区间，例如 'v1.0..HEAD' 或 'abc123..def456'。给了 range 就会忽略 since")),
		mcp.WithString("path", mcp.Description("只看某个文件或目录的提交历史，相对仓库根的路径")),
	), s.handleGitLog)

	m.AddTool(mcp.NewTool("git_diff",
		mcp.WithDescription("查看两个提交之间的差异。默认只返回文件级别的统计（哪些文件改了多少行），需要具体代码时把 stat_only 设为 false。"),
		mcp.WithString("from", mcp.Description("起始提交/分支/标签"), mcp.Required()),
		mcp.WithString("to", mcp.Description("结束提交，默认 HEAD")),
		mcp.WithBoolean("stat_only", mcp.Description("true 只返回文件改动统计，false 返回完整 diff。默认 true，完整 diff 很长，确认需要再设 false")),
		mcp.WithString("path", mcp.Description("只看某个文件或目录的差异")),
	), s.handleGitDiff)

	m.AddTool(mcp.NewTool("git_blame",
		mcp.WithDescription("查看某个文件指定行区间由谁在什么时候写的。用于追溯某段代码的来源和责任人。"),
		mcp.WithString("path", mcp.Description("文件路径，相对仓库根"), mcp.Required()),
		mcp.WithNumber("start_line", mcp.Description("起始行号，从 1 开始")),
		mcp.WithNumber("end_line", mcp.Description("结束行号")),
	), s.handleGitBlame)

	m.AddTool(mcp.NewTool("search_code",
		mcp.WithDescription("在仓库里按关键词/正则搜索代码，返回匹配的文件与行号。用于定位某个功能、符号、配置在哪里实现。只搜索被 Git 跟踪的文件。"),
		mcp.WithString("pattern", mcp.Description("搜索的关键词或正则表达式"), mcp.Required()),
		mcp.WithString("glob", mcp.Description("限定文件范围，例如 '*.go'、'demo/*'")),
		mcp.WithNumber("limit", mcp.Description("最多返回多少行匹配，默认 50")),
	), s.handleSearchCode)

	m.AddTool(mcp.NewTool("read_file",
		mcp.WithDescription("读取仓库内某个文件的内容，可指定行区间。通常在 search_code 定位到位置之后，用它看具体上下文。"),
		mcp.WithString("path", mcp.Description("文件路径，相对仓库根"), mcp.Required()),
		mcp.WithNumber("start_line", mcp.Description("起始行号，从 1 开始，默认 1")),
		mcp.WithNumber("end_line", mcp.Description("结束行号，默认到文件末尾")),
	), s.handleReadFile)

	m.AddTool(mcp.NewTool("list_files",
		mcp.WithDescription("列出仓库里被 Git 跟踪的文件。用于快速了解工程结构。"),
		mcp.WithString("glob", mcp.Description("过滤模式，例如 '*.go'、'demo/**'。留空返回全部")),
	), s.handleListFiles)
}

func (s *Server) handleGitLog(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := clampInt(req.GetInt("limit", 20), 1, 200)

	args := []string{"log", "--no-merges", "--date=short", "--pretty=format:%h  %an  %ad  %s", "-n", strconv.Itoa(limit)}

	if rng := req.GetString("range", ""); rng != "" {
		args = append(args, rng)
	} else if since := req.GetString("since", ""); since != "" {
		args = append(args, "--since="+since)
	}

	// -- 之后的内容一律被 git 当成路径，避免和分支重名时产生歧义。
	// Anything after -- is treated as a path by git, avoiding ambiguity with same-named branches.
	if path := req.GetString("path", ""); path != "" {
		rel, err := s.safeRelPath(path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		args = append(args, "--", rel)
	}

	return s.gitResult(ctx, args, "没有符合条件的提交")
}

func (s *Server) handleGitDiff(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	from, err := req.RequireString("from")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	to := req.GetString("to", "HEAD")

	args := []string{"diff"}
	if req.GetBool("stat_only", true) {
		args = append(args, "--stat")
	}
	args = append(args, from+".."+to)

	if path := req.GetString("path", ""); path != "" {
		rel, err := s.safeRelPath(path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		args = append(args, "--", rel)
	}

	return s.gitResult(ctx, args, "两个提交之间没有差异")
}

func (s *Server) handleGitBlame(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	rel, err := s.safeRelPath(path)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	args := []string{"blame", "--date=short"}
	if start := req.GetInt("start_line", 0); start > 0 {
		end := req.GetInt("end_line", start)
		if end < start {
			end = start
		}
		args = append(args, "-L", fmt.Sprintf("%d,%d", start, end))
	}
	args = append(args, "--", rel)

	return s.gitResult(ctx, args, "该文件没有 blame 信息")
}

func (s *Server) handleSearchCode(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pattern, err := req.RequireString("pattern")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	limit := clampInt(req.GetInt("limit", 50), 1, 200)

	// git grep 只搜索被跟踪的文件，天然跳过 .git、构建产物和被忽略的目录。
	// git grep only searches tracked files, naturally skipping .git, build output and ignored paths.
	args := []string{"grep", "-n", "-I", "--max-count", strconv.Itoa(limit), "-e", pattern}
	if glob := req.GetString("glob", ""); glob != "" {
		args = append(args, "--", glob)
	}

	out, err := s.runGit(ctx, args...)
	if err != nil {
		// git grep 没匹配到时退出码是 1，对使用者来说这不是错误。
		// git grep exits 1 on no match, which isn't an error from the caller's point of view.
		if strings.Contains(err.Error(), "exit status 1") {
			return mcp.NewToolResultText("没有匹配到任何内容"), nil
		}
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(nonEmpty(out, "没有匹配到任何内容")), nil
}

func (s *Server) handleReadFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	rel, err := s.safeRelPath(path)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// 走 git show 而不是 os.ReadFile：未被跟踪的文件读不到，安全边界和其他工具保持一致。
	// Uses git show rather than os.ReadFile: untracked files stay unreadable, keeping the
	// security boundary identical across tools.
	out, err := s.runGit(ctx, "show", "HEAD:"+rel)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	start := req.GetInt("start_line", 0)
	end := req.GetInt("end_line", 0)
	if start > 0 || end > 0 {
		out = sliceLines(out, start, end)
	}
	return mcp.NewToolResultText(nonEmpty(out, "文件为空或指定行区间没有内容")), nil
}

func (s *Server) handleListFiles(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := []string{"ls-files"}
	if glob := req.GetString("glob", ""); glob != "" {
		args = append(args, "--", glob)
	}
	return s.gitResult(ctx, args, "没有匹配的文件")
}

// gitResult 收敛“执行 git、失败转成工具错误、空输出给个说明”的重复逻辑。
// gitResult folds up the repeated "run git, turn failure into a tool error, explain empty output" logic.
func (s *Server) gitResult(ctx context.Context, args []string, emptyHint string) (*mcp.CallToolResult, error) {
	out, err := s.runGit(ctx, args...)
	if err != nil {
		// 返回 ToolResultError 而不是 Go error：让模型看到失败原因并自行改参数重试，
		// 而不是把整条 Agent 链路打断。
		// Returns ToolResultError rather than a Go error so the model can see why it failed and retry
		// with different arguments, instead of tearing down the whole agent run.
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(nonEmpty(out, emptyHint)), nil
}

func sliceLines(s string, start, end int) string {
	lines := strings.Split(s, "\n")
	if start < 1 {
		start = 1
	}
	if end < 1 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return ""
	}

	numbered := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		numbered = append(numbered, fmt.Sprintf("%d\t%s", i, lines[i-1]))
	}
	return strings.Join(numbered, "\n")
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
