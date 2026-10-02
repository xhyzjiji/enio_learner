// Package tools 把各来源的工具合并成一份挂给 Agent 的工具集。
// Package tools merges tools from every source into the single set handed to the agent.
package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"private/agent_basedon_eino/internal/agent/config"
	"private/agent_basedon_eino/internal/agent/kernel"
	"private/agent_basedon_eino/internal/agent/tools/cli"
	"private/agent_basedon_eino/internal/agent/tools/mcp"
)

// Source 标识一个工具的来源，用于重名消歧与页面展示。
// Source identifies where a tool came from, for disambiguation and UI display.
type Source string

const (
	SourceMCP     Source = "mcp"
	SourceCLI     Source = "cli"
	SourceBuiltin Source = "builtin"
)

// Entry 是工具清单里的一项。
// Entry is one item of the tool inventory.
type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      Source `json:"source"`
	// Renamed 为真表示该工具因重名被加了来源前缀，页面需要标注出来，
	// 否则用户在 MCP server 上看到的工具名和对话里看到的对不上。
	// Renamed reports that a source prefix was added because of a name collision. The UI must
	// surface it, otherwise the tool name shown on the MCP server will not match the one seen
	// in the conversation.
	Renamed bool `json:"renamed"`
}

// Registry 按当前配置装配工具集。
//
// 它是一个"每轮现算"的装配器而不是缓存：配置在页面上随时可改，缓存意味着要处理
// 失效通知，而装配一次的成本主要在 MCP 连接上，那部分本来就在 Manager 里复用了。
//
// Registry assembles the tool set from the current configuration.
//
// It recomputes per turn rather than caching: configuration changes at any moment from the UI,
// and caching would mean handling invalidation, while the cost of one assembly sits almost
// entirely in the MCP connections, which Manager already reuses.
type Registry struct {
	mcp    *mcp.Manager
	cli    *cli.Store
	runner *cli.Runner
	logger *slog.Logger
}

// NewRegistry 构造装配器。
// NewRegistry builds the registry.
func NewRegistry(m *mcp.Manager, c *cli.Store, r *cli.Runner, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{mcp: m, cli: c, runner: r, logger: logger}
}

// Scope 限制可用工具范围，定时任务用它裁剪能力。
// Scope restricts the available tools; scheduled tasks use it to trim capabilities.
type Scope struct {
	// Allow 为非空时只保留其中列出的工具名。
	// When non-empty, only the listed tool names survive.
	Allow []string
}

func (s Scope) permits(name string) bool {
	if len(s.Allow) == 0 {
		return true
	}
	for _, n := range s.Allow {
		if n == name {
			return true
		}
	}
	return false
}

// Assemble 返回本轮可用的工具与对应清单。
//
// 重名处理：同名工具保留先到者原名，后到者加来源前缀。不能直接丢弃后到者——
// 用户在页面上配了它就期望它能用；也不能都加前缀——那样绝大多数不冲突的工具
// 都要被改名，模型在文档和提示里学到的工具名就全对不上了。
//
// Assemble returns the tools available this turn along with their inventory.
//
// Name collisions: the first tool keeps its original name and later ones get a source prefix.
// Dropping the later one is wrong because the user configured it expecting it to work, and
// prefixing everything is wrong too because it would rename the vast majority of
// non-conflicting tools, leaving the model's learned tool names matching nothing.
func (r *Registry) Assemble(ctx context.Context, rt config.Runtime, scope Scope) ([]tool.BaseTool, []Entry, error) {
	type candidate struct {
		t      tool.BaseTool
		source Source
	}
	var candidates []candidate

	if r.mcp != nil {
		mcpTools, err := r.mcp.Tools(ctx, rt.MaxToolResultBytes)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range mcpTools {
			candidates = append(candidates, candidate{t, SourceMCP})
		}
	}
	if r.cli != nil && r.runner != nil {
		defs, err := r.cli.ListTools(ctx)
		if err != nil {
			return nil, nil, err
		}
		cliTools, err := cli.BuildTools(ctx, defs, r.runner, rt.MaxToolResultBytes)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range cliTools {
			candidates = append(candidates, candidate{t, SourceCLI})
		}
	}

	seen := make(map[string]bool, len(candidates))
	var (
		out     []tool.BaseTool
		entries []Entry
	)
	for _, c := range candidates {
		info, err := c.t.Info(ctx)
		if err != nil {
			r.logger.Warn("skip tool with unreadable info", "source", c.source, "err", err)
			continue
		}
		name, renamed := info.Name, false
		if seen[name] {
			name = string(c.source) + "_" + info.Name
			renamed = true
			if seen[name] {
				r.logger.Warn("skip tool whose prefixed name also collides", "name", name)
				continue
			}
		}
		if !scope.permits(info.Name) && !scope.permits(name) {
			continue
		}
		seen[name] = true
		t := c.t
		if renamed {
			t = renameTool(t, name)
		}
		out = append(out, t)
		entries = append(entries, Entry{
			Name: name, Description: info.Desc, Source: c.source, Renamed: renamed,
		})
	}
	return out, entries, nil
}

// Augmenter 返回一个把工具集写进快照的 kernel.Augmenter。
// Augmenter returns a kernel.Augmenter that writes the tool set into the snapshot.
func (r *Registry) Augmenter(scope Scope) kernel.Augmenter {
	return &toolAugmenter{reg: r, scope: scope}
}

type toolAugmenter struct {
	reg   *Registry
	scope Scope
}

func (a *toolAugmenter) Name() string { return "tools" }

func (a *toolAugmenter) Augment(ctx context.Context, snap *kernel.Snapshot) error {
	ts, _, err := a.reg.Assemble(ctx, snap.Runtime, a.scope)
	if err != nil {
		return err
	}
	snap.Tools = append(snap.Tools, ts...)
	return nil
}

// renamedTool 用新名字包装一个工具，其余行为原样透传。
// renamedTool wraps a tool under a new name and passes everything else through.
type renamedTool struct {
	inner tool.BaseTool
	name  string
}

func renameTool(t tool.BaseTool, name string) tool.BaseTool {
	if inv, ok := t.(tool.InvokableTool); ok {
		return &renamedInvokable{renamedTool{inner: t, name: name}, inv}
	}
	return &renamedTool{inner: t, name: name}
}

func (r *renamedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	info, err := r.inner.Info(ctx)
	if err != nil {
		return nil, err
	}
	clone := *info
	clone.Name = r.name
	clone.Desc = fmt.Sprintf("%s\n\n(原名 %s，因重名加了来源前缀 / renamed from %s due to a name collision)",
		strings.TrimSpace(info.Desc), info.Name, info.Name)
	return &clone, nil
}

type renamedInvokable struct {
	renamedTool
	inv tool.InvokableTool
}

func (r *renamedInvokable) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	return r.inv.InvokableRun(ctx, args, opts...)
}
