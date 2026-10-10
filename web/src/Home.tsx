import { useState } from 'react'
import { createSession } from './api'
import Dashboard from './Dashboard'

const EXAMPLES = [
  '一家咖啡店的电商网站',
  '一个记录每日待办的小工具',
  '一个数据运营分析仪表盘',
  '一个设计师的作品集网站',
]

export default function Home({
  user,
  view,
  onViewChange,
  onCreated,
}: {
  user: string
  view: 'new' | 'projects'
  onViewChange: (view: 'new' | 'projects') => void
  onCreated: (id: string) => void
}) {
  const [prompt, setPrompt] = useState('')
  const [competition, setCompetition] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const submit = async (text: string, contest = false) => {
    if (!text.trim() || busy) return
    setBusy(true)
    setError('')
    try {
      onCreated(await createSession(text.trim(), contest))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <div className="home">
      <div className="home-user">{user}</div>
      <nav className="home-tabs">
        <button className={view === 'new' ? 'on' : ''} onClick={() => onViewChange('new')}>
          新建项目
        </button>
        <button className={view === 'projects' ? 'on' : ''} onClick={() => onViewChange('projects')}>
          我的项目
        </button>
      </nav>
      <div className="home-brand">Atoms Demo</div>
      {view === 'projects' ? (
        <Dashboard onOpen={onCreated} />
      ) : (
        <>
          <h1 className="home-title">把想法变成可运行的应用</h1>
          <p className="home-subtitle">描述你想构建的应用，一支 AI 团队将为你生成、构建并展示它。</p>
          <form
            className="home-composer"
            onSubmit={(e) => {
              e.preventDefault()
              void submit(prompt, competition)
            }}
          >
            <textarea
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="例如：一家咖啡店的电商网站，能浏览商品、加入购物车…"
              rows={3}
              autoFocus
            />
            <div className="home-composer-row">
              <label className="home-competition">
                <input
                  type="checkbox"
                  checked={competition}
                  onChange={(e) => setCompetition(e.target.checked)}
                />
                竞赛模式（多位 AI 参赛者同台生成，点选胜者）
              </label>
              <button type="submit" disabled={busy}>
                {busy ? '创建中…' : competition ? '开赛' : '开始构建'}
              </button>
            </div>
          </form>
          {error && <div className="home-error">{error}</div>}
          <div className="home-examples">
            {EXAMPLES.map((ex) => (
              <button key={ex} type="button" className="chip" onClick={() => void submit(ex)}>
                {ex}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  )
}
