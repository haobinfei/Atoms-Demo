// Package changes records what each 回合 (Turn) and each 手改 (hand edit)
// changed in a session's generated app: which file, who changed it, and the
// line diff. The 事件流 (Event Stream) already says a file changed — this
// package is the "what exactly changed" half, so the workspace can list a
// turn's files with a per-file diff (ticket #54).
//
// Records are appended to one JSONL file per session, under the same
// workspace/<owner>/<id>/ layout the app and dist directories use, so both
// the FileStore and the PGStore deployments get them for free and a deleted
// session takes its records with it.
package changes

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// 改动来源 (change authors). The ticket asks the page to tell an Agent's
// rewrite apart from a 用户手改, and these two are the whole distinction.
const (
	AuthorAgent = "agent"
	AuthorUser  = "user"
)

// 改动类型 (change kinds).
const (
	KindAdded     = "added"
	KindModified  = "modified"
	KindDeleted   = "deleted"
	KindUnchanged = "unchanged"
)

// Record is one 改动记录 (Change Record): a single file write, keyed to the
// seq of the 文件 (file) event that announced it. Seq is the join key with
// the 事件流, so the timeline can attach a diff to the exact row it shows
// even when the same path changes several times over a session's life.
type Record struct {
	Seq      int       `json:"seq"`
	Path     string    `json:"path"`
	Author   string    `json:"author"`
	Kind     string    `json:"kind"`
	Added    int       `json:"added"`
	Removed  int       `json:"removed"`
	Binary   bool      `json:"binary,omitempty"`
	TooLarge bool      `json:"tooLarge,omitempty"`
	At       time.Time `json:"at"`
	// Diff is the unified diff. Only Detail returns it: the list carries
	// summaries, because a turn's diffs are far too much to ship to every
	// timeline render (ticket #54).
	Diff string `json:"diff,omitempty"`
}

// Summary is a Record without its diff body — what the timeline lists.
type Summary struct {
	Seq      int       `json:"seq"`
	Path     string    `json:"path"`
	Author   string    `json:"author"`
	Kind     string    `json:"kind"`
	Added    int       `json:"added"`
	Removed  int       `json:"removed"`
	Binary   bool      `json:"binary,omitempty"`
	TooLarge bool      `json:"tooLarge,omitempty"`
	At       time.Time `json:"at"`
}

// Summary strips the diff body for the list view.
func (r Record) Summary() Summary {
	return Summary{
		Seq: r.Seq, Path: r.Path, Author: r.Author, Kind: r.Kind,
		Added: r.Added, Removed: r.Removed, Binary: r.Binary, TooLarge: r.TooLarge, At: r.At,
	}
}

// NewRecord derives the 改动记录 for one write from the file's previous and
// new content. seq is the 文件 (file) event that announced the write, so the
// timeline can attach the diff to the row it shows.
//
// A pair that is not text, or is too large to diff, degrades to a bare
// "changed" record: the page still lists the file, it just has no diff to
// show (ticket #54).
func NewRecord(seq int, path, author, old, new string) Record {
	rec := Record{Seq: seq, Path: path, Author: author, At: time.Now().UTC()}
	switch {
	case old == new:
		rec.Kind = KindUnchanged
		return rec
	case old == "":
		rec.Kind = KindAdded
	case new == "":
		rec.Kind = KindDeleted
	default:
		rec.Kind = KindModified
	}
	if !Textual(old) || !Textual(new) {
		rec.Binary = true
		return rec
	}
	if len(old) > maxDiffBytes || len(new) > maxDiffBytes {
		rec.TooLarge = true
		return rec
	}
	diff := Unified(path, old, new)
	rec.Added, rec.Removed = countChanges(diff)
	rec.Diff = diff
	return rec
}

// Store appends and reads 改动记录. It is safe for concurrent use: appends
// are single O_APPEND writes of one line, and readers only ever see whole
// lines.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at the workspace directory.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir exposes the workspace root; tests use it to reach a session's file.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(owner, id string) (string, error) {
	if !session.ValidOwner(owner) {
		return "", fmt.Errorf("changes: invalid owner %q", owner)
	}
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("changes: invalid session id %q", id)
	}
	return filepath.Join(s.dir, owner, id, "changes.jsonl"), nil
}

// Append files one record. Appending is best effort by design: the 事件流
// stays the source of truth for "a file changed", so a failed record only
// costs the diff, never the turn.
func (s *Store) Append(owner, id string, rec Record) error {
	p, err := s.path(owner, id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(raw)
	return err
}

// List returns every record of a session in write order. A session with no
// records yet is an empty list rather than an error: the first 回合 may
// still be running when the timeline asks.
func (s *Store) List(owner, id string) ([]Record, error) {
	p, err := s.path(owner, id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// A torn line (a crash mid-append) must not cost the whole
			// list: the page keeps every other file's diff.
			slog.Warn("skip unreadable change record", "session", id, "err", err)
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// Detail returns one record with its diff body. ok=false means no record
// carries that seq — the file event exists but its record was never written
// (or the session predates change records).
func (s *Store) Detail(owner, id string, seq int) (Record, bool, error) {
	recs, err := s.List(owner, id)
	if err != nil {
		return Record{}, false, err
	}
	// Newest wins: a retried 回合 can announce the same path under a new
	// seq, and the timeline links the seq it last saw.
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Seq == seq {
			return recs[i], true, nil
		}
	}
	return Record{}, false, nil
}
