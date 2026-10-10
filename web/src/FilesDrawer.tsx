import { useEffect, useState } from 'react'
import { getFile, listFiles, type FileEntry } from './api'

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
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!open) return
    void listFiles(sessionId).then(setFiles).catch(() => setFiles([]))
  }, [open, sessionId])

  useEffect(() => {
    if (!active) return
    void getFile(sessionId, active).then((f) => setContent(f.content))
  }, [active, sessionId])

  if (!open) return null

  const copy = async () => {
    await navigator.clipboard.writeText(content)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
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
          <ul className="fd-list">
            {files.map((f) => (
              <li key={f.path}>
                <button className={active === f.path ? 'on' : ''} onClick={() => setActive(f.path)}>
                  {f.path}
                </button>
              </li>
            ))}
          </ul>
          <div className="fd-view">
            {active ? (
              <>
                <div className="fd-toolbar">
                  <code>{active}</code>
                  <button onClick={() => void copy()}>{copied ? '已复制' : '复制'}</button>
                </div>
                <pre>{content}</pre>
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
