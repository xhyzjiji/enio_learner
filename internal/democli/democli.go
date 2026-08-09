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

// Demo 描述一个可独立运行的 demo。/ Demo describes one independently runnable demo.
type Demo struct {
	Name string
	Desc string
	Run  RunFunc
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

// All 返回按名字排序的全部 demo。/ All returns every demo sorted by name.
func All() []Demo {
	demos := make([]Demo, 0, len(registry))
	for _, d := range registry {
		demos = append(demos, d)
	}
	sort.Slice(demos, func(i, j int) bool { return demos[i].Name < demos[j].Name })
	return demos
}

// Main 是单个 demo 独立入口的统一收尾：跑一次，出错就打印并以非零码退出。
// Main is the shared tail of a standalone demo entrypoint: run once, print any error and exit non-zero.
func Main(name string, run RunFunc) {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "demo %s failed: %v\n", name, err)
		os.Exit(1)
	}
}
