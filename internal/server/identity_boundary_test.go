package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
)

type boundaryActor struct {
	name, email string
	client      *http.Client
	member      bool
}

type boundaryHTTP struct {
	url, clientIP string
	mail          *capturedMail
}

func (h boundaryHTTP) request(t *testing.T, actor boundaryActor, method, path, body string, want int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, h.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", h.url)
	req.Header.Set("X-Real-IP", h.clientIP)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := actor.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s as %s: %v", method, path, actor.name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s as %s = %d, want %d; body=%s", method, path, actor.name, resp.StatusCode, want, raw)
	}
	return raw
}

func (h boundaryHTTP) register(t *testing.T, name, email string) boundaryActor {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	actor := boundaryActor{name: name, email: email, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	credentials := fmt.Sprintf(`{"email":%q,"password":"boundary-long-password"}`, email)
	h.request(t, actor, http.MethodPost, "/api/auth/register", credentials, http.StatusAccepted)
	h.request(t, actor, http.MethodPost, "/api/auth/verify", fmt.Sprintf(`{"token":%q}`, h.mail.token(t, "verify")), http.StatusNoContent)
	h.request(t, actor, http.MethodPost, "/api/auth/login", credentials, http.StatusOK)
	return actor
}

func (h boundaryHTTP) waitReady(t *testing.T, actor boundaryActor, id string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var session struct {
			Status string `json:"status"`
		}
		raw := h.request(t, actor, http.MethodGet, "/api/sessions/"+id, "", http.StatusOK)
		if err := json.Unmarshal(raw, &session); err != nil {
			t.Fatal(err)
		}
		if session.Status == "ready" {
			return
		}
		if session.Status == "failed" {
			t.Fatalf("project build failed: %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("project did not finish building")
}

// All identities, membership changes, generation, publication and orders pass
// through HTTP. Only the external email delivery is captured; project storage,
// the template provider, worker queue and Postgres identity store are real.
func TestIdentityHTTPPreviewPublicationAndPaymentBoundary(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DSN for Postgres HTTP integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	nonce := uint64(time.Now().UnixNano())
	adminEmail := fmt.Sprintf("boundary-admin-%d@example.test", nonce)
	svc := identity.New(pool)
	svc.SetAdminEmails([]string{adminEmail})
	mail := &capturedMail{}
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(), Identity: svc, Mailer: mail, Prov: provider.NewTemplate(),
		TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	h := boundaryHTTP{
		url: ts.URL, mail: mail,
		clientIP: netip.MustParseAddr(fmt.Sprintf("2001:db8:3:0:%x:%x:%x:%x",
			uint16(nonce>>48), uint16(nonce>>32), uint16(nonce>>16), uint16(nonce))).String(),
	}
	owner := h.register(t, "owner", fmt.Sprintf("boundary-owner-%d@example.test", nonce))
	editor := h.register(t, "editor", fmt.Sprintf("boundary-editor-%d@example.test", nonce))
	outsider := h.register(t, "nonmember", fmt.Sprintf("boundary-other-%d@example.test", nonce))
	admin := h.register(t, "admin", adminEmail)
	anon := boundaryActor{name: "anonymous", client: &http.Client{Timeout: 10 * time.Second}}
	owner.member, editor.member = true, true
	actors := []boundaryActor{anon, outsider, owner, editor, admin}
	var me struct {
		Admin bool `json:"admin"`
	}
	if err := json.Unmarshal(h.request(t, admin, http.MethodGet, "/api/me", "", 200), &me); err != nil || !me.Admin {
		t.Fatalf("administrator fixture is not an administrator: %+v, %v", me, err)
	}
	var team struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(h.request(t, owner, http.MethodPost, "/api/workspaces", `{"name":"Boundary Team"}`, 201), &team); err != nil || team.ID == "" {
		t.Fatalf("create team: %v", err)
	}
	h.request(t, owner, http.MethodPost, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, team.ID), 200)
	h.request(t, owner, http.MethodPost, "/api/workspaces/"+team.ID+"/invitations", fmt.Sprintf(`{"email":%q,"role":"editor"}`, editor.email), 201)
	h.request(t, editor, http.MethodPost, "/api/invitations/accept", fmt.Sprintf(`{"token":%q}`, mail.token(t, "invite")), 200)
	h.request(t, editor, http.MethodPost, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, team.ID), 200)
	var created struct {
		ID string `json:"sessionId"`
	}
	if err := json.Unmarshal(h.request(t, owner, http.MethodPost, "/api/sessions", `{"prompt":"一家名为「BoundaryShop」的咖啡电商网站"}`, 200), &created); err != nil || created.ID == "" {
		t.Fatalf("create project: %v", err)
	}
	id := created.ID
	h.waitReady(t, owner, id)
	appPath := "/app/" + id + "/"
	orderBody := fmt.Sprintf(`{"app":%q,"items":[{"name":"Coffee","price":28,"qty":2}]}`, id)
	orderListPath := "/api/pay/orders?app=" + id

	assertContent := func(t *testing.T, published bool) {
		t.Helper()
		for _, actor := range actors {
			t.Run(actor.name, func(t *testing.T) {
				appStatus, projectStatus, ordersStatus := http.StatusNotFound, http.StatusNotFound, http.StatusNotFound
				if published || actor.member {
					appStatus = http.StatusOK
				}
				if actor.member {
					projectStatus, ordersStatus = http.StatusOK, http.StatusOK
				} else if actor.name == "anonymous" {
					projectStatus = http.StatusUnauthorized
				}
				h.request(t, actor, http.MethodGet, appPath, "", appStatus)
				h.request(t, actor, http.MethodGet, appPath+"main.js", "", appStatus)
				for _, suffix := range []string{"", "/files", "/file/src/App.tsx", "/export", "/publish"} {
					h.request(t, actor, http.MethodGet, "/api/sessions/"+id+suffix, "", projectStatus)
				}
				h.request(t, actor, http.MethodGet, orderListPath, "", ordersStatus)
			})
		}
	}
	t.Run("unpublished content is confined to members", func(t *testing.T) {
		assertContent(t, false)
		for _, actor := range actors {
			t.Run("order creation/"+actor.name, func(t *testing.T) {
				want := http.StatusNotFound
				if actor.member {
					want = http.StatusOK
				}
				h.request(t, actor, http.MethodPost, "/api/pay/orders", orderBody, want)
			})
		}
	})
	t.Run("editor cannot publish unpublish or delete an existing project", func(t *testing.T) {
		for _, suffix := range []string{"/publish", "/unpublish"} {
			h.request(t, editor, http.MethodPost, "/api/sessions/"+id+suffix, "", 403)
		}
		h.request(t, editor, http.MethodDelete, "/api/sessions/"+id, "", 403)
	})
	t.Run("nonmembers cannot manage the existing project", func(t *testing.T) {
		for _, actor := range []boundaryActor{anon, outsider, admin} {
			t.Run(actor.name, func(t *testing.T) {
				want := http.StatusNotFound
				if actor.name == "anonymous" {
					want = http.StatusUnauthorized
				}
				for _, suffix := range []string{"/publish", "/unpublish"} {
					h.request(t, actor, http.MethodPost, "/api/sessions/"+id+suffix, "", want)
				}
				h.request(t, actor, http.MethodDelete, "/api/sessions/"+id, "", want)
			})
		}
	})
	h.request(t, owner, http.MethodPost, "/api/sessions/"+id+"/publish", "", http.StatusAccepted)
	h.waitReady(t, owner, id)

	t.Run("public app does not expose source history or order collection", func(t *testing.T) {
		assertContent(t, true)
		for _, actor := range actors {
			t.Run("order creation/"+actor.name, func(t *testing.T) {
				h.request(t, actor, http.MethodPost, "/api/pay/orders", orderBody, http.StatusOK)
			})
		}
	})
	t.Run("editing a published app keeps the anonymous snapshot unchanged", func(t *testing.T) {
		published := h.request(t, anon, http.MethodGet, appPath+"main.js", "", 200)
		h.request(t, editor, http.MethodPost, "/api/sessions/"+id+"/chat", `{"message":"把店名改为「PrivateDraft」"}`, 200)
		h.waitReady(t, editor, id)
		preview := h.request(t, editor, http.MethodGet, appPath+"main.js", "", 200)
		publicAfter := h.request(t, anon, http.MethodGet, appPath+"main.js", "", 200)
		if bytes.Equal(preview, published) || !bytes.Contains(preview, []byte("PrivateDraft")) {
			t.Fatal("the member preview did not incorporate the new draft")
		}
		if !bytes.Equal(publicAfter, published) || bytes.Contains(publicAfter, []byte("PrivateDraft")) {
			t.Fatal("an unpublished edit changed the public snapshot")
		}
	})
	var order, otherOrder struct {
		ID         string `json:"id"`
		Capability string `json:"capability"`
		Amount     int    `json:"amount"`
	}
	if err := json.Unmarshal(h.request(t, anon, http.MethodPost, "/api/pay/orders", orderBody, 200), &order); err != nil || order.ID == "" || order.Capability == "" || order.Amount != 56 {
		t.Fatalf("anonymous order missing its capability or correct amount: %+v, %v", order, err)
	}
	if err := json.Unmarshal(h.request(t, anon, http.MethodPost, "/api/pay/orders", orderBody, 200), &otherOrder); err != nil || otherOrder.ID == "" || otherOrder.Capability == order.Capability {
		t.Fatalf("second order missing a distinct capability: %+v, %v", otherOrder, err)
	}
	orderPath := "/api/pay/orders/" + order.ID + "?app=" + id
	payPath := "/api/pay/orders/" + order.ID + "/pay?app=" + id
	checkoutPath := "/pay/checkout/" + order.ID + "?app=" + id
	token := "&token=" + url.QueryEscape(order.Capability)
	t.Run("anonymous order capability is confined to one order", func(t *testing.T) {
		for _, actor := range actors {
			t.Run(actor.name, func(t *testing.T) {
				want := http.StatusNotFound
				if actor.member {
					want = http.StatusOK
				}
				h.request(t, actor, http.MethodGet, orderPath, "", want)
				h.request(t, actor, http.MethodGet, checkoutPath, "", want)
				if !actor.member {
					h.request(t, actor, http.MethodPost, payPath, `{"card":"4242424242424242"}`, 404)
					wrongToken := "&token=" + url.QueryEscape(otherOrder.Capability)
					h.request(t, actor, http.MethodGet, orderPath+wrongToken, "", 404)
					h.request(t, actor, http.MethodGet, checkoutPath+wrongToken, "", 404)
					h.request(t, actor, http.MethodPost, payPath+wrongToken, `{"card":"4242424242424242"}`, 404)
				}
				h.request(t, actor, http.MethodGet, orderPath+token, "", 200)
				h.request(t, actor, http.MethodGet, checkoutPath+token, "", 200)
			})
		}
		h.request(t, anon, http.MethodGet, orderListPath+token, "", 404)
		h.request(t, anon, http.MethodPost, payPath+token, `{"card":"4111111111111111"}`, 402)
		var paid struct {
			Status    string `json:"status"`
			CardLast4 string `json:"cardLast4"`
		}
		if err := json.Unmarshal(h.request(t, anon, http.MethodPost, payPath+token, `{"card":"4242424242424242"}`, 200), &paid); err != nil || paid.Status != "paid" || paid.CardLast4 != "4242" {
			t.Fatalf("capability did not permit the valid simulated payment: %+v, %v", paid, err)
		}
		h.request(t, anon, http.MethodPost, payPath+token, `{"card":"4242424242424242"}`, 409)
	})
	h.request(t, owner, http.MethodPost, "/api/sessions/"+id+"/unpublish", "", http.StatusNoContent)
	t.Run("unpublishing also revokes anonymous checkout and payment", func(t *testing.T) {
		assertContent(t, false)
		for _, actor := range []boundaryActor{anon, outsider, admin} {
			t.Run(actor.name, func(t *testing.T) {
				h.request(t, actor, http.MethodPost, "/api/pay/orders", orderBody, 404)
				h.request(t, actor, http.MethodGet, orderPath+token, "", 404)
				h.request(t, actor, http.MethodGet, checkoutPath+token, "", 404)
				h.request(t, actor, http.MethodPost, payPath+token, `{"card":"4242424242424242"}`, 404)
				h.request(t, actor, http.MethodPost, "/api/pay/orders/"+otherOrder.ID+"/pay?app="+id+"&token="+url.QueryEscape(otherOrder.Capability), `{"card":"4242424242424242"}`, 404)
			})
		}
		for _, actor := range []boundaryActor{owner, editor} {
			h.request(t, actor, http.MethodGet, orderPath, "", 200)
			h.request(t, actor, http.MethodGet, checkoutPath, "", 200)
			var memberOrder struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(h.request(t, actor, http.MethodPost, "/api/pay/orders", orderBody, 200), &memberOrder); err != nil || memberOrder.ID == "" {
				t.Fatalf("member order after unpublish: %+v, %v", memberOrder, err)
			}
			h.request(t, actor, http.MethodPost, "/api/pay/orders/"+memberOrder.ID+"/pay?app="+id, `{"card":"4242424242424242"}`, 200)
		}
	})
}
