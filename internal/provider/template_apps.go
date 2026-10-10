// The per-category template bodies. Generated apps are real React: state,
// effects, localStorage, canvas charts. Placeholders __NAME__ / __ACCENT__
// are substituted by the builders (string concat, never fmt: CSS is full of
// percent signs).
package provider

import "strings"

func css(accent, extra string) string {
	base := `:root{--accent:__ACCENT__;--bg:#0f1115;--panel:#171a21;--border:#262b36;--text:#e5e7eb;--dim:#9ca3af}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font-family:-apple-system,'PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif}
button{font:inherit;cursor:pointer;border:none;border-radius:8px}
input{font:inherit}
`
	return strings.NewReplacer("__ACCENT__", accent).Replace(base + extra)
}

// ---------- 待办 (todo) ----------

func todoApp(spec Spec) (string, string) {
	app := `import { useEffect, useState } from 'react'

type Todo = { id: number; text: string; done: boolean }

const STORAGE = 'app-__NAME__-todos'

export default function App() {
  const [todos, setTodos] = useState<Todo[]>(() => {
    try { return JSON.parse(localStorage.getItem(STORAGE) || '[]') } catch { return [] }
  })
  const [draft, setDraft] = useState('')
  const [filter, setFilter] = useState<'all' | 'active' | 'done'>('all')

  useEffect(() => { localStorage.setItem(STORAGE, JSON.stringify(todos)) }, [todos])

  const add = () => {
    const text = draft.trim()
    if (!text) return
    setTodos([{ id: Date.now(), text, done: false }, ...todos])
    setDraft('')
  }
  const toggle = (id: number) => setTodos(todos.map(t => t.id === id ? { ...t, done: !t.done } : t))
  const remove = (id: number) => setTodos(todos.filter(t => t.id !== id))
  const clearDone = () => setTodos(todos.filter(t => !t.done))

  const shown = todos.filter(t => filter === 'all' || (filter === 'done') === t.done)
  const left = todos.filter(t => !t.done).length

  return (
    <div className="wrap">
      <header className="head">
        <h1>__NAME__</h1>
        <p>今日事，今日毕。</p>
      </header>
      <div className="composer">
        <input value={draft} onChange={e => setDraft(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && add()} placeholder="要做点什么？" autoFocus />
        <button className="primary" onClick={add}>添加</button>
      </div>
      <div className="filters">
        {(['all', 'active', 'done'] as const).map(f => (
          <button key={f} className={filter === f ? 'on' : ''} onClick={() => setFilter(f)}>
            {f === 'all' ? '全部' : f === 'active' ? '进行中' : '已完成'}
          </button>
        ))}
        <span className="left">{left} 件待办</span>
      </div>
      <ul className="list">
        {shown.map(t => (
          <li key={t.id} className={t.done ? 'done' : ''}>
            <label>
              <input type="checkbox" checked={t.done} onChange={() => toggle(t.id)} />
              <span>{t.text}</span>
            </label>
            <button className="del" onClick={() => remove(t.id)} aria-label="删除">×</button>
          </li>
        ))}
        {shown.length === 0 && <li className="empty">这里空空如也</li>}
      </ul>
      {todos.some(t => t.done) && <button className="clear" onClick={clearDone}>清除已完成</button>}
    </div>
  )
}
`
	styles := `
.wrap{max-width:560px;margin:0 auto;padding:48px 20px}
.head h1{margin:0 0 4px;font-size:28px}
.head p{color:var(--dim);margin:0 0 24px}
.composer{display:flex;gap:8px;margin-bottom:12px}
.composer input{flex:1;background:var(--panel);border:1px solid var(--border);color:var(--text);border-radius:8px;padding:10px 14px;outline:none}
.composer input:focus{border-color:var(--accent)}
.primary{background:var(--accent);color:#111;padding:10px 20px;font-weight:600}
.filters{display:flex;gap:6px;align-items:center;margin-bottom:8px}
.filters button{background:var(--panel);border:1px solid var(--border);color:var(--dim);padding:4px 12px;border-radius:999px;font-size:13px}
.filters button.on{color:var(--accent);border-color:var(--accent)}
.left{margin-left:auto;font-size:13px;color:var(--dim)}
.list{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:6px}
.list li{display:flex;align-items:center;gap:8px;background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:10px 14px}
.list li.done span{text-decoration:line-through;color:var(--dim)}
.list label{flex:1;display:flex;align-items:center;gap:10px;cursor:pointer}
.list input[type=checkbox]{width:18px;height:18px;accent-color:var(--accent)}
.del{background:none;color:var(--dim);font-size:18px;padding:0 4px}
.del:hover{color:#ef4444}
.empty{justify-content:center;color:var(--dim)}
.clear{margin-top:12px;background:none;color:var(--dim);padding:6px 0}
.clear:hover{color:#ef4444}
`
	return strings.NewReplacer("__NAME__", spec.Name).Replace(app), css(spec.Accent, styles)
}

// ---------- 数据看板 (dashboard) ----------

func dashboardApp(spec Spec) (string, string) {
	app := `import { useEffect, useRef, useState } from 'react'

const REVENUE = [42, 48, 45, 61, 58, 72, 69, 84, 79, 92, 88, 104]
const MONTHS = ['1月', '2月', '3月', '4月', '5月', '6月', '7月', '8月', '9月', '10月', '11月', '12月']
const CHANNELS = [
  { name: '自然搜索', value: 46 },
  { name: '社交媒体', value: 27 },
  { name: '直接访问', value: 16 },
  { name: '推荐链接', value: 11 },
]
const ROWS = [
  { id: 'A-1042', item: '年度专业版', amount: '¥1,999', status: '已支付' },
  { id: 'A-1041', item: '月度团队版', amount: '¥299', status: '已支付' },
  { id: 'A-1040', item: '月度专业版', amount: '¥99', status: '待支付' },
  { id: 'A-1039', item: '年度团队版', amount: '¥2,999', status: '已支付' },
]

function LineChart({ data }: { data: number[] }) {
  const ref = useRef<HTMLCanvasElement>(null)
  useEffect(() => {
    const c = ref.current!
    const ctx = c.getContext('2d')!
    const W = (c.width = c.clientWidth * 2), H = (c.height = c.clientHeight * 2)
    const max = Math.max(...data) * 1.15, pad = 20
    const stepX = (W - pad * 2) / (data.length - 1)
    ctx.strokeStyle = '#262b36'; ctx.lineWidth = 1
    for (let g = 0; g < 4; g++) {
      const y = pad + (H - pad * 2) * g / 3
      ctx.beginPath(); ctx.moveTo(pad, y); ctx.lineTo(W - pad, y); ctx.stroke()
    }
    ctx.beginPath()
    data.forEach((v, i) => {
      const x = pad + stepX * i, y = H - pad - (v / max) * (H - pad * 2)
      i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)
    })
    ctx.strokeStyle = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()
    ctx.lineWidth = 4; ctx.stroke()
    ctx.lineTo(W - pad, H - pad); ctx.lineTo(pad, H - pad); ctx.closePath()
    ctx.globalAlpha = 0.12; ctx.fillStyle = ctx.strokeStyle; ctx.fill(); ctx.globalAlpha = 1
  }, [data])
  return <canvas ref={ref} className="chart" />
}

export default function App() {
  const [nav, setNav] = useState('总览')
  const items = ['总览', '收入', '用户', '渠道']
  const kpis = [
    { label: '本月收入', value: '¥ 84.2k', delta: '+12.4%' },
    { label: '活跃用户', value: '6,318', delta: '+8.1%' },
    { label: '转化率', value: '3.42%', delta: '+0.6%' },
    { label: '客单价', value: '¥ 268', delta: '-1.2%' },
  ]
  return (
    <div className="shell">
      <aside className="side">
        <div className="brand">__NAME__</div>
        {items.map(it => (
          <button key={it} className={nav === it ? 'on' : ''} onClick={() => setNav(it)}>{it}</button>
        ))}
      </aside>
      <main className="main">
        <h2>{nav}</h2>
        <div className="kpis">
          {kpis.map(k => (
            <div className="kpi" key={k.label}>
              <div className="k-label">{k.label}</div>
              <div className="k-value">{k.value}</div>
              <div className={'k-delta ' + (k.delta.startsWith('+') ? 'up' : 'down')}>{k.delta}</div>
            </div>
          ))}
        </div>
        <div className="panel">
          <h3>月度收入趋势</h3>
          <LineChart data={REVENUE} />
          <div className="months">{MONTHS.map(m => <span key={m}>{m}</span>)}</div>
        </div>
        <div className="two">
          <div className="panel">
            <h3>流量渠道</h3>
            {CHANNELS.map(ch => (
              <div className="bar-row" key={ch.name}>
                <span>{ch.name}</span>
                <div className="bar"><div style={{ width: ch.value + '%' }} /></div>
                <b>{ch.value}%</b>
              </div>
            ))}
          </div>
          <div className="panel">
            <h3>最近订单</h3>
            <table>
              <tbody>
                {ROWS.map(r => (
                  <tr key={r.id}>
                    <td>{r.id}</td><td>{r.item}</td><td>{r.amount}</td>
                    <td><span className={'tag ' + (r.status === '已支付' ? 'ok' : '')}>{r.status}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </main>
    </div>
  )
}
`
	styles := `
.shell{display:grid;grid-template-columns:200px 1fr;min-height:100vh}
.side{background:var(--panel);border-right:1px solid var(--border);padding:20px 12px;display:flex;flex-direction:column;gap:4px}
.brand{font-weight:700;color:var(--accent);padding:0 10px 16px;font-size:17px}
.side button{background:none;color:var(--dim);text-align:left;padding:9px 12px;border-radius:8px}
.side button.on,.side button:hover{background:#1f2430;color:var(--text)}
.main{padding:28px;overflow:auto}
.main h2{margin:0 0 20px}
.kpis{display:grid;grid-template-columns:repeat(4,1fr);gap:14px;margin-bottom:16px}
.kpi{background:var(--panel);border:1px solid var(--border);border-radius:12px;padding:16px}
.k-label{color:var(--dim);font-size:13px}
.k-value{font-size:24px;font-weight:700;margin:6px 0}
.k-delta{font-size:12px}.up{color:#22c55e}.down{color:#ef4444}
.panel{background:var(--panel);border:1px solid var(--border);border-radius:12px;padding:18px;margin-bottom:16px}
.panel h3{margin:0 0 14px;font-size:15px}
.chart{width:100%;height:220px}
.months{display:flex;justify-content:space-between;color:var(--dim);font-size:11px;padding:0 8px}
.two{display:grid;grid-template-columns:1fr 1fr;gap:16px}
.bar-row{display:grid;grid-template-columns:70px 1fr 40px;gap:10px;align-items:center;font-size:13px;margin:10px 0}
.bar{height:8px;background:#1f2430;border-radius:999px;overflow:hidden}
.bar div{height:100%;background:var(--accent);border-radius:999px}
table{width:100%;border-collapse:collapse;font-size:13px}
td{padding:8px 6px;border-bottom:1px solid var(--border)}
.tag{font-size:12px;padding:2px 8px;border-radius:999px;border:1px solid var(--border);color:var(--dim)}
.tag.ok{color:#22c55e;border-color:#22c55e}
@media(max-width:900px){.shell{grid-template-columns:1fr}.side{flex-direction:row;align-items:center}.kpis,.two{grid-template-columns:1fr}}
`
	return strings.NewReplacer("__NAME__", spec.Name).Replace(app), css(spec.Accent, styles)
}
