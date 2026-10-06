import { useState } from 'react'
import type { ApprovalRequest } from '../../api/types'

/**
 * 命令执行前的确认卡片。
 *
 * 命令原文用等宽字体完整展示且不折行省略——这是你做判断的唯一依据，
 * 截断它等于让你在看不清内容的情况下点同意。
 *
 * Confirmation card shown before a command runs.
 *
 * The command is displayed in full in a monospace font and never elided: it is the sole basis
 * for your decision, and truncating it would mean approving something you cannot fully read.
 */
export function ApprovalCard({
  request,
  onResolve,
}: {
  request: ApprovalRequest
  onResolve: (requestId: string, approved: boolean) => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<'approved' | 'denied' | 'stale' | null>(null)

  const decide = async (approved: boolean) => {
    setBusy(true)
    try {
      await onResolve(request.id, approved)
      setDone(approved ? 'approved' : 'denied')
    } catch {
      setDone('stale')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="self-start w-full max-w-[85%] rounded-xl border border-amber-500/40 bg-amber-500/5 px-4 py-3">
      <div className="mb-2 flex items-center gap-2 text-xs text-amber-400">
        <span>⚠</span>
        <span>Agent 请求执行命令</span>
      </div>

      <pre className="mb-3 overflow-x-auto whitespace-pre-wrap break-all rounded-md bg-zinc-900 px-3 py-2 font-mono text-[12px] leading-relaxed text-zinc-200">
        {request.command}
      </pre>

      {done === null ? (
        <div className="flex items-center gap-2">
          <button
            type="button"
            disabled={busy}
            onClick={() => void decide(true)}
            className="rounded bg-amber-600 px-3 py-1 text-xs text-white disabled:opacity-50"
          >
            允许执行
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => void decide(false)}
            className="rounded border border-edge px-3 py-1 text-xs text-zinc-400 hover:text-white disabled:opacity-50"
          >
            拒绝
          </button>
          <span className="text-[10px] text-zinc-600">5 分钟内不回应将按拒绝处理</span>
        </div>
      ) : (
        <p className="text-xs text-zinc-500">
          {done === 'approved' && '已允许，命令执行中…'}
          {done === 'denied' && '已拒绝，命令未执行。'}
          {done === 'stale' && '该请求已失效（等待超时），命令未执行。'}
        </p>
      )}
    </div>
  )
}
