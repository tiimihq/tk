package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exercise the full HTTP request/response contract without binding a port.
// This keeps backend tests runnable in restricted developer environments.
type memoryServer struct {
	URL     string
	handler http.Handler
	mu      sync.Mutex
	closed  bool
}

func newMemoryServer(h http.Handler) *memoryServer {
	return &memoryServer{URL: "http://linear.test", handler: h}
}
func (s *memoryServer) Close()               { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true }
func (s *memoryServer) Client() *http.Client { return &http.Client{Transport: s} }
func (s *memoryServer) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("server offline")
	}
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	return w.Result(), nil
}
func testOAuth(t *testing.T) *linearOAuth {
	t.Helper()
	c := &linearOAuth{ClientID: "public-client", AccessToken: "opaque-token", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour), WorkspaceID: "workspace", AgentID: "agent-claude", AgentName: "Claude via tk", HumanID: "user-me"}
	if err := saveOAuth(c); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestAppOwnership(t *testing.T) {
	f, srv, _ := setupLinear(t)
	c := testOAuth(t)
	f.key = "Bearer " + c.AccessToken
	f.viewer = c.AgentID
	be, err := newLinearBackend()
	if err != nil {
		t.Fatal(err)
	}
	be.http = srv.Client()
	items, err := be.inbox()
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: %+v %v", items, err)
	}
	s, err := be.start("ENG-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.issues["ENG-1"].Assignee != "user-me" || f.issues["ENG-1"].Delegate != c.AgentID {
		t.Fatal("human replaced or delegation missing")
	}
	if s.AgentID != c.AgentID || s.ConnectionID == "" {
		t.Fatalf("session: %+v", s)
	}
	if err = be.note(&s, "working"); err != nil {
		t.Fatal(err)
	}
	if _, err = be.finish(&s, "handoff", "continue later"); err != nil {
		t.Fatal(err)
	}
	if f.issues["ENG-1"].Assignee != "user-me" {
		t.Fatal("handoff changed human")
	}
	if _, err = be.start("ENG-2"); err == nil {
		t.Fatal("claimed another human's issue")
	}
	f.issues["ENG-1"].Delegate = "agent-codex"
	if _, err = be.start("ENG-1"); err == nil {
		t.Fatal("stole delegation")
	}
	if err = be.note(&s, "stale session"); err == nil {
		t.Fatal("wrote after delegation changed")
	}
	f.issues["ENG-1"].Delegate = c.AgentID
	s.ConnectionID = "different-workspace"
	if err = be.postCost(&s, map[string]any{}); err == nil {
		t.Fatal("cost crossed connection")
	}
	writeTestConfig(t, `{"linear":{"team":"ENG"}}`)
	if _, err = be.createIssue("new", ""); err != nil {
		t.Fatal(err)
	}
	if f.issues["ENG-3"].Assignee != "user-me" || f.issues["ENG-3"].Delegate != c.AgentID {
		t.Fatal("new issue ownership wrong")
	}
}
func TestOAuthCallback(t *testing.T) {
	ch := make(chan string, 1)
	h := oauthCallbackHandler("expected", ch)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/oauth/callback?state=wrong&code=secret", nil))
	if w.Code != 400 || len(ch) != 0 {
		t.Fatal("accepted wrong state")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/oauth/callback?state=expected&error=access_denied", nil))
	if got := <-ch; got != "" {
		t.Fatal("denial accepted")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/oauth/callback?state=expected&code=ok", nil))
	if got := <-ch; got != "ok" {
		t.Fatal(got)
	}
	// RFC 7636 vector.
	if pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal("bad PKCE")
	}
}
func TestOAuthRefreshConcurrent(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	c := testOAuth(t)
	c.ExpiresAt = time.Now().Add(-time.Minute)
	if err := saveOAuth(c); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := newMemoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.Form.Get("client_secret") != "" || r.Form.Get("refresh_token") != "refresh" || r.Form.Get("client_id") != "public-client" {
			t.Error("bad refresh form")
		}
		fmt.Fprint(w, `{"access_token":"rotated","refresh_token":"rotated-refresh","expires_in":3600,"token_type":"Bearer"}`)
	}))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := refreshOAuth(srv.Client(), srv.URL, oauthConnection(c))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("refresh calls=%d", calls)
	}
	saved, err := readOAuth()
	if err != nil || saved.RefreshToken != "rotated-refresh" {
		t.Fatalf("rotation not persisted: %v", err)
	}
	info, err := os.Stat(fileSecrets{}.path("linear-oauth"))
	if err != nil || info.Mode().Perm() != 0600 || fileExists(oauthPath()) {
		t.Fatal("credential permissions")
	}
	if _, err = refreshOAuth(srv.Client(), srv.URL, "other"); err == nil {
		t.Fatal("connection replacement accepted")
	}
}
func TestOAuthExchangeAndFailure(t *testing.T) {
	c := &linearOAuth{AccessToken: "original"}
	srv := newMemoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code_verifier") != "verifier" || r.Form.Get("code") != "code" {
			t.Error("missing PKCE")
		}
		http.Error(w, "secret response", 401)
	}))
	err := exchangeOAuth(context.Background(), srv.Client(), srv.URL, url.Values{"code_verifier": {"verifier"}, "code": {"code"}}, c)
	if err == nil || strings.Contains(err.Error(), "secret response") || c.AccessToken != "original" {
		t.Fatalf("unsafe failure %v", err)
	}
}
func TestAppUsesFreshOwnership(t *testing.T) {
	f, srv, _ := setupLinear(t)
	c := testOAuth(t)
	f.key = "Bearer " + c.AccessToken
	be, _ := newLinearBackend()
	be.http = srv.Client()
	if _, err := be.fetch("ENG-1"); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if _, err := be.start("ENG-1"); err == nil {
		t.Fatal("offline cached ownership allowed mutation")
	}
}

func TestOAuthSuccessfulExchange(t *testing.T) {
	c := &linearOAuth{ClientID: "public-client"}
	srv := newMemoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Error("wrong exchange request")
		}
		_ = r.ParseForm()
		if r.Form.Get("code_verifier") != "verifier" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_secret") != "" {
			t.Error("incorrect PKCE exchange")
		}
		fmt.Fprint(w, `{"access_token":"opaque","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer"}`)
	}))
	err := exchangeOAuth(context.Background(), srv.Client(), srv.URL, url.Values{"code_verifier": {"verifier"}, "grant_type": {"authorization_code"}}, c)
	if err != nil || c.AccessToken != "opaque" || time.Until(c.ExpiresAt) < 59*time.Minute {
		t.Fatalf("exchange failed: %v", err)
	}
}

func TestConnectionGuardsAndLogout(t *testing.T) {
	_, srv, _ := setupLinear(t)
	c := testOAuth(t)
	be, err := newLinearBackend()
	if err != nil {
		t.Fatal(err)
	}
	be.http = srv.Client()
	s := session{Key: "ENG-1", SessionID: "session", ConnectionID: be.connectionID()}
	if err = saveSession(s); err != nil {
		t.Fatal(err)
	}
	if err = cmdLinearLogout(); err == nil {
		t.Fatal("logout allowed during active session")
	}
	local, err := newLocalBackend()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = syncCost(local, &s, time.Now()); err == nil {
		t.Fatal("Linear cost sent to local backend")
	}
	if err = closeSession(s); err != nil {
		t.Fatal(err)
	}
	if err = cmdLinearLogout(); err == nil {
		t.Fatal("logout allowed with pending final sync")
	}
	if err = os.Remove(filepath.Join(tkHome(), "closed", "session.json")); err != nil {
		t.Fatal(err)
	}
	if err = cmdLinearLogout(); err != nil {
		t.Fatal(err)
	}
	if _, err = readOAuth(); !os.IsNotExist(err) {
		t.Fatal("credentials retained")
	}
	// A daemon holding a prior connection cannot silently use a replacement.
	c.WorkspaceID = "another-workspace"
	if err = saveOAuth(c); err != nil {
		t.Fatal(err)
	}
	if _, err = be.inbox(); err == nil {
		t.Fatal("stale daemon adopted replacement connection")
	}
}

func TestListenLoopback(t *testing.T) {
	lns, err := listenLoopback("http://localhost:0/oauth/callback")
	if err == nil {
		for _, l := range lns {
			l.Close()
		}
		t.Fatal("port 0 has no fixed port and must be rejected")
	}
	if _, err := listenLoopback("http://example.com:8765/cb"); err == nil {
		t.Fatal("non-loopback callback accepted")
	}
	// grab a free port, then listen on it the way login does
	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	lns, err = listenLoopback(fmt.Sprintf("http://localhost:%d/oauth/callback", port))
	if err != nil || len(lns) == 0 {
		t.Fatalf("listen: %v", err)
	}
	for _, l := range lns {
		l.Close()
	}
}
