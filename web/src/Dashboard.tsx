import { useCallback, useEffect, useRef, useState } from 'react'
import {
  deleteSession,
  duplicateSession,
  isAuthError,
  listSessions,
  pinSession,
  redirectToLogin,
  renameSession,
  type SessionSummary,
} from './api'

const CATEGORY_LABEL: Record<string, string> = {
  todo: '待办',
  dashboard: '数据',
  ecommerce: '电商',
  site: '网站',
}

const STATUS_LABEL: Record<string, string> = {
  building: '构建中',
  ready: '完成',
  failed: '失败',
}

const STATUS_BADGE: Record<string, string> = {
  building: 'badge',
  ready: 'badge badge--success',
  failed: 'badge badge--danger',
}

function ProjectCover({ session }: { session: SessionSummary }) {
  const [previewFailed, setPreviewFailed] = useState(false)
  const canPreview = session.status === 'ready' && !previewFailed

  return (
    <div className={`dash-cover cover-${session.category}`} aria-label={`${session.name} 项目预览`}>
      {canPreview ? (
        <div className="dash-cover-frame">
          <iframe
            src={`/app/${session.id}/?v=${encodeURIComponent(session.updatedAt)}`}
            title={`${session.name} 预览`}
            loading="lazy"
            tabIndex={-1}
            onError={() => setPreviewFailed(true)}
          />
        </div>
      ) : (
        <>
          <span>{(session.name.trim()[0] ?? 'A').toUpperCase()}</span>
          <small>{session.status === 'building' ? '正在构建' : '预览暂不可用'}</small>
        </>
      )}
      {canPreview && <span className="dash-cover-live">实时预览</span>}
    </div>
  )
}

export default function Dashboard({
  onOpen,
  canDelete,
  onNewProject,
}: {
  onOpen: (id: string) => void
  canDelete: boolean
  onNewProject: () => void
}) {
  const [sessions, setSessions] = useState<SessionSummary[]>([])
  const [query, setQuery] = useState('')
  const [error, setError] = useState(false)
  const [renaming, setRenaming] = useState<string | null>(null)
  const [renameText, setRenameText] = useState('')
  const reqSeq = useRef(0)
  const committingRef = useRef(false)

  // The sequence guard discards stale list responses: searching types
  // fast, and a slow earlier response must not overwrite a newer list.
  const refresh = useCallback((q: string) => {
    const seq = ++reqSeq.current
    listSessions(q).then(
      (list) => {
        if (seq !== reqSeq.current) return
        setSessions(list)
        setError(false)
      },
      (err) => {
        if (seq !== reqSeq.current) return
        if (isAuthError(err)) {
          redirectToLogin()
          return
        }
        setError(true)
      },
    )
  }, [])

  useEffect(() => {
    refresh(query)
  }, [query, refresh])

  async function togglePin(sess: SessionSummary) {
    try {
      await pinSession(sess.id, !sess.pinned)
      refresh(query)
    } catch {
      setError(true)
    }
  }

  async function doRename(sess: SessionSummary) {
    // onBlur and onSubmit can both fire in the same tick; commit once.
    if (committingRef.current) return
    committingRef.current = true
    setTimeout(() => {
      committingRef.current = false
    }, 0)
    const name = renameText.trim()
    setRenaming(null)
    if (!name || name === sess.name) return
    try {
      await renameSession(sess.id, name)
      refresh(query)
    } catch {
      setError(true)
    }
  }

  async function doDuplicate(sess: SessionSummary) {
    try {
      await duplicateSession(sess.id)
      refresh(query)
    } catch {
      setError(true)
    }
  }

  async function doDelete(sess: SessionSummary) {
    if (!window.confirm(`删除「${sess.name}」？此操作不可恢复。`)) return
    try {
      await deleteSession(sess.id)
      refresh(query)
    } catch {
      setError(true)
    }
  }

  return (
    <div className="dash">
      <input
        className="dash-search"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="搜索项目…"
      />
      {error && <div className="ws-chat-error">操作失败，请稍后重试</div>}
      {sessions.length === 0 && !error && (
        <div className="dash-empty">
          <div className="dash-empty-title">{query ? '没有匹配的项目' : '还没有项目'}</div>
          <p>
            {query
              ? '换个关键词再试试，或清空搜索框查看全部项目。'
              : '描述一个想法，AI 团队会把它生成、构建并发布成可运行的应用。'}
          </p>
          {!query && (
            <button className="btn btn--primary" onClick={onNewProject}>
              新建项目
            </button>
          )}
        </div>
      )}
      <div className="dash-grid">
        {sessions.map((sess) => (
          <div key={sess.id} className={`dash-card dash-${sess.status}`}>
            <ProjectCover session={sess} />
            <button className="dash-open" onClick={() => onOpen(sess.id)}>
              <div className="dash-meta">
                <span className="badge badge--accent">{CATEGORY_LABEL[sess.category] ?? sess.category}</span>
                <span className={STATUS_BADGE[sess.status] ?? 'badge'}>
                  {STATUS_LABEL[sess.status] ?? sess.status}
                </span>
              </div>
              {renaming === sess.id ? (
                <form
                  onSubmit={(e) => {
                    e.preventDefault()
                    void doRename(sess)
                  }}
                >
                  <input
                    className="dash-rename"
                    value={renameText}
                    onChange={(e) => setRenameText(e.target.value)}
                    onBlur={() => void doRename(sess)}
                    autoFocus
                  />
                </form>
              ) : (
                <span className="dash-name">{sess.name}</span>
              )}
              <time className="dash-time">{new Date(sess.updatedAt).toLocaleString()}</time>
            </button>
            <div className="dash-actions">
              <button
                title={sess.pinned ? '取消置顶' : '置顶'}
                className={sess.pinned ? 'dash-on' : ''}
                onClick={() => void togglePin(sess)}
              >
                ★
              </button>
              <button
                title="重命名"
                onClick={() => {
                  setRenaming(sess.id)
                  setRenameText(sess.name)
                }}
              >
                ✎
              </button>
              <button title="创建副本" onClick={() => void doDuplicate(sess)}>
                ⎘
              </button>
              {canDelete && <button title="删除" onClick={() => void doDelete(sess)}>×</button>}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
