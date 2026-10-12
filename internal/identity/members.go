package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) ListMembers(ctx context.Context, actorID, workspaceID string) ([]Member, error) {
	if _, err := s.CheckRole(ctx, actorID, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT a.id,a.email,m.role FROM identity_members m JOIN identity_accounts a ON a.id=m.account_id WHERE m.workspace_id=$1 ORDER BY a.email", workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err = rows.Scan(&m.AccountID, &m.Email, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) Invite(ctx context.Context, actorID, workspaceID, email, role string) (Invitation, error) {
	email = normalizeEmail(email)
	if !validEmail(email) || !validRole(role) {
		return Invitation{}, ErrConflict
	}
	if err := s.requireOwner(ctx, actorID, workspaceID); err != nil {
		return Invitation{}, err
	}
	var kind string
	err := s.pool.QueryRow(ctx, "SELECT kind FROM identity_workspaces WHERE id=$1", workspaceID).Scan(&kind)
	if err != nil {
		return Invitation{}, err
	}
	if kind != "team" {
		return Invitation{}, ErrForbidden
	}
	token, err := random()
	if err != nil {
		return Invitation{}, err
	}
	id, err := random()
	if err != nil {
		return Invitation{}, err
	}
	expires := time.Now().Add(7 * 24 * time.Hour)
	_, err = s.pool.Exec(ctx, "INSERT INTO identity_invitations(id,workspace_id,email,role,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6)", id, workspaceID, email, role, digest(token), expires)
	return Invitation{ID: id, WorkspaceID: workspaceID, Email: email, Role: role, Token: token, ExpiresAt: expires}, err
}
func validRole(role string) bool { return role == "owner" || role == "editor" }

func (s *Service) AcceptInvitation(ctx context.Context, actorID, token string) (Workspace, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	var id, workspaceID, email, role string
	var expires time.Time
	var accepted, revoked *time.Time
	err = tx.QueryRow(ctx, "SELECT id,workspace_id,email,role,expires_at,accepted_at,revoked_at FROM identity_invitations WHERE token_hash=$1 FOR UPDATE", digest(token)).Scan(&id, &workspaceID, &email, &role, &expires, &accepted, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrExpired
	}
	if err != nil {
		return Workspace{}, err
	}
	if accepted != nil || revoked != nil || !expires.After(time.Now()) {
		return Workspace{}, ErrExpired
	}
	var accountEmail string
	var verified, disabled bool
	err = tx.QueryRow(ctx, "SELECT email,verified,disabled FROM identity_accounts WHERE id=$1", actorID).Scan(&accountEmail, &verified, &disabled)
	if errors.Is(err, pgx.ErrNoRows) || accountEmail != email || !verified || disabled {
		return Workspace{}, ErrForbidden
	}
	if err != nil {
		return Workspace{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity_members(workspace_id,account_id,role) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", workspaceID, actorID, role); err != nil {
		return Workspace{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_invitations SET accepted_at=now() WHERE id=$1", id); err != nil {
		return Workspace{}, err
	}
	w, err := workspace(tx.QueryRow(ctx, "SELECT w.id,w.name,w.kind,m.role FROM identity_workspaces w JOIN identity_members m ON m.workspace_id=w.id AND m.account_id=$1 WHERE w.id=$2", actorID, workspaceID))
	if err != nil {
		return Workspace{}, err
	}
	return w, tx.Commit(ctx)
}

func (s *Service) RevokeInvitation(ctx context.Context, actorID, invitationID string) error {
	var workspaceID string
	err := s.pool.QueryRow(ctx, "SELECT workspace_id FROM identity_invitations WHERE id=$1", invitationID).Scan(&workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExpired
	}
	if err != nil {
		return err
	}
	if err = s.requireOwner(ctx, actorID, workspaceID); err != nil {
		return err
	}
	result, err := s.pool.Exec(ctx, "UPDATE identity_invitations SET revoked_at=now() WHERE id=$1 AND accepted_at IS NULL AND revoked_at IS NULL", invitationID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrExpired
	}
	return nil
}

func (s *Service) SetMemberRole(ctx context.Context, actorID, workspaceID, targetID, role string) error {
	if !validRole(role) {
		return ErrConflict
	}
	return s.changeMember(ctx, actorID, workspaceID, targetID, role, false)
}
func (s *Service) RemoveMember(ctx context.Context, actorID, workspaceID, targetID string) error {
	return s.changeMember(ctx, actorID, workspaceID, targetID, "", true)
}
func (s *Service) LeaveWorkspace(ctx context.Context, actorID, workspaceID string) error {
	return s.changeMember(ctx, actorID, workspaceID, actorID, "", true)
}
func (s *Service) changeMember(ctx context.Context, actorID, workspaceID, targetID, newRole string, remove bool) error {
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
	if kind == "personal" {
		return ErrForbidden
	}
	var actorRole, targetRole string
	err = tx.QueryRow(ctx, "SELECT role FROM identity_members WHERE workspace_id=$1 AND account_id=$2", workspaceID, actorID).Scan(&actorRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if actorID != targetID || !remove {
		if actorRole != "owner" {
			return ErrForbidden
		}
	}
	err = tx.QueryRow(ctx, "SELECT role FROM identity_members WHERE workspace_id=$1 AND account_id=$2", workspaceID, targetID).Scan(&targetRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if targetRole == "owner" && (remove || newRole != "owner") {
		var owners int
		err = tx.QueryRow(ctx, "SELECT count(*) FROM identity_members WHERE workspace_id=$1 AND role='owner'", workspaceID).Scan(&owners)
		if err != nil {
			return err
		}
		if owners <= 1 {
			return ErrConflict
		}
	}
	if remove {
		_, err = tx.Exec(ctx, "DELETE FROM identity_members WHERE workspace_id=$1 AND account_id=$2", workspaceID, targetID)
	} else {
		_, err = tx.Exec(ctx, "UPDATE identity_members SET role=$3 WHERE workspace_id=$1 AND account_id=$2", workspaceID, targetID, newRole)
	}
	if err != nil {
		return err
	}
	// A removed member keeps their devices, with no remaining team context.
	if remove {
		if err = returnToPersonalWorkspace(ctx, tx, workspaceID, targetID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) ListInvitations(ctx context.Context, actorID, workspaceID string) ([]Invitation, error) {
	if err := s.requireOwner(ctx, actorID, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT id,email,role,expires_at FROM identity_invitations WHERE workspace_id=$1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now() ORDER BY expires_at", workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var inv Invitation
		inv.WorkspaceID = workspaceID
		if err = rows.Scan(&inv.ID, &inv.Email, &inv.Role, &inv.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}
func normalizeName(name string) string { return strings.TrimSpace(name) }
