package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCodexRollout writes a rollout in the shape Codex CLI 0.166 logs: session_meta,
// turn_context, then per response a token_usage_record followed by a token_count
// event repeating the same numbers (which must not be counted again).
func writeCodexRollout(t *testing.T, root, cwd, name string, at time.Time, responses ...string) {
	t.Helper()
	dir := filepath.Join(root, at.Format("2006"), at.Format("01"), at.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ts := func(d time.Duration) string { return at.Add(d).UTC().Format("2006-01-02T15:04:05.000Z") }
	var b strings.Builder
	fmt.Fprintf(&b, `{"timestamp":"%s","type":"session_meta","payload":{"id":"%s","cwd":"%s","cli_version":"0.166.0"}}`+"\n", ts(0), name, cwd)
	fmt.Fprintf(&b, `{"timestamp":"%s","type":"turn_context","payload":{"model":"gpt-6-astra","cwd":"%s"}}`+"\n", ts(time.Second), cwd)
	for i, id := range responses {
		u := `{"input_tokens":18000,"cached_input_tokens":12000,"cache_write_input_tokens":0,"output_tokens":100,"reasoning_output_tokens":0,"total_tokens":18100}`
		fmt.Fprintf(&b, `{"timestamp":"%s","type":"token_usage_record","payload":{"response_id":"%s","usage":%s}}`+"\n", ts(time.Duration(2+i)*time.Second), id, u)
		fmt.Fprintf(&b, `{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":%s}}}`+"\n", ts(time.Duration(2+i)*time.Second), u)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-"+name+".jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexUsageSync(t *testing.T) {
	home, codex, project := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("TK_HOME", home)
	t.Setenv("TK_CODEX_SESSIONS", codex)
	t.Setenv("TK_CLAUDE_LOG_DIR", t.TempDir()) // no Claude usage
	start := time.Now().Add(-5 * time.Minute)
	writeCodexRollout(t, codex, filepath.Join(project, "sub"), "here", start.Add(time.Minute), "resp_a", "resp_b")
	writeCodexRollout(t, codex, t.TempDir(), "elsewhere", start.Add(time.Minute), "resp_x")
	writeCodexRollout(t, codex, project, "before", start.Add(-time.Hour), "resp_old")

	lb, _ := newLocalBackend()
	is, _ := lb.createIssue("x", "")
	s, err := lb.start(is.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	s.SessionID, s.StartedAt, s.Cwd = newSessionID(), start.UTC().Format(time.RFC3339), project

	line, err := syncCost(lb, &s, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "2 replies (2 new)") || !strings.Contains(line, "in 12000 | cache read 24000") || !strings.Contains(line, "~$0.15") {
		t.Fatalf("cost line = %q", line)
	}
	tot, _ := lb.costSummary(is.Identifier)
	if tot.Events != 1 || tot.Unpriced != 0 || tot.Cents != 15.4 { // 12000×$10 + 24000×$1 + 200×$50 per million
		t.Fatalf("want one priced openai batch of 15.4 cents, got %+v", tot)
	}
	var ev map[string]any
	_ = lb.withStore(false, func(st *localStore) error { ev = st.Issues[0].CostEvents[0]; return nil })
	if ev["provider"] != "openai" || ev["model"] != "gpt-6-astra" || ev["costStatus"] != "estimated" || ev["costCents"] != "15.400000" {
		t.Fatalf("event = %v", ev)
	}
	if line, _ := syncCost(lb, &s, time.Now().Add(time.Minute)); !strings.Contains(line, "(0 new)") {
		t.Fatalf("repeat sync posted again: %q", line)
	}

	// A session linked to one Claude Code transcript is a Claude session: Codex logs are not its usage.
	s2 := s
	transcript := filepath.Join(t.TempDir(), "claude-1.jsonl")
	_ = os.WriteFile(transcript, nil, 0o600)
	s2.SessionID, s2.ClaudeSessionID, s2.TranscriptPath = newSessionID(), "claude-1", transcript
	msgs, _, err := sessionUsage(&s2, start, time.Now().Add(time.Minute))
	if err != nil || len(msgs) != 0 {
		t.Fatalf("linked Claude session read Codex usage: %d msgs, %v", len(msgs), err)
	}
}

// The real log from the pilot, when present on this machine, must parse.
func TestCodexUsageRealLogIfPresent(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "11", "02", "rollout-2026-11-02T15-49-*.jsonl"))
	if len(files) == 0 {
		t.Skip("no pilot Codex log on this machine")
	}
	msgs, err := readCodexUsage(files, time.Time{}, time.Now())
	if err != nil || len(msgs) == 0 {
		t.Fatalf("parsed %d responses, err %v", len(msgs), err)
	}
	for _, m := range msgs {
		if m.Provider != "openai" || m.Model == "" || m.Input < 0 || m.Output <= 0 {
			t.Fatalf("bad record %+v", m)
		}
	}
	t.Logf("pilot Codex log: %d responses, model %s", len(msgs), msgs[0].Model)
}
