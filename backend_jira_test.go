package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeJira answers the Jira Cloud REST v3 calls jiraBackend makes, in the shapes
// of Atlassian's OpenAPI spec (ADF bodies, status categories, transitions).
type fakeJira struct {
	mu        sync.Mutex
	t         *testing.T
	auth      string
	me        string
	statuses  map[string]jiraStatus
	issues    map[string]*fakeJiraIssue
	order     []string
	noReturn  bool // workflow can't move back to To Do
	counter   int
	oldSearch bool
	lagged    map[string]bool // created but not yet in search unless reconciled
}

type fakeJiraIssue struct {
	key, summary, status, assignee string
	description                    any
	comments                       []map[string]any
}

func st(id, name, cat string) jiraStatus {
	s := jiraStatus{ID: id, Name: name}
	s.StatusCategory.Key = cat
	return s
}

func newFakeJira(t *testing.T) *fakeJira {
	f := &fakeJira{t: t, me: "acc-me", issues: map[string]*fakeJiraIssue{},
		auth: "Basic " + base64.StdEncoding.EncodeToString([]byte("dev@example.com:jira-token")),
		statuses: map[string]jiraStatus{
			"1": st("1", "To Do", "new"), "3": st("3", "In Progress", "indeterminate"),
			"4": st("4", "In Review", "indeterminate"), "5": st("5", "Done", "done"), "6": st("6", "Won't Do", "done"),
		}}
	f.add("Add cursor pagination", "1", "acc-me", "Replace offset paging")
	f.add("Someone else's issue", "1", "acc-other", "")
	f.add("Unassigned bug", "1", "", "")
	f.add("Already shipped", "5", "acc-me", "")
	return f
}

func (f *fakeJira) add(summary, status, assignee, desc string) *fakeJiraIssue {
	f.counter++
	is := &fakeJiraIssue{key: fmt.Sprintf("ENG-%d", f.counter), summary: summary, status: status, assignee: assignee}
	if desc != "" {
		is.description = adfDoc(desc)
	}
	f.issues[is.key] = is
	f.order = append(f.order, is.key)
	return is
}

func (f *fakeJira) json(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeJira) issueJSON(is *fakeJiraIssue) map[string]any {
	var assignee any
	if is.assignee != "" {
		assignee = map[string]string{"accountId": is.assignee, "displayName": "User " + is.assignee}
	}
	return map[string]any{"id": "10" + strings.TrimPrefix(is.key, "ENG-"), "key": is.key, "fields": map[string]any{
		"summary": is.summary, "description": is.description, "status": f.statuses[is.status],
		"priority": map[string]string{"name": "High"}, "assignee": assignee,
		"comment": map[string]any{"comments": is.comments, "total": len(is.comments)}}}
}

func (f *fakeJira) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != f.auth {
		f.json(w, 401, map[string]any{"errorMessages": []string{"Client must be authenticated to access this resource."}})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	path := r.URL.Path
	parts := strings.Split(strings.TrimPrefix(path, "/rest/api/3/"), "/")
	switch {
	case path == "/rest/api/3/search":
		f.oldSearch = true
		f.json(w, 410, map[string]any{"errorMessages": []string{"The requested API has been removed."}})
	case path == "/rest/api/3/myself":
		f.json(w, 200, map[string]string{"accountId": f.me, "displayName": "Dev"})
	case path == "/rest/api/3/search/jql" && r.Method == http.MethodPost:
		jql, _ := body["jql"].(string)
		if !strings.Contains(jql, "assignee = currentUser()") || !strings.Contains(jql, "statusCategory != Done") {
			f.t.Errorf("unexpected JQL %q", jql)
		}
		reconciled := map[string]bool{}
		if ids, ok := body["reconcileIssues"].([]any); ok {
			for _, id := range ids {
				reconciled[fmt.Sprintf("10%v", id)] = true
			}
		}
		var out []any
		for _, k := range f.order {
			is := f.issues[k]
			if f.lagged[k] && !reconciled["10"+f.issueJSON(is)["id"].(string)] {
				continue
			}
			if is.assignee == f.me && f.statuses[is.status].StatusCategory.Key != "done" {
				out = append(out, f.issueJSON(is))
			}
		}
		f.json(w, 200, map[string]any{"issues": out, "isLast": true})
	case path == "/rest/api/3/issue" && r.Method == http.MethodPost:
		fields := body["fields"].(map[string]any)
		if fields["project"].(map[string]any)["key"] != "ENG" || fields["issuetype"].(map[string]any)["name"] != "Task" {
			f.json(w, 400, map[string]any{"errors": map[string]string{"project": "valid project is required"}})
			return
		}
		is := f.add(fields["summary"].(string), "1", fields["assignee"].(map[string]any)["accountId"].(string), "")
		is.description = fields["description"]
		if f.lagged == nil {
			f.lagged = map[string]bool{}
		}
		f.lagged[is.key] = true
		f.json(w, 201, map[string]string{"id": f.issueJSON(is)["id"].(string), "key": is.key})
	case len(parts) >= 2 && parts[0] == "issue":
		is := f.issues[parts[1]]
		if is == nil {
			f.json(w, 404, map[string]any{"errorMessages": []string{"Issue does not exist or you do not have permission to see it."}})
			return
		}
		sub := strings.Join(parts[2:], "/")
		switch {
		case sub == "" && r.Method == http.MethodGet:
			f.json(w, 200, f.issueJSON(is))
		case sub == "assignee" && r.Method == http.MethodPut:
			is.assignee, _ = body["accountId"].(string)
			w.WriteHeader(204)
		case sub == "comment" && r.Method == http.MethodPost:
			b := body["body"].(map[string]any)
			if b["type"] != "doc" || b["version"] != float64(1) {
				f.json(w, 400, map[string]any{"errorMessages": []string{"Comment body is not valid ADF"}})
				return
			}
			is.comments = append(is.comments, map[string]any{"body": b, "created": time.Now().UTC().Format("2006-01-02T15:04:05.000-0700")})
			time.Sleep(2 * time.Millisecond)
			f.json(w, 201, map[string]string{"id": "c"})
		case sub == "transitions" && r.Method == http.MethodGet:
			var ts []any
			for _, id := range []string{"1", "3", "4", "5", "6"} {
				if id == is.status || (f.noReturn && id == "1") {
					continue
				}
				ts = append(ts, map[string]any{"id": "t" + id, "name": "Move to " + f.statuses[id].Name, "to": f.statuses[id]})
			}
			f.json(w, 200, map[string]any{"transitions": ts})
		case sub == "transitions" && r.Method == http.MethodPost:
			id := strings.TrimPrefix(body["transition"].(map[string]any)["id"].(string), "t")
			if _, ok := f.statuses[id]; !ok || id == is.status || (f.noReturn && id == "1") {
				f.json(w, 400, map[string]any{"errorMessages": []string{"Transition id is not valid for this issue."}})
				return
			}
			is.status = id
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func setupJira(t *testing.T, cfg string) (*fakeJira, *httptest.Server, *jiraBackend) {
	t.Helper()
	f := newFakeJira(t)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("TK_HOME", t.TempDir())
	t.Setenv("TK_JIRA_URL", srv.URL)
	t.Setenv("JIRA_API_TOKEN", "jira-token")
	t.Setenv("TK_CLAUDE_LOG_DIR", t.TempDir())
	t.Setenv("TK_CODEX_SESSIONS", t.TempDir())
	t.Setenv("TK_MODE", "jira")
	writeTestConfig(t, cfg)
	be, err := openBackend()
	if err != nil {
		t.Fatal(err)
	}
	return f, srv, be.(*jiraBackend)
}

const jiraCfg = `{"jira":{"site":"https://example.atlassian.net","email":"dev@example.com","project":"ENG"}}`

func TestJiraWorkLoop(t *testing.T) {
	f, _, be := setupJira(t, jiraCfg)
	items, err := be.inbox()
	if err != nil || len(items) != 1 || items[0].Identifier != "ENG-1" || items[0].Status != "todo" || items[0].Priority != "high" {
		t.Fatalf("inbox = %+v, %v", items, err)
	}
	if f.oldSearch {
		t.Fatal("used the removed /rest/api/3/search endpoint")
	}
	if _, err := be.start("ENG-2"); err == nil || !strings.Contains(err.Error(), "assigned to") {
		t.Fatalf("start on someone else's issue: %v", err)
	}
	if _, err := be.start("ENG-4"); err == nil || !strings.Contains(err.Error(), "Done") {
		t.Fatalf("start on a done issue: %v", err)
	}
	if err := cmdStart(be, "ENG-1"); err != nil {
		t.Fatal(err)
	}
	if f.issues["ENG-1"].status != "3" {
		t.Fatalf("after start: status %s", f.issues["ENG-1"].status)
	}
	if err := cmdNote(be, "keyset store done"); err != nil {
		t.Fatal(err)
	}
	s, _ := loadSession()
	_ = be.postCost(s, map[string]any{"idempotencyKey": "k1", "costCents": "30", "costStatus": "estimated"})
	if err := cmdFinish(be, "handoff", "part 1 done\nnext: API endpoint"); err != nil {
		t.Fatal(err)
	}
	if f.issues["ENG-1"].status != "1" {
		t.Fatalf("after handoff: status %s", f.issues["ENG-1"].status)
	}
	last := adfText(f.issues["ENG-1"].comments[len(f.issues["ENG-1"].comments)-1]["body"])
	if !strings.HasPrefix(last, agentNotePrefix+"Handoff: part 1 done\nnext: API endpoint") || !strings.Contains(last, "~$0.30") {
		t.Fatalf("handoff comment = %q", last)
	}
	hc, err := be.context("ENG-1")
	if err != nil || hc.ContinuationSummary == nil || !strings.Contains(hc.ContinuationSummary.Body, "Handoff: part 1 done") ||
		hc.Issue.Description != "Replace offset paging" {
		t.Fatalf("context = %+v %v", hc, err)
	}

	if err := cmdStart(be, "ENG-1"); err != nil {
		t.Fatal(err)
	}
	if err := cmdStart(be, "ENG-3"); err == nil {
		t.Fatal("only one session at a time")
	}
	if err := cmdFinish(be, "done", "tests pass"); err != nil {
		t.Fatal(err)
	}
	if f.issues["ENG-1"].status != "4" {
		t.Fatalf("done should go to In Review, got %s", f.issues["ENG-1"].status)
	}
	// an unassigned issue gets assigned to the developer on start
	if err := cmdStart(be, "ENG-3"); err != nil {
		t.Fatal(err)
	}
	if f.issues["ENG-3"].assignee != "acc-me" || f.issues["ENG-3"].status != "3" {
		t.Fatalf("unassigned issue after start: %+v", f.issues["ENG-3"])
	}
}

func TestJiraWorkflowVariants(t *testing.T) {
	f, _, be := setupJira(t, `{"jira":{"site":"x","email":"dev@example.com","project":"ENG","doneStatus":"Done"}}`)
	is, err := be.createIssue("Order cancellation", "refund rules\n\nsecond paragraph")
	if err != nil || is.Identifier != "ENG-5" || f.issues["ENG-5"].assignee != "acc-me" {
		t.Fatalf("create = %+v, %v", is, err)
	}
	if got := adfText(f.issues["ENG-5"].description); got != "refund rules\n\nsecond paragraph" {
		t.Fatalf("description round trip = %q", got)
	}
	// a workflow with no way back to To Do: handoff keeps the status and says why
	f.noReturn = true
	if err := cmdStart(be, "ENG-5"); err != nil {
		t.Fatal(err)
	}
	if err := cmdFinish(be, "handoff", "paused"); err != nil {
		t.Fatal(err)
	}
	c := adfText(f.issues["ENG-5"].comments[0]["body"])
	if f.issues["ENG-5"].status != "3" || !strings.Contains(c, "no move back to To Do") {
		t.Fatalf("no-return handoff: status %s comment %q", f.issues["ENG-5"].status, c)
	}
	// doneStatus=Done skips the review status
	if err := cmdStart(be, "ENG-5"); err != nil {
		t.Fatal(err)
	}
	if err := cmdFinish(be, "done", ""); err != nil {
		t.Fatal(err)
	}
	if f.issues["ENG-5"].status != "5" {
		t.Fatalf("doneStatus=Done gave %s", f.issues["ENG-5"].status)
	}
	if st := jiraTkStatus(f.statuses["6"]); st != "cancelled" {
		t.Fatalf("Won't Do maps to %s", st)
	}
}

func TestJiraOfflineAndAuth(t *testing.T) {
	_, srv, be := setupJira(t, jiraCfg)
	if _, err := be.inbox(); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if items, err := be.inbox(); err != nil || len(items) != 1 {
		t.Fatalf("offline inbox = %+v, %v", items, err)
	}
	if _, err := be.start("ENG-1"); err == nil {
		t.Fatal("writes must fail offline")
	}
	_, _, be2 := setupJira(t, jiraCfg)
	be2.rc.auth = func(r *http.Request) { r.Header.Set("Authorization", "Basic bad") }
	if _, err := be2.inbox(); err == nil || !strings.Contains(err.Error(), "must be authenticated") {
		t.Fatalf("bad token: %v", err)
	}
	t.Setenv("JIRA_API_TOKEN", "")
	if _, err := newJiraBackend(); err == nil || !strings.Contains(err.Error(), "API token") {
		t.Fatalf("missing token: %v", err)
	}
}

func TestADF(t *testing.T) {
	doc := adfDoc("line one\nline two\n\nsecond para")
	if got := adfText(doc); got != "line one\nline two\n\nsecond para" {
		t.Fatalf("round trip %q", got)
	}
	nested := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "bulletList", "content": []any{
			map[string]any{"type": "listItem", "content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "a"}}}}},
			map[string]any{"type": "listItem", "content": []any{map[string]any{"type": "paragraph", "content": []any{
				map[string]any{"type": "mention", "attrs": map[string]any{"text": "@sam"}}, map[string]any{"type": "text", "text": " b"}}}}}}}}}
	if got := adfText(nested); got != "a\n\n@sam b" {
		t.Fatalf("nested %q", got)
	}
}

// Jira search lags writes; tk passes new issues as reconcileIssues so tk next shows them.
func TestJiraNewIssueVisibleViaReconcile(t *testing.T) {
	_, _, be := setupJira(t, jiraCfg)
	is, err := be.createIssue("Fresh issue", "")
	if err != nil {
		t.Fatal(err)
	}
	items, err := be.inbox()
	found := false
	for _, it := range items {
		found = found || it.Identifier == is.Identifier
	}
	if err != nil || !found {
		t.Fatalf("inbox right after create = %+v, %v", items, err)
	}
}
