export interface McpServer {
  id: string
  name: string
  endpoint: string
  secret_ref: string
  enabled: boolean
  health: 'unknown' | 'ok' | 'error'
  last_error: string
  last_checked_at: number
  created_at: number
}

export interface ParamSpec {
  type: string
  description: string
  required: boolean
}

export interface CliToolDef {
  id: string
  name: string
  description: string
  command: string
  args_template: string[]
  params: Record<string, ParamSpec>
  work_dir: string
  timeout_sec: number
  enabled: boolean
  created_at: number
}

export interface PolicyRule {
  id: string
  pattern: string
  action: 'allow' | 'deny'
  note: string
}

export interface ToolEntry {
  name: string
  description: string
  source: 'mcp' | 'cli' | 'builtin'
  renamed: boolean
}

interface ErrorBody {
  message: string
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
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

export const toolsApi = {
  inventory: () => req<{ tools: ToolEntry[] }>('/api/tools').then((r) => r.tools),

  listMcp: () => req<{ servers: McpServer[] }>('/api/tools/mcp').then((r) => r.servers),
  createMcp: (body: Partial<McpServer>) =>
    req<McpServer>('/api/tools/mcp', { method: 'POST', body: JSON.stringify(body) }),
  updateMcp: (id: string, body: Partial<McpServer>) =>
    req<McpServer>(`/api/tools/mcp/${id}`, { method: 'PUT', body: JSON.stringify(body) }),
  deleteMcp: (id: string) => req<void>(`/api/tools/mcp/${id}`, { method: 'DELETE' }),
  testMcp: (id: string) =>
    req<{ ok: boolean; tools?: string[]; error?: string }>(`/api/tools/mcp/${id}/test`, {
      method: 'POST',
    }),

  listCli: () => req<{ tools: CliToolDef[] }>('/api/tools/cli').then((r) => r.tools),
  saveCli: (body: Partial<CliToolDef>) =>
    req<CliToolDef>('/api/tools/cli', { method: 'POST', body: JSON.stringify(body) }),
  deleteCli: (id: string) => req<void>(`/api/tools/cli/${id}`, { method: 'DELETE' }),

  listPolicy: () => req<{ rules: PolicyRule[] }>('/api/tools/policy').then((r) => r.rules),
  savePolicy: (body: Partial<PolicyRule>) =>
    req<PolicyRule>('/api/tools/policy', { method: 'POST', body: JSON.stringify(body) }),
  deletePolicy: (id: string) => req<void>(`/api/tools/policy/${id}`, { method: 'DELETE' }),
}
