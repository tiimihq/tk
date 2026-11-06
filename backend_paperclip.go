package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// paperclipBackend records work on a Paperclip server through the tk-bridge
// plugin's agent-authenticated routes, with no heartbeat runs; the agent's
// wakes should be switched off so Paperclip never launches or recovers it.
type paperclipBackend struct {
	c     *client
	live  bool // read from the server rather than the cache (session-start brief)
	label string
}

func (p *paperclipBackend) name() string { return "paperclip (" + p.c.baseURL + ")" }

func (p *paperclipBackend) read(path string) ([]byte, error) {
	if p.live {
		return liveGet(p.c, path)
	}
	return cachedGet(p.c, path)
}

func (p *paperclipBackend) inbox() ([]inboxItem, error) {
	raw, err := p.read("/api/agents/me/inbox-lite")
	if err != nil {
		return nil, err
	}
	var items []inboxItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("unexpected inbox format: %w", err)
	}
	return items, nil
}

func (p *paperclipBackend) issue(key string) (issue, error) {
	var is issue
	raw, err := p.read(issuePath(key, ""))
	if err != nil {
		return is, err
	}
	if err := json.Unmarshal(sanitizeJSON(raw), &is); err != nil {
		return is, fmt.Errorf("unexpected issue format: %w", err)
	}
	return is, nil
}

func (p *paperclipBackend) context(key string) (heartbeatContext, error) {
	raw, err := p.read(issuePath(key, "/heartbeat-context"))
	if err != nil {
		return heartbeatContext{}, err
	}
	return parseContext(raw)
}

func (p *paperclipBackend) start(key string) (session, error) {
	var me struct {
		ID string `json:"id"`
	}
	if err := getJSON(p.c, "/api/agents/me", &me); err != nil {
		return session{}, err
	}
	var is struct {
		ID              string `json:"id"`
		Identifier      string `json:"identifier"`
		AssigneeAgentID string `json:"assigneeAgentId"`
	}
	if err := getJSON(p.c, issuePath(key, ""), &is); err != nil {
		return session{}, err
	}
	invalidateIssue(is.Identifier)
	if is.AssigneeAgentID != "" && is.AssigneeAgentID != me.ID {
		return session{}, fmt.Errorf("%s is assigned to another agent", is.Identifier)
	}
	if _, err := p.bridge(is.ID, "start", nil); err != nil {
		return session{}, fmt.Errorf("start: %w", err)
	}
	return session{Key: is.Identifier, IssueID: is.ID, AgentID: me.ID}, nil
}

func (p *paperclipBackend) note(s *session, text string) error {
	_, err := p.bridge(s.IssueID, "note", map[string]any{"text": text})
	invalidateIssue(s.Key)
	return err
}

func (p *paperclipBackend) finish(s *session, kind, text string) (string, error) {
	out, err := p.bridge(s.IssueID, kind, map[string]any{"text": text})
	invalidateIssue(s.Key)
	if err != nil {
		return "", err
	}
	status, _ := out["status"].(string)
	return status, nil
}

func (p *paperclipBackend) createIssue(title, description string) (issue, error) {
	return issue{}, errors.New("in Paperclip mode, create issues in the Paperclip UI (agents can't create them without a run)")
}

func (p *paperclipBackend) postCost(s *session, event map[string]any) error {
	var me struct {
		CompanyID string `json:"companyId"`
	}
	if err := getJSON(p.c, "/api/agents/me", &me); err != nil {
		return err
	}
	_, err := p.c.send(http.MethodPost, "/api/companies/"+me.CompanyID+"/cost-events", event, "")
	return err
}

// refresh stores the agent's inbox and each listed issue (plus the open
// session's issue) in the cache tk reads first.
func (p *paperclipBackend) refresh() error {
	inbox, err := p.c.get("/api/agents/me/inbox-lite")
	if err != nil {
		return err
	}
	if err := writeCache("/api/agents/me/inbox-lite", inbox); err != nil {
		return err
	}
	var items []inboxItem
	_ = json.Unmarshal(inbox, &items)
	keys := map[string]bool{}
	for _, it := range items {
		keys[it.Identifier] = true
	}
	all, _ := loadSessions()
	for _, s := range all {
		keys[s.Key] = true
	}
	for key := range keys {
		for _, suffix := range []string{"", "/heartbeat-context"} {
			path := issuePath(key, suffix)
			body, err := p.c.get(path)
			if err != nil {
				return err
			}
			if err := writeCache(path, body); err != nil {
				return err
			}
		}
	}
	return nil
}

// bridge posts to a tk-bridge route for an issue and decodes the reply.
func (p *paperclipBackend) bridge(issueID, route string, body map[string]any) (map[string]any, error) {
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["agentLabel"]; !ok {
		body["agentLabel"] = p.agentLabel()
	}
	raw, err := p.c.send(http.MethodPost, "/api/plugins/"+bridgePlugin()+"/api/issues/"+issueID+"/"+route, body, "")
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	_ = json.Unmarshal(sanitizeJSON(raw), &out)
	return out, nil
}

func (p *paperclipBackend) agentLabel() string {
	if p.label == "" {
		var me struct {
			Name string `json:"name"`
		}
		if getJSON(p.c, "/api/agents/me", &me) == nil {
			p.label = me.Name
		}
	}
	return p.label
}

// newSessionID identifies a session in the ledger and closed-session records.
func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "s-" + hex.EncodeToString(b)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func cwdOrEmpty() string {
	d, _ := os.Getwd()
	return d
}
