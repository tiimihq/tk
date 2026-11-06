package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// localBackend keeps everything on this machine: no server, no account. Field
// names follow Paperclip's, so a later `tk login` can upload issues as they are.
//
// Storage is one JSON file (~/.tk/local/store.json) guarded by an exclusive file
// lock and replaced atomically, so tk and tkd can't overwrite each other. It is
// sized for one developer's issues; a SQLite store can replace it behind the
// same interface.
type localBackend struct{}

type localStore struct {
	Prefix  string       `json:"prefix"`
	Counter int          `json:"counter"`
	Agent   localAgent   `json:"agent"`
	Issues  []localIssue `json:"issues"`
}

type localAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type localIssue struct {
	ID              string           `json:"id"`
	Identifier      string           `json:"identifier"`
	Title           string           `json:"title"`
	Description     string           `json:"description"`
	Status          string           `json:"status"`
	Priority        string           `json:"priority"`
	AssigneeAgentID string           `json:"assigneeAgentId,omitempty"`
	CreatedAt       string           `json:"createdAt"`
	UpdatedAt       string           `json:"updatedAt"`
	Comments        []localComment   `json:"comments,omitempty"`
	CostEvents      []map[string]any `json:"costEvents,omitempty"`
}

type localComment struct {
	ID            string `json:"id"`
	Body          string `json:"body"`
	AuthorAgentID string `json:"authorAgentId,omitempty"`
	Author        string `json:"author,omitempty"` // "you" for the developer's own comments
	CreatedAt     string `json:"createdAt"`
}

func localDir() string { return filepath.Join(tkHome(), "local") }

func newLocalBackend() (*localBackend, error) {
	if err := os.MkdirAll(localDir(), 0o700); err != nil {
		return nil, err
	}
	return &localBackend{}, nil
}

func (l *localBackend) name() string { return "local (" + localDir() + ")" }

// withStore runs fn on the store under an exclusive lock; when write is true the
// store is saved afterwards (atomically, via a temporary file and rename).
func (l *localBackend) withStore(write bool, fn func(*localStore) error) error {
	return withFileLock(filepath.Join(localDir(), "store.lock"), func() error { return l.withStoreLocked(write, fn) })
}

func (l *localBackend) withStoreLocked(write bool, fn func(*localStore) error) error {
	path := filepath.Join(localDir(), "store.json")
	st := &localStore{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, st); err != nil {
			return fmt.Errorf("corrupt %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if st.Prefix == "" {
		st.Prefix = "LOC"
	}
	if st.Agent.ID == "" {
		st.Agent = localAgent{ID: "local-agent", Name: "local agent"}
	}
	if err := fn(st); err != nil {
		return err
	}
	if !write {
		return nil
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (st *localStore) find(key string) (*localIssue, error) {
	for i := range st.Issues {
		if strings.EqualFold(st.Issues[i].Identifier, key) || st.Issues[i].ID == key {
			return &st.Issues[i], nil
		}
	}
	return nil, fmt.Errorf("issue %s not found (tk new \"<title>\" creates one)", key)
}

func (li *localIssue) toIssue() issue {
	return issue{Identifier: li.Identifier, Title: li.Title, Description: li.Description,
		Status: li.Status, Priority: li.Priority, AssigneeAgentID: li.AssigneeAgentID}
}

func (li *localIssue) addComment(agentID, body string) {
	li.Comments = append(li.Comments, localComment{ID: newSessionID(), Body: body, AuthorAgentID: agentID, CreatedAt: nowRFC3339()})
	li.UpdatedAt = nowRFC3339()
}

func (l *localBackend) inbox() ([]inboxItem, error) {
	var out []inboxItem
	err := l.withStore(false, func(st *localStore) error {
		for _, li := range st.Issues {
			if li.AssigneeAgentID == st.Agent.ID && (li.Status == "todo" || li.Status == "in_progress" || li.Status == "blocked") {
				out = append(out, inboxItem{Identifier: li.Identifier, Title: li.Title, Status: li.Status, Priority: li.Priority})
			}
		}
		return nil
	})
	return out, err
}

func (l *localBackend) issue(key string) (issue, error) {
	var is issue
	err := l.withStore(false, func(st *localStore) error {
		li, err := st.find(key)
		if err == nil {
			is = li.toIssue()
		}
		return err
	})
	return is, err
}

// context builds the same brief Paperclip's heartbeat-context gives: the issue,
// plus a summary made from the latest notes and handoffs.
func (l *localBackend) context(key string) (heartbeatContext, error) {
	var hc heartbeatContext
	err := l.withStore(false, func(st *localStore) error {
		li, err := st.find(key)
		if err != nil {
			return err
		}
		hc.Issue = li.toIssue()
		if n := len(li.Comments); n > 0 {
			var b strings.Builder
			b.WriteString("## Recent Concrete Actions\n")
			for _, c := range li.Comments[max(0, n-5):] {
				b.WriteString("- " + oneLine(c.Body) + "\n")
			}
			hc.ContinuationSummary = &struct {
				Body string `json:"body"`
			}{Body: b.String()}
			hc.CommentCursor = &struct {
				TotalComments   int    `json:"totalComments"`
				LatestCommentAt string `json:"latestCommentAt"`
			}{TotalComments: n, LatestCommentAt: li.Comments[n-1].CreatedAt}
		}
		return nil
	})
	return hc, err
}

func (l *localBackend) start(key string) (session, error) {
	var s session
	err := l.withStore(true, func(st *localStore) error {
		li, err := st.find(key)
		if err != nil {
			return err
		}
		if li.Status == "done" || li.Status == "cancelled" {
			return fmt.Errorf("%s is %s", li.Identifier, li.Status)
		}
		li.Status, li.AssigneeAgentID, li.UpdatedAt = "in_progress", st.Agent.ID, nowRFC3339()
		s = session{Key: li.Identifier, IssueID: li.ID, AgentID: st.Agent.ID}
		return nil
	})
	return s, err
}

func (l *localBackend) note(s *session, text string) error {
	return l.withStore(true, func(st *localStore) error {
		li, err := st.find(s.IssueID)
		if err != nil {
			return err
		}
		li.addComment(s.AgentID, text)
		return nil
	})
}

// finish: a handoff puts the issue back to todo, still assigned; done closes it
// (locally the developer is the reviewer).
func (l *localBackend) finish(s *session, kind, text string) (string, error) {
	var status string
	err := l.withStore(true, func(st *localStore) error {
		li, err := st.find(s.IssueID)
		if err != nil {
			return err
		}
		cancelled := li.Status == "cancelled" // cancelled by a person while the agent worked
		switch kind {
		case "handoff":
			if !cancelled {
				li.Status = "todo"
			}
			li.addComment(s.AgentID, "Handoff: "+text)
		case "done":
			if !cancelled {
				li.Status = "done"
			}
			if text == "" {
				text = "finished"
			}
			li.addComment(s.AgentID, "Done: "+text)
		default:
			return fmt.Errorf("unknown finish %q", kind)
		}
		status = li.Status
		return nil
	})
	return status, err
}

func (l *localBackend) createIssue(title, description string) (issue, error) {
	var is issue
	err := l.withStore(true, func(st *localStore) error {
		st.Counter++
		now := nowRFC3339()
		li := localIssue{ID: newSessionID(), Identifier: fmt.Sprintf("%s-%d", st.Prefix, st.Counter), Title: title,
			Description: description, Status: "todo", Priority: "medium", AssigneeAgentID: st.Agent.ID,
			CreatedAt: now, UpdatedAt: now}
		st.Issues = append(st.Issues, li)
		is = li.toIssue()
		return nil
	})
	return is, err
}

func (l *localBackend) postCost(s *session, event map[string]any) error {
	return l.withStore(true, func(st *localStore) error {
		li, err := st.find(s.IssueID)
		if err != nil {
			return err
		}
		for _, e := range li.CostEvents {
			if e["idempotencyKey"] == event["idempotencyKey"] {
				return nil // already recorded
			}
		}
		li.CostEvents = append(li.CostEvents, event)
		return nil
	})
}

func (l *localBackend) refresh() error { return nil }

// costSummary totals an issue's recorded cost (shown by tk peek).
func (l *localBackend) costSummary(key string) (t costTotals, err error) {
	err = l.withStore(false, func(st *localStore) error {
		li, err := st.find(key)
		if err != nil {
			return err
		}
		t = sumCostEvents(li.CostEvents)
		return nil
	})
	return
}
