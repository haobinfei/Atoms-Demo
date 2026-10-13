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

// latestReadyBuildSeq returns the seq of the newest 完成 event that produced a
// servable build, or 0 when the session has no ready build yet. The preview
// iframe is keyed on builtAt, so this is the only signal allowed to remount it
// (ticket #26): a build that failed leaves no new version to show.
function latestReadyBuildSeq(events: SessionEvent[]): number {
  let seq = 0
  for (const ev of events) {
    if (ev.type === 'done' && ((ev.payload?.status as string) ?? 'ready') === 'ready') {
      seq = Math.max(seq, ev.seq)
    }
  }
  return seq
}

export default function Workspace({
  sessionId,
  user,
  canPublish,
  onExit,
}: {
  sessionId: string
  user: string
  canPublish: boolean
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
  // Highest 完成 event already reflected in the preview. Only a build that
  // advances past this may remount the iframe, so the 15s reconcile poll can
  // never interrupt a preview the user is working in (ticket #26).
  const builtSeqRef = useRef(0)
  const busy = (session?.status ?? 'building') === 'building' || sending

  // markBuilt bumps the preview key only when the build actually advanced.
  const markBuilt = (seq: number) => {
    if (seq <= builtSeqRef.current) return
    builtSeqRef.current = seq
    setBuiltAt(new Date().toISOString())
  }

  // Load persisted history, then follow the live event stream.
  useEffect(() => {
    let source: EventSource | null = null
    let cancelled = false
    let watchdog: ReturnType<typeof setInterval> | null = null

    void getSession(sessionId)
      .then((sess) => {
        if (cancelled) return
        setSession(sess)
        // Sessions saved before the events array existed can carry
        // null; normalize so the timeline never throws mid-setup.
        const persisted = sess.events ?? []
        seenRef.current = persisted.length
        // A session opened after its build finished must not remount the frame
        // on the first poll: the preview is already showing that build.
        builtSeqRef.current = latestReadyBuildSeq(persisted)
        setEvents(persisted)
        source = openEventStream(sessionId, persisted.length)
        source.onmessage = (msg) => {
          const ev = JSON.parse(msg.data as string) as SessionEvent
          seenRef.current = Math.max(seenRef.current, ev.seq)
          setEvents((prev) => (prev.some((e) => e.seq === ev.seq) ? prev : [...prev, ev]))
          if (ev.type === 'done') {
            const status = ((ev.payload?.status as string) ?? 'ready') as Session['status']
            setSession((prev) => (prev ? { ...prev, status } : prev))
            if (status === 'ready') markBuilt(ev.seq)
          }
          if (ev.type === 'contest' || ev.type === 'score' || ev.type === 'publish' || ev.type === 'done') {
            // Contest progress lives on the session; refetch so the
            // score cards and entry statuses stay in sync.
            void getSession(sessionId).then(setSession).catch(() => {})
          }
        }
        source.onerror = () => {
          // EventSource auto-reconnects; nothing to do.
        }

        // Watchdog: SSE can silently wedge behind proxies — a deploy
        // rollout swapping the server pod mid-build leaves the stream
        // half-alive (no data, no error), and EventSource never retries.
        // While the session is building, poll for reconciliation so a
        // stuck 生成中 recovers on its own within ~15s.
        const reconcile = () => {
          void getSession(sessionId)
            .then((sess) => {
              if (cancelled) return
              setSession(sess)
              const persisted = sess.events ?? []
              seenRef.current = Math.max(seenRef.current, ...persisted.map((e) => e.seq), 0)
              setEvents((prev) => {
                const have = new Set(prev.map((e) => e.seq))
                const missing = persisted.filter((e) => !have.has(e.seq))
                return missing.length ? [...prev, ...missing] : prev
              })
              // A wedged stream can miss 完成 entirely, so replay it from the
              // persisted events. markBuilt keeps this to exactly one refresh
              // per real build instead of one per poll.
              markBuilt(latestReadyBuildSeq(persisted))
            })
            .catch(() => {})
        }
        watchdog = setInterval(reconcile, 15000)
      })
      .catch((err) => {
        // A 401 means the local session expired mid-use.
        if (isAuthError(err)) redirectToLogin()
        else console.error('load session failed', err)
      })

    return () => {
      cancelled = true
      source?.close()
      if (watchdog) clearInterval(watchdog)
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
          {canPublish && <button disabled={session?.status === 'building'} onClick={() => setPublishOpen(true)}>发布</button>}
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
