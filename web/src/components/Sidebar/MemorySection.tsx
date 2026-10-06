import { useEffect, useState } from "react";

interface Memory {
  key: string;
  content: string;
  category: string;
  created_at: number;
  updated_at: number;
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

export function MemorySection() {
  const [list, setList] = useState<Memory[]>([]);
  const [editKey, setEditKey] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);

  const reload = () => {
    req<{ memories: Memory[] }>("/api/memories")
      .then((r) => setList(r.memories))
      .catch((e: Error) => setError(e.message));
  };
  useEffect(reload, []);

  const save = async (key: string) => {
    try {
      await req(`/api/memories/${encodeURIComponent(key)}`, {
        method: "PUT",
        body: JSON.stringify({ content: draft, category: "" }),
      });
      setEditKey(null);
      setError(null);
      reload();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div className="space-y-2">
      {error && <p className="break-all text-red-400">{error}</p>}
      {list.length === 0 && (
        <p className="text-zinc-600">
          还没有长期记忆。对话中让我记住的事会出现在这里。
        </p>
      )}
      {list.map((m) => (
        <div key={m.key} className="rounded-md bg-surface px-2 py-2">
          <div className="flex items-center gap-2">
            <span className="flex-1 truncate font-mono text-[11px] text-sky-400">
              {m.key}
            </span>
            {m.category && (
              <span className="text-[10px] text-zinc-600">{m.category}</span>
            )}
            <button
              type="button"
              onClick={() => {
                setEditKey(m.key);
                setDraft(m.content);
              }}
              className="hover:text-white"
            >
              编辑
            </button>
            <button
              type="button"
              onClick={() =>
                void req(`/api/memories/${encodeURIComponent(m.key)}`, {
                  method: "DELETE",
                }).then(reload)
              }
              className="hover:text-red-400"
            >
              删除
            </button>
          </div>
          {editKey === m.key ? (
            <div className="mt-1 space-y-1">
              <textarea
                value={draft}
                rows={2}
                onChange={(e) => setDraft(e.target.value)}
                className="w-full resize-y rounded border border-edge bg-panel px-2 py-1 text-[11px] text-zinc-200 outline-none focus:border-zinc-500"
              />
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={() => void save(m.key)}
                  className="text-blue-400"
                >
                  保存
                </button>
                <button
                  type="button"
                  onClick={() => setEditKey(null)}
                  className="text-zinc-500"
                >
                  取消
                </button>
              </div>
            </div>
          ) : (
            <p className="mt-0.5 text-[11px] text-zinc-500">{m.content}</p>
          )}
        </div>
      ))}
    </div>
  );
}
