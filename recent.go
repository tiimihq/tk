package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Trackers' list/search endpoints catch up a few seconds after a write (GitHub's
// issue list took ~4 s in a live test; Jira's search is documented as eventually
// consistent). So `tk new` then `tk next` could miss the new issue. tk remembers
// what it created recently and adds those to the inbox, checking each one
// directly (single-issue reads are immediately consistent).

const recentWindow = 2 * time.Minute

type recentIssue struct {
	Key string    `json:"key"`
	ID  string    `json:"id,omitempty"`
	At  time.Time `json:"at"`
}

func recentPath(scope string) string {
	return filepath.Join(tkHome(), "recent", safeName(scope)+".json")
}

func rememberCreated(scope, key, id string) {
	list := recentCreated(scope)
	list = append(list, recentIssue{Key: key, ID: id, At: time.Now()})
	if os.MkdirAll(filepath.Dir(recentPath(scope)), 0o700) != nil {
		return
	}
	if b, err := json.Marshal(list); err == nil {
		_ = os.WriteFile(recentPath(scope), b, 0o600)
	}
}

// recentCreated returns issues created in this scope within recentWindow.
func recentCreated(scope string) []recentIssue {
	raw, err := os.ReadFile(recentPath(scope))
	if err != nil {
		return nil
	}
	var all, out []recentIssue
	_ = json.Unmarshal(raw, &all)
	for _, r := range all {
		if time.Since(r.At) < recentWindow {
			out = append(out, r)
		}
	}
	return out
}
