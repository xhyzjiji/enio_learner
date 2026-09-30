// Package democli 收敛各个 demo 入口的公共逻辑：注册表、错误处理、退出码。
// Package democli holds what every demo entrypoint shares: the registry, error handling and exit codes.
//
// 对应 Python 里每个文件末尾的 if __name__ == "__main__" 块。
// It plays the role of the `if __name__ == "__main__"` block at the bottom of each Python file.
package democli

import (
	"context"
	"fmt"
	"os"
	"sort"
)

// RunFunc 是每个 demo 对外暴露的执行函数。/ RunFunc is the entry function each demo exposes.
type RunFunc func(context.Context) error

// 能力分类，决定分发器里的分组。按 Order 由浅入深排列，也就是建议的学习顺序。
// Capability categories, used to group demos in the dispatcher. Ordered shallow-to-deep by Order,
// which doubles as the suggested learning path.
const (
	CatCompose    = "编排底座（不涉及 LLM）/ orchestration, no LLM"
	CatAgent      = "单 Agent / single agent"
	CatMCP        = "外部工具集成 / external tool integration"
	CatSkill      = "技能驱动（实战案例）/ skill-driven, hands-on case study"
	CatMultiAgent = "多智能体编排 / multi-agent orchestration"
)

// Demo 描述一个可独立运行的 demo。/ Demo describes one independently runnable demo.
type Demo struct {
	Name string
	Desc string

	// Category 是能力分类，Order 是全局学习顺序，两者共同决定分发器的输出排版。
	// Category is the capability group and Order the global learning position; together they drive the listing.
	Category string
	Order    int

	// Needs 一句话说明运行前置条件，避免照着列表跑却缺环境变量。
	// Needs states the prerequisites in one line, so nobody runs a demo only to hit a missing env var.
	Needs string

	Run RunFunc
}

// Group 是按 Category 聚合后的一组 demo。/ Group is a set of demos sharing one Category.
type Group struct {
	Category string
	Demos    []Demo
}

// registry 由各 demo 在 init 中注册，避免本包反向依赖 demo 包。
// registry is populated by each demo's init, so this package doesn't depend on the demo packages.
var registry = map[string]Demo{}

// Register 登记一个 demo，供统一分发器使用。/ Register records a demo for the dispatcher.
func Register(d Demo) {
	if _, dup := registry[d.Name]; dup {
		panic(fmt.Sprintf("democli: duplicate demo name %q", d.Name))
	}
	registry[d.Name] = d
}

// Lookup 按名字取出 demo。/ Lookup fetches a demo by name.
func Lookup(name string) (Demo, bool) {
	d, ok := registry[name]
	return d, ok
}

// All 返回按学习顺序排序的全部 demo。/ All returns every demo in learning order.
func All() []Demo {
	demos := make([]Demo, 0, len(registry))
	for _, d := range registry {
		demos = append(demos, d)
	}
	sort.Slice(demos, func(i, j int) bool {
		if demos[i].Order != demos[j].Order {
			return demos[i].Order < demos[j].Order
		}
		return demos[i].Name < demos[j].Name
	})
	return demos
}

// Grouped 按 Category 聚合 demo，组内保持学习顺序，组间按各自最靠前的 Order 排列。
// Grouped buckets demos by Category, keeping learning order within a group and ordering
// groups by their earliest Order.
func Grouped() []Group {
	var groups []Group
	index := map[string]int{}

	// All 已排好序，顺序遍历即可让组内和组间都自然有序。
	// All is already sorted, so a single pass keeps both intra- and inter-group order correct.
	for _, d := range All() {
		i, ok := index[d.Category]
		if !ok {
			index[d.Category] = len(groups)
			groups = append(groups, Group{Category: d.Category})
			i = len(groups) - 1
		}
		groups[i].Demos = append(groups[i].Demos, d)
	}
	return groups
}

// Main 是单个 demo 独立入口的统一收尾：跑一次，出错就打印并以非零码退出。
// Main is the shared tail of a standalone demo entrypoint: run once, print any error and exit non-zero.
func Main(name string, run RunFunc) {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "demo %s failed: %v\n", name, err)
		os.Exit(1)
	}
}
