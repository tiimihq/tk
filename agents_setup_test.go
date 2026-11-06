package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// inProject runs the test inside a fresh project directory with its own TK_HOME and HOME.
func inProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TK_HOME", filepath.Join(dir, ".tkhome"))
	setHome(t, filepath.Join(dir, "home"))
	t.Setenv("TK_MODE", "local")
	t.Setenv("CODEX_HOME", "")
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestEveryRecipeInstallsChecksAndRemoves(t *testing.T) {
	dir := inProject(t)
	// Content that belongs to the user and must survive.
	_ = os.WriteFile("AGENTS.md", []byte("# My project\n\nUse tabs.\n"), 0o644)
	_ = os.MkdirAll(".codex", 0o755)
	_ = os.WriteFile(".codex/config.toml", []byte("model = \"x\"\n\n[features]\nweb = true\n"), 0o644)
	_ = os.WriteFile(".codex/hooks.json", []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`), 0o644)
	_ = os.WriteFile(".aider.conf.yml", []byte("model: x\nread:\n  - CONVENTIONS.md\n"), 0o644)

	for _, r := range recipes() {
		if err := cmdSetup([]string{r.name}, map[string]bool{}); err != nil {
			t.Fatalf("install %s: %v", r.name, err)
		}
		if err := cmdSetup([]string{r.name}, map[string]bool{"check": true}); err != nil && r.name != "codex" {
			t.Fatalf("check %s after install: %v", r.name, err)
		}
		// running it again changes nothing and reports nothing
		before := snapshot(t, dir)
		c, _ := newSetupCtx(map[string]bool{})
		if changed, err := r.install(c); err != nil || len(changed) != 0 {
			t.Fatalf("%s: second install reported changes %v (%v)", r.name, changed, err)
		}
		if after := snapshot(t, dir); after != before {
			t.Fatalf("%s: second install changed files", r.name)
		}
	}
	if !strings.Contains(read(t, "AGENTS.md"), "Use tabs.") || strings.Count(read(t, "AGENTS.md"), tkBegin) != 1 {
		t.Fatalf("AGENTS.md:\n%s", read(t, "AGENTS.md"))
	}
	toml := read(t, ".codex/config.toml")
	if !strings.Contains(toml, "web = true") || !strings.Contains(toml, "hooks = true") || strings.Count(toml, "[features]") != 1 {
		t.Fatalf("config.toml:\n%s", toml)
	}
	var hooks map[string]any
	_ = json.Unmarshal([]byte(read(t, ".codex/hooks.json")), &hooks)
	raw := read(t, ".codex/hooks.json")
	if !strings.Contains(raw, "echo mine") || !strings.Contains(raw, "hook codex SessionStart") || !strings.Contains(raw, "hook codex UserPromptSubmit") {
		t.Fatalf("codex hooks.json:\n%s", raw)
	}
	if c := read(t, ".cursor/hooks.json"); !strings.Contains(c, `"version": 1`) || !strings.Contains(c, "hook cursor postToolUse") {
		t.Fatalf("cursor hooks.json:\n%s", c)
	}
	if !strings.Contains(read(t, ".cursor/rules/tk.mdc"), "alwaysApply: true") {
		t.Fatal("cursor rule missing front matter")
	}
	if a := read(t, ".aider.conf.yml"); !strings.Contains(a, "  - CONVENTIONS.md") || !strings.Contains(a, "  - .aider/TK.md") {
		t.Fatalf("aider conf:\n%s", a)
	}
	if !fileExists("AGENTS.md.bak") {
		t.Fatal("changed files are backed up")
	}

	for _, r := range recipes() {
		if err := cmdSetup([]string{r.name}, map[string]bool{"remove": true}); err != nil {
			t.Fatalf("remove %s: %v", r.name, err)
		}
		if err := cmdSetup([]string{r.name}, map[string]bool{"check": true}); err == nil && r.name != "codex" {
			t.Fatalf("check %s should fail after remove", r.name)
		}
	}
	if a := read(t, "AGENTS.md"); a != "# My project\n\nUse tabs.\n" {
		t.Fatalf("AGENTS.md after remove = %q", a)
	}
	if raw := read(t, ".codex/hooks.json"); !strings.Contains(raw, "echo mine") || strings.Contains(raw, "hook codex") {
		t.Fatalf("codex hooks after remove:\n%s", raw)
	}
	if a := read(t, ".aider.conf.yml"); strings.Contains(a, "TK.md") || !strings.Contains(a, "CONVENTIONS.md") {
		t.Fatalf("aider after remove:\n%s", a)
	}
	for _, f := range []string{".windsurf/rules/tk.md", ".cursor/rules/tk.mdc", ".github/copilot-instructions.md", "GEMINI.md"} {
		if fileExists(f) {
			t.Fatalf("%s should be gone", f)
		}
	}
}

func snapshot(t *testing.T, dir string) string {
	var b strings.Builder
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.Contains(p, ".tkhome") || strings.HasSuffix(p, ".bak") {
			return nil
		}
		fmt.Fprintf(&b, "%s=%s\n", p, read(t, p))
		return nil
	})
	return b.String()
}

func TestGlobalSetupAndRefusals(t *testing.T) {
	dir := inProject(t)
	home := filepath.Join(dir, "home")
	if err := cmdSetup([]string{"codex"}, map[string]bool{"global": true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, filepath.Join(home, ".codex", "hooks.json")), "hook codex SessionStart") || !hasSection(filepath.Join(home, ".codex", "AGENTS.md")) {
		t.Fatal("global codex setup should write under ~/.codex")
	}
	if fileExists(filepath.Join(dir, "AGENTS.md")) {
		t.Fatal("global setup must not touch the project")
	}
	if err := cmdSetup([]string{"windsurf"}, map[string]bool{"global": true}); err == nil {
		t.Fatal("windsurf has no global setup")
	}
	if err := cmdSetup([]string{"nope"}, map[string]bool{}); err == nil {
		t.Fatal("unknown recipe accepted")
	}
	_ = os.MkdirAll(".gemini", 0o755)
	_ = os.WriteFile(".gemini/settings.json", []byte("{not json"), 0o644)
	if err := cmdSetup([]string{"gemini"}, map[string]bool{}); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("broken settings must be left alone: %v", err)
	}
}

func TestInitSetsUpDetectedAgents(t *testing.T) {
	dir := inProject(t)
	bin := filepath.Join(dir, "bin")
	_ = os.MkdirAll(bin, 0o755)
	for _, name := range []string{"codex", "gemini"} { // pretend these are installed
		_ = os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755)
	}
	t.Setenv("PATH", bin)
	if err := cmdInit(map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(".codex/hooks.json") || !fileExists(".gemini/settings.json") || !hasSection("AGENTS.md") {
		t.Fatal("init should set up the detected agents")
	}
	if fileExists(".cursor/hooks.json") || fileExists(".claude/settings.local.json") {
		t.Fatal("init must not set up tools that aren't installed")
	}
}

func runHook(t *testing.T, tool, event string, in map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(in)
	var out bytes.Buffer
	cmdAgentHook(tool, event, bytes.NewReader(b), &out)
	if strings.TrimSpace(out.String()) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("%s %s printed non-JSON: %q", tool, event, out.String())
	}
	return m
}

func TestAgentHooks(t *testing.T) {
	dir := inProject(t)
	lb, _ := newLocalBackend()
	lb.createIssue("Add pagination", "")
	transcript := filepath.Join(dir, "rollout.jsonl")
	_ = os.WriteFile(transcript, nil, 0o600)
	in := map[string]any{"session_id": "codex-1", "transcript_path": transcript, "cwd": dir}

	// Codex: brief at start; nothing on a normal prompt; brief once after compaction.
	out := runHook(t, "codex", "SessionStart", in)
	ctx, _ := out["hookSpecificOutput"].(map[string]any)
	if ctx["hookEventName"] != "SessionStart" || !strings.Contains(fmt.Sprint(ctx["additionalContext"]), "LOC-1") {
		t.Fatalf("codex SessionStart = %v", out)
	}
	if out := runHook(t, "codex", "UserPromptSubmit", in); out != nil {
		t.Fatalf("no refresh without compaction, got %v", out)
	}
	runHook(t, "codex", "PostCompact", in)
	if out := runHook(t, "codex", "UserPromptSubmit", in); out == nil {
		t.Fatal("first prompt after compaction should get the brief")
	}
	if out := runHook(t, "codex", "UserPromptSubmit", in); out != nil {
		t.Fatal("brief only once after compaction")
	}

	// Cursor: additional_context at start; postToolUse is {} unless compacted.
	cin := map[string]any{"conversation_id": "cur-1", "workspace_roots": []string{dir}}
	if out := runHook(t, "cursor", "sessionStart", cin); !strings.Contains(fmt.Sprint(out["additional_context"]), "tk") {
		t.Fatalf("cursor sessionStart = %v", out)
	}
	if out := runHook(t, "cursor", "postToolUse", cin); len(out) != 0 {
		t.Fatalf("postToolUse should be a no-op, got %v", out)
	}
	runHook(t, "cursor", "preCompact", cin)
	if out := runHook(t, "cursor", "postToolUse", cin); out["additional_context"] == nil {
		t.Fatal("postToolUse after compaction should restore the brief")
	}

	// Gemini: Claude-style envelope.
	if out := runHook(t, "gemini", "SessionStart", map[string]any{"session_id": "g-1", "cwd": dir}); out["hookSpecificOutput"] == nil {
		t.Fatalf("gemini SessionStart = %v", out)
	}

	// Garbage input never breaks the agent.
	var buf bytes.Buffer
	cmdAgentHook("codex", "SessionStart", strings.NewReader("not json"), &buf)

	// tk start inside the Codex session links its exact log.
	if err := cmdStart(lb, "LOC-1"); err != nil {
		t.Fatal(err)
	}
	s, _ := loadSession()
	if s.AgentTool != "codex" || s.TranscriptPath != transcript || s.ClaudeSessionID != "codex-1" {
		t.Fatalf("session link = %+v", s)
	}
}

func TestGeminiUsage(t *testing.T) {
	dir := inProject(t)
	root := filepath.Join(dir, "gemini-tmp")
	t.Setenv("GEMINI_DATA_DIR", root)
	sum := sha256Hex(dir)
	chats := filepath.Join(root, sum, "chats")
	_ = os.MkdirAll(chats, 0o755)
	now := time.Now().UTC()
	file := map[string]any{"sessionId": "g1", "messages": []any{
		map[string]any{"id": "u1", "type": "user", "timestamp": now.Format(time.RFC3339Nano)},
		map[string]any{"id": "m1", "type": "gemini", "model": "gemini-3-pro", "timestamp": now.Format(time.RFC3339Nano),
			"tokens": map[string]any{"input": 1000, "output": 50, "cached": 600, "thoughts": 20, "total": 1070}},
	}}
	b, _ := json.Marshal(file)
	_ = os.WriteFile(filepath.Join(chats, "session-1.json"), b, 0o644)
	line, _ := json.Marshal(map[string]any{"sessionId": "g2", "id": "m2", "type": "gemini", "model": "gemini-3-flash", "timestamp": now.Format(time.RFC3339Nano),
		"tokens": map[string]any{"input_tokens": 10, "output_tokens": 5}})
	_ = os.WriteFile(filepath.Join(chats, "session-2.jsonl"), append(line, '\n'), 0o644)

	msgs, err := readGeminiUsage(geminiChatFiles(filepath.Join(dir, "sub"), now.Add(-time.Hour)), now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(msgs) != 2 {
		t.Fatalf("gemini msgs = %+v, %v", msgs, err)
	}
	m := msgs[0]
	if m.Key != "gemini:g1:m1" && msgs[1].Key == "gemini:g1:m1" {
		m = msgs[1]
	}
	if m.Input != 400 || m.CacheRead != 600 || m.Output != 70 || m.Provider != "google" {
		t.Fatalf("gemini token mapping = %+v", m)
	}
}

func TestOpenCodeUsage(t *testing.T) {
	dir := inProject(t)
	root := filepath.Join(dir, "opencode")
	t.Setenv("OPENCODE_DATA_DIR", root)
	now := time.Now().UTC()
	msg := func(id, provider, cwd string) map[string]any {
		return map[string]any{"id": id, "role": "assistant", "modelID": "claude-sonnet-5", "providerID": provider,
			"path": map[string]any{"cwd": cwd, "root": cwd}, "time": map[string]any{"created": now.UnixMilli()},
			"tokens": map[string]any{"input": 100, "output": 40, "reasoning": 10, "cache": map[string]any{"read": 900, "write": 50}}}
	}
	// legacy JSON files
	_ = os.MkdirAll(filepath.Join(root, "storage", "message", "ses1"), 0o755)
	for _, m := range []map[string]any{msg("msg_a", "anthropic", dir), msg("msg_other", "anthropic", "/elsewhere"), msg("msg_claude", "claude-agent", dir)} {
		b, _ := json.Marshal(m)
		_ = os.WriteFile(filepath.Join(root, "storage", "message", "ses1", m["id"].(string)+".json"), b, 0o644)
	}
	// SQLite database, when the sqlite3 CLI is available
	if sqlite, err := exec.LookPath("sqlite3"); err == nil {
		b, _ := json.Marshal(msg("msg_db", "openai", filepath.Join(dir, "sub")))
		sql := fmt.Sprintf("CREATE TABLE message (id TEXT, session_id TEXT, time_created INTEGER, data TEXT); INSERT INTO message VALUES ('msg_db','ses2',%d,'%s');",
			now.UnixMilli(), strings.ReplaceAll(string(b), "'", "''"))
		if out, err := exec.Command(sqlite, filepath.Join(root, "opencode.db"), sql).CombinedOutput(); err != nil {
			t.Fatalf("sqlite3: %v %s", err, out)
		}
	}
	msgs, err := readOpenCodeUsage(dir, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := 1
	if _, err := exec.LookPath("sqlite3"); err == nil {
		want = 2
	}
	if len(msgs) != want {
		t.Fatalf("want %d messages (other projects and claude-agent rows skipped), got %+v", want, msgs)
	}
	for _, m := range msgs {
		if m.Input != 100 || m.CacheRead != 900 || m.CacheWrite != 50 || m.Output != 50 {
			t.Fatalf("opencode token mapping = %+v", m)
		}
	}
	for _, m := range msgs {
		p, _ := priceCents(m)
		if m.Provider == "anthropic" && p == "" {
			t.Fatal("anthropic models in OpenCode should be priced")
		}
		if m.Provider == "openai" && p != "" {
			t.Fatal("a Claude model reported under another provider must not get Anthropic's price")
		}
	}
}

func TestCodexProjectTrust(t *testing.T) {
	dir := inProject(t)
	home := filepath.Join(dir, "home")
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	if codexProjectTrusted(home, dir) {
		t.Fatal("no config: not trusted")
	}
	cfg := fmt.Sprintf("[projects.\"/other\"]\ntrust_level = \"trusted\"\n\n[projects.\"%s\"]\ntrust_level = \"trusted\"\n", realPath(dir))
	_ = os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(cfg), 0o600)
	if !codexProjectTrusted(home, dir) {
		t.Fatal("trusted project not recognised")
	}
	if err := cmdSetup([]string{"codex"}, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if err := cmdSetup([]string{"codex"}, map[string]bool{"check": true}); err != nil {
		t.Fatalf("check with a trusted project: %v", err)
	}
}
