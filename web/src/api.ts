export interface SessionEvent {
  seq: number
  type: 'plan' | 'spec' | 'architecture' | 'file' | 'score' | 'done' | 'error' | 'agent'
  agent?: string
  payload?: Record<string, unknown>
  at: string
}

export interface Session {
  id: string
  prompt: string
  status: 'building' | 'ready' | 'failed'
  createdAt: string
  updatedAt: string
  events: SessionEvent[]
  messages: unknown[]
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

export function openEventStream(id: string, after: number): EventSource {
  return new EventSource(`/api/sessions/${id}/events?after=${after}`)
}
