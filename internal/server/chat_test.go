package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// TestChatTurnRegeneratesApp drives a first build, then a modification turn,
// and asserts the full-regeneration contract: user message on the timeline,
// changed files on disk, history persisted.
func TestChatTurnRegeneratesApp(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	first := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	// Turn 2: ask for a blue theme. The template provider receives the
	// augmented prompt, so the accent must actually change.
	chatBody, _ := json.Marshal(map[string]string{"message": "把主题改成蓝色"})
	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json", strings.NewReader(string(chatBody)))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d, want 200", resp.StatusCode)
	}

	after := len(first)
	events := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", after, 60)
	var sawUserMsg bool
	for _, ev := range events {
		if ev.Type == "message" && ev.Payload["role"] == "user" {
			sawUserMsg = true
		}
	}
	if !sawUserMsg {
		t.Fatalf("user message not on timeline: %+v", events)
	}

	// The pipeline kept serving events with gapless per-session seq numbers
	// across turns: turn 2 continues at seq len(turn 1)+1.
	for i, ev := range events {
		if ev.Seq != after+i+1 {
			t.Fatalf("seq gap at %d: %+v", i, ev)
		}
	}

	// The rebuilt styles are served through the file API.
	fileResp, err := http.Get(ts.URL + "/api/sessions/" + id + "/file/src/styles.css")
	if err != nil {
		t.Fatalf("get styles: %v", err)
	}
	defer fileResp.Body.Close()
	if fileResp.StatusCode != http.StatusOK {
		t.Fatalf("get styles status = %d, want 200", fileResp.StatusCode)
	}
	var file struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(fileResp.Body).Decode(&file); err != nil {
		t.Fatalf("decode file: %v", err)
	}
	css := file.Content
	if !strings.Contains(css, "#3b82f6") {
		t.Fatal("theme color did not change to blue after chat turn")
	}

	// History: both the original prompt and the follow-up are persisted.
	get, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	var sess session.Session
	if err := json.NewDecoder(get.Body).Decode(&sess); err != nil {
		t.Fatalf("decode: %v", err)
	}
	get.Body.Close()
	var userMsgs []string
	for _, m := range sess.Messages {
		if m.Role == "user" {
			userMsgs = append(userMsgs, m.Content)
		}
	}
	if len(userMsgs) != 2 || userMsgs[0] != "一家咖啡店的电商网站" || userMsgs[1] != "把主题改成蓝色" {
		t.Fatalf("history = %v", userMsgs)
	}
}

// TestChatRejectsBadRequests covers the validation and conflict paths:
// empty messages are a 400, and chatting while a turn is running is a 409.
func TestChatRejectsBadRequests(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一个数据看板")

	// Still building: the first turn owns the session.
	conflict, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json",
		strings.NewReader(`{"message":"把主题改成蓝色"}`))
	if err != nil {
		t.Fatalf("chat while building: %v", err)
	}
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("chat while building status = %d, want 409", conflict.StatusCode)
	}

	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	// Empty message after the turn finished.
	bad, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json",
		strings.NewReader(`{"message":"  "}`))
	if err != nil {
		t.Fatalf("empty chat: %v", err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty chat status = %d, want 400", bad.StatusCode)
	}

	// Unknown session.
	missing, err := http.Post(ts.URL+"/api/sessions/nope/chat", "application/json",
		strings.NewReader(`{"message":"你好"}`))
	if err != nil {
		t.Fatalf("missing session chat: %v", err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session chat status = %d, want 404", missing.StatusCode)
	}
}
