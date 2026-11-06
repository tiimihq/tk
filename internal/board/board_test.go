package board

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// memStore is an in-memory store with tk's rules, for testing the board's keys.
type memStore struct {
	items   []issueDetail
	session []string
	n       int
}

func (s *memStore) find(key string) (*issueDetail, error) {
	for i := range s.items {
		if s.items[i].Identifier == key {
			return &s.items[i], nil
		}
	}
	return nil, fmt.Errorf("issue %s not found", key)
}
func (s *memStore) Load() ([]issueDetail, []string, error) {
	return append([]issueDetail(nil), s.items...), s.session, nil
}
func (s *memStore) Create(title string) (string, error) {
	s.n++
	key := fmt.Sprintf("LOC-%d", s.n)
	s.items = append(s.items, issueDetail{Identifier: key, Title: title, Status: "todo", Priority: "medium"})
	return key, nil
}
func (s *memStore) Update(key, field, value string) error {
	it, err := s.find(key)
	if err != nil {
		return err
	}
	switch field {
	case "title":
		it.Title = value
	case "description":
		it.Description = value
	case "priority":
		it.Priority = value
	case "status":
		it.Status = value
	}
	return nil
}
func (s *memStore) Comment(key, body string) error {
	it, err := s.find(key)
	if err != nil {
		return err
	}
	it.Comments = append(it.Comments, commentView{Author: "you", Kind: "comment", Body: body, CreatedAt: "2026-11-03T04:00:00Z"})
	return nil
}
func (s *memStore) Cancel(key string) error { return s.Update(key, "status", "cancelled") }

func press(m *boardModel, ks ...string) {
	for _, k := range ks {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "ctrl+s":
			msg = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		m.Update(msg)
	}
}

func typeText(m *boardModel, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func newTestBoard(st Store) *boardModel {
	m := newBoardModel(st)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

func TestBoardKeys(t *testing.T) {
	st := &memStore{}
	m := newTestBoard(st)
	if !strings.Contains(m.View().Content, "Todo 0") {
		t.Fatalf("empty board:\n%s", m.View().Content)
	}
	press(m, "n")
	typeText(m, "Add pagination")
	press(m, "enter", "n")
	typeText(m, "Fix refunds")
	press(m, "enter")
	if len(m.items) != 2 || m.flash != "LOC-2 created" {
		t.Fatalf("items=%d flash=%q", len(m.items), m.flash)
	}
	if sel, _ := m.selected(); sel.Identifier != "LOC-2" {
		t.Fatalf("new issue should be selected, got %s", sel.Identifier)
	}
	press(m, ">")
	if it, _ := st.find("LOC-2"); it.Status != "in_progress" {
		t.Fatalf("status = %s", it.Status)
	}
	if sel, _ := m.selected(); sel.Identifier != "LOC-2" || m.col != 1 {
		t.Fatalf("selection didn't follow: col %d %s", m.col, sel.Identifier)
	}
	press(m, "c")
	typeText(m, "looks good")
	press(m, "enter", "p", "e")
	m.input.SetValue("Fix refund rounding")
	press(m, "enter")
	it, _ := st.find("LOC-2")
	if len(it.Comments) != 1 || it.Priority != "high" || it.Title != "Fix refund rounding" {
		t.Fatalf("after edits: %+v", it)
	}
	press(m, "enter")
	if m.mode != modeDetail || !strings.Contains(m.View().Content, "looks good") {
		t.Fatalf("detail view:\n%s", m.View().Content)
	}
	press(m, "d")
	typeText(m, "Round half to even")
	press(m, "ctrl+s")
	if it, _ := st.find("LOC-2"); it.Description != "Round half to even" || m.mode != modeDetail {
		t.Fatalf("description %q mode %v", it.Description, m.mode)
	}
	press(m, "x", "n")
	if it, _ := st.find("LOC-2"); it.Status == "cancelled" {
		t.Fatal("cancel without confirmation")
	}
	press(m, "x", "y", "esc")
	if it, _ := st.find("LOC-2"); it.Status != "cancelled" {
		t.Fatalf("status = %s", it.Status)
	}
	if strings.Contains(m.View().Content, "Cancelled 1") {
		t.Fatal("cancelled column should be hidden by default")
	}
	press(m, "a")
	if !strings.Contains(m.View().Content, "Cancelled 1") {
		t.Fatalf("a should show cancelled:\n%s", m.View().Content)
	}
	press(m, "n")
	typeText(m, "never saved")
	press(m, "esc")
	if len(st.items) != 2 {
		t.Fatal("esc must not create an issue")
	}
}

func TestBoardRefreshShowsAgent(t *testing.T) {
	st := &memStore{}
	m := newTestBoard(st)
	key, _ := st.Create("Agent task")
	_ = st.Update(key, "status", "in_progress")
	st.session = []string{key}
	it, _ := st.find(key)
	it.Comments = append(it.Comments, commentView{Author: "agent", Kind: "note", Body: "progress from the agent", CreatedAt: "2026-11-03T04:00:00Z"})
	m.Update(boardTick{}) // the periodic refresh picks up changes made elsewhere
	if v := m.View().Content; !strings.Contains(v, "agent working on LOC-1") || !strings.Contains(v, "In progress 1") {
		t.Fatalf("board after refresh:\n%s", v)
	}
	press(m, "right", "enter")
	if !strings.Contains(m.View().Content, "progress from the agent") {
		t.Fatalf("detail should show the agent's note:\n%s", m.View().Content)
	}
}
