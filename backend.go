package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// backend is where tk keeps issues and records work. Every command goes through
// it, so tk and tkd don't care whether a server exists:
//
//   - localBackend: a store on this machine; no server, no account (the default).
//
//   - paperclipBackend: a Paperclip server, through the tk-bridge plugin.
//
//   - linearBackend: a team's Linear workspace, through its GraphQL API.
//
// Later backends (Jira, GitHub Issues) implement the same interface.
type backend interface {
	name() string
	inbox() ([]inboxItem, error)
	issue(key string) (issue, error)
	context(key string) (heartbeatContext, error)
	// start claims an issue for this agent and returns the new session.
	start(key string) (session, error)
	note(s *session, text string) error
	// finish ends a session: "handoff" pauses (back to todo, still assigned),
	// "done" sends finished work on. It returns the issue's new status.
	finish(s *session, kind, text string) (string, error)
	createIssue(title, description string) (issue, error)
	// postCost records one cost event; repeats with the same idempotencyKey are ignored.
	postCost(s *session, event map[string]any) error
	// refresh updates any local copy of remote data (no-op when there is no server).
	refresh() error
}

// tkConfig is ~/.tk/config.json. Environment variables override it.
type tkConfig struct {
	Mode      string `json:"mode"` // "local" (default), "paperclip", "linear", "jira" or "github"
	Paperclip struct {
		URL    string `json:"url"`
		Plugin string `json:"plugin"` // id of the installed tk-bridge plugin
	} `json:"paperclip"`
	Linear struct {
		Team      string `json:"team"`      // team key for tk new, e.g. "ENG"
		DoneState string `json:"doneState"` // state for tk done; default: a "review" state, else the first completed one
		URL       string `json:"url"`       // GraphQL endpoint (default https://api.linear.app/graphql)
		Project   string `json:"project"`   // optional: project (id or URL slug id) for tk new
		ClientID  string `json:"clientId"`  // tk app's public OAuth client ID (tk login linear)
		User      string `json:"user"`      // your Linear user id or email (tk login linear)
	} `json:"linear"`
	Jira struct {
		Site       string `json:"site"`       // https://<you>.atlassian.net
		Email      string `json:"email"`      // Atlassian account email (token: JIRA_API_TOKEN or ~/.tk/jira_token)
		Project    string `json:"project"`    // optional: project key for tk new and the inbox filter
		IssueType  string `json:"issueType"`  // for tk new (default Task)
		DoneStatus string `json:"doneStatus"` // status for tk done; default: a "review" status, else Done
	} `json:"jira"`
	GitHub struct {
		Repo            string `json:"repo"`            // owner/name
		InProgressLabel string `json:"inProgressLabel"` // default "in progress"
		ReviewLabel     string `json:"reviewLabel"`     // if set, tk done adds it and keeps the issue open
		URL             string `json:"url"`             // API base for GitHub Enterprise (default https://api.github.com)
		ClientID        string `json:"clientId"`        // tk GitHub App's public client ID (tk login github)
	} `json:"github"`
}

func configPath() string { return filepath.Join(tkHome(), "config.json") }

func loadConfig() tkConfig {
	var cfg tkConfig
	if raw, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(raw, &cfg)
	}
	return cfg
}

// configuredMode decides the backend, highest priority first: TK_MODE; Paperclip
// settings in the environment; the config file; an existing saved agent key
// (installs from before local mode); otherwise local.
func configuredMode() string {
	if m := strings.TrimSpace(os.Getenv("TK_MODE")); m != "" {
		return m
	}
	for _, v := range []string{"TK_API_URL", "TK_BRIDGE_PLUGIN", "TK_TOKEN", "TK_TOKEN_FILE"} {
		if os.Getenv(v) != "" {
			return "paperclip"
		}
	}
	if m := loadConfig().Mode; m != "" {
		return m
	}
	if _, err := os.Stat(filepath.Join(tkHome(), "agent_token.json")); err == nil {
		return "paperclip"
	}
	return "local"
}

func openBackend() (backend, error) {
	switch mode := configuredMode(); mode {
	case "local":
		return newLocalBackend()
	case "linear":
		return newLinearBackend()
	case "jira":
		return newJiraBackend()
	case "github":
		return newGithubBackend()
	case "paperclip":
		c, err := newClient()
		if err != nil {
			return nil, err
		}
		if bridgePlugin() == "" {
			return nil, errors.New("Paperclip mode needs the tk-bridge plugin: set TK_BRIDGE_PLUGIN or paperclip.plugin in ~/.tk/config.json")
		}
		return &paperclipBackend{c: c}, nil
	default:
		return nil, errors.New("unknown mode " + mode + " (use local, paperclip, linear, jira or github)")
	}
}

func bridgePlugin() string {
	if p := strings.TrimSpace(os.Getenv("TK_BRIDGE_PLUGIN")); p != "" {
		return p
	}
	return loadConfig().Paperclip.Plugin
}

func paperclipURL() string {
	if u := os.Getenv("TK_API_URL"); u != "" {
		return u
	}
	if u := loadConfig().Paperclip.URL; u != "" {
		return u
	}
	return "http://127.0.0.1:3100"
}

func writeFileAll(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
