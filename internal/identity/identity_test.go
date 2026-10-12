package identity_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testService(t *testing.T) *identity.Service {
	t.Helper()
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := identity.InitSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return identity.New(pool)
}

func TestVerifiedAccountCanLoginAndRevokeSession(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	account, verification, err := svc.Register(ctx, fmt.Sprintf("test-%d@example.test", time.Now().UnixNano()), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, account.Email, "correct horse battery staple"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("unverified login: %v", err)
	}
	if _, err := svc.VerifyEmail(ctx, verification); err != nil {
		t.Fatal(err)
	}
	session, err := svc.Login(ctx, account.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := svc.Authenticate(ctx, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Account.ID != account.ID || principal.Workspace.Kind != "personal" {
		t.Fatalf("principal = %+v", principal)
	}
	if err := svc.Logout(ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, session.Token); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked session: %v", err)
	}
}

func TestLastOwnerCannotLeaveUntilAnotherOwnerIsPromoted(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	a, token, err := svc.Register(ctx, fmt.Sprintf("owner-%d@example.test", time.Now().UnixNano()), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyEmail(ctx, token); err != nil {
		t.Fatal(err)
	}
	w, err := svc.CreateTeam(ctx, a.ID, "A team")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.LeaveWorkspace(ctx, a.ID, w.ID); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("last owner left: %v", err)
	}
}

func TestInvitationIsSingleUseAndBoundToVerifiedEmail(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	owner, verifyOwner, err := svc.Register(ctx, fmt.Sprintf("invite-owner-%d@example.test", time.Now().UnixNano()), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.VerifyEmail(ctx, verifyOwner); err != nil {
		t.Fatal(err)
	}
	w, err := svc.CreateTeam(ctx, owner.ID, "Invite team")
	if err != nil {
		t.Fatal(err)
	}
	email := fmt.Sprintf("invitee-%d@example.test", time.Now().UnixNano())
	invite, err := svc.Invite(ctx, owner.ID, w.ID, email, "editor")
	if err != nil {
		t.Fatal(err)
	}
	invitee, verifyInvitee, err := svc.Register(ctx, email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AcceptInvitation(ctx, invitee.ID, invite.Token); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("unverified invitee accepted: %v", err)
	}
	if _, err = svc.VerifyEmail(ctx, verifyInvitee); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AcceptInvitation(ctx, invitee.ID, invite.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AcceptInvitation(ctx, invitee.ID, invite.Token); !errors.Is(err, identity.ErrExpired) {
		t.Fatalf("reused invitation: %v", err)
	}
	if err = svc.SetMemberRole(ctx, owner.ID, w.ID, invitee.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err = svc.LeaveWorkspace(ctx, owner.ID, w.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.LeaveWorkspace(ctx, invitee.ID, w.ID); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("last owner left: %v", err)
	}
}

func TestPasswordResetRevokesEveryDeviceAndOtherResetLinks(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	account, verification, err := svc.Register(ctx, fmt.Sprintf("reset-%d@example.test", time.Now().UnixNano()), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.VerifyEmail(ctx, verification); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login(ctx, account.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Login(ctx, account.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	reset, err := svc.RequestPasswordReset(ctx, account.Email)
	if err != nil {
		t.Fatal(err)
	}
	otherReset, err := svc.RequestPasswordReset(ctx, account.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.ResetPassword(ctx, reset, "another correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	for _, session := range []identity.LoginSession{first, second} {
		if _, err = svc.Authenticate(ctx, session.Token); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("old device remained logged in: %v", err)
		}
	}
	if err = svc.ResetPassword(ctx, otherReset, "third correct horse battery staple"); !errors.Is(err, identity.ErrExpired) {
		t.Fatalf("old reset link usable: %v", err)
	}
	if _, err = svc.Login(ctx, account.Email, "another correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
}

func TestNextLoginRestoresLastSelectedWorkspace(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	account, verification, err := svc.Register(ctx, fmt.Sprintf("switch-%d@example.test", time.Now().UnixNano()), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.VerifyEmail(ctx, verification); err != nil {
		t.Fatal(err)
	}
	team, err := svc.CreateTeam(ctx, account.ID, "Another workspace")
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login(ctx, account.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if first.Workspace.Kind != identity.WorkspacePersonal {
		t.Fatalf("first workspace = %+v", first.Workspace)
	}
	if _, err = svc.SwitchWorkspace(ctx, first.Token, team.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Logout(ctx, first.Token); err != nil {
		t.Fatal(err)
	}
	next, err := svc.Login(ctx, account.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if next.Workspace.ID != team.ID {
		t.Fatalf("next workspace = %+v", next.Workspace)
	}
}
