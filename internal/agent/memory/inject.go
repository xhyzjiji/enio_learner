package memory

import (
	"context"
	"fmt"
	"strings"

	"private/agent_basedon_eino/internal/agent/kernel"
)

// Injector 把长期记忆注入系统提示词，并在记忆过多时改为按需召回。
//
// 分档的理由很实际：记忆少的时候全量注入最可靠——模型一定看得到，不依赖它
// 主动想起来要查。但记忆涨到几百条后，全量注入会吃掉大量上下文，而且绝大多数
// 与当前话题无关，反而稀释了模型对有用信息的注意力。
//
// Injector injects long-term memories into the system prompt, switching to on-demand recall once
// there are too many.
//
// The tiering has a practical reason: with few memories, injecting all of them is the most
// reliable option — the model definitely sees them and need not think to look them up. Past a
// few hundred, though, wholesale injection eats a large slice of the context with items mostly
// unrelated to the current topic, diluting the model's attention to what actually matters.
type Injector struct {
	store *Store
}

// NewInjector 构造注入器。
// NewInjector builds the injector.
func NewInjector(s *Store) *Injector { return &Injector{store: s} }

// Augmenter 返回把记忆写进快照的 kernel.Augmenter。
// Augmenter returns a kernel.Augmenter that writes memories into the snapshot.
func (i *Injector) Augmenter() kernel.Augmenter { return &memAugmenter{inj: i} }

type memAugmenter struct{ inj *Injector }

func (a *memAugmenter) Name() string { return "memory" }

func (a *memAugmenter) Augment(ctx context.Context, snap *kernel.Snapshot) error {
	limit := snap.Runtime.MemoryInjectLimit
	count, err := a.inj.store.Count(ctx)
	if err != nil {
		return err
	}

	// 记忆写入工具任何时候都挂：能记住新事情与能召回旧事情是两件事。
	// The remember tool is always mounted: recording something new and recalling something old
	// are separate capabilities.
	snap.Tools = append(snap.Tools, NewRememberTool(a.inj.store, snap.SessionID))

	if count == 0 {
		return nil
	}
	if count <= limit {
		all, err := a.inj.store.List(ctx)
		if err != nil {
			return err
		}
		snap.Instruction = appendMemories(snap.Instruction, all)
		// 全量注入时**不挂** recall 工具：记忆已经全在提示词里了，再给一个查询工具
		// 只会诱导模型多跑一次无谓的工具调用去查它眼前就有的东西。
		// With everything injected the recall tool is NOT mounted: the memories are already in
		// the prompt, and offering a lookup tool would only tempt the model into a pointless
		// extra call to fetch what is right in front of it.
		return nil
	}

	snap.Instruction = appendMemoryHint(snap.Instruction, count)
	snap.Tools = append(snap.Tools, NewRecallTool(a.inj.store))
	return nil
}

func appendMemories(instruction string, all []Memory) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(instruction))
	sb.WriteString("\n\n## 关于这位用户你已经知道的事 / What you already know about this user\n")
	for _, m := range all {
		if m.Category != "" {
			fmt.Fprintf(&sb, "- [%s] %s：%s\n", m.Category, m.Key, m.Content)
		} else {
			fmt.Fprintf(&sb, "- %s：%s\n", m.Key, m.Content)
		}
	}
	sb.WriteString("\n这些是跨对话保留的长期信息，回答时应当自然地考虑它们，" +
		"但不要主动复述给用户听。\nThese are long-term facts kept across conversations. " +
		"Take them into account naturally, but do not recite them back to the user.\n")
	return sb.String()
}

func appendMemoryHint(instruction string, count int) string {
	return fmt.Sprintf(
		"%s\n\n## 长期记忆 / Long-term memory\n"+
			"你保存了 %d 条关于这位用户的长期信息，数量较多因此没有全部列出。"+
			"当问题可能涉及用户的偏好、背景或以往约定时，用 recall 工具按关键词查询。\n"+
			"You hold %d long-term facts about this user — too many to list here. "+
			"When a question may involve their preferences, background or earlier agreements, "+
			"look them up with the recall tool.\n",
		strings.TrimSpace(instruction), count, count)
}
