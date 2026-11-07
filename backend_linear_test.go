package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLinear answers the GraphQL operations linearBackend sends, using the
// shapes of Linear's public schema, and keeps issues in memory.
type fakeLinear struct {
	mu          sync.Mutex
	key         string
	viewer      string
	states      []linearState
	issues      map[string]*fakeIssue // by identifier
	counter     int
	requests    int
	lastProject string
}

type fakeIssue struct {
	ID, Identifier, Title, Description, StateID, Assignee, Delegate string
	Comments                                                        []map[string]any
}

func newFakeLinear() *fakeLinear {
	f := &fakeLinear{key: "lin_api_test", viewer: "user-me", issues: map[string]*fakeIssue{}, states: []linearState{
		{ID: "st-backlog", Name: "Backlog", Type: "backlog", Position: 0},
		{ID: "st-todo", Name: "Todo", Type: "unstarted", Position: 1},
		{ID: "st-prog", Name: "In Progress", Type: "started", Position: 2},
		{ID: "st-review", Name: "In Review", Type: "started", Position: 3},
		{ID: "st-done", Name: "Done", Type: "completed", Position: 4},
		{ID: "st-cancel", Name: "Canceled", Type: "canceled", Position: 5},
	}}
	f.add("Add cursor pagination", "st-todo", "user-me")
	f.add("Someone else's issue", "st-todo", "user-other")
	return f
}

func (f *fakeLinear) add(title, state, assignee string) *fakeIssue {
	f.counter++
	is := &fakeIssue{ID: fmt.Sprintf("uuid-%d", f.counter), Identifier: fmt.Sprintf("ENG-%d", f.counter), Title: title, StateID: state, Assignee: assignee}
	f.issues[is.Identifier] = is
	return is
}

func (f *fakeLinear) state(id string) linearState {
	for _, s := range f.states {
		if s.ID == id {
			return s
		}
	}
	return linearState{}
}

func (f *fakeLinear) byID(id string) *fakeIssue {
	for _, is := range f.issues {
		if is.ID == id || is.Identifier == id {
			return is
		}
	}
	return nil
}

func (f *fakeLinear) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if r.Header.Get("Authorization") != f.key {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"errors":[{"message":"Authentication required"}]}`)
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	q, v := req.Query, req.Variables
	var data any
	switch {
	case strings.Contains(q, "issues(first: 50, after:"):
		var nodes []map[string]any
		for _, is := range f.issues {
			st := f.state(is.StateID)
			if is.Assignee == v["owner"] && (is.Delegate == "" || is.Delegate == v["agent"]) && (st.Type == "unstarted" || st.Type == "started") {
				nodes = append(nodes, map[string]any{"identifier": is.Identifier, "title": is.Title, "state": st})
			}
		}
		data = map[string]any{"issues": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}
	case strings.Contains(q, "assignedIssues"):
		var nodes []map[string]any
		for i := 1; i <= f.counter; i++ {
			is := f.issues[fmt.Sprintf("ENG-%d", i)]
			st := f.state(is.StateID)
			wanted := strings.Contains(q, `"`+st.Type+`"`) // honour the query's state filter
			if is.Assignee == f.viewer && wanted && (st.Type == "backlog" || st.Type == "unstarted" || st.Type == "started") {
				nodes = append(nodes, map[string]any{"identifier": is.Identifier, "title": is.Title, "priority": 3, "state": st})
			}
		}
		data = map[string]any{"viewer": map[string]any{"id": f.viewer, "assignedIssues": map[string]any{"nodes": nodes}}}
	case strings.Contains(q, "viewer { id }"):
		data = map[string]any{"viewer": map[string]any{"id": f.viewer}}
	case strings.Contains(q, "issue(id: $id)"):
		is := f.byID(v["id"].(string))
		if is == nil {
			fmt.Fprint(w, `{"errors":[{"message":"Entity not found: Issue"}],"data":null}`)
			return
		}
		var assignee any
		if is.Assignee != "" {
			assignee = map[string]any{"id": is.Assignee}
		}
		comments := is.Comments
		if len(comments) > 5 {
			comments = comments[len(comments)-5:]
		}
		data = map[string]any{"issue": map[string]any{"id": is.ID, "identifier": is.Identifier, "title": is.Title,
			"description": is.Description, "priority": 3, "state": f.state(is.StateID), "assignee": assignee, "delegate": func() any {
				if is.Delegate != "" {
					return map[string]any{"id": is.Delegate}
				}
				return nil
			}(),
			"team":     map[string]any{"id": "team-eng", "states": map[string]any{"nodes": f.states}},
			"comments": map[string]any{"nodes": comments}}}
	case strings.Contains(q, "issueUpdate"):
		is := f.byID(v["id"].(string))
		in := v["input"].(map[string]any)
		if s, ok := in["stateId"].(string); ok {
			is.StateID = s
		}
		if a, ok := in["assigneeId"].(string); ok {
			is.Assignee = a
		}
		if a, ok := in["delegateId"].(string); ok {
			is.Delegate = a
		}
		data = map[string]any{"issueUpdate": map[string]any{"success": true}}
	case strings.Contains(q, "commentCreate"):
		in := v["input"].(map[string]any)
		is := f.byID(in["issueId"].(string))
		is.Comments = append(is.Comments, map[string]any{"body": in["body"], "createdAt": time.Now().UTC().Format(time.RFC3339Nano)})
		time.Sleep(2 * time.Millisecond) // distinct timestamps
		data = map[string]any{"commentCreate": map[string]any{"success": true}}
	case strings.Contains(q, "project(id: $id)"):
		data = map[string]any{"project": map[string]any{"id": "proj-uuid"}}
	case strings.Contains(q, "teams(filter"):
		var nodes []any
		if v["key"] == "ENG" {
			nodes = append(nodes, map[string]any{"id": "team-eng", "states": map[string]any{"nodes": f.states}})
		}
		data = map[string]any{"teams": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issueCreate"):
		in := v["input"].(map[string]any)
		state, _ := in["stateId"].(string)
		if state == "" {
			state = "st-backlog" // Linear's default
		}
		is := f.add(in["title"].(string), state, in["assigneeId"].(string))
		if in["projectId"] != nil && in["projectId"] != "proj-uuid" {
			panic("bad projectId")
		}
		is.Delegate, _ = in["delegateId"].(string)
		f.lastProject, _ = in["projectId"].(string)
		data = map[string]any{"issueCreate": map[string]any{"issue": map[string]any{"id": is.ID, "identifier": is.Identifier,
			"title": is.Title, "priority": 0, "state": f.state(is.StateID)}}}
	default:
		fmt.Fprintf(w, `{"errors":[{"message":"fake: unhandled query"}]}`)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func setupLinear(t *testing.T) (*fakeLinear, *memoryServer, *linearBackend) {
	t.Helper()
	f := newFakeLinear()
	srv := newMemoryServer(f)
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("TK_HOME", home)
	t.Setenv("TK_LINEAR_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", f.key)
	t.Setenv("TK_CLAUDE_LOG_DIR", t.TempDir())
	t.Setenv("TK_MODE", "linear")
	be, err := openBackend()
	if err != nil {
		t.Fatal(err)
	}
	be.(*linearBackend).http = srv.Client()
	return f, srv, be.(*linearBackend)
}

func TestLinearWorkLoop(t *testing.T) {
	f, _, be := setupLinear(t)

	items, err := be.inbox()
	if err != nil || len(items) != 1 || items[0].Identifier != "ENG-1" || items[0].Status != "todo" {
		t.Fatalf("inbox = %+v, %v", items, err)
	}
	if _, err := be.start("ENG-2"); err == nil || !strings.Contains(err.Error(), "someone else") {
		t.Fatalf("start on another person's issue: %v", err)
	}
	if err := cmdStart(be, "ENG-1"); err != nil {
		t.Fatal(err)
	}
	if got := f.issues["ENG-1"].StateID; got != "st-prog" {
		t.Fatalf("after start state = %s, want In Progress", got)
	}
	if err := cmdNote(be, "keyset store done"); err != nil {
		t.Fatal(err)
	}
	s, _ := loadSession()
	if err := be.postCost(s, map[string]any{"idempotencyKey": "k1", "costCents": "12.5"}); err != nil {
		t.Fatal(err)
	}
	_ = be.postCost(s, map[string]any{"idempotencyKey": "k1", "costCents": "12.5"}) // repeat is ignored
	if err := cmdFinish(be, "handoff", "part 1 done; next: API endpoint"); err != nil {
		t.Fatal(err)
	}
	if got := f.issues["ENG-1"].StateID; got != "st-todo" {
		t.Fatalf("after handoff state = %s, want Todo", got)
	}
	last := f.issues["ENG-1"].Comments[len(f.issues["ENG-1"].Comments)-1]["body"].(string)
	if !strings.HasPrefix(last, linearNotePrefix+"Handoff: part 1") || !strings.Contains(last, "~$0.12") || !strings.Contains(last, "(1 cost events)") {
		t.Fatalf("handoff comment = %q", last)
	}
	hc, err := be.context("ENG-1")
	if err != nil || hc.ContinuationSummary == nil || !strings.Contains(hc.ContinuationSummary.Body, "- Handoff: part 1 done") ||
		strings.Contains(hc.ContinuationSummary.Body, linearNotePrefix) {
		t.Fatalf("context = %+v, %v", hc.ContinuationSummary, err)
	}
	if err := cmdStart(be, "ENG-1"); err != nil {
		t.Fatal(err)
	}
	if err := cmdFinish(be, "done", "tests pass"); err != nil {
		t.Fatal(err)
	}
	if got := f.issues["ENG-1"].StateID; got != "st-review" {
		t.Fatalf("after done state = %s, want In Review", got)
	}
	if tot, _ := be.costSummary("ENG-1"); tot.Events != 1 || tot.Cents != 12.5 || tot.Unpriced != 0 {
		t.Fatalf("cost = %+v", tot)
	}
	if err := cmdStart(be, "ENG-9"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("start unknown issue: %v", err)
	}
}

func TestLinearDoneStateConfigAndCreate(t *testing.T) {
	f, _, be := setupLinear(t)
	if _, err := be.createIssue("x", ""); err == nil || !strings.Contains(err.Error(), "team") {
		t.Fatalf("create without team: %v", err)
	}
	writeTestConfig(t, `{"mode":"linear","linear":{"team":"ENG","doneState":"Done","project":"d4a4920ff979"}}`)
	is, err := be.createIssue("Order cancellation", "refund rules")
	if err != nil || is.Identifier != "ENG-3" || f.issues["ENG-3"].Assignee != "user-me" {
		t.Fatalf("create = %+v, %v", is, err)
	}
	if items, _ := be.inbox(); len(items) != 2 || items[1].Identifier != "ENG-3" || items[1].Status != "todo" {
		t.Fatalf("new issue should be in Todo and listed by tk next: %+v", items)
	}
	if f.lastProject != "proj-uuid" {
		t.Fatalf("issue not created in the configured project: %q", f.lastProject)
	}
	if err := cmdStart(be, "ENG-3"); err != nil {
		t.Fatal(err)
	}
	if err := cmdFinish(be, "done", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.issues["ENG-3"].StateID; got != "st-done" {
		t.Fatalf("done with doneState=Done gave %s", got)
	}
}

func TestLinearOfflineReadsAndAuth(t *testing.T) {
	_, srv, be := setupLinear(t)
	if _, err := be.inbox(); err != nil { // fills the cache
		t.Fatal(err)
	}
	srv.Close()
	items, err := be.inbox()
	if err != nil || len(items) != 1 {
		t.Fatalf("offline inbox = %+v, %v", items, err)
	}
	if _, err := be.start("ENG-1"); err == nil {
		t.Fatal("writes must fail while Linear is unreachable")
	}

	_, _, be2 := setupLinear(t)
	be2.key = "wrong"
	if _, err := be2.inbox(); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("bad key: %v", err)
	}
}

func writeTestConfig(t *testing.T, body string) {
	t.Helper()
	if err := writeFileAll(configPath(), body); err != nil {
		t.Fatal(err)
	}
}

func TestLinearInboxIncludesBacklogInProgressFirst(t *testing.T) {
	f, _, be := setupLinear(t)
	f.add("Triage later", "st-backlog", "user-me")
	f.add("Already going", "st-prog", "user-me")
	items, err := be.inbox()
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range items {
		titles = append(titles, it.Title)
	}
	got := strings.Join(titles, " | ")
	if got != "Already going | Add cursor pagination | Triage later" {
		t.Fatalf("inbox order = %q (want started, then todo, then backlog)", got)
	}
}
