package cli

import "strings"

// 判据的三个阈值。它们共同决定"这是一段被转义坏掉的正文"而不是"这是一段代码"。
//
// The three thresholds of the heuristic. Together they separate "a chunk of text whose escaping
// is broken" from "a chunk of code".
const (
	// inlineTextMinLen 是引号段长度下限，只用来排除零碎的短参数。
	//
	// 它刻意定得不高。实测那段真实正文只有 139 个字符却塞了 10 处 \n——中文密度高，
	// 按英文的直觉去估长度会直接漏判。真正的判别信号是下面的 \n 次数，长度只是辅助。
	//
	// inlineTextMinLen is the minimum length of a quoted segment, used only to rule out short
	// incidental arguments.
	//
	// It is deliberately modest. The real body text measured just 139 characters yet packed in
	// ten occurrences of \n — Chinese is dense, and sizing this threshold by English intuition
	// misses the case outright. The actual signal is the escape count below; length merely
	// supports it.
	inlineTextMinLen = 80

	// inlineTextMinEscapes 是段内字面量 \n 的出现次数下限。
	// 一两处还可能是有意为之，三处以上只可能是在用 \n 分隔多行正文。
	// inlineTextMinEscapes is the minimum count of literal \n occurrences within the segment.
	// One or two may be deliberate; three or more only happen when \n is being used to separate
	// lines of body text.
	inlineTextMinEscapes = 3
)

// textTools 是把 \n 当作语义一部分的解释器与文本工具。
//
// 命令里只要出现其中任意一个，整条命令就跳过检查。这是刻意的保守：
// `python3 -c` 的脚本、`awk` 的程序体、`sed` 的替换表达式里，\n 本来就该是字面量，
// 拦下它们是在制造新问题。漏判只是回到现状，误判却会让本来能跑的命令跑不了。
//
// textTools lists interpreters and text utilities for which \n is part of the semantics.
//
// The presence of any of them anywhere in the command skips the check entirely. The bias is
// deliberately conservative: inside a `python3 -c` script, an `awk` program or a `sed`
// expression, a literal \n is exactly what is meant, and blocking those would create a new
// problem. A missed detection merely preserves the status quo, whereas a false positive breaks
// commands that used to work.
var textTools = map[string]bool{
	"awk": true, "bash": true, "echo": true, "gawk": true, "grep": true, "jq": true,
	"node": true, "perl": true, "printf": true, "python": true, "python3": true,
	"rg": true, "ruby": true, "sed": true, "sh": true, "tr": true, "zsh": true,
}

// inlineTextRefusal 检查命令行里是否嵌入了转义已经坏掉的多行正文，
// 命中时返回给模型看的拒绝说明，未命中返回空串。
//
// 这条检查存在的理由是实测结论：把同样的要求写进系统提示词，模型能一字不差复述，
// 实际干活时却一条都不执行——14 轮工具调用里 0 次改用文件。提示词是建议，
// 这里是约束，而只有约束拦得住。
//
// inlineTextRefusal reports whether the command line embeds multi-line body text whose escaping
// has already been destroyed, returning the explanation shown to the model, or "" when clean.
//
// The check exists because of a measured result: given the same guidance in the system prompt,
// the model recited it back verbatim yet followed none of it in practice — zero of fourteen tool
// calls switched to a file. A prompt is a suggestion; this is a constraint, and only the
// constraint actually holds.
func inlineTextRefusal(command string) string {
	quoted, outside := splitQuoting(command)
	if mentionsTextTool(outside) {
		return ""
	}
	for _, seg := range quoted {
		if len([]rune(seg)) < inlineTextMinLen || strings.Count(seg, `\n`) < inlineTextMinEscapes {
			continue
		}
		// JSON 请求体里的 \n 是合法转义，接收方解析后就是真换行，不能拦。
		// curl -d '{"content":"a\nb\nc"}' 是完全正常的写法。
		//
		// Inside a JSON body \n is a valid escape that the receiver decodes into a real newline,
		// so it must not be blocked: curl -d '{"content":"a\nb\nc"}' is perfectly ordinary.
		if looksLikeJSON(seg) {
			continue
		}
		return "[命令未执行 / command not executed]\n\n" +
			"参数里嵌了一段长正文，其中的换行是字面量的反斜杠加 n，不是真正的换行。" +
			"写进命令行的内容要穿过 JSON 参数与 shell 两层转义，这段文本在到达命令之前就已经坏了；" +
			"执行下去只会把 \\n 原样写进目标文件或文档。\n\n" +
			"改法：先用 write_file 把这段正文写成工作区里的文件（例如 body.md），" +
			"再让命令从文件读取。常见形式是 --content @body.md、-f body.md 或 < body.md，" +
			"具体支持哪一种，先看这条命令的 --help。\n\n" +
			"An argument embeds a long body of text whose newlines are a literal backslash-n " +
			"rather than real newlines. Inlined content must survive two layers of escaping, " +
			"JSON arguments then shell, and this text was already corrupted before reaching the " +
			"command; running it would write \\n verbatim into the target.\n\n" +
			"Instead: write the body to a file in the workspace with write_file (say body.md), " +
			"then have the command read it. --content @body.md, -f body.md and < body.md are the " +
			"common forms; check this command's --help for which one it supports."
	}
	return ""
}

// escapedNewlineRefusal 检查待写入文件的内容是否整篇都在用字面量 \n 代替换行，
// 命中时返回拒绝说明，未命中返回空串。
//
// 光拦住 execute 是不够的。实测拦下命令行之后，模型确实改用了 write_file 加 @文件，
// 但把同样坏掉的 \n 原样写进了文件——问题只是从命令行搬到了文件里，产出一样坏。
// 真正的源头在模型生成 JSON 工具参数时多转义了一层，两个出口都得堵。
//
// 判据刻意定得很硬：整篇一个真换行都没有，却有三处以上字面量 \n。
// 任何真实的多行文件都不满足前半条，所以误伤的空间极小。
//
// escapedNewlineRefusal reports whether file content uses literal \n throughout in place of
// real newlines, returning the explanation shown to the model, or "" when clean.
//
// Guarding execute alone is not enough. Once the command line was blocked, the model did switch
// to write_file plus @file — and wrote the very same broken \n into that file, relocating the
// problem rather than fixing it and producing identically bad output. The real source is one
// extra layer of escaping when the model generates JSON tool arguments, so both exits need
// closing.
//
// The test is deliberately strict: not one real newline in the whole payload, yet three or more
// literal \n. No genuine multi-line file satisfies the first half, leaving little room for a
// false positive.
func escapedNewlineRefusal(content string) string {
	if strings.ContainsRune(content, '\n') ||
		strings.Count(content, `\n`) < inlineTextMinEscapes ||
		looksLikeJSON(content) {
		return ""
	}
	return "[文件未写入 / file not written]\n\n" +
		"内容里有多处字面量的反斜杠加 n，却没有一个真正的换行——换行在生成时被多转义了一层，" +
		"这样写进去，文件里留下的会是 \\n 这两个字符本身，而不是分段。\n\n" +
		"请重新生成内容，直接用真正的换行来分段，不要用 \\n 这种记号代替换行。\n\n" +
		"The content contains several literal backslash-n sequences and not a single real " +
		"newline: the line breaks picked up an extra layer of escaping. Written as is, the file " +
		"would contain the two characters \\n rather than actual line breaks.\n\n" +
		"Regenerate the content using real newlines to separate lines; do not substitute a \\n " +
		"notation for them."
}

// looksLikeJSON 判断一段文本是否是 JSON 对象或数组。
// 只看首尾字符就够了：这里要区分的是"结构化请求体"和"文档正文"，前者必然成对包裹。
//
// looksLikeJSON reports whether a segment is a JSON object or array.
// Inspecting only the delimiters suffices: the distinction being drawn is between a structured
// request body and document prose, and the former is always bracketed.
func looksLikeJSON(seg string) bool {
	t := strings.TrimSpace(seg)
	return strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") ||
		strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")
}

// splitQuoting 把命令拆成「引号内的各段」与「引号外的剩余部分」。
//
// 双引号内的反斜杠转义必须原样跳过，否则 \" 会被当成引号结束，后面的切分全错。
// 单引号内反斜杠不具转义含义，不作处理。
//
// splitQuoting separates a command into its quoted segments and the remaining unquoted text.
//
// Backslash escapes inside double quotes must be skipped verbatim, otherwise \" reads as a
// closing quote and every subsequent split is wrong. Inside single quotes a backslash carries no
// escaping meaning and is left alone.
func splitQuoting(command string) (quoted []string, outside string) {
	var seg, out strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '"' && c == '\\' && i+1 < len(command):
			seg.WriteByte(c)
			seg.WriteByte(command[i+1])
			i++
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote != 0 && c == quote:
			quoted = append(quoted, seg.String())
			seg.Reset()
			quote = 0
		case quote != 0:
			seg.WriteByte(c)
		default:
			out.WriteByte(c)
		}
	}
	// 引号未闭合时剩下的内容照样要算作一段，否则缺一个结尾引号就能绕过检查。
	// An unterminated quote still counts as a segment; otherwise a missing closing quote would
	// be enough to bypass the check.
	if seg.Len() > 0 {
		quoted = append(quoted, seg.String())
	}
	return quoted, out.String()
}

// mentionsTextTool 判断引号外的部分是否调用了 textTools 里的工具。
// 只看引号外是为了避免正文里恰好出现 "sed" 这样的词就让整条命令免检。
//
// mentionsTextTool reports whether the unquoted part invokes one of textTools.
// Restricting the scan to unquoted text prevents a body that happens to contain the word "sed"
// from exempting the whole command.
func mentionsTextTool(outside string) bool {
	for _, field := range strings.FieldsFunc(outside, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '|' || r == ';' ||
			r == '&' || r == '(' || r == ')' || r == '<' || r == '>' || r == '='
	}) {
		if i := strings.LastIndexByte(field, '/'); i >= 0 {
			field = field[i+1:]
		}
		if textTools[field] {
			return true
		}
	}
	return false
}
