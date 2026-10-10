import { useState } from 'react'
import { pickWinner, type Contest } from './api'

// ContestPanel is the pre-winner 竞赛 (Competition) view: one pane per
// 参赛者 with a live preview, Nina's score card and a winner pick button.
export default function ContestPanel({
  sessionId,
  contest,
  onPicked,
}: {
  sessionId: string
  contest: Contest
  onPicked: (provider: string) => void
}) {
  const [error, setError] = useState(false)
  const [picking, setPicking] = useState('')
  const allFailed =
    contest.done && contest.entries.every((entry) => entry.status !== 'ready')

  async function choose(provider: string) {
    setPicking(provider)
    setError(false)
    try {
      await pickWinner(sessionId, provider)
      onPicked(provider)
    } catch {
      setError(true)
      setPicking('')
    }
  }

  return (
    <div className="contest">
      <div className="contest-head">
        <h2>竞赛模式：点选胜者</h2>
        {!contest.done && <span className="contest-running">参赛者生成与评分中…</span>}
        {error && <span className="ws-chat-error">操作失败，请稍后重试</span>}
      </div>
      {allFailed && (
        <div className="contest-all-failed">
          全部参赛者已减员，没有可点选的版本。可在左侧对话中描述调整要求，让 AI 重新生成。
        </div>
      )}
      <div className="contest-grid">
        {contest.entries.map((entry) => (
          <div key={entry.provider} className={`contest-card contest-${entry.status}`}>
            <div className="contest-provider">{entry.provider}</div>
            {entry.status === 'ready' ? (
              <iframe
                className="contest-frame"
                src={`/app/${sessionId}/contest/${entry.provider}/`}
                title={`${entry.provider} 的版本`}
              />
            ) : (
              <div className="contest-wait">
                {entry.status === 'building' ? '生成中…' : '生成失败，已减员'}
              </div>
            )}
            {entry.scores && (
              <div className="contest-scores">
                <span>设计 {entry.scores.design}</span>
                <span>功能 {entry.scores.functionality}</span>
                <span>代码 {entry.scores.codeQuality}</span>
              </div>
            )}
            {entry.comment && <div className="contest-comment">{entry.comment}</div>}
            <button
              className="contest-pick"
              disabled={!contest.done || entry.status !== 'ready' || picking !== ''}
              onClick={() => void choose(entry.provider)}
            >
              {picking === entry.provider ? '选定中…' : '选为胜者'}
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
