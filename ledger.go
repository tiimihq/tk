package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ledger records which Claude Code replies of one session run have been posted
// as cost events. Batches are written as pending before they are posted (a
// write-ahead log): after a crash, a pending batch is re-posted with the same
// idempotency key, so Paperclip ignores the repeat and nothing is counted twice.
type ledger struct {
	path    string
	Posted  map[string]string       `json:"posted"`  // reply key -> batch key
	Pending map[string]pendingBatch `json:"pending"` // batch key -> batch awaiting confirmation
}

type pendingBatch struct {
	Replies []string       `json:"replies"`
	Event   map[string]any `json:"event"`
}

func ledgerPath(runID string) string { return filepath.Join(tkHome(), "ledger", runID+".json") }

func loadLedger(runID string) (*ledger, error) {
	l := &ledger{path: ledgerPath(runID), Posted: map[string]string{}, Pending: map[string]pendingBatch{}}
	raw, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, l); err != nil {
		return nil, err
	}
	if l.Posted == nil {
		l.Posted = map[string]string{}
	}
	if l.Pending == nil {
		l.Pending = map[string]pendingBatch{}
	}
	return l, nil
}

func (l *ledger) save() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func (l *ledger) commit(batchKey string) {
	for _, r := range l.Pending[batchKey].Replies {
		l.Posted[r] = batchKey
	}
	delete(l.Pending, batchKey)
}

// batchKey is deterministic for a set of replies, so a re-sent batch carries the same key.
func batchKey(runID, model string, replies []string) string {
	sorted := append([]string(nil), replies...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(runID + "\n" + model + "\n" + strings.Join(sorted, "\n")))
	return "tk-batch:" + hex.EncodeToString(sum[:16])
}
