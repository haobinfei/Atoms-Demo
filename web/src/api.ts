export interface SessionEvent {
  seq: number
  type:
    | 'plan'
    | 'spec'
    | 'architecture'
    | 'file'
    | 'score'
    | 'contest'
    | 'publish'
    | 'done'
    | 'error'
    | 'agent'
    | 'message'
    | 'recovered'
    | 'cancel'
  agent?: string
  payload?: Record<string, unknown>
  at: string
}

export interface ChatMessage {
  role: 'user' | 'agent'
  agent?: string
  content: string
  at: string
}

export interface ContestScores {
  design: number
  functionality: number
  codeQuality: number
}

export interface ContestEntry {
  provider: string
  status: 'building' | 'ready' | 'failed'
  scores?: ContestScores
  comment?: string
}

export interface Contest {
  entries: ContestEntry[]
  winner?: string
  done: boolean
}

export interface Session {
  id: string
  prompt: string
  status: 'building' | 'ready' | 'failed'
  createdAt: string
  updatedAt: string
  events: SessionEvent[]
  messages: ChatMessage[]
  contest?: Contest
  // The in-flight 任务 (Job) kind while status is "building" (turn | contest |
  // publish). It is how the client tells a 发布 apart from a 回合.
  kind?: string
  publishes?: Publish[]
  // Derived server-side per request: whether /app/{id}/ currently has a
  // servable build. A rendering hint only, never an authorization signal.
  previewReady: boolean
  // When the session first entered the queue (present if status is building).
  queuedAt?: string
  // When a worker claimed the session and began execution (present while a
  // worker has the lease).
  startedAt?: string
}

export async function createSession(prompt: string, competition = false): Promise<string> {
  const resp = await fetch('/api/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt, competition }),
  })
  if (!resp.ok) throw new Error(`create session: ${resp.status}`)
  const data = (await resp.json()) as { sessionId: string }
  return data.sessionId
}

export async function pickWinner(id: string, provider: string): Promise<void> {
  const resp = await fetch(`/api/sessions/${id}/winner`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider }),
  })
  if (!resp.ok) throw new Error(`pick winner: ${resp.status}`)
}

export async function getSession(id: string): Promise<Session> {
  const resp = await fetch(`/api/sessions/${id}`)
  if (!resp.ok) throw new Error(`get session: ${resp.status}`)
  return (await resp.json()) as Session
}

export interface WorkspaceInfo {
  id: string
  name: string
  kind: 'personal' | 'team'
  role: 'owner' | 'editor'
}

export interface Account {
  id: string
  email: string
  verified: boolean
  admin: boolean
  currentWorkspace: string
  workspaces: WorkspaceInfo[]
}

async function apiRequest<T = void>(path: string, method = 'GET', body?: unknown, extraHeaders?: Record<string, string>): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: { ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...extraHeaders },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!resp.ok) {
    const data = await resp.json().catch(() => null) as { error?: string; message?: string } | null
    throw new Error(`${resp.status}: ${data?.message || data?.error || resp.statusText}`)
  }
  if (resp.status === 204) return undefined as T
  const raw = await resp.text()
  return (raw ? JSON.parse(raw) : undefined) as T
}

export const getMe = () => apiRequest<Account>('/api/me')
export const register = (email: string, password: string) => apiRequest('/api/auth/register', 'POST', { email, password })
export const verifyEmail = (token: string) => apiRequest('/api/auth/verify', 'POST', { token })
export const login = (email: string, password: string) => apiRequest('/api/auth/login', 'POST', { email, password })
export const logout = () => apiRequest('/api/auth/logout', 'POST')
export const resendVerification = (email: string) => apiRequest('/api/auth/resend-verification', 'POST', { email })
export const forgotPassword = (email: string) => apiRequest('/api/auth/forgot-password', 'POST', { email })
export const resetPassword = (token: string, password: string) => apiRequest('/api/auth/reset-password', 'POST', { token, password })
export const changePassword = (currentPassword: string, newPassword: string) => apiRequest('/api/auth/change-password', 'POST', { currentPassword, newPassword })
export const deleteAccount = (email: string) => apiRequest('/api/account', 'DELETE', undefined, { 'X-Confirm-Delete': email })

export const createWorkspace = (name: string) => apiRequest('/api/workspaces', 'POST', { name })
export const switchWorkspace = (id: string) => apiRequest('/api/workspaces/current', 'POST', { id })
export const deleteWorkspace = (id: string, name: string) => apiRequest(`/api/workspaces/${encodeURIComponent(id)}`, 'DELETE', undefined, { 'X-Confirm-Delete': name })
export const leaveWorkspace = (id: string) => apiRequest(`/api/workspaces/${encodeURIComponent(id)}/leave`, 'POST')

export interface Member {
  accountId: string
  email: string
  role: 'owner' | 'editor'
}
export interface Invitation {
  id: string
  email: string
  role: 'owner' | 'editor'
  expiresAt: string
}
export const listMembers = (id: string) => apiRequest<{ members: Member[] }>(`/api/workspaces/${encodeURIComponent(id)}/members`)
export const updateMember = (id: string, accountId: string, role: Member['role']) => apiRequest(`/api/workspaces/${encodeURIComponent(id)}/members/${encodeURIComponent(accountId)}`, 'PATCH', { role })
export const removeMember = (id: string, accountId: string) => apiRequest(`/api/workspaces/${encodeURIComponent(id)}/members/${encodeURIComponent(accountId)}`, 'DELETE')
export const listInvitations = (id: string) => apiRequest<{ invitations: Invitation[] }>(`/api/workspaces/${encodeURIComponent(id)}/invitations`)
export const inviteMember = (id: string, email: string, role: Member['role']) => apiRequest(`/api/workspaces/${encodeURIComponent(id)}/invitations`, 'POST', { email, role })
export const revokeInvitation = (id: string, invitationId: string) => apiRequest(`/api/workspaces/${encodeURIComponent(id)}/invitations/${encodeURIComponent(invitationId)}`, 'DELETE')
export const acceptInvitation = (token: string) => apiRequest('/api/invitations/accept', 'POST', { token })

export interface AdminAccount {
  id: string
  email: string
  disabled: boolean
  verified: boolean
  memberships: { workspaceId: string; workspaceName: string; role: Member['role'] }[]
}
export interface AuditEntry {
  id: string
  actorId: string
  action: string
  target: string
  reason: string
  at: string
}
export const listAdminAccounts = () => apiRequest<{ accounts: AdminAccount[] }>('/api/admin/accounts')
export const setAccountDisabled = (id: string, disabled: boolean, reason: string) => apiRequest(`/api/admin/accounts/${encodeURIComponent(id)}`, 'PATCH', { disabled, reason })
export const rescueWorkspace = (id: string, accountId: string, reason: string) => apiRequest(`/api/admin/workspaces/${encodeURIComponent(id)}/rescue`, 'POST', { accountId, reason })
export const listAudit = () => apiRequest<{ entries: AuditEntry[] }>('/api/admin/audit')

export interface SessionSummary {
  id: string
  name: string
  category: string
  status: 'building' | 'ready' | 'failed'
  pinned: boolean
  prompt: string
  updatedAt: string
  previewReady: boolean
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

// Session expiry returns the user to the local login screen.
export function isAuthError(e: unknown): boolean {
  return e instanceof Error && /\b401\b/.test(e.message)
}

export function redirectToLogin(): void {
  // Carry the 项目 the user was in. App reads ?s= on load, so an expired
  // session comes back to that 项目 instead of the home screen — and if it is
  // gone or not theirs, the workspace says so rather than showing a blank.
  const id = new URLSearchParams(location.search).get('s')
  window.location.href = id ? `/?s=${encodeURIComponent(id)}` : '/'
}

export async function chatSession(id: string, message: string): Promise<void> {
  const resp = await fetch(`/api/sessions/${id}/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message }),
  })
  if (!resp.ok) throw new Error(`chat: ${resp.status}`)
}

// Re-runs the last 回合 after it failed. The requirement and history are
// already on the session, so no message is sent.
export const retrySession = (id: string) =>
  apiRequest(`/api/sessions/${encodeURIComponent(id)}/retry`, 'POST')

// 取消回合 (Cancel Turn): asks the server to stop the 回合 now executing. The
// server only records the request — the Worker aborts between steps and keeps
// the previous version — so the session stays 构建中 until it lands.
export const cancelSession = (id: string) =>
  apiRequest(`/api/sessions/${encodeURIComponent(id)}/cancel`, 'POST')

export interface Publish {
  version: number
  url: string
  at: string
  unpublished?: boolean
}

// startPublish claims the session and puts the rebuild on a Worker, returning
// as soon as the server has accepted it (202). The new version is recorded
// later — so this deliberately has no deadline: a slow build is not a failure,
// and treating it as one invites a duplicate 发布.
export async function startPublish(id: string): Promise<void> {
  const resp = await fetch(`/api/sessions/${id}/publish`, { method: 'POST' })
  if (!resp.ok) throw new Error(`publish: ${resp.status}`)
}

export async function getPublishState(id: string): Promise<{ publishes: Publish[]; published: boolean }> {
  const resp = await fetch(`/api/sessions/${id}/publish`)
  if (!resp.ok) throw new Error(`get publish state: ${resp.status}`)
  const data = (await resp.json()) as { publishes?: Publish[]; published?: boolean }
  return { publishes: data.publishes ?? [], published: data.published ?? (data.publishes?.length ?? 0) > 0 }
}

export const unpublishSession = (id: string) => apiRequest(`/api/sessions/${encodeURIComponent(id)}/unpublish`, 'POST')

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
