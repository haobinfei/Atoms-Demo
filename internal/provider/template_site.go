// 电商 (ecommerce) and 站点 (site) templates.
package provider

import (
	"strconv"
	"strings"
)

// ---------- 电商 (ecommerce) ----------

var ecommerceProducts = []struct {
	Name  string
	Price int
	Tag   string
}{
	{"招牌拿铁", 28, "热卖"},
	{"手冲单品", 36, "新豆"},
	{"冷萃咖啡", 32, ""},
	{"燕麦拿铁", 30, ""},
	{"意式浓缩", 18, ""},
	{"芝士蛋糕", 26, "甜点"},
}

func ecommerceApp(spec Spec) (string, string) {
	app := `import { useEffect, useState } from 'react'
import { createOrder, listOrders, consumePaidCallback, type PayItem, type PayOrder } from './atomspay'

type Product = { name: string; price: number; tag?: string }
type CartItem = Product & { qty: number }

const STORAGE = 'app-__NAME__-cart'
const PRODUCTS: Product[] = __PRODUCTS__

export default function App() {
  const [cart, setCart] = useState<CartItem[]>(() => {
    try { return JSON.parse(localStorage.getItem(STORAGE) || '[]') } catch { return [] }
  })
  const [open, setOpen] = useState(false)
  const [orders, setOrders] = useState<PayOrder[]>([])
  const [paidId, setPaidId] = useState<string | null>(null)
  const [checkingOut, setCheckingOut] = useState(false)

  useEffect(() => { localStorage.setItem(STORAGE, JSON.stringify(cart)) }, [cart])

  // 拉取服务端订单 + 处理平台收银台支付成功回调（?paid=）。
  useEffect(() => {
    let alive = true
    const refresh = () => listOrders().then(os => { if (alive) setOrders(os) }).catch(() => {})
    refresh()
    const paid = consumePaidCallback()
    if (paid) {
      setPaidId(paid)
      refresh()
    }
    return () => { alive = false }
  }, [])

  const add = (p: Product) => setCart(cs => {
    const hit = cs.find(c => c.name === p.name)
    return hit ? cs.map(c => c.name === p.name ? { ...c, qty: c.qty + 1 } : c) : [...cs, { ...p, qty: 1 }]
  })
  const change = (name: string, d: number) => setCart(cs =>
    cs.map(c => c.name === name ? { ...c, qty: c.qty + d } : c).filter(c => c.qty > 0))

  const total = cart.reduce((s, c) => s + c.price * c.qty, 0)
  const count = cart.reduce((s, c) => s + c.qty, 0)

  // 下单 → 平台模拟收银台；支付成功后由 ?paid= 回调回到本页。
  const checkout = () => {
    if (checkingOut || cart.length === 0) return
    setCheckingOut(true)
    const items: PayItem[] = cart.map(c => ({ name: c.name, price: c.price, qty: c.qty }))
    createOrder(items)
      .then(order => { window.location.href = order.checkoutUrl })
      .catch(() => { setCheckingOut(false) })
  }

  return (
    <div>
      <header className="top">
        <div className="logo">__NAME__</div>
        <button className="cart-btn" onClick={() => setOpen(true)}>购物车（{count}）</button>
      </header>
      {paidId && <div className="paid-banner">支付成功，订单列表已更新。</div>}
      <section className="hero">
        <h1>用心挑选的每一款</h1>
        <p>产地直采，新鲜烘焙，48 小时内送达。</p>
      </section>
      <main className="grid">
        {PRODUCTS.map((p, i) => (
          <article className="card" key={p.name}>
            <div className="thumb" style={{ background: 'hsl(' + (20 + i * 30) + ' 60% 45%)' }}>
              {p.tag && <span className="tag">{p.tag}</span>}
              {p.name.slice(0, 1)}
            </div>
            <div className="card-body">
              <div className="row"><b>{p.name}</b><span className="price">¥{p.price}</span></div>
              <button className="primary" onClick={() => add(p)}>加入购物车</button>
            </div>
          </article>
        ))}
      </main>
      <footer className="foot">__NAME__ · 由 Atoms Demo 生成</footer>
      {open && (
        <div className="drawer-mask" onClick={() => setOpen(false)}>
          <aside className="drawer" onClick={e => e.stopPropagation()}>
            <div className="row"><h3>购物车</h3><button onClick={() => setOpen(false)}>×</button></div>
            {cart.length === 0 && <p className="dim">还没有商品，去挑几件吧。</p>}
            {cart.map(c => (
              <div className="row item" key={c.name}>
                <span>{c.name}</span>
                <div className="qty">
                  <button onClick={() => change(c.name, -1)}>-</button>
                  <b>{c.qty}</b>
                  <button onClick={() => change(c.name, 1)}>+</button>
                </div>
                <span>¥{c.price * c.qty}</span>
              </div>
            ))}
            {cart.length > 0 && (
              <>
                <div className="row total"><b>合计</b><b>¥{total}</b></div>
                <button className="primary wide" disabled={checkingOut} onClick={checkout}>
                  {checkingOut ? '正在下单…' : '去结算'}
                </button>
              </>
            )}
            <h3 className="orders-title">我的订单</h3>
            {orders.length === 0 && <p className="dim">暂无订单。</p>}
            {orders.map(o => (
              <div className="order-row" key={o.id}>
                <div>
                  <div className="order-id">订单 {o.id.slice(0, 8)}</div>
                  <div className="dim">{o.items.map(it => it.name + '×' + it.qty).join('、')}</div>
                </div>
                <div className="order-side">
                  <span className={'badge ' + (o.status === 'paid' ? 'badge-paid' : 'badge-pending')}>
                    {o.status === 'paid' ? '已支付' : '待支付'}
                  </span>
                  <b>¥{o.amount}</b>
                </div>
              </div>
            ))}
          </aside>
        </div>
      )}
    </div>
  )
}
`
	var prod strings.Builder
	prod.WriteString("[\n")
	for i, p := range ecommerceProducts {
		prod.WriteString("  { name: '" + p.Name + "', price: " + strconv.Itoa(p.Price))
		if p.Tag != "" {
			prod.WriteString(", tag: '" + p.Tag + "'")
		}
		prod.WriteString(" }")
		if i < len(ecommerceProducts)-1 {
			prod.WriteString(",")
		}
		prod.WriteString("\n")
	}
	prod.WriteString("]")

	styles := `
.top{position:sticky;top:0;display:flex;justify-content:space-between;align-items:center;padding:14px 24px;background:rgba(15,17,21,.9);backdrop-filter:blur(8px);border-bottom:1px solid var(--border)}
.logo{font-weight:700;font-size:18px;color:var(--accent)}
.cart-btn{background:var(--panel);border:1px solid var(--border);color:var(--text);padding:8px 16px}
.hero{text-align:center;padding:64px 20px 40px}
.hero h1{font-size:34px;margin:0 0 10px}
.hero p{color:var(--dim);margin:0}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:18px;padding:0 24px 40px;max-width:1080px;margin:0 auto}
.card{background:var(--panel);border:1px solid var(--border);border-radius:14px;overflow:hidden}
.thumb{height:150px;display:flex;align-items:center;justify-content:center;font-size:44px;font-weight:700;color:rgba(255,255,255,.85);position:relative}
.tag{position:absolute;top:10px;left:10px;background:rgba(0,0,0,.45);font-size:12px;padding:2px 10px;border-radius:999px}
.card-body{padding:14px}
.row{display:flex;justify-content:space-between;align-items:center;gap:10px}
.price{color:var(--accent);font-weight:600}
.primary{background:var(--accent);color:#111;padding:9px 0;width:100%;font-weight:600;margin-top:10px}
.primary.wide{margin-top:14px}
.foot{text-align:center;color:var(--dim);font-size:13px;padding:24px;border-top:1px solid var(--border)}
.drawer-mask{position:fixed;inset:0;background:rgba(0,0,0,.5);display:flex;justify-content:flex-end}
.drawer{background:var(--panel);width:360px;max-width:90vw;height:100%;padding:20px;border-left:1px solid var(--border);overflow:auto}
.drawer h3{margin:0}
.drawer .row button{background:none;color:var(--dim);font-size:20px}
.dim{color:var(--dim)}
.item{padding:10px 0;border-bottom:1px solid var(--border);font-size:14px}
.qty{display:flex;align-items:center;gap:10px}
.qty button{background:#1f2430;color:var(--text);width:26px;height:26px;border-radius:6px}
.total{padding-top:14px;font-size:16px}
.paid-banner{background:rgba(34,197,94,.12);border:1px solid #22c55e;border-radius:8px;color:#4ade80;font-size:14px;margin:14px auto 0;max-width:1080px;padding:10px 14px;text-align:center}
.orders-title{border-top:1px solid var(--border);margin:20px 0 6px;padding-top:16px}
.order-row{align-items:center;border-bottom:1px solid var(--border);display:flex;font-size:14px;justify-content:space-between;padding:10px 0}
.order-id{font-weight:600}
.order-side{align-items:flex-end;display:flex;flex-direction:column;gap:4px}
.badge{border-radius:999px;font-size:12px;padding:2px 10px}
.badge-paid{background:rgba(34,197,94,.15);color:#4ade80}
.badge-pending{background:rgba(249,115,22,.15);color:var(--accent)}
`
	return strings.NewReplacer("__NAME__", spec.Name, "__PRODUCTS__", prod.String()).Replace(app),
		css(spec.Accent, styles)
}

// ---------- 站点 (site: portfolio / restaurant / blog / landing) ----------

type siteContent struct {
	Tagline  string
	HeroSub  string
	CTA      string
	Sections [][2]string // [heading, sub]
}

var siteContents = map[string]siteContent{
	"portfolio": {
		Tagline: "你好，我是一名产品设计师",
		HeroSub: "专注体验设计与设计系统，相信好的设计是诚实的。",
		CTA:     "查看作品",
		Sections: [][2]string{
			{"精选作品", "近三年最有代表性的六个项目"},
			{"关于我", "八年设计经验，服务过多个从 0 到 1 的产品"},
			{"联系我", "欢迎交流与合作"},
		},
	},
	"restaurant": {
		Tagline: "__NAME__ · 好味道值得等待",
		HeroSub: "当季食材，每日现做。这个周末，来吃点好的。",
		CTA:     "查看菜单",
		Sections: [][2]string{
			{"招牌菜单", "主厨推荐，每道都是经典"},
			{"我们的故事", "从一间小店开始，做了十二年"},
			{"门店与预订", "欢迎提前预订座位"},
		},
	},
	"blog": {
		Tagline: "__NAME__",
		HeroSub: "记录关于产品、设计与技术的思考，每周更新。",
		CTA:     "开始阅读",
		Sections: [][2]string{
			{"最新文章", "最近的五篇思考"},
			{"专栏", "系统设计 · 交互细节 · 工程实践"},
			{"订阅", "把新文章送到你的邮箱"},
		},
	},
	"landing": {
		Tagline: "__NAME__ · 让工作更高效",
		HeroSub: "一个现代化的协作平台，帮助团队把想法快速变成结果。",
		CTA:     "免费开始",
		Sections: [][2]string{
			{"核心功能", "为高频协作场景而生"},
			{"客户评价", "他们已经把效率提升了 40%"},
			{"价格", "按需选择，随时升级"},
		},
	},
}

func siteApp(spec Spec) (string, string) {
	content := siteContents[spec.SiteKind]
	app := `import { useState } from 'react'

const SECTIONS = __SECTIONS__
const NAV = ['首页', '功能', '评价', '联系']

export default function App() {
  const [active, setActive] = useState('首页')
  const tiles = ['#1f2937', '#3f3f46', '#44403c', '#334155', '#3b3b3b', '#403a3f']
  return (
    <div>
      <nav className="nav">
        <b className="brand">__NAME__</b>
        <div className="links">
          {NAV.map(n => (
            <a key={n} className={active === n ? 'on' : ''} onClick={() => setActive(n)}>{n}</a>
          ))}
        </div>
      </nav>
      <header className="hero">
        <h1>__TAGLINE__</h1>
        <p>__HEROSUB__</p>
        <button className="cta">__CTA__</button>
      </header>
      {SECTIONS.map((s, i) => (
        <section className="section" key={s[0]} id={'sec-' + i}>
          <h2>{s[0]}</h2>
          <p className="sub">{s[1]}</p>
          <div className="tiles">
            {Array.from({ length: i === 0 ? 3 : 2 }).map((_, j) => (
              <div className="tile" key={j} style={{ background: tiles[(i * 2 + j) % tiles.length] }}>
                <span>{s[0]} · {j + 1}</span>
              </div>
            ))}
          </div>
        </section>
      ))}
      <footer className="foot">__NAME__ · 由 Atoms Demo 生成</footer>
    </div>
  )
}
`
	var secs strings.Builder
	for i, s := range content.Sections {
		if i > 0 {
			secs.WriteString(", ")
		}
		secs.WriteString("['" + s[0] + "', '" + s[1] + "']")
	}
	repl := strings.NewReplacer(
		"__NAME__", spec.Name,
		"__TAGLINE__", strings.ReplaceAll(content.Tagline, "__NAME__", spec.Name),
		"__HEROSUB__", content.HeroSub,
		"__CTA__", content.CTA,
		"__SECTIONS__", "["+secs.String()+"]",
	)
	styles := `
.nav{position:sticky;top:0;display:flex;justify-content:space-between;align-items:center;padding:14px 28px;background:rgba(15,17,21,.9);backdrop-filter:blur(8px);border-bottom:1px solid var(--border)}
.brand{color:var(--accent)}
.links{display:flex;gap:18px}
.links a{color:var(--dim);cursor:pointer;font-size:14px}
.links a.on,.links a:hover{color:var(--text)}
.hero{text-align:center;padding:90px 20px 60px}
.hero h1{font-size:38px;margin:0 0 14px}
.hero p{color:var(--dim);font-size:16px;margin:0 0 26px}
.cta{background:var(--accent);color:#111;padding:12px 32px;font-weight:700;font-size:15px}
.section{max-width:960px;margin:0 auto;padding:50px 20px}
.section h2{font-size:26px;margin:0 0 6px}
.sub{color:var(--dim);margin:0 0 24px}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:16px}
.tile{aspect-ratio:16/9;border-radius:12px;display:flex;align-items:flex-end;padding:14px;border:1px solid var(--border)}
.tile span{color:rgba(255,255,255,.75);font-size:13px}
.foot{text-align:center;color:var(--dim);font-size:13px;padding:28px;border-top:1px solid var(--border)}
`
	return repl.Replace(app), css(spec.Accent, styles)
}
