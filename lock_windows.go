//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows has no flock; LockFileEx on the first byte gives the same exclusive,
// process-wide advisory lock (released when the handle closes).
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
