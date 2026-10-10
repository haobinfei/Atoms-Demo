import { useEffect, useState } from 'react'
import QRCode from 'qrcode'
import { getPublishes, publishSession, type Publish } from './api'

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

  useEffect(() => {
    void getPublishes(sessionId)
      .then((list) => {
        setPublishes(list)
        if (list.length > 0) setLatest(list[list.length - 1])
      })
      .catch(() => setError(true))
  }, [sessionId])

  const publicURL = latest ? `${window.location.origin}${latest.url}` : ''

  useEffect(() => {
    if (!publicURL) return
    void QRCode.toDataURL(publicURL, { width: 220, margin: 1 }).then(setQrDataUrl)
  }, [publicURL])

  async function doPublish() {
    setPublishing(true)
    setError(false)
    try {
      const pub = await publishSession(sessionId)
      setLatest(pub)
      setPublishes((prev) => [...prev, pub])
    } catch {
      setError(true)
    } finally {
      setPublishing(false)
    }
  }

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(publicURL)
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

        {qrDataUrl && (
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

        {error && <div className="ws-chat-error">操作失败，请稍后重试</div>}

        <div className="pub-actions">
          <button className="pub-primary" disabled={publishing} onClick={() => void doPublish()}>
            {publishing ? '发布中…' : latest ? `再次发布（下一版 v${latest.version + 1}）` : '发布 v1'}
          </button>
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
