package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// client talks to a Paperclip server as an agent.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

func newClient() (*client, error) {
	base := strings.TrimRight(paperclipURL(), "/")
	token, err := loadToken()
	if err != nil {
		return nil, err
	}
	return &client{baseURL: base, token: token, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

// loadToken reads the agent key from TK_TOKEN, or from TK_TOKEN_FILE, which may be
// either a bare token or the JSON printed by `paperclipai token agent create --json`.
func loadToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("TK_TOKEN")); t != "" {
		return t, nil
	}
	path := os.Getenv("TK_TOKEN_FILE")
	if path == "" {
		// Written by `tk daemon install`; lets tk work without any environment set up.
		path = filepath.Join(tkHome(), "agent_token.json")
		if _, err := os.Stat(path); err != nil {
			return "", errors.New("no agent key: set TK_TOKEN or TK_TOKEN_FILE")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading TK_TOKEN_FILE: %w", err)
	}
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, "{") {
		return text, nil
	}
	var created struct {
		Key struct {
			Token string `json:"token"`
		} `json:"key"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.Key.Token == "" {
		return "", errors.New("TK_TOKEN_FILE JSON has no key.token")
	}
	return created.Key.Token, nil
}

// get fetches path and returns the raw body; non-2xx responses become errors
// carrying the server's error message.
func (c *client) get(path string) ([]byte, error) {
	return c.send(http.MethodGet, path, nil, "")
}

// send performs a request with an optional JSON body. runID, when set, is sent
// as X-Paperclip-Run-Id, which Paperclip requires for agent writes.
func (c *client) send(method, path string, body any, runID string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(buf))
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if runID != "" {
		req.Header.Set("X-Paperclip-Run-Id", runID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &unreachableError{base: c.baseURL, err: err}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(respBody, &e)
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(respBody))
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, firstSentence(msg))
	}
	return respBody, nil
}

// unreachableError means the server could not be contacted at all, as opposed
// to answering with an error. Only this kind may fall back to cached data.
type unreachableError struct {
	base string
	err  error
}

func (e *unreachableError) Error() string { return fmt.Sprintf("cannot reach %s: %v", e.base, e.err) }
func (e *unreachableError) Unwrap() error { return e.err }

func isUnreachable(err error) bool {
	var u *unreachableError
	return errors.As(err, &u)
}

func issuePath(key string, suffix string) string {
	return "/api/issues/" + url.PathEscape(key) + suffix
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}
