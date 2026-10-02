import { create } from 'zustand'
import { api, streamChat } from '@/api/client'
import type { Session, StoredMessage } from '@/api/types'

/**
 * 界面上的一条消息。它比后端的 StoredMessage 多一些只在前端存在的状态：
 * 流式生成中的临时内容、工具调用的进行态。
 *
 * One message as rendered. It carries state that exists only on the frontend beyond the
 * backend's StoredMessage: content still being streamed, and in-flight tool calls.
 */
export interface ChatMessage {
  key: string
  role: 'user' | 'assistant' | 'tool' | 'system'
  content: string
  reasoning?: string
  toolCalls?: { id: string; name: string; arguments: string }[]
  toolCallId?: string
  toolName?: string
  streaming?: boolean
}

interface ChatState {
  sessions: Session[]
  currentId: string | null
  messages: ChatMessage[]
  sending: boolean
  notice: string | null
  error: string | null

  loadSessions: () => Promise<void>
  selectSession: (id: string) => Promise<void>
  newSession: () => Promise<void>
  renameSession: (id: string, title: string) => Promise<void>
  deleteSession: (id: string) => Promise<void>
  send: (text: string) => Promise<void>
  interrupt: () => Promise<void>
  dismissError: () => void
}

/** 当前对话的中止控制器，不进 store —— 它不是渲染依赖。
 *  Abort controller of the active turn. It stays out of the store because nothing renders it. */
let abort: AbortController | null = null

function toChatMessage(m: StoredMessage): ChatMessage {
  return {
    key: `db-${m.id}`,
    role: m.role as ChatMessage['role'],
    content: m.content,
    toolCalls: m.tool_calls?.map((tc) => ({
      id: tc.id,
      name: tc.function.name,
      arguments: tc.function.arguments,
    })),
    toolCallId: m.tool_call_id,
    toolName: m.tool_name,
  }
}

export const useChat = create<ChatState>((set, get) => ({
  sessions: [],
  currentId: null,
  messages: [],
  sending: false,
  notice: null,
  error: null,

  dismissError: () => set({ error: null }),

  loadSessions: async () => {
    try {
      const sessions = await api.listSessions()
      set({ sessions })
      if (!get().currentId && sessions.length > 0) {
        await get().selectSession(sessions[0].id)
      }
    } catch (e) {
      set({ error: (e as Error).message })
    }
  },

  selectSession: async (id) => {
    set({ currentId: id, messages: [], notice: null })
    try {
      const messages = await api.listMessages(id)
      // 切换过程中用户可能又点了别的会话，这时旧请求的结果必须丢弃，
      // 否则会把 A 会话的消息渲染到 B 会话里。
      // The user may have picked another session meanwhile; the stale response must be dropped,
      // otherwise session A's messages would render inside session B.
      if (get().currentId !== id) return
      set({ messages: messages.map(toChatMessage) })
    } catch (e) {
      set({ error: (e as Error).message })
    }
  },

  newSession: async () => {
    try {
      const s = await api.createSession()
      set((st) => ({ sessions: [s, ...st.sessions], currentId: s.id, messages: [], notice: null }))
    } catch (e) {
      set({ error: (e as Error).message })
    }
  },

  renameSession: async (id, title) => {
    try {
      const s = await api.renameSession(id, title)
      set((st) => ({ sessions: st.sessions.map((x) => (x.id === id ? s : x)) }))
    } catch (e) {
      set({ error: (e as Error).message })
    }
  },

  deleteSession: async (id) => {
    try {
      await api.deleteSession(id)
      const rest = get().sessions.filter((s) => s.id !== id)
      set({ sessions: rest })
      if (get().currentId === id) {
        set({ currentId: null, messages: [] })
        if (rest.length > 0) await get().selectSession(rest[0].id)
      }
    } catch (e) {
      set({ error: (e as Error).message })
    }
  },

  send: async (text) => {
    let sessionId = get().currentId
    if (!sessionId) {
      await get().newSession()
      sessionId = get().currentId
      if (!sessionId) return
    }

    const turn = Date.now()
    set((st) => ({
      sending: true,
      notice: null,
      error: null,
      messages: [
        ...st.messages,
        { key: `u-${turn}`, role: 'user', content: text },
        { key: `a-${turn}`, role: 'assistant', content: '', streaming: true },
      ],
    }))

    abort = new AbortController()
    const assistantKey = `a-${turn}`

    const patchAssistant = (fn: (m: ChatMessage) => ChatMessage) =>
      set((st) => ({ messages: st.messages.map((m) => (m.key === assistantKey ? fn(m) : m)) }))

    try {
      for await (const ev of streamChat(sessionId, text, abort.signal)) {
        switch (ev.event) {
          case 'message_delta':
            patchAssistant((m) => ({
              ...m,
              content: m.content + ev.data.content,
              reasoning: ev.data.reasoning ? (m.reasoning ?? '') + ev.data.reasoning : m.reasoning,
            }))
            break
          case 'tool_call':
            patchAssistant((m) => ({ ...m, toolCalls: [...(m.toolCalls ?? []), ev.data] }))
            break
          case 'tool_result':
            set((st) => ({
              messages: [
                ...st.messages,
                {
                  key: `t-${ev.data.id}`,
                  role: 'tool',
                  content: ev.data.content,
                  toolCallId: ev.data.id,
                  toolName: ev.data.name,
                },
              ],
            }))
            break
          case 'compression':
            set({ notice: ev.data.message })
            break
          case 'error':
            set({ error: ev.data.message })
            break
          case 'done':
            if (ev.data.interrupted) set({ notice: '本轮已中断 / this turn was interrupted' })
            break
        }
      }
    } catch (e) {
      if ((e as Error).name !== 'AbortError') set({ error: (e as Error).message })
    } finally {
      abort = null
      patchAssistant((m) => ({ ...m, streaming: false }))
      set({ sending: false })
      // 标题是后端异步生成的，本轮结束时刷一次列表把新标题取回来。
      // Titles are generated asynchronously on the backend; refreshing the list at the end of
      // the turn pulls the new one in.
      void get().loadSessions()
    }
  },

  interrupt: async () => {
    const id = get().currentId
    if (!id) return
    // 先让后端取消整条调用链，再中止本地这条 fetch。顺序反过来的话，
    // 浏览器断开连接了，服务端那轮还在继续跑并继续计费。
    // Ask the backend to cancel the whole call chain first, then abort the local fetch. The
    // other order disconnects the browser while the server-side turn keeps running, and billing.
    try {
      await api.interrupt(id)
    } catch (e) {
      set({ error: (e as Error).message })
    }
    abort?.abort()
  },
}))
