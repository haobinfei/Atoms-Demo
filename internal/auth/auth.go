// Package auth wires the Atoms Demo into AuthSphere as an OIDC client:
// the middleware verifies AuthSphere JWTs against the issuer's JWKS
// endpoint, injects the user id into the request context, and the login /
// callback handlers run the authorization-code flow. Session data is
// isolated per user by the session store; /app/{id}/ stays public via the
// store's id index.
//
// Endpoints follow AuthSphere's real routes ({issuer}/oauth/authorize,
// {issuer}/oauth/token, {issuer}/.well-known/jwks.json) — no discovery
// document required, matching the AuthSphere Go SDK.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/haobinfei/Atoms-Demo/internal/session"
)

const (
	// CookieToken carries the raw id_token after login; CookieState is the
	// OIDC state nonce for the callback check.
	CookieToken = "atoms_token"
	CookieState = "atoms_state"
)

type contextKey string

const userKey contextKey = "auth-user"

// UserFrom returns the authenticated user id on the context ("" when the
// request did not pass through the middleware, e.g. /app or dev bypass).
func UserFrom(ctx context.Context) string {
	v, _ := ctx.Value(userKey).(string)
	return v
}

type Config struct {
	// Issuer is the AuthSphere base URL, e.g. https://auth.ss-vip.cn
	// (no trailing slash).
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string // must match the application's registered redirect_uris
	// SkipIssuerCheck tolerates deployments where the iss claim differs
	// from the reachable issuer URL (e.g. behind nginx); signature
	// verification is unaffected.
	SkipIssuerCheck bool
	// InsecureCookies omits the Secure flag so local http development can
	// carry the session cookie.
	InsecureCookies bool
}

// Middleware verifies JWTs and guards /api/*.
type Middleware struct {
	cfg      Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
}

func New(ctx context.Context, cfg Config) (*Middleware, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, fmt.Errorf("auth: issuer, client id and redirect url are required")
	}
	keySet := oidc.NewRemoteKeySet(ctx, cfg.Issuer+"/.well-known/jwks.json")
	verifier := oidc.NewVerifier(cfg.Issuer, keySet, &oidc.Config{
		ClientID:          cfg.ClientID,
		SkipClientIDCheck: cfg.ClientID == "",
		SkipIssuerCheck:   cfg.SkipIssuerCheck,
	})
	return &Middleware{cfg: cfg, verifier: verifier, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

// Wrap guards /api/* (except /api/auth/*): a valid Bearer token or
// CookieToken is required; the user id lands on the request context.
// Non-/api routes (the SPA, /app/{id}/) pass through untouched.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		user, err := m.verifyRequest(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	})
}

// verifyRequest extracts the token from the Authorization header or the
// cookie and returns the validated subject.
func (m *Middleware) verifyRequest(r *http.Request) (string, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if raw == "" {
		if c, err := r.Cookie(CookieToken); err == nil {
			raw = c.Value
		}
	}
	if raw == "" {
		return "", fmt.Errorf("no token")
	}
	token, err := m.verifier.Verify(r.Context(), raw)
	if err != nil {
		return "", fmt.Errorf("verify token: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := token.Claims(&claims); err != nil {
		return "", fmt.Errorf("read claims: %w", err)
	}
	if !session.ValidOwner(claims.Sub) {
		return "", fmt.Errorf("unusable subject")
	}
	return claims.Sub, nil
}

// Login starts the authorization-code flow: 302 to AuthSphere's authorize
// page with a state nonce kept in a cookie for the callback.
func (m *Middleware) Login(w http.ResponseWriter, r *http.Request) {
	state, err := randomHex(16)
	if err != nil {
		http.Error(w, "generate state", http.StatusInternalServerError)
		return
	}
	m.setCookie(w, CookieState, state, 10*time.Minute)
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {m.cfg.ClientID},
		"redirect_uri":  {m.cfg.RedirectURL},
		"scope":         {"openid profile email"},
		"state":         {state},
	}
	http.Redirect(w, r, m.cfg.Issuer+"/oauth/authorize?"+q.Encode(), http.StatusFound)
}

// Callback exchanges the authorization code for an id_token, verifies it,
// plants the session cookie and sends the browser back to the SPA.
func (m *Middleware) Callback(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("error") != "" {
		http.Error(w, "authorization failed: "+r.URL.Query().Get("error"), http.StatusBadRequest)
		return
	}
	stateCookie, err := r.Cookie(CookieState)
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "code is required", http.StatusBadRequest)
		return
	}
	idToken, err := m.exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "token exchange: "+err.Error(), http.StatusBadGateway)
		return
	}
	if _, err := m.verifier.Verify(r.Context(), idToken); err != nil {
		http.Error(w, "verify id token: "+err.Error(), http.StatusBadGateway)
		return
	}
	m.setCookie(w, CookieToken, idToken, 24*time.Hour)
	http.Redirect(w, r, "/", http.StatusFound)
}

// exchange swaps the code for tokens at AuthSphere's token endpoint.
func (m *Middleware) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {m.cfg.RedirectURL},
		"client_id":    {m.cfg.ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.Issuer+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(m.cfg.ClientID), url.QueryEscape(m.cfg.ClientSecret))
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.IDToken == "" {
		return "", fmt.Errorf("token endpoint returned no id_token")
	}
	return body.IDToken, nil
}

func (m *Middleware) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   !m.cfg.InsecureCookies,
	})
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
