package main

import (
	"io/fs"
	"os"
	"runtime"
	"testing"
)

// realHome is the developer's home folder, for the few tests that only read
// real agent logs when they happen to exist.
var realHome, _ = os.UserHomeDir()

// Tests must never touch the developer's real Keychain, Secret Service or home
// folder: every test starts with a throwaway home.
func TestMain(m *testing.M) {
	os.Setenv("TK_SECRET_STORE", "file")
	home, err := os.MkdirTemp("", "tk-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home) // Windows reads this, not HOME
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// setHome points the home folder at dir on every OS.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// ownerOnly reports whether a file tk wrote is private to its owner. Windows has
// no Unix mode bits (files in the user profile are private through ACLs).
func ownerOnly(info fs.FileInfo) bool {
	return runtime.GOOS == "windows" || info.Mode().Perm() == 0o600
}
