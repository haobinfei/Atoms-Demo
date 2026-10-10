// Package server exposes the Atoms Demo HTTP API: session lifecycle and
// the SSE 事件流 (Event Stream). Web assets are served separately (web/).
package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/builder"
	"github.com/haobinfei/Atoms-Demo/internal/pipeline"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

type Config struct {
	WorkspaceDir string
	// DevAuth bypasses the AuthSphere gate for local development
	// (replaced by the real middleware in the auth slice).
	DevAuth bool
	// Prov overrides the 生成引擎; defaults to the ADR-0002 env chain.
	Prov provider.Provider
	// Env configures the fallback chain when Prov is nil.
	Env provider.EnvConfig
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
	mux.HandleFunc("GET /api/providers", s.handleProviders)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("POST /api/sessions/{id}/chat", s.handleChat)
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /api/sessions/{id}/files", s.handleListFiles)
	mux.HandleFunc("GET /api/sessions/{id}/file/{path...}", s.handleGetFile)
	mux.HandleFunc("GET /api/sessions/{id}/export", s.handleExport)
	mux.HandleFunc("GET /app/{id}/{path...}", s.handleApp)
	return mux
}

// workspaceDir validates the id is a plain workspace folder name (no
// traversal) and returns the session's workspace directory.
func (s *Server) workspaceDir(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid id")
	}
	return s.store.Workspace(id), nil
}

// appDir returns the session's generated-app directory.
func (s *Server) appDir(id string) (string, error) {
	ws, err := s.workspaceDir(id)
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
	ws, err := s.workspaceDir(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dist := filepath.Join(ws, "dist")
	rel := r.PathValue("path")
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
	appDir, err := s.appDir(r.PathValue("id"))
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
	appDir, err := s.appDir(r.PathValue("id"))
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
	appDir, err := s.appDir(r.PathValue("id"))
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
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	sess, err := s.store.Create(strings.TrimSpace(req.Prompt))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The pipeline (FakeProvider for this slice) runs asynchronously and
	// streams events into the session's event log; see handleEvents.
	s.runPipeline(sess.ID)
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": sess.ID})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Load(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// runPipeline executes the agent pipeline for the session asynchronously;
// events land in the session's event log and are fanned out to SSE clients.
func (s *Server) runPipeline(id string) {
	go s.pipeline.Run(context.Background(), id)
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
	if _, err := s.store.Load(id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	// BeginTurn serializes concurrent chat requests atomically; without it
	// two racing POSTs could both pass a plain status check and interleave
	// two pipelines on one session.
	prev, ok, err := s.store.BeginTurn(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusConflict, "session is building, wait for the current turn")
		return
	}
	msg := session.Message{Role: "user", Content: strings.TrimSpace(req.Message), At: time.Now()}
	if err := s.store.AppendMessage(id, msg); err != nil {
		// The session is "building" but no pipeline owns it; roll back so
		// the next request is not rejected forever.
		_ = s.store.SetStatus(id, prev)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pipeline.EmitUserMessage(id, msg)
	s.runPipeline(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "building"})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid after")
			return
		}
		after = n
	}
	sess, err := s.store.Load(id)
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
