package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// tk login github: sign in through the tk GitHub App with the device flow.
// The user approves a short code on github.com; tk gets a user access token that
// acts for the user with the app's badge ("you + tk" on comments). The device
// flow needs only the app's public client ID, for login and for refresh, so
// nothing secret ships in tk. Tokens last 8 hours and refresh for 6 months.
// The app must be installed on the repository for its tokens to reach it.

// githubPublicClientID is the published tk GitHub App's client ID; empty until
// the app is registered. github.clientId in config or an argument overrides it.
var githubPublicClientID = ""

type githubOAuth struct {
	ClientID         string    `json:"clientId"`
	AccessToken      string    `json:"accessToken"`
	RefreshToken     string    `json:"refreshToken,omitempty"`
	ExpiresAt        time.Time `json:"expiresAt,omitempty"` // zero: doesn't expire
	RefreshExpiresAt time.Time `json:"refreshExpiresAt,omitempty"`
	Login            string    `json:"login"`
}

func githubOAuthBase() string {
	return strings.TrimRight(firstNonEmpty(os.Getenv("TK_GITHUB_OAUTH_URL"), "https://github.com"), "/")
}

// githubPollUnit is how long one "interval" second lasts (tests shorten it).
var githubPollUnit = time.Second

func readGitHubOAuth() (*githubOAuth, error) {
	v := getSecret("github-oauth")
	if v == "" {
		return nil, os.ErrNotExist
	}
	var c githubOAuth
	if err := json.Unmarshal([]byte(v), &c); err != nil || c.AccessToken == "" || c.ClientID == "" {
		return nil, errors.New("GitHub app login is damaged; run tk login github again")
	}
	return &c, nil
}

func saveGitHubOAuth(c *githubOAuth) error {
	raw, _ := json.Marshal(c)
	return setSecret("github-oauth", string(raw))
}

func withGitHubOAuthLock(fn func() error) error {
	return withFileLock(filepath.Join(tkHome(), "github_oauth.lock"), fn)
}

// githubAppToken returns a current access token from the app login, refreshing
// it (under a lock shared by tk and tkd) when it is about to expire. ok=false
// means there is no app login.
func githubAppToken(client *http.Client) (token string, ok bool, err error) {
	if _, err := readGitHubOAuth(); errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	err = withGitHubOAuthLock(func() error {
		c, err := readGitHubOAuth()
		if err != nil {
			return err
		}
		if c.ExpiresAt.IsZero() || time.Until(c.ExpiresAt) > time.Minute {
			token = c.AccessToken
			return nil
		}
		if c.RefreshToken == "" || (!c.RefreshExpiresAt.IsZero() && time.Now().After(c.RefreshExpiresAt)) {
			return errors.New("GitHub app login expired; run tk login github")
		}
		tok, err := githubTokenRequest(client, url.Values{"client_id": {c.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken}})
		if err != nil {
			return fmt.Errorf("refreshing GitHub app login: %w", err)
		}
		applyGitHubToken(c, tok)
		token = c.AccessToken
		return saveGitHubOAuth(c)
	})
	return token, err == nil, err
}

type githubTokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	TokenType             string `json:"token_type"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
	Interval              int    `json:"interval"`
}

func applyGitHubToken(c *githubOAuth, t githubTokenResponse) {
	c.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		c.RefreshToken = t.RefreshToken
	}
	c.ExpiresAt, c.RefreshExpiresAt = time.Time{}, time.Time{}
	if t.ExpiresIn > 0 {
		c.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	if t.RefreshTokenExpiresIn > 0 {
		c.RefreshExpiresAt = time.Now().Add(time.Duration(t.RefreshTokenExpiresIn) * time.Second)
	}
}

// githubTokenRequest posts to GitHub's token endpoint. GitHub answers OAuth
// errors with HTTP 200 and an "error" field, which is returned as-is for the
// device flow to interpret.
func githubTokenRequest(client *http.Client, form url.Values) (githubTokenResponse, error) {
	var t githubTokenResponse
	req, err := http.NewRequest(http.MethodPost, githubOAuthBase()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return t, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return t, &unreachableError{base: githubOAuthBase(), err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, fmt.Errorf("unexpected token response (HTTP %d)", resp.StatusCode)
	}
	if t.Error == "" && (t.AccessToken == "" || resp.StatusCode/100 != 2) {
		return t, fmt.Errorf("token request failed (HTTP %d)", resp.StatusCode)
	}
	if t.Error != "" && form.Get("grant_type") == "refresh_token" {
		return t, fmt.Errorf("%s: %s", t.Error, t.ErrorDescription)
	}
	return t, nil
}

// cmdGitHubLogin: tk login github [client-id]
func cmdGitHubLogin(args []string) error {
	clientID := firstNonEmpty(argAt(args, 0), loadConfig().GitHub.ClientID, githubPublicClientID)
	if clientID == "" {
		return errors.New("no GitHub app client ID yet: register the tk GitHub App (enable device flow) and run tk login github <client-id>, or set github.clientId in ~/.tk/config.json")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, githubOAuthBase()+"/login/device/code", strings.NewReader(url.Values{"client_id": {clientID}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("can't reach GitHub: %w", err)
	}
	var dc struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if json.Unmarshal(raw, &dc) != nil || dc.DeviceCode == "" {
		if dc.Error == "device_flow_disabled" {
			return errors.New("device flow is off for this GitHub App: enable it in the app's settings")
		}
		return fmt.Errorf("GitHub didn't start the login (HTTP %d %s)", resp.StatusCode, dc.Error)
	}
	fmt.Printf("Open %s and enter the code: %s\n", dc.VerificationURI, dc.UserCode)
	if runtime.GOOS == "darwin" && os.Getenv("TK_NO_BROWSER") == "" {
		_ = exec.Command("open", dc.VerificationURI).Start()
	}
	interval := max(dc.Interval, 5)
	deadline := time.Now().Add(time.Duration(max(dc.ExpiresIn, 60)) * githubPollUnit)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * githubPollUnit)
		t, err := githubTokenRequest(client, url.Values{"client_id": {clientID}, "device_code": {dc.DeviceCode},
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
		if err != nil {
			return err
		}
		switch t.Error {
		case "":
			c := &githubOAuth{ClientID: clientID}
			applyGitHubToken(c, t)
			login, err := githubLoginOf(client, c.AccessToken)
			if err != nil {
				return err
			}
			c.Login = login
			if err := withGitHubOAuthLock(func() error { return saveGitHubOAuth(c) }); err != nil {
				return err
			}
			fmt.Printf("Signed in to GitHub as %s through the tk app (saved to %s).\nComments show as you with the tk badge. Install the app on each repository tk should use.\n", login, activeSecretStore().kind())
			return nil
		case "authorization_pending":
		case "slow_down":
			interval = max(t.Interval, interval+5)
		case "expired_token":
			return errors.New("the code expired before it was approved; run tk login github again")
		case "access_denied":
			return errors.New("login was cancelled on GitHub")
		default:
			return fmt.Errorf("GitHub login failed: %s %s", t.Error, t.ErrorDescription)
		}
	}
	return errors.New("the code expired before it was approved; run tk login github again")
}

func githubLoginOf(client *http.Client, token string) (string, error) {
	api := strings.TrimRight(firstNonEmpty(os.Getenv("TK_GITHUB_URL"), loadConfig().GitHub.URL, "https://api.github.com"), "/")
	req, _ := http.NewRequest(http.MethodGet, api+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var u struct {
		Login string `json:"login"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&u) != nil || u.Login == "" {
		return "", fmt.Errorf("GitHub didn't accept the new token (HTTP %d)", resp.StatusCode)
	}
	return u.Login, nil
}

func cmdGitHubLogout() error {
	return withGitHubOAuthLock(func() error {
		had := getSecret("github-oauth") != ""
		if err := deleteSecret("github-oauth"); err != nil {
			return err
		}
		if had {
			fmt.Println("GitHub app login removed from this machine (the app stays installed on your repositories).")
		}
		return nil
	})
}
