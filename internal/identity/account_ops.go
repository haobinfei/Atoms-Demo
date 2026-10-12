package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func (s *Service) RequestPasswordReset(ctx context.Context, email string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, "SELECT id FROM identity_accounts WHERE email=$1 AND verified AND NOT disabled", normalizeEmail(email)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	token, err := random()
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, "INSERT INTO identity_credentials(token_hash,account_id,purpose,expires_at) VALUES($1,$2,'reset',$3)", digest(token), id, time.Now().Add(time.Hour))
	return token, err
}

func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if len(newPassword) < 12 {
		return ErrInvalidCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	var accountID string
	err = s.pool.QueryRow(ctx, "SELECT account_id FROM identity_credentials WHERE token_hash=$1 AND purpose='reset' AND used_at IS NULL AND expires_at>now()", digest(token)).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExpired
	}
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize resets for an account before consuming any credential. Two
	// different links must not each hold a credential while waiting for the
	// other reset to update the account and invalidate all remaining links.
	var verified, disabled bool
	err = tx.QueryRow(ctx, "SELECT verified,disabled FROM identity_accounts WHERE id=$1 FOR UPDATE", accountID).Scan(&verified, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExpired
	}
	if err != nil {
		return err
	}
	if !verified || disabled {
		return ErrForbidden
	}
	err = tx.QueryRow(ctx, "UPDATE identity_credentials SET used_at=now() WHERE token_hash=$1 AND account_id=$2 AND purpose='reset' AND used_at IS NULL AND expires_at>now() RETURNING account_id", digest(token), accountID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExpired
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_accounts SET password_hash=$1 WHERE id=$2 AND NOT disabled", string(hash), accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_credentials SET used_at=now() WHERE account_id=$1 AND purpose='reset' AND used_at IS NULL", accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL", accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ChangePassword(ctx context.Context, sessionToken, currentPassword, newPassword string) error {
	if len(newPassword) < 12 {
		return ErrInvalidCredentials
	}
	p, err := s.Authenticate(ctx, sessionToken)
	if err != nil {
		return err
	}
	var oldHash string
	err = s.pool.QueryRow(ctx, "SELECT password_hash FROM identity_accounts WHERE id=$1", p.Account.ID).Scan(&oldHash)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(oldHash), []byte(currentPassword)) != nil {
		return ErrInvalidCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, "UPDATE identity_accounts SET password_hash=$1 WHERE id=$2 AND password_hash=$3", string(newHash), p.Account.ID, oldHash)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_sessions SET revoked_at=now() WHERE account_id=$1 AND id<>$2 AND revoked_at IS NULL", p.Account.ID, p.SessionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) DeleteTeam(ctx context.Context, actorID, workspaceID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var kind string
	err = tx.QueryRow(ctx, "SELECT kind FROM identity_workspaces WHERE id=$1 FOR UPDATE", workspaceID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if kind != "team" {
		return ErrForbidden
	}
	var role string
	err = tx.QueryRow(ctx, "SELECT role FROM identity_members WHERE workspace_id=$1 AND account_id=$2", workspaceID, actorID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || role != "owner" {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if err = returnToPersonalWorkspace(ctx, tx, workspaceID, ""); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM identity_workspaces WHERE id=$1", workspaceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// returnToPersonalWorkspace keeps affected devices signed in after their team
// context disappears. The deleted/removed team is no longer an authorization
// context, and later logins also resume in the account's personal workspace.
func returnToPersonalWorkspace(ctx context.Context, tx pgx.Tx, workspaceID, accountID string) error {
	// Lock every affected account before touching any session, in the same
	// order as password and account-state changes. Include devices selected
	// in this team even when another device last selected a different team.
	rows, err := tx.Query(ctx, `SELECT a.id FROM identity_accounts a
 WHERE ($2='' OR a.id=$2) AND (a.last_workspace_id=$1 OR EXISTS(
 SELECT 1 FROM identity_sessions s WHERE s.account_id=a.id AND s.workspace_id=$1))
 ORDER BY a.id FOR UPDATE OF a`, workspaceID, accountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE identity_accounts a SET last_workspace_id=m.workspace_id
 FROM identity_members m JOIN identity_workspaces w ON w.id=m.workspace_id AND w.kind='personal'
 WHERE a.id=m.account_id AND a.last_workspace_id=$1 AND ($2='' OR a.id=$2)`, workspaceID, accountID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE identity_sessions s SET workspace_id=m.workspace_id
 FROM identity_members m JOIN identity_workspaces w ON w.id=m.workspace_id AND w.kind='personal'
 WHERE s.account_id=m.account_id AND s.workspace_id=$1 AND ($2='' OR s.account_id=$2)`, workspaceID, accountID)
	return err
}

func (s *Service) DeleteAccount(ctx context.Context, accountID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lock all team rows in a stable order before checking owner counts.
	rows, err := tx.Query(ctx, "SELECT w.id FROM identity_workspaces w JOIN identity_members m ON m.workspace_id=w.id WHERE m.account_id=$1 AND w.kind='team' ORDER BY w.id FOR UPDATE OF w", accountID)
	if err != nil {
		return err
	}
	var teams []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		teams = append(teams, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range teams {
		var role string
		var owners int
		err = tx.QueryRow(ctx, "SELECT role FROM identity_members WHERE workspace_id=$1 AND account_id=$2", id, accountID).Scan(&role)
		if err != nil {
			return err
		}
		if role == "owner" {
			err = tx.QueryRow(ctx, "SELECT count(*) FROM identity_members WHERE workspace_id=$1 AND role='owner'", id).Scan(&owners)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrConflict
			}
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM identity_workspaces WHERE kind='personal' AND id IN (SELECT workspace_id FROM identity_members WHERE account_id=$1)", accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM identity_accounts WHERE id=$1", accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) admin(ctx context.Context, actorID string) error {
	var email string
	var verified, disabled bool
	err := s.pool.QueryRow(ctx, "SELECT email,verified,disabled FROM identity_accounts WHERE id=$1", actorID).Scan(&email, &verified, &disabled)
	if errors.Is(err, pgx.ErrNoRows) || !verified || disabled || !s.admins[email] {
		return ErrForbidden
	}
	return err
}
func (s *Service) IsAdmin(ctx context.Context, actorID string) bool {
	return s.admin(ctx, actorID) == nil
}
func (s *Service) ListAccounts(ctx context.Context, actorID string) ([]Account, error) {
	if err := s.admin(ctx, actorID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT id,email,verified,disabled FROM identity_accounts ORDER BY created_at DESC LIMIT 200")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Email, &a.Verified, &a.Disabled); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) ListAccountMemberships(ctx context.Context, actorID, targetID string) ([]Workspace, error) {
	if err := s.admin(ctx, actorID); err != nil {
		return nil, err
	}
	return s.ListWorkspaces(ctx, targetID)
}

func (s *Service) SetAccountDisabled(ctx context.Context, actorID, targetID string, disabled bool, reason string) error {
	if err := s.admin(ctx, actorID); err != nil {
		return err
	}
	if reason == "" {
		return ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, "UPDATE identity_accounts SET disabled=$1 WHERE id=$2", disabled, targetID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrForbidden
	}
	if disabled {
		if _, err = tx.Exec(ctx, "UPDATE identity_sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL", targetID); err != nil {
			return err
		}
	}
	action := "account_restore"
	if disabled {
		action = "account_disable"
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity_audit(actor_id,action,subject_id,reason) VALUES($1,$2,$3,$4)", actorID, action, targetID, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) RescueOwner(ctx context.Context, actorID, workspaceID, newOwnerID, reason string) error {
	if err := s.admin(ctx, actorID); err != nil {
		return err
	}
	if reason == "" {
		return ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var kind string
	err = tx.QueryRow(ctx, "SELECT kind FROM identity_workspaces WHERE id=$1 FOR UPDATE", workspaceID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) || kind != "team" {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, "UPDATE identity_members SET role='owner' WHERE workspace_id=$1 AND account_id=$2", workspaceID, newOwnerID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrForbidden
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity_audit(actor_id,action,subject_id,reason) VALUES($1,'owner_rescue',$2,$3)", actorID, workspaceID, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) ListAudit(ctx context.Context, actorID string) ([]AuditEntry, error) {
	if err := s.admin(ctx, actorID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT id,COALESCE(actor_id,''),action,subject_id,reason,at FROM identity_audit ORDER BY id DESC LIMIT 200")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if err = rows.Scan(&a.ID, &a.ActorID, &a.Action, &a.SubjectID, &a.Reason, &a.At); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
