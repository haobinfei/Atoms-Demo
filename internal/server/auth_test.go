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
		ClientSecret:    "sk-test",
		RedirectURL:     "http://localhost:4173/api/auth/callback",
		InsecureCookies: true,
	})
	if err != nil {
		t.Fatalf("auth middleware: %v", err)
	}
	srv := server.New(server.Config{WorkspaceDir: t.TempDir(), Auth: mw, Prov: provider.NewTemplate()})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
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
	if pubResp.StatusCode != http.StatusOK {
		t.Fatalf("publish status = %d, want 200", pubResp.StatusCode)
	}
	anon, err := http.Get(e.ts.URL + "/app/" + out.SessionID + "/")
	if err != nil {
		t.Fatalf("anonymous published app: %v", err)
	}
	anon.Body.Close()
	if anon.StatusCode != http.StatusOK {
		t.Fatalf("anonymous published app status = %d, want 200", anon.StatusCode)
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
