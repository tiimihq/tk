package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// restClient is the JSON-over-HTTP client the Jira and GitHub backends share.
// Reads that pass a cacheKey are cached and served from the cache when the
// server can't be reached (never when it answers with an error).
type restClient struct {
	base   string
	auth   func(*http.Request)
	authE  func(*http.Request) error // used instead of auth when set; can fail (e.g. a token refresh)
	http   *http.Client
	errMsg func([]byte) string // pulls the server's error message out of a body
}

func (c *restClient) do(method, path string, body any, cacheKey string, out any) error {
	raw, err := c.send(method, path, body)
	if err != nil && isUnreachable(err) && cacheKey != "" {
		if cached, cerr := os.ReadFile(cacheFile(cacheKey)); cerr == nil {
			raw, err = cached, nil
		}
	} else if err == nil && cacheKey != "" {
		_ = writeCache(cacheKey, raw)
	}
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected response from %s: %w", c.base, err)
	}
	return nil
}

func (c *restClient) send(method, path string, body any) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if body != nil {
			buf, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			reader = strings.NewReader(string(buf))
		}
		req, err := http.NewRequest(method, c.base+path, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.authE != nil {
			if err := c.authE(req); err != nil {
				return nil, err
			}
		} else {
			c.auth(req)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, &unreachableError{base: c.base, err: err}
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		// One polite retry when rate limited, if the server says when.
		if resp.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 && s <= 10 {
				time.Sleep(time.Duration(s) * time.Second)
				continue
			}
		}
		if resp.StatusCode/100 != 2 {
			msg := ""
			if c.errMsg != nil {
				msg = c.errMsg(raw)
			}
			if msg == "" {
				msg = strings.TrimSpace(string(raw))
			}
			return nil, &httpError{status: resp.StatusCode, msg: firstSentence(msg)}
		}
		return raw, nil
	}
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.msg) }

func isStatus(err error, code int) bool {
	var h *httpError
	return errors.As(err, &h) && h.status == code
}

// agentNotePrefix marks comments tk posts with a personal credential, which the
// tracker attributes to the developer, so people can tell agent notes apart.
const agentNotePrefix = "[tk agent] "

type timedComment struct{ Body, At string }

// contextFromComments builds the resume brief tk prints from an issue's latest
// comments, the same shape every backend returns.
func contextFromComments(is issue, comments []timedComment) heartbeatContext {
	hc := heartbeatContext{Issue: is}
	if n := len(comments); n > 0 {
		recent := comments[max(0, n-5):]
		var b strings.Builder
		b.WriteString("## Recent Concrete Actions\n")
		for _, c := range recent {
			b.WriteString("- " + oneLine(strings.TrimPrefix(c.Body, agentNotePrefix)) + "\n")
		}
		hc.ContinuationSummary = &struct {
			Body string `json:"body"`
		}{Body: b.String()}
		hc.CommentCursor = &struct {
			TotalComments   int    `json:"totalComments"`
			LatestCommentAt string `json:"latestCommentAt"`
		}{TotalComments: n, LatestCommentAt: comments[n-1].At}
	}
	return hc
}

// secretFrom returns the first non-empty environment variable, else the
// contents of the named file under ~/.tk.
func secretFrom(file string, envs ...string) string {
	for name, k := range knownSecrets {
		if k.legacy == file {
			return getSecret(name, envs...)
		}
	}
	return getSecret(file, envs...)
}

func tkHomeFile(name string) string { return tkHome() + string(os.PathSeparator) + name }

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

var regexpBlankRuns = regexp.MustCompile(`\n{3,}`)
