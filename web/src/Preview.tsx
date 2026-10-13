import { useState } from 'react'

export default function Preview({
  sessionId,
  builtAt,
  src,
}: {
  sessionId: string
  // Changes after every successful build; used as the iframe key so the
  // preview reloads automatically when a turn finishes.
  builtAt: string
  // Optional override, e.g. a contest loser's archived version.
  src?: string
}) {
  const [device, setDevice] = useState<'desktop' | 'mobile'>('desktop')
  const [reloadKey, setReloadKey] = useState(0)

  // One address drives both the frame and the "open in new tab" link, so the
  // tab always lands on the version being previewed — including a contest
  // candidate, whose archived path is already versioned (ticket #26).
  const base = src ?? `/app/${sessionId}/`
  const frameSrc = src ? src : `${base}?v=${encodeURIComponent(builtAt)}`

  return (
    <div className="preview">
      <div className="preview-bar">
        <div className="device-toggle">
          <button
            className={device === 'desktop' ? 'on' : ''}
            onClick={() => setDevice('desktop')}
            title="桌面视口"
          >
            桌面
          </button>
          <button
            className={device === 'mobile' ? 'on' : ''}
            onClick={() => setDevice('mobile')}
            title="移动视口"
          >
            移动
          </button>
        </div>
        <div className="preview-actions">
          <button onClick={() => setReloadKey((k) => k + 1)} title="刷新预览">
            刷新
          </button>
          <a href={base} target="_blank" rel="noreferrer" title="在新标签页打开">
            打开
          </a>
        </div>
      </div>
      <div className="preview-stage">
        <iframe
          key={`${src ?? 'official'}-${builtAt}-${reloadKey}`}
          src={frameSrc}
          className={device === 'mobile' ? 'frame mobile' : 'frame'}
          title="应用预览"
        />
      </div>
    </div>
  )
}
