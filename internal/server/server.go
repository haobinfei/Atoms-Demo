// Package server exposes the Atoms Demo HTTP API: session lifecycle and
// the SSE 事件流 (Event Stream). Web assets are served separately (web/).
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	return &Server{
		store:  store,
		cfg:    cfg,
		broker: broker,
		pipeline: &pipeline.Pipeline{
			Store:     store,
			Broker:    broker,
			Prov:      prov,
			StepDelay: 120 * time.Millisecond,
		},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleEvents)
	return mux
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	writeEvent := func(ev session.Event) {
		data, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Replay persisted history the client has not seen yet.
	for _, ev := range sess.Events {
		if ev.Seq > after {
			writeEvent(ev)
		}
	}

	// Stream live events until the client goes away.
	ch := s.broker.Subscribe(id)
	defer s.broker.Unsubscribe(id, ch)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			writeEvent(ev)
		}
	}
}
