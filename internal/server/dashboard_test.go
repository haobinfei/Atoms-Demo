package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

type summary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Pinned   bool   `json:"pinned"`
}

func listSessions(t *testing.T, tsURL, query string) []summary {
	t.Helper()
	url := tsURL + "/api/sessions/list"
	if query != "" {
		url += "?q=" + query
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list sessions status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Sessions []summary `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return out.Sessions
}

func postJSON(t *testing.T, url string, body any, wantStatus int) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s status = %d, want %d", url, resp.StatusCode, wantStatus)
	}
}

func findSummary(t *testing.T, list []summary, id string) summary {
	t.Helper()
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("session %s not in list %+v", id, list)
	return summary{}
}

// TestDashboardLifecycle covers list/search/rename/pin/duplicate/delete,
// asserting each mutation survives a reload (all state is disk-persisted).
func TestDashboardLifecycle(t *testing.T) {
	ts := newTestServer(t)
	id1 := createSession(t, ts.URL, "一家咖啡店的电商网站")
	id2 := createSession(t, ts.URL, "一个数据看板")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id1+"/events", 0, 40)
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id2+"/events", 0, 40)

	// List: both sessions with name, category, status.
	all := listSessions(t, ts.URL, "")
	if len(all) != 2 {
		t.Fatalf("list len = %d, want 2", len(all))
	}
	s1 := findSummary(t, all, id1)
	if s1.Name != "一家咖啡店的电商网站" || s1.Category != "ecommerce" || s1.Status != "ready" {
		t.Fatalf("summary 1 = %+v", s1)
	}
	if findSummary(t, all, id2).Category != "dashboard" {
		t.Fatal("session 2 category mismatch")
	}

	// Search matches name/prompt and filters out the rest.
	hits := listSessions(t, ts.URL, "咖啡")
	if len(hits) != 1 || hits[0].ID != id1 {
		t.Fatalf("search 咖啡 = %+v", hits)
	}
	if n := len(listSessions(t, ts.URL, "不存在的词")); n != 0 {
		t.Fatalf("search miss len = %d, want 0", n)
	}

	// Rename persists.
	postJSON(t, ts.URL+"/api/sessions/"+id2+"/rename", map[string]string{"name": "运营周报看板"}, 200)
	if got := findSummary(t, listSessions(t, ts.URL, ""), id2).Name; got != "运营周报看板" {
		t.Fatalf("renamed summary = %q", got)
	}
	get, err := http.Get(ts.URL + "/api/sessions/" + id2)
	if err != nil {
		t.Fatalf("get renamed: %v", err)
	}
	var sess struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(get.Body).Decode(&sess); err != nil {
		t.Fatalf("decode: %v", err)
	}
	get.Body.Close()
	if sess.Name != "运营周报看板" {
		t.Fatalf("session name = %q", sess.Name)
	}

	// Pin floats to the front and persists.
	postJSON(t, ts.URL+"/api/sessions/"+id2+"/pin", map[string]bool{"pinned": true}, 200)
	pinned := listSessions(t, ts.URL, "")
	if !pinned[0].Pinned || pinned[0].ID != id2 {
		t.Fatalf("pinned first = %+v", pinned[0])
	}

	// Duplicate: appears in the list with a copy name and serves its own
	// public URL immediately (files are copied).
	dupResp, err := http.Post(ts.URL+"/api/sessions/"+id2+"/duplicate", "application/json", nil)
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	var dup struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(dupResp.Body).Decode(&dup); err != nil {
		t.Fatalf("decode duplicate: %v", err)
	}
	dupResp.Body.Close()
	if dup.SessionID == "" || dup.SessionID == id2 {
		t.Fatalf("duplicate id = %q", dup.SessionID)
	}
	dupSum := findSummary(t, listSessions(t, ts.URL, ""), dup.SessionID)
	if dupSum.Name != "副本 · 运营周报看板" || dupSum.Category != "dashboard" {
		t.Fatalf("duplicate summary = %+v", dupSum)
	}
	app, err := http.Get(fmt.Sprintf("%s/app/%s/", ts.URL, dup.SessionID))
	if err != nil {
		t.Fatalf("open duplicate app: %v", err)
	}
	app.Body.Close()
	if app.StatusCode != http.StatusOK {
		t.Fatalf("duplicate app status = %d, want 200", app.StatusCode)
	}

	// Delete: gone everywhere, public URL dies with it.
	delReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id1, nil)
	if err != nil {
		t.Fatalf("build delete: %v", err)
	}
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", delResp.StatusCode)
	}
	rest := listSessions(t, ts.URL, "")
	if len(rest) != 2 { // id2 + its duplicate
		t.Fatalf("list after delete len = %d, want 2", len(rest))
	}
	gone, err := http.Get(ts.URL + "/api/sessions/" + id1)
	if err != nil {
		t.Fatalf("get deleted: %v", err)
	}
	gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted session status = %d, want 404", gone.StatusCode)
	}
	appGone, err := http.Get(fmt.Sprintf("%s/app/%s/", ts.URL, id1))
	if err != nil {
		t.Fatalf("deleted app: %v", err)
	}
	appGone.Body.Close()
	if appGone.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted app status = %d, want 404", appGone.StatusCode)
	}

	// Mutations on a foreign/missing session: 404 / 400.
	postJSON(t, ts.URL+"/api/sessions/nope/rename", map[string]string{"name": "x"}, 404)
	postJSON(t, ts.URL+"/api/sessions/nope/pin", map[string]bool{"pinned": true}, 404)
	dupMissing, err := http.Post(ts.URL+"/api/sessions/nope/duplicate", "application/json", nil)
	if err != nil {
		t.Fatalf("duplicate missing: %v", err)
	}
	dupMissing.Body.Close()
	if dupMissing.StatusCode != http.StatusNotFound {
		t.Fatalf("duplicate missing status = %d, want 404", dupMissing.StatusCode)
	}
	postJSON(t, ts.URL+"/api/sessions/"+id2+"/rename", map[string]string{"name": "  "}, 400)
}
