// PGStore is the Postgres implementation of Store: session metadata, the
// append-only event stream, messages, publishes and contest state live in
// the atoms database (see gitops db/migrations). Generated app/dist files
// stay on disk under the same workspace/<owner>/<id>/ layout — only
// session.json and the by-id index move into the database.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements Store on Postgres; dir holds the on-disk app/dist
// files exactly like FileStore.
type PGStore struct {
	dir  string
	pool *pgxpool.Pool
}

var _ Store = (*PGStore)(nil)

// NewPGStore opens a pool against dsn (postgres:// URL from the
// atoms-middleware Secret) and verifies connectivity.
func NewPGStore(dir, dsn string) (*PGStore, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("pg pool: %w", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg ping: %w", err)
	}
	return &PGStore{dir: dir, pool: pool}, nil
}

// Close releases the pool; mostly for tests.
func (p *PGStore) Close() { p.pool.Close() }

func (p *PGStore) Dir() string { return p.dir }

func (p *PGStore) Workspace(owner, id string) string {
	return filepath.Join(p.dir, owner, id)
}

func (p *PGStore) Create(owner, prompt string) (*Session, error) {
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
		Events:    []Event{},
		Publishes: []Publish{},
	}
	_, err = p.pool.Exec(context.Background(),
		`INSERT INTO sessions (id, owner, name, prompt, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $3, 'building', $4, $4)`, id, owner, prompt, now)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	if err := p.AppendMessage(owner, id, sess.Messages[0]); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.Workspace(owner, id), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir workspace: %w", err)
	}
	return sess, nil
}

func (p *PGStore) IndexOwner(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid id")
	}
	var owner string
	err := p.pool.QueryRow(context.Background(),
		`SELECT owner FROM sessions WHERE id = $1`, id).Scan(&owner)
	if err != nil {
		return "", err
	}
	if !ValidOwner(owner) {
		return "", fmt.Errorf("invalid owner in index")
	}
	return owner, nil
}

const sessionSelect = `SELECT id, owner, name, prompt, category, pinned, status,
	publishes, contest, created_at, updated_at FROM sessions WHERE owner = $1 AND id = $2`

func (p *PGStore) Load(owner, id string) (*Session, error) {
	sess, err := p.scanSession(p.pool.QueryRow(context.Background(), sessionSelect, owner, id))
	if err != nil {
		return nil, err
	}
	if err := p.loadStream(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (p *PGStore) scanSession(row pgx.Row) (*Session, error) {
	var sess Session
	var publishes, contest []byte
	err := row.Scan(&sess.ID, &sess.Owner, &sess.Name, &sess.Prompt, &sess.Category,
		&sess.Pinned, &sess.Status, &publishes, &contest, &sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return nil, err
	}
	sess.Publishes = []Publish{}
	if len(publishes) > 0 {
		if err := json.Unmarshal(publishes, &sess.Publishes); err != nil {
			return nil, fmt.Errorf("decode publishes: %w", err)
		}
	}
	if len(contest) > 0 {
		sess.Contest = &Contest{}
		if err := json.Unmarshal(contest, sess.Contest); err != nil {
			return nil, fmt.Errorf("decode contest: %w", err)
		}
	}
	return &sess, nil
}

// loadStream fills the append-only slices, normalizing empty results to
// empty arrays (never JSON null — the workspace reads len(events) before
// the first SSE event arrives).
func (p *PGStore) loadStream(sess *Session) error {
	rows, err := p.pool.Query(context.Background(),
		`SELECT seq, type, agent, payload, at FROM events WHERE session_id = $1 ORDER BY seq`, sess.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	sess.Events = []Event{}
	for rows.Next() {
		var ev Event
		var payload []byte
		if err := rows.Scan(&ev.Seq, &ev.Type, &ev.Agent, &payload, &ev.At); err != nil {
			return err
		}
		if len(payload) > 0 {
			ev.Payload = map[string]any{}
			if err := json.Unmarshal(payload, &ev.Payload); err != nil {
				return fmt.Errorf("decode event payload: %w", err)
			}
		}
		sess.Events = append(sess.Events, ev)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	mrows, err := p.pool.Query(context.Background(),
		`SELECT role, agent, content, at FROM messages WHERE session_id = $1 ORDER BY id`, sess.ID)
	if err != nil {
		return err
	}
	defer mrows.Close()
	sess.Messages = []Message{}
	for mrows.Next() {
		var msg Message
		if err := mrows.Scan(&msg.Role, &msg.Agent, &msg.Content, &msg.At); err != nil {
			return err
		}
		sess.Messages = append(sess.Messages, msg)
	}
	return mrows.Err()
}

// Save persists the scalar aggregate fields plus the JSONB sub-structures
// (publishes, contest). Events and messages are append-only and never
// rewritten here — a full-rewrite Save would clobber concurrent appends.
func (p *PGStore) Save(owner string, sess *Session) error {
	publishes, err := json.Marshal(sess.Publishes)
	if err != nil {
		return err
	}
	var contest []byte
	if sess.Contest != nil {
		if contest, err = json.Marshal(sess.Contest); err != nil {
			return err
		}
	}
	tag, err := p.pool.Exec(context.Background(),
		`UPDATE sessions SET name=$3, prompt=$4, category=$5, pinned=$6, status=$7,
			publishes=$8, contest=$9, updated_at=$10
		 WHERE owner=$1 AND id=$2`,
		owner, sess.ID, sess.Name, sess.Prompt, sess.Category, sess.Pinned,
		sess.Status, publishes, contest, time.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session %s not found", sess.ID)
	}
	return nil
}

// AppendEvent assigns the next gapless seq with a subquery and retries on
// the rare seq race (contest entrants append concurrently): the unique
// (session_id, seq) key turns a stale MAX() into a retryable error.
func (p *PGStore) AppendEvent(owner, id string, ev Event) (Event, error) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return Event{}, err
	}
	const insert = `INSERT INTO events (session_id, seq, type, agent, payload, at)
		SELECT $1, COALESCE((SELECT MAX(seq) FROM events WHERE session_id = $1), 0) + 1, $2, $3, $4, $5
		RETURNING seq`
	for range 5 {
		err = p.pool.QueryRow(context.Background(), insert,
			id, ev.Type, ev.Agent, payload, ev.At).Scan(&ev.Seq)
		if err == nil {
			return ev, nil
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			return Event{}, err
		}
	}
	return Event{}, fmt.Errorf("append event: seq contention after retries")
}

func (p *PGStore) AppendMessage(owner, id string, msg Message) error {
	if msg.At.IsZero() {
		msg.At = time.Now()
	}
	_, err := p.pool.Exec(context.Background(),
		`INSERT INTO messages (session_id, role, agent, content, at) VALUES ($1, $2, $3, $4, $5)`,
		id, msg.Role, msg.Agent, msg.Content, msg.At)
	return err
}

func (p *PGStore) BeginTurn(owner, id string) (prev string, ok bool, err error) {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx,
		`SELECT status FROM sessions WHERE owner=$1 AND id=$2 FOR UPDATE`, owner, id).Scan(&prev)
	if err != nil {
		return "", false, err
	}
	if prev == "building" {
		return "building", false, nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET status='building', updated_at=now() WHERE id=$1`, id); err != nil {
		return "", false, err
	}
	return prev, true, tx.Commit(ctx)
}

func (p *PGStore) SetStatus(owner, id, status string) error {
	_, err := p.pool.Exec(context.Background(),
		`UPDATE sessions SET status=$3, updated_at=now() WHERE owner=$1 AND id=$2`, owner, id, status)
	return err
}

func (p *PGStore) RecordPublish(owner, id, url string) (Publish, error) {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Publish{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var raw []byte
	if err := tx.QueryRow(ctx,
		`SELECT publishes FROM sessions WHERE owner=$1 AND id=$2 FOR UPDATE`, owner, id).Scan(&raw); err != nil {
		return Publish{}, err
	}
	var pubs []Publish
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &pubs); err != nil {
			return Publish{}, err
		}
	}
	pub := Publish{Version: len(pubs) + 1, URL: url, At: time.Now()}
	pubs = append(pubs, pub)
	encoded, err := json.Marshal(pubs)
	if err != nil {
		return Publish{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET publishes=$2, updated_at=now() WHERE id=$1`, id, encoded); err != nil {
		return Publish{}, err
	}
	return pub, tx.Commit(ctx)
}

// mutateContest is the shared read-modify-write skeleton for contest state
// transitions; the session row lock serializes entrants. A session without
// a contest is a no-op (mirroring FileStore, which silently skips a nil
// Contest) unless allowEmpty — StartContest uses that to initialize one.
func (p *PGStore) mutateContest(owner, id string, allowEmpty bool, mutate func(*Contest)) error {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var raw []byte
	if err := tx.QueryRow(ctx,
		`SELECT contest FROM sessions WHERE owner=$1 AND id=$2 FOR UPDATE`, owner, id).Scan(&raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		if !allowEmpty {
			return nil
		}
		raw = []byte(`{}`)
	}
	contest := &Contest{}
	if err := json.Unmarshal(raw, contest); err != nil {
		return err
	}
	mutate(contest)
	encoded, err := json.Marshal(contest)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET contest=$2, updated_at=now() WHERE id=$1`, id, encoded); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *PGStore) StartContest(owner, id string, providers []string) error {
	return p.mutateContest(owner, id, true, func(c *Contest) {
		*c = Contest{Entries: make([]ContestEntry, 0, len(providers))}
		for _, name := range providers {
			c.Entries = append(c.Entries, ContestEntry{Provider: name, Status: "building"})
		}
	})
}

func (p *PGStore) UpdateContestEntry(owner, id, provider string, mutate func(*ContestEntry)) error {
	return p.mutateContest(owner, id, false, func(c *Contest) {
		for i := range c.Entries {
			if c.Entries[i].Provider == provider {
				mutate(&c.Entries[i])
			}
		}
	})
}

func (p *PGStore) FinishContest(owner, id string) error {
	return p.mutateContest(owner, id, false, func(c *Contest) { c.Done = true })
}

func (p *PGStore) PickContestWinner(owner, id, provider string) error {
	return p.mutateContest(owner, id, false, func(c *Contest) { c.Winner = provider })
}

func (p *PGStore) ClaimContestWinner(owner, id, provider string) error {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var raw []byte
	if err := tx.QueryRow(ctx,
		`SELECT contest FROM sessions WHERE owner=$1 AND id=$2 FOR UPDATE`, owner, id).Scan(&raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		return ErrNoContest
	}
	contest := &Contest{}
	if err := json.Unmarshal(raw, contest); err != nil {
		return err
	}
	if !contest.Done {
		return ErrContestRunning
	}
	if contest.Winner != "" {
		return ErrWinnerPicked
	}
	ready := false
	for _, entry := range contest.Entries {
		if entry.Provider == provider && entry.Status == "ready" {
			ready = true
		}
	}
	if !ready {
		return ErrNotReady
	}
	contest.Winner = provider
	encoded, err := json.Marshal(contest)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET contest=$2, updated_at=now() WHERE id=$1`, id, encoded); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *PGStore) ClearContestWinner(owner, id string) error {
	return p.mutateContest(owner, id, false, func(c *Contest) { c.Winner = "" })
}

func (p *PGStore) List(owner string) ([]Session, error) {
	rows, err := p.pool.Query(context.Background(),
		`SELECT id, owner, name, prompt, category, pinned, status,
			publishes, contest, created_at, updated_at
		 FROM sessions WHERE owner = $1
		 ORDER BY pinned DESC, updated_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		sess, err := p.scanSession(rows)
		if err != nil {
			return nil, err
		}
		if err := p.loadStream(sess); err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *PGStore) Rename(owner, id, name string) error {
	return p.touch(owner, id, `UPDATE sessions SET name=$3, updated_at=now() WHERE owner=$1 AND id=$2`, name)
}

func (p *PGStore) SetPinned(owner, id string, pinned bool) error {
	return p.touch(owner, id, `UPDATE sessions SET pinned=$3, updated_at=now() WHERE owner=$1 AND id=$2`, pinned)
}

func (p *PGStore) touch(owner, id, stmt string, arg any) error {
	tag, err := p.pool.Exec(context.Background(), stmt, owner, id, arg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session %s not found", id)
	}
	return nil
}

func (p *PGStore) Duplicate(owner, id string) (*Session, error) {
	src, err := p.Load(owner, id)
	if err != nil {
		return nil, err
	}
	newID, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
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
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`INSERT INTO sessions (id, owner, name, prompt, category, status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
		newID, owner, dst.Name, dst.Prompt, dst.Category, status, now); err != nil {
		return nil, err
	}
	for _, msg := range src.Messages {
		if _, err := tx.Exec(ctx,
			`INSERT INTO messages (session_id, role, agent, content, at) VALUES ($1,$2,$3,$4,$5)`,
			newID, msg.Role, msg.Agent, msg.Content, msg.At); err != nil {
			return nil, err
		}
	}
	for _, ev := range src.Events {
		payload, err := json.Marshal(ev.Payload)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO events (session_id, seq, type, agent, payload, at) VALUES ($1,$2,$3,$4,$5,$6)`,
			newID, ev.Seq, ev.Type, ev.Agent, payload, ev.At); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.Workspace(owner, newID), 0o755); err != nil {
		return nil, err
	}
	for _, sub := range []string{"app", "dist"} {
		_ = CopyDirTree(filepath.Join(p.Workspace(owner, id), sub), filepath.Join(p.Workspace(owner, newID), sub))
	}
	return dst, nil
}

func (p *PGStore) Delete(owner, id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid id")
	}
	tag, err := p.pool.Exec(context.Background(),
		`DELETE FROM sessions WHERE owner=$1 AND id=$2`, owner, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session %s not found", id)
	}
	return os.RemoveAll(p.Workspace(owner, id))
}
