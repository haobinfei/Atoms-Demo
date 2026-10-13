import { Fragment } from 'react'
import type { SessionEvent } from './api'
import { persona } from './agents'

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
  error: '出错',
  agent: '工作中',
}

const STAGE_LABEL: Record<string, string> = {
  plan: '需求拆解',
  spec: '产品规格',
  architecture: '架构设计',
  generate: '代码生成',
  write: '文件写入',
  build: '构建',
}

function detail(ev: SessionEvent): string {
  const p = ev.payload ?? {}
  return (p.detail as string) ?? (p.path as string) ?? (p.message as string) ?? ''
}

// Where the turn broke, in words the user can place.
function stageOf(ev: SessionEvent): string {
  const stage = (ev.payload?.stage as string) ?? ''
  if (!stage) return ''
  return STAGE_LABEL[stage] ?? stage
}

// What to do about it. The raw provider error is not actionable on its own.
function hintOf(ev: SessionEvent): string {
  return (ev.payload?.hint as string) ?? ''
}

export default function Timeline({ events, prompt }: { events: SessionEvent[]; prompt: string }) {
  return (
    <div className="timeline">
      <div className="tl-user-bubble">{prompt}</div>
      {events.map((ev, i) => {
        if (ev.type === 'message') {
          return (
            <div key={ev.seq} className="tl-user-bubble">
              {((ev.payload?.content as string) ?? '')}
            </div>
          )
        }
        const who = persona(ev.agent)
        const prev = i > 0 ? events[i - 1] : null
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
                <>
                  {ev.type === 'file' && <code className="tl-path">{detail(ev)}</code>}
                  {ev.type !== 'file' && detail(ev) && <span className="tl-detail">{detail(ev)}</span>}
                </>
              )}
            </div>
          </Fragment>
        )
      })}
      {events.length > 0 && events[events.length - 1].type !== 'done' && (
        <div className="tl-typing">智能体团队工作中…</div>
      )}
    </div>
  )
}
