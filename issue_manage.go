package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// issueManager is what a person needs beyond the agent work loop: see every
// issue, edit it, comment as themselves, cancel it. `tk list/edit/comment/cancel`
// and `tk ui` use it. The local backend implements it; tracker backends keep
// their own UI for this and may add it later.
type issueManager interface {
	listIssues(includeClosed bool) ([]issueDetail, error)
	issueDetailOf(key string) (issueDetail, error)
	createIssue(title, description string) (issue, error)
	updateIssue(key string, p issuePatch) (issueDetail, error)
	addHumanComment(key, body string) error
}

// issueDetail is an issue with its timeline and cost, as the UI shows it.
type issueDetail struct {
	Identifier  string        `json:"identifier"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      string        `json:"status"`
	Priority    string        `json:"priority"`
	Assignee    string        `json:"assignee"` // "agent" or ""
	CreatedAt   string        `json:"createdAt"`
	UpdatedAt   string        `json:"updatedAt"`
	Comments    []commentView `json:"comments,omitempty"`
	Cost        costTotals    `json:"cost"`
	CostText    string        `json:"costText"`
}

type commentView struct {
	Author    string `json:"author"` // "agent" or "you"
	Kind      string `json:"kind"`   // "note", "handoff", "done", "comment"
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

// issuePatch holds the fields to change; nil means unchanged.
type issuePatch struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Priority    *string `json:"priority"`
	Status      *string `json:"status"`
}

var (
	localStatuses   = []string{"backlog", "todo", "in_progress", "blocked", "done", "cancelled"}
	localPriorities = []string{"urgent", "high", "medium", "low", "none"}
)

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func isClosed(status string) bool { return status == "done" || status == "cancelled" }

func requireManager(be backend) (issueManager, error) {
	if m, ok := be.(issueManager); ok {
		return m, nil
	}
	return nil, fmt.Errorf("this command works in local mode; in %s use the tracker's own app", be.name())
}

// ---- local backend implementation ----

func (li *localIssue) detail() issueDetail {
	d := issueDetail{Identifier: li.Identifier, Title: li.Title, Description: li.Description, Status: li.Status,
		Priority: li.Priority, CreatedAt: li.CreatedAt, UpdatedAt: li.UpdatedAt}
	if li.AssigneeAgentID != "" {
		d.Assignee = "agent"
	}
	for _, c := range li.Comments {
		v := commentView{Author: "agent", Kind: "note", Body: c.Body, CreatedAt: c.CreatedAt}
		if c.Author == "you" {
			v.Author, v.Kind = "you", "comment"
		}
		switch {
		case strings.HasPrefix(c.Body, "Handoff: "):
			v.Kind, v.Body = "handoff", strings.TrimPrefix(c.Body, "Handoff: ")
		case strings.HasPrefix(c.Body, "Done: "):
			v.Kind, v.Body = "done", strings.TrimPrefix(c.Body, "Done: ")
		case strings.HasPrefix(c.Body, "Cancelled: "):
			v.Kind, v.Body = "cancelled", strings.TrimPrefix(c.Body, "Cancelled: ")
		}
		d.Comments = append(d.Comments, v)
	}
	d.Cost = sumCostEvents(li.CostEvents)
	d.CostText = d.Cost.String()
	return d
}

var statusOrder = map[string]int{"in_progress": 0, "blocked": 1, "todo": 2, "backlog": 3, "done": 4, "cancelled": 5}
var priorityOrder = map[string]int{"urgent": 0, "high": 1, "medium": 2, "low": 3, "none": 4}

func (l *localBackend) listIssues(includeClosed bool) ([]issueDetail, error) {
	var out []issueDetail
	err := l.withStore(false, func(st *localStore) error {
		for i := range st.Issues {
			if includeClosed || !isClosed(st.Issues[i].Status) {
				out = append(out, st.Issues[i].detail())
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if statusOrder[a.Status] != statusOrder[b.Status] {
			return statusOrder[a.Status] < statusOrder[b.Status]
		}
		if priorityOrder[a.Priority] != priorityOrder[b.Priority] {
			return priorityOrder[a.Priority] < priorityOrder[b.Priority]
		}
		return a.UpdatedAt > b.UpdatedAt
	})
	return out, err
}

func (l *localBackend) issueDetailOf(key string) (issueDetail, error) {
	var d issueDetail
	err := l.withStore(false, func(st *localStore) error {
		li, err := st.find(key)
		if err == nil {
			d = li.detail()
		}
		return err
	})
	return d, err
}

func (l *localBackend) updateIssue(key string, p issuePatch) (issueDetail, error) {
	var d issueDetail
	err := l.withStore(true, func(st *localStore) error {
		li, err := st.find(key)
		if err != nil {
			return err
		}
		if p.Title != nil {
			t := strings.TrimSpace(*p.Title)
			if t == "" {
				return errors.New("title can't be empty")
			}
			li.Title = t
		}
		if p.Description != nil {
			li.Description = strings.TrimSpace(*p.Description)
		}
		if p.Priority != nil {
			if !oneOf(*p.Priority, localPriorities) {
				return fmt.Errorf("priority must be one of %s", strings.Join(localPriorities, ", "))
			}
			li.Priority = *p.Priority
		}
		if p.Status != nil {
			if !oneOf(*p.Status, localStatuses) {
				return fmt.Errorf("status must be one of %s", strings.Join(localStatuses, ", "))
			}
			li.Status = *p.Status
		}
		li.UpdatedAt = nowRFC3339()
		d = li.detail()
		return nil
	})
	return d, err
}

func (l *localBackend) addHumanComment(key, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return errors.New("comment is empty")
	}
	return l.withStore(true, func(st *localStore) error {
		li, err := st.find(key)
		if err != nil {
			return err
		}
		li.Comments = append(li.Comments, localComment{ID: newSessionID(), Body: body, Author: "you", CreatedAt: nowRFC3339()})
		li.UpdatedAt = nowRFC3339()
		return nil
	})
}

// cancelIssue records the reason (if any) and marks the issue cancelled.
func cancelIssue(m issueManager, key, reason string) (issueDetail, error) {
	if reason = strings.TrimSpace(reason); reason != "" {
		if err := m.addHumanComment(key, "Cancelled: "+reason); err != nil {
			return issueDetail{}, err
		}
	}
	st := "cancelled"
	return m.updateIssue(key, issuePatch{Status: &st})
}

// ---- commands ----

func formatListLine(d issueDetail) string {
	parts := []string{d.Identifier, d.Status, d.Priority, d.Title}
	if d.Cost.Events > 0 {
		parts = append(parts, "cost "+d.Cost.String())
	}
	return cleanText(strings.Join(parts, " | "))
}

func cmdList(be backend, flags map[string]bool) error {
	m, err := requireManager(be)
	if err != nil { // a remote tracker: tk lists what is assigned to you there
		fmt.Printf("(%s: your assigned issues; the full list is in the tracker)\n", be.name())
		return cmdNext(be, flags)
	}
	items, err := m.listIssues(flags["all"])
	if err != nil {
		return err
	}
	if flags["json"] {
		return printJSON(items)
	}
	if len(items) == 0 {
		if flags["all"] {
			fmt.Println("no issues (tk new \"<title>\" creates one)")
		} else {
			fmt.Println("no open issues (--all includes done and cancelled)")
		}
		return nil
	}
	for _, d := range items {
		fmt.Println(formatListLine(d))
	}
	return nil
}

// cmdEdit: tk edit KEY field=value ... (fields: title, description, priority, status).
func cmdEdit(be backend, key string, assigns []string) error {
	m, err := requireManager(be)
	if err != nil {
		return err
	}
	if len(assigns) == 0 {
		return errors.New(`usage: tk edit <KEY> title="..." description="..." priority=high status=todo`)
	}
	var p issuePatch
	for _, a := range assigns {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("%q: expected field=value", a)
		}
		val := v
		switch strings.ToLower(k) {
		case "title":
			p.Title = &val
		case "description", "desc":
			p.Description = &val
		case "priority":
			p.Priority = &val
		case "status":
			p.Status = &val
		default:
			return fmt.Errorf("unknown field %q (title, description, priority, status)", k)
		}
	}
	d, err := m.updateIssue(key, p)
	if err != nil {
		return err
	}
	fmt.Println(formatListLine(d))
	return nil
}

// commenter is a remote tracker that can comment on any issue without a session.
type commenter interface{ commentOn(key, text string) error }

func cmdComment(be backend, key, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New(`usage: tk comment <KEY> "<text>"`)
	}
	if c, ok := be.(commenter); ok {
		if err := c.commentOn(key, strings.TrimSpace(text)); err != nil {
			return err
		}
		fmt.Printf("%s comment added\n", key)
		return nil
	}
	m, err := requireManager(be)
	if err != nil {
		return err
	}
	if err := m.addHumanComment(key, text); err != nil {
		return err
	}
	fmt.Printf("%s comment added\n", key)
	return nil
}

func cmdCancel(be backend, key, reason string) error {
	m, err := requireManager(be)
	if err != nil {
		return err
	}
	all, _ := loadSessions()
	for _, s := range all {
		if strings.EqualFold(s.Key, key) {
			fmt.Printf("note: a tk session is open on %s; the agent's tk done/handoff will keep it cancelled\n", s.Key)
		}
	}
	d, err := cancelIssue(m, key, reason)
	if err != nil {
		return err
	}
	fmt.Println(formatListLine(d))
	return nil
}
