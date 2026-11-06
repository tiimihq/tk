package main

import (
	"os"
	"path/filepath"
)

// withFileLock runs fn holding an exclusive advisory lock on path. tk and tkd are
// separate processes, so shared state (the open session, a session's cost ledger)
// is only read-modified-written under one of these locks. flock locks belong to the
// open file, so never take the same lock twice in one call chain: it would block.
func withFileLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)
	return fn()
}

// withSessionLock guards opening, finishing and closing the session. Lock order:
// session lock, then a ledger lock; never the reverse.
func withSessionLock(fn func() error) error {
	return withFileLock(filepath.Join(tkHome(), "session.lock"), fn)
}
