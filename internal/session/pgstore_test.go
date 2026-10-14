// PG conformance suite: runs against the real atoms database when
// PGSTORE_DSN is set (CI/dev points it at a throwaway database); skipped
// otherwise — FileStore tests cover the same contract locally.
package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newPG(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}
	store, err := NewPGStore(filepath.Join(t.TempDir(), "ws"), dsn)
	if err != nil {
		t.Fatalf("NewPGStore: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestPGStoreConformance(t *testing.T) {
	p := newPG(t)
	const owner = "pgtest@example.com"

	sess, err := p.Create(owner, "a todo app")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := sess.ID

	// 事件流：seq 从 1 起无空洞单调递增。
	for i, typ := range []string{"plan", "file", "done"} {
		ev, err := p.AppendEvent(owner, id, Event{Type: typ, Payload: map[string]any{"n": i}})
		if err != nil {
			t.Fatalf("AppendEvent %s: %v", typ, err)
		}
		if ev.Seq != i+1 {
			t.Fatalf("seq = %d, want %d", ev.Seq, i+1)
		}
	}
	if err := p.AppendMessage(owner, id, Message{Role: "user", Content: "more"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	loaded, err := p.Load(owner, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Events) != 3 || len(loaded.Messages) != 2 {
		t.Fatalf("stream len = %d events / %d messages, want 3/2", len(loaded.Events), len(loaded.Messages))
	}
	if loaded.Events[1].Payload["n"].(float64) != 1 {
		t.Fatalf("event payload roundtrip lost: %v", loaded.Events[1].Payload)
	}

	// BeginTurn 语义：首个调用获胜，并发看到 building → ok=false。
	if _, ok, err := p.BeginTurn(owner, id, "turn"); ok || err != nil {
		t.Fatalf("BeginTurn on building session: ok=%v err=%v", ok, err)
	}
	if err := p.SetStatus(owner, id, "ready"); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	prev, ok, err := p.BeginTurn(owner, id, "turn")
	if !ok || err != nil || prev != "ready" {
		t.Fatalf("BeginTurn: prev=%q ok=%v err=%v", prev, ok, err)
	}
	if err := p.SetStatus(owner, id, "failed"); err != nil {
		t.Fatalf("SetStatus rollback: %v", err)
	}

	// 发布版本：1 起无空洞。
	for want := 1; want <= 2; want++ {
		pub, err := p.RecordPublish(owner, id, "https://x/app/"+id+"/")
		if err != nil {
			t.Fatalf("RecordPublish: %v", err)
		}
		if pub.Version != want {
			t.Fatalf("publish version = %d, want %d", pub.Version, want)
		}
	}

	// 竞赛状态机：错误映射与 FileStore 一致。
	if err := p.StartContest(owner, id, []string{"deepseek", "gemma"}); err != nil {
		t.Fatalf("StartContest: %v", err)
	}
	if err := p.ClaimContestWinner(owner, id, "deepseek"); !errors.Is(err, ErrContestRunning) {
		t.Fatalf("Claim running: %v", err)
	}
	_ = p.UpdateContestEntry(owner, id, "deepseek", func(e *ContestEntry) { e.Status = "ready" })
	_ = p.FinishContest(owner, id)
	if err := p.ClaimContestWinner(owner, id, "gemma"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Claim not-ready: %v", err)
	}
	if err := p.ClaimContestWinner(owner, id, "deepseek"); err != nil {
		t.Fatalf("Claim winner: %v", err)
	}
	if err := p.ClaimContestWinner(owner, id, "deepseek"); !errors.Is(err, ErrWinnerPicked) {
		t.Fatalf("Claim twice: %v", err)
	}

	// 列表排序：pinned 优先，其后 updated_at 倒序。
	other, err := p.Create(owner, "second")
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := p.Rename(owner, other.ID, "renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := p.SetPinned(owner, id, true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	list, err := p.List(owner)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) < 2 || list[0].ID != id || !list[0].Pinned {
		t.Fatalf("List order wrong: %+v", list)
	}

	// 副本：消息/事件随迁，发布记录清零，building 归一为 failed。
	dup, err := p.Duplicate(owner, id)
	if err != nil {
		t.Fatalf("Duplicate: %v", err)
	}
	dl, err := p.Load(owner, dup.ID)
	if err != nil {
		t.Fatalf("Load duplicate: %v", err)
	}
	// 竞赛状态不随迁（与 FileStore 一致：副本不带 contest）。
	if len(dl.Events) != 3 || len(dl.Messages) != 2 || len(dl.Publishes) != 0 || dl.Contest != nil {
		t.Fatalf("duplicate content wrong: ev=%d msg=%d pub=%d contest=%v",
			len(dl.Events), len(dl.Messages), len(dl.Publishes), dl.Contest != nil)
	}

	// 公共 id→owner 索引与删除。
	if got, err := p.IndexOwner(id); err != nil || got != owner {
		t.Fatalf("IndexOwner: %q %v", got, err)
	}
	if err := p.Delete(owner, dup.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := p.Load(owner, dup.ID); err == nil {
		t.Fatalf("deleted session still loads")
	}
}

// Ticket #56: recent comes first, and pinned stays above unpinned even when
// older. This is the same rule FileStore uses (store_test covers it), but we
// check PGStore too because the SQL ORDER BY is the only part that enforces it.
func TestPGListOrder(t *testing.T) {
	p := newPG(t)
	const owner = "listorder@example.com"

	a, err := p.Create(owner, "older unpinned")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Rename(owner, a.ID, "A"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	b, err := p.Create(owner, "newer unpinned")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Rename(owner, b.ID, "B"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	c, err := p.Create(owner, "oldest but pinned")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Rename(owner, c.ID, "C"); err != nil {
		t.Fatal(err)
	}

	if err := p.SetPinned(owner, c.ID, true); err != nil {
		t.Fatal(err)
	}

	list, err := p.List(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 3 {
		t.Fatalf("expected at least 3 sessions, got %d", len(list))
	}
	// The pinned one must be first, the other two must be newest-first.
	if list[0].ID != c.ID || !list[0].Pinned {
		t.Errorf("first item should be pinned C, got %s pinned=%v", list[0].ID, list[0].Pinned)
	}
	var foundB, foundA bool
	for i := 1; i < len(list); i++ {
		if list[i].ID == b.ID {
			foundB = true
			if foundA {
				t.Error("B (newer) should come before A (older)")
			}
		}
		if list[i].ID == a.ID {
			foundA = true
		}
	}
	if !foundB || !foundA {
		t.Error("A or B missing from list")
	}
}

// Ticket #68: the timing columns must survive a round trip through Postgres.
// The FileStore persists the whole Session as JSON, so only this layer can
// catch a SELECT that forgets to read them back.
func TestPGTurnTimingRoundTrip(t *testing.T) {
	p := newPG(t)
	const owner = "timing@example.com"

	sess, err := p.Create(owner, "一个待办应用")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	loaded, err := p.Load(owner, sess.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.QueuedAt == nil {
		t.Fatal("Create wrote queued_at but Load did not read it back")
	}
	if loaded.StartedAt != nil {
		t.Fatal("nothing claimed the session, so started_at must be null")
	}

	if ok, err := p.ClaimLease(owner, sess.ID, "worker-1", time.Minute); err != nil || !ok {
		t.Fatalf("ClaimLease = %v, %v; want true", ok, err)
	}
	claimed, err := p.Load(owner, sess.ID)
	if err != nil {
		t.Fatalf("load after claim: %v", err)
	}
	if claimed.StartedAt == nil {
		t.Fatal("ClaimLease wrote started_at but Load did not read it back")
	}

	// A follow-up turn restarts the clock and clears the start.
	if err := p.SetStatus(owner, sess.ID, "ready"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, ok, err := p.BeginTurn(owner, sess.ID, "turn"); err != nil || !ok {
		t.Fatalf("BeginTurn = %v, %v; want true", ok, err)
	}
	next, err := p.Load(owner, sess.ID)
	if err != nil {
		t.Fatalf("load after BeginTurn: %v", err)
	}
	if next.StartedAt != nil {
		t.Fatal("BeginTurn left the previous turn's started_at in place")
	}
	if next.QueuedAt == nil || !next.QueuedAt.After(*claimed.QueuedAt) {
		t.Fatalf("BeginTurn did not advance queued_at: %v", next.QueuedAt)
	}

	// List reads through the same scan path, so it must carry them too.
	list, err := p.List(owner)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, item := range list {
		if item.ID != sess.ID {
			continue
		}
		found = true
		if item.QueuedAt == nil {
			t.Fatal("List did not read queued_at back")
		}
	}
	if !found {
		t.Fatal("session missing from List")
	}
}
