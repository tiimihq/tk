package main

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// tk receipt: what your coding agents cost, per issue, from the logs they have
// already written. No tracker, no tk init: the issue comes from the git branch
// each reply was made on (eng-142-fix-refunds → ENG-142, 123-fix → #123).

// receiptRow is one line of the receipt: an issue, or a project + branch.
type receiptRow struct {
	Key      string          `json:"key"`   // "ENG-142", "#123", or "" for a branch with no issue
	Title    string          `json:"title"` // from the branch name
	Project  string          `json:"project"`
	Branch   string          `json:"branch"`
	Cents    float64         `json:"cents"`
	Replies  int             `json:"replies"`
	Unpriced int             `json:"unpriced"`
	Sessions map[string]bool `json:"-"`
	Agents   map[string]bool `json:"-"`
	NSession int             `json:"sessions"`
	AgentLst []string        `json:"agents"`
	First    time.Time       `json:"first"`
	Last     time.Time       `json:"last"`
}

type receipt struct {
	Since     time.Time          `json:"since"`
	Until     time.Time          `json:"until"`
	Cents     float64            `json:"cents"`
	ByAgent   map[string]float64 `json:"byAgentCents"`
	Replies   int                `json:"replies"`
	Unpriced  int                `json:"unpriced"`
	Sessions  int                `json:"sessions"`
	Rows      []*receiptRow      `json:"rows"`
	IssueRows int                `json:"issueRows"`
}

var (
	// prefix-number issue keys inside a branch: ENG-142, abc-12. The prefix needs a
	// letter first and at most 10 characters, like Linear and Jira team keys.
	issueInBranch = regexp.MustCompile(`(?i)(?:^|[/_-])([a-z][a-z0-9]{1,9})-(\d{1,6})(?:$|[/_-])`)
	// GitHub's "gh issue develop" and the web UI make branches like 123-fix-login.
	ghIssueBranch = regexp.MustCompile(`^(?:[^/]+/)*(\d{1,7})-[a-z]`)
	// Words that look like issue prefixes in branch names but aren't.
	notIssuePrefix = map[string]bool{
		"feature": true, "feat": true, "fix": true, "bugfix": true, "hotfix": true, "release": true,
		"chore": true, "wip": true, "test": true, "tests": true, "update": true, "bump": true, "v": true,
		"rc": true, "dependabot": true, "renovate": true, "revert": true, "draft": true, "docs": true,
		"refactor": true, "build": true, "ci": true, "perf": true, "style": true, "patch": true,
		"part": true, "step": true, "phase": true, "sprint": true, "week": true, "day": true, "pr": true,
		"issue": true, "ticket": true, "task": true, "story": true, "bug": true, "spike": true, "try": true,
	}
)

// issueFromBranch returns the issue key a branch names, and a readable title from
// the rest of the branch: ("prateek/eng-142-fix-refund-rounding") → ENG-142, "fix refund rounding".
func issueFromBranch(branch string) (key, title string) {
	b := strings.TrimSpace(branch)
	if b == "" || b == "HEAD" {
		return "", ""
	}
	for _, m := range issueInBranch.FindAllStringSubmatchIndex(b, -1) {
		prefix := strings.ToLower(b[m[2]:m[3]])
		if notIssuePrefix[prefix] || letterCount(prefix) < 2 { // v1-2, r3-4: versions, not issues
			continue
		}
		key = strings.ToUpper(prefix) + "-" + b[m[4]:m[5]]
		return key, branchTitle(b[m[5]:])
	}
	if m := ghIssueBranch.FindStringSubmatch(b); m != nil {
		rest := b[strings.Index(b, m[1]+"-")+len(m[1]):]
		return "#" + m[1], branchTitle(rest)
	}
	return "", ""
}

func branchTitle(rest string) string {
	t := strings.NewReplacer("-", " ", "_", " ", "/", " ").Replace(rest)
	t = strings.Join(strings.Fields(t), " ")
	if len(t) > 40 {
		t = strings.TrimSpace(t[:40]) + "…"
	}
	return t
}

func letterCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			n++
		}
	}
	return n
}

// receiptWindow reads --7d, --30d, --90d or --all (default 30 days).
func receiptWindow(flags map[string]bool, now time.Time) (time.Time, string) {
	for _, d := range []int{1, 7, 14, 30, 90, 365} {
		if flags[strconv.Itoa(d)+"d"] {
			return now.AddDate(0, 0, -d), fmt.Sprintf("last %d days", d)
		}
	}
	if flags["all"] {
		return time.Time{}, "all time"
	}
	return now.AddDate(0, 0, -30), "last 30 days"
}

// allClaudeLogs is every Claude Code session log on this machine.
func allClaudeLogs() []string {
	root := os.Getenv("TK_CLAUDE_PROJECTS")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".claude", "projects")
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	sub, _ := filepath.Glob(filepath.Join(root, "*", "*", "subagents", "*.jsonl")) // subagent transcripts
	return append(files, sub...)
}

// allCodexLogs is every Codex rollout touched since `since`.
func allCodexLogs(since time.Time) []string {
	files, _ := filepath.Glob(filepath.Join(codexSessionsRoot(), "*", "*", "*", "rollout-*.jsonl"))
	var out []string
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && !info.ModTime().Before(since) {
			out = append(out, f)
		}
	}
	return out
}

func buildReceipt(since, until time.Time) (*receipt, error) {
	claude, err := readClaudeUsage(allClaudeLogs(), since, until)
	if err != nil {
		return nil, err
	}
	codex, err := readCodexUsage(allCodexLogs(since), since, until)
	if err != nil {
		return nil, err
	}
	r := &receipt{Since: since, Until: until, ByAgent: map[string]float64{}}
	rows := map[string]*receiptRow{}
	sessions := map[string]bool{}
	add := func(agent string, msgs []claudeMessage) {
		for _, m := range msgs {
			if m.Branch == "HEAD" { // detached HEAD: no branch, same as an empty one
				m.Branch = ""
			}
			key, title := issueFromBranch(m.Branch)
			project := filepath.Base(m.Cwd)
			if m.Cwd == "" {
				project = "(unknown folder)"
			}
			id := key
			if id == "" {
				id = "branch\x00" + project + "\x00" + m.Branch
			}
			row := rows[id]
			if row == nil {
				row = &receiptRow{Key: key, Title: title, Project: project, Branch: m.Branch,
					Sessions: map[string]bool{}, Agents: map[string]bool{}, First: m.At}
				rows[id] = row
			}
			if row.Title == "" && title != "" {
				row.Title = title
			}
			row.Replies++
			row.Agents[agent] = true
			if m.Session != "" {
				row.Sessions[agent+m.Session] = true
				sessions[agent+m.Session] = true
			}
			if m.At.Before(row.First) {
				row.First = m.At
			}
			if m.At.After(row.Last) {
				row.Last = m.At
			}
			r.Replies++
			c, _ := priceCents(m)
			if c == "" {
				row.Unpriced++
				r.Unpriced++
				continue
			}
			v, _ := new(big.Rat).SetString(c)
			f, _ := v.Float64()
			row.Cents += f
			r.Cents += f
			r.ByAgent[agent] += f
		}
	}
	add("Claude Code", claude)
	add("Codex", codex)
	r.Sessions = len(sessions)
	for _, row := range rows {
		row.NSession = len(row.Sessions)
		for a := range row.Agents {
			row.AgentLst = append(row.AgentLst, a)
		}
		sort.Strings(row.AgentLst)
		if row.Key != "" {
			r.IssueRows++
		}
		r.Rows = append(r.Rows, row)
	}
	// Issues first, then branches; most expensive first within each.
	sort.Slice(r.Rows, func(i, j int) bool {
		a, b := r.Rows[i], r.Rows[j]
		if (a.Key != "") != (b.Key != "") {
			return a.Key != ""
		}
		if a.Cents != b.Cents {
			return a.Cents > b.Cents
		}
		return a.Replies > b.Replies
	})
	return r, nil
}

func cmdReceipt(flags map[string]bool) error {
	now := time.Now()
	since, label := receiptWindow(flags, now)
	r, err := buildReceipt(since, now)
	if err != nil {
		return err
	}
	if flags["json"] {
		return printJSON(r)
	}
	if r.Replies == 0 {
		return errors.New("no Claude Code or Codex usage found in the " + label + " (logs are read from ~/.claude/projects and ~/.codex/sessions)")
	}
	fmt.Print(formatReceipt(r, label, flags["full"]))
	return nil
}

func dollars(cents float64) string {
	d := cents / 100
	switch {
	case d >= 1000:
		return "$" + commaInt(int64(d+0.5))
	case d >= 0.995:
		return fmt.Sprintf("$%.2f", d)
	case cents >= 0.5:
		return fmt.Sprintf("$%.2f", d)
	case cents > 0:
		return "<$0.01"
	}
	return "$0.00"
}

func commaInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func formatReceipt(r *receipt, label string, full bool) string {
	var b strings.Builder
	var agents []string
	for _, a := range []string{"Claude Code", "Codex"} {
		if c, ok := r.ByAgent[a]; ok {
			agents = append(agents, a+" "+dollars(c))
		}
	}
	fmt.Fprintf(&b, "Your coding agents, %s: %s", label, dollars(r.Cents))
	if len(agents) > 1 {
		fmt.Fprintf(&b, "  (%s)", strings.Join(agents, ", "))
	}
	fmt.Fprintf(&b, "\n%d sessions · %s replies · API list prices\n\n", r.Sessions, commaInt(int64(r.Replies)))

	limit := 12
	if full {
		limit = len(r.Rows)
	}
	shown, restCents, restRows := 0, 0.0, 0
	sawBranch := false
	for _, row := range r.Rows {
		if shown >= limit {
			restCents += row.Cents
			restRows++
			continue
		}
		if row.Key == "" && !sawBranch {
			sawBranch = true
			if r.IssueRows > 0 {
				b.WriteString("\n")
			}
		}
		shown++
		name := row.Key
		desc := row.Title
		if name == "" {
			name = row.Project
			desc = "on " + branchLabel(row.Branch)
		}
		sess := "1 session"
		if row.NSession != 1 {
			sess = fmt.Sprintf("%d sessions", row.NSession)
		}
		meta := sess
		if len(row.AgentLst) > 1 {
			meta += " · " + strings.Join(row.AgentLst, "+")
		}
		cost := dollars(row.Cents)
		if row.Unpriced > 0 {
			cost += "*"
		}
		fmt.Fprintf(&b, "  %-18s %-34s %9s   %s\n", cleanText(trunc(name, 18)), cleanText(trunc(desc, 34)), cost, meta)
	}
	if restRows > 0 {
		fmt.Fprintf(&b, "  %-18s %-34s %9s\n", fmt.Sprintf("+%d more", restRows), "(tk receipt --full)", dollars(restCents))
	}
	b.WriteString("\n")
	if r.Unpriced > 0 {
		fmt.Fprintf(&b, "* %d replies from models tk can't price yet are not counted (tk cost update).\n", r.Unpriced)
	}
	if r.IssueRows == 0 {
		b.WriteString("No issue branches found. Name branches after issues (eng-142-fix-login, 123-fix-login)\nor run tk init to track cost by issue from now on.\n")
	} else {
		b.WriteString("Issues come from branch names. tk init tracks them exactly from now on, with handoffs.\n")
	}
	b.WriteString("Estimates at API list prices; on a subscription you pay a flat fee.\n")
	return b.String()
}

func branchLabel(b string) string {
	switch b {
	case "", "HEAD":
		return "no branch"
	}
	return b
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
