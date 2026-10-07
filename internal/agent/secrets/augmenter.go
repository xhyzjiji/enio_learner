package secrets

import (
	"context"
	"fmt"
	"strings"

	"private/agent_basedon_eino/internal/agent/kernel"
)

// secretsNotice 把已配置的凭证清单告诉模型。
//
// 不说等于没配。模型不会去猜一个它没听说过的目录，碰到需要密钥的接口时它只有两种
// 反应：向用户要一遍（于是密钥进了对话历史，正好是这套机制要避免的），或者编一个
// 占位值发出去然后把 401 归因到网络。
//
// 清单里给的是**路径**而不是值，用法示例也只示范 $(cat <path>) 这一种嵌法。
// 多出来的那条"不要单独打印"是必要的：模型在拼命令前习惯先 cat 一下确认内容对不对，
// 这个无害的习惯会把凭证直接送进对话记录，而且记录此后每一轮都会重新送回给它。
//
// secretsNotice tells the model which credentials are configured.
//
// Saying nothing is equivalent to configuring nothing. The model will not guess at a directory
// it has never heard of, so when it meets an endpoint requiring a key it has only two moves: ask
// the user to paste it (putting the key into the conversation history, precisely what this
// mechanism exists to avoid), or invent a placeholder and blame the resulting 401 on the network.
//
// The listing gives PATHS, never values, and the usage example demonstrates only the
// $(cat <path>) form. The "never print it" rule earns its place: before assembling a command a
// model habitually cats a file to confirm its contents, and that harmless habit drops the
// credential straight into the transcript, which is then replayed to it every subsequent turn.
const secretsNotice = `

以下凭证已经配置好，存放在本机文件里。需要时直接在命令中读取对应文件，不要向用户索要：
%s
用法：把 $(cat <上面对应的路径>) 嵌进命令，例如
  curl -H "Authorization: Bearer $(cat /路径/SOME_KEY)" https://api.example.com/v1/search
不要为了确认而单独打印凭证内容（不要 cat 它，也不要 echo 它）。一旦打印，凭证就进了
对话记录，而对话记录在之后每一轮都会重新送回给你，也会随会话导出；嵌在命令里则不会。
清单里没有出现的凭证就是没配置：直接告诉用户去左侧「凭证」分区添加哪一个名字，不要猜也不要编。

The following credentials are configured and stored in local files. Read the matching file inside
your command when you need one; never ask the user to paste it:
%s
Usage: embed $(cat <the matching path above>) in the command, for example
  curl -H "Authorization: Bearer $(cat /path/to/SOME_KEY)" https://api.example.com/v1/search
Never print a credential just to check it (no cat, no echo). Printing places it in the
conversation transcript, which is replayed to you on every later turn and travels with session
exports; embedding it in a command does not.
A credential absent from the list is simply not configured: tell the user which name to add under
the sidebar's credential section rather than guessing or inventing a value.`

// Augmenter 返回把凭证清单注入系统提示词的增强器。
// Augmenter returns the augmenter that injects the credential listing into the system prompt.
func (s *Store) Augmenter() kernel.Augmenter { return &augmenter{store: s} }

type augmenter struct{ store *Store }

func (a *augmenter) Name() string { return "secrets" }

func (a *augmenter) Augment(_ context.Context, snap *kernel.Snapshot) error {
	// execute 关着时子进程根本起不来，凭证读不出来也用不上，讲了只是白占上下文。
	// With execute disabled no child process runs, so a credential can neither be read nor used;
	// mentioning it would only consume context.
	if snap.Shell == nil {
		return nil
	}

	list, err := a.store.List()
	if err != nil {
		// 这里返回错误会中止整轮对话，是刻意的。静默降级的后果是模型突然声称密钥
		// 没配、转而向用户索要，而用户明明刚配过——那种自相矛盾比一条明确的报错
		// 难查得多。目录不存在不会走到这里，List 已经把它当成"一条都没配"。
		//
		// Returning an error aborts the whole turn, deliberately. Degrading silently would have
		// the model suddenly claim the key is unconfigured and ask the user for it moments after
		// they configured it, and that contradiction is far harder to diagnose than an explicit
		// failure. A missing directory never reaches here; List already reports it as "none".
		return err
	}
	if len(list) == 0 {
		return nil
	}

	var sb strings.Builder
	for _, in := range list {
		fmt.Fprintf(&sb, "- %s：%s\n", in.Name, in.Path)
	}
	listing := strings.TrimRight(sb.String(), "\n")
	snap.Instruction += fmt.Sprintf(secretsNotice, listing, listing)
	return nil
}
