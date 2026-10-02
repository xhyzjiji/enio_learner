// Package web 把前端构建产物嵌入二进制。
//
// 这个包只有一个作用：让 `go:embed dist` 写在 `web/` 目录里。go:embed 不能引用父目录，
// 所以嵌入指令必须和 `dist/` 同级，不能放在 internal/agent/httpapi 下面。
//
// Package web embeds the frontend build output into the binary.
//
// Its only purpose is to place `go:embed dist` inside the `web/` directory. go:embed cannot
// reference parent directories, so the directive must sit next to `dist/` and cannot live
// under internal/agent/httpapi.
package web

import (
	"embed"
	"io/fs"
)

// all: 前缀是必需的：不加的话 go:embed 会跳过以 `_` 或 `.` 开头的文件，
// 而前端构建产物里 `.vite` 之类的目录并不罕见。
// The all: prefix is required: without it go:embed skips files starting with `_` or `.`, and
// frontend build output containing directories such as `.vite` is not unusual.
//
//go:embed all:dist
var distFS embed.FS

// Dist 返回以 dist 为根的文件系统。
// Dist returns a file system rooted at dist.
func Dist() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
