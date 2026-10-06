// 与 Go 侧结构体一一对应的类型定义。
// Type definitions mirroring the Go-side structs one for one.

export interface Session {
  id: string
  title: string
  created_at: number
  updated_at: number
  archived: boolean
  token_usage: number
  compact_count: number
}

/** 与 eino schema.ToolCall 的 JSON 形状一致，注意参数嵌在 function 里。
 *  Matches the JSON shape of eino's schema.ToolCall; note that arguments nest under function. */
export interface StoredToolCall {
  id: string
  type?: string
  function: { name: string; arguments: string }
}

/** 界面渲染用的扁平工具调用。
 *  Flattened tool call for rendering. */
export interface ToolCall {
  id: string
  name: string
  arguments: string
}

export interface StoredMessage {
  id: string
  session_id: string
  seq: number
  role: string
  content: string
  tool_calls?: StoredToolCall[]
  tool_call_id?: string
  tool_name?: string
  created_at: number
}

export interface RuntimeConfig {
  model_name: string
  title_model_name: string
  embedding_model: string
  max_tokens_for_clear: number
  max_length_for_trunc: number
  summarize_tokens: number
  memory_inject_limit: number
  enable_execute: boolean
  require_exec_approval: boolean
  max_tool_result_bytes: number
  max_scheduled_tasks: number
  min_schedule_interval_sec: number
  exec_timeout_sec: number
  retrieval_top_k: number
}

// SSE 事件。判别字段是 event，与 Go 侧 httpapi/stream.go 的常量保持一致。
// SSE events. The discriminant is event, matching the constants in Go's httpapi/stream.go.
// ApprovalRequest 是一条待确认的 shell 命令。
// ApprovalRequest is one shell command awaiting confirmation.
export interface ApprovalRequest {
  id: string
  session_id: string
  command: string
  created_at: number
}

export type ChatEvent =
  | { event: 'message_delta'; data: { content: string; reasoning?: string } }
  | { event: 'tool_call'; data: { id: string; name: string; arguments: string } }
  | { event: 'tool_result'; data: { id: string; name: string; content: string } }
  | { event: 'compression'; data: { stage: 'before' | 'after'; message: string } }
  | { event: 'approval_request'; data: ApprovalRequest }
  | { event: 'error'; data: { message: string } }
  | { event: 'done'; data: { session_id: string; interrupted: boolean } }
