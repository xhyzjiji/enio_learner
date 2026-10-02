import clsx from 'clsx'
import { useChat } from '@/store/chat'

export function SessionList() {
  const sessions = useChat((s) => s.sessions)
  const currentId = useChat((s) => s.currentId)
  const select = useChat((s) => s.selectSession)
  const create = useChat((s) => s.newSession)
  const remove = useChat((s) => s.deleteSession)

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center justify-between px-3 py-2">
        <span className="text-xs font-medium uppercase tracking-wide text-zinc-500">会话</span>
        <button
          type="button"
          onClick={() => void create()}
          className="rounded-md px-2 py-1 text-sm text-zinc-400 hover:bg-surface hover:text-white"
          title="新建对话"
        >
          ＋
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {sessions.length === 0 && (
          <p className="px-2 py-3 text-xs text-zinc-600">还没有会话，点 ＋ 开始</p>
        )}
        {sessions.map((s) => (
          <div
            key={s.id}
            className={clsx(
              'group flex items-center gap-1 rounded-lg px-2 py-2 text-sm',
              s.id === currentId ? 'bg-surface text-white' : 'text-zinc-400 hover:bg-surface/60',
            )}
          >
            <button
              type="button"
              onClick={() => void select(s.id)}
              className="flex-1 truncate text-left"
              title={s.title}
            >
              {s.title || '新对话'}
            </button>
            <button
              type="button"
              onClick={() => void remove(s.id)}
              className="opacity-0 transition-opacity group-hover:opacity-100 hover:text-red-400"
              title="删除会话"
            >
              ×
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
