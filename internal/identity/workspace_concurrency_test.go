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

func workspaceTestPool(t *testing.T, name string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_TEST_DSN is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = name
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// waitForWorkspaceLock only coordinates concurrent public operations; the
// assertions below observe membership and login behavior through Service.
func waitForWorkspaceLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')", name).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("operation %s did not reach the lock barrier: %v", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestConcurrentRemovalAndWorkspaceSwitchKeepDevicesSignedIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nonce := time.Now().UnixNano()
	pool := workspaceTestPool(t, fmt.Sprintf("workspace-setup-%d", nonce))
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	svc := identity.New(pool)
	register := func(prefix string) identity.Account {
		t.Helper()
		a, verification, err := svc.Register(ctx, fmt.Sprintf("%s-%d@example.test", prefix, nonce), "correct horse battery staple")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.VerifyEmail(ctx, verification); err != nil {
			t.Fatal(err)
		}
		return a
	}
	owner := register("switch-owner")
	editor := register("switch-editor")
	team, err := svc.CreateTeam(ctx, owner.ID, "Concurrent team")
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := svc.Invite(ctx, owner.ID, team.ID, editor.Email, identity.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AcceptInvitation(ctx, editor.ID, invitation.Token); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login(ctx, editor.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Login(ctx, editor.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	personalID := first.Workspace.ID
	if _, err = svc.SwitchWorkspace(ctx, first.Token, team.ID); err != nil {
		t.Fatal(err)
	}

	// Queue removal before the second device's switch. An account lock makes
	// the overlap reproducible without pausing inside the identity service.
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	if _, err = barrier.Exec(ctx, "SELECT id FROM identity_accounts WHERE id=$1 FOR UPDATE", editor.ID); err != nil {
		t.Fatal(err)
	}
	removeName := fmt.Sprintf("workspace-remove-%d", nonce)
	removeService := identity.New(workspaceTestPool(t, removeName))
	removed := make(chan error, 1)
	go func() { removed <- removeService.RemoveMember(ctx, owner.ID, team.ID, editor.ID) }()
	waitForWorkspaceLock(t, ctx, pool, removeName)
	switchName := fmt.Sprintf("workspace-switch-%d", nonce)
	switchService := identity.New(workspaceTestPool(t, switchName))
	switched := make(chan error, 1)
	go func() {
		_, switchErr := switchService.SwitchWorkspace(ctx, second.Token, team.ID)
		switched <- switchErr
	}()
	waitForWorkspaceLock(t, ctx, pool, switchName)
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-removed; err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if err = <-switched; !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("switch after removal = %v, want forbidden", err)
	}
	for _, device := range []identity.LoginSession{first, second} {
		principal, authErr := svc.Authenticate(ctx, device.Token)
		if authErr != nil || principal.Workspace.ID != personalID {
			t.Fatalf("device after removal = %+v, %v; want personal workspace", principal, authErr)
		}
	}
	next, err := svc.Login(ctx, editor.Email, "correct horse battery staple")
	if err != nil || next.Workspace.ID != personalID {
		t.Fatalf("next login = %+v, %v; want personal workspace", next.Workspace, err)
	}
}

func TestConcurrentRemovalAndLoginReturnsUsableSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nonce := time.Now().UnixNano()
	pool := workspaceTestPool(t, fmt.Sprintf("login-workspace-setup-%d", nonce))
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	svc := identity.New(pool)
	register := func(prefix string) identity.Account {
		t.Helper()
		a, verification, err := svc.Register(ctx, fmt.Sprintf("%s-%d@example.test", prefix, nonce), "correct horse battery staple")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.VerifyEmail(ctx, verification); err != nil {
			t.Fatal(err)
		}
		return a
	}
	owner, editor := register("login-owner"), register("login-editor")
	team, err := svc.CreateTeam(ctx, owner.ID, "Login team")
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := svc.Invite(ctx, owner.ID, team.ID, editor.Email, identity.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AcceptInvitation(ctx, editor.ID, invitation.Token); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login(ctx, editor.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	personalID := first.Workspace.ID
	if _, err = svc.SwitchWorkspace(ctx, first.Token, team.ID); err != nil {
		t.Fatal(err)
	}
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	if _, err = barrier.Exec(ctx, "SELECT id FROM identity_accounts WHERE id=$1 FOR UPDATE", editor.ID); err != nil {
		t.Fatal(err)
	}
	removeName := fmt.Sprintf("login-workspace-remove-%d", nonce)
	removeService := identity.New(workspaceTestPool(t, removeName))
	removed := make(chan error, 1)
	go func() { removed <- removeService.RemoveMember(ctx, owner.ID, team.ID, editor.ID) }()
	waitForWorkspaceLock(t, ctx, pool, removeName)
	loginName := fmt.Sprintf("login-workspace-login-%d", nonce)
	loginService := identity.New(workspaceTestPool(t, loginName))
	type loginResult struct {
		session identity.LoginSession
		err     error
	}
	loggedIn := make(chan loginResult, 1)
	go func() {
		session, loginErr := loginService.Login(ctx, editor.Email, "correct horse battery staple")
		loggedIn <- loginResult{session, loginErr}
	}()
	waitForWorkspaceLock(t, ctx, pool, loginName)
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-removed; err != nil {
		t.Fatal(err)
	}
	result := <-loggedIn
	if result.err != nil {
		t.Fatalf("login after removal: %v", result.err)
	}
	principal, err := svc.Authenticate(ctx, result.session.Token)
	if err != nil || principal.Workspace.ID != personalID {
		t.Fatalf("new login after removal = %+v, %v; want personal workspace", principal, err)
	}
}

func TestWorkspaceSwitchDoesNotRestoreRevokedDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nonce := time.Now().UnixNano()
	pool := workspaceTestPool(t, fmt.Sprintf("revoked-switch-setup-%d", nonce))
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	svc := identity.New(pool)
	a, verify, err := svc.Register(ctx, fmt.Sprintf("revoked-switch-%d@example.test", nonce), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.VerifyEmail(ctx, verify); err != nil {
		t.Fatal(err)
	}
	team, err := svc.CreateTeam(ctx, a.ID, "Revoked device team")
	if err != nil {
		t.Fatal(err)
	}
	device, err := svc.Login(ctx, a.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	if _, err = barrier.Exec(ctx, "SELECT id FROM identity_accounts WHERE id=$1 FOR UPDATE", a.ID); err != nil {
		t.Fatal(err)
	}
	switchName := fmt.Sprintf("revoked-switch-%d", nonce)
	switchService := identity.New(workspaceTestPool(t, switchName))
	switched := make(chan error, 1)
	go func() {
		_, switchErr := switchService.SwitchWorkspace(ctx, device.Token, team.ID)
		switched <- switchErr
	}()
	waitForWorkspaceLock(t, ctx, pool, switchName)
	if err = svc.Logout(ctx, device.Token); err != nil {
		t.Fatal(err)
	}
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-switched; !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("switch after logout = %v, want unauthorized", err)
	}
	if _, err = svc.Authenticate(ctx, device.Token); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked device authenticated: %v", err)
	}
	next, err := svc.Login(ctx, a.Email, "correct horse battery staple")
	if err != nil || next.Workspace.ID != device.Workspace.ID {
		t.Fatalf("revoked switch changed next login = %+v, %v", next.Workspace, err)
	}
}

func TestConcurrentWorkspaceSwitchKeepsDeviceChoices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nonce := time.Now().UnixNano()
	pool := workspaceTestPool(t, fmt.Sprintf("device-switch-setup-%d", nonce))
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	svc := identity.New(pool)
	a, verify, err := svc.Register(ctx, fmt.Sprintf("device-switch-%d@example.test", nonce), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.VerifyEmail(ctx, verify); err != nil {
		t.Fatal(err)
	}
	teamA, err := svc.CreateTeam(ctx, a.ID, "Device team A")
	if err != nil {
		t.Fatal(err)
	}
	teamB, err := svc.CreateTeam(ctx, a.ID, "Device team B")
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login(ctx, a.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Login(ctx, a.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	switchBoth := func(firstToken, secondToken, phase string) {
		t.Helper()
		barrier, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer barrier.Rollback(context.Background())
		if _, err = barrier.Exec(ctx, "SELECT id FROM identity_accounts WHERE id=$1 FOR UPDATE", a.ID); err != nil {
			t.Fatal(err)
		}
		nameA := fmt.Sprintf("device-switch-A-%s-%d", phase, nonce)
		nameB := fmt.Sprintf("device-switch-B-%s-%d", phase, nonce)
		svcA := identity.New(workspaceTestPool(t, nameA))
		svcB := identity.New(workspaceTestPool(t, nameB))
		finished := make(chan error, 2)
		go func() { _, e := svcA.SwitchWorkspace(ctx, firstToken, teamA.ID); finished <- e }()
		go func() { _, e := svcB.SwitchWorkspace(ctx, secondToken, teamB.ID); finished <- e }()
		waitForWorkspaceLock(t, ctx, pool, nameA)
		waitForWorkspaceLock(t, ctx, pool, nameB)
		if err = barrier.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err = <-finished; err != nil {
				t.Fatalf("concurrent device switch: %v", err)
			}
		}
	}
	switchBoth(first.Token, second.Token, "two-devices")
	for _, device := range []struct{ token, workspaceID string }{{first.Token, teamA.ID}, {second.Token, teamB.ID}} {
		p, authErr := svc.Authenticate(ctx, device.token)
		if authErr != nil || p.Workspace.ID != device.workspaceID {
			t.Fatalf("device selection = %+v, %v; want %s", p.Workspace, authErr, device.workspaceID)
		}
	}
	// Both requests from one device must commit its selection together with
	// the next-login preference, while other devices keep their own context.
	switchBoth(first.Token, first.Token, "one-device")
	p, err := svc.Authenticate(ctx, first.Token)
	if err != nil {
		t.Fatal(err)
	}
	next, err := svc.Login(ctx, a.Email, "correct horse battery staple")
	if err != nil || next.Workspace.ID != p.Workspace.ID {
		t.Fatalf("next login = %+v, %v; want final device selection %s", next.Workspace, err, p.Workspace.ID)
	}
	p, err = svc.Authenticate(ctx, second.Token)
	if err != nil || p.Workspace.ID != teamB.ID {
		t.Fatalf("other device selection = %+v, %v; want team B", p.Workspace, err)
	}
}

func TestConcurrentPasswordResetConsumesAllLinksAndRevokesDevices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nonce := time.Now().UnixNano()
	pool := workspaceTestPool(t, fmt.Sprintf("reset-concurrency-setup-%d", nonce))
	if err := identity.InitSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	svc := identity.New(pool)
	a, verify, err := svc.Register(ctx, fmt.Sprintf("reset-concurrency-%d@example.test", nonce), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.VerifyEmail(ctx, verify); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login(ctx, a.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Login(ctx, a.Email, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	tokenA, err := svc.RequestPasswordReset(ctx, a.Email)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := svc.RequestPasswordReset(ctx, a.Email)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	if _, err = barrier.Exec(ctx, "SELECT id FROM identity_accounts WHERE id=$1 FOR UPDATE", a.ID); err != nil {
		t.Fatal(err)
	}
	nameA := fmt.Sprintf("reset-concurrency-A-%d", nonce)
	nameB := fmt.Sprintf("reset-concurrency-B-%d", nonce)
	svcA := identity.New(workspaceTestPool(t, nameA))
	svcB := identity.New(workspaceTestPool(t, nameB))
	type resetResult struct {
		password string
		err      error
	}
	completed := make(chan resetResult, 2)
	go func() {
		const password = "reset password from link A"
		completed <- resetResult{password, svcA.ResetPassword(ctx, tokenA, password)}
	}()
	go func() {
		const password = "reset password from link B"
		completed <- resetResult{password, svcB.ResetPassword(ctx, tokenB, password)}
	}()
	waitForWorkspaceLock(t, ctx, pool, nameA)
	waitForWorkspaceLock(t, ctx, pool, nameB)
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var winningPassword string
	for range 2 {
		result := <-completed
		if result.err == nil {
			if winningPassword != "" {
				t.Fatal("both password-reset links succeeded")
			}
			winningPassword = result.password
		} else if !errors.Is(result.err, identity.ErrExpired) {
			t.Fatalf("concurrent password reset = %v, want success or expired", result.err)
		}
	}
	if winningPassword == "" {
		t.Fatal("neither password-reset link succeeded")
	}
	for _, device := range []identity.LoginSession{first, second} {
		if _, err = svc.Authenticate(ctx, device.Token); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("old device authenticated after password reset: %v", err)
		}
	}
	for _, token := range []string{tokenA, tokenB} {
		if err = svc.ResetPassword(ctx, token, "reused password-reset link"); !errors.Is(err, identity.ErrExpired) {
			t.Fatalf("reset link reusable after reset: %v", err)
		}
	}
	if _, err = svc.Login(ctx, a.Email, winningPassword); err != nil {
		t.Fatalf("winning password cannot login: %v", err)
	}
}
