package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Usage readers for Gemini CLI and OpenCode, and sessionUsage, which picks the
// right reader for a tk session. Formats follow ccusage's documented adapters
// (github.com/ryoppippi/ccusage), since both tools change their logs often.

// ---- Gemini CLI: ~/.gemini/tmp/<project hash>/chats/*.json[l] ----

func geminiRoot() string {
	if d := os.Getenv("GEMINI_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "tmp")
}

// geminiChatFiles returns chat logs of the project at cwd (or a parent):
// Gemini names each project's folder by the SHA-256 of its root path.
func geminiChatFiles(cwd string, since time.Time) []string {
	var out []string
	for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
		sum := sha256.Sum256([]byte(dir))
		for _, pat := range []string{"*.json", "*.jsonl"} {
			files, _ := filepath.Glob(filepath.Join(geminiRoot(), hex.EncodeToString(sum[:]), "chats", pat))
			for _, f := range files {
				if info, err := os.Stat(f); err == nil && !info.ModTime().Before(since.Add(-time.Minute)) {
					out = append(out, f)
				}
			}
		}
		if filepath.Dir(dir) == dir {
			return out
		}
	}
}

func readGeminiUsage(files []string, since, until time.Time) ([]claudeMessage, error) {
	seen := map[string]bool{}
	var out []claudeMessage
	add := func(sessionID string, m map[string]any) {
		if m["type"] != "gemini" {
			return
		}
		tok, _ := m["tokens"].(map[string]any)
		if tok == nil {
			return
		}
		ts, _ := m["timestamp"].(string)
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || at.Before(since) || at.After(until) {
			return
		}
		id, _ := m["id"].(string)
		key := "gemini:" + sessionID + ":" + firstNonEmpty(id, ts)
		if seen[key] {
			return
		}
		seen[key] = true
		input := numAt(tok, "input", "prompt", "input_tokens", "prompt_tokens")
		cached := numAt(tok, "cached", "cached_tokens")
		output := numAt(tok, "output", "candidates", "output_tokens", "candidates_tokens")
		thoughts := numAt(tok, "thoughts", "reasoning", "thoughts_tokens", "reasoning_tokens")
		model, _ := m["model"].(string)
		out = append(out, claudeMessage{Key: key, Provider: "google", Model: firstNonEmpty(model, "gemini-unknown-model"), At: at,
			Input: max(0, input-cached), CacheRead: cached, Output: output + thoughts}) // thoughts are billed as output
	}
	addRecord := func(rec map[string]any) {
		sid, _ := rec["sessionId"].(string)
		if sid == "" {
			sid, _ = rec["session_id"].(string)
		}
		if msgs, ok := rec["messages"].([]any); ok {
			for _, x := range msgs {
				if m, ok := x.(map[string]any); ok {
					add(sid, m)
				}
			}
			return
		}
		add(sid, rec)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(f, ".jsonl") {
			sc := bufio.NewScanner(bytes.NewReader(raw))
			sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
			sid := strings.TrimSuffix(filepath.Base(f), ".jsonl")
			for sc.Scan() {
				var rec map[string]any
				if json.Unmarshal(sc.Bytes(), &rec) == nil {
					if rec["sessionId"] == nil && rec["session_id"] == nil {
						rec["sessionId"] = sid
					}
					addRecord(rec)
				}
			}
			continue
		}
		var rec map[string]any
		if json.Unmarshal(raw, &rec) == nil {
			addRecord(rec)
		}
	}
	return out, nil
}

func numAt(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case string:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// ---- OpenCode: SQLite database (read with the sqlite3 CLI) plus legacy JSON ----

func opencodeRoot() string {
	if d := os.Getenv("OPENCODE_DATA_DIR"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "opencode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode")
}

// opencodeMessage is the part of an OpenCode assistant message tk uses.
type opencodeMessage struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionID"`
	Role       string `json:"role"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Model      *struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
	} `json:"model"`
	Path *struct {
		Cwd  string `json:"cwd"`
		Root string `json:"root"`
	} `json:"path"`
	Time *struct {
		Created float64 `json:"created"` // ms
	} `json:"time"`
	Tokens *struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

// readOpenCodeUsage returns assistant messages from OpenCode sessions run in cwd
// (or inside it). Messages without a recorded directory can't be attributed and
// are skipped. Rows OpenCode ran through Claude Code (provider claude-agent) are
// skipped too: Claude Code's own log already counts them.
func readOpenCodeUsage(cwd string, since, until time.Time) ([]claudeMessage, error) {
	var raws [][]byte
	dbs, _ := filepath.Glob(filepath.Join(opencodeRoot(), "opencode*.db"))
	if sqlite, err := exec.LookPath("sqlite3"); err == nil {
		for _, db := range dbs {
			for _, table := range []string{"message", "session_message"} {
				q := fmt.Sprintf("SELECT id, session_id, data FROM %s WHERE time_created >= %d", table, since.Add(-time.Minute).UnixMilli())
				out, err := exec.Command(sqlite, "-readonly", "-json", db, q).Output()
				if err != nil || len(bytes.TrimSpace(out)) == 0 {
					continue // table or column missing in this schema version
				}
				var rows []struct {
					ID        string `json:"id"`
					SessionID string `json:"session_id"`
					Data      string `json:"data"`
				}
				if json.Unmarshal(out, &rows) != nil {
					continue
				}
				for _, r := range rows {
					var m map[string]any
					if json.Unmarshal([]byte(r.Data), &m) != nil {
						continue
					}
					m["id"], m["sessionID"] = r.ID, r.SessionID
					b, _ := json.Marshal(m)
					raws = append(raws, b)
				}
			}
		}
	}
	legacy, _ := filepath.Glob(filepath.Join(opencodeRoot(), "storage", "message", "*", "*.json"))
	for _, f := range legacy {
		if info, err := os.Stat(f); err != nil || info.ModTime().Before(since.Add(-time.Minute)) {
			continue
		}
		if b, err := os.ReadFile(f); err == nil {
			raws = append(raws, b)
		}
	}

	seen := map[string]bool{}
	var out []claudeMessage
	for _, b := range raws {
		var m opencodeMessage
		if json.Unmarshal(b, &m) != nil || m.Role != "assistant" || m.Tokens == nil || m.Time == nil || m.ID == "" {
			continue
		}
		model, provider := m.ModelID, m.ProviderID
		if m.Model != nil {
			model, provider = firstNonEmpty(m.Model.ID, model), firstNonEmpty(m.Model.ProviderID, provider)
		}
		if provider == "claude-agent" {
			continue
		}
		dir := ""
		if m.Path != nil {
			dir = firstNonEmpty(m.Path.Cwd, m.Path.Root)
		}
		if dir == "" || !(sameProject(cwd, dir) || sameProject(dir, cwd)) {
			continue
		}
		at := time.UnixMilli(int64(m.Time.Created)).UTC()
		key := "opencode:" + m.ID
		if at.Before(since) || at.After(until) || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, claudeMessage{Key: key, Provider: firstNonEmpty(provider, "opencode"), Model: firstNonEmpty(model, "opencode-unknown-model"), At: at,
			Input: m.Tokens.Input, CacheRead: m.Tokens.Cache.Read, CacheWrite: m.Tokens.Cache.Write, Output: m.Tokens.Output + m.Tokens.Reasoning})
	}
	return out, nil
}

// ---- which logs belong to a tk session ----

// sessionUsage is every agent reply the tk session can see, oldest first, and a
// description of where it came from. When a hook linked the session to one
// agent's log (Claude Code, Codex or Gemini), only that log is read. Otherwise
// every supported agent's usage in the session's directory is read.
func sessionUsage(s *session, since, until time.Time) ([]claudeMessage, string, error) {
	var msgs []claudeMessage
	var err error
	if s.TranscriptPath != "" && fileExists(s.TranscriptPath) {
		files := []string{s.TranscriptPath}
		switch s.AgentTool {
		case "codex":
			msgs, err = readCodexUsage(files, since, until)
		case "gemini":
			msgs, err = readGeminiUsage(files, since, until)
		default:
			msgs, err = readClaudeUsage(files, since, until)
		}
		sortByTime(msgs)
		return msgs, toolLabel(s.AgentTool) + " session " + short(s.ClaudeSessionID), err
	}

	files, desc, err := sessionLogs(s)
	if err != nil {
		return nil, "", err
	}
	if msgs, err = readClaudeUsage(files, since, until); err != nil {
		return nil, "", err
	}
	sources := []string{}
	if cfiles, err := codexSessionFiles(s.Cwd, since); err == nil && len(cfiles) > 0 {
		cm, err := readCodexUsage(cfiles, since, until)
		if err != nil {
			return nil, "", err
		}
		msgs = append(msgs, cm...)
		sources = append(sources, fmt.Sprintf("%d Codex session(s)", len(cfiles)))
	}
	if gfiles := geminiChatFiles(s.Cwd, since); len(gfiles) > 0 {
		gm, err := readGeminiUsage(gfiles, since, until)
		if err != nil {
			return nil, "", err
		}
		msgs = append(msgs, gm...)
		sources = append(sources, fmt.Sprintf("%d Gemini chat(s)", len(gfiles)))
	}
	if om, err := readOpenCodeUsage(s.Cwd, since, until); err == nil && len(om) > 0 {
		msgs = append(msgs, om...)
		sources = append(sources, "OpenCode")
	}
	if len(sources) > 0 {
		desc += " + " + strings.Join(sources, " + ")
	}
	sortByTime(msgs)
	return msgs, desc, nil
}

func sortByTime(msgs []claudeMessage) {
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].At.Before(msgs[j].At) })
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
