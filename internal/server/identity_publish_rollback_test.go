package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A failed durable publish must preserve the public snapshot while members
// keep previewing their newer draft. Registration, editing, publication and
// assertions all use HTTP; only email delivery and RecordPublish are replaced.
func TestIdentityHTTPLocalPublishRecordFailurePreservesPublicSnapshot(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DSN for Postgres HTTP integration")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := identity.InitSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store := &failingRecordStore{Store: session.NewStore(dir)}
	mail := &capturedMail{}
	srv := server.New(server.Config{
		WorkspaceDir: dir, Store: store, Identity: identity.New(pool), Mailer: mail,
		Prov:              provider.NewTemplate(),
		TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	nonce := uint64(time.Now().UnixNano())
	h := boundaryHTTP{
		url: ts.URL, mail: mail,
		clientIP: netip.MustParseAddr(fmt.Sprintf("2001:db8:4:0:%x:%x:%x:%x",
			uint16(nonce>>48), uint16(nonce>>32), uint16(nonce>>16), uint16(nonce))).String(),
	}
	owner := h.register(t, "owner", fmt.Sprintf("publish-rollback-%d@example.test", nonce))
	anon := boundaryActor{name: "anonymous", client: &http.Client{Timeout: 10 * time.Second}}
	var created struct {
		ID string `json:"sessionId"`
	}
	if err := json.Unmarshal(h.request(t, owner, http.MethodPost, "/api/sessions",
		`{"prompt":"一家名为「PublishedShop」的咖啡电商网站"}`, http.StatusOK), &created); err != nil || created.ID == "" {
		t.Fatalf("create project: id=%q err=%v", created.ID, err)
	}
	id := created.ID
	projectPath := "/api/sessions/" + id
	appPath := "/app/" + id + "/"
	h.waitReady(t, owner, id)
	h.request(t, owner, http.MethodPost, projectPath+"/publish", "", http.StatusAccepted)
	h.waitReady(t, owner, id)
	oldHTML := h.request(t, anon, http.MethodGet, appPath, "", http.StatusOK)
	oldBundle := h.request(t, anon, http.MethodGet, appPath+"main.js", "", http.StatusOK)
	oldHistory := h.request(t, owner, http.MethodGet, projectPath+"/publish", "", http.StatusOK)
	var published struct {
		Publishes []session.Publish `json:"publishes"`
		Published bool              `json:"published"`
	}
	if err := json.Unmarshal(oldHistory, &published); err != nil || len(published.Publishes) != 1 || !published.Published {
		t.Fatalf("initial publication did not complete: history=%s err=%v", oldHistory, err)
	}

	h.request(t, owner, http.MethodPost, projectPath+"/chat", `{"message":"把店名改为「RollbackDraft」"}`, http.StatusOK)
	h.waitReady(t, owner, id)
	newDraft := h.request(t, owner, http.MethodGet, appPath+"main.js", "", http.StatusOK)
	if bytes.Equal(newDraft, oldBundle) || !bytes.Contains(newDraft, []byte("RollbackDraft")) {
		t.Fatal("member preview did not incorporate the unpublished edit")
	}
	store.fail.Store(true)
	h.request(t, owner, http.MethodPost, projectPath+"/publish", "", http.StatusAccepted)
	deadline := time.Now().Add(10 * time.Second)
	reported := false
	for time.Now().Before(deadline) {
		var project struct {
			Status string `json:"status"`
			Events []struct {
				Type    string `json:"type"`
				Payload struct {
					Message string `json:"message"`
				} `json:"payload"`
			} `json:"events"`
		}
		if err := json.Unmarshal(h.request(t, owner, http.MethodGet, projectPath, "", http.StatusOK), &project); err != nil {
			t.Fatal(err)
		}
		if project.Status == "ready" {
			for _, event := range project.Events {
				if event.Type == "error" && strings.Contains(event.Payload.Message, "injected publish record failure") {
					reported = true
				}
			}
		}
		if reported {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reported {
		t.Fatal("failed publication did not return to ready with a visible error")
	}
	if got := h.request(t, anon, http.MethodGet, appPath, "", http.StatusOK); !bytes.Equal(got, oldHTML) {
		t.Fatal("failed publication changed the anonymous HTML snapshot")
	}
	if got := h.request(t, anon, http.MethodGet, appPath+"main.js", "", http.StatusOK); !bytes.Equal(got, oldBundle) {
		t.Fatal("failed publication exposed the new draft to anonymous visitors")
	}
	if got := h.request(t, owner, http.MethodGet, appPath+"main.js", "", http.StatusOK); !bytes.Equal(got, newDraft) {
		t.Fatal("failed publication changed the member's draft preview")
	}
	if got := h.request(t, owner, http.MethodGet, projectPath+"/publish", "", http.StatusOK); !bytes.Equal(got, oldHistory) {
		t.Fatal("failed publication changed the durable publication history")
	}
}
