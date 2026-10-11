package server_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/auth"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

// authEnv bundles an OIDC issuer stub (JWKS + token endpoint) with the RSA
// key used to sign test tokens, plus an app server wired against it.
type authEnv struct {
	ts     *httptest.Server
	issuer *httptest.Server
	priv   *rsa.PrivateKey
}

// testClientSecret deliberately uses special characters: RFC 6749 Basic
// credentials must travel raw, so this doubles as a regression test for
// double percent-encoding in the token exchange.
const testClientSecret = "sk-test+secret/with=chars"

// newAuthTestServer stands up the full stack behind AuthSphere auth.
func newAuthTestServer(t *testing.T) *authEnv {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			n := base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes())
			writeJSON(w, map[string]any{"keys": []map[string]any{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "test-key",
				"n": n, "e": "AQAB",
			}}})
		case "/oauth/token":
			if err := r.ParseForm(); err != nil || r.PostForm.Get("code") != "good-code" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			id, secret, ok := r.BasicAuth()
			if !ok || id != "atoms-test" || secret != testClientSecret {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			idToken := signJWT(t, priv, issuerURL(r), "user-a", "atoms-test", time.Hour)
			writeJSON(w, map[string]any{"id_token": idToken, "token_type": "Bearer"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(issuer.Close)

	mw, err := auth.New(context.Background(), auth.Config{
		Issuer:          issuer.URL,
		ClientID:        "atoms-test",
		ClientSecret:    testClientSecret,
		RedirectURL:     "http://localhost:4173/api/auth/callback",
		InsecureCookies: true,
	})
	if err != nil {
		t.Fatalf("auth middleware: %v", err)
	}
	srv := server.New(server.Config{WorkspaceDir: t.TempDir(), Auth: mw, Prov: provider.NewTemplate()})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return &authEnv{ts: ts, issuer: issuer, priv: priv}
}

// issuerURL returns the stub's external URL regardless of which request
// reached the handler (httptest URLs are stable).
func issuerURL(r *http.Request) string {
	return "http://" + r.Host
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// signJWT builds a minimal RS256 JWT: header.payload.signature.
func signJWT(t *testing.T, priv *rsa.PrivateKey, iss, sub, aud string, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test-key"})
	payload, _ := json.Marshal(map[string]any{
		"iss": iss, "sub": sub, "aud": aud,
		"exp": now.Add(ttl).Unix(), "iat": now.Unix(),
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (e *authEnv) token(t *testing.T, sub string) string {
	t.Helper()
	return signJWT(t, e.priv, e.issuer.URL, sub, "atoms-test", time.Hour)
}

// getWithToken performs a GET carrying the token as a Bearer credential.
func getWithToken(t *testing.T, rawURL, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

func TestAPIRequiresAuth(t *testing.T) {
	e := newAuthTestServer(t)

	// The health check stays open for load balancers and container probes.
	health, err := http.Get(e.ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", health.StatusCode)
	}

	// Any /api/* call without credentials is a 401…
	resp, err := http.Get(e.ts.URL + "/api/sessions/whatever")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	// …but /app/{id}/ is public and only 404s, never 401s.
	appResp, err := http.Get(e.ts.URL + "/app/whatever/")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	appResp.Body.Close()
	if appResp.StatusCode != http.StatusNotFound {
		t.Fatalf("public app status = %d, want 404 (not 401)", appResp.StatusCode)
	}

	// Expired tokens are rejected too.
	expired := signJWT(t, e.priv, e.issuer.URL, "user-a", "atoms-test", -time.Hour)
	expResp := getWithToken(t, e.ts.URL+"/api/sessions/whatever", expired)
	expResp.Body.Close()
	if expResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d, want 401", expResp.StatusCode)
	}

	// Wrong audience and wrong issuer are rejected as well.
	wrongAud := signJWT(t, e.priv, e.issuer.URL, "user-a", "someone-else", time.Hour)
	audResp := getWithToken(t, e.ts.URL+"/api/sessions/whatever", wrongAud)
	audResp.Body.Close()
	if audResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-aud token status = %d, want 401", audResp.StatusCode)
	}
	wrongIss := signJWT(t, e.priv, "http://evil.example", "user-a", "atoms-test", time.Hour)
	issResp := getWithToken(t, e.ts.URL+"/api/sessions/whatever", wrongIss)
	issResp.Body.Close()
	if issResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-iss token status = %d, want 401", issResp.StatusCode)
	}

	// A malformed Authorization header is a hard 401, not a cookie fallback.
	malReq, err := http.NewRequest(http.MethodGet, e.ts.URL+"/api/sessions/whatever", nil)
	if err != nil {
		t.Fatalf("build malformed request: %v", err)
	}
	malReq.Header.Set("Authorization", "Basic abc")
	malResp, err := http.DefaultClient.Do(malReq)
	if err != nil {
		t.Fatalf("malformed authz: %v", err)
	}
	malResp.Body.Close()
	if malResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("malformed authz status = %d, want 401", malResp.StatusCode)
	}

	// Login redirects to the issuer's authorize page.
	noFollow := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	loginResp, err := noFollow.Get(e.ts.URL + "/api/auth/login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	loginResp.Body.Close()
	loc, err := url.Parse(loginResp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if loginResp.StatusCode != http.StatusFound ||
		!strings.HasPrefix(loc.String(), e.issuer.URL+"/oauth/authorize") {
		t.Fatalf("login redirect = %d %q, want 302 to issuer authorize",
			loginResp.StatusCode, loc.String())
	}
	if loc.Query().Get("client_id") != "atoms-test" {
		t.Fatalf("authorize client_id = %q", loc.Query().Get("client_id"))
	}
}

func TestUserIsolation(t *testing.T) {
	e := newAuthTestServer(t)

	// user-a creates a session via the API with a Bearer token.
	createBody := strings.NewReader(`{"prompt":"一个数据看板"}`)
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/api/sessions", createBody)
	if err != nil {
		t.Fatalf("build create: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token(t, "user-a"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()

	// The dashboard list is owner-scoped: user-b sees nothing.
	foreignList := getWithToken(t, e.ts.URL+"/api/sessions/list", e.token(t, "user-b"))
	var listOut struct {
		Sessions []any `json:"sessions"`
	}
	if err := json.NewDecoder(foreignList.Body).Decode(&listOut); err != nil {
		t.Fatalf("decode foreign list: %v", err)
	}
	foreignList.Body.Close()
	if len(listOut.Sessions) != 0 {
		t.Fatalf("foreign list len = %d, want 0", len(listOut.Sessions))
	}

	// Dashboard write operations are owner-scoped as well: user-b mutating
	// user-a's session gets a plain 404.
	bToken := e.token(t, "user-b")
	postTo := func(path, body string) int {
		req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/api/sessions/"+out.SessionID+path,
			strings.NewReader(body))
		if err != nil {
			t.Fatalf("build %s: %v", path, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := postTo("/rename", `{"name":"劫持"}`); code != http.StatusNotFound {
		t.Fatalf("foreign rename status = %d, want 404", code)
	}
	if code := postTo("/pin", `{"pinned":true}`); code != http.StatusNotFound {
		t.Fatalf("foreign pin status = %d, want 404", code)
	}
	if code := postTo("/duplicate", ""); code != http.StatusNotFound {
		t.Fatalf("foreign duplicate status = %d, want 404", code)
	}
	delReq, err := http.NewRequest(http.MethodDelete, e.ts.URL+"/api/sessions/"+out.SessionID, nil)
	if err != nil {
		t.Fatalf("build foreign delete: %v", err)
	}
	delReq.Header.Set("Authorization", "Bearer "+bToken)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("foreign delete: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign delete status = %d, want 404", delResp.StatusCode)
	}

	// user-a can read it; user-b gets a plain 404 (no existence leak).
	own := getWithToken(t, e.ts.URL+"/api/sessions/"+out.SessionID, e.token(t, "user-a"))
	own.Body.Close()
	if own.StatusCode != http.StatusOK {
		t.Fatalf("owner read status = %d, want 200", own.StatusCode)
	}
	other := getWithToken(t, e.ts.URL+"/api/sessions/"+out.SessionID, e.token(t, "user-b"))
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign read status = %d, want 404", other.StatusCode)
	}

	// The event stream and chat are owner-scoped as well.
	events := getWithToken(t, e.ts.URL+"/api/sessions/"+out.SessionID+"/events", e.token(t, "user-b"))
	events.Body.Close()
	if events.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign events status = %d, want 404", events.StatusCode)
	}
	for _, path := range []string{"files", "file/src/App.tsx", "export", "publish"} {
		resp := getWithToken(t, e.ts.URL+"/api/sessions/"+out.SessionID+"/"+path, e.token(t, "user-b"))
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign GET %s status = %d, want 404", path, resp.StatusCode)
		}
	}
	chatReq, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/sessions/"+out.SessionID+"/chat",
		strings.NewReader(`{"message":"把主题改成蓝色"}`))
	chatReq.Header.Set("Content-Type", "application/json")
	chatReq.Header.Set("Authorization", "Bearer "+e.token(t, "user-b"))
	chatResp, err := http.DefaultClient.Do(chatReq)
	if err != nil {
		t.Fatalf("foreign chat: %v", err)
	}
	chatResp.Body.Close()
	if chatResp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign chat status = %d, want 404", chatResp.StatusCode)
	}

	// The published public URL works for nobody-in-particular: sharing the
	// link is the capability.
	<-waitReady(t, e.ts.URL, out.SessionID, e.token(t, "user-a"))
	pubReq, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/sessions/"+out.SessionID+"/publish", nil)
	pubReq.Header.Set("Authorization", "Bearer "+e.token(t, "user-a"))
	pubResp, err := http.DefaultClient.Do(pubReq)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	pubResp.Body.Close()
	// Publish is asynchronous: the request is accepted (202) and the rebuild
	// lands on a Worker. Poll the public URL until it serves.
	if pubResp.StatusCode != http.StatusAccepted {
		t.Fatalf("publish status = %d, want 202", pubResp.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	var anon *http.Response
	for {
		anon, err = http.Get(e.ts.URL + "/app/" + out.SessionID + "/")
		if err != nil {
			t.Fatalf("anonymous published app: %v", err)
		}
		anon.Body.Close()
		if anon.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("anonymous published app status = %d, want 200", anon.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCallbackSetsSessionCookie(t *testing.T) {
	e := newAuthTestServer(t)
	noFollow := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// Missing state cookie: rejected.
	badState, err := noFollow.Get(e.ts.URL + "/api/auth/callback?code=good-code&state=forged")
	if err != nil {
		t.Fatalf("callback without state cookie: %v", err)
	}
	badState.Body.Close()
	if badState.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback without state cookie status = %d, want 400", badState.StatusCode)
	}

	// State cookie present but not matching the callback's state: rejected.
	mismatchReq, err := http.NewRequest(http.MethodGet,
		e.ts.URL+"/api/auth/callback?code=good-code&state=forged", nil)
	if err != nil {
		t.Fatalf("build mismatch callback: %v", err)
	}
	mismatchReq.AddCookie(&http.Cookie{Name: auth.CookieState, Value: "legit-state"})
	mismatchResp, err := noFollow.Do(mismatchReq)
	if err != nil {
		t.Fatalf("mismatch callback: %v", err)
	}
	mismatchResp.Body.Close()
	if mismatchResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("state mismatch status = %d, want 400", mismatchResp.StatusCode)
	}

	// Full happy path: login to obtain the state cookie, then callback.
	loginResp, err := noFollow.Get(e.ts.URL + "/api/auth/login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	var stateCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == auth.CookieState {
			stateCookie = c
		}
	}
	loginResp.Body.Close()
	if stateCookie == nil {
		t.Fatal("login did not set a state cookie")
	}
	state := stateCookie.Value

	cbReq, err := http.NewRequest(http.MethodGet,
		e.ts.URL+"/api/auth/callback?code=good-code&state="+state, nil)
	if err != nil {
		t.Fatalf("build callback: %v", err)
	}
	cbReq.AddCookie(stateCookie)
	cbResp, err := noFollow.Do(cbReq)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", cbResp.StatusCode)
	}
	if loc := cbResp.Header.Get("Location"); loc != "/" {
		t.Fatalf("callback redirect = %q, want /", loc)
	}
	var tokenCookie *http.Cookie
	for _, c := range cbResp.Cookies() {
		if c.Name == auth.CookieToken {
			tokenCookie = c
		}
	}
	if tokenCookie == nil || tokenCookie.Value == "" {
		t.Fatal("callback did not set the session cookie")
	}

	// The cookie alone authenticates subsequent API calls.
	meReq, err := http.NewRequest(http.MethodGet, e.ts.URL+"/api/me", nil)
	if err != nil {
		t.Fatalf("build me: %v", err)
	}
	meReq.AddCookie(tokenCookie)
	meResp, err := http.DefaultClient.Do(meReq)
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("me status = %d, want 200", meResp.StatusCode)
	}
	var me struct {
		User string `json:"user"`
	}
	if err := json.NewDecoder(meResp.Body).Decode(&me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.User != "user-a" {
		t.Fatalf("me user = %q, want user-a", me.User)
	}
}

// waitReady polls the session until the pipeline reports ready.
func waitReady(t *testing.T, tsURL, id, token string) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			resp := getWithToken(t, tsURL+"/api/sessions/"+id, token)
			var sess struct {
				Status string `json:"status"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&sess)
			resp.Body.Close()
			if sess.Status == "ready" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("session %s never became ready", id)
	}()
	return done
}
