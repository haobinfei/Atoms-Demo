// Package session owns the 会话 (Session) aggregate: a workspace on disk
// holding generated files, message history and the agent event log.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one entry of the 事件流 (Event Stream): the SSE timeline's
// single source of truth. Seq is 1-based and gapless per session.
type Event struct {
	Seq     int            `json:"seq"`
	Type    string         `json:"type"` // plan | spec | architecture | file | score | done | error | agent
	Agent   string         `json:"agent,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
	At      time.Time      `json:"at"`
}

// Session is one "idea → app" workspace (会话).
type Session struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"` // building | ready | failed
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Events    []Event   `json:"events"`
	Messages  []Message `json:"messages"`
	Publishes []Publish `json:"publishes"`
}

// Publish is one 发布 (Publish) of the session's built app: a versioned,
// publicly reachable snapshot of /app/{id}/.
type Publish struct {
	Version int       `json:"version"`
	URL     string    `json:"url"`
	At      time.Time `json:"at"`
}

// Message is one user or agent utterance inside the session timeline. The
// timeline itself renders the 事件流 (Event Stream); Messages are the
// persisted conversation record a chat-style client would show.
type Message struct {
	Role    string    `json:"role"` // user | agent
	Agent   string    `json:"agent,omitempty"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}

// Store persists sessions as workspace/<id>/session.json.
type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Workspace(id string) string {
	return filepath.Join(s.dir, id)
}

func (s *Store) Create(prompt string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &Session{
		ID:        id,
		Prompt:    prompt,
		Status:    "building",
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []Message{{Role: "user", Content: prompt, At: now}},
		Publishes: []Publish{},
	}
	if err := os.MkdirAll(s.Workspace(id), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir workspace: %w", err)
	}
	if err := s.saveLocked(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) Load(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(id)
}

func (s *Store) loadLocked(id string) (*Session, error) {
	data, err := os.ReadFile(filepath.Join(s.Workspace(id), "session.json"))
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("decode session %s: %w", id, err)
	}
	return &sess, nil
}

func (s *Store) Save(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.UpdatedAt = time.Now()
	return s.saveLocked(sess)
}

func (s *Store) saveLocked(sess *Session) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Workspace(sess.ID), "session.json"), data, 0o644)
}

// AppendEvent assigns the next sequence number, appends and persists.
func (s *Store) AppendEvent(id string, ev Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(id)
	if err != nil {
		return Event{}, err
	}
	ev.Seq = len(sess.Events) + 1
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	sess.Events = append(sess.Events, ev)
	if err := s.saveLocked(sess); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// AppendMessage persists one user utterance to the session timeline.
func (s *Store) AppendMessage(id string, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(id)
	if err != nil {
		return err
	}
	if msg.At.IsZero() {
		msg.At = time.Now()
	}
	sess.Messages = append(sess.Messages, msg)
	return s.saveLocked(sess)
}

// BeginTurn atomically flips the session into "building" so concurrent chat
// requests serialize instead of racing two pipelines: the first caller wins,
// later ones see ok=false and must be rejected. prev is the status to
// restore with SetStatus if the caller fails after winning.
func (s *Store) BeginTurn(id string) (prev string, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(id)
	if err != nil {
		return "", false, err
	}
	if sess.Status == "building" {
		return "building", false, nil
	}
	prev = sess.Status
	sess.Status = "building"
	if err := s.saveLocked(sess); err != nil {
		return "", false, err
	}
	return prev, true, nil
}

// SetStatus overwrites the session status (turn rollback, future admin ops).
func (s *Store) SetStatus(id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(id)
	if err != nil {
		return err
	}
	sess.Status = status
	return s.saveLocked(sess)
}

// RecordPublish appends one publish entry, assigning the next version
// (1-based, gapless) under the store lock.
func (s *Store) RecordPublish(id, url string) (Publish, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(id)
	if err != nil {
		return Publish{}, err
	}
	pub := Publish{Version: len(sess.Publishes) + 1, URL: url, At: time.Now()}
	sess.Publishes = append(sess.Publishes, pub)
	if err := s.saveLocked(sess); err != nil {
		return Publish{}, err
	}
	return pub, nil
}

func newID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
