package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// linearBackend works on a team's existing Linear workspace through its GraphQL
// API, with a personal API key: the agent acts as the developer who runs it.
//
// Mapping:
//   - inbox: the viewer's assigned issues in "unstarted" or "started" states
//   - start: move to the team's first started state (not a review state), assign to the viewer
//   - note: a comment (prefixed so people can tell agent notes apart)
//   - handoff: back to the team's first unstarted state, with a handoff comment
//   - done: to linear.doneState, else a state named "...review...", else the first completed state
//   - cost: Linear has no cost field, so cost events stay in tk's cost store
//     (~/.tk/costs) and finishing a session posts a one-line cost summary comment.
type linearBackend struct {
	url    string
	key    string
	http   *http.Client
	viewer string // cached viewer id
	bearer bool
	oauth  *linearOAuth
}

const linearNotePrefix = "[tk agent] "

func linearKey() (string, error) {
	if k := getSecret("linear", "TK_LINEAR_API_KEY", "LINEAR_API_KEY"); k != "" {
		return k, nil
	}
	return "", errors.New("Linear mode needs an API key: tk secret set linear (or LINEAR_API_KEY)")
}

func newLinearBackend() (*linearBackend, error) {
	var key string
	var auth *linearOAuth
	var err error
	// A saved app login takes precedence over personal keys, including shell keys.
	auth, err = readOAuth()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if auth != nil {
		key = auth.AccessToken
	} else {
		key, err = linearKey()
		if err != nil {
			return nil, err
		}
	}
	u := os.Getenv("TK_LINEAR_URL") // tests point this at a fake server
	if u == "" {
		u = loadConfig().Linear.URL
	}
	if u == "" {
		u = "https://api.linear.app/graphql"
	}
	return &linearBackend{url: u, key: key, oauth: auth, bearer: auth != nil, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (l *linearBackend) name() string {
	if l.oauth != nil {
		return "linear (" + l.oauth.AgentName + ")"
	}
	return "linear"
}
func (l *linearBackend) connectionID() string {
	identity := l.key
	if l.oauth != nil {
		identity = oauthConnection(l.oauth)
	}
	sum := sha256.Sum256([]byte(l.url + "\n" + identity))
	return fmt.Sprintf("%x", sum[:16])
}
func (l *linearBackend) cacheKey(key string) string { return "/linear/" + l.connectionID() + key }
func (l *linearBackend) invalidate(key string) {
	_ = os.Remove(cacheFile(l.cacheKey("/linear/issues/" + key)))
	_ = os.Remove(cacheFile(l.cacheKey("/linear/inbox")))
}
func (l *linearBackend) ownerID() (string, error) {
	if l.oauth != nil {
		return l.oauth.HumanID, nil
	}
	return l.viewerID()
}
func (l *linearBackend) checkOwnership(li linearIssue) error {
	owner, err := l.ownerID()
	if err != nil {
		return err
	}
	if li.Assignee != nil && li.Assignee.ID != owner {
		return fmt.Errorf("%s is assigned to someone else", li.Identifier)
	}
	if l.oauth != nil && li.Delegate != nil && li.Delegate.ID != l.oauth.AgentID {
		return fmt.Errorf("%s is delegated to another agent", li.Identifier)
	}
	return nil
}
func (l *linearBackend) checkSession(s *session) error {
	if s.ConnectionID != "" && s.ConnectionID != l.connectionID() {
		return errors.New("session belongs to a different Linear connection")
	}
	if l.oauth != nil && s.ConnectionID == "" {
		return errors.New("legacy session cannot be used with an app login")
	}
	return nil
}
func (l *linearBackend) costKey(key string) string {
	if l.oauth != nil {
		return l.connectionID() + "-" + key
	}
	return key
}

// gql runs a query. Read queries pass a cacheKey: the answer is cached, and
// served from the cache when Linear can't be reached.
func (l *linearBackend) gql(query string, vars map[string]any, cacheKey string, out any) error {
	if cacheKey != "" {
		cacheKey = l.cacheKey(cacheKey)
	}
	raw, err := l.post(query, vars)
	if err != nil && isUnreachable(err) && cacheKey != "" {
		if cached, cerr := os.ReadFile(cacheFile(cacheKey)); cerr == nil {
			raw, err = cached, nil
		}
	}
	if err != nil {
		return err
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("unexpected Linear response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return errors.New("Linear: " + resp.Errors[0].Message)
	}
	if cacheKey != "" {
		_ = writeCache(cacheKey, raw)
	}
	return json.Unmarshal(resp.Data, out)
}

func (l *linearBackend) post(query string, vars map[string]any) ([]byte, error) {
	if l.oauth != nil {
		c, err := refreshOAuth(l.http, linearTokenURL, oauthConnection(l.oauth))
		if err != nil {
			return nil, err
		}
		l.key = c.AccessToken
	}
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequest(http.MethodPost, l.url, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.bearer || strings.HasPrefix(l.key, "lin_oauth_") || strings.HasPrefix(l.key, "Bearer ") {
		req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(l.key, "Bearer "))
	} else {
		req.Header.Set("Authorization", l.key) // personal API keys go without "Bearer"
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return nil, &unreachableError{base: l.url, err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 && !strings.Contains(string(raw), `"errors"`) {
		return nil, fmt.Errorf("Linear HTTP %d: %s", resp.StatusCode, firstSentence(strings.TrimSpace(string(raw))))
	}
	return raw, nil
}

type linearState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

type linearIssue struct {
	ID          string      `json:"id"`
	Identifier  string      `json:"identifier"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Priority    float64     `json:"priority"`
	State       linearState `json:"state"`
	Delegate    *struct {
		ID string `json:"id"`
	} `json:"delegate"`
	Assignee *struct {
		ID string `json:"id"`
	} `json:"assignee"`
	Team struct {
		ID     string `json:"id"`
		States struct {
			Nodes []linearState `json:"nodes"`
		} `json:"states"`
	} `json:"team"`
	Comments struct {
		Nodes []struct {
			Body      string `json:"body"`
			CreatedAt string `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
}

// tkStatus maps a Linear workflow state to tk's status words.
func tkStatus(st linearState) string {
	switch st.Type {
	case "unstarted":
		return "todo"
	case "started":
		if strings.Contains(strings.ToLower(st.Name), "review") {
			return "in_review"
		}
		return "in_progress"
	case "completed":
		return "done"
	case "canceled", "duplicate":
		return "cancelled"
	default: // triage, backlog
		return st.Type
	}
}

func tkPriority(p float64) string {
	switch int(p) {
	case 1:
		return "urgent"
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	default:
		return "none"
	}
}

func (li linearIssue) toIssue(viewer string) issue {
	is := issue{Identifier: li.Identifier, Title: li.Title, Description: li.Description,
		Status: tkStatus(li.State), Priority: tkPriority(li.Priority)}
	if li.Delegate != nil {
		is.DelegateAgentID = li.Delegate.ID
	}
	if li.Assignee != nil {
		if li.Assignee.ID == viewer {
			is.AssigneeAgentID = li.Assignee.ID
		} else {
			is.AssigneeUserID = li.Assignee.ID
		}
	}
	return is
}

func (l *linearBackend) viewerID() (string, error) {
	if l.viewer != "" {
		return l.viewer, nil
	}
	var d struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}
	if err := l.gql(`query { viewer { id } }`, nil, "/linear/viewer", &d); err != nil {
		return "", err
	}
	l.viewer = d.Viewer.ID
	return l.viewer, nil
}

const linearInboxQuery = `query {
  viewer { id assignedIssues(first: 50, filter: { state: { type: { in: ["unstarted", "started"] } } }) {
    nodes { identifier title priority state { name type } } } } }`

func (l *linearBackend) inbox() ([]inboxItem, error) {
	if l.oauth != nil {
		return l.appInbox()
	}
	var d struct {
		Viewer struct {
			ID             string `json:"id"`
			AssignedIssues struct {
				Nodes []linearIssue `json:"nodes"`
			} `json:"assignedIssues"`
		} `json:"viewer"`
	}
	if err := l.gql(linearInboxQuery, nil, "/linear/inbox", &d); err != nil {
		return nil, err
	}
	l.viewer = d.Viewer.ID
	items := make([]inboxItem, 0, len(d.Viewer.AssignedIssues.Nodes))
	for _, n := range d.Viewer.AssignedIssues.Nodes {
		items = append(items, inboxItem{Identifier: n.Identifier, Title: n.Title, Status: tkStatus(n.State), Priority: tkPriority(n.Priority)})
	}
	return items, nil
}

const linearIssueQuery = `query($id: String!) { issue(id: $id) {
  id identifier title description priority
  state { id name type position } assignee { id } delegate { id }
  team { id states { nodes { id name type position } } }
  comments(last: 5) { nodes { body createdAt } } } }`

func (l *linearBackend) fetch(key string) (linearIssue, error) { return l.fetchIssue(key, true) }
func (l *linearBackend) fetchIssue(key string, cached bool) (linearIssue, error) {
	cache := ""
	if cached {
		cache = "/linear/issues/" + key
	}
	var d struct {
		Issue *linearIssue `json:"issue"`
	}
	if err := l.gql(linearIssueQuery, map[string]any{"id": key}, cache, &d); err != nil {
		return linearIssue{}, err
	}
	if d.Issue == nil {
		return linearIssue{}, fmt.Errorf("issue %s not found in Linear", key)
	}
	return *d.Issue, nil
}

func (l *linearBackend) issue(key string) (issue, error) {
	li, err := l.fetch(key)
	if err != nil {
		return issue{}, err
	}
	return li.toIssue(""), nil
}

func (l *linearBackend) context(key string) (heartbeatContext, error) {
	var hc heartbeatContext
	li, err := l.fetch(key)
	if err != nil {
		return hc, err
	}
	hc.Issue = li.toIssue("")
	comments := li.Comments.Nodes
	sort.Slice(comments, func(i, j int) bool { return comments[i].CreatedAt < comments[j].CreatedAt })
	if n := len(comments); n > 0 {
		var b strings.Builder
		b.WriteString("## Recent Concrete Actions\n")
		for _, c := range comments {
			b.WriteString("- " + oneLine(strings.TrimPrefix(c.Body, linearNotePrefix)) + "\n")
		}
		hc.ContinuationSummary = &struct {
			Body string `json:"body"`
		}{Body: b.String()}
		hc.CommentCursor = &struct {
			TotalComments   int    `json:"totalComments"`
			LatestCommentAt string `json:"latestCommentAt"`
		}{TotalComments: n, LatestCommentAt: comments[n-1].CreatedAt}
	}
	return hc, nil
}

// pickState returns the team's first state (by board position) of the given
// type that passes keep.
func pickState(states []linearState, typ string, keep func(linearState) bool) *linearState {
	var best *linearState
	for i := range states {
		s := states[i]
		if s.Type == typ && (keep == nil || keep(s)) && (best == nil || s.Position < best.Position) {
			best = &states[i]
		}
	}
	return best
}

func isReview(s linearState) bool { return strings.Contains(strings.ToLower(s.Name), "review") }

func (l *linearBackend) setState(li linearIssue, st *linearState, assignee string) error {
	input := map[string]any{"stateId": st.ID}
	if assignee != "" {
		if l.oauth == nil {
			input["assigneeId"] = assignee
		} else {
			input["delegateId"] = l.oauth.AgentID
			if li.Assignee == nil {
				input["assigneeId"] = l.oauth.HumanID
			}
		}
	}
	var d struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	err := l.gql(`mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }`,
		map[string]any{"id": li.ID, "input": input}, "", &d)
	if err == nil && !d.IssueUpdate.Success {
		err = fmt.Errorf("Linear did not update %s", li.Identifier)
	}
	l.invalidate(li.Identifier)
	return err
}

func (l *linearBackend) comment(issueID, body string) error {
	var d struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	err := l.gql(`mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success } }`,
		map[string]any{"input": map[string]any{"issueId": issueID, "body": linearNotePrefix + body}}, "", &d)
	if err == nil && !d.CommentCreate.Success {
		err = errors.New("Linear did not create the comment")
	}
	return err
}

func (l *linearBackend) start(key string) (session, error) {
	viewer, err := l.ownerID()
	if err != nil {
		return session{}, err
	}
	li, err := l.fetchIssue(key, false)
	if err != nil {
		return session{}, err
	}
	if err := l.checkOwnership(li); err != nil {
		return session{}, err
	}
	switch li.State.Type {
	case "completed", "canceled", "duplicate":
		return session{}, fmt.Errorf("%s is %s", li.Identifier, li.State.Name)
	}
	st := pickState(li.Team.States.Nodes, "started", func(s linearState) bool { return !isReview(s) })
	if st == nil {
		return session{}, fmt.Errorf("team of %s has no started state", li.Identifier)
	}
	if err := l.setState(li, st, viewer); err != nil {
		return session{}, err
	}
	agent := viewer
	if l.oauth != nil {
		agent = l.oauth.AgentID
	}
	return session{Key: li.Identifier, IssueID: li.ID, AgentID: agent, ConnectionID: l.connectionID()}, nil
}

func (l *linearBackend) note(s *session, text string) error {
	if err := l.checkSession(s); err != nil {
		return err
	}
	li, err := l.fetchIssue(s.Key, false)
	if err != nil {
		return err
	}
	if err = l.checkOwnership(li); err != nil {
		return err
	}
	l.invalidate(s.Key)
	return l.comment(s.IssueID, text)
}

func (l *linearBackend) finish(s *session, kind, text string) (string, error) {
	if err := l.checkSession(s); err != nil {
		return "", err
	}
	li, err := l.fetchIssue(s.Key, false)
	if err != nil {
		return "", err
	}
	if err := l.checkOwnership(li); err != nil {
		return "", err
	}
	states := li.Team.States.Nodes
	var st *linearState
	var body string
	switch kind {
	case "handoff":
		st, body = pickState(states, "unstarted", nil), "Handoff: "+text
	case "done":
		if want := loadConfig().Linear.DoneState; want != "" {
			for i := range states {
				if strings.EqualFold(states[i].Name, want) {
					st = &states[i]
				}
			}
			if st == nil {
				return "", fmt.Errorf("linear.doneState %q is not a state of this team", want)
			}
		}
		if st == nil {
			st = pickState(states, "started", isReview)
		}
		if st == nil {
			st = pickState(states, "completed", nil)
		}
		if text == "" {
			text = "finished"
		}
		body = "Done: " + text
	default:
		return "", fmt.Errorf("unknown finish %q", kind)
	}
	if st == nil {
		return "", fmt.Errorf("no suitable Linear state for %s", kind)
	}
	if line := costStoreSummaryLine(l.costKey(s.Key)); line != "" {
		body += "\n\n" + line
	}
	if err := l.comment(li.ID, body); err != nil {
		return "", err
	}
	if err := l.setState(li, st, ""); err != nil {
		return "", err
	}
	return st.Name, nil
}

func (l *linearBackend) createIssue(title, description string) (issue, error) {
	team := loadConfig().Linear.Team
	if team == "" {
		return issue{}, errors.New(`set the team for new issues: "linear": {"team": "ENG"} in ~/.tk/config.json`)
	}
	viewer, err := l.ownerID()
	if err != nil {
		return issue{}, err
	}
	var t struct {
		Teams struct {
			Nodes []struct {
				ID     string `json:"id"`
				States struct {
					Nodes []linearState `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := l.gql(`query($key: String!) { teams(filter: { key: { eq: $key } }) { nodes { id states { nodes { id name type position } } } } }`,
		map[string]any{"key": team}, "", &t); err != nil {
		return issue{}, err
	}
	if len(t.Teams.Nodes) == 0 {
		return issue{}, fmt.Errorf("no Linear team with key %s", team)
	}
	var d struct {
		IssueCreate struct {
			Issue linearIssue `json:"issue"`
		} `json:"issueCreate"`
	}
	input := map[string]any{"teamId": t.Teams.Nodes[0].ID, "title": title, "assigneeId": viewer}
	if l.oauth != nil {
		input["delegateId"] = l.oauth.AgentID
	}
	// Linear's default for new issues is Backlog, which tk next doesn't list; an issue
	// created with tk new is meant to be worked on, so it goes to the first Todo-type state.
	if st := pickState(t.Teams.Nodes[0].States.Nodes, "unstarted", nil); st != nil {
		input["stateId"] = st.ID
	}
	if description != "" {
		input["description"] = description
	}
	if p := loadConfig().Linear.Project; p != "" {
		var pr struct {
			Project *struct {
				ID string `json:"id"`
			} `json:"project"`
		}
		if err := l.gql(`query($id: String!) { project(id: $id) { id } }`, map[string]any{"id": p}, "", &pr); err != nil {
			return issue{}, fmt.Errorf("linear.project %s: %w", p, err)
		}
		if pr.Project == nil {
			return issue{}, fmt.Errorf("linear.project %s not found", p)
		}
		input["projectId"] = pr.Project.ID
	}
	if err := l.gql(`mutation($input: IssueCreateInput!) { issueCreate(input: $input) { issue { id identifier title priority state { name type } } } }`,
		map[string]any{"input": input}, "", &d); err != nil {
		return issue{}, err
	}
	l.invalidate(d.IssueCreate.Issue.Identifier)
	return d.IssueCreate.Issue.toIssue(""), nil
}

func (l *linearBackend) postCost(s *session, event map[string]any) error {
	if err := l.checkSession(s); err != nil {
		return err
	}
	return costStoreAdd(l.costKey(s.Key), event)
}

func (l *linearBackend) costSummary(key string) (costTotals, error) {
	return costStoreTotals(l.costKey(key))
}

// refresh keeps the inbox and the open session's issue cached for offline reads.
func (l *linearBackend) refresh() error {
	if _, err := l.inbox(); err != nil {
		return err
	}
	all, _ := loadSessions()
	for _, s := range all {
		if _, err := l.fetch(s.Key); err != nil {
			return err
		}
	}
	return nil
}

// Only show this operator's work delegated to the installed agent, plus
// undelegated work that the operator can explicitly start. Follow pagination.
func (l *linearBackend) appInbox() ([]inboxItem, error) {
	var items []inboxItem
	var cursor any
	for {
		var d struct {
			Issues struct {
				Nodes    []linearIssue
				PageInfo struct {
					HasNextPage bool
					EndCursor   string
				}
			}
		}
		err := l.gql(`query($owner: ID!, $agent: ID!, $after: String) {
   issues(first: 50, after: $after, filter: { assignee: {id: {eq: $owner}},
    or: [{delegate: {id: {eq: $agent}}}, {delegate: {null: true}}],
    state: {type: {in: ["unstarted", "started"]}} }) {
    nodes {identifier title priority state {name type}}
    pageInfo {hasNextPage endCursor} } }`, map[string]any{"owner": l.oauth.HumanID, "agent": l.oauth.AgentID, "after": cursor}, fmt.Sprintf("/linear/inbox/%v", cursor), &d)
		if err != nil {
			return nil, err
		}
		for _, n := range d.Issues.Nodes {
			items = append(items, inboxItem{Identifier: n.Identifier, Title: n.Title, Status: tkStatus(n.State), Priority: tkPriority(n.Priority)})
		}
		if !d.Issues.PageInfo.HasNextPage {
			return items, nil
		}
		if d.Issues.PageInfo.EndCursor == "" || cursor == d.Issues.PageInfo.EndCursor {
			return nil, errors.New("invalid Linear pagination cursor")
		}
		cursor = d.Issues.PageInfo.EndCursor
	}
}

func (l *linearBackend) setTimeout(d time.Duration) { l.http.Timeout = d }
