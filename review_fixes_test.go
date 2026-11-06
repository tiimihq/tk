package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Regression tests for the five issues found in the 2026-11-02 code review.

// countingBackend is the local backend with postCost counted and slowed down,
// which widens the window in which two syncs would interleave.
type countingBackend struct {
	*localBackend
	posts atomic.Int32
}

func (c *countingBackend) postCost(s *session, e map[string]any) error {
	c.posts.Add(1)
	time.Sleep(20 * time.Millisecond)
	return c.localBackend.postCost(s, e)
}

func writeUsageLog(t *testing.T, dir string, at time.Time, ids ...string) {
	t.Helper()
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, `{"type":"assistant","requestId":"req_%s","timestamp":"%s","message":{"id":"%s","model":"claude-sonnet-5","usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":10,"output_tokens":5}}}`+"\n",
			id, at.UTC().Format("2006-01-02T15:04:05.000Z"), id)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 1. Concurrent syncs of one session must neither lose ledger entries nor post a reply twice.
func TestConcurrentCostSyncKeepsLedger(t *testing.T) {
	home, logs := t.TempDir(), t.TempDir()
	t.Setenv("TK_HOME", home)
	t.Setenv("TK_CLAUDE_LOG_DIR", logs)
	lb, _ := newLocalBackend()
	be := &countingBackend{localBackend: lb}
	is, _ := lb.createIssue("x", "")
	s, err := lb.start(is.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)
	s.SessionID, s.StartedAt = newSessionID(), start.UTC().Format(time.RFC3339)
	writeUsageLog(t, logs, start.Add(10*time.Second), "m1", "m2", "m3")

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := syncCost(be, &s, time.Now().Add(time.Minute)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := be.posts.Load(); n != 1 {
		t.Fatalf("the one batch was posted %d times", n)
	}
	led, _ := loadLedger(sessionKey(&s))
	if len(led.Posted) != 3 || len(led.Pending) != 0 {
		t.Fatalf("ledger lost entries: posted=%d pending=%d", len(led.Posted), len(led.Pending))
	}
}

// 2. Closing a stale session must not delete the newer session that replaced it.
func TestStaleCloseKeepsNewerSession(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	old := session{Key: "LOC-1", SessionID: "s-old", StartedAt: nowRFC3339()}
	newer := session{Key: "LOC-2", SessionID: "s-new", StartedAt: nowRFC3339()}
	if err := saveSession(newer); err != nil {
		t.Fatal(err)
	}
	if err := withSessionLock(func() error { return closeSession(old) }); err != nil {
		t.Fatal(err)
	}
	cur, _ := loadSession()
	if cur == nil || cur.SessionID != "s-new" {
		t.Fatalf("newer session was removed: %+v", cur)
	}
	if _, err := os.Stat(filepath.Join(tkHome(), "closed", "s-old.json")); err != nil {
		t.Fatal("the stale session still needs its final-sync record")
	}
	if err := withSessionLock(func() error { return closeSession(newer) }); err != nil {
		t.Fatal(err)
	}
	if cur, _ := loadSession(); cur != nil {
		t.Fatal("closing the current session should remove it")
	}
}

// 3. Pending work blocks a login to a different connection but not a re-login to the same one.
func TestReloginAllowedForSameConnection(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	if err := linearConnectionCompatible("conn-a"); err != nil {
		t.Fatalf("no pending work: %v", err)
	}
	if err := saveSession(session{Key: "FOC-1", SessionID: "s-1", ConnectionID: "conn-a"}); err != nil {
		t.Fatal(err)
	}
	if err := linearConnectionCompatible("conn-a"); err != nil {
		t.Fatalf("same connection must be allowed to recover a revoked token: %v", err)
	}
	if err := linearConnectionCompatible("conn-b"); err == nil {
		t.Fatal("a different connection must be refused while work is pending")
	}
	if err := withSessionLock(func() error { return closeSession(session{Key: "FOC-1", SessionID: "s-1", ConnectionID: "conn-a"}) }); err != nil {
		t.Fatal(err)
	}
	if err := linearConnectionCompatible("conn-b"); err == nil {
		t.Fatal("a pending final sync must also pin the connection")
	}
	if err := linearConnectionCompatible("conn-a"); err != nil {
		t.Fatal(err)
	}
}

// 4. A refresh that cannot reach Linear is an outage (cache fallback), a rejected one is not.
func TestRefreshOutageIsUnreachable(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	c := testOAuth(t)
	c.ExpiresAt = time.Now().Add(-time.Minute)
	if err := saveOAuth(c); err != nil {
		t.Fatal(err)
	}
	down := newMemoryServer(http.NotFoundHandler())
	down.Close() // its transport now fails every request, like a network outage
	if _, err := refreshOAuth(down.Client(), down.URL, oauthConnection(c)); err == nil || !isUnreachable(err) {
		t.Fatalf("outage during refresh should be unreachable, got %v", err)
	}
	rejected := newMemoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	if _, err := refreshOAuth(rejected.Client(), rejected.URL, oauthConnection(c)); err == nil || isUnreachable(err) {
		t.Fatalf("a rejected refresh must stay an auth error, got %v", err)
	}
}

// 5. Unpriced usage is reported as unknown, never as $0.00.
func TestUnpricedCostIsNotZero(t *testing.T) {
	cases := []struct {
		events []map[string]any
		want   string
	}{
		{[]map[string]any{{"costStatus": "unpriced", "costCents": "0"}}, "1 cost events, not priced"},
		{[]map[string]any{{"costStatus": "estimated", "costCents": "42"}, {"costStatus": "unpriced", "costCents": "0"}}, "~$0.42 API-equivalent + 1 unpriced events"},
		{[]map[string]any{{"costStatus": "estimated", "costCents": "42"}}, "~$0.42 API-equivalent (1 cost events)"},
	}
	for _, c := range cases {
		if got := sumCostEvents(c.events).String(); !strings.HasPrefix(got, c.want) {
			t.Errorf("got %q, want prefix %q", got, c.want)
		}
	}
}
