package session

import "time"

// LeasePending is the 认领租约 (execution lease) owner written by an HTTP
// claim (BeginTurn) or the recovery sweeper before a Worker takes over. A
// Worker may always take over a pending lease; it may only take over another
// Worker's lease once that lease has expired.
const LeasePending = "__pending__"

// CancelEventType is the 取消回合 (Cancel Turn) request on the 事件流. It is
// the persisted flag ADR-0013 asks for: written by whichever replica served
// the request, read back by whichever replica is executing the 回合.
const CancelEventType = "cancel"

// JobKind reports the in-flight 任务 (Job) kind of a building session
// (turn | contest | publish), inferring a turn for legacy sessions that never
// recorded one. It is the exported form of the recovery sweeper's own
// resolution so the HTTP layer cannot disagree with recovery about what is
// running.
func JobKind(sess *Session) string { return resolveKind(sess) }

// defaultClaimTTL bounds the enqueue→execute window: a claim written by
// BeginTurn that no Worker picks up within it becomes claimable again, so a
// Job lost between the HTTP handler and the Queue is still recovered.
const defaultClaimTTL = 2 * time.Minute

// BuildingRef identifies one 会话 (Session) the recovery sweeper must
// re-enqueue: its owner, id and the in-flight 任务 (Job) kind.
type BuildingRef struct {
	Owner string
	ID    string
	Kind  string
}

// Store is the 会话 (Session) persistence contract. FileStore implements
// it on session.json under the workspace dir (tests, local dev);
// PGStore implements it on Postgres (production). Callers only rely on
// this interface, so the implementation can be swapped by config.
type Store interface {
	Dir() string
	Workspace(owner, id string) string

	Create(owner, prompt string) (*Session, error)
	IndexOwner(id string) (string, error)
	Load(owner, id string) (*Session, error)
	Save(owner string, sess *Session) error
	List(owner string) ([]Session, error)
	Rename(owner, id, name string) error
	SetPinned(owner, id string, pinned bool) error
	Duplicate(owner, id string) (*Session, error)
	Delete(owner, id string) error

	// AppendEvent assigns the next gapless seq (1-based) and persists.
	AppendEvent(owner, id string, ev Event) (Event, error)
	AppendMessage(owner, id string, msg Message) error

	// EventsAfter returns the persisted events with Seq > after, ordered by
	// seq. It is the SSE reconnect/gap-fill replay path: Redis Streams is
	// only the hot path, so a missed live event is topped up from here.
	EventsAfter(owner, id string, after int) ([]Event, error)

	// RequestCancel records a 取消回合 (Cancel Turn) request while the session
	// is building, returning the appended event. The flag lives in the
	// 事件流 rather than in process memory, so the Worker executing the 回合
	// sees it whichever replica served the request, and a 回合 re-run after a
	// restart sees it too (ADR-0013). ok=false means nothing was in flight.
	//
	// The status check and the append are one critical section, because the
	// 回合's own status flip is what closes the window: a request that lost
	// that race must be rejected rather than left on the stream to cancel the
	// *next* turn.
	RequestCancel(owner, id string) (ev Event, ok bool, err error)

	// BeginTurn atomically flips the session to "building" and records the
	// 任务 (Job) kind plus a pending 认领租约 (lease); ok=false means another
	// turn holds it (the HTTP layer answers 409).
	BeginTurn(owner, id, kind string) (prev string, ok bool, err error)
	SetStatus(owner, id, status string) error
	// SetJobKind records the in-flight Job kind so crash recovery can
	// re-enqueue it after a restart (turn | contest | publish).
	SetJobKind(owner, id, kind string) error

	// ClaimLease atomically takes the execution lease for a building
	// session. ok=false means the session is not building, or another
	// executor holds a live lease; the caller must not run the session. ttl
	// bounds how long a crashed holder blocks a re-run. A pending lease
	// (BeginTurn / sweeper) is always takeover-able by a Worker.
	ClaimLease(owner, id, holder string, ttl time.Duration) (ok bool, err error)
	// ReleaseLease clears the lease when holder still owns it (best effort).
	ReleaseLease(owner, id, holder string) error
	// ListBuilding returns every building session whose lease is absent or
	// expired — the crash-recovery sweep set.
	ListBuilding() ([]BuildingRef, error)

	RecordPublish(owner, id, url string) (Publish, error)
	SetPublic(owner, id string, public bool) error

	StartContest(owner, id string, providers []string) error
	UpdateContestEntry(owner, id, provider string, mutate func(*ContestEntry)) error
	FinishContest(owner, id string) error
	PickContestWinner(owner, id, provider string) error
	ClaimContestWinner(owner, id, provider string) error
	ClearContestWinner(owner, id string) error
}

var _ Store = (*FileStore)(nil)
