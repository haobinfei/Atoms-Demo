import { useState } from 'react'

export default function Preview({
  sessionId,
  builtAt,
  src,
  ready = true,
  published = false,
}: {
  sessionId: string
  // Changes after every successful build; used as the iframe key so the
  // preview reloads automatically when a turn finishes.
  builtAt: string
  // Optional override, e.g. a contest loser's archived version.
  src?: string
  // Derived server-side: is there a build for /app to serve? The frame is not
  // mounted until there is one, so a session without a build shows a stated
  // placeholder instead of loading an error page (ADR-0014).
  ready?: boolean
  // Whether the newest 发布 (Publish) is still publicly reachable. The frame
  // always shows the draft build, which is the same bytes as the published one
  // right after a publish — and diverges as soon as another 回合 runs.
  published?: boolean
}) {
  const [device, setDevice] = useState<'desktop' | 'mobile'>('desktop')
  const [reloadKey, setReloadKey] = useState(0)
  // Which frame key has finished loading. Keyed rather than boolean so a new
  // build (or a manual refresh) shows the loading state again.
  const [loadedKey, setLoadedKey] = useState('')

  // One address drives both the frame and the "open in new tab" link, so the
  // tab always lands on the version being previewed — including a contest
  // candidate, whose archived path is already versioned (ticket #26).
  const base = src ?? `/app/${sessionId}/`
  const frameSrc = src ? src : `${base}?v=${encodeURIComponent(builtAt)}`
  const frameKey = `${src ?? 'official'}-${builtAt}-${reloadKey}`
  const loading = ready && loadedKey !== frameKey

  return (
    <div className="preview">
      <div className="preview-bar">
        <div className="preview-bar-left">
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
          {/* 草稿版 vs 已发布版: the frame is always the draft build, and the
              two only coincide right after a 发布. */}
          <span className={`preview-badge preview-badge--${published ? 'published' : 'draft'}`}>
            {published ? '已发布版' : '草稿版'}
          </span>
        </div>
        <div className="preview-actions">
          <button onClick={() => setReloadKey((k) => k + 1)} disabled={!ready} title="刷新预览">
            刷新
          </button>
          <a href={base} target="_blank" rel="noreferrer" title="在新标签页打开">
            打开
          </a>
        </div>
      </div>
      <div className="preview-stage">
        {ready ? (
          <>
            {loading && <div className="preview-loading">预览加载中…</div>}
            <iframe
              key={frameKey}
              src={frameSrc}
              className={device === 'mobile' ? 'frame mobile' : 'frame'}
              title="应用预览"
              onLoad={() => setLoadedKey(frameKey)}
            />
          </>
        ) : (
          <div className="preview-idle">应用产出构建结果后，这里会显示预览。</div>
        )}
      </div>
    </div>
  )
}
