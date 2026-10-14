package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// changeSummary mirrors the list endpoint's record shape. It deliberately has
// no Diff field: the list must never ship diff bodies (ticket #54).
type changeSummary struct {
	Seq      int    `json:"seq"`
	Path     string `json:"path"`
	Author   string `json:"author"`
	Kind     string `json:"kind"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Binary   bool   `json:"binary"`
	TooLarge bool   `json:"tooLarge"`
	At       string `json:"at"`
}

type changeDetail struct {
	changeSummary
	Diff string `json:"diff"`
}

func listChanges(t *testing.T, base, id string) (int, []changeSummary) {
	t.Helper()
	resp, err := http.Get(base + "/api/sessions/" + id + "/changes")
	if err != nil {
		t.Fatalf("list changes: %v", err)
	}
	defer resp.Body.Close()
	var out []changeSummary
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode changes: %v", err)
		}
	}
	return resp.StatusCode, out
}

func getChange(t *testing.T, base, id string, seq int) (int, changeDetail) {
	t.Helper()
	resp, err := http.Get(base + "/api/sessions/" + id + "/changes/" + strconv.Itoa(seq))
	if err != nil {
		t.Fatalf("get change: %v", err)
	}
	defer resp.Body.Close()
	var out changeDetail
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode change: %v", err)
		}
	}
	return resp.StatusCode, out
}

// TestTurnRecordsEveryAnnouncedFile is the core join: each 文件 (file) event
// the 回合 announced has exactly one 改动记录 keyed to its seq, so the
// timeline can attach a diff to the row it already renders (ticket #54).
func TestTurnRecordsEveryAnnouncedFile(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	status, recs := listChanges(t, ts.URL, id)
	if status != http.StatusOK {
		t.Fatalf("list changes = %d, want 200", status)
	}
	announced := map[int]string{}
	for _, ev := range sessionEvents(t, ts.URL, id) {
		if ev.Type == "file" {
			announced[ev.Seq] = ev.Payload["path"].(string)
		}
	}
	if len(recs) != len(announced) {
		t.Fatalf("%d records for %d announced files", len(recs), len(announced))
	}
	bySeq := map[int]changeSummary{}
	for _, rec := range recs {
		bySeq[rec.Seq] = rec
	}
	for seq, path := range announced {
		rec, ok := bySeq[seq]
		if !ok {
			t.Fatalf("no change record for seq %d (%s)", seq, path)
		}
		if rec.Path != path {
			t.Fatalf("seq %d carries %q, want %q", seq, rec.Path, path)
		}
		if rec.Author != "agent" {
			t.Fatalf("seq %d author = %q, want agent", seq, rec.Author)
		}
		// The first 回合 writes a fresh tree, so every file is an addition.
		if rec.Kind != "added" {
			t.Fatalf("seq %d kind = %q, want added", seq, rec.Kind)
		}
		if rec.Added == 0 || rec.Removed != 0 {
			t.Fatalf("seq %d counts = +%d/-%d, want +n/-0", seq, rec.Added, rec.Removed)
		}
		if rec.At == "" {
			t.Fatalf("seq %d has no timestamp", seq)
		}
	}
}

// The list endpoint must not ship diff bodies: a turn's diffs are far too
// much for every timeline render, which is why they are fetched one at a
// time (ticket #54).
func TestListChangesCarriesNoDiffBodies(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	raw := getBody(t, ts.URL+"/api/sessions/"+id+"/changes")
	if strings.Contains(raw, `"diff"`) {
		t.Fatal("the list endpoint shipped a diff body")
	}
	if !strings.Contains(raw, `"kind":"added"`) {
		t.Fatalf("unexpected list body: %s", raw)
	}
}

// TestChangeDetailJoinsToTheAnnouncedRow proves the join lands on real
// content: the diff of the App.tsx record is that file's own text.
func TestChangeDetailJoinsToTheAnnouncedRow(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	_, recs := listChanges(t, ts.URL, id)
	appSeq := 0
	for _, rec := range recs {
		if rec.Path == "src/App.tsx" {
			appSeq = rec.Seq
		}
	}
	if appSeq == 0 {
		t.Fatal("no record for src/App.tsx")
	}
	status, detail := getChange(t, ts.URL, id, appSeq)
	if status != http.StatusOK {
		t.Fatalf("get change = %d, want 200", status)
	}
	if !strings.HasPrefix(detail.Diff, "--- /dev/null\n+++ b/src/App.tsx\n") {
		t.Fatalf("added-file header wrong:\n%s", detail.Diff)
	}
	if !strings.Contains(detail.Diff, "+export default function App() {") {
		t.Fatalf("diff does not carry the file's own content:\n%s", detail.Diff)
	}
	if detail.Added == 0 {
		t.Fatal("an added file must report added lines")
	}
}

// TestHandEditRecordsAUserChange is the ticket's other half: a 手改 must be
// distinguishable from the Agent's writes, and its diff must show the edit
// (ticket #54).
func TestHandEditRecordsAUserChange(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	const edited = "export default function App(){ return <main>HAND_EDITED_MARKER</main> }"
	status, out := putFile(t, ts.URL, id, "src/App.tsx", edited)
	if status != http.StatusOK {
		t.Fatalf("put file = %d, want 200 (%v)", status, out)
	}

	// The record must join to the 文件 event the edit itself emitted, not to
	// the turn's earlier one for the same path.
	editSeq := 0
	for _, ev := range sessionEvents(t, ts.URL, id) {
		if ev.Type == "file" && ev.Payload["edited"] == true && ev.Payload["path"] == "src/App.tsx" {
			editSeq = ev.Seq
		}
	}
	if editSeq == 0 {
		t.Fatal("no edited file event on the stream")
	}

	_, recs := listChanges(t, ts.URL, id)
	var hand *changeSummary
	for i := range recs {
		if recs[i].Seq == editSeq {
			hand = &recs[i]
		}
	}
	if hand == nil {
		t.Fatal("no change record keyed to the edit's event seq")
	}
	if hand.Author != "user" {
		t.Fatalf("author = %q, want user", hand.Author)
	}
	if hand.Kind != "modified" {
		t.Fatalf("kind = %q, want modified", hand.Kind)
	}

	_, detail := getChange(t, ts.URL, id, editSeq)
	// The edit itself, as one added line, next to the lines it replaced.
	if !strings.Contains(detail.Diff, "+export default function App(){ return <main>HAND_EDITED_MARKER</main> }") {
		t.Fatalf("diff does not show the edit:\n%s", detail.Diff)
	}
	if !strings.Contains(detail.Diff, "-export default function App() {") {
		t.Fatalf("diff does not show the replaced line:\n%s", detail.Diff)
	}
}

// A binary 改动记录 degrades to "changed, no diff": the page lists the file
// and says so, and never holds a body it cannot render (ticket #54).
func TestBinaryChangeRecordDegrades(t *testing.T) {
	ts, dir := newTestServerDir(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	// Written the way the pipeline would have written it for a binary file.
	line := `{"seq":999999,"path":"assets/logo.png","author":"agent","kind":"modified","binary":true,"at":"2026-10-14T00:00:00Z"}` + "\n"
	p := filepath.Join(dir, "dev-user", id, "changes.jsonl")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open changes file: %v", err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append binary record: %v", err)
	}
	f.Close()

	_, recs := listChanges(t, ts.URL, id)
	var found bool
	for _, rec := range recs {
		if rec.Seq == 999999 {
			found = true
			if !rec.Binary {
				t.Fatal("binary flag lost in the list")
			}
		}
	}
	if !found {
		t.Fatal("binary record missing from the list")
	}

	status, detail := getChange(t, ts.URL, id, 999999)
	if status != http.StatusOK {
		t.Fatalf("get change = %d, want 200", status)
	}
	if !detail.Binary {
		t.Fatal("binary flag lost in the detail")
	}
	if detail.Diff != "" {
		t.Fatalf("a binary record must carry no diff, got:\n%s", detail.Diff)
	}
}

func TestChangeRoutesRefuseBadRequests(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	// An unknown session is a 404, not an empty list: another workspace's
	// id must not be told whether it has any changes.
	if status, _ := listChanges(t, ts.URL, "no-such-session"); status != http.StatusNotFound {
		t.Fatalf("list changes for an unknown session = %d, want 404", status)
	}
	if status, _ := getChange(t, ts.URL, "no-such-session", 1); status != http.StatusNotFound {
		t.Fatalf("get change for an unknown session = %d, want 404", status)
	}
	// A seq that no record carries is a 404 too.
	if status, _ := getChange(t, ts.URL, id, 424242); status != http.StatusNotFound {
		t.Fatalf("get change for an unknown seq = %d, want 404", status)
	}
	// A seq that is not a number is a 400, not a 500.
	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/changes/not-a-number")
	if err != nil {
		t.Fatalf("get change: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("get change with a bad seq = %d, want 400", resp.StatusCode)
	}
}
