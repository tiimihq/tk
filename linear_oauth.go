package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const linearTokenURL = "https://api.linear.app/oauth/token"

// linearCallback is the redirect URL registered with the Linear OAuth app. It uses
// "localhost", the form Linear's own docs show; plain http is fine for a loopback
// address (RFC 8252). TK_LINEAR_REDIRECT overrides it, e.g. to use 127.0.0.1.
var linearCallback = firstNonEmpty(os.Getenv("TK_LINEAR_REDIRECT"), "http://localhost:8765/oauth/callback")

// listenLoopback listens on the callback's port on both IPv4 and IPv6 loopback,
// since browsers may resolve "localhost" to either. At least one must succeed.
func listenLoopback(callback string) ([]net.Listener, error) {
	u, err := url.Parse(callback)
	if err != nil || u.Port() == "" || u.Port() == "0" { // the port must match the registered URL
		return nil, fmt.Errorf("bad callback URL %q", callback)
	}
	hosts := []string{"127.0.0.1", "::1"}
	switch u.Hostname() {
	case "127.0.0.1":
		hosts = hosts[:1]
	case "::1":
		hosts = hosts[1:]
	case "localhost":
	default:
		return nil, fmt.Errorf("callback %q must be a loopback address (localhost or 127.0.0.1)", callback)
	}
	var lns []net.Listener
	var firstErr error
	for _, h := range hosts {
		ln, err := net.Listen("tcp", net.JoinHostPort(h, u.Port()))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		lns = append(lns, ln)
	}
	if len(lns) == 0 {
		return nil, fmt.Errorf("OAuth callback port %s unavailable: %w", u.Port(), firstErr)
	}
	return lns, nil
}

type linearOAuth struct {
	ClientID     string    `json:"clientId"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	WorkspaceID  string    `json:"workspaceId"`
	AgentID      string    `json:"agentId"`
	AgentName    string    `json:"agentName"`
	HumanID      string    `json:"humanId"`
}

func oauthPath() string { return filepath.Join(tkHome(), "linear_oauth.json") }
func readOAuth() (*linearOAuth, error) {
	v := getSecret("linear-oauth") // the store, else the legacy file
	if v == "" {
		return nil, os.ErrNotExist
	}
	raw := []byte(v)
	var c linearOAuth
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("invalid Linear OAuth credentials; reconnect with tk login linear")
	}
	if c.ClientID == "" || c.AccessToken == "" || c.RefreshToken == "" || c.AgentID == "" || c.HumanID == "" || c.WorkspaceID == "" {
		return nil, errors.New("incomplete Linear OAuth credentials; reconnect")
	}
	return &c, nil
}
func saveOAuth(c *linearOAuth) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := setSecret("linear-oauth", string(raw)); err != nil {
		return err
	}
	_ = os.Remove(oauthPath()) // a legacy file would shadow nothing now, but don't leave tokens lying around
	return nil
}
func withOAuthLock(fn func() error) error { return withFileLock(oauthPath()+".lock", fn) }
func exchangeOAuth(ctx context.Context, client *http.Client, endpoint string, form url.Values, c *linearOAuth) error {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		// Typed so read paths fall back to the cache, as for any other outage;
		// a rejected refresh (HTTP error below) stays an authentication error.
		return &unreachableError{base: endpoint, err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Linear token exchange failed (HTTP %d); reconnect if authorization expired", resp.StatusCode)
	}
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Type    string `json:"token_type"`
	}
	if err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&tok); err != nil {
		return errors.New("invalid Linear token response")
	}
	if tok.Access == "" || tok.Refresh == "" || tok.Expires <= 0 || !strings.EqualFold(tok.Type, "Bearer") {
		return errors.New("incomplete Linear token response")
	}
	c.AccessToken, c.RefreshToken, c.ExpiresAt = tok.Access, tok.Refresh, time.Now().Add(time.Duration(tok.Expires)*time.Second)
	return nil
}
func refreshOAuth(client *http.Client, endpoint, expected string) (*linearOAuth, error) {
	var c *linearOAuth
	err := withOAuthLock(func() error {
		var err error
		c, err = readOAuth()
		if err != nil {
			return err
		}
		if oauthConnection(c) != expected {
			return errors.New("Linear connection changed; restart the command or daemon")
		}
		if time.Until(c.ExpiresAt) > time.Minute {
			return nil
		}
		err = exchangeOAuth(context.Background(), client, endpoint, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "refresh_token": {c.RefreshToken}}, c)
		if err != nil {
			return err
		}
		return saveOAuth(c)
	})
	return c, err
}
func oauthConnection(c *linearOAuth) string { return c.WorkspaceID + ":" + c.AgentID + ":" + c.HumanID }
func randomOAuthValue() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), err
}
func pkceChallenge(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func oauthCallbackHandler(state string, result chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" || r.URL.Path != "/oauth/callback" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid OAuth state", 400)
			return
		}
		code := r.URL.Query().Get("code")
		if r.URL.Query().Get("error") != "" {
			code = ""
		}
		select {
		case result <- code:
			fmt.Fprintln(w, "Authorization received. Return to your terminal.")
		default:
			http.Error(w, "Callback already received", 409)
		}
	})
}

// Credentials use an explicitly documented owner-only file for this preview.
// One installed agent identity per TK_HOME; app name is controlled in Linear.
// linearPublicClientID is the client ID of the published tk app on Linear. It's
// public (logins use PKCE, no secret ships). Empty until the app is registered;
// linear.clientId in ~/.tk/config.json or an argument overrides it.
var linearPublicClientID = ""

// cmdLinearLogin: tk login linear [client-id] [you: Linear user id or email].
// Who "you" are (the human who stays accountable) is found automatically when
// not given: from a saved personal API key, else from `git config user.email`.
func cmdLinearLogin(args []string) error {
	if len(args) > 2 {
		return errors.New("usage: tk login linear [client-id] [your Linear user id or email]")
	}
	clientID := firstNonEmpty(argAt(args, 0), loadConfig().Linear.ClientID, linearPublicClientID)
	if strings.Contains(clientID, "@") { // one argument that is an email: the client id is configured
		clientID, args = firstNonEmpty(loadConfig().Linear.ClientID, linearPublicClientID), []string{"", args[0]}
	}
	if clientID == "" {
		return errors.New("no Linear app client ID yet: register the tk app in Linear (callback " + linearCallback + ") and run tk login linear <client-id>, or set linear.clientId in ~/.tk/config.json")
	}
	human := firstNonEmpty(argAt(args, 1), loadConfig().Linear.User)
	state, err := randomOAuthValue()
	if err != nil {
		return err
	}
	verifier, err := randomOAuthValue()
	if err != nil {
		return err
	}
	lns, err := listenLoopback(linearCallback)
	if err != nil {
		return err
	}
	result := make(chan string, 1)
	server := &http.Server{Handler: oauthCallbackHandler(state, result), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	for _, ln := range lns {
		go server.Serve(ln)
	}
	q := url.Values{"client_id": {clientID}, "redirect_uri": {linearCallback}, "response_type": {"code"}, "scope": {"read,write,app:assignable"}, "actor": {"app"}, "state": {state}, "code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"}}
	authURL := "https://linear.app/oauth/authorize?" + q.Encode()
	fmt.Println("Authorize the agent app in your browser:\n" + authURL)
	if runtime.GOOS == "darwin" {
		_ = exec.Command("open", authURL).Run()
	}
	var code string
	select {
	case code = <-result:
	case <-time.After(5 * time.Minute):
		return errors.New("Linear login timed out; existing credentials unchanged")
	}
	if code == "" {
		return errors.New("Linear authorization denied or missing code; existing credentials unchanged")
	}
	c := &linearOAuth{ClientID: clientID, HumanID: human}
	client := &http.Client{Timeout: 20 * time.Second}
	if err = exchangeOAuth(context.Background(), client, linearTokenURL, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {linearCallback}, "code": {code}, "code_verifier": {verifier}}, c); err != nil {
		return err
	}
	be := &linearBackend{url: "https://api.linear.app/graphql", key: c.AccessToken, bearer: true, http: client}
	var identity struct {
		Viewer struct {
			ID, Name string
			App      bool
		}
		Organization struct{ ID string }
		User         struct {
			ID          string
			App, Active bool
		}
	}
	if human, err = resolveLinearHuman(be, human); err != nil {
		return err
	}
	if err = be.gql(`query($human: String!) { viewer { id name app } organization { id } user(id: $human) { id app active } }`, map[string]any{"human": human}, "", &identity); err != nil {
		return err
	}
	if !identity.Viewer.App || identity.Viewer.ID == "" || identity.Organization.ID == "" || identity.User.ID != human || identity.User.App || !identity.User.Active {
		return errors.New("expected an app identity and an active human user in its workspace")
	}
	c.AgentID, c.AgentName, c.WorkspaceID = identity.Viewer.ID, identity.Viewer.Name, identity.Organization.ID
	be.oauth = c // only to compute the connection id; no further calls on be
	if err = withOAuthLock(func() error {
		// Pending work (an open session, a final sync) is pinned to its connection.
		// Reconnecting the SAME workspace/agent/human, e.g. after the token was
		// revoked, is how that work gets finished, so it is allowed.
		if err := linearConnectionCompatible(be.connectionID()); err != nil {
			return err
		}
		return saveOAuth(c)
	}); err != nil {
		return err
	}
	fmt.Printf("Connected %s; you stay the accountable owner (%s). Credentials saved to %s.\nUse TK_MODE=linear tk next. Restart tkd after changing its connection.\n", c.AgentName, c.HumanID, activeSecretStore().kind())
	return nil
}
func linearConnectionIdle() error {
	all, err := loadSessions()
	if err != nil {
		return err
	}
	if len(all) > 0 {
		return errors.New("finish the open sessions before changing Linear login")
	}
	closed, err := filepath.Glob(filepath.Join(tkHome(), "closed", "*.json"))
	if err != nil {
		return err
	}
	if len(closed) > 0 {
		return errors.New("wait for final cost sync before changing Linear login")
	}
	return nil
}

// linearConnectionCompatible allows a login when no work is pending, or when all
// pending work belongs to the connection being (re)established.
func linearConnectionCompatible(conn string) error {
	if linearConnectionIdle() == nil {
		return nil
	}
	pending, err := pendingConnections()
	if err != nil {
		return err
	}
	for _, p := range pending {
		if p != conn {
			return errors.New("an open session or final cost sync belongs to another connection; finish it (or wait) before connecting a different Linear app or user")
		}
	}
	return nil
}

// pendingConnections lists the connection ids of the open session and of closed
// sessions still awaiting their final cost sync.
func pendingConnections() ([]string, error) {
	var out []string
	all, err := loadSessions()
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		out = append(out, s.ConnectionID)
	}
	files, err := filepath.Glob(filepath.Join(tkHome(), "closed", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var s session
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("unreadable %s: %w", f, err)
		}
		out = append(out, s.ConnectionID)
	}
	return out, nil
}

func cmdLinearLogout() error {
	return withOAuthLock(func() error {
		if err := linearConnectionIdle(); err != nil {
			return err
		}
		had := getSecret("linear-oauth") != ""
		if err := deleteSecret("linear-oauth"); err != nil {
			return err
		}
		if had {
			fmt.Println("Local OAuth credentials removed; workspace installation was not revoked. Personal-key compatibility remains available.")
		}
		return nil
	})
}

func argAt(args []string, i int) string {
	if i < len(args) {
		return strings.TrimSpace(args[i])
	}
	return ""
}

// resolveLinearHuman turns "who you are" into a Linear user id: an id as given;
// an email looked up in the workspace; nothing given: the viewer of a saved
// personal API key, else `git config user.email`.
func resolveLinearHuman(app *linearBackend, hint string) (string, error) {
	if hint != "" && !strings.Contains(hint, "@") {
		return hint, nil
	}
	if hint == "" {
		if key := getSecret("linear", "TK_LINEAR_API_KEY", "LINEAR_API_KEY"); key != "" {
			personal := &linearBackend{url: app.url, key: key, http: app.http}
			if id, err := personal.viewerID(); err == nil && id != "" {
				return id, nil
			}
		}
		if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
			hint = strings.TrimSpace(string(out))
		}
	}
	if hint == "" {
		return "", errors.New("couldn't tell which Linear user you are: run tk login linear <client-id> <your email>, or set linear.user")
	}
	var res struct {
		Users struct {
			Nodes []struct {
				ID          string
				App, Active bool
			}
		}
	}
	if err := app.gql(`query($email: String!) { users(filter: { email: { eq: $email } }) { nodes { id app active } } }`, map[string]any{"email": hint}, "", &res); err != nil {
		return "", err
	}
	for _, u := range res.Users.Nodes {
		if !u.App && u.Active {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("no active Linear user with email %s in this workspace: pass your Linear email or user id", hint)
}
