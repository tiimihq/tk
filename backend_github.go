package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// githubBackend works on one repository's GitHub Issues through the REST API
// with a token (the agent acts as the token's user).
//
// GitHub issues are only open or closed, so tk uses labels for the rest:
//   - inbox: open issues in github.repo assigned to you (pull requests skipped)
//   - start: assign to you if unassigned; add the in-progress label
//   - note: a comment, prefixed [tk agent]
//   - handoff: remove the in-progress label; comment
//   - done: comment; remove in-progress; add github.reviewLabel and keep it open
//     if set, else close it as completed
//   - cost: kept in tk's cost store; handoff/done comments carry the cost line
//
// Issue keys are numbers: `tk start 42` (or #42).
type githubBackend struct {
	rc          *restClient
	repo        string // owner/name
	inProgress  string
	reviewLabel string
	login       string
}

// githubToken is a fixed token: an environment variable, a saved token, or the
// GitHub CLI's login. (The app login, which refreshes, is handled separately.)
func githubToken() string {
	for _, e := range []string{"TK_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(e)); v != "" {
			return v
		}
	}
	if t := secretFrom("github_token"); t != "" {
		return t
	}
	// Reuse the GitHub CLI's login when it's installed and signed in.
	if path, err := exec.LookPath("gh"); err == nil {
		if out, err := exec.Command(path, "auth", "token").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func newGithubBackend() (*githubBackend, error) {
	cfg := loadConfig().GitHub
	repo := firstNonEmpty(os.Getenv("TK_GITHUB_REPO"), cfg.Repo)
	if !repoRe.MatchString(repo) {
		return nil, errors.New(`GitHub mode needs the repository: "github": {"repo": "owner/name"} in ~/.tk/config.json`)
	}
	base := strings.TrimRight(firstNonEmpty(os.Getenv("TK_GITHUB_URL"), cfg.URL, "https://api.github.com"), "/")
	httpc := &http.Client{Timeout: 15 * time.Second}
	headers := func(r *http.Request, token string) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Accept", "application/vnd.github+json")
		r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	rc := &restClient{base: base, http: httpc, errMsg: githubErrorMessage}
	// Token order: an explicit environment token, then the tk app login
	// (refreshed as needed), then a saved token, then the GitHub CLI's login.
	envToken := firstNonEmpty(os.Getenv("TK_GITHUB_TOKEN"), os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
	if _, err := readGitHubOAuth(); envToken == "" && err == nil {
		rc.authE = func(r *http.Request) error {
			tok, _, err := githubAppToken(httpc)
			if err != nil {
				return err
			}
			headers(r, tok)
			return nil
		}
	} else {
		token := githubToken()
		if token == "" {
			return nil, errors.New("GitHub mode needs a login: tk login github, tk secret set github, GITHUB_TOKEN, or `gh auth login`")
		}
		rc.auth = func(r *http.Request) { headers(r, token) }
	}
	return &githubBackend{rc: rc, repo: repo, inProgress: firstNonEmpty(cfg.InProgressLabel, "in progress"), reviewLabel: cfg.ReviewLabel}, nil
}

func githubErrorMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if jsonUnmarshal(raw, &e) != nil {
		return ""
	}
	return e.Message
}

func (g *githubBackend) name() string               { return "github (" + g.repo + ")" }
func (g *githubBackend) setTimeout(d time.Duration) { g.rc.http.Timeout = d }
func (g *githubBackend) cacheKey(parts ...string) string {
	return "/github/" + strings.ReplaceAll(g.repo, "/", "_") + "/" + strings.Join(parts, "/")
}

// number accepts "42", "#42" or "owner/repo#42" (for this repo).
func (g *githubBackend) number(key string) (int, error) {
	k := strings.TrimSpace(key)
	if i := strings.LastIndex(k, "#"); i >= 0 {
		if prefix := k[:i]; prefix != "" && !strings.EqualFold(prefix, g.repo) {
			return 0, fmt.Errorf("%s is not in %s", key, g.repo)
		}
		k = k[i+1:]
	}
	n, err := strconv.Atoi(k)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("GitHub issue keys are numbers, e.g. tk start 42 (got %q)", key)
	}
	return n, nil
}

func ghKey(n int) string { return "#" + strconv.Itoa(n) }

// costKey keeps cost records of different repositories apart.
func (g *githubBackend) costKey(n int) string {
	return strings.ToUpper(strings.ReplaceAll(g.repo, "/", "_")) + "-" + strconv.Itoa(n)
}

type ghIssue struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
	StateReason string `json:"state_reason"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	PullRequest any `json:"pull_request"`
}

func (gi ghIssue) hasLabel(name string) bool {
	for _, l := range gi.Labels {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

func (g *githubBackend) status(gi ghIssue) string {
	switch {
	case gi.State == "closed" && gi.StateReason == "not_planned":
		return "cancelled"
	case gi.State == "closed":
		return "done"
	case g.reviewLabel != "" && gi.hasLabel(g.reviewLabel):
		return "in_review"
	case gi.hasLabel(g.inProgress):
		return "in_progress"
	}
	return "todo"
}

var ghPriorityLabels = map[string]string{"urgent": "urgent", "p0": "urgent", "critical": "urgent",
	"high": "high", "p1": "high", "medium": "medium", "p2": "medium", "low": "low", "p3": "low"}

func ghPriority(gi ghIssue) string {
	for _, l := range gi.Labels {
		n := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(l.Name), "priority:"), "priority/")))
		if p, ok := ghPriorityLabels[n]; ok {
			return p
		}
	}
	return "none"
}

func (g *githubBackend) toIssue(gi ghIssue) issue {
	is := issue{Identifier: ghKey(gi.Number), Title: gi.Title, Description: gi.Body, Status: g.status(gi), Priority: ghPriority(gi)}
	for _, a := range gi.Assignees {
		if strings.EqualFold(a.Login, g.login) {
			is.AssigneeAgentID = a.Login
		} else if is.AssigneeUserID == "" {
			is.AssigneeUserID = a.Login
		}
	}
	return is
}

func (g *githubBackend) me() (string, error) {
	if g.login != "" {
		return g.login, nil
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := g.rc.do(http.MethodGet, "/user", nil, g.cacheKey("user"), &u); err != nil {
		return "", err
	}
	g.login = u.Login
	return g.login, nil
}

func (g *githubBackend) inbox() ([]inboxItem, error) {
	login, err := g.me()
	if err != nil {
		return nil, err
	}
	var list []ghIssue
	path := fmt.Sprintf("/repos/%s/issues?state=open&assignee=%s&per_page=50&sort=updated", g.repo, url.QueryEscape(login))
	if err := g.rc.do(http.MethodGet, path, nil, g.cacheKey("inbox"), &list); err != nil {
		return nil, err
	}
	items := []inboxItem{}
	listed := map[string]bool{}
	for _, gi := range list {
		if gi.PullRequest != nil { // the issues API lists pull requests too
			continue
		}
		listed[ghKey(gi.Number)] = true
		items = append(items, inboxItem{Identifier: ghKey(gi.Number), Title: gi.Title, Status: g.status(gi), Priority: ghPriority(gi)})
	}
	// Issues tk just created may not be in the list yet; read them directly.
	for _, r := range recentCreated("github-" + g.repo) {
		if listed[r.Key] {
			continue
		}
		n, err := g.number(r.Key)
		if err != nil {
			continue
		}
		gi, err := g.fetch(n)
		if err != nil || gi.State != "open" {
			continue
		}
		for _, a := range gi.Assignees {
			if strings.EqualFold(a.Login, login) {
				items = append([]inboxItem{{Identifier: ghKey(n), Title: gi.Title, Status: g.status(gi), Priority: ghPriority(gi)}}, items...)
				break
			}
		}
	}
	return items, nil
}

func (g *githubBackend) fetch(n int) (ghIssue, error) {
	var gi ghIssue
	err := g.rc.do(http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", g.repo, n), nil, g.cacheKey("issue", strconv.Itoa(n)), &gi)
	if isStatus(err, http.StatusNotFound) {
		return gi, fmt.Errorf("issue #%d not found in %s", n, g.repo)
	}
	if err == nil && gi.PullRequest != nil {
		return gi, fmt.Errorf("#%d is a pull request, not an issue", n)
	}
	return gi, err
}

func (g *githubBackend) issue(key string) (issue, error) {
	n, err := g.number(key)
	if err != nil {
		return issue{}, err
	}
	if _, err := g.me(); err != nil {
		return issue{}, err
	}
	gi, err := g.fetch(n)
	if err != nil {
		return issue{}, err
	}
	return g.toIssue(gi), nil
}

func (g *githubBackend) context(key string) (heartbeatContext, error) {
	n, err := g.number(key)
	if err != nil {
		return heartbeatContext{}, err
	}
	if _, err := g.me(); err != nil {
		return heartbeatContext{}, err
	}
	gi, err := g.fetch(n)
	if err != nil {
		return heartbeatContext{}, err
	}
	var comments []struct {
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
	if err := g.rc.do(http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", g.repo, n), nil,
		g.cacheKey("comments", strconv.Itoa(n)), &comments); err != nil {
		return heartbeatContext{}, err
	}
	cs := make([]timedComment, len(comments))
	for i, c := range comments {
		cs[i] = timedComment{Body: c.Body, At: c.CreatedAt}
	}
	return contextFromComments(g.toIssue(gi), cs), nil
}

func (g *githubBackend) addLabel(n int, label string) error {
	return g.rc.do(http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/labels", g.repo, n), map[string]any{"labels": []string{label}}, "", nil)
}

func (g *githubBackend) removeLabel(n int, label string) error {
	err := g.rc.do(http.MethodDelete, fmt.Sprintf("/repos/%s/issues/%d/labels/%s", g.repo, n, url.PathEscape(label)), nil, "", nil)
	if isStatus(err, http.StatusNotFound) { // wasn't there
		return nil
	}
	return err
}

func (g *githubBackend) start(key string) (session, error) {
	n, err := g.number(key)
	if err != nil {
		return session{}, err
	}
	login, err := g.me()
	if err != nil {
		return session{}, err
	}
	gi, err := g.fetch(n)
	if err != nil {
		return session{}, err
	}
	defer g.invalidate(n)
	if gi.State == "closed" {
		return session{}, fmt.Errorf("#%d is closed", n)
	}
	mine := false
	for _, a := range gi.Assignees {
		mine = mine || strings.EqualFold(a.Login, login)
	}
	if len(gi.Assignees) > 0 && !mine {
		return session{}, fmt.Errorf("#%d is assigned to %s", n, gi.Assignees[0].Login)
	}
	if !mine {
		if err := g.rc.do(http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/assignees", g.repo, n),
			map[string]any{"assignees": []string{login}}, "", nil); err != nil {
			return session{}, fmt.Errorf("assigning #%d to you: %w", n, err)
		}
	}
	if !gi.hasLabel(g.inProgress) {
		if err := g.addLabel(n, g.inProgress); err != nil {
			return session{}, fmt.Errorf("labelling #%d %q: %w", n, g.inProgress, err)
		}
	}
	return session{Key: ghKey(n), IssueID: strconv.Itoa(n), AgentID: login}, nil
}

func (g *githubBackend) comment(n int, text string) error {
	defer g.invalidate(n)
	return g.rc.do(http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", g.repo, n), map[string]string{"body": agentNotePrefix + text}, "", nil)
}

func (g *githubBackend) note(s *session, text string) error {
	n, err := g.number(s.Key)
	if err != nil {
		return err
	}
	return g.comment(n, text)
}

func (g *githubBackend) finish(s *session, kind, text string) (string, error) {
	n, err := g.number(s.Key)
	if err != nil {
		return "", err
	}
	defer g.invalidate(n)
	var body string
	switch kind {
	case "handoff":
		body = "Handoff: " + text
	case "done":
		if text == "" {
			text = "finished"
		}
		body = "Done: " + text
	default:
		return "", fmt.Errorf("unknown finish %q", kind)
	}
	if line := costStoreSummaryLine(g.costKey(n)); line != "" {
		body += "\n\n" + line
	}
	if err := g.comment(n, body); err != nil {
		return "", err
	}
	if err := g.removeLabel(n, g.inProgress); err != nil {
		return "", err
	}
	if kind == "handoff" {
		return "todo", nil
	}
	if g.reviewLabel != "" {
		if err := g.addLabel(n, g.reviewLabel); err != nil {
			return "", err
		}
		return "in review (" + g.reviewLabel + ")", nil
	}
	if err := g.rc.do(http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", g.repo, n),
		map[string]string{"state": "closed", "state_reason": "completed"}, "", nil); err != nil {
		return "", err
	}
	return "closed", nil
}

func (g *githubBackend) createIssue(title, description string) (issue, error) {
	login, err := g.me()
	if err != nil {
		return issue{}, err
	}
	in := map[string]any{"title": title, "assignees": []string{login}}
	if description != "" {
		in["body"] = description
	}
	var gi ghIssue
	if err := g.rc.do(http.MethodPost, "/repos/"+g.repo+"/issues", in, "", &gi); err != nil {
		return issue{}, err
	}
	_ = os.Remove(cacheFile(g.cacheKey("inbox")))
	rememberCreated("github-"+g.repo, ghKey(gi.Number), "")
	return g.toIssue(gi), nil
}

func (g *githubBackend) postCost(s *session, event map[string]any) error {
	n, err := g.number(s.Key)
	if err != nil {
		return err
	}
	return costStoreAdd(g.costKey(n), event)
}

func (g *githubBackend) costSummary(key string) (costTotals, error) {
	n, err := g.number(key)
	if err != nil {
		return costTotals{}, err
	}
	return costStoreTotals(g.costKey(n))
}

func (g *githubBackend) refresh() error {
	if _, err := g.inbox(); err != nil {
		return err
	}
	all, _ := loadSessions()
	for _, s := range all {
		if n, err := g.number(s.Key); err == nil {
			if _, err := g.fetch(n); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *githubBackend) invalidate(n int) {
	for _, k := range []string{g.cacheKey("issue", strconv.Itoa(n)), g.cacheKey("comments", strconv.Itoa(n)), g.cacheKey("inbox")} {
		_ = os.Remove(cacheFile(k))
	}
}

// commentOn adds a comment to any issue, without a tk session (tk comment).
func (g *githubBackend) commentOn(key, text string) error {
	n, err := g.number(key)
	if err != nil {
		return err
	}
	return g.comment(n, text)
}
