// Package session owns the 会话 (Session) aggregate: a workspace on disk
// holding generated files, message history and the agent event log. Every
// session belongs to exactly one owner (a JWT user id); workspaces are
// partitioned on disk as workspace/<owner>/<id>.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	Owner     string    `json:"owner"`
	Name      string    `json:"name"`
	Category  string    `json:"category"`
	Pinned    bool      `json:"pinned"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"` // building | ready | failed
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Events    []Event   `json:"events"`
	Messages  []Message `json:"messages"`
	Publishes []Publish `json:"publishes"`
	Contest   *Contest  `json:"contest,omitempty"`
}

// Scores are Nina's three-dimension verdict on one contest version.
type Scores struct {
	Design        int `json:"design"`
	Functionality int `json:"functionality"`
	CodeQuality   int `json:"codeQuality"`
}

// ContestEntry is one 参赛者 (Contestant)'s run: generation, build and
// (once ready) Nina's score.
type ContestEntry struct {
	Provider string  `json:"provider"`
	Status   string  `json:"status"` // building | ready | failed
	Scores   *Scores `json:"scores,omitempty"`
	Comment  string  `json:"comment,omitempty"`
}

// Contest is one 竞赛 (Competition): parallel generation by several
// 生成引擎, judged by Nina; the user picks the winner, losers stay
// archived under contest/<provider>/ for review.
type Contest struct {
	Entries []ContestEntry `json:"entries"`
	Winner  string         `json:"winner,omitempty"`
	Done    bool           `json:"done"`
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

// Store persists sessions as workspace/<owner>/<id>/session.json.
type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Workspace(owner, id string) string {
	return filepath.Join(s.dir, owner, id)
}

func (s *Store) Create(owner, prompt string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !ValidOwner(owner) {
		return nil, fmt.Errorf("invalid owner")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &Session{
		ID:        id,
		Owner:     owner,
		Name:      prompt,
		Prompt:    prompt,
		Status:    "building",
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []Message{{Role: "user", Content: prompt, At: now}},
		Publishes: []Publish{},
	}
	if err := os.MkdirAll(s.Workspace(owner, id), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir workspace: %w", err)
	}
	// Public URLs (/app/{id}/) carry no owner, so the random session id is
	// the capability: a global id→owner index lets the public handler
	// locate the workspace without authentication.
	if err := os.MkdirAll(filepath.Join(s.dir, "by-id"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir id index: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "by-id", id), []byte(owner), 0o644); err != nil {
		return nil, fmt.Errorf("index session owner: %w", err)
	}
	if err := s.saveLocked(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// IndexOwner resolves a session id to its owner via the public-URL index.
// The returned owner is validated so a tampered index can never steer path
// resolution outside the workspace.
func (s *Store) IndexOwner(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid id")
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "by-id", id))
	if err != nil {
		return "", err
	}
	owner := strings.TrimSpace(string(data))
	if !ValidOwner(owner) {
		return "", fmt.Errorf("invalid owner in index")
	}
	return owner, nil
}

// ValidOwner reports whether name is safe to use as a directory segment.
func ValidOwner(name string) bool {
	if name == "" || len(name) > 128 || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '@' {
			continue
		}
		return false
	}
	return true
}

func (s *Store) Load(owner, id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(owner, id)
}

func (s *Store) loadLocked(owner, id string) (*Session, error) {
	data, err := os.ReadFile(filepath.Join(s.Workspace(owner, id), "session.json"))
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("decode session %s: %w", id, err)
	}
	return &sess, nil
}

func (s *Store) Save(owner string, sess *Session) error {
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
	return os.WriteFile(filepath.Join(s.Workspace(sess.Owner, sess.ID), "session.json"), data, 0o644)
}

// AppendEvent assigns the next sequence number, appends and persists.
func (s *Store) AppendEvent(owner, id string, ev Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(owner, id)
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
func (s *Store) AppendMessage(owner, id string, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(owner, id)
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
func (s *Store) BeginTurn(owner, id string) (prev string, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(owner, id)
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
func (s *Store) SetStatus(owner, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(owner, id)
	if err != nil {
		return err
	}
	sess.Status = status
	return s.saveLocked(sess)
}

// RecordPublish appends one publish entry, assigning the next version
// (1-based, gapless) under the store lock.
func (s *Store) RecordPublish(owner, id, url string) (Publish, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(owner, id)
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

// StartContest initializes the contest state with the given 参赛者 names.
func (s *Store) StartContest(owner, id string, providers []string) error {
	return s.update(owner, id, func(sess *Session) {
		sess.Contest = &Contest{Entries: make([]ContestEntry, 0, len(providers))}
		for _, name := range providers {
			sess.Contest.Entries = append(sess.Contest.Entries, ContestEntry{
				Provider: name, Status: "building",
			})
		}
	})
}

// UpdateContestEntry mutates contestant entries under the store lock —
// the build goroutines each own one entry, and a stale-copy save here
// would drop sibling updates. Every entry with a matching provider name
// is updated, so two same-named contestants cannot wedge each other.
func (s *Store) UpdateContestEntry(owner, id, provider string, mutate func(*ContestEntry)) error {
	return s.update(owner, id, func(sess *Session) {
		if sess.Contest == nil {
			return
		}
		for i := range sess.Contest.Entries {
			if sess.Contest.Entries[i].Provider == provider {
				mutate(&sess.Contest.Entries[i])
			}
		}
	})
}

// FinishContest marks the contest scored and done.
func (s *Store) FinishContest(owner, id string) error {
	return s.update(owner, id, func(sess *Session) {
		if sess.Contest != nil {
			sess.Contest.Done = true
		}
	})
}

// PickContestWinner records the user's choice.
func (s *Store) PickContestWinner(owner, id, provider string) error {
	return s.update(owner, id, func(sess *Session) {
		if sess.Contest != nil {
			sess.Contest.Winner = provider
		}
	})
}

// Contest claim errors returned by ClaimContestWinner so the HTTP layer
// can map them onto status codes.
var (
	ErrNoContest      = errors.New("session has no contest")
	ErrContestRunning = errors.New("contest is still running")
	ErrWinnerPicked   = errors.New("winner already picked")
	ErrNotReady       = errors.New("provider is not a ready contestant")
)

// ClaimContestWinner validates and records the user's choice atomically
// under the store lock, so two concurrent POST /winner requests cannot
// both pass the checks. The caller installs the winner's files onto the
// session root afterwards; on failure there it must ClearContestWinner.
func (s *Store) ClaimContestWinner(owner, id string, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(owner, id)
	if err != nil {
		return err
	}
	if sess.Contest == nil {
		return ErrNoContest
	}
	if !sess.Contest.Done {
		return ErrContestRunning
	}
	if sess.Contest.Winner != "" {
		return ErrWinnerPicked
	}
	ready := false
	for _, entry := range sess.Contest.Entries {
		if entry.Provider == provider && entry.Status == "ready" {
			ready = true
		}
	}
	if !ready {
		return ErrNotReady
	}
	sess.Contest.Winner = provider
	return s.saveLocked(sess)
}

// ClearContestWinner rolls back a claim when installing the winner's
// files onto the session root failed.
func (s *Store) ClearContestWinner(owner, id string) error {
	return s.update(owner, id, func(sess *Session) {
		if sess.Contest != nil {
			sess.Contest.Winner = ""
		}
	})
}

// List returns all sessions of one owner: pinned first, then newest
// update first. Reading every session.json under the single store lock is
// a deliberate tradeoff at demo scale — do not copy this pattern onto a
// high-traffic path.
func (s *Store) List(owner string) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, owner))
	if err != nil {
		if os.IsNotExist(err) {
			return []Session{}, nil
		}
		return nil, err
	}
	out := make([]Session, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sess, err := s.loadLocked(owner, entry.Name())
		if err != nil {
			continue // partially written workspace; skip
		}
		out = append(out, *sess)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// Rename sets the display name of a session.
func (s *Store) Rename(owner, id, name string) error {
	return s.update(owner, id, func(sess *Session) {
		sess.Name = name
	})
}

// SetPinned toggles the pin that floats a session to the dashboard front.
func (s *Store) SetPinned(owner, id string, pinned bool) error {
	return s.update(owner, id, func(sess *Session) {
		sess.Pinned = pinned
	})
}

func (s *Store) update(owner, id string, mutate func(*Session)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(owner, id)
	if err != nil {
		return err
	}
	mutate(sess)
	// Metadata edits (rename/pin) count as modifications for the
	// dashboard's 修改时间.
	sess.UpdatedAt = time.Now()
	return s.saveLocked(sess)
}

// Duplicate deep-copies a session under a fresh id: history and generated
// files travel along, publish records reset (the public URL is id-based).
func (s *Store) Duplicate(owner, id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.loadLocked(owner, id)
	if err != nil {
		return nil, err
	}
	newID, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	// A copy has no pipeline running: inheriting "building" would strand
	// it forever (BeginTurn refuses to start on a building session).
	// Normalize to "failed" so the first chat turn regenerates everything.
	status := src.Status
	if status == "building" {
		status = "failed"
	}
	dst := &Session{
		ID:        newID,
		Owner:     owner,
		Name:      "副本 · " + src.Name,
		Category:  src.Category,
		Prompt:    src.Prompt,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  src.Messages,
		Events:    src.Events,
		Publishes: []Publish{},
	}
	if err := os.MkdirAll(s.Workspace(owner, newID), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.dir, "by-id", newID), []byte(owner), 0o644); err != nil {
		return nil, fmt.Errorf("index session owner: %w", err)
	}
	if err := s.saveLocked(dst); err != nil {
		return nil, err
	}
	// Best-effort copy of the generated app and dist so the copy previews
	// immediately; a missed file only means the next turn rebuilds it.
	// Copying while holding the store lock blocks every writer for the
	// duration; accepted at demo scale (see List).
	for _, sub := range []string{"app", "dist"} {
		_ = CopyDirTree(filepath.Join(s.Workspace(owner, id), sub), filepath.Join(s.Workspace(owner, newID), sub))
	}
	return dst, nil
}

// Delete removes the workspace and the public-URL index entry.
func (s *Store) Delete(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid id")
	}
	if _, err := s.loadLocked(owner, id); err != nil {
		return err
	}
	// Two-phase, non-atomic by design: if the index removal fails the
	// workspace is already gone, so /app/{id}/ 404s either way.
	if err := os.RemoveAll(s.Workspace(owner, id)); err != nil {
		return err
	}
	return os.Remove(filepath.Join(s.dir, "by-id", id))
}

// CopyDirTree copies a whole directory tree; a missing source is not an
// error (e.g. a failed session has no dist yet).
func CopyDirTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func newID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
