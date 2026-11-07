package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// claudeMessage is one agent reply's token usage, from a Claude Code session log
// or a Codex rollout (codex_usage.go).
type claudeMessage struct {
	Key          string // unique per billed reply: message id + request id, or codex:<response id>
	Provider     string // "anthropic" (default) or "openai"
	Model        string
	At           time.Time
	Input        int64 // fresh input tokens
	CacheRead    int64
	CacheWrite   int64 // all cache writes (Anthropic); CacheWrite5m of them at the 5-minute rate, the rest 1-hour
	CacheWrite5m int64
	Output       int64
	Tier         string // service tier when the log names one ("standard", "priority", "fast")
	// Where the reply happened, when the log says (tk receipt groups by these).
	Branch, Cwd, Session string
}

var nonPathChars = regexp.MustCompile(`[^A-Za-z0-9-]`)

// claudeLogDir is where Claude Code keeps session logs for the directory it was
// launched in. tk may run in a subdirectory, so it walks up to the nearest
// directory that has a log folder.
func claudeLogDir(cwd string) string {
	if d := os.Getenv("TK_CLAUDE_LOG_DIR"); d != "" {
		return d
	}
	root := os.Getenv("TK_CLAUDE_PROJECTS")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".claude", "projects")
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(root, nonPathChars.ReplaceAllString(dir, "-"))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	return filepath.Join(root, nonPathChars.ReplaceAllString(cwd, "-"))
}

// sessionLogs returns the log files to read for a tk session and a description of
// them: exactly the linked Claude session's transcript when the session-start hook
// recorded one, otherwise every log in the project's folder.
func sessionLogs(s *session) ([]string, string, error) {
	if s.TranscriptPath != "" {
		if _, err := os.Stat(s.TranscriptPath); err == nil {
			return []string{s.TranscriptPath}, "Claude session " + short(s.ClaudeSessionID), nil
		}
	}
	dir := claudeLogDir(s.Cwd)
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	return files, dir, err
}

// readClaudeUsage returns each assistant reply in files logged between since and
// until, counted once even when Claude Code writes it on several lines.
func readClaudeUsage(files []string, since, until time.Time) ([]claudeMessage, error) {
	seen := map[string]bool{}
	var out []claudeMessage
	for _, f := range files {
		if info, err := os.Stat(f); err != nil || info.ModTime().Before(since.Add(-time.Minute)) {
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
		for sc.Scan() {
			var rec struct {
				Type      string `json:"type"`
				RequestID string `json:"requestId"`
				Timestamp string `json:"timestamp"`
				GitBranch string `json:"gitBranch"`
				Cwd       string `json:"cwd"`
				SessionID string `json:"sessionId"`
				Message   struct {
					ID    string `json:"id"`
					Model string `json:"model"`
					Usage *struct {
						Input         int64  `json:"input_tokens"`
						CacheRead     int64  `json:"cache_read_input_tokens"`
						CacheWrite    int64  `json:"cache_creation_input_tokens"`
						Output        int64  `json:"output_tokens"`
						ServiceTier   string `json:"service_tier"`
						Speed         string `json:"speed"`
						CacheCreation *struct {
							FiveMin int64 `json:"ephemeral_5m_input_tokens"`
						} `json:"cache_creation"`
					} `json:"usage"`
				} `json:"message"`
			}
			// "<synthetic>" replies are Claude Code's own placeholders (errors, interrupts): no API call, no cost.
			if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Type != "assistant" || rec.Message.Usage == nil || rec.Message.ID == "" || rec.Message.Model == "<synthetic>" {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
			if err != nil || at.Before(since) || at.After(until) {
				continue
			}
			key := rec.Message.ID + ":" + rec.RequestID
			if seen[key] {
				continue
			}
			seen[key] = true
			u := rec.Message.Usage
			m := claudeMessage{Key: key, Model: rec.Message.Model, At: at,
				Input: u.Input, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Output: u.Output, Tier: u.ServiceTier,
				Branch: rec.GitBranch, Cwd: rec.Cwd, Session: rec.SessionID}
			if u.CacheCreation != nil {
				m.CacheWrite5m = min(u.CacheCreation.FiveMin, u.CacheWrite)
			}
			if u.Speed == "fast" {
				m.Tier = "fast"
			}
			out = append(out, m)
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("reading %s: %w", filepath.Base(f), err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// syncCost posts the session's not-yet-posted Claude Code replies, logged between
// the session start and until, as one cost event per model. It first re-posts any
// batch left pending by a crash. The returned line summarises the whole session.
func syncCost(be backend, s *session, until time.Time) (string, error) {
	if err := validateSessionBackend(be, s); err != nil {
		return "", err
	}
	since, err := time.Parse(time.RFC3339, s.StartedAt)
	if err != nil {
		return "", fmt.Errorf("bad session start time: %w", err)
	}
	// tk and tkd can sync the same session at once; without this lock both load
	// the ledger, post, and the later save drops the other's committed replies.
	var line string
	err = withFileLock(ledgerPath(sessionKey(s))+".lock", func() error {
		var e error
		line, e = syncCostLocked(be, s, since, until)
		return e
	})
	return line, err
}

func syncCostLocked(be backend, s *session, since, until time.Time) (string, error) {
	led, err := loadLedger(sessionKey(s))
	if err != nil {
		return "", err
	}
	post := func(key string) error {
		if err := be.postCost(s, led.Pending[key].Event); err != nil {
			return err
		}
		led.commit(key)
		return led.save()
	}
	for key := range led.Pending {
		if err := post(key); err != nil {
			return "", fmt.Errorf("re-posting pending batch: %w", err)
		}
	}

	msgs, dir, err := sessionUsage(s, since, until)
	if err != nil {
		return "", err
	}
	byModel := map[string][]claudeMessage{}
	for _, m := range msgs {
		if _, done := led.Posted[m.Key]; !done {
			byModel[m.Model] = append(byModel[m.Model], m)
		}
	}
	newReplies := 0
	for model, all := range byModel {
		for _, group := range splitByPriced(all) {
			key, event := buildBatch(s, model, group)
			keys := make([]string, len(group))
			for i, m := range group {
				keys[i] = m.Key
			}
			led.Pending[key] = pendingBatch{Replies: keys, Event: event}
			if err := led.save(); err != nil { // write-ahead: record before posting
				return "", err
			}
			if err := post(key); err != nil {
				return "", fmt.Errorf("posting %s batch: %w", model, err)
			}
			newReplies += len(group)
		}
	}
	return sessionCostLine(msgs, newReplies, dir), nil
}

// buildBatch sums replies of one model into a single cost event.
func buildBatch(s *session, model string, group []claudeMessage) (string, map[string]any) {
	var sum claudeMessage
	sum.Model = model
	keys := make([]string, len(group))
	for i, m := range group {
		keys[i] = m.Key
		sum.Input += m.Input
		sum.CacheRead += m.CacheRead
		sum.CacheWrite += m.CacheWrite
		sum.CacheWrite5m += m.CacheWrite5m
		sum.Output += m.Output
		if m.At.After(sum.At) {
			sum.At = m.At
		}
	}
	key := batchKey(sessionKey(s), model, keys)
	provider := "anthropic"
	if len(group) > 0 && group[0].Provider != "" {
		provider = group[0].Provider
	}
	event := map[string]any{
		"idempotencyKey":    key,
		"agentId":           s.AgentID,
		"issueId":           s.IssueID,
		"provider":          provider,
		"biller":            provider,
		"billingType":       "unknown",
		"model":             model,
		"inputTokens":       sum.Input + sum.CacheWrite, // Paperclip counts cache writes inside input
		"cachedInputTokens": sum.CacheRead,
		"outputTokens":      sum.Output,
		"occurredAt":        sum.At.UTC().Format("2006-01-02T15:04:05.000Z"),
		"costCents":         "0",
		"costStatus":        "unpriced",
	}
	// Price each reply on its own (tiers depend on each prompt's size; prices on
	// its date) and add them up. splitByPriced keeps a batch all-priced or all-unpriced.
	total, version, priced := new(big.Rat), "", true
	for _, m := range group {
		cents, v := priceCents(m)
		r, ok := new(big.Rat).SetString(cents)
		if cents == "" || !ok {
			priced = false
			break
		}
		total.Add(total, r)
		version = v
	}
	// Tokens are the permanent record: with them, cost can be recomputed when prices change.
	event["tokens"] = map[string]int64{"freshInput": sum.Input, "cacheRead": sum.CacheRead,
		"cacheWrite5m": sum.CacheWrite5m, "cacheWrite1h": sum.CacheWrite - sum.CacheWrite5m, "output": sum.Output, "replies": int64(len(group))}
	if priced && len(group) > 0 {
		event["costCents"] = total.FloatString(6)
		event["costStatus"] = "estimated"
		event["pricingProvenance"] = map[string]any{"source": "rate_card", "version": version, "evidence": pricingEvidence}
	}
	return key, event
}

// splitByPriced separates replies tk can price from those it can't, so an
// unknown cost is never folded into a priced total.
func splitByPriced(group []claudeMessage) [][]claudeMessage {
	var priced, unpriced []claudeMessage
	for _, m := range group {
		if c, _ := priceCents(m); c != "" {
			priced = append(priced, m)
		} else {
			unpriced = append(unpriced, m)
		}
	}
	var out [][]claudeMessage
	for _, g := range [][]claudeMessage{priced, unpriced} {
		if len(g) > 0 {
			out = append(out, g)
		}
	}
	return out
}

func sessionCostLine(msgs []claudeMessage, newReplies int, dir string) string {
	if len(msgs) == 0 {
		return "cost: no agent usage yet in " + dir
	}
	var in, cached, write, out int64
	total := new(big.Rat)
	unpriced := 0
	for _, m := range msgs {
		in, cached, write, out = in+m.Input, cached+m.CacheRead, write+m.CacheWrite, out+m.Output
		if cents, _ := priceCents(m); cents != "" {
			r, _ := new(big.Rat).SetString(cents)
			total.Add(total, r)
		} else {
			unpriced++
		}
	}
	usd := new(big.Rat).Quo(total, big.NewRat(100, 1)).FloatString(2)
	price := "~$" + usd + " API-equivalent"
	switch {
	case unpriced == len(msgs): // unknown, not free
		price = "not priced (model not in the rate card)"
	case unpriced > 0:
		price += fmt.Sprintf(" + %d unpriced replies", unpriced)
	}
	return fmt.Sprintf("cost: %d replies (%d new) | in %d | cache read %d | cache write %d | out %d | %s",
		len(msgs), newReplies, in, cached, write, out, price)
}

func cmdCost(be backend, args []string) error {
	if len(args) > 0 && args[0] == "update" {
		return cmdCostUpdate()
	}
	s, err := requireSession()
	if err != nil {
		return err
	}
	line, err := syncCost(be, s, time.Now().Add(time.Minute))
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimSpace(line))
	fmt.Println(priceSourceNote())
	return nil
}
