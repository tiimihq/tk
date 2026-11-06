package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// The daemon refreshes this cache; tk reads it first so reads are instant and
// keep working when the Paperclip server is unreachable.

const cacheFreshFor = 60 * time.Second

var cacheNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func cacheFile(apiPath string) string {
	return filepath.Join(tkHome(), "cache", cacheNameChars.ReplaceAllString(apiPath, "_")+".json")
}

func writeCache(apiPath string, body []byte) error {
	f := cacheFile(apiPath)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// invalidateIssue drops cached copies that a change to the issue makes stale:
// the issue, its context, and the inbox it may have entered or left.
func invalidateIssue(key string) {
	for _, p := range []string{issuePath(key, ""), issuePath(key, "/heartbeat-context"), "/api/agents/me/inbox-lite"} {
		_ = os.Remove(cacheFile(p))
	}
}

// liveGet asks the server first and caches the answer; only if the server is
// unreachable does it return a cached copy. For moments that must be current,
// such as the brief shown when a Claude session starts.
func liveGet(c *client, apiPath string) ([]byte, error) {
	body, err := c.get(apiPath)
	if err == nil {
		_ = writeCache(apiPath, body)
		return body, nil
	}
	if !isUnreachable(err) {
		return nil, err
	}
	return os.ReadFile(cacheFile(apiPath))
}

// cachedGet returns a fresh cached copy if the daemon wrote one recently;
// otherwise it asks the server and caches the answer. If the server is
// unreachable it falls back to a stale copy and says so on stderr.
func cachedGet(c *client, apiPath string) ([]byte, error) {
	f := cacheFile(apiPath)
	if info, err := os.Stat(f); err == nil && time.Since(info.ModTime()) < cacheFreshFor {
		if body, err := os.ReadFile(f); err == nil {
			return body, nil
		}
	}
	body, err := c.get(apiPath)
	if err == nil {
		_ = writeCache(apiPath, body)
		return body, nil
	}
	if !isUnreachable(err) {
		return nil, err
	}
	info, statErr := os.Stat(f)
	if statErr != nil {
		return nil, err
	}
	stale, readErr := os.ReadFile(f)
	if readErr != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "tk: server unreachable; showing cached copy from %s\n", info.ModTime().Format("15:04:05"))
	return stale, nil
}
