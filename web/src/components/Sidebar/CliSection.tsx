import { useEffect, useState } from 'react'
import { toolsApi, type CliToolDef } from '@/api/tools'

const emptyForm = {
  name: '',
  description: '',
  command: '',
  args_template: '',
  params: '',
  timeout_sec: 30,
}

export function CliSection() {
  const [tools, setTools] = useState<CliToolDef[]>([])
  const [adding, setAdding] = useState(false)
  const [form, setForm] = useState(emptyForm)
  const [error, setError] = useState<string | null>(null)

  const reload = () => {
    toolsApi
      .listCli()
      .then(setTools)
      .catch((e: Error) => setError(e.message))
  }
  useEffect(reload, [])

  const submit = async () => {
    try {
      // 参数模板一行一个，比让用户手写 JSON 数组省事得多，也避免了逗号和引号的错误。
      // One argument per line is far easier than making the user hand-write a JSON array, and it
      // sidesteps comma and quoting mistakes.
      const args = form.args_template
        .split('\n')
        .map((l) => l.trim())
        .filter(Boolean)
      await toolsApi.saveCli({
        name: form.name,
        description: form.description,
        command: form.command,
        args_template: args,
        params: form.params ? JSON.parse(form.params) : {},
        timeout_sec: Number(form.timeout_sec) || 30,
        enabled: true,
        work_dir: '',
      })
      setForm(emptyForm)
      setAdding(false)
      setError(null)
      reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <div className="space-y-2">
      {error && <p className="break-all text-red-400">{error}</p>}
      {tools.length === 0 && <p className="text-zinc-600">还没有定义本地命令工具</p>}

      {tools.map((t) => (
        <div key={t.id} className="rounded-md bg-surface px-2 py-2">
          <div className="flex items-center gap-2">
            <span className="flex-1 truncate font-mono text-[11px] text-emerald-400">{t.name}</span>
            <button
              type="button"
              onClick={() => void toolsApi.deleteCli(t.id).then(reload)}
              className="hover:text-red-400"
            >
              删除
            </button>
          </div>
          <p className="mt-0.5 break-all font-mono text-[11px] text-zinc-600">
            {t.command} {t.args_template.join(' ')}
          </p>
        </div>
      ))}

      {adding ? (
        <div className="space-y-1 rounded-md bg-surface p-2">
          <Field label="工具名" value={form.name} onChange={(v) => setForm({ ...form, name: v })} />
          <Field
            label="描述"
            value={form.description}
            onChange={(v) => setForm({ ...form, description: v })}
          />
          <Field
            label="命令"
            value={form.command}
            onChange={(v) => setForm({ ...form, command: v })}
            placeholder="rg"
          />
          <Area
            label="参数模板（一行一个）"
            value={form.args_template}
            onChange={(v) => setForm({ ...form, args_template: v })}
            placeholder={'-n\n{{pattern}}\n{{path}}'}
          />
          <Area
            label="参数声明（JSON）"
            value={form.params}
            onChange={(v) => setForm({ ...form, params: v })}
            placeholder={'{"pattern":{"type":"string","description":"搜索词","required":true}}'}
          />
          <p className="text-[11px] text-zinc-600">
            模型只能填模板里的槽位，命令本身不可改，参数也不会经过 shell 解析。
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
          ＋ 添加命令工具
        </button>
      )}
    </div>
  )
}

function Field(p: {
  label: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  return (
    <label className="block">
      <span className="text-[11px] text-zinc-600">{p.label}</span>
      <input
        value={p.value}
        placeholder={p.placeholder}
        onChange={(e) => p.onChange(e.target.value)}
        className="mt-0.5 w-full rounded border border-edge bg-panel px-2 py-1 text-xs text-zinc-200 outline-none focus:border-zinc-500"
      />
    </label>
  )
}

function Area(p: {
  label: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  return (
    <label className="block">
      <span className="text-[11px] text-zinc-600">{p.label}</span>
      <textarea
        value={p.value}
        rows={3}
        placeholder={p.placeholder}
        onChange={(e) => p.onChange(e.target.value)}
        className="mt-0.5 w-full resize-y rounded border border-edge bg-panel px-2 py-1 font-mono text-[11px] text-zinc-200 outline-none focus:border-zinc-500"
      />
    </label>
  )
}
