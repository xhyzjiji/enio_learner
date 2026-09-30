// Package skill 实现技能的渐进披露（progressive disclosure）。
// Package skill implements progressive disclosure of skills.
//
// 问题背景：如果把所有技能的完整说明都塞进 system prompt，技能一多就会撑爆上下文，
// 而且绝大部分内容和当前这次提问无关，反而干扰模型。
// The problem: cramming every skill's full text into the system prompt blows up the context as
// skills accumulate, and most of it is irrelevant to the question at hand — it distracts the model.
//
// 解法是分两级：
//   - 第一级：只把「名字 + 一句话描述」常驻 system prompt，便宜，用来做选择；
//   - 第二级：模型判断某个技能匹配后，调用 load_skill 工具把完整正文取回来。
//
// The fix is two tiers:
//   - Tier 1: only "name + one-line description" stays in the system prompt — cheap, enough to choose;
//   - Tier 2: once the model picks a skill, it calls the load_skill tool to fetch the full text.
//
// 所以技能描述的写法很关键：description 要写清楚「什么时候该用它」，因为那是模型做选择时
// 唯一能看到的信息；正文才写「具体怎么做」。
// This makes description wording critical: it must say *when* to use the skill, since that is all the
// model sees when choosing. The body is where *how* belongs.
package skill

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// fileName 是每个技能目录里约定的文件名，和 Claude / Cursor 的 Agent Skills 约定一致。
// fileName is the conventional file inside each skill directory, matching Claude / Cursor Agent Skills.
const fileName = "SKILL.md"

// Skill 是一个技能：前言里的元信息加上正文步骤。
// Skill is one skill: the front-matter metadata plus the body of instructions.
type Skill struct {
	Name        string
	Description string
	Body        string
}

// Registry 持有全部已加载的技能。/ Registry holds every loaded skill.
type Registry struct {
	skills map[string]Skill
	names  []string
}

// Load 从文件系统里扫描 <任意目录>/SKILL.md 并解析。
// Load scans the filesystem for <any dir>/SKILL.md and parses them.
//
// 接收 fs.FS 而不是目录路径，是为了让调用方用 go:embed 把技能编进二进制，
// 这样在任何工作目录下 go run 都能跑，不依赖相对路径。
// It takes an fs.FS rather than a path so callers can go:embed the skills into the binary,
// making `go run` work from any working directory without relying on relative paths.
func Load(fsys fs.FS) (*Registry, error) {
	reg := &Registry{skills: map[string]Skill{}}

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Base(p) != fileName {
			return nil
		}

		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}

		s, err := parse(string(raw))
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		if _, dup := reg.skills[s.Name]; dup {
			return fmt.Errorf("duplicate skill name %q in %s", s.Name, p)
		}

		reg.skills[s.Name] = s
		reg.names = append(reg.names, s.Name)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(reg.names) == 0 {
		return nil, fmt.Errorf("no %s found", fileName)
	}
	sort.Strings(reg.names)
	return reg, nil
}

// Catalog 渲染第一级信息：喂给 system prompt 的技能清单。
// Catalog renders tier one: the skill listing that goes into the system prompt.
func (r *Registry) Catalog() string {
	var sb strings.Builder
	for _, name := range r.names {
		s := r.skills[name]
		sb.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Names 返回全部技能名，按字典序。/ Names returns every skill name in lexical order.
func (r *Registry) Names() []string {
	return append([]string(nil), r.names...)
}

// LoadSkillInput 是 load_skill 的入参。/ LoadSkillInput is the input of load_skill.
type LoadSkillInput struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"要加载的技能名称，必须是系统提示词中「可用技能」清单里列出的名字之一"`
}

// NewLoadSkillTool 构造第二级的取用工具。
// NewLoadSkillTool builds the tier-two retrieval tool.
func (r *Registry) NewLoadSkillTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"load_skill",
		"加载一个技能的完整执行说明。当你判断某个技能适用于当前任务时，先调用它拿到详细步骤，再按步骤执行。",
		func(_ context.Context, in LoadSkillInput) (string, error) {
			s, ok := r.skills[in.Name]
			if !ok {
				// 返回可用列表而不是干巴巴的报错，模型下一轮就能自己纠正。
				// Returning the available list instead of a bare error lets the model self-correct next turn.
				return fmt.Sprintf("技能 %q 不存在。可用技能：%s", in.Name, strings.Join(r.names, "、")), nil
			}
			return s.Body, nil
		},
	)
}

// parse 解析 SKILL.md：--- 包围的 YAML 前言 + Markdown 正文。
// parse reads a SKILL.md: YAML front matter fenced by ---, followed by the Markdown body.
//
// 前言只有 name 和 description 两个键，手写解析即可，不值得为此引入 YAML 依赖。
// The front matter has only name and description, so hand-parsing beats pulling in a YAML dependency.
func parse(raw string) (Skill, error) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Skill{}, fmt.Errorf("missing front matter: file must start with ---")
	}

	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Skill{}, fmt.Errorf("unterminated front matter: missing closing ---")
	}

	front := rest[:end]
	body := strings.TrimLeft(rest[end+len("\n---"):], "\n")

	var s Skill
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Skill{}, fmt.Errorf("malformed front matter line: %q", line)
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)

		switch strings.TrimSpace(key) {
		case "name":
			s.Name = value
		case "description":
			s.Description = value
		}
	}

	if s.Name == "" || s.Description == "" {
		return Skill{}, fmt.Errorf("front matter must define both name and description")
	}
	if strings.TrimSpace(body) == "" {
		return Skill{}, fmt.Errorf("skill body is empty")
	}

	s.Body = strings.TrimSpace(body)
	return s, nil
}
