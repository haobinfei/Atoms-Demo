// Package server exposes the Atoms Demo HTTP API: session lifecycle and
// the SSE 事件流 (Event Stream). Web assets are served separately (web/).
package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/auth"
	"github.com/haobinfei/Atoms-Demo/internal/builder"
	"github.com/haobinfei/Atoms-Demo/internal/eventstream"
	"github.com/haobinfei/Atoms-Demo/internal/jobqueue"
	"github.com/haobinfei/Atoms-Demo/internal/pay"
	"github.com/haobinfei/Atoms-Demo/internal/pipeline"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

type Config struct {
	WorkspaceDir string
	// Auth is the AuthSphere OIDC middleware. When set, every /api/* route
	// (except /api/auth/*) requires a valid JWT and sessions are isolated
	// per user; /app/{id}/ stays public via the session store's id index.
	Auth *auth.Middleware
	// DevAuth bypasses the AuthSphere gate for local development: requests
	// run as DevUser without any token check.
	DevAuth bool
	DevUser string
	// Prov overrides the 生成引擎; defaults to the ADR-0002 env chain.
	Prov provider.Provider
	// Env configures the fallback chain when Prov is nil.
	Env provider.EnvConfig
	// Store overrides session persistence; nil means FileStore under
	// WorkspaceDir. Production passes a PGStore via DATABASE_DSN.
	Store session.Store
	// Competitors are the 参赛者 for 竞赛 (Competition) mode; defaults to
	// the members of the fallback chain (gemma 离线时自动减员).
	Competitors []provider.Provider
	// ScoreFunc is Nina's scoring brain; defaults to LLM with a
	// deterministic heuristic fallback.
	ScoreFunc ScoreFunc
	// Bus fans events out to SSE clients. nil means an in-process
	// MemoryBus; production passes a FanoutBus over Redis Streams
	// (ADR-0007) so multiple replicas share the live hot path.
	Bus eventstream.Bus
	// HeartbeatInterval emits an SSE comment line when no event has been
	// written, keeping proxies from closing an idle connection. Zero uses
	// the 15s production default; tests shrink it.
	HeartbeatInterval time.Duration
	// PollInterval re-reads Postgres for events missed on the Redis hot
	// path (Redis down or a dropped stream entry). Zero uses 5s; this is
	// the graceful-degradation fallback required by #17.
	PollInterval time.Duration
	// Queue is the 队列 (Queue) that turn/contest/publish 任务 (Job) enter.
	// nil means an in-process MemoryQueue sized QueueWorkers×4; production
	// passes a RedisQueue so Workers span replicas (ADR-0007).
	Queue jobqueue.Queue
	// QueueWorkers is the Worker pool size and thus the LLM concurrency
	// gate. Zero uses the 3-worker production default.
	QueueWorkers int
}

type Server struct {
	store    session.Store
	cfg      Config
	bus      eventstream.Bus
	queue    jobqueue.Queue
	pipeline *pipeline.Pipeline
	builder  *builder.Builder
	payStore *pay.Store

	// cancel stops the Worker pool; closeOnce makes Close idempotent.
	cancel    context.CancelFunc
	closeOnce sync.Once
	// running marks sessions a Worker on this replica is executing right
	// now, so a duplicate delivery (at-least-once redelivery) never runs two
	// pipelines on one session at once. Cross-replica exclusion is the lease
	// work tracked in #21.
	running sync.Map
}

func New(cfg Config) *Server {
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = "workspace"
	}
	if cfg.DevUser == "" {
		cfg.DevUser = "dev-user"
	}
	store := cfg.Store
	if store == nil {
		store = session.NewStore(cfg.WorkspaceDir)
	}
	bus := cfg.Bus
	if bus == nil {
		bus = eventstream.NewMemoryBus()
	}
	prov := cfg.Prov
	if prov == nil {
		prov = provider.ChainFromEnv(context.Background(), cfg.Env)
	}
	workers := cfg.QueueWorkers
	if workers <= 0 {
		workers = 3
	}
	cfg.QueueWorkers = workers
	queue := cfg.Queue
	if queue == nil {
		queue = jobqueue.NewMemoryQueue(workers * 4)
	}
	cfg.Queue = queue
	build := builder.New()
	s := &Server{
		store:    store,
		cfg:      cfg,
		bus:      bus,
		queue:    queue,
		payStore: pay.New(),
		pipeline: &pipeline.Pipeline{
			Store:     store,
			Bus:       bus,
			Prov:      prov,
			Builder:   build,
			StepDelay: 120 * time.Millisecond,
		},
		builder: build,
	}
	s.startWorkers(workers)
	return s
}

// startWorkers launches the Worker pool: each goroutine is a consumer of the
// Queue (a Redis consumer-group member, or a reader of the in-process
// channel). Worker count is the LLM concurrency gate.
func (s *Server) startWorkers(n int) {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	host, _ := os.Hostname()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%d-%d", host, os.Getpid(), i)
		go func(workerID string) {
			if err := s.queue.Run(ctx, workerID, s.handleJob); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("job worker stopped", "worker", workerID, "err", err)
			}
		}(id)
	}
}

// handleJob is the Worker-side claim: the correctness backstop behind the HTTP
// thread's fast BeginTurn 409. It re-reads the session and only runs work whose
// status is still "building"; a stale duplicate (the turn already finished, or
// the status was rolled back) is acknowledged with no side effects. A per-
// session in-process guard drops a concurrent duplicate of the same session.
func (s *Server) handleJob(ctx context.Context, job jobqueue.Job) error {
	sess, err := s.store.Load(job.Owner, job.SessionID)
	if err != nil {
		// Session deleted while the Job was queued: nothing to do.
		return nil
	}
	if sess.Status != "building" {
		return nil
	}
	if _, running := s.running.LoadOrStore(job.SessionID, struct{}{}); running {
		return nil
	}
	defer s.running.Delete(job.SessionID)

	switch job.Kind {
	case jobqueue.KindTurn:
		s.pipeline.Run(ctx, job.Owner, job.SessionID)
	case jobqueue.KindContest:
		// A whole 竞赛 (contest) is one Job: contestants still fan out in
		// parallel inside, so a single Worker never serializes them.
		s.runContest(ctx, job.Owner, job.SessionID, sess.Prompt)
	case jobqueue.KindPublish:
		s.runPublish(ctx, job.Owner, job.SessionID)
	}
	return nil
}

// Close stops the Worker pool and releases the event bus (and its Redis
// client, when configured). It is safe to call more than once.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.queue != nil {
			if e := s.queue.Close(); e != nil {
				err = e
			}
		}
		if s.bus != nil {
			if e := s.bus.Close(); e != nil {
				err = e
			}
		}
	})
	return err
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.cfg.Auth != nil {
		mux.HandleFunc("GET /api/auth/login", s.cfg.Auth.Login)
		mux.HandleFunc("GET /api/auth/callback", s.cfg.Auth.Callback)
	}
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/providers", s.handleProviders)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("GET /api/sessions/list", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions/{id}/rename", s.handleRename)
	mux.HandleFunc("POST /api/sessions/{id}/pin", s.handlePin)
	mux.HandleFunc("POST /api/sessions/{id}/duplicate", s.handleDuplicate)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDelete)
	mux.HandleFunc("POST /api/sessions/{id}/chat", s.handleChat)
	mux.HandleFunc("POST /api/sessions/{id}/winner", s.handleWinner)
	mux.HandleFunc("POST /api/sessions/{id}/publish", s.handlePublish)
	mux.HandleFunc("GET /api/sessions/{id}/publish", s.handleGetPublishes)
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /api/sessions/{id}/files", s.handleListFiles)
	mux.HandleFunc("GET /api/sessions/{id}/file/{path...}", s.handleGetFile)
	mux.HandleFunc("GET /api/sessions/{id}/export", s.handleExport)
	mux.HandleFunc("POST /api/pay/orders", s.handlePayCreate)
	mux.HandleFunc("GET /api/pay/orders", s.handlePayList)
	mux.HandleFunc("POST /api/pay/orders/{id}/pay", s.handlePayPay)
	mux.HandleFunc("GET /pay/checkout/{id}", s.handlePayCheckout)
	mux.HandleFunc("GET /app/{id}/{path...}", s.handleApp)
	var handler http.Handler = mux
	if s.cfg.Auth != nil {
		handler = s.cfg.Auth.Wrap(handler)
	} else if s.cfg.DevAuth {
		handler = devUserMiddleware(handler, s.cfg.DevUser)
	}
	return handler
}

// devUserMiddleware injects the development user into every request
// context, so the handlers treat it exactly like an authenticated user.
func devUserMiddleware(next http.Handler, user string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), devUserKey{}, user)))
	})
}

// devUserKey carries the dev-bypass user; the auth package owns the
// production key, so this one is server-local.
type devUserKey struct{}

// requestUser returns the authenticated (or dev-bypass) user for the
// request; "" only for routes that never pass through the middleware.
func (s *Server) requestUser(r *http.Request) string {
	if u := auth.UserFrom(r.Context()); u != "" {
		return u
	}
	v, _ := r.Context().Value(devUserKey{}).(string)
	return v
}

// competitors resolves the 参赛者 list: explicit config wins; otherwise
// the fallback chain's members race each other (a probed-out local LLM
// simply never appears — graceful 减员).
func (s *Server) competitors() []provider.Provider {
	if len(s.cfg.Competitors) > 0 {
		return s.cfg.Competitors
	}
	if chain, ok := s.pipeline.Prov.(provider.Chain); ok {
		// Race real 生成引擎 only: the template fallback is a deterministic
		// offline guarantee, not a model — as a contestant it adds no
		// information and would pollute an arena-style comparison.
		real := make([]provider.Provider, 0, len(chain.Providers))
		for _, p := range chain.Providers {
			if p.Name() != "template" {
				real = append(real, p)
			}
		}
		if len(real) > 0 {
			return real
		}
		return chain.Providers
	}
	return []provider.Provider{s.pipeline.Prov}
}

// scoreFunc resolves Nina's scoring brain.
func (s *Server) scoreFunc() ScoreFunc {
	if s.cfg.ScoreFunc != nil {
		return s.cfg.ScoreFunc
	}
	var llm provider.Provider
	if chain, ok := s.pipeline.Prov.(provider.Chain); ok {
		for _, p := range chain.Providers {
			if p.Name() != "template" {
				llm = p
				break
			}
		}
	}
	return DefaultScoreFunc(llm)
}

// handleMe reports who the current request runs as; the web login gate
// uses it to decide whether to redirect to AuthSphere.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	if user == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": user})
}

// workspaceDir validates the id is a plain workspace folder name (no
// traversal) and returns the session's workspace directory.
func (s *Server) workspaceDir(user, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid id")
	}
	if !session.ValidOwner(user) {
		return "", fmt.Errorf("invalid user")
	}
	return s.store.Workspace(user, id), nil
}

// appDir returns the session's generated-app directory.
func (s *Server) appDir(user, id string) (string, error) {
	ws, err := s.workspaceDir(user, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(ws, "app"), nil
}

// safeJoin resolves a user-supplied relative path inside root, refusing
// anything that escapes it.
func safeJoin(root, rel string) (string, error) {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ".." {
			return "", fmt.Errorf("path traversal refused")
		}
	}
	clean := filepath.Clean("/" + filepath.FromSlash(rel))
	full := filepath.Join(root, clean)
	if !strings.HasPrefix(full, filepath.Clean(root)+string(os.PathSeparator)) && full != filepath.Clean(root) {
		return "", fmt.Errorf("path escapes root")
	}
	return full, nil
}

// handleApp serves the built dist of a session: the live preview URL and
// (later) the published URL. Publicly accessible, no auth.
// validProviderSegment reports whether name is safe as a contest
// archive directory segment. Provider names come from Provider.Name()
// (e.g. "deepseek", "gemma", "template"); the rules mirror
// session.ValidOwner but live here so the two domains stay decoupled.
func validProviderSegment(name string) bool {
	if name == "" || len(name) > 64 || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// /app is shared publicly: no user on the context, so the owner is
	// resolved through the store's id index (the random id is the capability).
	owner, err := s.store.IndexOwner(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ws, err := s.workspaceDir(owner, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	dist := filepath.Join(ws, "dist")
	// Loser versions of a contest live under contest/<provider>/dist and
	// stay publicly reviewable at /app/{id}/contest/{provider}/.
	rel := r.PathValue("path")
	if rest, found := strings.CutPrefix(rel, "contest/"); found {
		prov, sub, ok := strings.Cut(rest, "/")
		if !ok || !validProviderSegment(prov) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		dist = filepath.Join(ws, "contest", prov, "dist")
		rel = sub
	}
	full := filepath.Join(dist, rel)
	if rel == "" {
		full = filepath.Join(dist, "index.html")
	}
	if !strings.HasPrefix(full, filepath.Clean(dist)+string(os.PathSeparator)) {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	if _, err := os.Stat(full); err != nil {
		writeError(w, http.StatusNotFound, "not built yet")
		return
	}
	// Dist filenames are not content-hashed, and the preview iframe reloads
	// the same URLs after every build. Without an explicit policy browsers
	// fall back to heuristic caching and keep serving the pre-rebuild
	// bundle, so the preview shows a stale app. no-cache forces a
	// revalidation round-trip: unchanged files still get a cheap 304.
	w.Header().Set("Cache-Control", "no-cache")
	if rel == "" {
		s.servePreviewIndex(w, r, full)
		return
	}
	http.ServeFile(w, r, full)
}

// servePreviewIndex serves a built app's index.html with a per-build
// storage namespace injected. Generated apps routinely persist state in
// localStorage with "stored value wins over defaults" logic; without
// isolation an early (empty or outdated) write shadows the richer
// defaults of every later build, and the preview looks "stale" no matter
// how often it rebuilds. Keying the namespace to the build's mtime gives
// each rebuild a fresh storage view (new defaults show) while edits made
// inside one build still survive preview reloads.
func (s *Server) servePreviewIndex(w http.ResponseWriter, r *http.Request, indexPath string) {
	body, err := os.ReadFile(indexPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "not built yet")
		return
	}
	info, err := os.Stat(indexPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "not built yet")
		return
	}
	// Digits only — this string lands verbatim in the injected script.
	ns := fmt.Sprintf("b%d:", info.ModTime().Unix())
	injection := []byte(`<script data-atoms-storage-ns>
(() => {
  const NS = "` + ns + `";
  const wrap = (store) => {
    if (!store || store.__atomsNS) return;
    const get = store.getItem.bind(store);
    const set = store.setItem.bind(store);
    const del = store.removeItem.bind(store);
    store.getItem = (k) => get(NS + k);
    store.setItem = (k, v) => set(NS + k, String(v));
    store.removeItem = (k) => del(NS + k);
    Object.defineProperty(store, "__atomsNS", { value: NS });
  };
  try { wrap(window.localStorage); wrap(window.sessionStorage); } catch (e) { /* un-isolated fallback */ }
})();
</script>`)
	// Builder-generated HTML always carries <head>; fall back to prepending
	// so a hand-written oddity still gets the isolation.
	out := body
	if i := strings.Index(strings.ToLower(string(body)), "<head>"); i >= 0 {
		j := i + len("<head>")
		out = append(append(append([]byte{}, body[:j]...), injection...), body[j:]...)
	} else {
		out = append(append([]byte{}, injection...), body...)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(out)
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	appDir, err := s.appDir(s.requestUser(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	type entry struct {
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	out := make([]entry, 0)
	err = filepath.WalkDir(appDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(appDir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, entry{Path: filepath.ToSlash(rel), Size: int(info.Size())})
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "no files")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	appDir, err := s.appDir(s.requestUser(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	full, err := safeJoin(appDir, r.PathValue("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"path":    filepath.ToSlash(r.PathValue("path")),
		"content": string(data),
	})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	appDir, err := s.appDir(s.requestUser(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(appDir); err != nil {
		writeError(w, http.StatusNotFound, "no files yet")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"app.zip\"")
	zw := zip.NewWriter(w)
	err = filepath.WalkDir(appDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(appDir, path)
		if err != nil {
			return err
		}
		f, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, src); err != nil {
			_ = src.Close()
			return err
		}
		return src.Close()
	})
	if err != nil {
		return // headers already written; nothing graceful left
	}
	_ = zw.Close()
}

// handleProviders exposes the 兜底链 members so the UI (and operators) can
// see which 生成引擎 are active — e.g. whether local gemma was probed out.
func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": provider.Members(s.pipeline.Prov)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt      string `json:"prompt"`
		Competition bool   `json:"competition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	user := s.requestUser(r)
	sess, err := s.store.Create(user, strings.TrimSpace(req.Prompt))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The dashboard card shows the 品类 (category) the template engine
	// recognizes; LLM-only providers fall back to the generic bucket.
	loaded, err := s.store.Load(user, sess.ID)
	if err == nil {
		loaded.Category = provider.Analyze(loaded.Prompt).Category
		if err := s.store.Save(user, loaded); err == nil {
			sess = loaded
		}
	}
	kind := jobqueue.KindTurn
	if req.Competition {
		kind = jobqueue.KindContest
	}
	if err := s.enqueueJob(user, sess.ID, kind); err != nil {
		// The Queue is full or Redis is unreachable: surface 503 and mark the
		// session failed rather than silently running a bare goroutine or
		// dropping the turn (ADR-0007).
		slog.Error("enqueue create job", "session", sess.ID, "kind", kind, "err", err)
		_ = s.store.SetStatus(user, sess.ID, "failed")
		writeError(w, http.StatusServiceUnavailable, "generation queue unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": sess.ID})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Load(s.requestUser(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// enqueueJob puts a 任务 (Job) on the Queue. The work then runs on a Worker,
// not a bare goroutine (ADR-0007); events land in the session's event log and
// are fanned out to SSE clients. A full or unavailable Queue is a hard error
// the HTTP layer surfaces as 503 — the Job is never silently dropped.
func (s *Server) enqueueJob(user, id string, kind jobqueue.Kind) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.queue.Enqueue(ctx, jobqueue.Job{
		Owner:     user,
		SessionID: id,
		Kind:      kind,
	})
}

// handleListSessions returns the current user's sessions for the dashboard:
// name, category, status, pin — optionally filtered by a ?q= search over
// name and prompt.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	sessions, err := s.store.List(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type summary struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Category  string    `json:"category"`
		Status    string    `json:"status"`
		Pinned    bool      `json:"pinned"`
		Prompt    string    `json:"prompt"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	out := make([]summary, 0, len(sessions))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	for _, sess := range sessions {
		if q != "" && !strings.Contains(sess.Name, q) && !strings.Contains(sess.Prompt, q) {
			continue
		}
		out = append(out, summary{
			ID: sess.ID, Name: sess.Name, Category: sess.Category,
			Status: sess.Status, Pinned: sess.Pinned, Prompt: sess.Prompt,
			UpdatedAt: sess.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleRename sets the dashboard display name.
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.store.Rename(s.requestUser(r), r.PathValue("id"), strings.TrimSpace(req.Name)); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePin toggles the dashboard pin.
func (s *Server) handlePin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pinned bool `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "pinned is required")
		return
	}
	if err := s.store.SetPinned(s.requestUser(r), r.PathValue("id"), req.Pinned); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDuplicate deep-copies a session into a fresh one.
func (s *Server) handleDuplicate(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Duplicate(s.requestUser(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": sess.ID})
}

// handleDelete removes a session and its public-URL index entry.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(s.requestUser(r), r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleChat starts a new 回合 (Turn): the follow-up message lands on the
// timeline, then the 智能体 team regenerates the whole app against the
// augmented prompt (original idea + full history).
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	user := s.requestUser(r)
	if _, err := s.store.Load(user, id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	// BeginTurn serializes concurrent chat requests atomically; without it
	// two racing POSTs could both pass a plain status check and interleave
	// two pipelines on one session.
	prev, ok, err := s.store.BeginTurn(user, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusConflict, "session is building, wait for the current turn")
		return
	}
	msg := session.Message{Role: "user", Content: strings.TrimSpace(req.Message), At: time.Now()}
	if err := s.store.AppendMessage(user, id, msg); err != nil {
		// The session is "building" but no pipeline owns it; roll back so
		// the next request is not rejected forever.
		_ = s.store.SetStatus(user, id, prev)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pipeline.EmitUserMessage(user, id, msg)
	if err := s.enqueueJob(user, id, jobqueue.KindTurn); err != nil {
		// Queue full / Redis down: roll the session back so the next request is
		// not rejected forever, and surface 503. The user message stays on the
		// timeline (it was persisted before the enqueue); a retry appends the
		// follow-up again, the documented at-least-once tradeoff.
		slog.Error("enqueue chat turn", "session", id, "err", err)
		_ = s.store.SetStatus(user, id, prev)
		writeError(w, http.StatusServiceUnavailable, "generation queue unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "building"})
}

// handlePublish claims the session and puts a 发布 (Publish) Job on the Queue,
// returning immediately. The rebuild runs on a Worker (runPublish); the client
// polls the publish history for the new version. The synchronous pre-checks
// keep 404/409/400 behavior identical to the old inline handler.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := s.requestUser(r)
	if _, err := s.store.Load(user, id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	// Claim the session atomically: a chat turn starting mid-publish would
	// otherwise race the rebuild for the same distDir. The session shows
	// "building" for the duration, which also 409s chat requests.
	prev, ok, err := s.store.BeginTurn(user, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusConflict, "session is building, wait for the current turn")
		return
	}
	restore := func() { _ = s.store.SetStatus(user, id, prev) }
	// Pre-validate synchronously so "nothing to publish" stays a 400 rather
	// than an async error event the caller cannot correlate.
	appDir, err := s.appDir(user, id)
	if err != nil {
		restore()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(appDir); err != nil {
		restore()
		writeError(w, http.StatusBadRequest, "no app to publish yet")
		return
	}
	if err := s.enqueueJob(user, id, jobqueue.KindPublish); err != nil {
		// Queue full / Redis down: roll back and surface 503, never a silent
		// drop of the publish.
		slog.Error("enqueue publish", "session", id, "err", err)
		restore()
		writeError(w, http.StatusServiceUnavailable, "generation queue unavailable")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "publishing"})
}

// runPublish is the Worker-side 发布 (Publish) execution: rebuild dist into a
// side directory and swap, so a failed rebuild never takes down the version
// /app is currently serving. Any failure keeps the old dist, restores the
// session to "ready", and lands an error on the timeline; success records a
// version and emits a publish event.
func (s *Server) runPublish(_ context.Context, user, id string) {
	fail := func(msg string) {
		slog.Error("publish failed", "session", id, "detail", msg)
		_ = s.store.SetStatus(user, id, "ready")
		s.pipeline.EmitError(user, id, "发布失败："+msg)
	}
	appDir, err := s.appDir(user, id)
	if err != nil {
		fail(err.Error())
		return
	}
	ws, err := s.workspaceDir(user, id)
	if err != nil {
		fail(err.Error())
		return
	}
	distDir := filepath.Join(ws, "dist")
	// Build into a side directory and swap, so a failed rebuild never
	// takes down the version /app is currently serving. The live-dist
	// gap is two renames, not a full bundle write.
	tmpDir := distDir + ".new"
	bakDir := distDir + ".bak"
	_ = os.RemoveAll(tmpDir)
	_ = os.RemoveAll(bakDir)
	if err := s.builder.Build(appDir, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		fail("rebuild failed: " + err.Error())
		return
	}
	swapped := false
	if _, err := os.Stat(distDir); err == nil {
		if err := os.Rename(distDir, bakDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			fail("stash previous dist: " + err.Error())
			return
		}
		swapped = true
	}
	if err := os.Rename(tmpDir, distDir); err != nil {
		if swapped {
			_ = os.Rename(bakDir, distDir)
		}
		_ = os.RemoveAll(tmpDir)
		fail("activate new dist: " + err.Error())
		return
	}
	pub, err := s.store.RecordPublish(user, id, "/app/"+id+"/")
	if err != nil {
		// The new dist has no matching record; never let /app serve an
		// unrecorded version — put the last recorded one back.
		_ = os.RemoveAll(distDir)
		if swapped {
			if err := os.Rename(bakDir, distDir); err != nil {
				slog.Error("restore previous dist after record failure", "session", id, "err", err)
			}
		}
		fail(err.Error())
		return
	}
	_ = os.RemoveAll(bakDir)
	_ = s.store.SetStatus(user, id, "ready")
	s.pipeline.EmitPublishDone(user, id, pub.Version, pub.URL)
}

// handleGetPublishes returns the full publish history, oldest first.
func (s *Server) handleGetPublishes(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Load(s.requestUser(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"publishes": sess.Publishes})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := s.requestUser(r)
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid after")
			return
		}
		after = n
	}
	// Existence (and ownership) check before hijacking the response into a
	// stream: a missing session must be a 404, not an open SSE connection.
	if _, err := s.store.Load(user, id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	ctx := r.Context()
	// Subscribe before replaying: events published between the replay read
	// and the subscription would otherwise fall into a gap. Seq dedup below
	// collapses the subscribe/replay overlap.
	sub, err := s.bus.Subscribe(ctx, id, after)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "event stream unavailable")
		return
	}
	defer sub.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Send the headers right away: an event-free stream (client already
	// caught up) must not leave the response unsent until the first
	// heartbeat. This is what turns the handler into a real SSE stream.
	flusher.Flush()

	maxSeq := after
	writeEvent := func(ev session.Event) {
		// Dedup + order guard: replayed and live events may overlap, and a
		// stale Redis entry must never move the client's cursor backwards.
		if ev.Seq <= maxSeq {
			return
		}
		maxSeq = ev.Seq
		data, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Postgres is the source of truth: replay whatever the client missed
	// (initial history, a dropped Redis entry, or a whole Redis outage).
	replay := func() {
		events, err := s.store.EventsAfter(user, id, maxSeq)
		if err != nil {
			slog.Warn("replay events", "session", id, "after", maxSeq, "err", err)
			return
		}
		for _, ev := range events {
			writeEvent(ev)
		}
	}
	replay()

	heartbeatEvery := s.cfg.HeartbeatInterval
	if heartbeatEvery <= 0 {
		heartbeatEvery = 15 * time.Second
	}
	pollEvery := s.cfg.PollInterval
	if pollEvery <= 0 {
		pollEvery = 5 * time.Second
	}
	heartbeat := time.NewTicker(heartbeatEvery)
	defer heartbeat.Stop()
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			// A seq jump means the live path dropped events (slow consumer
			// or a trimmed Redis Stream): top the hole up from Postgres
			// before writing the live event that exposed it.
			if ev.Seq > maxSeq+1 {
				replay()
			}
			writeEvent(ev)
		case <-poll.C:
			// Fallback so a client keeps making progress even when the
			// Redis hot path is down and no live event will ever arrive.
			replay()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
