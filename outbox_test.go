package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineWritesQueueAndFlush(t *testing.T) {
	f, srv, be := setupGitHub(t, `{"github":{"repo":"acme/shop"}}`)
	logger := log.New(io.Discard, "", 0)
	if err := cmdStart(be, "1"); err != nil {
		t.Fatal(err)
	}
	online := srv.URL
	be.rc.base = "http://127.0.0.1:1" // nothing listens here: offline

	if err := cmdNote(be, "written offline"); err != nil {
		t.Fatalf("note offline should queue, got %v", err)
	}
	if err := cmdFinish(be, "done", "finished offline"); err != nil {
		t.Fatalf("done offline should queue, got %v", err)
	}
	if all, _ := loadSessions(); len(all) != 0 {
		t.Fatal("the session should close so the agent can move on")
	}
	if n := len(pendingOutbox()); n != 2 {
		t.Fatalf("queued = %d, want 2", n)
	}
	flushOutbox(be, logger) // still offline: nothing sent, order kept
	if n := len(pendingOutbox()); n != 2 || len(f.issues[1].comments) != 0 {
		t.Fatalf("offline flush changed things: queued %d, comments %d", n, len(f.issues[1].comments))
	}

	be.rc.base = online
	flushOutbox(be, logger)
	if n := len(pendingOutbox()); n != 0 {
		t.Fatalf("still queued after reconnecting: %d", n)
	}
	cs := f.issues[1].comments
	if len(cs) != 2 || !strings.Contains(cs[0]["body"], "written offline") || !strings.Contains(cs[1]["body"], "Done: finished offline") {
		t.Fatalf("comments sent out of order or missing: %v", cs)
	}
	if f.issues[1].state != "closed" {
		t.Fatalf("issue state after replay = %s", f.issues[1].state)
	}
}

func TestRejectedQueuedWriteIsSetAside(t *testing.T) {
	_, _, be := setupGitHub(t, `{"github":{"repo":"acme/shop"}}`)
	logger := log.New(io.Discard, "", 0)
	if err := cmdStart(be, "1"); err != nil {
		t.Fatal(err)
	}
	s, _ := loadSession()
	if err := enqueue("note", "", "will be rejected", s); err != nil {
		t.Fatal(err)
	}
	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
	}))
	defer reject.Close()
	be.rc.base = reject.URL
	for i := 0; i < maxOutboxAttempts; i++ {
		flushOutbox(be, logger)
	}
	failed, _ := filepath.Glob(filepath.Join(outboxDir(), "failed", "*.json"))
	if len(pendingOutbox()) != 0 || len(failed) != 1 {
		t.Fatalf("pending %d, failed %d", len(pendingOutbox()), len(failed))
	}
	out := captureStdout(t, func() { _ = cmdStatus() })
	if !strings.Contains(out, "rejected: 1") {
		t.Fatalf("status should report the rejected write:\n%s", out)
	}
}

func TestRealErrorsAreNotQueued(t *testing.T) {
	_, _, be := setupGitHub(t, `{"github":{"repo":"acme/shop"}}`)
	if err := cmdStart(be, "1"); err != nil {
		t.Fatal(err)
	}
	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Resource not accessible"}`, http.StatusForbidden)
	}))
	defer reject.Close()
	be.rc.base = reject.URL
	if err := cmdNote(be, "x"); err == nil || len(pendingOutbox()) != 0 {
		t.Fatalf("a 403 must fail now, not queue: err=%v queued=%d", err, len(pendingOutbox()))
	}
}
