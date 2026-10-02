package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// titlePrompt 要求模型只输出标题本身。加"不要加引号、不要解释"是因为模型
// 很容易回一句"好的，这段对话的标题是：……"，那样标题栏里就全是废话。
// titlePrompt asks the model to emit only the title. The "no quotes, no explanation" clause is
// there because models readily reply with "Sure, the title for this conversation is: ...",
// which would fill the title bar with noise.
const titlePrompt = `请为下面这段用户提问生成一个不超过 16 个字的简短标题，用于会话列表展示。
只输出标题本身，不要加引号，不要解释，不要加句号。

用户提问：
%s`

// maxTitleRunes 是标题的字符上限，超出后截断。
// maxTitleRunes caps the title length in runes; anything longer is truncated.
const maxTitleRunes = 20

// titleTimeout 限制标题生成的耗时。标题只是锦上添花，不值得为它拖住任何东西。
// titleTimeout bounds title generation. A title is a nicety and is not worth blocking anything.
const titleTimeout = 20 * time.Second

// TitleGenerator 异步生成会话标题。
//
// 之所以异步：标题生成要调一次模型，几百毫秒到数秒不等。如果同步做，用户发完第一条
// 消息后要干等标题出来才能看到回复开始流式输出，体验明显变差。而标题晚几秒出现，
// 用户几乎不会察觉。
//
// TitleGenerator produces conversation titles asynchronously.
//
// Why asynchronous: generating a title costs one model call, from a few hundred milliseconds to
// several seconds. Doing it synchronously would make the user wait for the title before the
// reply starts streaming — a clearly worse experience — whereas a title appearing a few seconds
// late is barely noticeable.
type TitleGenerator struct {
	store  *Store
	model  model.BaseChatModel
	logger *slog.Logger
}

// NewTitleGenerator 构造标题生成器。model 为 nil 时退化为本地截断生成，不调模型。
// NewTitleGenerator builds a title generator. When model is nil it degrades to local
// truncation without any model call.
func NewTitleGenerator(s *Store, m model.BaseChatModel, logger *slog.Logger) *TitleGenerator {
	if logger == nil {
		logger = slog.Default()
	}
	return &TitleGenerator{store: s, model: m, logger: logger}
}

// EnsureAsync 在后台补齐标题。已有标题时直接返回，不重复生成。
//
// 这里刻意不接受调用方的 ctx：调用方的 ctx 通常绑定在 HTTP 请求上，请求一结束就被
// 取消，标题就永远生成不出来了。
//
// EnsureAsync fills in the title in the background, returning immediately if one already exists.
//
// The caller's ctx is deliberately not accepted: it is usually tied to an HTTP request and gets
// cancelled the moment the request ends, which would mean the title never gets generated.
func (g *TitleGenerator) EnsureAsync(sessionID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
		defer cancel()
		if err := g.ensure(ctx, sessionID); err != nil {
			g.logger.Warn("generate session title failed", "session", sessionID, "err", err)
		}
	}()
}

func (g *TitleGenerator) ensure(ctx context.Context, sessionID string) error {
	sess, err := g.store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(sess.Title) != "" {
		return nil
	}

	question, err := g.store.LastUserMessage(ctx, sessionID)
	if err != nil {
		return err
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil
	}

	title := g.generate(ctx, question)
	if title == "" {
		return nil
	}
	return g.store.Rename(ctx, sessionID, title)
}

// generate 优先用模型生成标题，失败则回退到本地截断。
// 模型不可用不应该让会话列表里全是"未命名会话"。
// generate prefers a model-produced title and falls back to local truncation. An unavailable
// model should not leave the conversation list full of "Untitled".
func (g *TitleGenerator) generate(ctx context.Context, question string) string {
	if g.model != nil {
		msg, err := g.model.Generate(ctx, []*schema.Message{
			schema.UserMessage(fmt.Sprintf(titlePrompt, truncateRunes(question, 500))),
		})
		if err == nil && msg != nil {
			if t := cleanTitle(msg.Content); t != "" {
				return t
			}
		}
		if err != nil {
			g.logger.Debug("title model call failed, falling back to truncation", "err", err)
		}
	}
	return cleanTitle(question)
}

// cleanTitle 清掉模型常见的多余包装：引号、前后空白、换行、结尾标点。
// cleanTitle strips the wrappers models commonly add: quotes, surrounding whitespace, newlines
// and trailing punctuation.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, " \t\"'“”『』「」`")
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return r == '。' || r == '.' || r == '！' || r == '!' || r == '？' || r == '?' ||
			unicode.IsSpace(r)
	})
	return truncateRunes(strings.TrimSpace(s), maxTitleRunes)
}

// truncateRunes 按字符（而非字节）截断，避免把一个汉字切成两半。
// truncateRunes truncates by rune rather than byte, so a CJK character is never cut in half.
func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}
