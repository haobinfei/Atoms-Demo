package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// TestPublishVersionsAndHistory drives two publish rounds across a chat
// turn and asserts the full publish contract: public URL without
// credentials, monotonically increasing versions, complete history.
func TestPublishVersionsAndHistory(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	first := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	// Publish while a turn is running must be rejected.
	id2 := createSession(t, ts.URL, "一个数据看板")
	conflict, err := http.Post(ts.URL+"/api/sessions/"+id2+"/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish while building: %v", err)
	}
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("publish while building status = %d, want 409", conflict.StatusCode)
	}

	// First publish: version 1, public URL, and the app is actually served.
	pub1 := publish(t, ts.URL, id)
	if pub1.Version != 1 {
		t.Fatalf("first publish version = %d, want 1", pub1.Version)
	}
	if pub1.URL != "/app/"+id+"/" {
		t.Fatalf("publish url = %q, want /app/%s/", pub1.URL, id)
	}
	resp, err := http.Get(ts.URL + pub1.URL)
	if err != nil {
		t.Fatalf("open published app: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("published app status = %d, want 200", resp.StatusCode)
	}

	// A chat turn changes the app; publishing again must version upward.
	chatBody, _ := json.Marshal(map[string]string{"message": "把主题改成蓝色"})
	chatResp, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json", strings.NewReader(string(chatBody)))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	chatResp.Body.Close()
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", len(first), 60)
	publish(t, ts.URL, id)

	history := publishHistory(t, ts.URL, id)
	if len(history) != 2 {
		t.Fatalf("publish history len = %d, want 2", len(history))
	}
	if history[0].Version != 1 || history[1].Version != 2 {
		t.Fatalf("versions = %d,%d, want 1,2", history[0].Version, history[1].Version)
	}

	// Unknown session: 404 on both endpoints.
	missing, err := http.Post(ts.URL+"/api/sessions/nope/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish missing: %v", err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("publish missing status = %d, want 404", missing.StatusCode)
	}
	getMissing, err := http.Get(ts.URL + "/api/sessions/nope/publish")
	if err != nil {
		t.Fatalf("get publish missing: %v", err)
	}
	getMissing.Body.Close()
	if getMissing.StatusCode != http.StatusNotFound {
		t.Fatalf("get publish missing status = %d, want 404", getMissing.StatusCode)
	}
}

func publish(t *testing.T, tsURL, id string) struct {
	Version int    `json:"version"`
	URL     string `json:"url"`
} {
	t.Helper()
	resp, err := http.Post(tsURL+"/api/sessions/"+id+"/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish status = %d, want 200", resp.StatusCode)
	}
	var pub struct {
		Version int    `json:"version"`
		URL     string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pub); err != nil {
		t.Fatalf("decode publish: %v", err)
	}
	return pub
}

func publishHistory(t *testing.T, tsURL, id string) []struct {
	Version int    `json:"version"`
	URL     string `json:"url"`
} {
	t.Helper()
	resp, err := http.Get(tsURL + "/api/sessions/" + id + "/publish")
	if err != nil {
		t.Fatalf("publish history: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish history status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Publishes []struct {
			Version int    `json:"version"`
			URL     string `json:"url"`
		} `json:"publishes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	return out.Publishes
}
