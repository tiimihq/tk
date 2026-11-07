package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// jiraBackend works on Jira Cloud through REST API v3 with an API token
// (email + token, HTTP basic auth): the agent acts as the developer.
//
// Mapping (Jira workflows differ per project, so tk follows status categories
// and only uses transitions the workflow offers from the current status):
//   - inbox: issues assigned to the developer, status category not Done
//     (POST /rest/api/3/search/jql; the old /search endpoint is being removed)
//   - start: assign to the developer if unassigned; transition to an In Progress
//     category status that isn't a review status
//   - note: a comment (Atlassian Document Format), prefixed [tk agent]
//   - handoff: transition back to a To Do category status if the workflow allows
//     (otherwise the status stays and the comment says so)
//   - done: jira.doneStatus, else a "review" status, else a Done category status
//   - cost: kept in tk's cost store; handoff/done comments carry the cost line
type jiraBackend struct {
	rc         *restClient
	site       string
	project    string
	issueType  string
	doneStatus string
	me         string // accountId
}

func newJiraBackend() (*jiraBackend, error) {
	cfg := loadConfig().Jira
	site := strings.TrimRight(firstNonEmpty(os.Getenv("TK_JIRA_URL"), cfg.Site), "/")
	email := firstNonEmpty(os.Getenv("JIRA_EMAIL"), cfg.Email)
	token := secretFrom("jira_token", "JIRA_API_TOKEN")
	switch {
	case site == "":
		return nil, errors.New(`Jira mode needs the site: "jira": {"site": "https://<you>.atlassian.net"} in ~/.tk/config.json`)
	case email == "":
		return nil, errors.New(`Jira mode needs your Atlassian email: "jira": {"email": "..."} or JIRA_EMAIL`)
	case token == "":
		return nil, errors.New("Jira mode needs an API token: tk secret set jira (or JIRA_API_TOKEN)")
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token))
	rc := &restClient{base: site, http: &http.Client{Timeout: 15 * time.Second},
		auth: func(r *http.Request) { r.Header.Set("Authorization", basic) }, errMsg: jiraErrorMessage}
	return &jiraBackend{rc: rc, site: site, project: cfg.Project, issueType: firstNonEmpty(cfg.IssueType, "Task"), doneStatus: cfg.DoneStatus}, nil
}

func jiraErrorMessage(raw []byte) string {
	var e struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if jsonUnmarshal(raw, &e) != nil {
		return ""
	}
	msgs := append([]string(nil), e.ErrorMessages...)
	keys := make([]string, 0, len(e.Errors))
	for k := range e.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		msgs = append(msgs, k+": "+e.Errors[k])
	}
	return strings.Join(msgs, "; ")
}

func (j *jiraBackend) name() string                    { return "jira (" + j.site + ")" }
func (j *jiraBackend) setTimeout(d time.Duration)      { j.rc.http.Timeout = d }
func (j *jiraBackend) cacheKey(parts ...string) string { return "/jira/" + strings.Join(parts, "/") }

type jiraStatus struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"` // new, indeterminate, done
	} `json:"statusCategory"`
}

type jiraUser struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

type jiraIssue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary     string     `json:"summary"`
		Description any        `json:"description"` // ADF document
		Status      jiraStatus `json:"status"`
		Priority    *struct {
			Name string `json:"name"`
		} `json:"priority"`
		Assignee *jiraUser `json:"assignee"`
		Comment  *struct {
			Comments []struct {
				Body    any    `json:"body"`
				Created string `json:"created"`
			} `json:"comments"`
		} `json:"comment"`
	} `json:"fields"`
}

func jiraTkStatus(st jiraStatus) string {
	name := strings.ToLower(st.Name)
	switch st.StatusCategory.Key {
	case "new":
		return "todo"
	case "indeterminate":
		if strings.Contains(name, "review") {
			return "in_review"
		}
		if strings.Contains(name, "block") {
			return "blocked"
		}
		return "in_progress"
	case "done":
		for _, w := range []string{"cancel", "won't", "wont", "declined", "rejected", "duplicate"} {
			if strings.Contains(name, w) {
				return "cancelled"
			}
		}
		return "done"
	}
	return name
}

func jiraTkPriority(p *struct {
	Name string `json:"name"`
}) string {
	if p == nil {
		return "none"
	}
	switch strings.ToLower(p.Name) {
	case "highest", "blocker", "critical":
		return "urgent"
	case "high", "major":
		return "high"
	case "medium":
		return "medium"
	case "low", "lowest", "minor", "trivial":
		return "low"
	}
	return strings.ToLower(p.Name)
}

func (j *jiraBackend) toIssue(ji jiraIssue) issue {
	is := issue{Identifier: ji.Key, Title: ji.Fields.Summary, Description: adfText(ji.Fields.Description),
		Status: jiraTkStatus(ji.Fields.Status), Priority: jiraTkPriority(ji.Fields.Priority)}
	if a := ji.Fields.Assignee; a != nil {
		if a.AccountID == j.me {
			is.AssigneeAgentID = a.AccountID
		} else {
			is.AssigneeUserID = a.AccountID
		}
	}
	return is
}

func (j *jiraBackend) myself() (string, error) {
	if j.me != "" {
		return j.me, nil
	}
	var u jiraUser
	if err := j.rc.do(http.MethodGet, "/rest/api/3/myself", nil, j.cacheKey("myself"), &u); err != nil {
		return "", err
	}
	j.me = u.AccountID
	return j.me, nil
}

func (j *jiraBackend) inbox() ([]inboxItem, error) {
	if _, err := j.myself(); err != nil {
		return nil, err
	}
	jql := "assignee = currentUser() AND statusCategory != Done"
	if j.project != "" {
		jql = fmt.Sprintf(`project = "%s" AND %s`, j.project, jql)
	}
	jql += " ORDER BY priority DESC, updated DESC"
	var res struct {
		Issues []jiraIssue `json:"issues"`
	}
	body := map[string]any{"jql": jql, "fields": []string{"summary", "status", "priority"}, "maxResults": 50}
	// Jira's search lags writes; reconcileIssues makes it include issues tk just created.
	var reconcile []int64
	for _, r := range recentCreated("jira-" + j.site) {
		if id, err := strconv.ParseInt(r.ID, 10, 64); err == nil {
			reconcile = append(reconcile, id)
		}
	}
	if len(reconcile) > 0 {
		body["reconcileIssues"] = reconcile
	}
	if err := j.rc.do(http.MethodPost, "/rest/api/3/search/jql", body, j.cacheKey("inbox"), &res); err != nil {
		return nil, err
	}
	items := make([]inboxItem, 0, len(res.Issues))
	for _, ji := range res.Issues {
		items = append(items, inboxItem{Identifier: ji.Key, Title: ji.Fields.Summary,
			Status: jiraTkStatus(ji.Fields.Status), Priority: jiraTkPriority(ji.Fields.Priority)})
	}
	return items, nil
}

func (j *jiraBackend) fetch(key string) (jiraIssue, error) {
	var ji jiraIssue
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "?fields=summary,description,status,priority,assignee,comment"
	if err := j.rc.do(http.MethodGet, path, nil, j.cacheKey("issue", key), &ji); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return ji, fmt.Errorf("issue %s not found in Jira (or you can't see it)", key)
		}
		return ji, err
	}
	return ji, nil
}

func (j *jiraBackend) issue(key string) (issue, error) {
	if _, err := j.myself(); err != nil {
		return issue{}, err
	}
	ji, err := j.fetch(key)
	if err != nil {
		return issue{}, err
	}
	return j.toIssue(ji), nil
}

func (j *jiraBackend) context(key string) (heartbeatContext, error) {
	if _, err := j.myself(); err != nil {
		return heartbeatContext{}, err
	}
	ji, err := j.fetch(key)
	if err != nil {
		return heartbeatContext{}, err
	}
	var cs []timedComment
	if ji.Fields.Comment != nil {
		for _, c := range ji.Fields.Comment.Comments {
			cs = append(cs, timedComment{Body: adfText(c.Body), At: c.Created})
		}
	}
	return contextFromComments(j.toIssue(ji), cs), nil
}

type jiraTransition struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	To   jiraStatus `json:"to"`
}

func (j *jiraBackend) transitions(key string) ([]jiraTransition, error) {
	var res struct {
		Transitions []jiraTransition `json:"transitions"`
	}
	err := j.rc.do(http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", nil, "", &res)
	return res.Transitions, err
}

// pickTransition prefers a transition whose target is named like one of names,
// then any whose target passes keep.
func pickTransition(ts []jiraTransition, names []string, keep func(jiraStatus) bool) *jiraTransition {
	for _, n := range names {
		for i := range ts {
			if strings.EqualFold(ts[i].To.Name, n) || strings.EqualFold(ts[i].Name, n) {
				return &ts[i]
			}
		}
	}
	for i := range ts {
		if keep != nil && keep(ts[i].To) {
			return &ts[i]
		}
	}
	return nil
}

func (j *jiraBackend) doTransition(key string, t *jiraTransition) error {
	return j.rc.do(http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions",
		map[string]any{"transition": map[string]string{"id": t.ID}}, "", nil)
}

func transitionNames(ts []jiraTransition) string {
	var names []string
	for _, t := range ts {
		names = append(names, t.To.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func isReviewStatus(s jiraStatus) bool { return strings.Contains(strings.ToLower(s.Name), "review") }

func (j *jiraBackend) start(key string) (session, error) {
	me, err := j.myself()
	if err != nil {
		return session{}, err
	}
	ji, err := j.fetch(key)
	if err != nil {
		return session{}, err
	}
	defer j.invalidate(ji.Key)
	if a := ji.Fields.Assignee; a != nil && a.AccountID != me {
		return session{}, fmt.Errorf("%s is assigned to %s", ji.Key, firstNonEmpty(a.DisplayName, "someone else"))
	}
	if ji.Fields.Status.StatusCategory.Key == "done" {
		return session{}, fmt.Errorf("%s is %s", ji.Key, ji.Fields.Status.Name)
	}
	if ji.Fields.Assignee == nil {
		if err := j.rc.do(http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(ji.Key)+"/assignee",
			map[string]string{"accountId": me}, "", nil); err != nil {
			return session{}, fmt.Errorf("assigning %s to you: %w", ji.Key, err)
		}
	}
	cur := ji.Fields.Status
	if !(cur.StatusCategory.Key == "indeterminate" && !isReviewStatus(cur)) {
		ts, err := j.transitions(ji.Key)
		if err != nil {
			return session{}, err
		}
		t := pickTransition(ts, []string{"In Progress"}, func(s jiraStatus) bool {
			return s.StatusCategory.Key == "indeterminate" && !isReviewStatus(s)
		})
		if t == nil {
			return session{}, fmt.Errorf("%s: the workflow offers no move to an in-progress status from %s (available: %s)", ji.Key, cur.Name, transitionNames(ts))
		}
		if err := j.doTransition(ji.Key, t); err != nil {
			return session{}, fmt.Errorf("moving %s to %s: %w", ji.Key, t.To.Name, err)
		}
	}
	return session{Key: ji.Key, IssueID: ji.Key, AgentID: me}, nil
}

func (j *jiraBackend) comment(key, text string) error {
	defer j.invalidate(key)
	return j.rc.do(http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/comment",
		map[string]any{"body": adfDoc(agentNotePrefix + text)}, "", nil)
}

func (j *jiraBackend) note(s *session, text string) error { return j.comment(s.Key, text) }

func (j *jiraBackend) finish(s *session, kind, text string) (string, error) {
	ji, err := j.fetch(s.Key)
	if err != nil {
		return "", err
	}
	defer j.invalidate(s.Key)
	ts, err := j.transitions(s.Key)
	if err != nil {
		return "", err
	}
	var t *jiraTransition
	var body string
	switch kind {
	case "handoff":
		body = "Handoff: " + text
		t = pickTransition(ts, []string{"To Do", "Selected for Development", "Open"}, func(st jiraStatus) bool { return st.StatusCategory.Key == "new" })
	case "done":
		if text == "" {
			text = "finished"
		}
		body = "Done: " + text
		if j.doneStatus != "" {
			if t = pickTransition(ts, []string{j.doneStatus}, nil); t == nil {
				return "", fmt.Errorf("jira.doneStatus %q isn't reachable from %s (available: %s)", j.doneStatus, ji.Fields.Status.Name, transitionNames(ts))
			}
		} else {
			t = pickTransition(ts, nil, isReviewStatus)
			if t == nil {
				t = pickTransition(ts, []string{"Done"}, func(st jiraStatus) bool { return st.StatusCategory.Key == "done" && jiraTkStatus(st) == "done" })
			}
		}
	default:
		return "", fmt.Errorf("unknown finish %q", kind)
	}
	if line := costStoreSummaryLine(s.Key); line != "" {
		body += "\n\n" + line
	}
	if t == nil && kind == "handoff" {
		// Some workflows can't go back to To Do; keep the status, still record the handoff.
		body += "\n\n(status left at " + ji.Fields.Status.Name + ": the workflow offers no move back to To Do)"
	}
	if err := j.comment(s.Key, body); err != nil {
		return "", err
	}
	if t == nil {
		if kind == "done" {
			return "", fmt.Errorf("%s: the workflow offers no review or done status from %s (available: %s); set jira.doneStatus", s.Key, ji.Fields.Status.Name, transitionNames(ts))
		}
		return ji.Fields.Status.Name, nil
	}
	if err := j.doTransition(s.Key, t); err != nil {
		return "", fmt.Errorf("moving %s to %s: %w", s.Key, t.To.Name, err)
	}
	return t.To.Name, nil
}

func (j *jiraBackend) createIssue(title, description string) (issue, error) {
	if j.project == "" {
		return issue{}, errors.New(`set the project for new issues: "jira": {"project": "ENG"} in ~/.tk/config.json`)
	}
	me, err := j.myself()
	if err != nil {
		return issue{}, err
	}
	fields := map[string]any{"project": map[string]string{"key": j.project}, "summary": title,
		"issuetype": map[string]string{"name": j.issueType}, "assignee": map[string]string{"accountId": me}}
	if description != "" {
		fields["description"] = adfDoc(description)
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := j.rc.do(http.MethodPost, "/rest/api/3/issue", map[string]any{"fields": fields}, "", &created); err != nil {
		return issue{}, err
	}
	_ = os.Remove(cacheFile(j.cacheKey("inbox")))
	rememberCreated("jira-"+j.site, created.Key, created.ID)
	return issue{Identifier: created.Key, Title: title, Description: description, Status: "todo", Priority: "medium", AssigneeAgentID: me}, nil
}

func (j *jiraBackend) postCost(s *session, event map[string]any) error {
	return costStoreAdd(s.Key, event)
}

func (j *jiraBackend) costSummary(key string) (costTotals, error) { return costStoreTotals(key) }

func (j *jiraBackend) refresh() error {
	if _, err := j.inbox(); err != nil {
		return err
	}
	all, _ := loadSessions()
	for _, s := range all {
		if _, err := j.fetch(s.Key); err != nil {
			return err
		}
	}
	return nil
}

func (j *jiraBackend) invalidate(key string) {
	_ = os.Remove(cacheFile(j.cacheKey("issue", key)))
	_ = os.Remove(cacheFile(j.cacheKey("inbox")))
}

// ---- Atlassian Document Format ----

// adfDoc turns plain text into an ADF document: blank lines separate
// paragraphs, single newlines become hard breaks.
func adfDoc(text string) map[string]any {
	var paras []any
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		var content []any
		for i, line := range strings.Split(p, "\n") {
			if i > 0 {
				content = append(content, map[string]any{"type": "hardBreak"})
			}
			if line != "" {
				content = append(content, map[string]any{"type": "text", "text": line})
			}
		}
		paras = append(paras, map[string]any{"type": "paragraph", "content": content})
	}
	if paras == nil {
		paras = []any{map[string]any{"type": "paragraph", "content": []any{}}}
	}
	return map[string]any{"type": "doc", "version": 1, "content": paras}
}

// adfText flattens an ADF document (or a plain string) to text.
func adfText(node any) string {
	var b strings.Builder
	var walk func(n any)
	walk = func(n any) {
		switch v := n.(type) {
		case string:
			b.WriteString(v)
		case map[string]any:
			switch v["type"] {
			case "text":
				s, _ := v["text"].(string)
				b.WriteString(s)
			case "hardBreak":
				b.WriteString("\n")
			case "mention":
				if a, ok := v["attrs"].(map[string]any); ok {
					s, _ := a["text"].(string)
					b.WriteString(s)
				}
			}
			if kids, ok := v["content"].([]any); ok {
				for _, k := range kids {
					walk(k)
				}
			}
			switch v["type"] {
			case "paragraph", "heading", "listItem", "codeBlock", "blockquote":
				b.WriteString("\n\n")
			}
		case []any:
			for _, k := range v {
				walk(k)
			}
		}
	}
	walk(node)
	return strings.TrimSpace(regexpBlankRuns.ReplaceAllString(b.String(), "\n\n"))
}

// commentOn adds a comment to any issue, without a tk session (tk comment).
func (j *jiraBackend) commentOn(key, text string) error { return j.comment(key, text) }
