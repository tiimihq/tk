package main

import (
	"testing"
)

func TestIssueCommands(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	lb, _ := newLocalBackend()
	lb.createIssue("One", "")
	lb.createIssue("Two", "")
	if err := cmdEdit(lb, "LOC-1", []string{"title=One, renamed", "priority=urgent", "status=in_progress"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdEdit(lb, "LOC-1", []string{"priority=extreme"}); err == nil {
		t.Fatal("bad priority accepted")
	}
	if err := cmdEdit(lb, "LOC-1", []string{"colour=red"}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := cmdComment(lb, "LOC-1", "  "); err == nil {
		t.Fatal("empty comment accepted")
	}
	if err := cmdCancel(lb, "LOC-2", "duplicate of LOC-1"); err != nil {
		t.Fatal(err)
	}
	open, _ := lb.listIssues(false)
	all, _ := lb.listIssues(true)
	if len(open) != 1 || len(all) != 2 || open[0].Title != "One, renamed" || open[0].Priority != "urgent" {
		t.Fatalf("open=%+v all=%d", open, len(all))
	}
	d, _ := lb.issueDetailOf("LOC-2")
	if d.Status != "cancelled" || d.Comments[0].Kind != "cancelled" || d.Comments[0].Author != "you" {
		t.Fatalf("cancel: %+v", d)
	}

	// an agent finishing an issue a person cancelled meanwhile keeps it cancelled
	s, _ := lb.start("LOC-1")
	_ = cancelIssueOrFail(t, lb, "LOC-1")
	if st, _ := lb.finish(&s, "done", "finished anyway"); st != "cancelled" {
		t.Fatalf("finish on a cancelled issue gave %s", st)
	}

	// tracker backends point people at their own app
	if _, err := requireManager(&linearBackend{}); err == nil {
		t.Fatal("linear should not claim issue management yet")
	}
}

func cancelIssueOrFail(t *testing.T, m issueManager, key string) issueDetail {
	t.Helper()
	d, err := cancelIssue(m, key, "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The board's store works against the real local backend.
func TestBoardStore(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	lb, _ := newLocalBackend()
	st := boardStore{lb}
	key, err := st.Create("Title with -- dashes and \"quotes\"")
	if err != nil || key != "LOC-1" {
		t.Fatalf("create: %q %v", key, err)
	}
	for _, f := range [][2]string{{"status", "in_progress"}, {"priority", "urgent"}, {"description", "line one\nline two"}} {
		if err := st.Update(key, f[0], f[1]); err != nil {
			t.Fatalf("update %s: %v", f[0], err)
		}
	}
	if err := st.Update(key, "priority", "extreme"); err == nil {
		t.Fatal("bad priority accepted")
	}
	if err := st.Comment(key, "a comment"); err != nil {
		t.Fatal(err)
	}
	s, _ := lb.start(key)
	s.SessionID = "s-1"
	_ = saveSession(s)
	_ = lb.postCost(&s, map[string]any{"idempotencyKey": "k", "costCents": "12", "costStatus": "estimated"})
	items, session, err := st.Load()
	if err != nil || len(items) != 1 || len(session) != 1 || session[0] != key {
		t.Fatalf("load: %+v %q %v", items, session, err)
	}
	it := items[0]
	if it.Title != "Title with -- dashes and \"quotes\"" || it.Priority != "urgent" || it.Description != "line one\nline two" ||
		len(it.Comments) != 1 || it.Comments[0].Author != "you" || it.Cost.Events != 1 || it.Cost.Cents != 12 {
		t.Fatalf("round trip: %+v", it)
	}
	if err := st.Cancel(key); err != nil {
		t.Fatal(err)
	}
	if items, _, _ := st.Load(); items[0].Status != "cancelled" {
		t.Fatalf("cancel: %s", items[0].Status)
	}
}
