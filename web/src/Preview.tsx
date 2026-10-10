import { useState } from 'react'

export default function Preview({
  sessionId,
  builtAt,
}: {
  sessionId: string
  // Changes after every successful build; used as the iframe key so the
  // preview reloads automatically when a turn finishes.
  builtAt: string
}) {
  const [device, setDevice] = useState<'desktop' | 'mobile'>('desktop')
  const [reloadKey, setReloadKey] = useState(0)

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
          <a href={`/app/${sessionId}/`} target="_blank" rel="noreferrer" title="在新标签页打开">
            打开
          </a>
        </div>
      </div>
      <div className="preview-stage">
        <iframe
          key={`${builtAt}-${reloadKey}`}
          src={`/app/${sessionId}/`}
          className={device === 'mobile' ? 'frame mobile' : 'frame'}
          title="应用预览"
        />
      </div>
    </div>
  )
}
