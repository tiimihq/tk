package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Offline writes. When the tracker can't be reached, tk note / handoff / done
// are saved to an outbox and the agent carries on; tkd sends them, oldest first,
// once the tracker answers again. Only "can't connect" errors are queued: a
// tracker that answers with an error (permission, validation) still fails at once.
//
// Caveat: if a request reached the tracker but the answer was lost, the replay
// can post that comment twice. A duplicate note beats a lost one.

type outboxEntry struct {
	ID        string  `json:"id"`
	Mode      string  `json:"mode"` // backend mode it was written for
	Op        string  `json:"op"`   // "note" or "finish"
	Kind      string  `json:"kind,omitempty"`
	Text      string  `json:"text"`
	Session   session `json:"session"`
	CreatedAt string  `json:"createdAt"`
	Attempts  int     `json:"attempts"`
	LastError string  `json:"lastError,omitempty"`
}

func outboxDir() string { return filepath.Join(tkHome(), "outbox") }

func withOutboxLock(fn func() error) error {
	return withFileLock(filepath.Join(tkHome(), "outbox.lock"), fn)
}

// enqueue saves a write for later. Entry ids sort by creation time.
func enqueue(op, kind, text string, s *session) error {
	e := outboxEntry{ID: fmt.Sprintf("%020d-%s", time.Now().UnixNano(), newSessionID()), Mode: configuredMode(), Op: op, Kind: kind,
		Text: text, Session: *s, CreatedAt: nowRFC3339()}
	return withOutboxLock(func() error {
		if err := os.MkdirAll(outboxDir(), 0o700); err != nil {
			return err
		}
		raw, _ := json.Marshal(e)
		return os.WriteFile(filepath.Join(outboxDir(), e.ID+".json"), raw, 0o600)
	})
}

func pendingOutbox() []string {
	files, _ := filepath.Glob(filepath.Join(outboxDir(), "*.json"))
	sort.Strings(files)
	return files
}

const maxOutboxAttempts = 5

// flushOutbox sends queued writes in order. It stops at the first one that still
// can't connect (later writes must not overtake it), and sets aside an entry the
// tracker keeps rejecting, so one bad write can't block the rest forever.
func flushOutbox(be backend, logger *log.Logger) {
	_ = withOutboxLock(func() error {
		for _, f := range pendingOutbox() {
			raw, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var e outboxEntry
			if json.Unmarshal(raw, &e) != nil {
				_ = os.Rename(f, failedPath(f))
				continue
			}
			if e.Mode != configuredMode() {
				continue // written for another backend; waits until that mode is back
			}
			s := e.Session
			if err := validateSessionBackend(be, &s); err != nil {
				continue
			}
			switch e.Op {
			case "note":
				err = be.note(&s, e.Text)
			case "finish":
				_, err = be.finish(&s, e.Kind, e.Text)
			default:
				err = fmt.Errorf("unknown op %q", e.Op)
			}
			if err == nil {
				_ = os.Remove(f)
				logger.Printf("%s queued %s sent", s.Key, describeOp(e))
				continue
			}
			if isUnreachable(err) {
				return nil // still offline: keep this and everything after it
			}
			e.Attempts++
			e.LastError = err.Error()
			if e.Attempts >= maxOutboxAttempts {
				_ = os.Rename(f, failedPath(f))
				logger.Printf("%s queued %s failed %d times, set aside in %s: %v", s.Key, describeOp(e), e.Attempts, filepath.Dir(failedPath(f)), err)
				continue
			}
			b, _ := json.Marshal(e)
			_ = os.WriteFile(f, b, 0o600)
			logger.Printf("%s queued %s failed (attempt %d): %v", s.Key, describeOp(e), e.Attempts, err)
			return nil // keep order: retry this one next tick
		}
		return nil
	})
}

func failedPath(f string) string {
	dir := filepath.Join(outboxDir(), "failed")
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, filepath.Base(f))
}

func describeOp(e outboxEntry) string {
	if e.Op == "finish" {
		return e.Kind
	}
	return e.Op
}

// queueIfOffline saves the write when err means the tracker couldn't be reached.
func queueIfOffline(err error, op, kind, text string, s *session) (bool, error) {
	if err == nil || !isUnreachable(err) {
		return false, err
	}
	if qerr := enqueue(op, kind, text, s); qerr != nil {
		return false, errors.Join(err, qerr)
	}
	return true, nil
}
