// Package skills 管理技能：数据库中页面创建的技能与磁盘目录扫描到的技能。
//
// 两个来源合并时数据库优先。这个方向不是随手定的：磁盘目录通常是用户从别处拷来的
// 技能包，而数据库里的是他在页面上明确编辑过的。用户编辑过的东西被一次目录扫描
// 悄悄覆盖掉，是最难接受的一类数据丢失。
//
// Package skills manages skills from two sources: those created in the UI and stored in the
// database, and those discovered by scanning a directory on disk.
//
// The database wins on collisions. That direction is deliberate: a directory usually holds skill
// packs copied in from elsewhere, whereas the database holds what the user explicitly edited in
// the UI. Having your own edits silently overwritten by a directory scan is the least acceptable
// kind of data loss.
package skills

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	skillmw "github.com/cloudwego/eino/adk/middlewares/skill"
	"gopkg.in/yaml.v3"

	"private/agent_basedon_eino/internal/agent/store"
)

// Skill 是一条技能记录。
// Skill is one skill record.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	// ContextMode 为空表示 inline（在当前对话里展开），另两个取值是 fork 与
	// fork_with_context。T005 实测确认框架只认这三种，没有别的常量。
	// An empty ContextMode means inline (expanded in the current conversation); the other two
	// values are fork and fork_with_context. Gate T005 confirmed the framework recognises only
	// these three and defines no other constants.
	ContextMode string `json:"context_mode"`
	Agent       string `json:"agent"`
	Model       string `json:"model"`
	Enabled     bool   `json:"enabled"`
	// Source 标明来源，页面据此决定是否允许编辑。
	// Source marks the origin so the UI can decide whether editing is allowed.
	Source    string `json:"source"`
	Path      string `json:"path,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`

	// 以下字段只来自磁盘 SKILL.md 的 frontmatter，页面创建的技能没有。
	// 它们不入库，每次扫描重新解析，因此改完文件就是最新的。
	//
	// The fields below come only from the frontmatter of a SKILL.md on disk; skills created in
	// the UI have none. They are not persisted and are re-parsed on every scan, so editing the
	// file is immediately reflected.
	DisplayName string   `json:"display_name,omitempty"`
	Version     string   `json:"version,omitempty"`
	Author      string   `json:"author,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	// RequiresBins 是技能声明的外部命令依赖及其当前可用性。
	//
	// 单列出名字没什么用：技能装好了但它依赖的 CLI 没装，是这类技能包最常见的失败
	// 方式，而且失败时只会表现为模型跑了一条命令拿到 127。把"在不在"直接查出来
	// 摆在页面上，这个问题在用技能之前就能看见。
	//
	// RequiresBins lists the external commands a skill declares, along with their current
	// availability.
	//
	// Listing names alone would be of little use: a skill installed without its CLI is the most
	// common failure mode for these packs, and it surfaces only as the model running a command
	// and getting a 127 back. Resolving availability up front puts the problem on screen before
	// the skill is ever used.
	RequiresBins []BinRequirement `json:"requires_bins,omitempty"`

	// CLIHelp 是技能声明的帮助命令，页面原样展示供用户自己验证。
	// CLIHelp is the help command a skill declares, shown verbatim for the user to verify.
	CLIHelp string `json:"cli_help,omitempty"`
}

// BinRequirement 是一条外部命令依赖。
// BinRequirement is one external command dependency.
type BinRequirement struct {
	Name string `json:"name"`
	// Available 的判断基于后端进程自己的 PATH。这正是该查的那一份：execute 的子进程
	// 通过环境白名单继承的就是同一个 PATH，所以这里查到的结果与技能实际执行时一致。
	//
	// Available is resolved against the backend process's own PATH. That is the right one to
	// check: execute's children inherit the very same PATH through the environment whitelist, so
	// what is reported here matches what the skill will actually encounter.
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
}

// 来源取值。
// Source values.
const (
	SourceDB   = "db"
	SourceDisk = "disk"
)

// scanTTL 是目录扫描结果的缓存时长。
//
// 不缓存的话，每轮对话的 skillTool.Info(ctx) 都会触发一次全目录遍历——技能目录
// 大一点，这笔开销就直接加在每一次模型调用前面。缓存几秒既挡住了这个问题，
// 又保留了"改完文件很快就能看到"的手感。
//
// scanTTL is how long a directory scan result is cached.
//
// Without it, skillTool.Info(ctx) would walk the entire directory on every turn, and with a
// sizeable skills directory that cost lands in front of every single model call. A few seconds
// of caching removes the problem while keeping edits visible almost immediately.
const scanTTL = 5 * time.Second

// Store 合并数据库与磁盘两个技能来源。
// Store merges the database and disk skill sources.
type Store struct {
	db  *store.DB
	dir string

	mu       sync.Mutex
	cached   []Skill
	cachedAt time.Time
}

// NewStore 构造技能存储。dir 为空时只用数据库来源。
// NewStore builds the skill store. An empty dir means the database is the only source.
func NewStore(db *store.DB, dir string) *Store {
	return &Store{db: db, dir: dir}
}

// ErrNotFound 表示技能不存在。
// ErrNotFound signals a missing skill.
var ErrNotFound = errors.New("skill not found")

// List 返回合并后的技能列表。
// List returns the merged skill list.
func (s *Store) List(ctx context.Context) ([]Skill, error) {
	dbSkills, err := s.listDB(ctx)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]Skill, len(dbSkills))
	for _, sk := range s.scanDir() {
		merged[sk.Name] = sk
	}
	for _, sk := range dbSkills {
		merged[sk.Name] = sk
	}

	out := make([]Skill, 0, len(merged))
	for _, sk := range merged {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get 返回单个技能。
// Get returns one skill.
func (s *Store) Get(ctx context.Context, name string) (Skill, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Skill{}, err
	}
	for _, sk := range all {
		if sk.Name == name {
			return sk, nil
		}
	}
	return Skill{}, ErrNotFound
}

func (s *Store) listDB(ctx context.Context) ([]Skill, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT name, description, body, context_mode, agent, model, enabled, created_at, updated_at
		FROM skills ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()

	var out []Skill
	for rows.Next() {
		var sk Skill
		if err := rows.Scan(&sk.Name, &sk.Description, &sk.Body, &sk.ContextMode,
			&sk.Agent, &sk.Model, &sk.Enabled, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, err
		}
		sk.Source = SourceDB
		out = append(out, sk)
	}
	return out, rows.Err()
}

// Save 新增或覆盖一个数据库技能。
// Save inserts or replaces a database skill.
func (s *Store) Save(ctx context.Context, sk Skill) (Skill, error) {
	if strings.TrimSpace(sk.Name) == "" {
		return sk, errors.New("技能名不能为空 / skill name must not be empty")
	}
	if err := validateContextMode(sk.ContextMode); err != nil {
		return sk, err
	}
	now := store.Now()
	sk.UpdatedAt = now
	if sk.CreatedAt == 0 {
		sk.CreatedAt = now
	}
	_, err := s.db.Write().ExecContext(ctx, `
		INSERT INTO skills (name, description, body, context_mode, agent, model, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			description = excluded.description, body = excluded.body,
			context_mode = excluded.context_mode, agent = excluded.agent, model = excluded.model,
			enabled = excluded.enabled, updated_at = excluded.updated_at`,
		sk.Name, sk.Description, sk.Body, sk.ContextMode, sk.Agent, sk.Model,
		sk.Enabled, sk.CreatedAt, sk.UpdatedAt)
	if err != nil {
		return sk, fmt.Errorf("save skill %s: %w", sk.Name, err)
	}
	sk.Source = SourceDB
	return sk, nil
}

// Delete 删除一个数据库技能。磁盘来源的技能删不掉，也不该删——
// 那是用户文件系统上的东西，页面上的删除按钮不该有这个权力。
// Delete removes a database skill. Disk-sourced skills cannot and should not be deleted here:
// they are files on the user's filesystem, and a delete button in a web UI has no business
// reaching them.
func (s *Store) Delete(ctx context.Context, name string) error {
	res, err := s.db.Write().ExecContext(ctx, `DELETE FROM skills WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete skill %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Reload 使目录扫描缓存立即失效。
// Reload invalidates the directory scan cache immediately.
func (s *Store) Reload() {
	s.mu.Lock()
	s.cachedAt = time.Time{}
	s.mu.Unlock()
}

func validateContextMode(mode string) error {
	switch skillmw.ContextMode(mode) {
	case "", skillmw.ContextModeFork, skillmw.ContextModeForkWithContext:
		return nil
	default:
		return fmt.Errorf(
			"context_mode 只能是空（inline）、%s 或 %s，收到 %q / context_mode must be empty (inline), "+
				"%s or %s, got %q",
			skillmw.ContextModeFork, skillmw.ContextModeForkWithContext, mode,
			skillmw.ContextModeFork, skillmw.ContextModeForkWithContext, mode)
	}
}

// scanDir 扫描技能目录，命中缓存时直接返回。
//
// 布局约定与 Claude/Cursor 的技能一致：每个技能是一个子目录，里面有 SKILL.md，
// 头部是 YAML frontmatter。沿用这个约定而不自创格式，是为了让用户能直接把已有的
// 技能包拷进来就用。
//
// scanDir walks the skills directory, returning the cache when still warm.
//
// The layout follows the same convention as Claude/Cursor skills: one subdirectory per skill
// containing SKILL.md with a YAML frontmatter header. Reusing that convention instead of
// inventing a format lets users drop existing skill packs straight in.
func (s *Store) scanDir() []Skill {
	if s.dir == "" {
		return nil
	}
	s.mu.Lock()
	if time.Since(s.cachedAt) < scanTTL {
		cached := s.cached
		s.mu.Unlock()
		return cached
	}
	s.mu.Unlock()

	var found []Skill
	_ = filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(d.Name(), "SKILL.md") {
			return nil //nolint:nilerr // 单个条目失败不应中断扫描 / one failed entry must not abort the scan
		}
		sk, err := parseSkillFile(p)
		if err != nil {
			return nil //nolint:nilerr
		}
		found = append(found, sk)
		return nil
	})

	s.mu.Lock()
	s.cached, s.cachedAt = found, time.Now()
	s.mu.Unlock()
	return found
}

func parseSkillFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	fm, body := splitFrontMatter(string(raw))

	var meta skillMeta
	if fm != "" {
		_ = yaml.Unmarshal([]byte(fm), &meta)
	}
	name := meta.Name
	if name == "" {
		// 没写 name 时用目录名兜底，这是绝大多数技能包的实际情况。
		// Falling back to the directory name covers what most skill packs actually look like.
		name = filepath.Base(filepath.Dir(path))
	}
	info, _ := os.Stat(path)
	var modified int64
	if info != nil {
		modified = info.ModTime().UnixMilli()
	}
	return Skill{
		Name: name, Description: meta.Description, Body: body,
		ContextMode: meta.Context, Agent: meta.Agent, Model: meta.Model,
		Enabled: true, Source: SourceDisk, Path: path,
		CreatedAt: modified, UpdatedAt: modified,
		DisplayName: meta.DisplayName, Version: meta.Version, Author: meta.Author,
		Tags:         meta.Tags,
		RequiresBins: resolveBins(meta.bins()),
		CLIHelp:      firstNonEmpty(meta.CLIHelp, meta.Metadata.CLIHelp),
	}, nil
}

// skillMeta 是 SKILL.md frontmatter 的解析目标。
//
// 依赖声明同时在三个位置接收，因为社区技能包在这件事上没有统一写法：实测
// lark-setup 把 bins 和 cliHelp 写成了顶层键（它的 metadata: 与 requires: 都是空值），
// 而规范写法是嵌在 metadata.requires 下面。只认一种写法就会在另一种上静默读到空值，
// 而"没有声明依赖"和"依赖没解析出来"在页面上看起来完全一样。
//
// skillMeta is the unmarshal target for SKILL.md frontmatter.
//
// Dependency declarations are accepted in three places because community packs have no single
// convention: lark-setup writes bins and cliHelp as top-level keys (its metadata: and requires:
// are both empty), while the documented form nests them under metadata.requires. Honouring only
// one shape would silently yield empty values for the other, and "declares no dependencies" is
// indistinguishable on screen from "dependencies failed to parse".
type skillMeta struct {
	Name        string   `yaml:"name"`
	DisplayName string   `yaml:"displayName"`
	Description string   `yaml:"description"`
	Context     string   `yaml:"context"`
	Agent       string   `yaml:"agent"`
	Model       string   `yaml:"model"`
	Version     string   `yaml:"version"`
	Author      string   `yaml:"author"`
	Tags        []string `yaml:"tags"`

	Bins     []string `yaml:"bins"`
	CLIHelp  string   `yaml:"cliHelp"`
	Requires struct {
		Bins []string `yaml:"bins"`
	} `yaml:"requires"`
	Metadata struct {
		CLIHelp  string `yaml:"cliHelp"`
		Requires struct {
			Bins []string `yaml:"bins"`
		} `yaml:"requires"`
	} `yaml:"metadata"`
}

// bins 按嵌套深度从深到浅取第一个非空的声明。
// bins returns the first non-empty declaration, deepest nesting first.
func (m skillMeta) bins() []string {
	for _, c := range [][]string{m.Metadata.Requires.Bins, m.Requires.Bins, m.Bins} {
		if len(c) > 0 {
			return c
		}
	}
	return nil
}

// resolveBins 查出每个依赖命令当前在不在 PATH 上。
//
// 这里每轮扫描都会跑一遍，但扫描本身有 scanTTL 缓存，几个命令的 LookPath 相比
// 一次目录遍历可以忽略。
//
// resolveBins resolves whether each declared command is currently on PATH.
//
// This runs on every scan, but the scan itself is behind scanTTL, and a handful of LookPath
// calls are negligible next to the directory walk they ride along with.
func resolveBins(names []string) []BinRequirement {
	if len(names) == 0 {
		return nil
	}
	out := make([]BinRequirement, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		req := BinRequirement{Name: n}
		if p, err := exec.LookPath(n); err == nil {
			req.Available, req.Path = true, p
		}
		out = append(out, req)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// splitFrontMatter 切出 YAML frontmatter 与正文。
// splitFrontMatter separates the YAML frontmatter from the body.
func splitFrontMatter(content string) (string, string) {
	trimmed := strings.TrimLeft(content, "\ufeff \t\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return "", content
	}
	rest := trimmed[3:]
	rest = strings.TrimLeft(rest, "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", content
	}
	fm := rest[:end]
	body := rest[end+4:]
	return fm, strings.TrimLeft(body, "\r\n")
}

var _ = sql.ErrNoRows
