package server_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// tinyPNG is a real 1x1 red PNG (69 bytes) — enough to exercise the endpoints
// without embedding a fixture file.
var tinyPNG = func() []byte {
	b, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC")
	if err != nil {
		panic(err)
	}
	return b
}()

func putThumbnail(t *testing.T, base, id string, body []byte) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/api/sessions/"+id+"/thumbnail", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "image/png")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put thumbnail: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func getThumbnail(t *testing.T, base, id string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(base + "/api/sessions/" + id + "/thumbnail")
	if err != nil {
		t.Fatalf("get thumbnail: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read thumbnail: %v", err)
	}
	return resp.StatusCode, string(raw), resp.Header
}

// TestThumbnailRoundTrip is the happy path: the browser captures the preview
// frame, the bytes land in the session's workspace, and the dashboard can
// read them back (ticket #56).
func TestThumbnailRoundTrip(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	// Before any capture the cover does not exist: the dashboard needs a 404
	// it can fall back from, not an empty 200.
	if status, _, _ := getThumbnail(t, ts.URL, id); status != http.StatusNotFound {
		t.Fatalf("thumbnail before upload = %d, want 404", status)
	}

	status, out := putThumbnail(t, ts.URL, id, tinyPNG)
	if status != http.StatusOK {
		t.Fatalf("put thumbnail = %d, want 200 (%v)", status, out)
	}

	status, body, header := getThumbnail(t, ts.URL, id)
	if status != http.StatusOK {
		t.Fatalf("get thumbnail = %d, want 200", status)
	}
	if body != string(tinyPNG) {
		t.Fatalf("thumbnail body = %d bytes, want the uploaded image", len(body))
	}
	if ct := header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	// The URL carries no cache-buster, so the cover of a fresh build would
	// otherwise stay hidden behind the previous one.
	if cc := header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}

// TestThumbnailRejectsNonPNG keeps arbitrary bytes off the disk: the body is
// written verbatim, so the type is checked on the bytes, not the header.
func TestThumbnailRejectsNonPNG(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	cases := []struct {
		name string
		body []byte
		want int
	}{
		{"not an image", []byte("<html>hello</html>"), http.StatusBadRequest},
		{"empty", nil, http.StatusBadRequest},
		{"png magic only, then junk", append(bytes.Clone(tinyPNG[:8]), []byte("junk")...), http.StatusOK},
		{"too large", append(bytes.Clone(tinyPNG), bytes.Repeat([]byte{0}, 1<<20)...), http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := putThumbnail(t, ts.URL, id, tc.body)
			if status != tc.want {
				t.Fatalf("put = %d, want %d", status, tc.want)
			}
		})
	}
}

// TestThumbnailIsMemberScoped is the boundary: the cover belongs to the
// session's workspace like everything else under /api. A session that does
// not live under the caller's workspace must be a 404, never a readable
// image, and an anonymous-shaped request never reaches the handler at all
// (the identity middleware rejects it before routing).
func TestThumbnailIsMemberScoped(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	if status, _ := putThumbnail(t, ts.URL, id, tinyPNG); status != http.StatusOK {
		t.Fatalf("put thumbnail = %d, want 200", status)
	}

	// Another workspace's session id: the caller cannot read or write it.
	other := "0123456789abcdef"
	if status, _ := putThumbnail(t, ts.URL, other, tinyPNG); status != http.StatusNotFound {
		t.Fatalf("put to a foreign session = %d, want 404", status)
	}
	if status, _, _ := getThumbnail(t, ts.URL, other); status != http.StatusNotFound {
		t.Fatalf("get a foreign session's thumbnail = %d, want 404", status)
	}
}

// TestDuplicateCopiesTheCover checks that a 副本 keeps its dashboard picture:
// the copy is a full snapshot of the session's derived files (ticket #56,
// mirroring the 改动记录 copy from ticket #54).
func TestDuplicateCopiesTheCover(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	if status, _ := putThumbnail(t, ts.URL, id, tinyPNG); status != http.StatusOK {
		t.Fatalf("put thumbnail = %d, want 200", status)
	}

	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/duplicate", "application/json", nil)
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	var dup struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dup); err != nil {
		resp.Body.Close()
		t.Fatalf("decode duplicate: %v", err)
	}
	resp.Body.Close()
	if dup.SessionID == "" {
		t.Fatal("duplicate returned no sessionId")
	}

	status, body, _ := getThumbnail(t, ts.URL, dup.SessionID)
	if status != http.StatusOK {
		t.Fatalf("copy thumbnail = %d, want 200", status)
	}
	if body != string(tinyPNG) {
		t.Fatal("the copy does not carry the original cover")
	}
}

// TestThumbnailSurvivesASecondUpload guards the overwrite path: a new build
// produces a new capture, and the old cover must be replaced, not appended
// to.
func TestThumbnailSurvivesASecondUpload(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	if status, _ := putThumbnail(t, ts.URL, id, tinyPNG); status != http.StatusOK {
		t.Fatalf("first put = %d, want 200", status)
	}
	second := append(bytes.Clone(tinyPNG), []byte("SECOND")...)
	if status, _ := putThumbnail(t, ts.URL, id, second); status != http.StatusOK {
		t.Fatalf("second put = %d, want 200", status)
	}
	_, body, _ := getThumbnail(t, ts.URL, id)
	if !strings.HasSuffix(body, "SECOND") {
		t.Fatalf("cover after re-upload is %d bytes, want the second image", len(body))
	}
}
