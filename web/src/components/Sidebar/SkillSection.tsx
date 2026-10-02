import { useEffect, useState } from 'react'

interface Skill {
  name: string
  description: string
  body: string
  context_mode: '' | 'fork' | 'fork_with_context'
  agent: string
  model: string
  enabled: boolean
  source: 'db' | 'disk'
  path?: string
}

const modeLabel: Record<Skill['context_mode'], string> = {
  '': 'inline（在当前对话展开）',
  fork: 'fork（独立上下文执行）',
  fork_with_context: 'fork_with_context（带当前上下文独立执行）',
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { message?: string } | null
    throw new Error(body?.message ?? `${res.status} ${res.statusText}`)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

const emptySkill: Skill = {
  name: '',
  description: '',
  body: '',
  context_mode: '',
  agent: '',
  model: '',
  enabled: true,
  source: 'db',
}

export function SkillSection() {
  const [list, setList] = useState<Skill[]>([])
  const [editing, setEditing] = useState<Skill | null>(null)
  const [error, setError] = useState<string | null>(null)

  const reload = () => {
    req<{ skills: Skill[] }>('/api/skills')
      .then((r) => setList(r.skills))
      .catch((e: Error) => setError(e.message))
  }
  useEffect(reload, [])

  const save = async () => {
    if (!editing) return
    try {
      await req('/api/skills', { method: 'POST', body: JSON.stringify(editing) })
      setEditing(null)
      setError(null)
      reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <div className="space-y-2">
      {error && <p className="break-all text-red-400">{error}</p>}
      <div className="flex gap-3">
        <button type="button" onClick={() => setEditing({ ...emptySkill })} className="text-blue-400">
          ＋ 新建技能
        </button>
        <button
          type="button"
          onClick={() => void req('/api/skills/reload', { method: 'POST' }).then(reload)}
          className="text-zinc-400 hover:text-white"
        >
          重新扫描目录
        </button>
      </div>

      {list.length === 0 && <p className="text-zinc-600">还没有技能</p>}
      {list.map((sk) => (
        <div key={sk.name} className="rounded-md bg-surface px-2 py-2">
          <div className="flex items-center gap-2">
            <span className="flex-1 truncate text-zinc-300">{sk.name}</span>
            <span className="text-[10px] text-zinc-600">{sk.source === 'disk' ? '目录' : '页面'}</span>
            {sk.source === 'db' && (
              <>
                <button type="button" onClick={() => setEditing(sk)} className="hover:text-white">
                  编辑
                </button>
                <button
                  type="button"
                  onClick={() =>
                    void req(`/api/skills/${encodeURIComponent(sk.name)}`, {
                      method: 'DELETE',
                    }).then(reload)
                  }
                  className="hover:text-red-400"
                >
                  删除
                </button>
              </>
            )}
          </div>
          <p className="mt-0.5 truncate text-[11px] text-zinc-600">
            {modeLabel[sk.context_mode]} · {sk.description || '无描述'}
          </p>
        </div>
      ))}

      {editing && (
        <div className="space-y-1 rounded-md bg-surface p-2">
          <label className="block">
            <span className="text-[11px] text-zinc-600">名称</span>
            <input
              value={editing.name}
              onChange={(e) => setEditing({ ...editing, name: e.target.value })}
              className="mt-0.5 w-full rounded border border-edge bg-panel px-2 py-1 text-xs text-zinc-200 outline-none focus:border-zinc-500"
            />
          </label>
          <label className="block">
            <span className="text-[11px] text-zinc-600">描述（模型据此决定何时用它）</span>
            <input
              value={editing.description}
              onChange={(e) => setEditing({ ...editing, description: e.target.value })}
              className="mt-0.5 w-full rounded border border-edge bg-panel px-2 py-1 text-xs text-zinc-200 outline-none focus:border-zinc-500"
            />
          </label>
          <label className="block">
            <span className="text-[11px] text-zinc-600">执行模式</span>
            <select
              value={editing.context_mode}
              onChange={(e) =>
                setEditing({ ...editing, context_mode: e.target.value as Skill['context_mode'] })
              }
              className="mt-0.5 w-full rounded border border-edge bg-panel px-2 py-1 text-xs text-zinc-200 outline-none focus:border-zinc-500"
            >
              <option value="">{modeLabel['']}</option>
              <option value="fork">{modeLabel.fork}</option>
              <option value="fork_with_context">{modeLabel.fork_with_context}</option>
            </select>
          </label>
          <label className="block">
            <span className="text-[11px] text-zinc-600">正文（Markdown）</span>
            <textarea
              value={editing.body}
              rows={6}
              onChange={(e) => setEditing({ ...editing, body: e.target.value })}
              className="mt-0.5 w-full resize-y rounded border border-edge bg-panel px-2 py-1 font-mono text-[11px] text-zinc-200 outline-none focus:border-zinc-500"
            />
          </label>
          <div className="flex gap-2 pt-1">
            <button type="button" onClick={() => void save()} className="text-blue-400">
              保存
            </button>
            <button type="button" onClick={() => setEditing(null)} className="text-zinc-500">
              取消
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
