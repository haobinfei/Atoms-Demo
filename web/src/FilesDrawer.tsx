import { useEffect, useMemo, useRef, useState } from 'react'
import { getFile, listFiles, putFile, SaveError, type FileEntry } from './api'
import { highlight } from './highlight'

export default function FilesDrawer({
  sessionId,
  open,
  onClose,
  onSaved,
}: {
  sessionId: string
  open: boolean
  onClose: () => void
  // Called after an edit lands, so the workspace can remount the preview on
  // the new build (ticket #53).
  onSaved?: () => void
}) {
  const [files, setFiles] = useState<FileEntry[]>([])
  const [active, setActive] = useState<string | null>(null)
  const [content, setContent] = useState('')
  const [loadFailed, setLoadFailed] = useState(false)
  const [query, setQuery] = useState('')
  const [copied, setCopied] = useState(false)
  const copyTimer = useRef<number | null>(null)
  // 编辑状态：draft 是用户正在敲的文本，只有保存成功才回写 content。
  // 保存失败时 draft 原样留着——用户敲的东西不能因为构建不过就消失。
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [buildError, setBuildError] = useState('')
  // A clean save used to be silent, which left the user unable to tell a save
  // that worked from one that never fired.
  const [saved, setSaved] = useState(false)
  const savedTimer = useRef<number | null>(null)

  useEffect(() => {
    return () => {
      if (copyTimer.current !== null) clearTimeout(copyTimer.current)
      if (savedTimer.current !== null) clearTimeout(savedTimer.current)
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
    // Switching files abandons an in-progress edit: keeping a draft from
    // another file would make the next save write the wrong content.
    setEditing(false)
    setDraft('')
    setSaveError('')
    setBuildError('')
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

  // startEdit seeds the draft from what is on screen, so the first keystroke
  // never lands on an empty box.
  const startEdit = () => {
    setDraft(content)
    setSaveError('')
    setBuildError('')
    setEditing(true)
  }

  // save keeps the user in the editor on every outcome except a clean one:
  // a rejected write and a failed rebuild both leave something to fix, and the
  // draft is what they fix it with.
  const save = async () => {
    if (!active || saving) return
    setSaving(true)
    setSaveError('')
    setBuildError('')
    try {
      const out = await putFile(sessionId, active, draft)
      setContent(draft)
      setEditing(false)
      if (!out.rebuilt && out.buildError) {
        // The write landed; the app did not compile. Say both.
        setBuildError(out.buildError)
      } else {
        setSaved(true)
        if (savedTimer.current !== null) clearTimeout(savedTimer.current)
        savedTimer.current = window.setTimeout(() => setSaved(false), 4000)
      }
      onSaved?.()
      void listFiles(sessionId).then(setFiles).catch(() => {})
    } catch (err) {
      // The draft stays exactly as typed — including on a 409, where the fix
      // is to wait rather than to retype.
      setSaveError(err instanceof SaveError ? err.message : '保存失败，内容还在编辑框里，可以重试。')
    } finally {
      setSaving(false)
    }
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
                  {editing ? (
                    <>
                      <button className="btn--primary" onClick={() => void save()} disabled={saving || loadFailed}>
                        {saving ? '保存中…' : '保存并重建'}
                      </button>
                      <button
                        onClick={() => {
                          setEditing(false)
                          setSaveError('')
                          setBuildError('')
                        }}
                        disabled={saving}
                      >
                        取消
                      </button>
                    </>
                  ) : (
                    <>
                      <button onClick={startEdit} disabled={loadFailed}>
                        编辑
                      </button>
                      <button onClick={() => void copy()} disabled={loadFailed}>
                        {copied ? '已复制' : '复制'}
                      </button>
                    </>
                  )}
                </div>
                {loadFailed ? (
                  <div className="fd-error" role="status">
                    这个文件读不出来，可能是它刚被重新生成。关掉再打开代码抽屉即可重试。
                  </div>
                ) : editing ? (
                  <>
                    {saveError && (
                      <div className="fd-error" role="alert">
                        {saveError}
                      </div>
                    )}
                    <textarea
                      className="fd-editor"
                      value={draft}
                      onChange={(e) => setDraft(e.target.value)}
                      spellCheck={false}
                      aria-label={`编辑 ${active}`}
                    />
                  </>
                ) : (
                  <>
                    {buildError && (
                      <div className="fd-error" role="alert">
                        <b>已保存，但重建失败：</b>
                        <span>{buildError}</span>
                        <span className="fd-hint">
                          文件已经写进工程，可以点「编辑」修好再保存；预览和 /app 仍在上一次构建的版本上。
                        </span>
                      </div>
                    )}
                    {saved && (
                      <div className="fd-saved" role="status">
                        已保存并重建，预览已刷新。
                      </div>
                    )}
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
                  </>
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
