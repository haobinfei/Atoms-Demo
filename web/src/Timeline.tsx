import { Fragment } from 'react'
import type { SessionEvent } from './api'
import { persona } from './agents'
import { groupTurns, stageLabel, summariseTurn, type TimelineTurn } from './turns'

const TYPE_LABEL: Record<string, string> = {
  plan: '制定计划',
  spec: '整理规格',
  architecture: '设计架构',
  file: '写入文件',
  score: '评分',
  publish: '发布',
  contest: '竞赛',
  done: '完成',
  recovered: '恢复重跑',
  cancel: '取消回合',
  error: '出错',
  agent: '工作中',
}

function detail(ev: SessionEvent): string {
  const p = ev.payload ?? {}
  return (p.detail as string) ?? (p.path as string) ?? (p.message as string) ?? ''
}

// Where the turn broke, in words the user can place.
function stageOf(ev: SessionEvent): string {
  return stageLabel((ev.payload?.stage as string) ?? '')
}

// What to do about it. The raw provider error is not actionable on its own.
function hintOf(ev: SessionEvent): string {
  return (ev.payload?.hint as string) ?? ''
}

// A turn's file writes collapse into one line: a build announces dozens of
// paths, and listing them inline buries everything else in the turn.
function TurnFiles({ files }: { files: string[] }) {
  if (files.length === 0) return null
  return (
    <details className="tl-files">
      <summary>修改了 {files.length} 个文件</summary>
      <ul>
        {files.map((path) => (
          <li key={path}>
            <code>{path}</code>
          </li>
        ))}
      </ul>
    </details>
  )
}

function TurnBody({ turn }: { turn: TimelineTurn }) {
  return (
    <>
      {turn.events.map((ev, i) => {
        // Rendered once by TurnFiles instead of one row per path.
        if (ev.type === 'file') return null
        // A cancelled turn's 完成 is bookkeeping for the client, not an agent
        // step: the 取消回合 record already says what happened.
        if (ev.type === 'done' && ev.payload?.canceled === true) return null
        const who = persona(ev.agent)
        const prev = i > 0 ? turn.events[i - 1] : null
        const showHeader = !prev || prev.agent !== ev.agent
        return (
          <Fragment key={ev.seq}>
            {showHeader && (
              <div className="tl-agent">
                <span className="tl-avatar" style={{ background: who.color }}>
                  {who.name[0]}
                </span>
                <span className="tl-name">{who.name}</span>
                <span className="tl-role">{who.role}</span>
              </div>
            )}
            <div className={`tl-item tl-${ev.type}`}>
              <span className="tl-type">{TYPE_LABEL[ev.type] ?? ev.type}</span>
              {ev.type === 'error' ? (
                <>
                  {stageOf(ev) && <span className="tl-stage">失败阶段：{stageOf(ev)}</span>}
                  {detail(ev) && <span className="tl-detail">{detail(ev)}</span>}
                  {hintOf(ev) && <p className="tl-hint">{hintOf(ev)}</p>}
                </>
              ) : (
                detail(ev) && <span className="tl-detail">{detail(ev)}</span>
              )}
            </div>
          </Fragment>
        )
      })}
      <TurnFiles files={turn.files} />
    </>
  )
}

// Timeline renders the 事件流 as 回合 rather than as one flat list, so a
// session that has been revised several times still reads as "here is what I
// asked for, here is what changed".
export default function Timeline({ events, prompt }: { events: SessionEvent[]; prompt: string }) {
  // Nothing has happened yet. Saying so beats rendering an empty first 回合
  // whose only content is a spinner (ticket #34).
  if (events.length === 0) {
    return (
      <div className="ws-state ws-state--empty" role="status">
        <div className="ws-state-title">还没有任何进展</div>
        <p className="ws-state-detail">智能体团队开始工作后，这里会按回合显示每一步。</p>
      </div>
    )
  }
  const turns = groupTurns(events, prompt)
  return (
    <div className="timeline">
      {turns.map((turn, i) => (
        <section className="tl-turn" key={turn.key}>
          <div className="tl-turn-head">
            <span className="tl-turn-index">第 {i + 1} 回合</span>
            <div className="tl-user-bubble">{turn.goal}</div>
          </div>
          <div className="tl-turn-body">
            <TurnBody turn={turn} />
            {turn.status === 'building' && <div className="tl-typing">智能体团队工作中…</div>}
          </div>
          <div className={`tl-turn-result tl-turn-result--${turn.canceled ? 'canceled' : turn.status}`}>
            {summariseTurn(turn)}
          </div>
        </section>
      ))}
    </div>
  )
}
