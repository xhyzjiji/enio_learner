import { useEffect, useState } from 'react'
import clsx from 'clsx'
import { toolsApi, type McpServer } from '@/api/tools'

const healthColor: Record<McpServer['health'], string> = {
  ok: 'bg-emerald-500',
  error: 'bg-red-500',
  unknown: 'bg-zinc-600',
}

export function McpSection() {
  const [servers, setServers] = useState<McpServer[]>([])
  const [adding, setAdding] = useState(false)
  const [form, setForm] = useState({ name: '', endpoint: '', secret_ref: '' })
  const [error, setError] = useState<string | null>(null)
  const [probe, setProbe] = useState<Record<string, string>>({})

  const reload = () => {
    toolsApi
      .listMcp()
      .then(setServers)
      .catch((e: Error) => setError(e.message))
  }
  useEffect(reload, [])

  const submit = async () => {
    try {
      await toolsApi.createMcp({ ...form, enabled: true })
      setForm({ name: '', endpoint: '', secret_ref: '' })
      setAdding(false)
      setError(null)
      reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const test = async (s: McpServer) => {
    setProbe((p) => ({ ...p, [s.id]: '测试中…' }))
    try {
      const r = await toolsApi.testMcp(s.id)
      setProbe((p) => ({
        ...p,
        [s.id]: r.ok ? `${r.tools?.length ?? 0} 个工具：${r.tools?.join(', ')}` : (r.error ?? '失败'),
      }))
    } catch (e) {
      setProbe((p) => ({ ...p, [s.id]: (e as Error).message }))
    }
    reload()
  }

  const toggle = async (s: McpServer) => {
    try {
      await toolsApi.updateMcp(s.id, { ...s, enabled: !s.enabled })
      reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <div className="space-y-2">
      {error && <p className="text-red-400">{error}</p>}
      {servers.length === 0 && <p className="text-zinc-600">还没有配置 MCP 服务</p>}

      {servers.map((s) => (
        <div key={s.id} className="rounded-md bg-surface px-2 py-2">
          <div className="flex items-center gap-2">
            <span className={clsx('h-2 w-2 shrink-0 rounded-full', healthColor[s.health])} />
            <span className="flex-1 truncate text-zinc-300">{s.name}</span>
            <button type="button" onClick={() => void test(s)} className="hover:text-white">
              测试
            </button>
            <button type="button" onClick={() => void toggle(s)} className="hover:text-white">
              {s.enabled ? '停用' : '启用'}
            </button>
            <button
              type="button"
              onClick={() => void toolsApi.deleteMcp(s.id).then(reload)}
              className="hover:text-red-400"
            >
              删除
            </button>
          </div>
          {(probe[s.id] || s.last_error) && (
            <p className="mt-1 break-all text-[11px] text-zinc-500">{probe[s.id] || s.last_error}</p>
          )}
        </div>
      ))}

      {adding ? (
        <div className="space-y-1 rounded-md bg-surface p-2">
          <Field label="名称" value={form.name} onChange={(v) => setForm({ ...form, name: v })} />
          <Field
            label="端点"
            value={form.endpoint}
            onChange={(v) => setForm({ ...form, endpoint: v })}
            placeholder="https://…/mcp"
          />
          <Field
            label="密钥环境变量名"
            value={form.secret_ref}
            onChange={(v) => setForm({ ...form, secret_ref: v })}
            placeholder="MY_MCP_TOKEN"
          />
          <p className="text-[11px] text-zinc-600">
            这里填环境变量的名字，不是密钥本身。密钥不会写入数据库。
          </p>
          <div className="flex gap-2 pt-1">
            <button type="button" onClick={() => void submit()} className="text-blue-400">
              保存
            </button>
            <button type="button" onClick={() => setAdding(false)} className="text-zinc-500">
              取消
            </button>
          </div>
        </div>
      ) : (
        <button type="button" onClick={() => setAdding(true)} className="text-blue-400">
          ＋ 添加服务
        </button>
      )}
    </div>
  )
}

function Field({
  label,
  value,
  onChange,
  placeholder,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  return (
    <label className="block">
      <span className="text-[11px] text-zinc-600">{label}</span>
      <input
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="mt-0.5 w-full rounded border border-edge bg-panel px-2 py-1 text-xs text-zinc-200 outline-none focus:border-zinc-500"
      />
    </label>
  )
}
