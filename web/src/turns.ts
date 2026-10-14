import type { SessionEvent } from './api'

// One 回合 (Turn): a user input plus the 智能体 team's response to it.
export interface TimelineTurn {
  key: string
  // 本轮目标: the prompt for the first turn, the follow-up message after that.
  goal: string
  events: SessionEvent[]
  // The 文件 (file) events of this turn, in the order they were announced.
  // Each carries the seq its 改动记录 is keyed to and the agent that wrote it,
  // which is how the page tells an Agent's rewrite from a 用户手改 (ticket #54).
  fileEvents: SessionEvent[]
  status: 'building' | 'ready' | 'failed'
  // Set when the user aborted this turn (取消回合). A cancelled turn is 就绪,
  // not 失败, and its outcome line must not read like a completed build.
  canceled: boolean
  // Set when the turn ended in failure, so the turn can say where it broke.
  failedStage: string
  failedHint: string
}

// groupTurns folds the flat 事件流 into 回合.
//
// A turn opens with the session prompt, and every subsequent user message
// opens the next one; the events until the following message belong to it.
// The closing 完成 event carries that turn's status. Nothing here depends on
// the session's message list, which a retry can append to without a matching
// user message.
export function groupTurns(events: SessionEvent[], prompt: string): TimelineTurn[] {
  const turns: TimelineTurn[] = []
  const open = (goal: string) => {
    turns.push({
      key: `turn-${turns.length + 1}`,
      goal: goal.trim() || '（未填写需求）',
      events: [],
      fileEvents: [],
      status: 'building',
      canceled: false,
      failedStage: '',
      failedHint: '',
    })
  }

  open(prompt)
  for (const ev of events) {
    if (ev.type === 'message') {
      open((ev.payload?.content as string) ?? '')
      continue
    }
    const turn = turns[turns.length - 1]
    turn.events.push(ev)
    if (ev.type === 'file') {
      turn.fileEvents.push(ev)
    } else if (ev.type === 'done') {
      turn.status = ((ev.payload?.status as string) ?? 'ready') as TimelineTurn['status']
      turn.canceled = ev.payload?.canceled === true
    } else if (ev.type === 'error') {
      turn.failedStage = (ev.payload?.stage as string) ?? ''
      turn.failedHint = (ev.payload?.hint as string) ?? ''
    }
  }
  return turns
}

// summariseTurn states the turn's outcome in one line.
export function summariseTurn(turn: TimelineTurn): string {
  if (turn.canceled) return '本轮已取消，保留上一版'
  if (turn.status === 'failed') return '本轮失败'
  if (turn.status === 'building') return '进行中…'
  if (turn.fileEvents.length === 0) return '本轮完成'
  return `本轮完成，修改了 ${turn.fileEvents.length} 个文件`
}

// 回合 stages, in words the user can place. The server sends the stage id.
const STAGE_LABEL: Record<string, string> = {
  plan: '需求拆解',
  spec: '产品规格',
  architecture: '架构设计',
  generate: '代码生成',
  write: '文件写入',
  build: '构建',
}

export function stageLabel(stage: string): string {
  if (!stage) return ''
  return STAGE_LABEL[stage] ?? stage
}

// lastFailure reports the newest failure's stage and advice, so the workspace
// can say what broke next to the retry button instead of leaving the user to
// find the error inside the timeline.
export function lastFailure(events: SessionEvent[]): { stage: string; hint: string } {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (ev.type !== 'error') continue
    return {
      stage: stageLabel((ev.payload?.stage as string) ?? ''),
      hint: (ev.payload?.hint as string) ?? '',
    }
  }
  return { stage: '', hint: '' }
}
