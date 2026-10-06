import { useEffect, useState } from "react";
import { createPortal } from "react-dom";

import { Markdown } from "../Chat/Markdown";

/** BinRequirement 是技能声明的一条外部命令依赖。 / BinRequirement is one declared external command dependency. */
export interface BinRequirement {
  name: string;
  available: boolean;
  path?: string;
}

export interface Skill {
  name: string;
  description: string;
  body: string;
  context_mode: "" | "fork" | "fork_with_context";
  agent: string;
  model: string;
  enabled: boolean;
  source: "db" | "disk";
  path?: string;
  display_name?: string;
  version?: string;
  author?: string;
  tags?: string[];
  requires_bins?: BinRequirement[];
  cli_help?: string;
}

export const modeLabel: Record<Skill["context_mode"], string> = {
  "": "inline（在当前对话展开）",
  fork: "fork（独立上下文执行）",
  fork_with_context: "fork_with_context（带当前上下文独立执行）",
};

/** missingBins 返回技能里尚未安装的依赖命令。 / missingBins returns the declared commands that are not installed. */
export function missingBins(sk: Skill): BinRequirement[] {
  return (sk.requires_bins ?? []).filter((b) => !b.available);
}

function Badge({
  children,
  tone = "zinc",
}: {
  children: React.ReactNode;
  tone?: "zinc" | "green" | "red";
}) {
  const tones = {
    zinc: "border-edge text-zinc-500",
    green: "border-emerald-900 text-emerald-400",
    red: "border-red-900 text-red-400",
  };
  return (
    <span className={`rounded border px-1.5 py-0.5 text-[10px] ${tones[tone]}`}>
      {children}
    </span>
  );
}

/**
 * SkillBrowser 是技能的全量浏览弹窗：左侧列出全部技能，右侧展示选中技能的完整内容。
 *
 * 侧栏里只放得下一行截断的描述，而技能的正文才是真正决定模型行为的东西——它被整段
 * 注入提示词。装了一个技能却看不到它往提示词里塞了什么，是这套机制最不该有的盲区，
 * 尤其技能来自第三方时。
 *
 * SkillBrowser is the full-catalog modal: every skill on the left, the selected one in full on
 * the right.
 *
 * The sidebar has room only for a single truncated line, yet a skill's body is what actually
 * steers the model — it is injected into the prompt wholesale. Installing a skill without being
 * able to see what it puts into the prompt is the one blind spot this mechanism cannot afford,
 * all the more so when the skill came from a third party.
 */
export function SkillBrowser({
  skills,
  initial,
  onClose,
}: {
  skills: Skill[];
  initial?: string;
  onClose: () => void;
}) {
  const [selected, setSelected] = useState(initial ?? skills[0]?.name ?? "");
  const current = skills.find((s) => s.name === selected) ?? skills[0];

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-6"
      onClick={onClose}
    >
      <div
        className="flex h-[82vh] w-full max-w-5xl overflow-hidden rounded-lg border border-edge bg-panel shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <aside className="w-60 shrink-0 overflow-y-auto border-r border-edge p-2">
          <div className="mb-2 px-1 text-[11px] text-zinc-600">
            共 {skills.length} 个技能
          </div>
          {skills.map((sk) => {
            const missing = missingBins(sk);
            return (
              <button
                key={sk.name}
                type="button"
                onClick={() => setSelected(sk.name)}
                className={`mb-1 block w-full rounded px-2 py-1.5 text-left text-xs ${
                  sk.name === current?.name
                    ? "bg-surface text-zinc-100"
                    : "text-zinc-400 hover:bg-surface/60"
                }`}
              >
                <span className="flex items-center gap-1">
                  <span className="flex-1 truncate">
                    {sk.display_name || sk.name}
                  </span>
                  {missing.length > 0 && (
                    <span className="text-red-400" title="依赖未安装">
                      ●
                    </span>
                  )}
                </span>
                {sk.display_name && (
                  <span className="mt-0.5 block truncate text-[10px] text-zinc-600">
                    {sk.name}
                  </span>
                )}
              </button>
            );
          })}
        </aside>

        <section className="flex-1 overflow-y-auto p-5">
          {!current && <p className="text-xs text-zinc-600">还没有技能</p>}
          {current && (
            <>
              <div className="flex items-start gap-3">
                <h2 className="flex-1 text-base text-zinc-100">
                  {current.display_name || current.name}
                </h2>
                <button
                  type="button"
                  onClick={onClose}
                  className="text-zinc-600 hover:text-zinc-300"
                >
                  ✕
                </button>
              </div>

              <div className="mt-2 flex flex-wrap items-center gap-1.5">
                <Badge>{current.source === "disk" ? "目录" : "页面"}</Badge>
                <Badge>{modeLabel[current.context_mode]}</Badge>
                {current.version && <Badge>v{current.version}</Badge>}
                {current.author && <Badge>{current.author}</Badge>}
                {(current.tags ?? []).map((t) => (
                  <Badge key={t}>#{t}</Badge>
                ))}
              </div>

              {current.path && (
                <p className="mt-2 break-all font-mono text-[10px] text-zinc-600">
                  {current.path}
                </p>
              )}

              {(current.requires_bins?.length || current.cli_help) && (
                <div className="mt-4 rounded-md border border-edge bg-surface p-3">
                  <h3 className="text-[11px] text-zinc-500">
                    依赖的命令行工具
                  </h3>
                  <div className="mt-2 space-y-1">
                    {(current.requires_bins ?? []).map((b) => (
                      <div
                        key={b.name}
                        className="flex items-center gap-2 text-xs"
                      >
                        <Badge tone={b.available ? "green" : "red"}>
                          {b.available ? "已安装" : "未安装"}
                        </Badge>
                        <span className="font-mono text-zinc-300">
                          {b.name}
                        </span>
                        {b.path && (
                          <span className="truncate font-mono text-[10px] text-zinc-600">
                            {b.path}
                          </span>
                        )}
                      </div>
                    ))}
                  </div>
                  {current.cli_help && (
                    <p className="mt-2 text-[11px] text-zinc-600">
                      自检命令：
                      <code className="ml-1 rounded bg-panel px-1 py-0.5 font-mono text-zinc-400">
                        {current.cli_help}
                      </code>
                    </p>
                  )}
                  {missingBins(current).length > 0 && (
                    <p className="mt-2 text-[11px] text-red-400">
                      缺少上面标红的命令，技能执行时会直接失败。
                    </p>
                  )}
                </div>
              )}

              <div className="mt-4">
                <h3 className="text-[11px] text-zinc-500">
                  描述（模型据此判断何时启用这个技能）
                </h3>
                <p className="mt-1 whitespace-pre-wrap text-xs leading-relaxed text-zinc-400">
                  {current.description || "无描述"}
                </p>
              </div>

              <div className="mt-4">
                <h3 className="text-[11px] text-zinc-500">
                  指令正文（技能被激活时整段注入提示词）
                </h3>
                <div className="mt-2 rounded-md border border-edge bg-surface p-3">
                  {current.body ? (
                    <Markdown content={current.body} />
                  ) : (
                    <p className="text-xs text-zinc-600">正文为空</p>
                  )}
                </div>
              </div>
            </>
          )}
        </section>
      </div>
    </div>,
    document.body,
  );
}
