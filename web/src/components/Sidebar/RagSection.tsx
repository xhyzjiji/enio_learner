import { useEffect, useRef, useState } from 'react'
import clsx from 'clsx'

interface RagDoc {
  id: string
  path: string
  title: string
  status: 'pending' | 'indexed' | 'failed'
  error: string
  chunk_count: number
  indexed_at: number
}

interface RagStatus {
  running: boolean
  total: number
  done: number
  failed: number
  current: string
  last_error: string
}

const statusDot: Record<RagDoc['status'], string> = {
  indexed: 'bg-emerald-500',
  failed: 'bg-red-500',
  pending: 'bg-amber-500',
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init)
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { message?: string } | null
    throw new Error(body?.message ?? `${res.status} ${res.statusText}`)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export function RagSection() {
  const [docs, setDocs] = useState<RagDoc[]>([])
  const [dir, setDir] = useState('')
  const [status, setStatus] = useState<RagStatus | null>(null)
  const [error, setError] = useState<string | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  const reload = () => {
    req<{ documents: RagDoc[]; dir: string }>('/api/rag/documents')
      .then((r) => {
        setDocs(r.documents)
        setDir(r.dir)
      })
      .catch((e: Error) => setError(e.message))
  }
  useEffect(reload, [])

  // 索引在后台跑，只有正在跑的时候才轮询。一直轮询会在空闲时白白产生请求。
  // Indexing runs in the background and is polled only while it is running. Polling
  // unconditionally would generate requests for nothing while idle.
  useEffect(() => {
    if (!status?.running) return
    const timer = setInterval(() => {
      req<RagStatus>('/api/rag/status')
        .then((s) => {
          setStatus(s)
          if (!s.running) reload()
        })
        .catch(() => undefined)
    }, 1000)
    return () => clearInterval(timer)
  }, [status?.running])

  const upload = async (file: File) => {
    const form = new FormData()
    form.append('file', file)
    try {
      await req('/api/rag/documents', { method: 'POST', body: form })
      setError(null)
      reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const reindex = async (force: boolean) => {
    try {
      setStatus(await req<RagStatus>(`/api/rag/reindex?force=${force}`, { method: 'POST' }))
      setError(null)
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <div className="space-y-2">
      {error && <p className="break-all text-red-400">{error}</p>}
      {dir && <p className="break-all text-[11px] text-zinc-600">目录：{dir}</p>}

      <div className="flex flex-wrap gap-3">
        <button type="button" onClick={() => fileInput.current?.click()} className="text-blue-400">
          ＋ 上传文档
        </button>
        <button
          type="button"
          onClick={() => void reindex(false)}
          className="text-zinc-400 hover:text-white"
        >
          增量索引
        </button>
        <button
          type="button"
          onClick={() => void reindex(true)}
          className="text-zinc-400 hover:text-white"
        >
          全量重建
        </button>
      </div>
      <input
        ref={fileInput}
        type="file"
        className="hidden"
        onChange={(e) => {
          const f = e.target.files?.[0]
          if (f) void upload(f)
          e.target.value = ''
        }}
      />

      {status?.running && (
        <p className="text-[11px] text-amber-400">
          索引中 {status.done}/{status.total}
          {status.failed > 0 && ` · 失败 ${status.failed}`}
          {status.current && ` · ${status.current}`}
        </p>
      )}

      {docs.length === 0 && <p className="text-zinc-600">文档库为空</p>}
      {docs.map((d) => (
        <div key={d.id} className="rounded-md bg-surface px-2 py-2">
          <div className="flex items-center gap-2">
            <span className={clsx('h-2 w-2 shrink-0 rounded-full', statusDot[d.status])} />
            <span className="flex-1 truncate text-zinc-300" title={d.path}>
              {d.title}
            </span>
            <span className="text-[10px] text-zinc-600">{d.chunk_count} 段</span>
            <button
              type="button"
              onClick={() => void req(`/api/rag/documents/${d.id}`, { method: 'DELETE' }).then(reload)}
              className="hover:text-red-400"
            >
              删除
            </button>
          </div>
          {d.error && <p className="mt-1 break-all text-[11px] text-red-400">{d.error}</p>}
        </div>
      ))}
    </div>
  )
}
