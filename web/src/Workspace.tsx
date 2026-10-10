import { useEffect, useRef, useState } from 'react'
import {
  chatSession,
  getSession,
  isAuthError,
  openEventStream,
  redirectToLogin,
  type Session,
  type SessionEvent,
} from './api'
import FilesDrawer from './FilesDrawer'
import Preview from './Preview'
import PublishPanel from './PublishPanel'
import Timeline from './Timeline'
import ContestPanel from './ContestPanel'

const QUICK_CHIPS = ['把主题改成蓝色', '把主题改成绿色', '把主题改成橙色']

export default function Workspace({
  sessionId,
  user,
  onExit,
}: {
  sessionId: string
  user: string
  onExit: () => void
}) {
  const [session, setSession] = useState<Session | null>(null)
  const [events, setEvents] = useState<SessionEvent[]>([])
  const [builtAt, setBuiltAt] = useState('')
  const [filesOpen, setFilesOpen] = useState(false)
  const [publishOpen, setPublishOpen] = useState(false)
  const [chatText, setChatText] = useState('')
  const [sending, setSending] = useState(false)
  const [chatError, setChatError] = useState(false)
  const [reviewVersion, setReviewVersion] = useState('')
  const seenRef = useRef(0)
  const busy = (session?.status ?? 'building') === 'building' || sending

  // Load persisted history, then follow the live event stream.
  useEffect(() => {
    let source: EventSource | null = null
    let cancelled = false

    void getSession(sessionId)
      .then((sess) => {
        if (cancelled) return
        setSession(sess)
        // Sessions saved before the events array existed can carry
        // null; normalize so the timeline never throws mid-setup.
        const persisted = sess.events ?? []
        seenRef.current = persisted.length
        setEvents(persisted)
        source = openEventStream(sessionId, persisted.length)
        source.onmessage = (msg) => {
          const ev = JSON.parse(msg.data as string) as SessionEvent
          seenRef.current = Math.max(seenRef.current, ev.seq)
          setEvents((prev) => (prev.some((e) => e.seq === ev.seq) ? prev : [...prev, ev]))
          if (ev.type === 'done') {
            const status = ((ev.payload?.status as string) ?? 'ready') as Session['status']
            setSession((prev) => (prev ? { ...prev, status } : prev))
            if (status === 'ready') setBuiltAt(new Date().toISOString())
          }
          if (ev.type === 'contest' || ev.type === 'score' || ev.type === 'done') {
            // Contest progress lives on the session; refetch so the
            // score cards and entry statuses stay in sync.
            void getSession(sessionId).then(setSession).catch(() => {})
          }
        }
        source.onerror = () => {
          // EventSource auto-reconnects; nothing to do.
        }
      })
      .catch((err) => {
        // A 401 here means the AuthSphere session expired mid-use; the
        // only recovery is a fresh login.
        if (isAuthError(err)) redirectToLogin()
        else console.error('load session failed', err)
      })

    return () => {
      cancelled = true
      source?.close()
    }
  }, [sessionId])

  const statusLabel =
    session?.status === 'ready' ? '构建完成' : session?.status === 'failed' ? '构建失败' : '构建中'

  async function sendChat(text: string) {
    const msg = text.trim()
    if (!msg || busy) return
    setSending(true)
    setChatError(false)
    try {
      await chatSession(sessionId, msg)
      setChatText('')
      // The SSE stream delivers the message event and the new turn; flip
      // the status immediately so the UI feels responsive.
      setSession((prev) => (prev ? { ...prev, status: 'building' } : prev))
    } catch (err) {
      if (isAuthError(err)) {
        redirectToLogin()
        return
      }
      setChatError(true)
      console.error('chat turn failed', err)
    } finally {
      setSending(false)
    }
  }

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
        <div className="ws-actions">
          <span className="ws-user">{user}</span>
          <button disabled={session?.status === 'building'} onClick={() => setPublishOpen(true)}>
            发布
          </button>
          <button onClick={() => setFilesOpen(true)}>代码</button>
          <a className="ws-export" href={`/api/sessions/${sessionId}/export`} download>
            导出
          </a>
        </div>
      </header>
      <div className="ws-body">
        <aside className="ws-timeline">
          <div className="ws-timeline-scroll">
            <Timeline events={events} prompt={session?.prompt ?? ''} />
          </div>
          <div className="ws-chat">
            <div className="ws-chips">
              {QUICK_CHIPS.map((chip) => (
                <button
                  key={chip}
                  className="ws-chip"
                  disabled={busy}
                  onClick={() => void sendChat(chip)}
                >
                  {chip}
                </button>
              ))}
            </div>
            <form
              className="ws-chat-form"
              onSubmit={(e) => {
                e.preventDefault()
                void sendChat(chatText)
              }}
            >
              <input
                value={chatText}
                onChange={(e) => setChatText(e.target.value)}
                placeholder={busy ? '智能体团队工作中…' : '继续描述你想要的变化…'}
                disabled={busy}
              />
              <button type="submit" disabled={busy || !chatText.trim()}>
                发送
              </button>
            </form>
            {chatError && <div className="ws-chat-error">发送失败，请稍后重试</div>}
          </div>
        </aside>
        <main className="ws-preview">
          {session?.contest && !session.contest.winner ? (
            <ContestPanel
              sessionId={sessionId}
              contest={session.contest}
              onPicked={(provider) => {
                setSession((prev) =>
                  prev?.contest
                    ? { ...prev, contest: { ...prev.contest, winner: provider } }
                    : prev,
                )
                setBuiltAt(new Date().toISOString())
              }}
            />
          ) : (
            <>
              {session?.contest && (
                <div className="ws-version-bar">
                  <select value={reviewVersion} onChange={(e) => setReviewVersion(e.target.value)}>
                    <option value="">
                      正式版{session.contest.winner ? `（${session.contest.winner}）` : ''}
                    </option>
                    {session.contest.entries
                      .filter((e) => e.status === 'ready' && e.provider !== session.contest?.winner)
                      .map((e) => (
                        <option key={e.provider} value={e.provider}>
                          回看 {e.provider}
                        </option>
                      ))}
                  </select>
                </div>
              )}
              <Preview
                sessionId={sessionId}
                builtAt={builtAt}
                src={reviewVersion ? `/app/${sessionId}/contest/${reviewVersion}/` : undefined}
              />
            </>
          )}
        </main>
      </div>
      <FilesDrawer sessionId={sessionId} open={filesOpen} onClose={() => setFilesOpen(false)} />
      {publishOpen && (
        <PublishPanel sessionId={sessionId} onClose={() => setPublishOpen(false)} />
      )}
    </div>
  )
}
