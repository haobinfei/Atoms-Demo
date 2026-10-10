import { useEffect, useRef, useState } from 'react'
import { getSession, openEventStream, type Session, type SessionEvent } from './api'
import Timeline from './Timeline'

export default function Workspace({
  sessionId,
  onExit,
}: {
  sessionId: string
  onExit: () => void
}) {
  const [session, setSession] = useState<Session | null>(null)
  const [events, setEvents] = useState<SessionEvent[]>([])
  const seenRef = useRef(0)

  // Load persisted history, then follow the live event stream.
  useEffect(() => {
    let source: EventSource | null = null
    let cancelled = false

    void getSession(sessionId).then((sess) => {
      if (cancelled) return
      setSession(sess)
      seenRef.current = sess.events.length
      setEvents(sess.events)
      source = openEventStream(sessionId, sess.events.length)
      source.onmessage = (msg) => {
        const ev = JSON.parse(msg.data as string) as SessionEvent
        seenRef.current = Math.max(seenRef.current, ev.seq)
        setEvents((prev) => (prev.some((e) => e.seq === ev.seq) ? prev : [...prev, ev]))
        if (ev.type === 'done') {
          setSession((prev) => (prev ? { ...prev, status: 'ready' } : prev))
        }
      }
      source.onerror = () => {
        // EventSource auto-reconnects; nothing to do.
      }
    })

    return () => {
      cancelled = true
      source?.close()
    }
  }, [sessionId])

  const statusLabel =
    session?.status === 'ready' ? '构建完成' : session?.status === 'failed' ? '构建失败' : '构建中'

  return (
    <div className="workspace">
      <header className="ws-header">
        <button className="ws-back" onClick={onExit} aria-label="返回首页">
          ←
        </button>
        <div className="ws-title">
          <div className="ws-prompt">{session?.prompt ?? sessionId}</div>
          <div className={`ws-status ws-status-${session?.status ?? 'building'}`}>{statusLabel}</div>
        </div>
      </header>
      <div className="ws-body">
        <aside className="ws-timeline">
          <Timeline events={events} prompt={session?.prompt ?? ''} />
        </aside>
        <main className="ws-preview">
          <div className="ws-preview-placeholder">
            <div className="ws-preview-title">实时预览</div>
            <div className="ws-preview-hint">构建器与实时预览在下一切片接入（票 #4）</div>
          </div>
        </main>
      </div>
    </div>
  )
}
