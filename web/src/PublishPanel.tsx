import { useEffect, useRef, useState } from 'react'
import QRCode from 'qrcode'
import {
  getPublishState,
  getSession,
  startPublish,
  unpublishSession,
  type Publish,
} from './api'

// PublishPanel is the 发布 (Publish) overlay: QR code for the public
// /app/{id}/ URL, one-click copy, and the versioned publish history.
export default function PublishPanel({
  sessionId,
  onClose,
}: {
  sessionId: string
  onClose: () => void
}) {
  const [publishes, setPublishes] = useState<Publish[]>([])
  const [latest, setLatest] = useState<Publish | null>(null)
  const [qrDataUrl, setQrDataUrl] = useState('')
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')
  const [publishing, setPublishing] = useState(false)
  const [published, setPublished] = useState(false)
  // The version count that means "the 发布 we started has landed".
  const expectedRef = useRef(0)

  useEffect(() => {
    void getPublishState(sessionId)
      .then(({ publishes: list, published: isPublished }) => {
        const safe = list ?? []
        setPublishes(safe)
        setPublished(isPublished)
        if (safe.length > 0) setLatest(safe[safe.length - 1])
      })
      .catch(() => setError('读取发布记录失败，请稍后重试。'))
  }, [sessionId])

  const publicURL = latest && published ? `${window.location.origin}${latest.url}` : ''

  useEffect(() => {
    if (!publicURL) return
    void QRCode.toDataURL(publicURL, { width: 220, margin: 1 })
      .then(setQrDataUrl)
      .catch(() => setError('二维码生成失败，可以复制链接分享。'))
  }, [publicURL])

  // While a 发布 is in flight, watch the 会话 rather than a stopwatch. A new
  // version landing is success; the 发布 job ending without one is the only
  // honest failure signal. A deadline here would report a slow build as a
  // failure — and the user, told it failed, would publish a duplicate.
  useEffect(() => {
    if (!publishing) return
    let stopped = false
    const tick = async () => {
      let sess
      try {
        sess = await getSession(sessionId)
      } catch {
        // A transient read failure is not a failed publish: keep waiting.
        return
      }
      if (stopped) return
      const list = sess.publishes ?? []
      setPublishes(list)
      setPublished(list.length > 0 && !list[list.length - 1].unpublished)
      if (list.length > 0) setLatest(list[list.length - 1])
      if (list.length >= expectedRef.current) {
        setPublishing(false)
        return
      }
      // Still the publish job's turn? Anything else means it ended — and
      // since no version was recorded, it did not succeed.
      if (sess.kind !== 'publish' && sess.status !== 'building') {
        setPublishing(false)
        setError('发布没有完成，上一版仍然有效，可以再试一次。')
      }
    }
    void tick()
    const timer = setInterval(() => void tick(), 1000)
    return () => {
      stopped = true
      clearInterval(timer)
    }
  }, [publishing, sessionId])

  async function doPublish() {
    if (publishing) return
    setError('')
    expectedRef.current = publishes.length + 1
    try {
      await startPublish(sessionId)
    } catch {
      setError('发起发布失败，请稍后重试。')
      return
    }
    setPublishing(true)
  }

  async function doUnpublish() {
    if (!window.confirm('取消公开后，当前分享链接和模拟支付入口将立即停止匿名访问。确定继续？')) return
    setError('')
    try {
      await unpublishSession(sessionId)
      setPublished(false)
      setQrDataUrl('')
    } catch {
      setError('取消公开失败，请稍后重试。')
    }
  }

  async function copyLink() {
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(publicURL)
      } else {
        // Non-secure contexts (e.g. plain http on a LAN IP) have no
        // navigator.clipboard; fall back to the legacy copy path.
        const ta = document.createElement('textarea')
        ta.value = publicURL
        document.body.appendChild(ta)
        ta.select()
        document.execCommand('copy')
        document.body.removeChild(ta)
      }
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      setError('复制失败，请手动选择链接。')
    }
  }

  return (
    <div className="pub-overlay" onClick={onClose}>
      <div className="pub-panel" onClick={(e) => e.stopPropagation()}>
        <div className="pub-head">
          <h2>发布应用</h2>
          <button className="pub-close" onClick={onClose} aria-label="关闭">
            ×
          </button>
        </div>

        {publishing && (
          <p className="pub-progress" role="status">
            正在重新构建并发布 v{publishes.length + 1}…构建较慢时会多等一会儿，请不要重复点击。
          </p>
        )}

        {publicURL && qrDataUrl && (
          <div className="pub-qr">
            <img src={qrDataUrl} alt="应用二维码" />
          </div>
        )}

        {publicURL && (
          <div className="pub-link-row">
            <code className="pub-link">{publicURL}</code>
            <button className="btn btn--ghost" onClick={() => void copyLink()}>
              {copied ? '已复制' : '复制链接'}
            </button>
          </div>
        )}

        {latest && !published && <p className="pub-unpublished">当前应用未公开，历史发布链接暂不可访问。</p>}

        {error && <div className="ws-chat-error">{error}</div>}

        <div className="pub-actions">
          <button className="btn btn--primary" disabled={publishing} onClick={() => void doPublish()}>
            {publishing ? '发布中…' : latest ? `再次发布（下一版 v${latest.version + 1}）` : '发布 v1'}
          </button>
          {published && (
            <button className="btn btn--danger" disabled={publishing} onClick={() => void doUnpublish()}>
              取消公开
            </button>
          )}
        </div>

        {publishes.length > 0 && (
          <ul className="pub-history">
            {publishes.map((pub) => (
              <li key={pub.version}>
                <span className="pub-version">v{pub.version}</span>
                <time>{new Date(pub.at).toLocaleString()}</time>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
