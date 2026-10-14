import { useEffect, useRef, useState } from 'react'
import {
  cancelSession,
  chatSession,
  getSession,
  isAuthError,
  listChanges,
  openEventStream,
  redirectToLogin,
  retrySession,
  type ChangeRecord,
  type Session,
  type SessionEvent,
} from './api'
import FilesDrawer from './FilesDrawer'
import Preview from './Preview'
import PublishPanel from './PublishPanel'
import Timeline from './Timeline'
import ContestPanel from './ContestPanel'
import { captureAndUpload } from './thumbnail'
import { lastFailure } from './turns'

const QUICK_CHIPS = ['把主题改成蓝色', '把主题改成绿色', '把主题改成橙色']

// 工作台分栏 (workspace split): a fixed ratio, collapsible either way. The
// preview and the conversation compete for the same screen, and which one the
// user needs changes with what they are doing (ADR-0013's round decided a
// fixed ratio plus collapse rather than a draggable divider).
type Layout = 'split' | 'chat' | 'preview'

// latestReadyBuildSeq returns the seq of the newest 完成 event that produced a
// servable build, or 0 when the session has no ready build yet. The preview
// iframe is keyed on builtAt, so this is the only signal allowed to remount it
// (ticket #26): a build that failed leaves no new version to show, and a
// 取消回合 (Cancel Turn) leaves the previous version exactly as it was.
function latestReadyBuildSeq(events: SessionEvent[]): number {
  let seq = 0
  for (const ev of events) {
    if (ev.type !== 'done' || ev.payload?.canceled === true) continue
    if (((ev.payload?.status as string) ?? 'ready') === 'ready') {
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
  // 改动记录 (Change Records) keyed to their 文件 event seq, so the timeline
  // can attach a diff to each row it renders (ticket #54).
  const [changes, setChanges] = useState<ChangeRecord[]>([])
  const [filesOpen, setFilesOpen] = useState(false)
  const [publishOpen, setPublishOpen] = useState(false)
  const [layout, setLayout] = useState<Layout>('split')
  // 四态 (four states): the workspace always states which one it is in rather
  // than rendering an ambiguous blank (ticket #34). 加载 and 失败 are settled
  // by the initial fetch; 断线 follows the event stream.
  const [loaded, setLoaded] = useState(false)
  const [loadFailed, setLoadFailed] = useState(false)
  const [offline, setOffline] = useState(false)
  const [chatText, setChatText] = useState('')
  const [sending, setSending] = useState(false)
  const [retrying, setRetrying] = useState(false)
  const [canceling, setCanceling] = useState(false)
  // Ticks every second while building to refresh the elapsed time shown in the
  // status badge (ticket #57).
  const [, setTick] = useState(0)
  // Follow-ups typed while a 回合 was running. They wait for it to finish
  // rather than interrupting it (ticket #29).
  const [queue, setQueue] = useState<string[]>([])
  const [chatError, setChatError] = useState(false)
  const [reviewVersion, setReviewVersion] = useState('')
  const seenRef = useRef(0)
  // Set when the running 回合 was aborted rather than finished. The queue must
  // not drain behind a cancel: those messages were written against the turn
  // the user just abandoned, so they wait for an explicit send (ADR-0013).
  const cancelRequestedRef = useRef(false)
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

  // A saved hand edit is a new build, so the preview has to remount on it the
  // way a finished 回合 makes it remount. There is no turn seq to compare
  // against here, so this bumps the frame key directly (ticket #53).
  const onFileSaved = () => {
    setBuiltAt(new Date().toISOString())
    void getSession(sessionId).then(setSession).catch(() => {})
  }

  // The dashboard cover is a screenshot of the preview, taken here because
  // this is the only place the frame is mounted and still (ticket #56). The
  // frame remounts on every build, so each finished build re-captures.
  //
  // Two guards: a frame that loads while the session is 构建中 is the previous
  // build (the new one gets its own load event when it is ready), and a
  // contest review frame shows a candidate version, not this session's app.
  const onFrameLoaded = (frame: HTMLIFrameElement) => {
    if (busy || reviewVersion) return
    void captureAndUpload(sessionId, frame).catch(() => {})
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
        setLoaded(true)
        // Sessions saved before the events array existed can carry
        // null; normalize so the timeline never throws mid-setup.
        const persisted = sess.events ?? []
        seenRef.current = persisted.length
        // A session opened after its build finished must not remount the frame
        // on the first poll: the preview is already showing that build.
        builtSeqRef.current = latestReadyBuildSeq(persisted)
        setEvents(persisted)
        source = openEventStream(sessionId, persisted.length)
        source.onopen = () => setOffline(false)
        source.onmessage = (msg) => {
          // A delivered event is proof the stream is live again.
          setOffline(false)
          const ev = JSON.parse(msg.data as string) as SessionEvent
          seenRef.current = Math.max(seenRef.current, ev.seq)
          setEvents((prev) => (prev.some((e) => e.seq === ev.seq) ? prev : [...prev, ev]))
          if (ev.type === 'cancel') {
            // The 取消回合 request is on the stream before the turn winds
            // down, so the queue is already protected when 完成 arrives.
            cancelRequestedRef.current = true
          }
          if (ev.type === 'done') {
            const status = ((ev.payload?.status as string) ?? 'ready') as Session['status']
            setSession((prev) => (prev ? { ...prev, status } : prev))
            if (ev.payload?.canceled === true) cancelRequestedRef.current = true
            else if (status === 'ready') markBuilt(ev.seq)
          }
          if (ev.type === 'contest' || ev.type === 'score' || ev.type === 'publish' || ev.type === 'done') {
            // Contest progress lives on the session; refetch so the
            // score cards and entry statuses stay in sync.
            void getSession(sessionId).then(setSession).catch(() => {})
          }
          if (ev.type === 'plan') {
            // The 回合's first stage means a Worker claimed the session and
            // started executing it; refetch so the status badge can switch
            // from 排队中 to 构建中 with the server's own startedAt (ticket #57).
            void getSession(sessionId).then(setSession).catch(() => {})
          }
        }
        source.onerror = () => {
          // EventSource auto-reconnects. Surfaced rather than swallowed: a
          // dropped stream means the timeline has stopped moving, and the
          // user is owed that explanation (ticket #34).
          setOffline(true)
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
        if (cancelled) return
        setLoaded(true)
        // A 401 means the local session expired mid-use.
        if (isAuthError(err)) redirectToLogin()
        else {
          setLoadFailed(true)
          console.error('load session failed', err)
        }
      })

    return () => {
      cancelled = true
      source?.close()
      if (watchdog) clearInterval(watchdog)
    }
  }, [sessionId])

  // The 改动记录 are refetched whenever a 文件 or 完成 event lands: a 回合
  // writes its records after announcing its files, and a 手改's record
  // arrives with its own event (ticket #54). A failed fetch keeps the old
  // list — the rows still render, they just have no diff to open yet.
  const changeTrigger = (() => {
    for (let i = events.length - 1; i >= 0; i--) {
      const ev = events[i]
      if (ev.type === 'file' || ev.type === 'done') return ev.seq
    }
    return 0
  })()
  useEffect(() => {
    let alive = true
    listChanges(sessionId).then(
      (list) => {
        if (alive) setChanges(list)
      },
      () => {},
    )
    return () => {
      alive = false
    }
  }, [sessionId, changeTrigger])

  // Tick every second while building so the elapsed time updates.
  useEffect(() => {
    if (session?.status !== 'building') return
    const timer = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(timer)
  }, [session?.status])

  const statusLabel = (() => {
    if (session?.status === 'ready') return '构建完成'
    if (session?.status === 'failed') return '构建失败'
    // building: show queue time if still queued, execution time if claimed.
    if (session?.startedAt) {
      const elapsed = Math.floor((Date.now() - new Date(session.startedAt).getTime()) / 1000)
      return `构建中 ${elapsed}s`
    }
    if (session?.queuedAt) {
      const queued = Math.floor((Date.now() - new Date(session.queuedAt).getTime()) / 1000)
      return `排队中 ${queued}s`
    }
    return '构建中'
  })()

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
      // Pick up the queue timestamp the server just recorded, so the badge
      // can say 排队中 with a real clock rather than waiting for the next poll.
      void getSession(sessionId).then(setSession).catch(() => {})
    } catch (err) {
      if (isAuthError(err)) {
        redirectToLogin()
        return
      }
      // Hand the text back rather than dropping it — including when the send
      // came from the queue, where the user would otherwise never see it fail.
      setChatText((current) => current || msg)
      setChatError(true)
      console.error('chat turn failed', err)
    } finally {
      setSending(false)
    }
  }

  // Submitting while a 回合 is running queues the message instead of
  // interrupting it: the turn in flight is never cancelled by a follow-up
  // (ticket #29).
  function submitChat(text: string) {
    const msg = text.trim()
    if (!msg) return
    if (busy) {
      setQueue((prev) => [...prev, msg])
      setChatText('')
      return
    }
    void sendChat(msg)
  }

  // Drain the queue once the current 回合 has finished. Dequeuing before
  // sending means a second pass cannot resend the same entry. A cancelled
  // turn ends the wait but not the messages: they stay queued until the user
  // sends them (ADR-0013).
  useEffect(() => {
    if (busy || queue.length === 0) return
    if (cancelRequestedRef.current) {
      cancelRequestedRef.current = false
      return
    }
    const [next, ...rest] = queue
    setQueue(rest)
    void sendChat(next)
  }, [busy, queue])

  // 取消回合 (Cancel Turn): stop the turn that is running. The server only
  // records the request, so the session stays 构建中 until the Worker reaches
  // its next step — hence the separate 取消中 label rather than an instant
  // status flip.
  async function cancel() {
    if (canceling) return
    setCanceling(true)
    setChatError(false)
    try {
      await cancelSession(sessionId)
      // Guard the queue from here, not from the stream: a wedged SSE
      // connection would otherwise let the follow-ups go out behind a cancel.
      cancelRequestedRef.current = true
    } catch (err) {
      if (isAuthError(err)) {
        redirectToLogin()
        return
      }
      setChatError(true)
      console.error('cancel turn failed', err)
    } finally {
      setCanceling(false)
    }
  }

  // Sends one queued message on the user's say-so. Used after a cancel, when
  // the queue is deliberately left alone.
  function sendQueued(at: number) {
    const text = queue[at]
    if (!text || busy) return
    setQueue((prev) => prev.filter((_, i) => i !== at))
    void sendChat(text)
  }

  // Re-runs the failed 回合 from the same requirement and history. No message
  // is sent, so nothing is duplicated on the timeline (ticket #30).
  async function retry() {
    if (busy || retrying) return
    setRetrying(true)
    setChatError(false)
    try {
      await retrySession(sessionId)
      // Flip immediately: the retry re-enters the ordinary turn path, so what
      // follows is the same event sequence a chat turn produces.
      setSession((prev) => (prev ? { ...prev, status: 'building' } : prev))
      void getSession(sessionId).then(setSession).catch(() => {})
    } catch (err) {
      if (isAuthError(err)) {
        redirectToLogin()
        return
      }
      setChatError(true)
      console.error('retry turn failed', err)
    } finally {
      setRetrying(false)
    }
  }

  const failure = lastFailure(events)
  // 加载: the session has not been read yet. Distinct from 空 (read, nothing
  // happened yet) and from a load that failed outright.
  const loading = !loaded && !loadFailed
  // The newest 发布 is still publicly reachable. The frame itself always shows
  // the draft build, which is what the badge is there to make explicit.
  const publishes = session?.publishes ?? []
  const published = publishes.length > 0 && !publishes[publishes.length - 1].unpublished

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
          {/* 固定比例 + 可折叠: which pane the user needs changes with what
              they are doing, so each one can take the whole body in one click. */}
          <div className="ws-layout" role="group" aria-label="布局">
            {(
              [
                ['split', '分栏'],
                ['chat', '放大对话'],
                ['preview', '放大预览'],
              ] as const
            ).map(([value, label]) => (
              <button
                key={value}
                className={layout === value ? 'on' : ''}
                aria-pressed={layout === value}
                aria-label={label}
                title={label}
                onClick={() => setLayout(value)}
              >
                {label === '分栏' ? '分栏' : label.slice(2)}
              </button>
            ))}
          </div>
          <span className="ws-user">{user}</span>
          {canPublish && <button disabled={session?.status === 'building'} onClick={() => setPublishOpen(true)}>发布</button>}
          <button onClick={() => setFilesOpen(true)}>代码</button>
          <a className="ws-export" href={`/api/sessions/${sessionId}/export`} download>
            导出
          </a>
        </div>
      </header>
      {loading ? (
        <div className="ws-state ws-state--loading" role="status">
          <div className="ws-state-title">正在载入会话…</div>
          <p className="ws-state-detail">正在读取历史消息与构建状态。</p>
        </div>
      ) : loadFailed ? (
        <div className="ws-state ws-state--failed" role="alert">
          <div className="ws-state-title">无法载入这个会话</div>
          <p className="ws-state-detail">可能是网络中断，也可能是会话已被删除。返回首页可以重试。</p>
          <button className="btn btn--primary btn--sm" onClick={onExit}>
            返回首页
          </button>
        </div>
      ) : (
        <>
          {/* 断线 and 回合失败 are stated above the split so they stay visible
              whichever pane is maximised. */}
          {(offline || session?.status === 'failed') && (
            <div className="ws-notices">
              {offline && (
                <div className="ws-state ws-state--offline" role="status">
                  <div className="ws-state-title">连接已断开，正在自动重连…</div>
                  <p className="ws-state-detail">已完成的进展不会丢失，恢复后会自动补齐。</p>
                </div>
              )}
              {session?.status === 'failed' && (
                <div className="ws-state ws-state--failed" role="alert">
                  <div className="ws-state-title">
                    本轮构建失败{failure.stage ? `：${failure.stage}` : ''}
                  </div>
                  <p className="ws-state-detail">
                    {failure.hint || '重试会从同一个需求与历史重新生成。'}
                  </p>
                  <button
                    className="btn btn--primary btn--sm"
                    disabled={busy || retrying}
                    onClick={() => void retry()}
                  >
                    {retrying ? '重试中…' : '重试本回合'}
                  </button>
                </div>
              )}
            </div>
          )}
          <div className={`ws-body ws-body--${layout}`}>
            <aside className="ws-timeline">
              <div className="ws-timeline-scroll">
                <Timeline
                  sessionId={sessionId}
                  events={events}
                  prompt={session?.prompt ?? ''}
                  changes={changes}
                />
              </div>
              <div className="ws-chat">
                {session?.status === 'building' && (
                  <div className="ws-cancel-bar">
                    <span>智能体团队正在生成，取消后会保留上一版。</span>
                    <button
                      className="btn btn--ghost btn--sm"
                      disabled={canceling}
                      onClick={() => void cancel()}
                    >
                      {canceling ? '取消中…' : '取消本回合'}
                    </button>
                  </div>
                )}
                <div className="ws-chips">
                  {QUICK_CHIPS.map((chip) => (
                    <button
                      key={chip}
                      className="ws-chip"
                      disabled={chatText.trim() !== '' && queue.length > 0}
                      onClick={() => submitChat(chip)}
                    >
                      {chip}
                    </button>
                  ))}
                </div>
                {queue.length > 0 && (
                  <ul className="ws-queue">
                    {queue.map((text, i) => (
                      <li key={`${i}-${text}`}>
                        <span className="ws-queue-tag">排队中</span>
                        <span className="ws-queue-text">{text}</span>
                        <button
                          className="ws-queue-send"
                          disabled={busy}
                          aria-label={`发送排队消息：${text}`}
                          onClick={() => sendQueued(i)}
                        >
                          发送
                        </button>
                        <button
                          className="ws-queue-drop"
                          aria-label={`移除排队消息：${text}`}
                          onClick={() => setQueue((prev) => prev.filter((_, at) => at !== i))}
                        >
                          ×
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
                <form
                  className="ws-chat-form"
                  onSubmit={(e) => {
                    e.preventDefault()
                    submitChat(chatText)
                  }}
                >
                  <textarea
                    value={chatText}
                    rows={2}
                    onChange={(e) => setChatText(e.target.value)}
                    onKeyDown={(e) => {
                      // Enter sends; Shift+Enter is a newline. An IME candidate
                      // confirmation also arrives as Enter and must never send.
                      if (e.key !== 'Enter' || e.shiftKey || e.nativeEvent.isComposing) return
                      e.preventDefault()
                      submitChat(chatText)
                    }}
                    placeholder={
                      busy ? '智能体团队工作中，消息会排队发送…' : '继续描述你想要的变化…'
                    }
                  />
                  <button type="submit" disabled={!chatText.trim()}>
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
                    ready={session?.previewReady ?? false}
                    published={published}
                    src={reviewVersion ? `/app/${sessionId}/contest/${reviewVersion}/` : undefined}
                    onFrameLoaded={onFrameLoaded}
                  />
                </>
              )}
            </main>
          </div>
        </>
      )}
      <FilesDrawer
        sessionId={sessionId}
        open={filesOpen}
        onClose={() => setFilesOpen(false)}
        onSaved={onFileSaved}
      />
      {publishOpen && (
        <PublishPanel sessionId={sessionId} onClose={() => setPublishOpen(false)} />
      )}
    </div>
  )
}
