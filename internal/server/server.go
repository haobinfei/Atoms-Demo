// Package server exposes the Atoms Demo HTTP API: session lifecycle and
// the SSE 事件流 (Event Stream). Web assets are served separately (web/).
package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/auth"
	"github.com/haobinfei/Atoms-Demo/internal/builder"
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
	// Competitors are the 参赛者 for 竞赛 (Competition) mode; defaults to
	// the members of the fallback chain (gemma 离线时自动减员).
	Competitors []provider.Provider
	// ScoreFunc is Nina's scoring brain; defaults to LLM with a
	// deterministic heuristic fallback.
	ScoreFunc ScoreFunc
}

type Server struct {
	store    *session.Store
	cfg      Config
	broker   *pipeline.Broker
	pipeline *pipeline.Pipeline
	builder  *builder.Builder
}

func New(cfg Config) *Server {
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = "workspace"
	}
	if cfg.DevUser == "" {
		cfg.DevUser = "dev-user"
	}
	store := session.NewStore(cfg.WorkspaceDir)
	broker := pipeline.NewBroker()
	prov := cfg.Prov
	if prov == nil {
		prov = provider.ChainFromEnv(context.Background(), cfg.Env)
	}
	build := builder.New()
	return &Server{
		store:  store,
		cfg:    cfg,
		broker: broker,
		pipeline: &pipeline.Pipeline{
			Store:     store,
			Broker:    broker,
			Prov:      prov,
			Builder:   build,
			StepDelay: 120 * time.Millisecond,
		},
		builder: build,
	}
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
		provider, sub, ok := strings.Cut(rest, "/")
		if !ok || !session.ValidOwner(provider) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		dist = filepath.Join(ws, "contest", provider, "dist")
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
	http.ServeFile(w, r, full)
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
	if req.Competition {
		go s.runContest(user, sess.ID, sess.Prompt)
	} else {
		// The pipeline runs asynchronously and streams events into the
		// session's event log; see handleEvents.
		s.runPipeline(user, sess.ID)
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

// runPipeline executes the agent pipeline for the session asynchronously;
// events land in the session's event log and are fanned out to SSE clients.
func (s *Server) runPipeline(user, id string) {
	go s.pipeline.Run(context.Background(), user, id)
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
	s.runPipeline(user, id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "building"})
}

// handlePublish rebuilds the dist and records one 发布 (Publish): the
// public, credential-free URL /app/{id}/ now serves this version.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := s.requestUser(r)
	if _, err := s.store.Load(user, id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	// Claim the session atomically: a chat turn starting mid-publish would
	// otherwise race this rebuild for the same distDir. The session shows
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
	ws, err := s.workspaceDir(user, id)
	if err != nil {
		restore()
		writeError(w, http.StatusBadRequest, err.Error())
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
		restore()
		writeError(w, http.StatusInternalServerError, "rebuild failed: "+err.Error())
		return
	}
	swapped := false
	if _, err := os.Stat(distDir); err == nil {
		if err := os.Rename(distDir, bakDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			restore()
			writeError(w, http.StatusInternalServerError, "stash previous dist: "+err.Error())
			return
		}
		swapped = true
	}
	if err := os.Rename(tmpDir, distDir); err != nil {
		if swapped {
			_ = os.Rename(bakDir, distDir)
		}
		_ = os.RemoveAll(tmpDir)
		restore()
		writeError(w, http.StatusInternalServerError, "activate new dist: "+err.Error())
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
		restore()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = os.RemoveAll(bakDir)
	_ = s.store.SetStatus(user, id, "ready")
	writeJSON(w, http.StatusOK, pub)
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
	sess, err := s.store.Load(user, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// Subscribe before replaying: events published between the load and the
	// subscription would otherwise fall into a gap (neither replayed nor
	// streamed). Seq dedup below collapses any overlap.
	ch := s.broker.Subscribe(id)
	defer s.broker.Unsubscribe(id, ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	maxSeq := after
	writeEvent := func(ev session.Event) {
		if ev.Seq <= maxSeq {
			return // already replayed (subscribe-before-load overlap)
		}
		maxSeq = ev.Seq
		data, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Replay persisted history the client has not seen yet.
	for _, ev := range sess.Events {
		writeEvent(ev)
	}

	// Stream live events until the client goes away.
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			writeEvent(ev)
		}
	}
}
