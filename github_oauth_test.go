package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHubOAuth plays github.com's device flow and token endpoints plus /user.
type fakeGitHubOAuth struct {
	mu          sync.Mutex
	polls       int
	refreshes   int
	deny        bool
	disabled    bool
	failRefresh bool
	token       string
	sawSecret   bool
}

func (f *fakeGitHubOAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	if r.Form.Get("client_secret") != "" {
		f.sawSecret = true
	}
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch r.URL.Path {
	case "/login/device/code":
		if f.disabled {
			reply(map[string]string{"error": "device_flow_disabled"})
			return
		}
		reply(map[string]any{"device_code": "dev-123", "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 1})
	case "/login/oauth/access_token":
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			f.polls++
			switch {
			case f.deny:
				reply(map[string]string{"error": "access_denied"})
			case f.polls <= 2:
				reply(map[string]string{"error": "authorization_pending"})
			case f.polls == 3:
				reply(map[string]any{"error": "slow_down", "interval": 2})
			default:
				f.token = "ghu_first"
				reply(map[string]any{"access_token": f.token, "expires_in": 28800, "refresh_token": "ghr_1", "refresh_token_expires_in": 15897600, "token_type": "bearer"})
			}
		case "refresh_token":
			f.refreshes++
			if f.failRefresh || r.Form.Get("refresh_token") != "ghr_1" {
				reply(map[string]string{"error": "bad_refresh_token", "error_description": "The refresh token passed is incorrect or expired."})
				return
			}
			f.token = "ghu_refreshed"
			reply(map[string]any{"access_token": f.token, "expires_in": 28800, "refresh_token": "ghr_1", "refresh_token_expires_in": 15897600, "token_type": "bearer"})
		}
	case "/user":
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(401)
			reply(map[string]string{"message": "Bad credentials"})
			return
		}
		reply(map[string]string{"login": "dev"})
	default:
		w.WriteHeader(404)
	}
}

func setupGitHubOAuth(t *testing.T) (*fakeGitHubOAuth, *httptest.Server) {
	t.Helper()
	t.Setenv("TK_HOME", t.TempDir())
	t.Setenv("TK_NO_BROWSER", "1")
	for _, e := range []string{"TK_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(e, "")
	}
	f := &fakeGitHubOAuth{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("TK_GITHUB_OAUTH_URL", srv.URL)
	t.Setenv("TK_GITHUB_URL", srv.URL)
	old := githubPollUnit
	githubPollUnit = time.Millisecond
	t.Cleanup(func() { githubPollUnit = old })
	return f, srv
}

func TestGitHubDeviceLoginAndRefresh(t *testing.T) {
	f, srv := setupGitHubOAuth(t)
	out := captureStdout(t, func() {
		if err := cmdGitHubLogin([]string{"Iv1.public"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "enter the code: ABCD-1234") || !strings.Contains(out, "Signed in to GitHub as dev") {
		t.Fatalf("login output:\n%s", out)
	}
	if f.polls != 4 {
		t.Fatalf("polls = %d (2 pending, 1 slow_down, then success)", f.polls)
	}
	c, err := readGitHubOAuth()
	if err != nil || c.AccessToken != "ghu_first" || c.Login != "dev" || c.ExpiresAt.IsZero() {
		t.Fatalf("saved login = %+v, %v", c, err)
	}

	// The backend uses the app login; when it is about to expire it refreshes with the client ID only.
	writeTestConfig(t, `{"github":{"repo":"acme/shop"}}`)
	be, err := newGithubBackend()
	if err != nil {
		t.Fatal(err)
	}
	be.rc.base = srv.URL
	if login, err := be.me(); err != nil || login != "dev" {
		t.Fatalf("me with app token: %q %v", login, err)
	}
	c.ExpiresAt = time.Now().Add(30 * time.Second)
	_ = saveGitHubOAuth(c)
	be.login = ""
	if login, err := be.me(); err != nil || login != "dev" || f.refreshes != 1 {
		t.Fatalf("after refresh: %q %v (refreshes %d)", login, err, f.refreshes)
	}
	if f.sawSecret {
		t.Fatal("a client secret was sent; the device flow needs none")
	}
	if c2, _ := readGitHubOAuth(); c2.AccessToken != "ghu_refreshed" {
		t.Fatalf("refreshed token not saved: %+v", c2)
	}

	// A failed refresh says so, instead of GitHub's generic "Bad credentials".
	c2, _ := readGitHubOAuth()
	c2.ExpiresAt, c2.RefreshToken = time.Now().Add(-time.Minute), "stale"
	_ = saveGitHubOAuth(c2)
	be.login = ""
	if _, err := be.me(); err == nil || !strings.Contains(err.Error(), "refreshing GitHub app login") {
		t.Fatalf("failed refresh: %v", err)
	}

	// An explicit environment token still wins over the app login.
	t.Setenv("GITHUB_TOKEN", "ghp_env")
	be2, _ := newGithubBackend()
	if be2.rc.authE != nil {
		t.Fatal("GITHUB_TOKEN should take precedence over the app login")
	}

	t.Setenv("GITHUB_TOKEN", "")
	captureStdout(t, func() { _ = cmdGitHubLogout() })
	if _, err := readGitHubOAuth(); err == nil {
		t.Fatal("logout should remove the app login")
	}
}

func TestGitHubDeviceLoginFailures(t *testing.T) {
	f, _ := setupGitHubOAuth(t)
	if err := cmdGitHubLogin(nil); err == nil || !strings.Contains(err.Error(), "no GitHub app client ID yet") {
		t.Fatalf("no client id: %v", err)
	}
	f.deny = true
	captureStdout(t, func() {
		if err := cmdGitHubLogin([]string{"Iv1.public"}); err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("denied: %v", err)
		}
	})
	f.disabled = true
	if err := cmdGitHubLogin([]string{"Iv1.public"}); err == nil || !strings.Contains(err.Error(), "device flow is off") {
		t.Fatalf("disabled: %v", err)
	}
}
