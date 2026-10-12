// 模拟收银台 HTTP surface. Published apps may accept public orders, but
// each visitor needs that order's capability to read or pay it.
package server

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strings"

	"github.com/haobinfei/Atoms-Demo/internal/pay"
)

// payDir resolves an app (session) id to its workspace directory via the
// public-URL index; unknown ids 404 just like /app/{id}/.
func (s *Server) payDir(app string) (string, error) {
	owner, err := s.store.IndexOwner(app)
	if err != nil {
		return "", err
	}
	return s.store.Workspace(owner, app), nil
}

func (s *Server) payAccess(w http.ResponseWriter, r *http.Request, app string, memberOnly bool) (string, bool) {
	owner, err := s.store.IndexOwner(app)
	if err != nil {
		writeError(w, 404, "app not found")
		return "", false
	}
	if s.cfg.Identity != nil {
		p, member := principalFrom(r)
		member = member && p.Workspace.ID == owner
		if memberOnly && !member {
			writeError(w, 404, "app not found")
			return "", false
		}
		if !member && !s.canViewApp(r, owner, app) {
			writeError(w, 404, "app not found")
			return "", false
		}
	}
	return s.store.Workspace(owner, app), true
}

func (s *Server) payOrderAccess(w http.ResponseWriter, r *http.Request, dir, app, id string) (*pay.Order, bool) {
	orders, err := s.payStore.List(dir)
	if err != nil {
		writeError(w, 500, "order lookup failed")
		return nil, false
	}
	for i := range orders {
		order := &orders[i]
		if order.ID == id && order.App == app {
			if s.cfg.Identity != nil {
				owner, _ := s.store.IndexOwner(app)
				p, ok := principalFrom(r)
				member := ok && p.Workspace.ID == owner
				if !member && (order.Capability == "" || r.URL.Query().Get("token") != order.Capability) {
					writeError(w, 404, "order not found")
					return nil, false
				}
			}
			return order, true
		}
	}
	writeError(w, 404, "order not found")
	return nil, false
}

func (s *Server) handlePayCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		App   string     `json:"app"`
		Items []pay.Item `json:"items"`
	}
	// Bound the body: items are capped by the pay store, and the raw
	// request must not be an unbounded disk-filler either.
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(body).Decode(&req); err != nil || strings.TrimSpace(req.App) == "" {
		writeError(w, http.StatusBadRequest, "app and items are required")
		return
	}
	dir, ok := s.payAccess(w, r, req.App, false)
	if !ok {
		return
	}
	order, err := s.payStore.Create(dir, req.App, req.Items)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *Server) handlePayList(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.payAccess(w, r, r.URL.Query().Get("app"), s.cfg.Identity != nil)
	if !ok {
		return
	}
	orders, err := s.payStore.List(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

func (s *Server) handlePayGet(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	dir, ok := s.payAccess(w, r, app, false)
	if !ok {
		return
	}
	order, ok := s.payOrderAccess(w, r, dir, app, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *Server) handlePayPay(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	dir, ok := s.payAccess(w, r, app, false)
	if !ok {
		return
	}
	if _, ok := s.payOrderAccess(w, r, dir, app, r.PathValue("id")); !ok {
		return
	}
	var req struct {
		Card string `json:"card"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "card is required")
		return
	}
	order, err := s.payStore.Pay(dir, r.PathValue("id"), req.Card)
	if errors.Is(err, pay.ErrWrongCard) {
		writeError(w, http.StatusPaymentRequired, err.Error())
		return
	}
	if errors.Is(err, pay.ErrAlreadyPaid) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, pay.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, order)
}

// handlePayCheckout serves the platform-hosted 模拟收银台 page for one
// order. It shouts the simulated nature of the payment from every corner
// and redirects back into the app with ?paid= on success.
func (s *Server) handlePayCheckout(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	dir, ok := s.payAccess(w, r, app, false)
	if !ok {
		return
	}
	id := r.PathValue("id")
	order, ok := s.payOrderAccess(w, r, dir, app, id)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(checkoutPage(order)))
}

// checkoutPage renders the mock checkout; the pay endpoint re-checks the
// card server-side, the page only collects it.
func checkoutPage(order *pay.Order) string {
	id := jsString(order.ID)
	app := jsString(order.App)
	token := jsString(order.Capability)
	var rows strings.Builder
	for _, it := range order.Items {
		rows.WriteString("<tr><td>" + html.EscapeString(it.Name) + "</td><td>" +
			it.PriceString() + " × " + it.QtyString() + "</td><td>" + it.LineString() + "</td></tr>")
	}
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>Atoms 收银台（模拟）</title>
<style>
body{margin:0;background:#0f1115;color:#e5e7eb;font-family:-apple-system,'PingFang SC','Microsoft YaHei',sans-serif;display:flex;justify-content:center;padding:48px 16px}
.box{background:#171a21;border:1px solid #262b36;border-radius:12px;max-width:420px;padding:24px;width:100%}
.banner{background:rgba(249,115,22,.12);border:1px solid #f97316;border-radius:8px;color:#f97316;font-size:13px;line-height:1.6;padding:10px 12px;text-align:center}
h1{font-size:18px;margin:18px 0 4px}
.dim{color:#9ca3af;font-size:12px;margin:0 0 14px}
table{border-collapse:collapse;font-size:14px;width:100%}
td{border-bottom:1px solid #262b36;padding:8px 4px;text-align:left}
td:last-child{text-align:right}
.total{display:flex;font-size:16px;font-weight:700;justify-content:space-between;padding:12px 4px}
label{display:block;font-size:13px;margin:14px 0 6px}
input{background:#0f1115;border:1px solid #262b36;border-radius:8px;color:#e5e7eb;font-size:15px;letter-spacing:.06em;padding:10px 12px;width:100%;box-sizing:border-box}
button{background:#f97316;border:none;border-radius:8px;color:#fff;cursor:pointer;font-size:15px;font-weight:600;margin-top:14px;padding:12px 0;width:100%}
.err{color:#ef4444;font-size:13px;margin-top:10px;min-height:18px;text-align:center}
</style>
</head>
<body>
<div class="box">
<div class="banner">模拟支付 · 不产生真实交易<br />仅接受测试卡号 4242 4242 4242 4242</div>
<h1>收银台</h1>
<p class="dim">订单号 ` + html.EscapeString(order.ID) + `</p>
<table>` + rows.String() + `</table>
<div class="total"><span>合计</span><span>¥` + order.AmountString() + `</span></div>
<label for="card">卡号</label>
<input id="card" value="` + pay.TestCard + `" autocomplete="off" />
<button id="pay">支付 ¥` + order.AmountString() + `</button>
<div class="err" id="err"></div>
</div>
<script>
document.getElementById('pay').onclick = function () {
  fetch('/api/pay/orders/` + id + `/pay?app=` + app + `&token=` + token + `', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ card: document.getElementById('card').value }),
  }).then(function (resp) {
    if (!resp.ok) {
      document.getElementById('err').textContent = resp.status === 402
        ? '该卡号不受理：模拟收银台仅接受测试卡号 4242 4242 4242 4242'
        : '支付失败（' + resp.status + '），请稍后重试'
      return
    }
    window.location.href = '/app/` + app + `/?paid=` + id + `'
  }).catch(function () {
    document.getElementById('err').textContent = '网络错误，请稍后重试'
  })
}
</script>
</body>
</html>`
}

// jsString escapes a value for interpolation into a single-quoted
// JavaScript string literal. Order ids are hex today, but the escape
// keeps a future id-character change from becoming a script-context XSS.
func jsString(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		`"`, `\"`,
		"<", `\u003c`,
		"\n", `\n`,
		"\r", `\r`,
	)
	return replacer.Replace(s)
}
