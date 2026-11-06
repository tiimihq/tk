package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub answers the REST calls githubBackend makes for one repository.
type fakeGitHub struct {
	mu     sync.Mutex
	t      *testing.T
	token  string
	login  string
	repo   string
	issues map[int]*fakeGHIssue
	next   int
	lagged map[int]bool // created but not yet visible in the list (like real GitHub for a few seconds)
}

type fakeGHIssue struct {
	number            int
	title, body       string
	state, reason     string
	labels, assignees []string
	pr                bool
	comments          []map[string]string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, token: "gh-token", login: "dev", repo: "acme/shop", issues: map[int]*fakeGHIssue{}}
	f.add("Add cursor pagination", []string{"dev"}, []string{"priority: high"}, false)
	f.add("Someone else's issue", []string{"sam"}, nil, false)
	f.add("A pull request", []string{"dev"}, nil, true)
	f.add("Unassigned bug", nil, []string{"P0"}, false)
	return f
}

func (f *fakeGitHub) add(title string, assignees, labels []string, pr bool) *fakeGHIssue {
	f.next++
	is := &fakeGHIssue{number: f.next, title: title, state: "open", assignees: assignees, labels: labels, pr: pr}
	f.issues[is.number] = is
	return is
}

func (is *fakeGHIssue) json() map[string]any {
	labels := []map[string]string{}
	for _, l := range is.labels {
		labels = append(labels, map[string]string{"name": l})
	}
	assignees := []map[string]string{}
	for _, a := range is.assignees {
		assignees = append(assignees, map[string]string{"login": a})
	}
	m := map[string]any{"number": is.number, "title": is.title, "body": is.body, "state": is.state, "labels": labels, "assignees": assignees}
	if is.reason != "" {
		m["state_reason"] = is.reason
	}
	if is.pr {
		m["pull_request"] = map[string]string{"url": "https://api.github.com/x"}
	}
	return m
}

func (f *fakeGitHub) reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.reply(w, 401, map[string]string{"message": "Bad credentials"})
		return
	}
	if r.Header.Get("X-GitHub-Api-Version") == "" {
		f.t.Error("missing X-GitHub-Api-Version header")
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.URL.Path == "/user" {
		f.reply(w, 200, map[string]string{"login": f.login})
		return
	}
	prefix := "/repos/" + f.repo + "/issues"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		f.reply(w, 404, map[string]string{"message": "Not Found"})
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			out := []any{}
			for n := 1; n <= f.next; n++ {
				is := f.issues[n]
				if q.Get("state") == "open" && is.state != "open" {
					continue
				}
				if a := q.Get("assignee"); a != "" && !contains(is.assignees, a) {
					continue
				}
				if f.lagged[n] {
					continue
				}
				out = append(out, is.json())
			}
			f.reply(w, 200, out)
		case http.MethodPost:
			is := f.add(body["title"].(string), toStrings(body["assignees"]), nil, false)
			is.body, _ = body["body"].(string)
			if f.lagged == nil {
				f.lagged = map[int]bool{}
			}
			f.lagged[is.number] = true
			f.reply(w, 201, is.json())
		}
		return
	}
	parts := strings.Split(rest, "/")
	n, _ := strconv.Atoi(parts[0])
	is := f.issues[n]
	if is == nil {
		f.reply(w, 404, map[string]string{"message": "Not Found"})
		return
	}
	sub := strings.Join(parts[1:], "/")
	switch {
	case sub == "" && r.Method == http.MethodGet:
		f.reply(w, 200, is.json())
	case sub == "" && r.Method == http.MethodPatch:
		is.state, _ = body["state"].(string)
		is.reason, _ = body["state_reason"].(string)
		f.reply(w, 200, is.json())
	case sub == "assignees" && r.Method == http.MethodPost:
		is.assignees = append(is.assignees, toStrings(body["assignees"])...)
		f.reply(w, 201, is.json())
	case sub == "labels" && r.Method == http.MethodPost: // GitHub creates labels that don't exist yet
		for _, l := range toStrings(body["labels"]) {
			if !contains(is.labels, l) {
				is.labels = append(is.labels, l)
			}
		}
		f.reply(w, 200, []any{})
	case strings.HasPrefix(sub, "labels/") && r.Method == http.MethodDelete:
		name := strings.TrimPrefix(r.URL.EscapedPath(), r.URL.EscapedPath()[:strings.Index(r.URL.EscapedPath(), "/labels/")+8])
		name = strings.ReplaceAll(name, "%20", " ")
		if !contains(is.labels, name) {
			f.reply(w, 404, map[string]string{"message": "Label does not exist"})
			return
		}
		var keep []string
		for _, l := range is.labels {
			if l != name {
				keep = append(keep, l)
			}
		}
		is.labels = keep
		f.reply(w, 200, []any{})
	case sub == "comments" && r.Method == http.MethodGet:
		out := []any{}
		for _, c := range is.comments {
			out = append(out, c)
		}
		f.reply(w, 200, out)
	case sub == "comments" && r.Method == http.MethodPost:
		is.comments = append(is.comments, map[string]string{"body": body["body"].(string), "created_at": time.Now().UTC().Format(time.RFC3339Nano)})
		time.Sleep(2 * time.Millisecond)
		f.reply(w, 201, map[string]int{"id": len(is.comments)})
	default:
		f.reply(w, 404, map[string]string{"message": "Not Found"})
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}

func toStrings(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

func setupGitHub(t *testing.T, cfg string) (*fakeGitHub, *httptest.Server, *githubBackend) {
	t.Helper()
	f := newFakeGitHub(t)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("TK_HOME", t.TempDir())
	t.Setenv("TK_GITHUB_URL", srv.URL)
	t.Setenv("TK_GITHUB_TOKEN", "gh-token")
	t.Setenv("TK_CLAUDE_LOG_DIR", t.TempDir())
	t.Setenv("TK_CODEX_SESSIONS", t.TempDir())
	t.Setenv("TK_MODE", "github")
	writeTestConfig(t, cfg)
	be, err := openBackend()
	if err != nil {
		t.Fatal(err)
	}
	return f, srv, be.(*githubBackend)
}

func TestGitHubWorkLoop(t *testing.T) {
	f, _, be := setupGitHub(t, `{"github":{"repo":"acme/shop"}}`)
	items, err := be.inbox()
	if err != nil || len(items) != 1 || items[0].Identifier != "#1" || items[0].Priority != "high" || items[0].Status != "todo" {
		t.Fatalf("inbox (no PRs, no one else's) = %+v, %v", items, err)
	}
	if _, err := be.start("2"); err == nil || !strings.Contains(err.Error(), "assigned to sam") {
		t.Fatalf("start on someone else's issue: %v", err)
	}
	if _, err := be.start("3"); err == nil || !strings.Contains(err.Error(), "pull request") {
		t.Fatalf("start on a PR: %v", err)
	}
	if _, err := be.start("other/repo#1"); err == nil {
		t.Fatal("key from another repo accepted")
	}
	if err := cmdStart(be, "#1"); err != nil {
		t.Fatal(err)
	}
	if !contains(f.issues[1].labels, "in progress") {
		t.Fatalf("labels after start: %v", f.issues[1].labels)
	}
	if err := cmdNote(be, "keyset store done"); err != nil {
		t.Fatal(err)
	}
	s, _ := loadSession()
	_ = be.postCost(s, map[string]any{"idempotencyKey": "k1", "costCents": "25", "costStatus": "estimated"})
	if err := cmdFinish(be, "handoff", "part 1 done; next: API endpoint"); err != nil {
		t.Fatal(err)
	}
	if contains(f.issues[1].labels, "in progress") || f.issues[1].state != "open" {
		t.Fatalf("after handoff: %+v", f.issues[1])
	}
	last := f.issues[1].comments[len(f.issues[1].comments)-1]["body"]
	if !strings.HasPrefix(last, agentNotePrefix+"Handoff: part 1") || !strings.Contains(last, "~$0.25") {
		t.Fatalf("handoff comment = %q", last)
	}
	hc, err := be.context("1")
	if err != nil || hc.ContinuationSummary == nil || !strings.Contains(hc.ContinuationSummary.Body, "- Handoff: part 1 done") {
		t.Fatalf("context = %+v %v", hc, err)
	}
	if tot, _ := be.costSummary("#1"); tot.Events != 1 || tot.Cents != 25 {
		t.Fatalf("cost = %+v", tot)
	}
	if err := cmdStart(be, "1"); err != nil {
		t.Fatal(err)
	}
	if err := cmdFinish(be, "done", "tests pass"); err != nil {
		t.Fatal(err)
	}
	if f.issues[1].state != "closed" || f.issues[1].reason != "completed" {
		t.Fatalf("done should close as completed: %+v", f.issues[1])
	}
	// unassigned: start assigns it to the developer
	if err := cmdStart(be, "4"); err != nil {
		t.Fatal(err)
	}
	if !contains(f.issues[4].assignees, "dev") {
		t.Fatalf("assignees after start: %v", f.issues[4].assignees)
	}
}

func TestGitHubReviewLabelAndCreate(t *testing.T) {
	f, _, be := setupGitHub(t, `{"github":{"repo":"acme/shop","inProgressLabel":"wip","reviewLabel":"needs review"}}`)
	is, err := be.createIssue("Order cancellation", "refund rules")
	if err != nil || is.Identifier != "#5" || !contains(f.issues[5].assignees, "dev") || f.issues[5].body != "refund rules" {
		t.Fatalf("create = %+v, %v", is, err)
	}
	if err := cmdStart(be, "5"); err != nil {
		t.Fatal(err)
	}
	if err := cmdFinish(be, "done", ""); err != nil {
		t.Fatal(err)
	}
	gi := f.issues[5]
	if gi.state != "open" || !contains(gi.labels, "needs review") || contains(gi.labels, "wip") {
		t.Fatalf("review label flow: %+v", gi)
	}
	if got, _ := be.issue("5"); got.Status != "in_review" {
		t.Fatalf("status = %s", got.Status)
	}
	f.issues[2].state, f.issues[2].reason = "closed", "not_planned"
	if got, _ := be.issue("2"); got.Status != "cancelled" {
		t.Fatalf("not_planned maps to %s", got.Status)
	}
}

func TestGitHubOfflineAndAuth(t *testing.T) {
	_, srv, be := setupGitHub(t, `{"github":{"repo":"acme/shop"}}`)
	if _, err := be.inbox(); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if items, err := be.inbox(); err != nil || len(items) != 1 {
		t.Fatalf("offline inbox = %+v, %v", items, err)
	}
	_, _, be2 := setupGitHub(t, `{"github":{"repo":"acme/shop"}}`)
	be2.rc.auth = func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong")
		r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	if _, err := be2.inbox(); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("bad token: %v", err)
	}
	writeTestConfig(t, `{"github":{"repo":"not a repo"}}`)
	if _, err := newGithubBackend(); err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Fatalf("bad repo: %v", err)
	}
}

// GitHub's list lags new issues by seconds; tk next must still show what tk new just made.
func TestGitHubNewIssueVisibleBeforeListCatchesUp(t *testing.T) {
	f, _, be := setupGitHub(t, `{"github":{"repo":"acme/shop"}}`)
	is, err := be.createIssue("Fresh issue", "")
	if err != nil {
		t.Fatal(err)
	}
	items, err := be.inbox()
	if err != nil || len(items) != 2 || items[0].Identifier != is.Identifier {
		t.Fatalf("inbox right after create = %+v, %v", items, err)
	}
	f.lagged = nil // list caught up: no duplicate
	if items, _ := be.inbox(); len(items) != 2 {
		t.Fatalf("duplicate after the list caught up: %+v", items)
	}
}
