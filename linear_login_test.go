package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLinearHuman(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Chdir(t.TempDir()) // outside any git repo, so no repo-level user.email
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("TK_LINEAR_API_KEY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "users(filter"):
			nodes := []any{}
			switch req.Variables["email"] {
			case "dev@example.com":
				nodes = append(nodes, map[string]any{"id": "app-bot", "app": true, "active": true}, map[string]any{"id": "user-dev", "app": false, "active": true})
			case "gone@example.com":
				nodes = append(nodes, map[string]any{"id": "user-gone", "app": false, "active": false})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"users": map[string]any{"nodes": nodes}}})
		case strings.Contains(req.Query, "viewer { id }") && r.Header.Get("Authorization") == "lin_api_personal":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]any{"id": "user-from-key"}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]string{"message": "unexpected"}}})
		}
	}))
	defer srv.Close()
	app := &linearBackend{url: srv.URL, key: "app-token", bearer: true, http: srv.Client()}

	cases := []struct{ hint, want, errHas string }{
		{"user-explicit", "user-explicit", ""},
		{"dev@example.com", "user-dev", ""}, // app users are skipped
		{"gone@example.com", "", "no active Linear user"},
	}
	for _, c := range cases {
		got, err := resolveLinearHuman(app, c.hint)
		if got != c.want || (c.errHas != "" && (err == nil || !strings.Contains(err.Error(), c.errHas))) {
			t.Errorf("%q: got %q, %v", c.hint, got, err)
		}
	}
	// nothing given, no key, no git email: a clear message
	if _, err := resolveLinearHuman(app, ""); err == nil || !strings.Contains(err.Error(), "which Linear user you are") {
		t.Fatalf("no hints: %v", err)
	}
	// nothing given: git email is used
	_ = os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\temail = dev@example.com\n"), 0o600)
	if got, err := resolveLinearHuman(app, ""); got != "user-dev" || err != nil {
		t.Fatalf("git email: %q %v", got, err)
	}
	// nothing given: a saved personal key wins over git
	if err := setSecret("linear", "lin_api_personal"); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveLinearHuman(app, ""); got != "user-from-key" || err != nil {
		t.Fatalf("personal key: %q %v", got, err)
	}
}

func TestLinearLoginNeedsAClientID(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	if err := cmdLinearLogin(nil); err == nil || !strings.Contains(err.Error(), "no Linear app client ID yet") {
		t.Fatalf("no client id: %v", err)
	}
	if err := cmdLinearLogin([]string{"dev@example.com"}); err == nil || !strings.Contains(err.Error(), "no Linear app client ID yet") {
		t.Fatalf("email only, no configured client id: %v", err)
	}
}
