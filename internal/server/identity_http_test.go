package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

type capturedMail struct {
	sync.Mutex
	body string
	err  error
}

func (m *capturedMail) Send(_ context.Context, _, _, body string) error {
	m.Lock()
	defer m.Unlock()
	m.body = body
	return m.err
}
func (m *capturedMail) token(t *testing.T, key string) string {
	t.Helper()
	m.Lock()
	body := m.body
	m.Unlock()
	parts := strings.Fields(body)
	if len(parts) == 0 {
		t.Fatal("no mail captured")
	}
	u, err := url.Parse(parts[len(parts)-1])
	if err != nil {
		t.Fatal(err)
	}
	token := u.Query().Get(key)
	if token == "" {
		t.Fatalf("mail link missing %s", key)
	}
	return token
}

func TestIdentityHTTPRegisterWorkspaceAndEditorBoundary(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DSN for Postgres HTTP integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	mail := &capturedMail{}
	srv := server.New(server.Config{WorkspaceDir: t.TempDir(), Identity: identity.New(pool), Mailer: mail, Prov: provider.NewTemplate()})
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	owner := &http.Client{Jar: jar}
	post := func(client *http.Client, path, body string, want int) map[string]any {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", ts.URL)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("POST %s = %d, want %d", path, resp.StatusCode, want)
		}
		var v map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return v
	}
	if resp, err := owner.Get(ts.URL + "/api/me"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("anonymous me = %d", resp.StatusCode)
		}
	}
	email := fmt.Sprintf("owner-%d@example.test", time.Now().UnixNano())
	post(owner, "/api/auth/register", fmt.Sprintf(`{"email":%q,"password":"very-long-password"}`, email), 202)
	post(owner, "/api/auth/verify", fmt.Sprintf(`{"token":%q}`, mail.token(t, "verify")), 204)
	post(owner, "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":"very-long-password"}`, email), 200)
	team := post(owner, "/api/workspaces", `{"name":"Test Team"}`, 201)
	teamID, _ := team["id"].(string)
	if teamID == "" {
		t.Fatal("team id missing")
	}
	post(owner, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, teamID), 200)

	otherJar, _ := cookiejar.New(nil)
	editor := &http.Client{Jar: otherJar}
	otherEmail := fmt.Sprintf("editor-%d@example.test", time.Now().UnixNano())
	post(editor, "/api/auth/register", fmt.Sprintf(`{"email":%q,"password":"very-long-password"}`, otherEmail), 202)
	post(editor, "/api/auth/verify", fmt.Sprintf(`{"token":%q}`, mail.token(t, "verify")), 204)
	post(editor, "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":"very-long-password"}`, otherEmail), 200)
	post(owner, "/api/workspaces/"+teamID+"/invitations", fmt.Sprintf(`{"email":%q,"role":"editor"}`, otherEmail), 201)
	post(editor, "/api/invitations/accept", fmt.Sprintf(`{"token":%q}`, mail.token(t, "invite")), 200)
	post(editor, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, teamID), 200)
	post(editor, "/api/sessions/missing/publish", `{}`, 403)
	post(editor, "/api/sessions/missing/unpublish", `{}`, 403)
	post(editor, "/api/auth/logout", `{}`, 204)
	if resp, err := editor.Get(ts.URL + "/api/me"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("logged-out me = %d", resp.StatusCode)
		}
	}
	mail.Lock()
	mail.err = errors.New("SMTP unavailable")
	mail.Unlock()
	unverifiedEmail := fmt.Sprintf("unverified-%d@example.test", time.Now().UnixNano())
	unknownEmail := fmt.Sprintf("unknown-%d@example.test", time.Now().UnixNano())
	post(owner, "/api/auth/register", fmt.Sprintf(`{"email":%q,"password":"very-long-password"}`, unverifiedEmail), 202)
	post(owner, "/api/auth/register", fmt.Sprintf(`{"email":%q,"password":"very-long-password"}`, unverifiedEmail), 202)
	post(owner, "/api/auth/resend-verification", fmt.Sprintf(`{"email":%q}`, unverifiedEmail), 202)
	post(owner, "/api/auth/resend-verification", fmt.Sprintf(`{"email":%q}`, unknownEmail), 202)
	post(owner, "/api/auth/forgot-password", fmt.Sprintf(`{"email":%q}`, email), 202)
	post(owner, "/api/auth/forgot-password", fmt.Sprintf(`{"email":%q}`, unknownEmail), 202)
}

func TestIdentityModeRejectsAnonymousProjectAccess(t *testing.T) {
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(),
		Identity:     identity.New(nil),
		Prov:         provider.NewTemplate(),
	})
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/list")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous project list status = %d, want 401", resp.StatusCode)
	}
}

func TestIdentityModeRequiresSameOriginForStateChanges(t *testing.T) {
	srv := server.New(server.Config{WorkspaceDir: t.TempDir(), Identity: identity.New(nil), Prov: provider.NewTemplate()})
	defer srv.Close()
	for _, tc := range []struct {
		name, origin, fetchSite string
		want                    int
	}{
		{name: "missing provenance", want: http.StatusForbidden},
		{name: "foreign origin", origin: "https://other.example", want: http.StatusForbidden},
		{name: "foreign fetch site", fetchSite: "cross-site", want: http.StatusForbidden},
		{name: "same origin", origin: "http://atoms.example", want: http.StatusBadRequest},
		{name: "same origin fetch site", fetchSite: "same-origin", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://atoms.example/api/auth/verify", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			resp := httptest.NewRecorder()
			srv.Handler().ServeHTTP(resp, req)
			if resp.Code != tc.want {
				t.Fatalf("status = %d, want %d", resp.Code, tc.want)
			}
		})
	}
}

func TestIdentityHTTPSharedRateLimitAndTrustedProxy(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DSN for Postgres HTTP integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	trusted := netip.MustParsePrefix("10.0.0.0/8")
	newHandler := func() (http.Handler, func()) {
		srv := server.New(server.Config{
			WorkspaceDir: t.TempDir(), Identity: identity.New(pool),
			TrustedProxyCIDRs: []netip.Prefix{trusted}, Prov: provider.NewTemplate(),
		})
		return srv.Handler(), func() { _ = srv.Close() }
	}
	first, closeFirst := newHandler()
	defer closeFirst()
	second, closeSecond := newHandler()
	defer closeSecond()
	seed := uint64(time.Now().UnixNano())
	address := func(subnet uint16) netip.Addr {
		// Split the seed into valid IPv6 hextets, then validate before making requests.
		return netip.MustParseAddr(fmt.Sprintf("2001:db8:%x:0:%x:%x:%x:%x",
			subnet, seed>>48, (seed>>32)&0xffff, (seed>>16)&0xffff, seed&0xffff))
	}
	request := func(handler http.Handler, peer, claimed string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://atoms.example/api/auth/verify", strings.NewReader(`{}`))
		req.RemoteAddr = peer
		req.Header.Set("Origin", "http://atoms.example")
		req.Header.Set("X-Real-IP", claimed)
		req.Header.Set("X-Forwarded-For", claimed)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp.Code
	}
	// An untrusted direct caller cannot rotate a header to evade the shared counter.
	peer := netip.AddrPortFrom(address(0), 1234).String()
	for i := 0; i < 20; i++ {
		if status := request(first, peer, fmt.Sprintf("192.0.2.%d", i+1)); status != 410 {
			t.Fatalf("untrusted request %d = %d, want 410", i, status)
		}
	}
	if status := request(second, peer, "192.0.2.200"); status != 429 {
		t.Fatalf("cross-instance untrusted request = %d, want 429", status)
	}
	// A trusted proxy may supply the client IP, but forged X-Forwarded-For is ignored.
	proxy := "10.0.0.5:1234"
	client := address(1).String()
	for i := 0; i < 20; i++ {
		if status := request(first, proxy, client); status != 410 {
			t.Fatalf("trusted request %d = %d, want 410", i, status)
		}
	}
	if status := request(second, proxy, client); status != 429 {
		t.Fatalf("cross-instance trusted request = %d, want 429", status)
	}
	if status := request(second, proxy, address(2).String()); status != 410 {
		t.Fatalf("other client behind trusted proxy = %d, want 410", status)
	}
}

func TestIdentityHTTPRateLimitStorageFailsClosed(t *testing.T) {
	srv := server.New(server.Config{WorkspaceDir: t.TempDir(), Identity: identity.New(nil), Prov: provider.NewTemplate()})
	defer srv.Close()
	req := httptest.NewRequest(http.MethodPost, "http://atoms.example/api/auth/verify", strings.NewReader(`{}`))
	req.Header.Set("Origin", "http://atoms.example")
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing rate limit storage = %d, want 503", resp.Code)
	}
}

func TestIdentityHTTPTeamLeaveAndDeleteKeepLogin(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DSN for Postgres HTTP integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	mail := &capturedMail{}
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(), Identity: identity.New(pool), Mailer: mail, Prov: provider.NewTemplate(),
		TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	nonce := uint64(time.Now().UnixNano())
	clientIP := netip.MustParseAddr(fmt.Sprintf("2001:db8:2:0:%x:%x:%x:%x",
		uint16(nonce>>48), uint16(nonce>>32), uint16(nonce>>16), uint16(nonce))).String()
	request := func(client *http.Client, method, path, body, confirmation string, want int) map[string]any {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", ts.URL)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Real-IP", clientIP)
		if confirmation != "" {
			req.Header.Set("X-Confirm-Delete", confirmation)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s = %d, want %d", method, path, resp.StatusCode, want)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	newAccount := func(prefix string) (*http.Client, string, string) {
		t.Helper()
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar}
		email := fmt.Sprintf("%s-%d@example.test", prefix, time.Now().UnixNano())
		credentials := fmt.Sprintf(`{"email":%q,"password":"very-long-password"}`, email)
		request(client, http.MethodPost, "/api/auth/register", credentials, "", 202)
		request(client, http.MethodPost, "/api/auth/verify", fmt.Sprintf(`{"token":%q}`, mail.token(t, "verify")), "", 204)
		request(client, http.MethodPost, "/api/auth/login", credentials, "", 200)
		me := request(client, http.MethodGet, "/api/me", "", "", 200)
		return client, email, me["currentWorkspace"].(string)
	}
	owner, _, ownerPersonal := newAccount("lifecycle-owner")
	editor, editorEmail, editorPersonal := newAccount("lifecycle-editor")
	team := request(owner, http.MethodPost, "/api/workspaces", `{"name":"Lifecycle Team"}`, "", 201)
	teamID := team["id"].(string)
	unused := request(owner, http.MethodPost, "/api/workspaces", `{"name":"Unused Team"}`, "", 201)
	unusedID := unused["id"].(string)

	// A workspace need not be the current selection to be deleted by its owner.
	request(owner, http.MethodDelete, "/api/workspaces/"+unusedID, "", "Unused Team", 204)
	me := request(owner, http.MethodGet, "/api/me", "", "", 200)
	if me["currentWorkspace"] != ownerPersonal {
		t.Fatalf("non-current deletion changed selection to %v", me["currentWorkspace"])
	}
	request(owner, http.MethodPost, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, unusedID), "", 403)
	request(owner, http.MethodPost, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, teamID), "", 200)
	request(owner, http.MethodDelete, "/api/workspaces/"+teamID, "", "wrong confirmation", 400)
	request(owner, http.MethodPost, "/api/workspaces/"+teamID+"/leave", "", "", 409)
	request(owner, http.MethodPost, "/api/workspaces/"+ownerPersonal+"/leave", "", "", 403)

	request(owner, http.MethodPost, "/api/workspaces/"+teamID+"/invitations", fmt.Sprintf(`{"email":%q}`, editorEmail), "", 201)
	request(editor, http.MethodPost, "/api/invitations/accept", fmt.Sprintf(`{"token":%q}`, mail.token(t, "invite")), "", 200)
	request(editor, http.MethodPost, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, teamID), "", 200)
	request(editor, http.MethodDelete, "/api/workspaces/"+teamID, "", "Lifecycle Team", 403)

	// An editor may leave without losing their login, and immediately loses team access.
	request(editor, http.MethodPost, "/api/workspaces/"+teamID+"/leave", "", "", 204)
	me = request(editor, http.MethodGet, "/api/me", "", "", 200)
	if me["currentWorkspace"] != editorPersonal {
		t.Fatalf("editor leave selected %v, want personal workspace", me["currentWorkspace"])
	}
	request(editor, http.MethodPost, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, teamID), "", 403)
	request(editor, http.MethodDelete, "/api/workspaces/"+teamID, "", "Lifecycle Team", 404)

	// After ownership handoff, the original owner can leave and the new owner can delete.
	request(owner, http.MethodPost, "/api/workspaces/"+teamID+"/invitations", fmt.Sprintf(`{"email":%q,"role":"owner"}`, editorEmail), "", 201)
	request(editor, http.MethodPost, "/api/invitations/accept", fmt.Sprintf(`{"token":%q}`, mail.token(t, "invite")), "", 200)
	request(editor, http.MethodPost, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, teamID), "", 200)
	request(owner, http.MethodPost, "/api/workspaces/"+teamID+"/leave", "", "", 204)
	me = request(owner, http.MethodGet, "/api/me", "", "", 200)
	if me["currentWorkspace"] != ownerPersonal {
		t.Fatalf("owner leave selected %v, want personal workspace", me["currentWorkspace"])
	}
	request(editor, http.MethodDelete, "/api/workspaces/"+teamID, "", "Lifecycle Team", 204)
	me = request(editor, http.MethodGet, "/api/me", "", "", 200)
	if me["currentWorkspace"] != editorPersonal {
		t.Fatalf("current team deletion selected %v, want personal workspace", me["currentWorkspace"])
	}
	request(editor, http.MethodPost, "/api/workspaces/current", fmt.Sprintf(`{"id":%q}`, teamID), "", 403)
}
