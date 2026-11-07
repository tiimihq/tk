package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Codex CLI usage. Codex writes one rollout file per session under
// ~/.codex/sessions/YYYY/MM/DD/. Its first record (session_meta) names the
// working directory; each model response is a token_usage_record with a unique
// response_id and that response's own usage (token_count events repeat the
// same numbers as running totals and are ignored). turn_context records name
// the model. OpenAI counts cached input inside input_tokens, so fresh input is
// input - cached.

func codexSessionsRoot() string {
	if d := os.Getenv("TK_CODEX_SESSIONS"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions")
}

// codexSessionFiles returns rollout files written since `since` by Codex
// sessions whose working directory is the tk session's directory, inside it,
// or a parent of it.
func codexSessionFiles(cwd string, since time.Time) ([]string, error) {
	var out []string
	for day := since.AddDate(0, 0, -1); !day.After(time.Now().AddDate(0, 0, 1)); day = day.AddDate(0, 0, 1) {
		dir := filepath.Join(codexSessionsRoot(), day.Format("2006"), day.Format("01"), day.Format("02"))
		files, err := filepath.Glob(filepath.Join(dir, "rollout-*.jsonl"))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			info, err := os.Stat(f)
			if err != nil || info.ModTime().Before(since.Add(-time.Minute)) {
				continue
			}
			if c := codexSessionCwd(f); c != "" && (sameProject(cwd, c) || sameProject(c, cwd)) {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

func codexSessionCwd(file string) string {
	fh, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for i := 0; i < 5 && sc.Scan(); i++ { // session_meta is the first record
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Type == "session_meta" {
			return rec.Payload.Cwd
		}
	}
	return ""
}

// readCodexUsage returns each Codex model response in files between since and until.
func readCodexUsage(files []string, since, until time.Time) ([]claudeMessage, error) {
	seen := map[string]bool{}
	var out []claudeMessage
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		model, branch, cwd, session := "", "", "", ""
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
		for sc.Scan() {
			var rec struct {
				Type      string `json:"type"`
				Timestamp string `json:"timestamp"`
				Payload   struct {
					ID  string `json:"id"`
					Cwd string `json:"cwd"`
					Git *struct {
						Branch string `json:"branch"`
					} `json:"git"`
					Model      string `json:"model"`
					ResponseID string `json:"response_id"`
					Usage      *struct {
						Input      int64 `json:"input_tokens"`
						Cached     int64 `json:"cached_input_tokens"`
						CacheWrite int64 `json:"cache_write_input_tokens"`
						Output     int64 `json:"output_tokens"`
					} `json:"usage"`
				} `json:"payload"`
			}
			if json.Unmarshal(sc.Bytes(), &rec) != nil {
				continue
			}
			switch rec.Type {
			case "session_meta":
				cwd, session = rec.Payload.Cwd, rec.Payload.ID
				if rec.Payload.Git != nil {
					branch = rec.Payload.Git.Branch
				}
				continue
			case "turn_context":
				if rec.Payload.Model != "" {
					model = rec.Payload.Model
				}
				continue
			case "token_usage_record":
			default:
				continue
			}
			u := rec.Payload.Usage
			if u == nil || rec.Payload.ResponseID == "" {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
			if err != nil || at.Before(since) || at.After(until) {
				continue
			}
			key := "codex:" + rec.Payload.ResponseID
			if seen[key] {
				continue
			}
			seen[key] = true
			fresh := u.Input - u.Cached - u.CacheWrite
			if fresh < 0 {
				fresh = 0
			}
			m := model
			if m == "" {
				m = "codex-unknown-model"
			}
			out = append(out, claudeMessage{Key: key, Provider: "openai", Model: m, At: at,
				Input: fresh, CacheRead: u.Cached, CacheWrite: u.CacheWrite, Output: u.Output,
				Branch: branch, Cwd: cwd, Session: session})
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("reading %s: %w", filepath.Base(f), err)
		}
	}
	return out, nil
}
