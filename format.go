package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// inboxItem is the subset of /api/agents/me/inbox-lite that tk shows.
type inboxItem struct {
	Identifier             string `json:"identifier"`
	Title                  string `json:"title"`
	Status                 string `json:"status"`
	Priority               string `json:"priority"`
	DependencyReady        bool   `json:"dependencyReady"`
	UnresolvedBlockerCount int    `json:"unresolvedBlockerCount"`
	ActiveRun              *struct {
		Execution *struct {
			Phase string `json:"phase"`
		} `json:"execution"`
	} `json:"activeRun"`
}

// issue is the subset of an issue that tk shows.
type issue struct {
	Identifier      string `json:"identifier"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Status          string `json:"status"`
	Priority        string `json:"priority"`
	AssigneeAgentID string `json:"assigneeAgentId"`
	DelegateAgentID string `json:"delegateAgentId,omitempty"`
	AssigneeUserID  string `json:"assigneeUserId"`
	BlockedBy       []struct {
		Identifier string `json:"identifier"`
		Status     string `json:"status"`
	} `json:"blockedBy"`
}

// heartbeatContext is the subset of /api/issues/:id/heartbeat-context that tk shows.
type heartbeatContext struct {
	Issue               issue `json:"issue"`
	ContinuationSummary *struct {
		Body string `json:"body"`
	} `json:"continuationSummary"`
	CommentCursor *struct {
		TotalComments   int    `json:"totalComments"`
		LatestCommentAt string `json:"latestCommentAt"`
	} `json:"commentCursor"`
	TaskQuestionContext *struct {
		Questions []json.RawMessage `json:"questions"`
	} `json:"taskQuestionContext"`
}

func formatInboxLine(it inboxItem) string {
	parts := []string{it.Identifier, it.Priority, it.Status, it.Title}
	if it.UnresolvedBlockerCount > 0 {
		parts = append(parts, fmt.Sprintf("blocked:%d", it.UnresolvedBlockerCount))
	}
	if it.ActiveRun != nil && it.ActiveRun.Execution != nil && it.ActiveRun.Execution.Phase != "" {
		parts = append(parts, "run:"+it.ActiveRun.Execution.Phase)
	}
	return cleanText(strings.Join(parts, " | "))
}

func formatPeek(is issue) string {
	assignee := "unassigned"
	switch {
	case is.AssigneeAgentID != "":
		assignee = "agent"
	case is.AssigneeUserID != "":
		assignee = "human"
	}
	parts := []string{is.Identifier, is.Status, is.Priority, is.Title, "blockers:" + blockerList(is), "assignee:" + assignee}
	if is.DelegateAgentID != "" {
		parts = append(parts, "delegate:"+is.DelegateAgentID)
	}
	return cleanText(strings.Join(parts, " | "))
}

func blockerList(is issue) string {
	var open []string
	for _, b := range is.BlockedBy {
		if b.Status != "done" && b.Status != "cancelled" {
			open = append(open, b.Identifier)
		}
	}
	if len(open) == 0 {
		return "none"
	}
	return strings.Join(open, ",")
}

// formatResume renders the compact brief an agent reads before working on an issue.
func formatResume(hc heartbeatContext, maxSummary int) string {
	is := hc.Issue
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s [%s, %s]\n", is.Identifier, is.Title, is.Status, is.Priority)
	if goal := oneLine(is.Description); goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n", goal)
	}
	fmt.Fprintf(&b, "Blockers: %s\n", blockerList(is))
	if hc.TaskQuestionContext != nil && len(hc.TaskQuestionContext.Questions) > 0 {
		fmt.Fprintf(&b, "Open questions: %d\n", len(hc.TaskQuestionContext.Questions))
	}
	total := 0
	if hc.CommentCursor != nil {
		total = hc.CommentCursor.TotalComments
	}
	if hc.ContinuationSummary != nil {
		if s := compactSummary(hc.ContinuationSummary.Body, maxSummary); s != "" {
			if first, rest, _ := strings.Cut(s, "\n"); first == "Notes:" && total > 0 { // tk's own list of comments
				label := fmt.Sprintf("All %d comments, oldest first (latest %s):", total, shortTime(hc.CommentCursor.LatestCommentAt))
				if strings.HasPrefix(rest, "(") {
					label = fmt.Sprintf("Comments (%d, newest that fit; tk show %s for all):", total, is.Identifier)
					_, rest, _ = strings.Cut(rest, "\n")
				}
				s = label + "\n" + rest
				total = 0
			}
			if first, _, _ := strings.Cut(s, "\n"); !strings.HasSuffix(first, ":") {
				b.WriteString("Summary:\n") // a summary that has no heading of its own
			}
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	if total > 0 { // comments counted but not listed (e.g. Paperclip)
		fmt.Fprintf(&b, "Comments: %d, latest %s\n", total, shortTime(hc.CommentCursor.LatestCommentAt))
	}
	return cleanText(strings.TrimRight(b.String(), "\n"))
}

// compactSummary drops the parts of Paperclip's continuation summary that the
// header line already states, collapses whitespace, and caps the length.
func compactSummary(body string, maxChars int) string {
	skipPrefixes := []string{
		"# Continuation Summary", "- Issue:", "- Status:", "- Priority:",
		"- Current mode:", "- Last updated by run:", "- Agent:",
	}
	var kept []string
	var objective bool // the Objective section repeats the Goal line, so it is dropped
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## Objective") {
			objective = true
			continue
		}
		if objective {
			if strings.HasPrefix(line, "## ") {
				objective = false
			} else {
				continue
			}
		}
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(line, p) {
				skip = true
				break
			}
		}
		if skip || line == "No explicit acceptance criteria captured." {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			line = sectionMarker + strings.TrimPrefix(line, "## ") + ":"
		}
		kept = append(kept, strings.Join(strings.Fields(line), " "))
	}
	kept = dropEmptySections(kept)
	out := strings.Join(kept, "\n")
	if maxChars <= 0 || len(out) <= maxChars {
		return out
	}
	// One list of notes (tk's own briefs): keep the newest notes that fit, since the
	// latest handoff matters most, and say how many earlier ones were left out.
	if len(kept) > 1 && strings.HasSuffix(kept[0], ":") && allBullets(kept[1:]) {
		notes := kept[1:]
		size, i := len(kept[0]), len(notes)
		for i > 0 && size+len(notes[i-1])+1 <= maxChars {
			size += len(notes[i-1]) + 1
			i--
		}
		if i == len(notes) { // even the newest note is too long: keep its start
			last := notes[i-1]
			notes[i-1] = strings.TrimSpace(last[:max(2, min(len(last), maxChars-len(kept[0])))]) + " …"
			i--
		}
		head := kept[0]
		if i > 0 {
			head += fmt.Sprintf("\n(%d earlier notes: tk show)", i)
		}
		return head + "\n" + strings.Join(notes[i:], "\n")
	}
	return strings.TrimSpace(out[:maxChars]) + " … (tk show)"
}

func allBullets(lines []string) bool {
	for _, l := range lines {
		if !strings.HasPrefix(l, "- ") {
			return false
		}
	}
	return true
}

// sectionMarker tags heading lines inside compactSummary so empty sections can be dropped.
const sectionMarker = "\x00"

// dropEmptySections removes headings with no content under them and strips the marker.
func dropEmptySections(lines []string) []string {
	var out []string
	for i, line := range lines {
		if strings.HasPrefix(line, sectionMarker) {
			if i+1 >= len(lines) || strings.HasPrefix(lines[i+1], sectionMarker) {
				continue
			}
			line = strings.TrimPrefix(line, sectionMarker)
		}
		out = append(out, line)
	}
	return out
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func shortTime(iso string) string {
	if len(iso) >= 16 {
		return strings.Replace(iso[:16], "T", " ", 1)
	}
	return iso
}

// localTime shows a stored UTC timestamp in the local time zone.
func localTime(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.Local().Format("Jan 2 15:04")
}
