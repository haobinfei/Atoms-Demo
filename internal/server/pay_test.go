package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

// payTestServer starts one session with the deterministic template
// 生成引擎 and waits for the build so order tests run against a real
// generated ecommerce app.
func payTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	return ts, id
}

func postRaw(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

type payOrder struct {
	ID        string `json:"id"`
	App       string `json:"app"`
	Amount    float64
	Status    string
	CardLast4 string `json:"cardLast4"`
	CreatedAt string `json:"createdAt"`
	PaidAt    string `json:"paidAt"`
}

func placeOrder(t *testing.T, tsURL, body string) (int, payOrder) {
	t.Helper()
	code, raw := postRaw(t, tsURL+"/api/pay/orders", body)
	var order payOrder
	_ = json.Unmarshal(raw, &order)
	return code, order
}

// TestPayFullLoop: 加购后的下单 → 收银台 → 测试卡支付 → 回调 → 订单
// 列表，全部经 HTTP 完成，金额由服务端按 items 计算。
func TestPayFullLoop(t *testing.T) {
	ts, id := payTestServer(t)

	code, order := placeOrder(t, ts.URL,
		fmt.Sprintf(`{"app":%q,"items":[{"name":"招牌拿铁","price":28,"qty":2},{"name":"芝士蛋糕","price":26,"qty":1}]}`, id))
	if code != http.StatusOK {
		t.Fatalf("create order status = %d", code)
	}
	if order.ID == "" || order.Status != "pending" || order.Amount != 82 {
		t.Fatalf("order = %+v", order)
	}

	// 订单列表：pending 可见，未支付字段为空。
	code, raw := get(t, ts.URL+"/api/pay/orders?app="+id)
	if code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	var list []payOrder
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 {
		t.Fatalf("list = %s", raw)
	}
	if list[0].CardLast4 != "" || list[0].PaidAt != "" {
		t.Fatalf("pending order leaked paid fields: %+v", list[0])
	}

	// 非测试卡号被拒。
	code, _ = postRaw(t, ts.URL+"/api/pay/orders/"+order.ID+"/pay?app="+id,
		`{"card":"4111 1111 1111 1111"}`)
	if code != http.StatusPaymentRequired {
		t.Fatalf("wrong card status = %d, want 402", code)
	}

	// 测试卡号（带空格写法）支付成功。
	code, raw = postRaw(t, ts.URL+"/api/pay/orders/"+order.ID+"/pay?app="+id,
		`{"card":"4242 4242 4242 4242"}`)
	if code != http.StatusOK {
		t.Fatalf("pay status = %d, body = %s", code, raw)
	}
	var paid payOrder
	if err := json.Unmarshal(raw, &paid); err != nil {
		t.Fatalf("decode paid: %v", err)
	}
	if paid.Status != "paid" || paid.CardLast4 != "4242" || paid.PaidAt == "" {
		t.Fatalf("paid order = %+v", paid)
	}

	// 重复支付是冲突，不是成功。
	code, _ = postRaw(t, ts.URL+"/api/pay/orders/"+order.ID+"/pay?app="+id,
		`{"card":"4242424242424242"}`)
	if code != http.StatusConflict {
		t.Fatalf("re-pay status = %d, want 409", code)
	}

	// 列表里已是已支付。
	_, raw = get(t, ts.URL+"/api/pay/orders?app="+id)
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 || list[0].Status != "paid" {
		t.Fatalf("list after pay = %s", raw)
	}

	// 不带空格的测试卡号同样受理（归一化路径的正向断言）。
	_, second := placeOrder(t, ts.URL,
		fmt.Sprintf(`{"app":%q,"items":[{"name":"意式浓缩","price":18,"qty":1}]}`, id))
	code, raw = postRaw(t, ts.URL+"/api/pay/orders/"+second.ID+"/pay?app="+id,
		`{"card":"4242424242424242"}`)
	if code != http.StatusOK {
		t.Fatalf("pay with bare card status = %d, body = %s", code, raw)
	}
}

// TestPayOrdersPersistAcrossRestart: 订单随会话落盘，新进程（新设备/
// 浏览器同理）打开同一应用仍可见历史订单。
func TestPayOrdersPersistAcrossRestart(t *testing.T) {
	ts, dir := newTestServerDir(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	_, order := placeOrder(t, ts.URL,
		fmt.Sprintf(`{"app":%q,"items":[{"name":"手冲单品","price":36,"qty":1}]}`, id))
	postRaw(t, ts.URL+"/api/pay/orders/"+order.ID+"/pay?app="+id,
		`{"card":"4242424242424242"}`)

	// A fresh server process over the same workspace dir.
	ts2 := httptest.NewServer(server.New(server.Config{
		WorkspaceDir: dir, DevAuth: true, Prov: provider.NewTemplate(),
	}).Handler())
	defer ts2.Close()
	code, raw := get(t, ts2.URL+"/api/pay/orders?app="+id)
	if code != http.StatusOK {
		t.Fatalf("list after restart status = %d", code)
	}
	var list []payOrder
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 || list[0].Status != "paid" {
		t.Fatalf("list after restart = %s", raw)
	}
}

// TestPayCheckoutPage: 平台托管收银台明示模拟性质，携带回调地址与
// 预填测试卡号。
func TestPayCheckoutPage(t *testing.T) {
	ts, id := payTestServer(t)
	_, order := placeOrder(t, ts.URL,
		fmt.Sprintf(`{"app":%q,"items":[{"name":"招牌拿铁","price":28,"qty":1}]}`, id))

	code, raw := get(t, ts.URL+"/pay/checkout/"+order.ID+"?app="+id)
	if code != http.StatusOK {
		t.Fatalf("checkout page status = %d", code)
	}
	html := string(raw)
	for _, want := range []string{"模拟支付", "不产生真实交易", "4242 4242 4242 4242",
		"/app/" + id + "/?paid=" + order.ID, "¥28"} {
		if !strings.Contains(html, want) {
			t.Fatalf("checkout page missing %q", want)
		}
	}

	if code, _ := get(t, ts.URL+"/pay/checkout/nope123?app="+id); code != http.StatusNotFound {
		t.Fatalf("unknown order checkout status = %d, want 404", code)
	}
}

// TestPayValidation: 下单参数与未知应用都被拒。
func TestPayValidation(t *testing.T) {
	ts, id := payTestServer(t)

	if code, _ := placeOrder(t, ts.URL, `{"app":"`+id+`","items":[]}`); code != http.StatusBadRequest {
		t.Fatalf("empty items status = %d, want 400", code)
	}
	if code, _ := placeOrder(t, ts.URL,
		fmt.Sprintf(`{"app":%q,"items":[{"name":"x","price":-1,"qty":1}]}`, id)); code != http.StatusBadRequest {
		t.Fatalf("negative price status = %d, want 400", code)
	}
	if code, _ := placeOrder(t, ts.URL,
		fmt.Sprintf(`{"app":%q,"items":[{"name":"x","price":1,"qty":0}]}`, id)); code != http.StatusBadRequest {
		t.Fatalf("zero qty status = %d, want 400", code)
	}
	if code, _ := get(t, ts.URL+"/api/pay/orders?app=unknown99"); code != http.StatusNotFound {
		t.Fatalf("unknown app list status = %d, want 404", code)
	}
	if code, _ := postRaw(t, ts.URL+"/api/pay/orders",
		`{"app":"unknown99","items":[{"name":"x","price":1,"qty":1}]}`); code != http.StatusNotFound {
		t.Fatalf("unknown app create status = %d, want 404", code)
	}
}

// TestEcommerceAppsGetPaySDK: 生成的电商应用带 AtomsPay SDK（构建时
// 注入），应用代码通过 SDK 完成下单与订单列表。
func TestEcommerceAppsGetPaySDK(t *testing.T) {
	ts, id := payTestServer(t)

	_, raw := get(t, ts.URL+"/api/sessions/"+id+"/files")
	if !strings.Contains(string(raw), "src/atomspay.ts") {
		t.Fatalf("files missing atomspay SDK: %s", raw)
	}
	_, raw = get(t, ts.URL+"/api/sessions/"+id+"/file/src/App.tsx")
	if !strings.Contains(string(raw), "createOrder") || !strings.Contains(string(raw), "listOrders") {
		t.Fatalf("ecommerce app not wired to SDK")
	}
}

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// TestPayAPIPublicUnderOIDC: behind AuthSphere, session APIs still demand
// a JWT but the 模拟收银台 stays public — a shared app's visitor must
// place and list orders without logging in (review finding on #10).
func TestPayAPIPublicUnderOIDC(t *testing.T) {
	env := newAuthTestServer(t)
	tok := env.token(t, "user-a")

	// Sanity: the auth gate itself is intact for session APIs.
	if resp, err := http.Post(env.ts.URL+"/api/sessions", "application/json",
		strings.NewReader(`{"prompt":"一家咖啡店"}`)); err != nil {
		t.Fatalf("unauthenticated create: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated create status = %d, want 401", resp.StatusCode)
		}
	}
	// Unknown app resolves to 404, not the gate's 401.
	if code, _ := get(t, env.ts.URL+"/api/pay/orders?app=nope00"); code != http.StatusNotFound {
		t.Fatalf("public pay list status = %d, want 404", code)
	}

	// Owner creates an ecommerce session over the authed API.
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/sessions",
		strings.NewReader(`{"prompt":"一家咖啡店的电商网站"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed create: %v", err)
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	id := created.SessionID
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := getWithToken(t, env.ts.URL+"/api/sessions/"+id, tok)
		var sess struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&sess)
		r.Body.Close()
		if sess.Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never became ready: %s", sess.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The shared visitor (no token) places and lists orders.
	code, order := placeOrder(t, env.ts.URL,
		fmt.Sprintf(`{"app":%q,"items":[{"name":"招牌拿铁","price":28,"qty":1}]}`, id))
	if code != http.StatusOK {
		t.Fatalf("visitor create order status = %d", code)
	}
	if code, _ := postRaw(t, env.ts.URL+"/api/pay/orders/"+order.ID+"/pay?app="+id,
		`{"card":"4242424242424242"}`); code != http.StatusOK {
		t.Fatalf("visitor pay status = %d", code)
	}
	code, raw := get(t, env.ts.URL+"/api/pay/orders?app="+id)
	if code != http.StatusOK {
		t.Fatalf("visitor list status = %d", code)
	}
	var list []payOrder
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 || list[0].Status != "paid" {
		t.Fatalf("visitor list = %s", raw)
	}
}
