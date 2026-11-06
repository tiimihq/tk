package main

import (
	"github.com/tiimihq/tk/internal/board"
)

// boardStore lets the terminal board work on whatever backend tk manages.
type boardStore struct{ m issueManager }

func cmdBoard(be backend) error {
	m, err := requireManager(be)
	if err != nil {
		return err
	}
	return board.Run(boardStore{m})
}

func (b boardStore) Load() ([]board.Issue, []string, error) {
	items, err := b.m.listIssues(true)
	if err != nil {
		return nil, nil, err
	}
	out := make([]board.Issue, len(items))
	for i, d := range items {
		bi := board.Issue{Identifier: cleanText(d.Identifier), Title: cleanText(d.Title), Description: cleanText(d.Description), Status: d.Status,
			Priority: d.Priority, Assignee: d.Assignee, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
			Cost: board.Cost{Events: d.Cost.Events, Cents: d.Cost.Cents, Unpriced: d.Cost.Unpriced}, CostText: d.CostText}
		for _, c := range d.Comments {
			bi.Comments = append(bi.Comments, board.Comment{Author: c.Author, Kind: c.Kind, Body: cleanText(c.Body), CreatedAt: c.CreatedAt})
		}
		out[i] = bi
	}
	var keys []string
	all, _ := loadSessions()
	for _, s := range all {
		keys = append(keys, s.Key)
	}
	return out, keys, nil
}

func (b boardStore) Create(title string) (string, error) {
	is, err := b.m.createIssue(title, "")
	return is.Identifier, err
}

func (b boardStore) Update(key, field, value string) error {
	var p issuePatch
	switch field {
	case "title":
		p.Title = &value
	case "description":
		p.Description = &value
	case "priority":
		p.Priority = &value
	case "status":
		p.Status = &value
	}
	_, err := b.m.updateIssue(key, p)
	return err
}

func (b boardStore) Comment(key, body string) error { return b.m.addHumanComment(key, body) }

func (b boardStore) Cancel(key string) error {
	_, err := cancelIssue(b.m, key, "")
	return err
}
