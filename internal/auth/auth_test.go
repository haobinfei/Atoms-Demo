package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/auth"
)

func newTestMiddleware(t *testing.T, insecure bool) *auth.Middleware {
	t.Helper()
	mw, err := auth.New(context.Background(), auth.Config{
		Issuer:          "https://auth.ss-vip.cn",
		ClientID:        "atoms-test",
		ClientSecret:    "secret",
		RedirectURL:     "https://atoms.ss-vip.cn/api/auth/callback",
		InsecureCookies: insecure,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	return mw
}

func TestLoginRedirectsExplicitHTTPToHTTPS(t *testing.T) {
	mw := newTestMiddleware(t, false)
	req := httptest.NewRequest(http.MethodGet, "http://atoms.ss-vip.cn/api/auth/login?from=home", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	rec := httptest.NewRecorder()

	mw.Login(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if loc.Scheme != "https" || loc.Host != "atoms.ss-vip.cn" ||
		loc.Path != "/api/auth/login" || loc.RawQuery != "from=home" {
		t.Fatalf("redirect = %q, want canonical HTTPS login URL", loc)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("HTTP redirect must not set state cookies, got %v", cookies)
	}
}

func TestLoginWithoutProxyProtoUsesNonSecureStateCookie(t *testing.T) {
	mw := newTestMiddleware(t, false)
	req := httptest.NewRequest(http.MethodGet, "http://atoms.ss-vip.cn/api/auth/login", nil)
	rec := httptest.NewRecorder()

	mw.Login(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if !strings.HasPrefix(loc.String(), "https://auth.ss-vip.cn/oauth/authorize?") {
		t.Fatalf("authorize redirect = %q", loc)
	}
	state := stateCookie(t, rec)
	if state.Secure {
		t.Fatal("state cookie must omit Secure when the login page is HTTP")
	}
	if state.Value != loc.Query().Get("state") {
		t.Fatalf("state cookie = %q, authorize state = %q", state.Value, loc.Query().Get("state"))
	}
}

func TestLoginOnForwardedHTTPSUsesSecureStateCookie(t *testing.T) {
	mw := newTestMiddleware(t, false)
	req := httptest.NewRequest(http.MethodGet, "https://atoms.ss-vip.cn/api/auth/login", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	mw.Login(rec, req)

	state := stateCookie(t, rec)
	if !state.Secure {
		t.Fatal("state cookie must be Secure on HTTPS")
	}
}

func stateCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.CookieState {
			return cookie
		}
	}
	t.Fatal("state cookie not set")
	return nil
}
