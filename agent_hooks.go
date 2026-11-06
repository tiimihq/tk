package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Native hooks for Codex, Cursor and Gemini CLI (Claude Code uses
// `tk hook session-start`, hook.go). Each handler:
//   - never fails and never blocks the agent: problems produce no output, exit 0;
//   - injects the tk brief at session start;
//   - recovers it after context compaction where the tool can't inject on the
//     compaction event itself: the compaction hook leaves a marker and the next
//     prompt (Codex) or tool call (Cursor) injects the brief once (the approach
//     Beads uses);
//   - records the agent session (id, transcript, directory) so `tk start` can
//     read exactly that session's usage log.

type agentHookInput struct {
	SessionID      string   `json:"session_id"`
	ConversationID string   `json:"conversation_id"` // Cursor
	TranscriptPath string   `json:"transcript_path"`
	Cwd            string   `json:"cwd"`
	WorkspaceRoots []string `json:"workspace_roots"` // Cursor
	HookEventName  string   `json:"hook_event_name"`
	Source         string   `json:"source"`
}

func (in agentHookInput) id() string {
	if in.SessionID != "" {
		return in.SessionID
	}
	return in.ConversationID
}

func (in agentHookInput) dir() string {
	if in.Cwd != "" {
		return in.Cwd
	}
	if len(in.WorkspaceRoots) > 0 {
		return in.WorkspaceRoots[0]
	}
	d, _ := os.Getwd()
	return d
}

func cmdAgentHook(tool, event string, stdin io.Reader, stdout io.Writer) {
	defer func() { _ = recover() }() // a hook must never break the agent
	raw, _ := io.ReadAll(io.LimitReader(stdin, 1<<20))
	var in agentHookInput
	_ = json.Unmarshal(raw, &in)
	if in.HookEventName != "" {
		event = in.HookEventName
	}
	marker := hookMarkerPath(tool, in.id(), in.dir())
	brief := func() string { return sessionBrief(claudeSession{Cwd: in.dir()}) }
	emit := func(v any) { _ = json.NewEncoder(stdout).Encode(v) }

	switch tool + ":" + event {
	case "codex:SessionStart":
		recordAgentSession(tool, in)
		if b := brief(); b != "" {
			emit(codexContext("SessionStart", b))
		}
	case "codex:PostCompact":
		touch(marker)
	case "codex:UserPromptSubmit":
		recordAgentSession(tool, in)
		if fileExists(marker) {
			_ = os.Remove(marker)
			if b := brief(); b != "" {
				emit(codexContext("UserPromptSubmit", b))
			}
		}
	case "cursor:sessionStart":
		recordAgentSession(tool, in)
		if b := brief(); b != "" {
			emit(map[string]any{"additional_context": b})
		} else {
			emit(map[string]any{})
		}
	case "cursor:preCompact":
		touch(marker)
		emit(map[string]any{"user_message": "tk: the work brief will be restored after compaction."})
	case "cursor:postToolUse":
		if fileExists(marker) {
			_ = os.Remove(marker)
			if b := brief(); b != "" {
				emit(map[string]any{"additional_context": b})
				return
			}
		}
		emit(map[string]any{})
	case "gemini:SessionStart":
		recordAgentSession(tool, in)
		if b := brief(); b != "" {
			emit(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": b}})
		}
	}
}

func codexContext(event, text string) map[string]any {
	return map[string]any{"continue": true, "hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}}
}

// recordAgentSession stores the session next to Claude Code's, tagged with its tool.
func recordAgentSession(tool string, in agentHookInput) {
	if in.id() == "" {
		return
	}
	cs := claudeSession{Tool: tool, SessionID: in.id(), TranscriptPath: in.TranscriptPath, Cwd: in.dir(), SeenAt: nowRFC3339()}
	if err := os.MkdirAll(claudeSessionsDir(), 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(cs); err == nil {
		_ = os.WriteFile(filepath.Join(claudeSessionsDir(), tool+"-"+safeName(in.id())+".json"), b, 0o600)
	}
}

func hookMarkerPath(tool, id, dir string) string {
	sum := sha256.Sum256([]byte(id + "\n" + dir))
	return filepath.Join(tkHome(), "hook-markers", tool+"-"+hex.EncodeToString(sum[:8]))
}

func touch(path string) {
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func safeName(s string) string {
	out := []rune{}
	for _, r := range s {
		if r == '/' || r == '\\' || r == ':' || r == '.' {
			r = '_'
		}
		out = append(out, r)
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return string(out)
}

// toolLabel names an agent for messages.
func toolLabel(tool string) string {
	switch tool {
	case "", "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini"
	case "cursor":
		return "Cursor"
	}
	return fmt.Sprint(tool)
}
