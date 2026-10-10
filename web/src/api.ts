export interface SessionEvent {
  seq: number
  type: 'plan' | 'spec' | 'architecture' | 'file' | 'score' | 'done' | 'error' | 'agent' | 'message'
  agent?: string
  payload?: Record<string, unknown>
  at: string
}

export interface ChatMessage {
  role: 'user'
  content: string
  at: string
}

export interface Session {
  id: string
  prompt: string
  status: 'building' | 'ready' | 'failed'
  createdAt: string
  updatedAt: string
  events: SessionEvent[]
  messages: ChatMessage[]
}

export async function createSession(prompt: string): Promise<string> {
  const resp = await fetch('/api/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  })
  if (!resp.ok) throw new Error(`create session: ${resp.status}`)
  const data = (await resp.json()) as { sessionId: string }
  return data.sessionId
}

export async function getSession(id: string): Promise<Session> {
  const resp = await fetch(`/api/sessions/${id}`)
  if (!resp.ok) throw new Error(`get session: ${resp.status}`)
  return (await resp.json()) as Session
}

export async function getMe(): Promise<string> {
  const resp = await fetch('/api/me')
  if (!resp.ok) throw new Error(`me: ${resp.status}`)
  const data = (await resp.json()) as { user: string }
  return data.user
}

export interface SessionSummary {
  id: string
  name: string
  category: string
  status: 'building' | 'ready' | 'failed'
  pinned: boolean
  prompt: string
  updatedAt: string
}

export async function listSessions(q = ''): Promise<SessionSummary[]> {
  const suffix = q ? `?q=${encodeURIComponent(q)}` : ''
  const resp = await fetch(`/api/sessions/list${suffix}`)
  if (!resp.ok) throw new Error(`list sessions: ${resp.status}`)
  const data = (await resp.json()) as { sessions: SessionSummary[] }
  return data.sessions ?? []
}

export async function renameSession(id: string, name: string): Promise<void> {
  const resp = await fetch(`/api/sessions/${id}/rename`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
  if (!resp.ok) throw new Error(`rename: ${resp.status}`)
}

export async function pinSession(id: string, pinned: boolean): Promise<void> {
  const resp = await fetch(`/api/sessions/${id}/pin`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ pinned }),
  })
  if (!resp.ok) throw new Error(`pin: ${resp.status}`)
}

export async function duplicateSession(id: string): Promise<string> {
  const resp = await fetch(`/api/sessions/${id}/duplicate`, { method: 'POST' })
  if (!resp.ok) throw new Error(`duplicate: ${resp.status}`)
  const data = (await resp.json()) as { sessionId: string }
  return data.sessionId
}

export async function deleteSession(id: string): Promise<void> {
  const resp = await fetch(`/api/sessions/${id}`, { method: 'DELETE' })
  if (!resp.ok) throw new Error(`delete: ${resp.status}`)
}

// isAuthError reports whether an API failure was a 401 — the AuthSphere
// session expired and the browser must go through login again.
export function isAuthError(e: unknown): boolean {
  return e instanceof Error && /\b401\b/.test(e.message)
}

export function redirectToLogin(): void {
  window.location.href = '/api/auth/login'
}

export async function chatSession(id: string, message: string): Promise<void> {
  const resp = await fetch(`/api/sessions/${id}/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message }),
  })
  if (!resp.ok) throw new Error(`chat: ${resp.status}`)
}

export interface Publish {
  version: number
  url: string
  at: string
}

export async function publishSession(id: string): Promise<Publish> {
  const resp = await fetch(`/api/sessions/${id}/publish`, { method: 'POST' })
  if (!resp.ok) throw new Error(`publish: ${resp.status}`)
  return (await resp.json()) as Publish
}

export async function getPublishes(id: string): Promise<Publish[]> {
  const resp = await fetch(`/api/sessions/${id}/publish`)
  if (!resp.ok) throw new Error(`get publishes: ${resp.status}`)
  const data = (await resp.json()) as { publishes: Publish[] }
  return data.publishes
}

export function openEventStream(id: string, after: number): EventSource {
  return new EventSource(`/api/sessions/${id}/events?after=${after}`)
}

export interface FileEntry {
  path: string
  size: number
}

export async function listFiles(id: string): Promise<FileEntry[]> {
  const resp = await fetch(`/api/sessions/${id}/files`)
  if (!resp.ok) throw new Error(`list files: ${resp.status}`)
  return (await resp.json()) as FileEntry[]
}

export async function getFile(id: string, path: string): Promise<{ path: string; content: string }> {
  const resp = await fetch(`/api/sessions/${id}/file/${encodeURIComponent(path)}`)
  if (!resp.ok) throw new Error(`get file: ${resp.status}`)
  return (await resp.json()) as { path: string; content: string }
}
