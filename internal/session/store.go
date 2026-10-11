package session

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

	// BeginTurn atomically flips the session to "building"; ok=false
	// means another turn holds it (the HTTP layer answers 409).
	BeginTurn(owner, id string) (prev string, ok bool, err error)
	SetStatus(owner, id, status string) error

	RecordPublish(owner, id, url string) (Publish, error)

	StartContest(owner, id string, providers []string) error
	UpdateContestEntry(owner, id, provider string, mutate func(*ContestEntry)) error
	FinishContest(owner, id string) error
	PickContestWinner(owner, id, provider string) error
	ClaimContestWinner(owner, id, provider string) error
	ClearContestWinner(owner, id string) error
}

var _ Store = (*FileStore)(nil)
