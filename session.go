package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// session is the developer session tk has open: one issue, worked on by the
// agent running in this directory.
type session struct {
	ConnectionID string `json:"connectionId,omitempty"`
	Key          string `json:"key"`
	IssueID      string `json:"issueId"`
	SessionID    string `json:"sessionId"`
	AgentID      string `json:"agentId"`
	StartedAt    string `json:"startedAt"`
	Cwd          string `json:"cwd"` // where the agent runs; locates its session logs
	EndedAt      string `json:"endedAt,omitempty"`
	// Set when the session-start hook has recorded the Claude Code session running tk:
	// cost is then read from exactly that session's log.
	ClaudeSessionID string `json:"claudeSessionId,omitempty"` // the linked agent session's id (any tool)
	TranscriptPath  string `json:"transcriptPath,omitempty"`
	AgentTool       string `json:"agentTool,omitempty"` // "" = Claude Code; codex, gemini, cursor
}

func tkHome() string {
	if d := os.Getenv("TK_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tk")
}

// legacySessionPath is where tk kept its single open session before it allowed
// several; a session found there is moved into sessionsDir on first use.
func legacySessionPath() string { return filepath.Join(tkHome(), "session.json") }

// sessionsDir holds one file per open session, named by session id, so closing
// one session can never touch another.
func sessionsDir() string { return filepath.Join(tkHome(), "sessions") }

func sessionFile(id string) string { return filepath.Join(sessionsDir(), safeName(id)+".json") }

// sessionKey identifies a session in the cost ledger and closed-session records.
func sessionKey(s *session) string { return s.SessionID }

// loadSessions returns every open session, oldest first.
func loadSessions() ([]session, error) {
	if raw, err := os.ReadFile(legacySessionPath()); err == nil { // migrate
		var s session
		if json.Unmarshal(raw, &s) == nil {
			if s.SessionID == "" {
				s.SessionID = newSessionID()
			}
			if err := saveSession(s); err != nil {
				return nil, err
			}
		}
		_ = os.Remove(legacySessionPath())
	}
	files, err := filepath.Glob(filepath.Join(sessionsDir(), "*.json"))
	if err != nil {
		return nil, err
	}
	var out []session
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s session
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("corrupt %s: %w", f, err)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out, nil
}

// findSessionIn returns the session that commands run in dir act on:
// TK_ISSUE names one explicitly; otherwise the session opened in dir or the
// closest folder above or below it; with nothing matching and exactly one
// session open, that one (so tk keeps working from any folder for one agent).
func findSessionIn(dir string) (*session, error) {
	all, err := loadSessions()
	if err != nil || len(all) == 0 {
		return nil, err
	}
	if want := strings.TrimSpace(os.Getenv("TK_ISSUE")); want != "" {
		for i := range all {
			if strings.EqualFold(all[i].Key, want) || strings.EqualFold(strings.TrimPrefix(all[i].Key, "#"), strings.TrimPrefix(want, "#")) {
				return &all[i], nil
			}
		}
		return nil, fmt.Errorf("no open session on %s (TK_ISSUE)", want)
	}
	var match []*session
	best := -1
	for i := range all {
		s := &all[i]
		if s.Cwd == "" || !(sameProject(s.Cwd, dir) || sameProject(dir, s.Cwd)) {
			continue
		}
		depth := len(realPath(s.Cwd))
		switch {
		case depth > best:
			match, best = []*session{s}, depth
		case depth == best:
			match = append(match, s)
		}
	}
	switch {
	case len(match) == 1:
		return match[0], nil
	case len(match) > 1:
		var keys []string
		for _, s := range match {
			keys = append(keys, s.Key)
		}
		return nil, fmt.Errorf("sessions open here on %s; set TK_ISSUE=<KEY> to choose", strings.Join(keys, ", "))
	case len(all) == 1:
		return &all[0], nil
	}
	return nil, nil
}

// loadSession is the session for the current directory (nil if none).
func loadSession() (*session, error) { return findSessionIn(cwdOrEmpty()) }

func saveSession(s session) error {
	if err := os.MkdirAll(sessionsDir(), 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(s)
	tmp := sessionFile(s.SessionID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, sessionFile(s.SessionID))
}

func requireSession() (*session, error) {
	s, err := loadSession()
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New(`no issue claimed in this folder: tk start <KEY> first (then note/handoff/done), or tk comment <KEY> "<text>" to just comment`)
	}
	return s, nil
}

// sessionOpen reports whether the session's file still exists (it hasn't been closed).
func sessionOpen(id string) bool { return fileExists(sessionFile(id)) }

// cmdStart claims an issue through the backend, opens a session linked to the
// Claude session running here (when the hook recorded one), and prints the brief.
func cmdStart(be backend, key string) error {
	var s session
	err := withSessionLock(func() error {
		all, err := loadSessions()
		if err != nil {
			return err
		}
		here := realPath(cwdOrEmpty())
		for _, cur := range all {
			if strings.EqualFold(strings.TrimPrefix(cur.Key, "#"), strings.TrimPrefix(key, "#")) {
				return fmt.Errorf("%s already has an open session (in %s); finish it with tk handoff or tk done first", cur.Key, cur.Cwd)
			}
			if realPath(cur.Cwd) == here && os.Getenv("TK_ISSUE") == "" {
				return fmt.Errorf("a session on %s is already open in this folder; finish it first, run the second agent in its own folder or git worktree, or set TK_ISSUE", cur.Key)
			}
		}
		if s, err = be.start(key); err != nil {
			return err
		}
		s.SessionID, s.StartedAt, s.Cwd = newSessionID(), nowRFC3339(), cwdOrEmpty()
		if cs := findClaudeSession(s.Cwd); cs != nil {
			s.ClaudeSessionID, s.TranscriptPath, s.AgentTool = cs.SessionID, cs.TranscriptPath, cs.Tool
		}
		return saveSession(s)
	})
	if err != nil {
		return err
	}
	if s.ClaudeSessionID != "" {
		fmt.Printf("%s started | %s session %s\n", s.Key, toolLabel(s.AgentTool), short(s.ClaudeSessionID))
	} else {
		fmt.Printf("%s started\n", s.Key)
	}
	return cmdResume(be, s.Key, map[string]bool{})
}

func cmdNote(be backend, text string) error {
	s, err := requireSession()
	if err != nil {
		return err
	}
	if err := validateSessionBackend(be, s); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("usage: tk note <text>")
	}
	err = be.note(s, strings.TrimSpace(text))
	if queued, qerr := queueIfOffline(err, "note", "", strings.TrimSpace(text), s); queued {
		fmt.Printf("%s note queued (tracker unreachable); tkd sends it when it's back\n", s.Key)
		return nil
	} else if qerr != nil {
		return qerr
	}
	fmt.Printf("%s note added\n", s.Key)
	return nil
}

// cmdFinish ends the session: "handoff" pauses it for the next session, "done"
// sends finished work on. Cost is synced first; a failed sync never blocks
// finishing, since tkd syncs again after the session (the ledger prevents double counting).
func cmdFinish(be backend, kind, text string) error {
	return withSessionLock(func() error { return finishLocked(be, kind, text) })
}

func finishLocked(be backend, kind, text string) error {
	s, err := requireSession()
	if err != nil {
		return err
	}
	if err := validateSessionBackend(be, s); err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if kind == "handoff" && text == "" {
		return errors.New("usage: tk handoff \"<what was done, what is left, what did not work>\"")
	}
	costLine, err := syncCost(be, s, time.Now().Add(2*time.Second)) // same margin as tkd's final sync: never count replies after the session ended
	if err != nil {
		costLine = "cost: not synced now (" + firstSentence(err.Error()) + "); tkd will sync it after the session"
	}
	status, err := be.finish(s, kind, text)
	if queued, qerr := queueIfOffline(err, "finish", kind, text, s); queued {
		// Offline: close the session here so the agent can move on; tkd updates the tracker later.
		status = "queued (tracker unreachable; tkd sends it when it's back)"
	} else if qerr != nil {
		return fmt.Errorf("%w (session still open; retry tk %s)", qerr, kind)
	}
	if err := closeSession(*s); err != nil {
		return err
	}
	if cr, ok := be.(costReporter); ok { // the issue's running total, where tk keeps it
		if t, err := cr.costSummary(s.Key); err == nil && t.Events > 0 && t.Unpriced < t.Events {
			costLine += fmt.Sprintf(" · %s total ~%s", s.Key, dollars(t.Cents))
		}
	}
	if kind == "handoff" {
		fmt.Printf("%s handed off (%s) | next session: tk start %s\n%s\n", s.Key, status, s.Key, costLine)
	} else {
		fmt.Printf("%s %s\n%s\n", s.Key, status, costLine)
	}
	return nil
}

// closeSession removes the open session and leaves a record for the daemon,
// which syncs cost once more after the last reply has been written to the log.
// The caller holds the session lock. Each session has its own file, so a stale
// caller (tkd acting on a session the developer already finished) can only
// remove that session's file, never another session's.
func closeSession(s session) error {
	s.EndedAt = nowRFC3339()
	dir := filepath.Join(tkHome(), "closed")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, safeFileName(sessionKey(&s))+".json"), raw, 0o600); err != nil {
		return err
	}
	err := os.Remove(sessionFile(s.SessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func getJSON(c *client, path string, out any) error {
	raw, err := c.get(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(sanitizeJSON(raw), out)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Linear session records are pinned to their connection, including delayed syncs.
func validateSessionBackend(be backend, s *session) error {
	if s.ConnectionID == "" {
		return nil
	}
	l, ok := be.(*linearBackend)
	if !ok {
		return errors.New("session belongs to Linear; restore its connection before continuing")
	}
	return l.checkSession(s)
}
