package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestSeveralSessionsInWorktrees(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TK_HOME", filepath.Join(root, "tkhome"))
	t.Setenv("TK_CLAUDE_LOG_DIR", t.TempDir())
	t.Setenv("TK_CODEX_SESSIONS", t.TempDir())
	t.Setenv("TK_ISSUE", "")
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	wtA, wtB := filepath.Join(root, "repo-a"), filepath.Join(root, "repo-b")
	_ = os.MkdirAll(filepath.Join(wtA, "sub"), 0o755)
	_ = os.MkdirAll(wtB, 0o755)

	lb, _ := newLocalBackend()
	lb.createIssue("Pagination", "")
	lb.createIssue("Refunds", "")
	lb.createIssue("Docs", "")

	chdir(t, wtA)
	if err := cmdStart(lb, "LOC-1"); err != nil {
		t.Fatal(err)
	}
	if err := cmdStart(lb, "LOC-3"); err == nil || !strings.Contains(err.Error(), "already open in this folder") {
		t.Fatalf("second session in the same folder: %v", err)
	}
	chdir(t, wtB)
	if err := cmdStart(lb, "LOC-1"); err == nil || !strings.Contains(err.Error(), "already has an open session") {
		t.Fatalf("same issue twice: %v", err)
	}
	if err := cmdStart(lb, "LOC-2"); err != nil {
		t.Fatal(err)
	}

	// Notes from each worktree (and a subfolder) land on that worktree's issue.
	chdir(t, filepath.Join(wtA, "sub"))
	if err := cmdNote(lb, "from A"); err != nil {
		t.Fatal(err)
	}
	chdir(t, wtB)
	if err := cmdNote(lb, "from B"); err != nil {
		t.Fatal(err)
	}
	a, _ := lb.issueDetailOf("LOC-1")
	b, _ := lb.issueDetailOf("LOC-2")
	if len(a.Comments) != 1 || a.Comments[0].Body != "from A" || len(b.Comments) != 1 || b.Comments[0].Body != "from B" {
		t.Fatalf("notes went to the wrong issue: A=%+v B=%+v", a.Comments, b.Comments)
	}

	// From an unrelated folder with two sessions open, tk can't guess.
	chdir(t, root)
	if err := cmdNote(lb, "where?"); err == nil || !strings.Contains(err.Error(), "set TK_ISSUE") {
		t.Fatalf("ambiguous folder: %v", err)
	}
	t.Setenv("TK_ISSUE", "LOC-2")
	if err := cmdNote(lb, "explicit"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TK_ISSUE", "")

	// Finishing B leaves A open.
	chdir(t, wtB)
	if err := cmdFinish(lb, "done", "refunds done"); err != nil {
		t.Fatal(err)
	}
	all, _ := loadSessions()
	if len(all) != 1 || all[0].Key != "LOC-1" {
		t.Fatalf("after finishing B: %+v", all)
	}
	// With one session left, tk works from any folder, as before.
	chdir(t, root)
	if err := cmdNote(lb, "still reaches A"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { _ = cmdStatus() })
	if !strings.Contains(out, "session: LOC-1") || !strings.Contains(out, wtA) {
		t.Fatalf("status:\n%s", out)
	}
}

func TestLegacySessionFileMigrates(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	_ = os.MkdirAll(tkHome(), 0o700)
	raw, _ := json.Marshal(session{Key: "LOC-7", IssueID: "x", StartedAt: nowRFC3339(), Cwd: "/somewhere"})
	_ = os.WriteFile(legacySessionPath(), raw, 0o600)
	all, err := loadSessions()
	if err != nil || len(all) != 1 || all[0].Key != "LOC-7" || all[0].SessionID == "" {
		t.Fatalf("migrated = %+v, %v", all, err)
	}
	if fileExists(legacySessionPath()) {
		t.Fatal("legacy file should be gone after migration")
	}
}

func TestClosingASessionOnlyRemovesItsOwnFile(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	one := session{Key: "LOC-1", SessionID: "s-1", StartedAt: nowRFC3339(), Cwd: "/a"}
	two := session{Key: "LOC-2", SessionID: "s-2", StartedAt: nowRFC3339(), Cwd: "/b"}
	_ = saveSession(one)
	_ = saveSession(two)
	_ = withSessionLock(func() error { return closeSession(one) })
	_ = withSessionLock(func() error { return closeSession(one) }) // a stale second close is harmless
	all, _ := loadSessions()
	if len(all) != 1 || all[0].SessionID != "s-2" || sessionOpen("s-1") {
		t.Fatalf("after closing s-1 twice: %+v", all)
	}
}
