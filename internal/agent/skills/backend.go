package skills

import (
	"context"
	"fmt"
	"path/filepath"

	skillmw "github.com/cloudwego/eino/adk/middlewares/skill"

	"private/agent_basedon_eino/internal/agent/kernel"
)

// Backend 实现 skill 中间件的 Backend 接口。
//
// 热重载不需要任何额外机制：T005 实测确认 List 是在 skillTool.Info(ctx) 里被调用的，
// 而 Info 每轮对话都会重新执行一次，技能目录本身就渲染进那个工具的 description。
// 所以页面上改完技能，下一轮对话模型看到的清单就是新的。
//
// Backend implements the skill middleware's Backend interface.
//
// Hot reload needs no extra machinery: gate T005 established that List is called from inside
// skillTool.Info(ctx), that Info re-runs on every turn, and that the catalog renders into that
// tool's description. A skill edited in the UI is therefore already reflected in what the model
// sees on the very next turn.
type Backend struct {
	store *Store
}

// NewBackend 构造技能后端。
// NewBackend builds the skill backend.
func NewBackend(s *Store) *Backend { return &Backend{store: s} }

// List 返回可用技能的元信息。
// List returns the metadata of available skills.
func (b *Backend) List(ctx context.Context) ([]skillmw.FrontMatter, error) {
	all, err := b.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]skillmw.FrontMatter, 0, len(all))
	for _, sk := range all {
		if !sk.Enabled {
			continue
		}
		out = append(out, toFrontMatter(sk))
	}
	return out, nil
}

// Get 返回一个技能的完整内容。
// Get returns the full content of one skill.
func (b *Backend) Get(ctx context.Context, name string) (skillmw.Skill, error) {
	sk, err := b.store.Get(ctx, name)
	if err != nil {
		return skillmw.Skill{}, fmt.Errorf("技能 %s 不存在 / skill %s not found", name, name)
	}
	// BaseDirectory 只有磁盘来源才有意义：技能正文里常用相对路径引用同目录下的
	// 脚本和参考文档，没有这个基准目录模型就找不到它们。数据库技能没有目录，
	// 留空即可。
	// BaseDirectory is meaningful only for disk-sourced skills: their bodies routinely reference
	// scripts and reference documents by relative path, and without the base directory the model
	// cannot locate them. Database skills have no directory, so it stays empty.
	var baseDir string
	if sk.Path != "" {
		baseDir = filepath.Dir(sk.Path)
	}
	return skillmw.Skill{
		FrontMatter:   toFrontMatter(sk),
		Content:       sk.Body,
		BaseDirectory: baseDir,
	}, nil
}

func toFrontMatter(sk Skill) skillmw.FrontMatter {
	return skillmw.FrontMatter{
		Name:        sk.Name,
		Description: sk.Description,
		Context:     skillmw.ContextMode(sk.ContextMode),
		Agent:       sk.Agent,
		Model:       sk.Model,
	}
}

// Augmenter 返回把技能后端写进快照的 kernel.Augmenter。
// Augmenter returns a kernel.Augmenter that writes the skill backend into the snapshot.
func (b *Backend) Augmenter() kernel.Augmenter { return &skillAugmenter{b: b} }

type skillAugmenter struct{ b *Backend }

func (a *skillAugmenter) Name() string { return "skills" }

func (a *skillAugmenter) Augment(_ context.Context, snap *kernel.Snapshot) error {
	snap.SkillBackend = a.b
	return nil
}

var _ skillmw.Backend = (*Backend)(nil)
