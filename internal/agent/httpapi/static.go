package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// MountStatic 把前端产物挂到根路径，并提供 SPA fallback。
//
// SPA fallback 的必要性：前端路由是客户端的，用户在 /settings 刷新页面时浏览器会向
// 服务端请求 /settings，而磁盘上并没有这个文件。不做 fallback 就是一个 404，
// 表现为"页面刷新就白屏"。所以任何非 /api 且不存在的路径都回 index.html，
// 由前端路由接管。
//
// MountStatic serves the frontend build at the root path with an SPA fallback.
//
// The fallback is necessary because frontend routing is client-side: refreshing on /settings
// makes the browser request /settings from the server, and no such file exists on disk. Without
// a fallback that is a 404, experienced as "the page goes blank on refresh". Any non-/api path
// that does not exist therefore returns index.html and lets the frontend router take over.
func (s *Server) MountStatic(dist fs.FS) {
	fileServer := http.FileServer(http.FS(dist))

	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// /api 下的未知路径必须是 404 JSON，绝不能回 index.html：
		// 前端拿到一坨 HTML 去 JSON.parse 会报一个与真实原因毫无关系的解析错误。
		// Unknown paths under /api must yield a JSON 404 and never index.html: handing HTML to
		// the frontend's JSON.parse produces a parse error unrelated to the real cause.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			WriteJSON(w, http.StatusNotFound, errorBody{
				Error:   "not_found",
				Message: "接口不存在 / no such endpoint: " + r.URL.Path,
			})
			return
		}
		if exists(dist, r.URL.Path) {
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, dist)
	})
}

func exists(dist fs.FS, urlPath string) bool {
	name := strings.TrimPrefix(path.Clean(urlPath), "/")
	if name == "" || name == "." {
		return false
	}
	f, err := dist.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	return err == nil && !info.IsDir()
}

func serveIndex(w http.ResponseWriter, dist fs.FS) {
	data, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		http.Error(w,
			"前端资源未构建，请先在 web/ 目录执行 npm run build / "+
				"frontend assets are not built; run npm run build in web/ first",
			http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// index.html 不能缓存：它引用的 JS/CSS 文件名带内容哈希，缓存住 index.html
	// 就等于永远加载旧版本的资源引用。
	// index.html must not be cached: it references content-hashed JS/CSS filenames, so a cached
	// index.html pins the app to an old set of asset references forever.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
