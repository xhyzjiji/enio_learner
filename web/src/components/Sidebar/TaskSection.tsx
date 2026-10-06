import { useEffect, useState } from "react";

interface Task {
  id: string;
  name: string;
  cron: string;
  prompt: string;
  enabled: boolean;
  keep_context: boolean;
  tool_scope: string[];
  last_run_at: number;
  next_run_at: number;
}

interface Run {
  id: string;
  started_at: number;
  finished_at: number;
  status: string;
  result: string;
  error: string;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as {
      message?: string;
    } | null;
    throw new Error(body?.message ?? `${res.status} ${res.statusText}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

const fmt = (ms: number) =>
  ms > 0 ? new Date(ms).toLocaleString("zh-CN", { hour12: false }) : "—";

const statusColor: Record<string, string> = {
  success: "text-emerald-400",
  failed: "text-red-400",
  running: "text-amber-400",
  missed: "text-zinc-500",
};

const emptyDraft: Task = {
  id: "",
  name: "",
  cron: "@daily",
  prompt: "",
  enabled: true,
  keep_context: false,
  tool_scope: [],
  last_run_at: 0,
  next_run_at: 0,
};

export function TaskSection() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [draft, setDraft] = useState<Task | null>(null);
  const [runsOf, setRunsOf] = useState<string | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [error, setError] = useState<string | null>(null);

  const reload = () => {
    req<{ tasks: Task[] }>("/api/tasks")
      .then((r) => setTasks(r.tasks))
      .catch((e: Error) => setError(e.message));
  };
  useEffect(reload, []);

  const save = async () => {
    if (!draft) return;
    try {
      await req("/api/tasks", { method: "POST", body: JSON.stringify(draft) });
      setDraft(null);
      setError(null);
      reload();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const openRuns = async (id: string) => {
    if (runsOf === id) {
      setRunsOf(null);
      return;
    }
    try {
      const r = await req<{ runs: Run[] }>(`/api/tasks/${id}/runs`);
      setRuns(r.runs);
      setRunsOf(id);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div className="space-y-2">
      {error && <p className="break-all text-red-400">{error}</p>}

      {tasks.length === 0 && !draft && (
        <p className="text-zinc-600">
          还没有定时任务。在对话里说「每天…」我就会自动登记。
        </p>
      )}

      {tasks.map((t) => (
        <div key={t.id} className="rounded-md bg-surface px-2 py-2">
          <div className="flex items-center gap-2">
            <span
              className={`h-1.5 w-1.5 rounded-full ${t.enabled ? "bg-emerald-400" : "bg-zinc-600"}`}
            />
            <span className="flex-1 truncate text-zinc-300">{t.name}</span>
            <button
              type="button"
              onClick={() => void openRuns(t.id)}
              className="hover:text-white"
            >
              记录
            </button>
            <button
              type="button"
              onClick={() =>
                void req(`/api/tasks/${t.id}/run`, { method: "POST" }).catch(
                  (e: Error) => setError(e.message),
                )
              }
              className="hover:text-white"
            >
              运行
            </button>
            <button
              type="button"
              onClick={() => setDraft(t)}
              className="hover:text-white"
            >
              编辑
            </button>
            <button
              type="button"
              onClick={() =>
                void req(`/api/tasks/${t.id}`, { method: "DELETE" }).then(
                  reload,
                )
              }
              className="hover:text-red-400"
            >
              删除
            </button>
          </div>
          <p className="mt-0.5 font-mono text-[10px] text-sky-400">{t.cron}</p>
          <p className="truncate text-[11px] text-zinc-500">{t.prompt}</p>
          <p className="text-[10px] text-zinc-600">下次 {fmt(t.next_run_at)}</p>

          {runsOf === t.id && (
            <div className="mt-1 space-y-1 border-t border-edge pt-1">
              {runs.length === 0 && (
                <p className="text-[10px] text-zinc-600">还没有执行记录。</p>
              )}
              {runs.map((r) => (
                <div key={r.id} className="text-[10px]">
                  <span className={statusColor[r.status] ?? "text-zinc-500"}>
                    {r.status}
                  </span>
                  <span className="ml-2 text-zinc-600">
                    {fmt(r.started_at)}
                  </span>
                  {r.error && (
                    <p className="break-all text-red-400">{r.error}</p>
                  )}
                  {r.result && (
                    <p className="line-clamp-2 text-zinc-500">{r.result}</p>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      ))}

      {draft ? (
        <div className="space-y-1 rounded-md bg-surface px-2 py-2">
          <input
            value={draft.name}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            placeholder="任务名"
            className="w-full rounded border border-edge bg-panel px-2 py-1 text-[11px] text-zinc-200 outline-none focus:border-zinc-500"
          />
          <input
            value={draft.cron}
            onChange={(e) => setDraft({ ...draft, cron: e.target.value })}
            placeholder="@daily 或 0 9 * * *"
            className="w-full rounded border border-edge bg-panel px-2 py-1 font-mono text-[11px] text-zinc-200 outline-none focus:border-zinc-500"
          />
          <textarea
            value={draft.prompt}
            rows={3}
            onChange={(e) => setDraft({ ...draft, prompt: e.target.value })}
            placeholder="到点后执行的指令，写成不依赖上下文的完整句子"
            className="w-full resize-y rounded border border-edge bg-panel px-2 py-1 text-[11px] text-zinc-200 outline-none focus:border-zinc-500"
          />
          <label className="flex items-center gap-2 text-[11px] text-zinc-500">
            <input
              type="checkbox"
              checked={draft.enabled}
              onChange={(e) =>
                setDraft({ ...draft, enabled: e.target.checked })
              }
            />
            启用
          </label>
          <label className="flex items-center gap-2 text-[11px] text-zinc-500">
            <input
              type="checkbox"
              checked={draft.keep_context}
              onChange={(e) =>
                setDraft({ ...draft, keep_context: e.target.checked })
              }
            />
            每次执行沿用同一会话
          </label>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => void save()}
              className="text-blue-400"
            >
              保存
            </button>
            <button
              type="button"
              onClick={() => setDraft(null)}
              className="text-zinc-500"
            >
              取消
            </button>
          </div>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setDraft(emptyDraft)}
          className="w-full rounded-md border border-dashed border-edge py-1 text-zinc-500 hover:text-zinc-300"
        >
          + 新建定时任务
        </button>
      )}
    </div>
  );
}
