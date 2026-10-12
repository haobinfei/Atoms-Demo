import { useEffect, useState } from 'react'
import QRCode from 'qrcode'
import { getPublishState, publishSession, unpublishSession, type Publish } from './api'

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
  const [error, setError] = useState(false)
  const [publishing, setPublishing] = useState(false)
  const [published, setPublished] = useState(false)

  useEffect(() => {
    void getPublishState(sessionId)
      .then(({ publishes: list, published: isPublished }) => {
        const safe = list ?? []
        setPublishes(safe)
        setPublished(isPublished)
        if (safe.length > 0) setLatest(safe[safe.length - 1])
      })
      .catch(() => setError(true))
  }, [sessionId])

  const publicURL = latest && published ? `${window.location.origin}${latest.url}` : ''

  useEffect(() => {
    if (!publicURL) return
    void QRCode.toDataURL(publicURL, { width: 220, margin: 1 })
      .then(setQrDataUrl)
      .catch(() => setError(true))
  }, [publicURL])

  async function doPublish() {
    setPublishing(true)
    setError(false)
    try {
      const pub = await publishSession(sessionId)
      setLatest(pub)
      setPublishes((prev) => [...prev, pub])
      setPublished(true)
    } catch {
      setError(true)
    } finally {
      setPublishing(false)
    }
  }

  async function doUnpublish() {
    if (!window.confirm('取消公开后，当前分享链接和模拟支付入口将立即停止匿名访问。确定继续？')) return
    setPublishing(true)
    setError(false)
    try {
      await unpublishSession(sessionId)
      setPublished(false)
      setQrDataUrl('')
    } catch {
      setError(true)
    } finally {
      setPublishing(false)
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
      setError(true)
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

        {publicURL && qrDataUrl && (
          <div className="pub-qr">
            <img src={qrDataUrl} alt="应用二维码" />
          </div>
        )}

        {publicURL && (
          <div className="pub-link-row">
            <code className="pub-link">{publicURL}</code>
            <button onClick={() => void copyLink()}>{copied ? '已复制' : '复制链接'}</button>
          </div>
        )}

        {latest && !published && <p className="pub-unpublished">当前应用未公开，历史发布链接暂不可访问。</p>}

        {error && <div className="ws-chat-error">操作失败，请稍后重试</div>}

        <div className="pub-actions">
          <button className="pub-primary" disabled={publishing} onClick={() => void doPublish()}>
            {publishing ? '发布中…' : latest ? `再次发布（下一版 v${latest.version + 1}）` : '发布 v1'}
          </button>
          {published && <button className="pub-unpublish" disabled={publishing} onClick={() => void doUnpublish()}>取消公开</button>}
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
