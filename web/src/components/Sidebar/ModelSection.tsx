import { useEffect, useState } from "react";
import type { RuntimeConfig } from "../../api/types";

interface ConfigResponse {
  runtime: RuntimeConfig;
  api_key_present: boolean;
  base_url: string;
  is_local: boolean;
}

/** 数值限额项的展示定义。hint 解释「调它会发生什么」，而不是复述字段名。 */
/** Display definition of a numeric limit. The hint explains what changing it does, rather than
 *  restating the field name. */
const numericFields: {
  key: keyof RuntimeConfig;
  label: string;
  hint: string;
}[] = [
  {
    key: "max_tokens_for_clear",
    label: "工具结果卸载阈值",
    hint: "上下文超过这么多 token 后，把旧工具结果挪到文件里。可逆，不丢信息。",
  },
  {
    key: "summarize_tokens",
    label: "历史摘要阈值",
    hint: "必须大于上一项：先做可逆的卸载，再做有损的摘要。",
  },
  {
    key: "max_length_for_trunc",
    label: "单条结果截断长度",
    hint: "单个工具返回超过这么多字符就卸载到文件。",
  },
  {
    key: "max_tool_result_bytes",
    label: "工具结果字节上限",
    hint: "硬上限，超出直接截断并标注。",
  },
  {
    key: "retrieval_top_k",
    label: "RAG 返回片段数",
    hint: "混合检索最终给模型看几段。",
  },
  {
    key: "memory_inject_limit",
    label: "记忆全量注入上限",
    hint: "低于它就整批塞进提示词且不挂 recall 工具；超过则改为按需检索。",
  },
  {
    key: "exec_timeout_sec",
    label: "命令执行超时（秒）",
    hint: "自由执行工具单条命令的时限，上限 600。下载、安装这类操作需要调大。",
  },
  {
    key: "max_scheduled_tasks",
    label: "定时任务数上限",
    hint: "防止 Agent 登记自我繁殖的任务。",
  },
  {
    key: "min_schedule_interval_sec",
    label: "任务最小间隔（秒）",
    hint: "挡住「每秒执行一次」这类表达式。",
  },
];

export function ModelSection() {
  const [cfg, setCfg] = useState<RuntimeConfig | null>(null);
  const [meta, setMeta] = useState<Omit<ConfigResponse, "runtime"> | null>(
    null,
  );
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const load = () => {
    fetch("/api/config")
      .then((r) => r.json() as Promise<ConfigResponse>)
      .then(({ runtime, ...rest }) => {
        setCfg(runtime);
        setMeta(rest);
        setDirty(false);
      })
      .catch((e: Error) => setError(e.message));
  };
  useEffect(load, []);

  const patch = (next: Partial<RuntimeConfig>) => {
    setCfg((c) => (c ? { ...c, ...next } : c));
    setDirty(true);
    setSaved(false);
  };

  const save = async () => {
    if (!cfg) return;
    setSaving(true);
    setError(null);
    try {
      const res = await fetch("/api/config", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(cfg),
      });
      const body = (await res.json()) as {
        message?: string;
        runtime?: RuntimeConfig;
      };
      if (!res.ok) {
        // 后端的校验信息已经写明了哪一项为什么不合法，原样透出比自造一句
        // 「保存失败」有用得多。
        // The backend's validation message already states which field is wrong and why; passing
        // it through beats inventing a generic "save failed".
        throw new Error(body.message ?? `${res.status} ${res.statusText}`);
      }
      if (body.runtime) setCfg(body.runtime);
      setDirty(false);
      setSaved(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  if (error && !cfg) return <p className="break-all text-red-400">{error}</p>;
  if (!cfg || !meta) return <p className="text-zinc-600">加载中…</p>;

  return (
    <div className="space-y-3">
      <div className="rounded-md bg-surface px-2 py-2">
        <div className="flex items-center gap-2">
          <span className="text-zinc-600">服务端点</span>
          <span
            className={`rounded px-1 text-[10px] ${
              meta.is_local
                ? "bg-emerald-500/15 text-emerald-400"
                : "bg-sky-500/15 text-sky-400"
            }`}
          >
            {meta.is_local ? "本地" : "远程"}
          </span>
        </div>
        <p className="mt-0.5 break-all font-mono text-[10px] text-zinc-400">
          {meta.base_url}
        </p>
        <p className="mt-1 text-[10px] text-zinc-600">
          由环境变量 AGENT_BASE_URL 决定，页面不可改。
          {meta.is_local
            ? " 本地端点无需密钥。"
            : meta.api_key_present
              ? " 密钥已配置。"
              : " ⚠ 未检测到 ZHIPUAI_API_KEY，对话会失败。"}
        </p>
      </div>

      <Field
        label="对话模型"
        value={cfg.model_name}
        onChange={(v) => patch({ model_name: v })}
        hint="Ollama 填 ollama list 里的名字，如 qwen3:14b"
      />
      <Field
        label="标题模型"
        value={cfg.title_model_name}
        onChange={(v) => patch({ title_model_name: v })}
        hint="只用来给会话起名，可以选更小更便宜的"
      />
      <Field
        label="嵌入模型"
        value={cfg.embedding_model}
        onChange={(v) => patch({ embedding_model: v })}
        hint="改动后必须重建 RAG 索引，维度不同的旧向量无法参与检索"
      />

      <label className="flex items-start gap-2 rounded-md bg-surface px-2 py-2">
        <input
          type="checkbox"
          checked={cfg.enable_execute}
          onChange={(e) => patch({ enable_execute: e.target.checked })}
          className="mt-0.5"
        />
        <span>
          <span className="text-zinc-300">允许自由执行命令</span>
          <span className="mt-0.5 block text-[10px] text-zinc-600">
            打开后模型可执行任意 shell
            命令。定时任务中始终强制关闭，因为无人值守时出了事没法叫停。
          </span>
        </span>
      </label>

      {cfg.enable_execute && (
        <label className="flex items-start gap-2 rounded-md bg-surface px-2 py-2">
          <input
            type="checkbox"
            checked={cfg.require_exec_approval}
            onChange={(e) => patch({ require_exec_approval: e.target.checked })}
            className="mt-0.5"
          />
          <span>
            <span className="text-zinc-300">每条命令执行前需人工确认</span>
            <span className="mt-0.5 block text-[10px] text-zinc-600">
              关掉这个开关，模型在之后的每一轮里都握着一个完整的
              shell，而你只能在结果里看到它跑过什么。
            </span>
          </span>
        </label>
      )}

      <div className="space-y-2">
        {numericFields.map((f) => (
          <div key={f.key}>
            <div className="flex items-center gap-2">
              <span className="flex-1 text-zinc-400">{f.label}</span>
              <input
                type="number"
                value={cfg[f.key] as number}
                onChange={(e) =>
                  patch({
                    [f.key]: Number(e.target.value),
                  } as Partial<RuntimeConfig>)
                }
                className="w-28 rounded border border-edge bg-panel px-2 py-1 text-right text-[11px] text-zinc-200 outline-none focus:border-zinc-500"
              />
            </div>
            <p className="text-[10px] text-zinc-600">{f.hint}</p>
          </div>
        ))}
      </div>

      {error && <p className="break-all text-red-400">{error}</p>}
      {saved && !dirty && (
        <p className="text-emerald-400">已保存，下一轮对话生效。</p>
      )}

      <div className="flex gap-2">
        <button
          type="button"
          disabled={!dirty || saving}
          onClick={() => void save()}
          className="rounded bg-blue-600 px-3 py-1 text-white disabled:bg-zinc-700 disabled:text-zinc-500"
        >
          {saving ? "保存中…" : "保存"}
        </button>
        {dirty && (
          <button
            type="button"
            onClick={load}
            className="text-zinc-500 hover:text-zinc-300"
          >
            放弃修改
          </button>
        )}
      </div>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  hint,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  hint: string;
}) {
  return (
    <div>
      <div className="flex items-center gap-2">
        <span className="w-16 shrink-0 text-zinc-400">{label}</span>
        <input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="flex-1 rounded border border-edge bg-panel px-2 py-1 font-mono text-[11px] text-zinc-200 outline-none focus:border-zinc-500"
        />
      </div>
      <p className="mt-0.5 text-[10px] text-zinc-600">{hint}</p>
    </div>
  );
}
