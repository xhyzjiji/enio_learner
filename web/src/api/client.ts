import type { ChatEvent, RuntimeConfig, Session, StoredMessage } from './types'

/**
 * 后端返回的错误体。message 是面向用户的，直接展示即可。
 * The backend's error body. message is user-facing and can be displayed as is.
 */
interface ErrorBody {
  error: string
  message: string
  field?: string
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as ErrorBody | null
    throw new Error(body?.message ?? `${res.status} ${res.statusText}`)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const api = {
  listSessions: (archived = false) =>
    request<{ sessions: Session[] }>(`/api/sessions?archived=${archived}`).then((r) => r.sessions),

  createSession: (title?: string) =>
    request<Session>('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ title: title ?? '' }),
    }),

  renameSession: (id: string, title: string) =>
    request<Session>(`/api/sessions/${id}`, { method: 'PATCH', body: JSON.stringify({ title }) }),

  setArchived: (id: string, archived: boolean) =>
    request<Session>(`/api/sessions/${id}`, { method: 'PATCH', body: JSON.stringify({ archived }) }),

  deleteSession: (id: string) => request<void>(`/api/sessions/${id}`, { method: 'DELETE' }),

  listMessages: (id: string) =>
    request<{ messages: StoredMessage[] }>(`/api/sessions/${id}/messages`).then((r) => r.messages),

  getConfig: () => request<{ runtime: RuntimeConfig; api_key_present: boolean }>('/api/config'),

  updateConfig: (runtime: RuntimeConfig) =>
    request<{ runtime: RuntimeConfig }>('/api/config', {
      method: 'PUT',
      body: JSON.stringify(runtime),
    }),

  approve: (requestId: string, approved: boolean) =>
    request<{ resolved: boolean }>('/api/chat/approve', {
      method: 'POST',
      body: JSON.stringify({ request_id: requestId, approved, reason: '' }),
    }),

  interrupt: (sessionId: string) =>
    request<{ interrupted: boolean }>('/api/chat/interrupt', {
      method: 'POST',
      body: JSON.stringify({ session_id: sessionId }),
    }),
}

/**
 * 发起一轮对话并逐个产出 SSE 事件。
 *
 * 这里用 fetch + ReadableStream 而不是 EventSource：EventSource 只能发 GET，
 * 没法携带请求体，把整条用户消息塞进查询串既有长度上限也会被记进各层访问日志。
 *
 * Starts one conversation turn and yields SSE events one by one.
 *
 * It uses fetch + ReadableStream rather than EventSource, because EventSource can only issue GET
 * requests and carries no body; squeezing a whole user message into the query string runs into
 * length limits and lands the text in every access log along the way.
 */
export async function* streamChat(
  sessionId: string,
  message: string,
  signal?: AbortSignal,
): AsyncGenerator<ChatEvent> {
  const res = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session_id: sessionId, message }),
    signal,
  })
  if (!res.ok || !res.body) {
    const body = (await res.json().catch(() => null)) as ErrorBody | null
    throw new Error(body?.message ?? `${res.status} ${res.statusText}`)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    // SSE 以空行分隔事件。网络分片可能把一个事件切成两半，所以只消费完整的
    // 事件块，残留部分留在 buffer 里等下一片数据。
    // SSE separates events with a blank line. Network chunking can split one event in half, so
    // only complete blocks are consumed and the remainder stays in buffer for the next chunk.
    let sep = buffer.indexOf('\n\n')
    while (sep !== -1) {
      const block = buffer.slice(0, sep)
      buffer = buffer.slice(sep + 2)
      const parsed = parseBlock(block)
      if (parsed) yield parsed
      sep = buffer.indexOf('\n\n')
    }
  }
}

function parseBlock(block: string): ChatEvent | null {
  let event = ''
  const dataLines: string[] = []
  for (const line of block.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
  }
  if (!event || dataLines.length === 0) return null
  try {
    return { event, data: JSON.parse(dataLines.join('\n')) } as ChatEvent
  } catch {
    return null
  }
}
