import { memo, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";

/**
 * 流式输出时这个组件每来一个 token 就要重渲染一次，而 markdown 解析并不便宜。
 * memo 让内容没变的历史消息不参与重渲染——否则一条长回复生成到后半段时，
 * 前面几十条历史消息会跟着一起反复解析。
 *
 * During streaming this component re-renders on every token, and markdown parsing is not cheap.
 * memo keeps unchanged historical messages out of the re-render — otherwise, halfway through a
 * long reply, dozens of earlier messages would be re-parsed on every single token.
 */
export const Markdown = memo(function Markdown({
  content,
}: {
  content: string;
}) {
  return (
    <div className="md">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        // ignoreMissing：模型经常标注 Ollama 内置语法库里没有的语言名，
        // 不忽略的话整个渲染会抛异常，一条回复直接白屏。
        // ignoreMissing: models routinely label code with languages the bundled grammar set does
        // not know; without it the whole render throws and the reply goes blank.
        rehypePlugins={[
          [rehypeHighlight, { ignoreMissing: true, detect: true }],
        ]}
        components={{ pre: CodeBlock }}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
});

/** 代码块外壳，带语言标签与复制按钮。 */
/** Code block shell with a language label and a copy button. */
function CodeBlock({
  children,
  ...props
}: React.HTMLAttributes<HTMLPreElement>) {
  const [copied, setCopied] = useState(false);

  const lang = extractLang(children);
  const copy = (e: React.MouseEvent<HTMLButtonElement>) => {
    const pre = e.currentTarget.parentElement?.querySelector("code");
    if (!pre) return;
    void navigator.clipboard.writeText(pre.textContent ?? "").then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div className="group relative my-3">
      {lang && (
        <span className="absolute left-3 top-2 text-[10px] uppercase tracking-wide text-zinc-600">
          {lang}
        </span>
      )}
      <button
        type="button"
        onClick={copy}
        className="absolute right-2 top-2 rounded bg-zinc-800 px-2 py-0.5 text-[10px] text-zinc-400 opacity-0 transition group-hover:opacity-100 hover:text-white"
      >
        {copied ? "已复制" : "复制"}
      </button>
      <pre {...props}>{children}</pre>
    </div>
  );
}

/** 从 react-markdown 传下来的 code 元素上取出 language-xxx 类名。 */
/** Extracts the language-xxx class from the code element handed down by react-markdown. */
function extractLang(children: React.ReactNode): string | null {
  const child = Array.isArray(children) ? children[0] : children;
  const className =
    (child as { props?: { className?: string } })?.props?.className ?? "";
  const match = /language-([\w+-]+)/.exec(className);
  return match ? match[1] : null;
}
