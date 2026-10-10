// AtomsPay SDK: the platform injects this module into every generated
// app at build time so ecommerce apps share one 下单/收银台/订单闭环
// instead of hand-rolling payment forms. Apps import it by relative
// path: import { createOrder, listOrders } from './atomspay'
//
// The source is a Go raw string: no backticks and no TS template literals
// allowed inside.
package provider

// AtomsPaySDKPath is where the injected module lands in the app tree.
const AtomsPaySDKPath = "src/atomspay.ts"

// AtomsPaySDK is the injected module's full TypeScript source.
const AtomsPaySDK = `// AtomsPay 模拟收银台 SDK（由平台在构建时注入，请勿修改本文件）。
// 本 SDK 只对接 Atoms Demo 平台的模拟支付，不产生任何真实交易。
export type PayItem = { name: string; price: number; qty: number }
export type PayOrder = {
  id: string
  app: string
  items: PayItem[]
  amount: number
  status: string
  cardLast4?: string
  createdAt: string
  paidAt?: string
}

// appId reads the hosting session id from the URL the app is served at
// (/app/<id>/...), so generated apps need zero configuration.
function appId(): string {
  return window.location.pathname.split('/')[2] || ''
}

// createOrder 下单并返回收银台地址；把页面跳转过去即完成闭环。
export async function createOrder(items: PayItem[]): Promise<{ id: string; checkoutUrl: string }> {
  const resp = await fetch('/api/pay/orders', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ app: appId(), items }),
  })
  if (!resp.ok) throw new Error('下单失败')
  const order = await resp.json()
  return { id: order.id, checkoutUrl: '/pay/checkout/' + order.id + '?app=' + appId() }
}

// listOrders 拉取本应用的全部订单（服务端持久化，换设备仍可见）。
export async function listOrders(): Promise<PayOrder[]> {
  const resp = await fetch('/api/pay/orders?app=' + encodeURIComponent(appId()))
  if (!resp.ok) return []
  return resp.json()
}

// consumePaidCallback 读取平台收银台支付成功后的 ?paid= 回调参数，
// 返回刚支付的订单号并清理地址栏；无回调时返回 null。
export function consumePaidCallback(): string | null {
  const params = new URLSearchParams(window.location.search)
  const paid = params.get('paid')
  if (!paid) return null
  params.delete('paid')
  const qs = params.toString()
  window.history.replaceState(null, '', window.location.pathname + (qs ? '?' + qs : '') + window.location.hash)
  return paid
}
`
