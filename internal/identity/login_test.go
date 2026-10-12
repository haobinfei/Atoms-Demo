package identity_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
)

func TestLoginUnknownEmailAndWrongPasswordHaveSameResult(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	email := fmt.Sprintf("login-errors-%d@example.test", time.Now().UnixNano())
	_, verification, err := svc.Register(ctx, email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyEmail(ctx, verification); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, email, password string
	}{
		{name: "known email wrong password", email: email, password: "wrong password"},
		{name: "unknown email", email: "unknown-" + email, password: "wrong password"},
		{name: "unknown email with valid password", email: "unknown-" + email, password: "correct horse battery staple"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			login, err := svc.Login(ctx, tc.email, tc.password)
			if !errors.Is(err, identity.ErrInvalidCredentials) {
				t.Fatalf("login error = %v, want invalid credentials", err)
			}
			if login.Token != "" || login.Account.ID != "" || login.Workspace.ID != "" {
				t.Fatalf("failed login returned a session: %+v", login)
			}
		})
	}
}
