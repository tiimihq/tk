package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssueFromBranch(t *testing.T) {
	cases := map[string][2]string{
		"prateek/eng-142-fix-refund-rounding": {"ENG-142", "fix refund rounding"},
		"ENG-142":                             {"ENG-142", ""},
		"feature/PROJ-7_add_sso":              {"PROJ-7", "add sso"},
		"123-fix-login":                       {"#123", "fix login"},
		"alice/45-cursor-pagination":          {"#45", "cursor pagination"},
		"feature-2":                           {"", ""},
		"release-2026":                        {"", ""},
		"fix-12-things":                       {"", ""},
		"v1-2":                                {"", ""},
		"main":                                {"", ""},
		"HEAD":                                {"", ""},
		"dependabot/npm_and_yarn/lodash-4.17": {"", ""},
		"hotfix/feat-9-crash":                 {"", ""},
		"2026-11-07-notes":                    {"", ""},
	}
	for branch, want := range cases {
		k, title := issueFromBranch(branch)
		if k != want[0] || title != want[1] {
			t.Errorf("%q → %q %q, want %q %q", branch, k, title, want[0], want[1])
		}
	}
}

func TestReceiptGroupsByIssueAcrossAgents(t *testing.T) {
	claude, codex := t.TempDir(), t.TempDir()
	t.Setenv("TK_CLAUDE_PROJECTS", claude)
	t.Setenv("TK_CODEX_SESSIONS", codex)
	t.Setenv("TK_HOME", t.TempDir())
	now := time.Now().UTC()
	proj := filepath.Join(claude, "-work-shop")
	_ = os.MkdirAll(proj, 0o700)
	line := func(id, branch string, at time.Time) string {
		b, _ := json.Marshal(map[string]any{
			"type": "assistant", "requestId": "r" + id, "timestamp": at.Format(time.RFC3339Nano),
			"gitBranch": branch, "cwd": "/work/shop", "sessionId": "s-" + branch,
			"message": map[string]any{"id": id, "model": "claude-sonnet-4-5",
				"usage": map[string]any{"input_tokens": 1000000, "output_tokens": 0}},
		})
		return string(b) + "\n"
	}
	body := line("a", "eng-142-fix-refunds", now.Add(-2*time.Hour)) +
		line("a", "eng-142-fix-refunds", now.Add(-2*time.Hour)) + // same reply logged twice: counted once
		line("b", "HEAD", now.Add(-time.Hour)) +
		line("c", "", now.Add(-time.Hour)) +
		line("old", "eng-1-old", now.AddDate(0, 0, -60)) // outside the window
	_ = os.WriteFile(filepath.Join(proj, "s.jsonl"), []byte(body), 0o600)
	writeCodexRollout(t, codex, "/work/shop", "cx", now.Add(-30*time.Minute), "resp_1")
	// the fixture's session_meta has no git block; give this rollout an issue branch
	files, _ := filepath.Glob(filepath.Join(codex, "*", "*", "*", "rollout-cx.jsonl"))
	raw, _ := os.ReadFile(files[0])
	raw = []byte(strings.Replace(string(raw), `"cli_version"`, `"git":{"branch":"prateek/ENG-142-fix-refunds"},"cli_version"`, 1))
	_ = os.WriteFile(files[0], raw, 0o600)

	r, err := buildReceipt(now.AddDate(0, 0, -30), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if r.IssueRows != 1 || r.Rows[0].Key != "ENG-142" || r.Rows[0].Title != "fix refunds" {
		t.Fatalf("issue row: %+v", r.Rows[0])
	}
	if got := r.Rows[0].AgentLst; len(got) != 2 {
		t.Fatalf("ENG-142 should combine Claude Code and Codex: %v", got)
	}
	if len(r.Rows) != 2 || r.Rows[1].Branch != "" || r.Rows[1].Replies != 2 {
		t.Fatalf("HEAD and empty branch should merge into one row: %+v", r.Rows)
	}
	if r.Replies != 4 {
		t.Fatalf("replies = %d, want 4 (dupe and old one excluded)", r.Replies)
	}
	out := formatReceipt(r, "last 30 days", false)
	for _, want := range []string{"Your coding agents, last 30 days:", "ENG-142", "fix refunds", "Claude Code+Codex", "on no branch"} {
		if !strings.Contains(out, want) {
			t.Fatalf("receipt missing %q:\n%s", want, out)
		}
	}
}
