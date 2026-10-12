package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

func TestIdentityHTTPUnknownEmailAndWrongPasswordHaveSameResponse(t *testing.T) {
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
	svc := identity.New(pool)
	email := fmt.Sprintf("http-login-errors-%d@example.test", time.Now().UnixNano())
	_, verification, err := svc.Register(ctx, email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyEmail(ctx, verification); err != nil {
		t.Fatal(err)
	}
	srv := server.New(server.Config{WorkspaceDir: t.TempDir(), Identity: svc, Prov: provider.NewTemplate()})
	defer srv.Close()
	seed := uint64(time.Now().UnixNano())
	peer := netip.AddrPortFrom(netip.MustParseAddr(fmt.Sprintf("2001:db8:3:0:%x:%x:%x:%x",
		uint16(seed>>48), uint16(seed>>32), uint16(seed>>16), uint16(seed))), 1234).String()
	var expectedBody string
	for _, tc := range []struct {
		name, email, password string
	}{
		{name: "known email wrong password", email: email, password: "wrong password"},
		{name: "unknown email", email: "unknown-" + email, password: "wrong password"},
		{name: "unknown email with valid password", email: "unknown-" + email, password: "correct horse battery staple"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"email":%q,"password":%q}`, tc.email, tc.password)
			req := httptest.NewRequest(http.MethodPost, "http://atoms.example/api/auth/login", strings.NewReader(body))
			req.RemoteAddr = peer
			req.Header.Set("Origin", "http://atoms.example")
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			srv.Handler().ServeHTTP(resp, req)
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("login status = %d, want 401", resp.Code)
			}
			if !strings.Contains(resp.Body.String(), "invalid credentials") {
				t.Fatalf("login body = %q, want generic credentials error", resp.Body.String())
			}
			if expectedBody == "" {
				expectedBody = resp.Body.String()
			} else if resp.Body.String() != expectedBody {
				t.Fatalf("login body = %q, want %q", resp.Body.String(), expectedBody)
			}
			if cookies := resp.Header().Values("Set-Cookie"); len(cookies) != 0 {
				t.Fatalf("failed login set cookies: %v", cookies)
			}
		})
	}
}
