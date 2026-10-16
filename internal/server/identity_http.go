package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/haobinfei/Atoms-Demo/internal/observability"
)

const sessionCookie = "atoms_session"

type principalKey struct{}

func principalFrom(r *http.Request) (identity.Principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(identity.Principal)
	return p, ok
}

func (s *Server) identityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				scheme := "http"
				if r.TLS != nil || s.cfg.SecureCookies {
					scheme = "https"
				}
				if err != nil || u.Host != r.Host || u.Scheme != scheme {
					writeError(w, http.StatusForbidden, "cross-origin request rejected")
					return
				}
			} else if r.Header.Get("Sec-Fetch-Site") != "same-origin" {
				writeError(w, http.StatusForbidden, "request origin required")
				return
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, http.StatusForbidden, "cross-site request rejected")
				return
			}
		}
		// Prometheus 抓取不带会话 Cookie，所以 /metrics 必须是公开路径
		// （ADR-0018）。它同时被观测中间件排除在埋点之外。
		public := r.URL.Path == observability.MetricsPath ||
			r.URL.Path == "/api/health" || strings.HasPrefix(r.URL.Path, "/api/auth/register") ||
			r.URL.Path == "/api/auth/verify" || r.URL.Path == "/api/auth/login" ||
			r.URL.Path == "/api/auth/resend-verification" || r.URL.Path == "/api/auth/forgot-password" ||
			r.URL.Path == "/api/auth/reset-password" || strings.HasPrefix(r.URL.Path, "/app/") ||
			strings.HasPrefix(r.URL.Path, "/api/pay/") || strings.HasPrefix(r.URL.Path, "/pay/")
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			p, err := s.cfg.Identity.Authenticate(r.Context(), c.Value)
			if err == nil {
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
			} else if !public {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
		}
		if !public {
			p, ok := principalFrom(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if s.ownerOnly(r) && p.Workspace.Role != identity.RoleOwner {
				writeError(w, http.StatusForbidden, "workspace owner required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ownerOnly(r *http.Request) bool {
	p := r.URL.Path
	return (r.Method == http.MethodDelete && strings.HasPrefix(p, "/api/sessions/")) ||
		(r.Method == http.MethodPost && (strings.HasSuffix(p, "/publish") || strings.HasSuffix(p, "/unpublish")))
}

func (s *Server) canViewApp(r *http.Request, owner, id string) bool {
	exists, err := s.cfg.Identity.WorkspaceExists(r.Context(), owner)
	if err != nil || !exists {
		return false
	}
	sess, err := s.store.Load(owner, id)
	if err != nil {
		return false
	}
	if p, ok := principalFrom(r); ok && p.Workspace.ID == owner {
		return true
	}
	if strings.HasPrefix(r.PathValue("path"), "contest/") {
		return false
	}
	return len(sess.Publishes) > 0 && !sess.Publishes[len(sess.Publishes)-1].Unpublished
}

func (s *Server) registerIdentityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/verify", s.handleVerify)
	mux.HandleFunc("POST /api/auth/resend-verification", s.handleResendVerification)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/auth/forgot-password", s.handleForgotPassword)
	mux.HandleFunc("POST /api/auth/reset-password", s.handleResetPassword)
	mux.HandleFunc("POST /api/auth/change-password", s.handleChangePassword)
	mux.HandleFunc("DELETE /api/account", s.handleDeleteAccount)
	mux.HandleFunc("POST /api/workspaces", s.handleCreateWorkspace)
	mux.HandleFunc("POST /api/workspaces/current", s.handleSwitchWorkspace)
	mux.HandleFunc("DELETE /api/workspaces/{id}", s.handleDeleteWorkspace)
	mux.HandleFunc("POST /api/workspaces/{id}/leave", s.handleLeaveWorkspace)
	mux.HandleFunc("GET /api/workspaces/{id}/members", s.handleListMembers)
	mux.HandleFunc("PATCH /api/workspaces/{id}/members/{accountId}", s.handleSetMemberRole)
	mux.HandleFunc("DELETE /api/workspaces/{id}/members/{accountId}", s.handleRemoveMember)
	mux.HandleFunc("GET /api/workspaces/{id}/invitations", s.handleListInvitations)
	mux.HandleFunc("POST /api/workspaces/{id}/invitations", s.handleInvite)
	mux.HandleFunc("DELETE /api/workspaces/{id}/invitations/{invitationId}", s.handleRevokeInvitation)
	mux.HandleFunc("POST /api/invitations/accept", s.handleAcceptInvitation)
	mux.HandleFunc("GET /api/admin/accounts", s.handleAdminAccounts)
	mux.HandleFunc("PATCH /api/admin/accounts/{id}", s.handleAdminAccount)
	mux.HandleFunc("POST /api/admin/workspaces/{id}/rescue", s.handleAdminRescue)
	mux.HandleFunc("GET /api/admin/audit", s.handleAdminAudit)
}

func readBody(r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(dst)
}

func identityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthorized), errors.Is(err, identity.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, identity.ErrForbidden):
		writeError(w, http.StatusForbidden, "permission denied")
	case errors.Is(err, identity.ErrConflict):
		writeError(w, http.StatusConflict, "operation conflicts with current state")
	case errors.Is(err, identity.ErrExpired):
		writeError(w, http.StatusGone, "link expired or already used")
	default:
		writeError(w, http.StatusInternalServerError, "request failed")
	}
}

func (s *Server) authClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	for _, prefix := range s.cfg.TrustedProxyCIDRs {
		if prefix.Contains(peer) {
			// Our Nginx overwrites X-Real-IP; arbitrary X-Forwarded-For is ignored.
			if client, err := netip.ParseAddr(r.Header.Get("X-Real-IP")); err == nil {
				return client.Unmap().String()
			}
			break
		}
	}
	return peer.String()
}

func (s *Server) allowAuth(w http.ResponseWriter, r *http.Request, kind, value string) bool {
	key := sha256.Sum256([]byte(kind + "|" + r.URL.Path + "|" + value))
	allowed, err := s.cfg.Identity.AllowAuthAttempt(r.Context(), hex.EncodeToString(key[:]), 600, 20)
	if err != nil {
		// 限流存储不可用被当成拒绝：这条必须能看见，否则「登录突然全挂」会被
		// 误判成用户忘记密码。
		observability.Obs().AuthRateLimited.Add(r.Context(), 1, observability.Attrs(
			observability.AttrScope.String(kind),
			observability.AttrOutcome.String(observability.OutcomeUnavailable),
		))
		slog.Error("authentication rate limit unavailable", "error", err)
		writeError(w, http.StatusServiceUnavailable, "try again later")
		return false
	}
	if !allowed {
		observability.Obs().AuthRateLimited.Add(r.Context(), 1, observability.Attrs(
			observability.AttrScope.String(kind),
			observability.AttrOutcome.String(observability.OutcomeExceeded),
		))
		writeError(w, http.StatusTooManyRequests, "try again later")
		return false
	}
	return true
}

func (s *Server) allowAuthIP(w http.ResponseWriter, r *http.Request) bool {
	return s.allowAuth(w, r, "ip", s.authClientIP(r))
}

func (s *Server) allowAuthEmail(w http.ResponseWriter, r *http.Request, email string) bool {
	return s.allowAuth(w, r, "email", strings.ToLower(strings.TrimSpace(email)))
}

func (s *Server) mailLink(r *http.Request, query, token, to, subject, body string) error {
	if s.cfg.Mailer == nil {
		return errors.New("mail sender unavailable")
	}
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		base = "http://" + r.Host
	}
	link := base + "/?" + query + "=" + url.QueryEscape(token)
	return s.cfg.Mailer.Send(r.Context(), to, subject, body+"\n\n"+link)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, Expires: expires})
}
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	s.setSessionCookie(w, r, "", time.Unix(0, 0))
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.allowAuthIP(w, r) {
		return
	}
	var req struct{ Email, Password string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if !s.allowAuthEmail(w, r, req.Email) {
		return
	}
	a, token, err := s.cfg.Identity.Register(r.Context(), req.Email, req.Password)
	if errors.Is(err, identity.ErrConflict) {
		writeJSON(w, 202, map[string]string{"status": "check-email"})
		return
	}
	if err != nil {
		identityError(w, err)
		return
	}
	if err := s.mailLink(r, "verify", token, a.Email, "验证 Atoms 账户", "请验证邮箱以启用账户："); err != nil {
		slog.Error("verification email delivery failed")
	}
	writeJSON(w, 202, map[string]string{"status": "check-email"})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req struct{ Token string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if !s.allowAuthIP(w, r) {
		return
	}
	_, err := s.cfg.Identity.VerifyEmail(r.Context(), req.Token)
	if err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResendVerification(w http.ResponseWriter, r *http.Request) {
	if !s.allowAuthIP(w, r) {
		return
	}
	var req struct{ Email string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if !s.allowAuthEmail(w, r, req.Email) {
		return
	}
	token, err := s.cfg.Identity.ResendVerification(r.Context(), req.Email)
	if err != nil {
		identityError(w, err)
		return
	}
	if token != "" {
		if err := s.mailLink(r, "verify", token, req.Email, "验证 Atoms 账户", "请验证邮箱以启用账户："); err != nil {
			slog.Error("verification email delivery failed")
		}
	}
	writeJSON(w, 202, map[string]string{"status": "check-email"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.allowAuthIP(w, r) {
		return
	}
	var req struct{ Email, Password string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if !s.allowAuthEmail(w, r, req.Email) {
		return
	}
	login, err := s.cfg.Identity.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		if errors.Is(err, identity.ErrInvalidCredentials) || errors.Is(err, identity.ErrForbidden) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
		} else {
			identityError(w, err)
		}
		return
	}
	s.setSessionCookie(w, r, login.Token, login.ExpiresAt)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.cfg.Identity.Logout(r.Context(), c.Value)
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	if !s.allowAuthIP(w, r) {
		return
	}
	var req struct{ Email string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if !s.allowAuthEmail(w, r, req.Email) {
		return
	}
	token, err := s.cfg.Identity.RequestPasswordReset(r.Context(), req.Email)
	if err != nil {
		identityError(w, err)
		return
	}
	if token != "" {
		if err := s.mailLink(r, "reset", token, req.Email, "重置 Atoms 密码", "请使用以下链接重置密码："); err != nil {
			slog.Error("password reset email delivery failed")
		}
	}
	writeJSON(w, 202, map[string]string{"status": "check-email"})
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !s.allowAuthIP(w, r) {
		return
	}
	var req struct{ Token, Password string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if err := s.cfg.Identity.ResetPassword(r.Context(), req.Token, req.Password); err != nil {
		identityError(w, err)
		return
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(204)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ CurrentPassword, NewPassword string }
	if readBody(r, &req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	c, _ := r.Cookie(sessionCookie)
	if err := s.cfg.Identity.ChangePassword(r.Context(), c.Value, req.CurrentPassword, req.NewPassword); err != nil {
		identityError(w, err)
		return
	}
	w.WriteHeader(204)
}
