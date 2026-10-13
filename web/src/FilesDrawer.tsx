import { useEffect, useMemo, useRef, useState } from 'react'
import { getFile, listFiles, type FileEntry } from './api'
import { highlight } from './highlight'

export default function FilesDrawer({
  sessionId,
  open,
  onClose,
}: {
  sessionId: string
  open: boolean
  onClose: () => void
}) {
  const [files, setFiles] = useState<FileEntry[]>([])
  const [active, setActive] = useState<string | null>(null)
  const [content, setContent] = useState('')
  const [loadFailed, setLoadFailed] = useState(false)
  const [query, setQuery] = useState('')
  const [copied, setCopied] = useState(false)
  const copyTimer = useRef<number | null>(null)

  useEffect(() => {
    return () => {
      if (copyTimer.current !== null) clearTimeout(copyTimer.current)
    }
  }, [])

  useEffect(() => {
    if (!open) return
    void listFiles(sessionId).then(setFiles).catch(() => setFiles([]))
  }, [open, sessionId])

  useEffect(() => {
    if (!active) return
    let stale = false
    setContent('')
    setLoadFailed(false)
    void getFile(sessionId, active)
      .then((f) => {
        if (!stale) setContent(f.content)
      })
      .catch(() => {
        // Stated as a failure rather than dressed up as source: a comment
        // reading "加载失败" is indistinguishable from a real comment.
        if (!stale) setLoadFailed(true)
      })
    return () => {
      stale = true
    }
  }, [active, sessionId])

  // Colourising is pure and only depends on what is open, so it must not run
  // again on every keystroke in the search box.
  const lines = useMemo(
    () => (active && !loadFailed ? highlight(content, active) : []),
    [content, active, loadFailed],
  )

  if (!open) return null

  const needle = query.trim().toLowerCase()
  const visible = needle ? files.filter((f) => f.path.toLowerCase().includes(needle)) : files

  const copy = async () => {
    await navigator.clipboard.writeText(content)
    setCopied(true)
    if (copyTimer.current !== null) clearTimeout(copyTimer.current)
    copyTimer.current = window.setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div className="drawer-mask" onClick={onClose}>
      <aside className="files-drawer" onClick={(e) => e.stopPropagation()}>
        <div className="fd-head">
          <b>工程文件</b>
          <button onClick={onClose} aria-label="关闭">
            ×
          </button>
        </div>
        <div className="fd-body">
          <div className="fd-side">
            <input
              className="fd-search"
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="按文件名搜索…"
              aria-label="按文件名搜索"
            />
            {visible.length === 0 ? (
              <p className="fd-no-match">没有匹配「{query.trim()}」的文件。</p>
            ) : (
              <ul className="fd-list">
                {visible.map((f) => (
                  <li key={f.path}>
                    <button className={active === f.path ? 'on' : ''} onClick={() => setActive(f.path)}>
                      {f.path}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div className="fd-view">
            {active ? (
              <>
                <div className="fd-toolbar">
                  <code>{active}</code>
                  <button onClick={() => void copy()} disabled={loadFailed}>
                    {copied ? '已复制' : '复制'}
                  </button>
                </div>
                {loadFailed ? (
                  <div className="fd-error" role="status">
                    这个文件读不出来，可能是它刚被重新生成。关掉再打开代码抽屉即可重试。
                  </div>
                ) : (
                  <div className="fd-code" role="group" aria-label="源码">
                    {lines.map((tokens, i) => (
                      <div className="fd-line" key={i}>
                        <span className="fd-line-no">{i + 1}</span>
                        <code className="fd-line-text">
                          {tokens.map((token, j) =>
                            token.kind === 'plain' ? (
                              token.text
                            ) : (
                              <span key={j} className={`tok-${token.kind}`}>
                                {token.text}
                              </span>
                            ),
                          )}
                        </code>
                      </div>
                    ))}
                  </div>
                )}
              </>
            ) : (
              <div className="fd-empty">选择左侧文件查看内容</div>
            )}
          </div>
        </div>
      </aside>
    </div>
  )
}
