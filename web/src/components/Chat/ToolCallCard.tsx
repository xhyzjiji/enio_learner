import { useState } from "react";

interface Props {
  name: string;
  args?: string;
  result?: string;
}

/**
 * 工具调用卡片。默认折叠：工具的参数和返回往往很长，展开着会把真正的回复挤出视野。
 * Tool call card, collapsed by default: arguments and results are often long enough to push the
 * actual reply out of view when expanded.
 */
export function ToolCallCard({ name, args, result }: Props) {
  const [open, setOpen] = useState(false);
  const body = result ?? args;

  return (
    <div className="my-2 rounded-lg border border-edge bg-panel text-sm">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-zinc-300 hover:text-white"
      >
        <span className="text-zinc-500">{open ? "▾" : "▸"}</span>
        <span className="font-mono text-xs text-emerald-400">{name}</span>
        <span className="text-xs text-zinc-500">
          {result ? "返回 / result" : "调用 / call"}
        </span>
      </button>
      {open && body && (
        <pre className="max-h-72 overflow-auto border-t border-edge px-3 py-2 font-mono text-xs text-zinc-400">
          {body}
        </pre>
      )}
    </div>
  );
}
