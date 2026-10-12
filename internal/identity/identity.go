package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

//go:embed schema.sql
var schema embed.FS

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrForbidden          = errors.New("forbidden")
	ErrConflict           = errors.New("conflict")
	ErrExpired            = errors.New("expired or already used")
)

const (
	RoleOwner         = "owner"
	RoleEditor        = "editor"
	WorkspacePersonal = "personal"
	WorkspaceTeam     = "team"
)

// dummyPasswordHash has the registration cost and makes unknown-account logins
// perform the same password comparison as known accounts. It is not a credential.
const dummyPasswordHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

type Account struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Verified bool   `json:"verified"`
	Disabled bool   `json:"disabled"`
}
type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}
type Principal struct {
	Account   Account
	Workspace Workspace
	SessionID string
}
type LoginSession struct {
	Token     string
	ExpiresAt time.Time
	Account   Account
	Workspace Workspace
}
type Invitation struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	Token       string    `json:"-"`
	ExpiresAt   time.Time `json:"expiresAt"`
}
type Member struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
	AccountID   string `json:"accountId"`
	Email       string `json:"email"`
	Role        string `json:"role"`
}
type AuditEntry struct {
	ID                                 int64
	ActorID, Action, SubjectID, Reason string
	At                                 time.Time
}
type Service struct {
	pool          *pgxpool.Pool
	admins        map[string]bool
	limitRequests atomic.Uint64
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }
func (s *Service) SetAdminEmails(emails []string) {
	s.admins = make(map[string]bool, len(emails))
	for _, email := range emails {
		s.admins[normalizeEmail(email)] = true
	}
}

// InitSchema is for explicit local/test setup; production migrations are owned by gitops.
func InitSchema(ctx context.Context, pool *pgxpool.Pool) error {
	sql, err := schema.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol)
	return err
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func digest(raw string) string           { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
func validEmail(email string) bool {
	i := strings.IndexByte(email, '@')
	return i > 0 && i < len(email)-3 && !strings.ContainsAny(email, " \t\n")
}
func account(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Email, &a.Verified, &a.Disabled)
	return a, err
}
func workspace(row pgx.Row) (Workspace, error) {
	var w Workspace
	err := row.Scan(&w.ID, &w.Name, &w.Kind, &w.Role)
	return w, err
}

func (s *Service) Register(ctx context.Context, email, password string) (Account, string, error) {
	email = normalizeEmail(email)
	if !validEmail(email) || len(password) < 12 {
		return Account{}, "", ErrInvalidCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Account{}, "", err
	}
	id, err := random()
	if err != nil {
		return Account{}, "", err
	}
	wsid, err := random()
	if err != nil {
		return Account{}, "", err
	}
	token, err := random()
	if err != nil {
		return Account{}, "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO identity_accounts(id,email,password_hash) VALUES($1,$2,$3)", id, email, string(hash)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Account{}, "", ErrConflict
		}
		return Account{}, "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity_workspaces(id,name,kind) VALUES($1,$2,'personal')", wsid, email); err != nil {
		return Account{}, "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity_members(workspace_id,account_id,role) VALUES($1,$2,'owner')", wsid, id); err != nil {
		return Account{}, "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity_credentials(token_hash,account_id,purpose,expires_at) VALUES($1,$2,'verify',$3)", digest(token), id, time.Now().Add(24*time.Hour)); err != nil {
		return Account{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return Account{}, "", err
	}
	return Account{ID: id, Email: email}, token, nil
}

func (s *Service) VerifyEmail(ctx context.Context, token string) (Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, "UPDATE identity_credentials SET used_at=now() WHERE token_hash=$1 AND purpose='verify' AND used_at IS NULL AND expires_at>now() RETURNING account_id", digest(token)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrExpired
	}
	if err != nil {
		return Account{}, err
	}
	a, err := account(tx.QueryRow(ctx, "UPDATE identity_accounts SET verified=true WHERE id=$1 RETURNING id,email,verified,disabled", id))
	if err != nil {
		return Account{}, err
	}
	return a, tx.Commit(ctx)
}

func (s *Service) ResendVerification(ctx context.Context, email string) (string, error) {
	email = normalizeEmail(email)
	token, err := random()
	if err != nil {
		return "", err
	}
	var id string
	err = s.pool.QueryRow(ctx, "SELECT id FROM identity_accounts WHERE email=$1 AND verified=false AND disabled=false", email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, "INSERT INTO identity_credentials(token_hash,account_id,purpose,expires_at) VALUES($1,$2,'verify',$3)", digest(token), id, time.Now().Add(24*time.Hour))
	return token, err
}

func (s *Service) Login(ctx context.Context, email, password string) (LoginSession, error) {
	email = normalizeEmail(email)
	var a Account
	var passwordHash string
	err := s.pool.QueryRow(ctx, "SELECT id,email,verified,disabled,password_hash FROM identity_accounts WHERE email=$1", email).Scan(&a.ID, &a.Email, &a.Verified, &a.Disabled, &passwordHash)
	unknownAccount := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !unknownAccount {
		return LoginSession{}, err
	}
	if unknownAccount {
		passwordHash = dummyPasswordHash
	}
	passwordErr := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password))
	if unknownAccount || passwordErr != nil {
		return LoginSession{}, ErrInvalidCredentials
	}
	if !a.Verified || a.Disabled {
		return LoginSession{}, ErrForbidden
	}
	token, err := random()
	if err != nil {
		return LoginSession{}, err
	}
	id, err := random()
	if err != nil {
		return LoginSession{}, err
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	for {
		w, err := workspace(s.pool.QueryRow(ctx, "SELECT w.id,w.name,w.kind,m.role FROM identity_workspaces w JOIN identity_members m ON m.workspace_id=w.id JOIN identity_accounts a ON a.id=m.account_id WHERE m.account_id=$1 ORDER BY (w.id=a.last_workspace_id) DESC,(w.kind='personal') DESC,w.created_at LIMIT 1", a.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return LoginSession{}, ErrUnauthorized
		}
		if err != nil {
			return LoginSession{}, err
		}
		session, err := s.loginInWorkspace(ctx, a.ID, passwordHash, token, id, w.ID, expires)
		if errors.Is(err, errWorkspaceSelectionChanged) {
			// A removal, deletion, or another device's selection won the race.
			// Release the old workspace before selecting and locking the new one.
			continue
		}
		return session, err
	}
}

var errWorkspaceSelectionChanged = errors.New("workspace selection changed")

func (s *Service) loginInWorkspace(ctx context.Context, accountID, passwordHash, token, sessionID, workspaceID string, expires time.Time) (LoginSession, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LoginSession{}, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, "SELECT id FROM identity_workspaces WHERE id=$1 FOR UPDATE", workspaceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginSession{}, errWorkspaceSelectionChanged
	}
	if err != nil {
		return LoginSession{}, err
	}
	var a Account
	var currentHash string
	err = tx.QueryRow(ctx, "SELECT id,email,verified,disabled,password_hash FROM identity_accounts WHERE id=$1 FOR UPDATE", accountID).Scan(&a.ID, &a.Email, &a.Verified, &a.Disabled, &currentHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginSession{}, ErrUnauthorized
	}
	if err != nil {
		return LoginSession{}, err
	}
	if currentHash != passwordHash {
		return LoginSession{}, ErrInvalidCredentials
	}
	if !a.Verified || a.Disabled {
		return LoginSession{}, ErrForbidden
	}
	w, err := workspace(tx.QueryRow(ctx, "SELECT w.id,w.name,w.kind,m.role FROM identity_workspaces w JOIN identity_members m ON m.workspace_id=w.id JOIN identity_accounts a ON a.id=m.account_id WHERE m.account_id=$1 ORDER BY (w.id=a.last_workspace_id) DESC,(w.kind='personal') DESC,w.created_at LIMIT 1", accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginSession{}, ErrUnauthorized
	}
	if err != nil {
		return LoginSession{}, err
	}
	if w.ID != workspaceID {
		return LoginSession{}, errWorkspaceSelectionChanged
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity_sessions(id,token_hash,account_id,workspace_id,expires_at) VALUES($1,$2,$3,$4,$5)", sessionID, digest(token), accountID, workspaceID, expires); err != nil {
		return LoginSession{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return LoginSession{}, err
	}
	return LoginSession{Token: token, ExpiresAt: expires, Account: a, Workspace: w}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	var p Principal
	err := s.pool.QueryRow(ctx, `SELECT a.id,a.email,a.verified,a.disabled,w.id,w.name,w.kind,m.role,s.id
 FROM identity_sessions s JOIN identity_accounts a ON a.id=s.account_id
 JOIN identity_workspaces w ON w.id=s.workspace_id JOIN identity_members m ON m.workspace_id=w.id AND m.account_id=a.id
 WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND a.verified AND NOT a.disabled`, digest(token)).Scan(&p.Account.ID, &p.Account.Email, &p.Account.Verified, &p.Account.Disabled, &p.Workspace.ID, &p.Workspace.Name, &p.Workspace.Kind, &p.Workspace.Role, &p.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthorized
	}
	return p, err
}
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, "UPDATE identity_sessions SET revoked_at=now() WHERE token_hash=$1", digest(token))
	return err
}

func (s *Service) ListWorkspaces(ctx context.Context, accountID string) ([]Workspace, error) {
	rows, err := s.pool.Query(ctx, "SELECT w.id,w.name,w.kind,m.role FROM identity_workspaces w JOIN identity_members m ON m.workspace_id=w.id WHERE m.account_id=$1 ORDER BY w.created_at", accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err = rows.Scan(&w.ID, &w.Name, &w.Kind, &w.Role); err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	return result, rows.Err()
}
func (s *Service) SwitchWorkspace(ctx context.Context, token, workspaceID string) (Workspace, error) {
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		return Workspace{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	// Match member removal and deletion: workspace, account, then session.
	// Lock only the target workspace so two devices can switch in opposite
	// directions without each holding the other's target workspace.
	var id string
	err = tx.QueryRow(ctx, "SELECT id FROM identity_workspaces WHERE id=$1 FOR UPDATE", workspaceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrForbidden
	}
	if err != nil {
		return Workspace{}, err
	}
	var verified, disabled bool
	err = tx.QueryRow(ctx, "SELECT verified,disabled FROM identity_accounts WHERE id=$1 FOR UPDATE", p.Account.ID).Scan(&verified, &disabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!verified || disabled)) {
		return Workspace{}, ErrUnauthorized
	}
	if err != nil {
		return Workspace{}, err
	}
	err = tx.QueryRow(ctx, "SELECT id FROM identity_sessions WHERE id=$1 AND account_id=$2 AND token_hash=$3 AND revoked_at IS NULL AND expires_at>now() FOR UPDATE", p.SessionID, p.Account.ID, digest(token)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrUnauthorized
	}
	if err != nil {
		return Workspace{}, err
	}
	// Query membership after acquiring the workspace lock; a query snapshot
	// taken before waiting could still contain a member who has just left.
	w, err := workspace(tx.QueryRow(ctx, "SELECT w.id,w.name,w.kind,m.role FROM identity_workspaces w JOIN identity_members m ON m.workspace_id=w.id WHERE w.id=$1 AND m.account_id=$2", workspaceID, p.Account.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrForbidden
	}
	if err != nil {
		return Workspace{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_sessions SET workspace_id=$1 WHERE id=$2", workspaceID, p.SessionID); err != nil {
		return Workspace{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_accounts SET last_workspace_id=$1 WHERE id=$2", workspaceID, p.Account.ID); err != nil {
		return Workspace{}, err
	}
	return w, tx.Commit(ctx)
}
func (s *Service) CreateTeam(ctx context.Context, actorID, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Workspace{}, ErrConflict
	}
	if err := s.requireVerified(ctx, actorID); err != nil {
		return Workspace{}, err
	}
	id, err := random()
	if err != nil {
		return Workspace{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO identity_workspaces(id,name,kind) VALUES($1,$2,'team')", id, name); err != nil {
		return Workspace{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity_members(workspace_id,account_id,role) VALUES($1,$2,'owner')", id, actorID); err != nil {
		return Workspace{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Workspace{}, err
	}
	return Workspace{ID: id, Name: name, Kind: "team", Role: "owner"}, nil
}
func (s *Service) requireVerified(ctx context.Context, id string) error {
	var ok bool
	err := s.pool.QueryRow(ctx, "SELECT verified AND NOT disabled FROM identity_accounts WHERE id=$1", id).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) || !ok {
		return ErrForbidden
	}
	return err
}
func (s *Service) requireOwner(ctx context.Context, actorID, workspaceID string) error {
	var role string
	err := s.pool.QueryRow(ctx, "SELECT m.role FROM identity_members m JOIN identity_accounts a ON a.id=m.account_id WHERE m.account_id=$1 AND m.workspace_id=$2 AND a.verified AND NOT a.disabled", actorID, workspaceID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || role != "owner" {
		return ErrForbidden
	}
	return err
}
func (s *Service) CheckRole(ctx context.Context, actorID, workspaceID string) (string, error) {
	var role string
	err := s.pool.QueryRow(ctx, "SELECT m.role FROM identity_members m JOIN identity_accounts a ON a.id=m.account_id WHERE m.account_id=$1 AND m.workspace_id=$2 AND a.verified AND NOT a.disabled", actorID, workspaceID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	return role, err
}
func (s *Service) Health(ctx context.Context) error {
	if s.pool == nil {
		return fmt.Errorf("identity: missing postgres pool")
	}
	return s.pool.Ping(ctx)
}

func (s *Service) WorkspaceExists(ctx context.Context, workspaceID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identity_workspaces WHERE id=$1)", workspaceID).Scan(&exists)
	return exists, err
}
