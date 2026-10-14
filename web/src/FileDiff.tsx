import { useEffect, useState } from 'react'
import { getChange, type ChangeDetail, type ChangeRecord } from './api'

// How many diff lines are rendered at once. A legitimate diff can be long;
// the cap exists so one pathological file can never freeze the timeline
// (ticket #54's "不阻塞页面").
const MAX_RENDERED_LINES = 1500

type LoadState =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; detail: ChangeDetail }

// FileDiff renders one file's 改动记录 (Change Record) as a unified diff.
//
// The diff is fetched when the row is opened, never with the page: a 回合 can
// announce dozens of files, and shipping every diff to every timeline render
// is exactly what the ticket's "不阻塞页面" rules out. A record that cannot be
// diffed — binary or oversized — says so instead of rendering an empty box.
export default function FileDiff({
  sessionId,
  record,
}: {
  sessionId: string
  record: ChangeRecord
}) {
  const [state, setState] = useState<LoadState>({ status: 'loading' })

  useEffect(() => {
    let alive = true
    setState({ status: 'loading' })
    getChange(sessionId, record.seq).then(
      (detail) => {
        if (alive) setState({ status: 'ready', detail })
      },
      () => {
        if (alive) setState({ status: 'error' })
      },
    )
    return () => {
      alive = false
    }
  }, [sessionId, record.seq])

  // Stated before any fetch: these records never carry a diff body, so
  // loading one would only produce the same answer slower.
  if (record.binary) {
    return <div className="fd-note">已改动：二进制文件，不展示差异。</div>
  }
  if (record.tooLarge) {
    return <div className="fd-note">已改动：文件较大，不展示差异。</div>
  }
  if (record.kind === 'unchanged') {
    return <div className="fd-note">重新生成，内容未变化。</div>
  }
  if (state.status === 'loading') {
    return <div className="fd-note">差异加载中…</div>
  }
  if (state.status === 'error') {
    return <div className="fd-note">差异加载失败，收起再展开可以重试。</div>
  }
  return <DiffBody diff={state.detail.diff} />
}

function DiffBody({ diff }: { diff: string }) {
  let lines = diff.split('\n')
  // The final newline leaves an empty trailing element; it is not a line.
  if (lines.length > 0 && lines[lines.length - 1] === '') lines = lines.slice(0, -1)
  const shown = lines.slice(0, MAX_RENDERED_LINES)
  return (
    <div className="fd-diff-wrap">
      <pre className="fd-diff">
        {shown.map((line, i) => (
          <span key={i} className={lineClass(line)}>
            {line}
          </span>
        ))}
      </pre>
      {lines.length > shown.length && (
        <div className="fd-note">其余 {lines.length - shown.length} 行已省略。</div>
      )}
    </div>
  )
}

function lineClass(line: string): string {
  if (line.startsWith('@@')) return 'fd-diff-hunk'
  if (line.startsWith('---') || line.startsWith('+++')) return 'fd-diff-file'
  if (line.startsWith('+')) return 'fd-diff-add'
  if (line.startsWith('-')) return 'fd-diff-del'
  if (line.startsWith('\\')) return 'fd-diff-meta'
  return ''
}
