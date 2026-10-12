package server

import (
	"net/http"
	"strings"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
)

func (s *Server) requireWorkspace(w http.ResponseWriter, r *http.Request, owner bool) (identity.Principal, bool) {
	p, ok := principalFrom(r)
	if !ok || r.PathValue("id") != p.Workspace.ID {
		writeError(w, http.StatusNotFound, "workspace not found")
		return identity.Principal{}, false
	}
	if owner && p.Workspace.Role != identity.RoleOwner {
		writeError(w, http.StatusForbidden, "workspace owner required")
		return identity.Principal{}, false
	}
	return p, true
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	p, _ := principalFrom(r)
	ws, err := s.cfg.Identity.CreateTeam(r.Context(), p.Account.ID, req.Name)
	if err != nil {
		identityError(w, err)
		return
	}
	writeJSON(w, 201, ws)
}

func (s *Server) handleSwitchWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	c, _ := r.Cookie(sessionCookie)
	ws, err := s.cfg.Identity.SwitchWorkspace(r.Context(), c.Value, req.ID)
	if err != nil {
		identityError(w, err)
		return
	}
	writeJSON(w, 200, ws)
}

func (s *Server) requestedWorkspace(w http.ResponseWriter, r *http.Request, owner bool) (identity.Principal, identity.Workspace, bool) {
	p, ok := principalFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return identity.Principal{}, identity.Workspace{}, false
	}
	workspaces, err := s.cfg.Identity.ListWorkspaces(r.Context(), p.Account.ID)
	if err != nil {
		identityError(w, err)
		return identity.Principal{}, identity.Workspace{}, false
	}
	for _, workspace := range workspaces {
		if workspace.ID != r.PathValue("id") {
			continue
		}
		if owner && workspace.Role != identity.RoleOwner {
			writeError(w, http.StatusForbidden, "workspace owner required")
			return identity.Principal{}, identity.Workspace{}, false
		}
		return p, workspace, true
	}
	writeError(w, http.StatusNotFound, "workspace not found")
	return identity.Principal{}, identity.Workspace{}, false
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	p, workspace, ok := s.requestedWorkspace(w, r, true)
	if !ok {
		return
	}
	if r.Header.Get("X-Confirm-Delete") != workspace.Name {
		writeError(w, 400, "confirm workspace name")
		return
	}
	if err := s.cfg.Identity.DeleteTeam(r.Context(), p.Account.ID, workspace.ID); err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleLeaveWorkspace(w http.ResponseWriter, r *http.Request) {
	p, workspace, ok := s.requestedWorkspace(w, r, false)
	if !ok {
		return
	}
	if err := s.cfg.Identity.LeaveWorkspace(r.Context(), p.Account.ID, workspace.ID); err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r)
	if r.Header.Get("X-Confirm-Delete") != p.Account.Email {
		writeError(w, 400, "confirm account email")
		return
	}
	if err := s.cfg.Identity.DeleteAccount(r.Context(), p.Account.ID); err != nil {
		identityError(w, err)
		return
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(204)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireWorkspace(w, r, false)
	if !ok {
		return
	}
	members, err := s.cfg.Identity.ListMembers(r.Context(), p.Account.ID, p.Workspace.ID)
	if err != nil {
		identityError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"members": members})
}

func (s *Server) handleSetMemberRole(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireWorkspace(w, r, true)
	if !ok {
		return
	}
	var req struct{ Role string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if err := s.cfg.Identity.SetMemberRole(r.Context(), p.Account.ID, p.Workspace.ID, r.PathValue("accountId"), req.Role); err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireWorkspace(w, r, true)
	if !ok {
		return
	}
	if err := s.cfg.Identity.RemoveMember(r.Context(), p.Account.ID, p.Workspace.ID, r.PathValue("accountId")); err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireWorkspace(w, r, true)
	if !ok {
		return
	}
	inv, err := s.cfg.Identity.ListInvitations(r.Context(), p.Account.ID, p.Workspace.ID)
	if err != nil {
		identityError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"invitations": inv})
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireWorkspace(w, r, true)
	if !ok {
		return
	}
	var req struct{ Email, Role string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if req.Role == "" {
		req.Role = identity.RoleEditor
	}
	inv, err := s.cfg.Identity.Invite(r.Context(), p.Account.ID, p.Workspace.ID, req.Email, req.Role)
	if err != nil {
		identityError(w, err)
		return
	}
	if err := s.mailLink(r, "invite", inv.Token, inv.Email, "Atoms 工作区邀请", "您收到工作区邀请："); err != nil {
		_ = s.cfg.Identity.RevokeInvitation(r.Context(), p.Account.ID, inv.ID)
		writeError(w, 503, "invitation email unavailable")
		return
	}
	writeJSON(w, 201, map[string]any{"id": inv.ID, "email": inv.Email, "role": inv.Role, "expiresAt": inv.ExpiresAt})
}

func (s *Server) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireWorkspace(w, r, true)
	if !ok {
		return
	}
	if err := s.cfg.Identity.RevokeInvitation(r.Context(), p.Account.ID, r.PathValue("invitationId")); err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r)
	var req struct{ Token string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	ws, err := s.cfg.Identity.AcceptInvitation(r.Context(), p.Account.ID, req.Token)
	if err != nil {
		identityError(w, err)
		return
	}
	writeJSON(w, 200, ws)
}

func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r)
	accounts, err := s.cfg.Identity.ListAccounts(r.Context(), p.Account.ID)
	if err != nil {
		identityError(w, err)
		return
	}
	out := make([]any, 0, len(accounts))
	for _, account := range accounts {
		memberships, err := s.cfg.Identity.ListAccountMemberships(r.Context(), p.Account.ID, account.ID)
		if err != nil {
			identityError(w, err)
			return
		}
		items := make([]any, 0, len(memberships))
		for _, ws := range memberships {
			items = append(items, map[string]any{"workspaceId": ws.ID, "workspaceName": ws.Name, "role": ws.Role})
		}
		out = append(out, map[string]any{"id": account.ID, "email": account.Email, "verified": account.Verified, "disabled": account.Disabled, "memberships": items})
	}
	writeJSON(w, 200, map[string]any{"accounts": out})
}

func (s *Server) handleAdminAccount(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r)
	var req struct {
		Disabled bool
		Reason   string
	}
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeError(w, 400, "reason required")
		return
	}
	if err := s.cfg.Identity.SetAccountDisabled(r.Context(), p.Account.ID, r.PathValue("id"), req.Disabled, req.Reason); err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleAdminRescue(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r)
	var req struct{ AccountID, Reason string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if err := s.cfg.Identity.RescueOwner(r.Context(), p.Account.ID, r.PathValue("id"), req.AccountID, req.Reason); err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r)
	entries, err := s.cfg.Identity.ListAudit(r.Context(), p.Account.ID)
	if err != nil {
		identityError(w, err)
		return
	}
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{"id": e.ID, "actorId": e.ActorID, "action": e.Action, "target": e.SubjectID, "reason": e.Reason, "at": e.At})
	}
	writeJSON(w, 200, map[string]any{"entries": out})
}
