package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The cost store keeps cost events on this machine for backends that have no
// place for them (Linear, Jira, GitHub): one file per issue under ~/.tk/costs,
// locked while written, de-duplicated by idempotencyKey. A hosted tk service
// can later collect these for team-wide reports.

func costStorePath(key string) string {
	return filepath.Join(tkHome(), "costs", safeFileName(strings.ToUpper(key))+".json")
}

func withCostFile(key string, write bool, fn func(*[]map[string]any) error) error {
	path := costStorePath(key)
	return withFileLock(path+".lock", func() error { return withCostFileLocked(path, write, fn) })
}

func withCostFileLocked(path string, write bool, fn func(*[]map[string]any) error) error {
	var events []map[string]any
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &events); err != nil {
			return fmt.Errorf("corrupt %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := fn(&events); err != nil || !write {
		return err
	}
	raw, _ := json.MarshalIndent(events, "", "  ")
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func costStoreAdd(key string, event map[string]any) error {
	return withCostFile(key, true, func(events *[]map[string]any) error {
		for _, e := range *events {
			if e["idempotencyKey"] == event["idempotencyKey"] {
				return nil
			}
		}
		*events = append(*events, event)
		return nil
	})
}

// costTotals sums recorded cost. Events with no price (a model missing from the
// rate card) are counted separately: their cost is unknown, not zero.
type costTotals struct {
	Events   int
	Cents    float64 // priced events only
	Unpriced int
}

func costStoreTotals(key string) (t costTotals, err error) {
	err = withCostFile(key, false, func(events *[]map[string]any) error {
		t = sumCostEvents(*events)
		return nil
	})
	return
}

func sumCostEvents(events []map[string]any) costTotals {
	var t costTotals
	for _, e := range events {
		t.Events++
		if e["costStatus"] == "unpriced" {
			t.Unpriced++
			continue
		}
		var c float64
		if s, ok := e["costCents"].(string); ok {
			fmt.Sscanf(s, "%g", &c)
		}
		t.Cents += c
	}
	return t
}

// String renders totals honestly: "~$0.42", "~$0.42 + 2 unpriced events", or
// "2 events, not priced" when nothing could be priced.
func (t costTotals) String() string {
	switch {
	case t.Events == 0:
		return "no cost recorded"
	case t.Unpriced == t.Events:
		return fmt.Sprintf("%d cost events, not priced (model not in the rate card)", t.Events)
	case t.Unpriced > 0:
		return fmt.Sprintf("~$%.2f API-equivalent + %d unpriced events (%d events)", t.Cents/100, t.Unpriced, t.Events)
	default:
		return fmt.Sprintf("~$%.2f API-equivalent (%d cost events)", t.Cents/100, t.Events)
	}
}

// costStoreSummaryLine is the cost line added to handoff and done comments.
func costStoreSummaryLine(key string) string {
	t, err := costStoreTotals(key)
	if err != nil || t.Events == 0 {
		return ""
	}
	return "tk cost so far: " + t.String()
}

// costReporter is implemented by backends that keep cost on this machine.
type costReporter interface {
	costSummary(key string) (costTotals, error)
}
