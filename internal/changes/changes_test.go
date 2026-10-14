package changes_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/changes"
)

func newSession(t *testing.T) (store *changes.Store, owner, id string) {
	t.Helper()
	dir := t.TempDir()
	owner, id = "owner@example.test", "abc123"
	if err := os.MkdirAll(filepath.Join(dir, owner, id), 0o755); err != nil {
		t.Fatal(err)
	}
	return changes.NewStore(dir), owner, id
}

func TestAppendListRoundTrip(t *testing.T) {
	store, owner, id := newSession(t)
	recs := []changes.Record{
		changes.NewRecord(3, "src/App.tsx", changes.AuthorAgent, "", "export default function App(){}\n"),
		changes.NewRecord(4, "src/main.tsx", changes.AuthorAgent, "old\n", "new\n"),
	}
	for _, rec := range recs {
		if err := store.Append(owner, id, rec); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := store.List(owner, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Path != "src/App.tsx" || got[0].Kind != changes.KindAdded {
		t.Fatalf("first record wrong: %+v", got[0])
	}
	if got[1].Path != "src/main.tsx" || got[1].Kind != changes.KindModified {
		t.Fatalf("second record wrong: %+v", got[1])
	}
	if got[0].Diff == "" {
		t.Fatal("an added file must carry its full-content diff")
	}
}

func TestListWithoutRecordsIsEmpty(t *testing.T) {
	store, owner, id := newSession(t)
	got, err := store.List(owner, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestDetail(t *testing.T) {
	store, owner, id := newSession(t)
	for seq := 1; seq <= 3; seq++ {
		rec := changes.NewRecord(seq, "f.txt", changes.AuthorUser, "old\n", fmt.Sprintf("new %d\n", seq))
		if err := store.Append(owner, id, rec); err != nil {
			t.Fatal(err)
		}
	}
	rec, ok, err := store.Detail(owner, id, 2)
	if err != nil || !ok {
		t.Fatalf("detail(2): ok=%v err=%v", ok, err)
	}
	if !strings.Contains(rec.Diff, "+new 2") {
		t.Fatalf("wrong record's diff:\n%s", rec.Diff)
	}
	if _, ok, _ := store.Detail(owner, id, 99); ok {
		t.Fatal("an unknown seq must report ok=false")
	}
}

// The same path can change more than once; the newest record for a seq wins.
func TestDetailNewestWins(t *testing.T) {
	store, owner, id := newSession(t)
	for _, content := range []string{"first\n", "second\n"} {
		rec := changes.NewRecord(5, "f.txt", changes.AuthorUser, "old\n", content)
		if err := store.Append(owner, id, rec); err != nil {
			t.Fatal(err)
		}
	}
	rec, ok, err := store.Detail(owner, id, 5)
	if err != nil || !ok {
		t.Fatalf("detail: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(rec.Diff, "+second") {
		t.Fatalf("newest record must win:\n%s", rec.Diff)
	}
}

// A torn last line (a crash mid-append) must not cost the whole list.
func TestListSkipsTornLine(t *testing.T) {
	store, owner, id := newSession(t)
	if err := store.Append(owner, id, changes.NewRecord(1, "a.txt", changes.AuthorAgent, "", "a\n")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(store.Dir(), owner, id, "changes.jsonl")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":2,"path":"b.txt"`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, err := store.List(owner, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Path != "a.txt" {
		t.Fatalf("torn line must be skipped, got %+v", got)
	}
}

func TestAppendRejectsUnsafeIDs(t *testing.T) {
	store, _, _ := newSession(t)
	for _, tc := range []struct{ owner, id string }{
		{"../escape", "abc123"},
		{"owner@example.test", "../abc123"},
		{"owner@example.test", ""},
	} {
		if err := store.Append(tc.owner, tc.id, changes.Record{Seq: 1}); err == nil {
			t.Fatalf("append(%q, %q) must fail", tc.owner, tc.id)
		}
	}
}

// Concurrent appends (a 回合 writing while a hand edit lands) must not lose
// or interleave records.
func TestConcurrentAppends(t *testing.T) {
	store, owner, id := newSession(t)
	const writers, each = 8, 10
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				rec := changes.NewRecord(w*each+i+1, "f.txt", changes.AuthorAgent, "old\n", "new\n")
				if err := store.Append(owner, id, rec); err != nil {
					t.Errorf("append: %v", err)
				}
			}
		}(w)
	}
	wg.Wait()
	got, err := store.List(owner, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != writers*each {
		t.Fatalf("len = %d, want %d", len(got), writers*each)
	}
}
