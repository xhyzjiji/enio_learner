// Package cli 提供本地命令执行与受限文件访问能力。
//
// 本包实现两类对外能力：
//   - Workspace：路径受限的 filesystem.Backend，同时服务 filesystem / reduction /
//     plantask 三个中间件
//   - 声明式 CLI 工具与 execute 自由执行工具
//
// 之所以自研 Workspace 而不直接用 eino-ext 的 adk/backend/local，是因为后者有两处
// 与本项目安全要求冲突的硬伤（T005 阅读源码时发现）：
//  1. 它的 Execute 走 /bin/sh -c 且不设置 cmd.Env，子进程会继承父进程全部环境变量，
//     模型只要执行一次 env 就能读到 ZHIPUAI_API_KEY
//  2. 它的所有路径方法只做 filepath.Clean，没有任何根目录限制，模型给什么路径就读写
//     什么路径
//
// Package cli provides local command execution and confined file access.
//
// It exposes two capabilities:
//   - Workspace: a path-confined filesystem.Backend serving the filesystem, reduction and
//     plantask middlewares simultaneously
//   - declarative CLI tools and the free-form execute tool
//
// Workspace is implemented here rather than reusing eino-ext's adk/backend/local because that
// package has two properties that conflict with this project's security requirements (found
// while reading its source during gate T005):
//  1. its Execute runs /bin/sh -c without setting cmd.Env, so the child inherits the parent's
//     entire environment and a single `env` call would hand the model ZHIPUAI_API_KEY
//  2. all of its path methods only apply filepath.Clean with no root confinement, so the model
//     reads and writes whatever path it names
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk/filesystem"

	"private/agent_basedon_eino/internal/agent/kernel"
)

const (
	// maxReadBytes 是单文件读取上限，防止一个大文件直接撑爆上下文。
	// maxReadBytes caps single-file reads so one large file cannot blow up the context.
	maxReadBytes = 2 << 20

	// maxGrepMatches 与 maxGlobResults 限制检索结果条数。
	// maxGrepMatches and maxGlobResults bound search result counts.
	maxGrepMatches  = 200
	maxGlobResults  = 500
	maxWalkFileSize = 4 << 20
)

// Workspace 是根目录受限的文件后端。
//
// 所有对外方法收到的路径都会先经 resolve 归一化并校验是否落在根目录内，
// 校验用的是 EvalSymlinks 之后的真实路径——只比较字符串的话，一个指向 /etc 的软链
// 就能绕过全部限制。
//
// Workspace is a file backend confined to a root directory.
//
// Every path arriving at a public method goes through resolve, which normalizes it and checks
// that it lands inside the root. The check happens on the real path after EvalSymlinks: a
// string-only comparison would be defeated by a single symlink pointing at /etc.
type Workspace struct {
	root string
}

// NewWorkspace 以 root 为根构造工作区。root 会被解析为绝对路径并在必要时创建。
// NewWorkspace builds a workspace rooted at root, resolved to an absolute path and created
// if necessary.
func NewWorkspace(root string) (*Workspace, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("workspace root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root %s: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace root %s: %w", abs, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("evaluate workspace root %s: %w", abs, err)
	}
	return &Workspace{root: real}, nil
}

// Root 返回工作区根目录的绝对路径。
// Root returns the absolute path of the workspace root.
func (w *Workspace) Root() string { return w.root }

// ErrOutsideWorkspace 表示路径越出了工作区根目录。
// ErrOutsideWorkspace signals a path escaping the workspace root.
var ErrOutsideWorkspace = errors.New("path escapes the workspace root")

// resolve 把模型给的路径归一化为工作区内的绝对路径。
//
// 对已存在的路径，用 EvalSymlinks 拿到真实路径再比对；对尚不存在的路径（写文件的
// 常见情况），退一步校验其最近的已存在父目录。只做字符串前缀比较是不够的：
// root/link 若指向 /etc，字符串上看它在 root 里面，实际读写的却是 /etc。
//
// resolve normalizes a model-supplied path into an absolute path inside the workspace.
//
// For existing paths it compares the real path after EvalSymlinks; for paths that do not exist
// yet (the common case when writing) it falls back to validating the nearest existing parent.
// A string prefix comparison alone is insufficient: if root/link points at /etc it looks like
// it is inside root while reads and writes actually land in /etc.
func (w *Workspace) resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return w.root, nil
	}
	candidate := p
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(w.root, candidate)
	}
	candidate = filepath.Clean(candidate)

	probe := candidate
	for {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if !within(w.root, real) {
				return "", fmt.Errorf("%w: %s", ErrOutsideWorkspace, p)
			}
			// 把已验证的真实前缀与剩余部分重新拼起来。
			// Reassemble the verified real prefix with the remaining segments.
			rest := strings.TrimPrefix(candidate, probe)
			return filepath.Join(real, rest), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve path %s: %w", p, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("%w: %s", ErrOutsideWorkspace, p)
		}
		probe = parent
	}
}

// within 判断 target 是否在 base 之内（含 base 本身）。
// within reports whether target lies inside base, base itself included.
func within(base, target string) bool {
	if base == target {
		return true
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// rel 把绝对路径转回相对于根目录的形式，避免把宿主机的真实目录结构泄露给模型。
// rel converts an absolute path back to a workspace-relative one, so the host's real directory
// layout is never revealed to the model.
func (w *Workspace) rel(abs string) string {
	r, err := filepath.Rel(w.root, abs)
	if err != nil {
		return abs
	}
	return r
}

// LsInfo 列出目录内容。
// LsInfo lists directory contents.
func (w *Workspace) LsInfo(_ context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	target, err := w.resolve(req.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s: %w", w.rel(target), err)
	}
	out := make([]filesystem.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, filesystem.FileInfo{
			Path:       w.rel(filepath.Join(target, e.Name())),
			IsDir:      e.IsDir(),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

// Read 按行读取文件，支持 offset 与 limit。
// Read reads a file by line with offset and limit support.
func (w *Workspace) Read(_ context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	target, err := w.resolve(req.FilePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", w.rel(target), err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("read %s: is a directory", w.rel(target))
	}
	if info.Size() > maxReadBytes && req.Limit == 0 {
		return nil, fmt.Errorf(
			"文件 %s 有 %d 字节，超过单次读取上限 %d，请用 offset/limit 分段读取 / "+
				"file %s is %d bytes, above the %d single-read cap; use offset/limit to page through it",
			w.rel(target), info.Size(), maxReadBytes, w.rel(target), info.Size(), maxReadBytes)
	}

	f, err := os.Open(target)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", w.rel(target), err)
	}
	defer f.Close()

	offset := req.Offset
	if offset < 1 {
		offset = 1
	}
	var sb strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxReadBytes)
	line := 0
	emitted := 0
	for sc.Scan() {
		line++
		if line < offset {
			continue
		}
		if req.Limit > 0 && emitted >= req.Limit {
			break
		}
		sb.WriteString(sc.Text())
		sb.WriteByte('\n')
		emitted++
		if sb.Len() > maxReadBytes {
			sb.WriteString("\n[内容已截断 / content truncated]\n")
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", w.rel(target), err)
	}
	return &filesystem.FileContent{Content: sb.String()}, nil
}

// Write 写入文件，必要时创建父目录。
// Write writes a file, creating parent directories when needed.
func (w *Workspace) Write(_ context.Context, req *filesystem.WriteRequest) error {
	target, err := w.resolve(req.FilePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", w.rel(target), err)
	}
	if err := os.WriteFile(target, []byte(req.Content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", w.rel(target), err)
	}
	return nil
}

// Edit 做字符串替换。ReplaceAll 为假且匹配不唯一时报错，而不是随便挑一处替换——
// 后者会让模型以为改对了，实际改到了别处。
// Edit performs string replacement. When ReplaceAll is false and the match is not unique it
// fails rather than picking one arbitrarily, which would leave the model believing it edited
// the right place while something else changed.
func (w *Workspace) Edit(_ context.Context, req *filesystem.EditRequest) error {
	if req.OldString == "" {
		return errors.New("old_string 不能为空 / old_string must not be empty")
	}
	if req.OldString == req.NewString {
		return errors.New("new_string 与 old_string 相同 / new_string equals old_string")
	}
	target, err := w.resolve(req.FilePath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read %s for edit: %w", w.rel(target), err)
	}
	content := string(raw)
	count := strings.Count(content, req.OldString)
	if count == 0 {
		return fmt.Errorf("在 %s 中未找到待替换内容 / old_string not found in %s",
			w.rel(target), w.rel(target))
	}
	if count > 1 && !req.ReplaceAll {
		return fmt.Errorf(
			"在 %s 中匹配到 %d 处，请提供更多上下文使其唯一，或使用 replace_all / "+
				"%d matches in %s; supply more context to make it unique, or use replace_all",
			w.rel(target), count, count, w.rel(target))
	}
	updated := strings.ReplaceAll(content, req.OldString, req.NewString)
	if err := os.WriteFile(target, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s after edit: %w", w.rel(target), err)
	}
	return nil
}

// Delete 删除文件，供 plantask 清理任务文件。
// Delete removes a file, used by plantask to clean up task files.
func (w *Workspace) Delete(_ context.Context, req *kernel.DeleteRequest) error {
	target, err := w.resolve(req.FilePath)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", w.rel(target), err)
	}
	return nil
}

// GlobInfo 按 glob 模式匹配文件，支持 ** 递归。
// GlobInfo matches files by glob pattern, with ** recursion support.
func (w *Workspace) GlobInfo(_ context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	base, err := w.resolve(req.Path)
	if err != nil {
		return nil, err
	}
	re, err := globToRegexp(req.Pattern)
	if err != nil {
		return nil, err
	}

	var out []filesystem.FileInfo
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // 单个条目不可读不应中断整次遍历 / one unreadable entry must not abort the walk
		}
		if len(out) >= maxGlobResults {
			return filepath.SkipAll
		}
		relPath := w.rel(p)
		if !re.MatchString(relPath) && !re.MatchString(filepath.Base(p)) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		out = append(out, filesystem.FileInfo{
			Path:       relPath,
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", req.Pattern, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// GrepRaw 在工作区内按正则搜索文件内容。
// GrepRaw searches file contents inside the workspace by regular expression.
func (w *Workspace) GrepRaw(_ context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	base, err := w.resolve(req.Path)
	if err != nil {
		return nil, err
	}
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("正则表达式无效 / invalid pattern %q: %w", req.Pattern, err)
	}
	var globRe *regexp.Regexp
	if req.Glob != "" {
		globRe, err = globToRegexp(req.Glob)
		if err != nil {
			return nil, err
		}
	}

	var out []filesystem.GrepMatch
	walkErr := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= maxGrepMatches {
			return filepath.SkipAll
		}
		relPath := w.rel(p)
		if globRe != nil && !globRe.MatchString(relPath) {
			return nil
		}
		if req.FileType != "" && !strings.EqualFold(strings.TrimPrefix(filepath.Ext(p), "."), req.FileType) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxWalkFileSize {
			return nil //nolint:nilerr
		}
		matches, err := grepFile(p, relPath, re, maxGrepMatches-len(out))
		if err != nil {
			return nil //nolint:nilerr
		}
		out = append(out, matches...)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("grep %s: %w", req.Pattern, walkErr)
	}
	return out, nil
}

func grepFile(abs, rel string, re *regexp.Regexp, limit int) ([]filesystem.GrepMatch, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []filesystem.GrepMatch
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		if len(out) >= limit {
			break
		}
		text := sc.Text()
		if re.MatchString(text) {
			out = append(out, filesystem.GrepMatch{Path: rel, Line: line, Content: text})
		}
	}
	return out, sc.Err()
}

// shouldSkipDir 跳过明显不该搜的目录，避免 node_modules 这类目录把结果淹没。
// shouldSkipDir skips directories that should obviously never be searched, so the likes of
// node_modules cannot drown the results.
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", ".venv", "__pycache__":
		return true
	}
	return false
}

// globToRegexp 把 glob 模式编译成正则。单独实现是因为标准库的 filepath.Match
// 不支持 ** 递归匹配。
// globToRegexp compiles a glob pattern into a regular expression. A hand-rolled version is
// needed because the standard library's filepath.Match does not support ** recursion.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		pattern = "*"
	}
	var sb strings.Builder
	sb.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				sb.WriteString(".*")
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
				}
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end < 0 {
				sb.WriteString(regexp.QuoteMeta(string(c)))
				continue
			}
			sb.WriteString(pattern[i : i+end+1])
			i += end
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return nil, fmt.Errorf("glob 模式无效 / invalid glob pattern %q: %w", pattern, err)
	}
	return re, nil
}

// 编译期确认 Workspace 满足 kernel.FileBackend。
// Compile-time assertion that Workspace satisfies kernel.FileBackend.
var _ kernel.FileBackend = (*Workspace)(nil)

// IsOutside 判断错误是否为路径越界。
// IsOutside reports whether an error denotes a path escaping the workspace.
func IsOutside(err error) bool { return errors.Is(err, ErrOutsideWorkspace) }
