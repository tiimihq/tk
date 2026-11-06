package main

import (
	"os"
	"testing"
)

// Tests must never touch the developer's real Keychain or Secret Service.
func TestMain(m *testing.M) {
	os.Setenv("TK_SECRET_STORE", "file")
	os.Exit(m.Run())
}
