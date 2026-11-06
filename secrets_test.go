package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsLookupOrderAndImport(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	// legacy file is still read
	_ = os.WriteFile(filepath.Join(tkHome(), "jira_token"), []byte("legacy-token\n"), 0o600)
	if got := getSecret("jira", "JIRA_API_TOKEN"); got != "legacy-token" {
		t.Fatalf("legacy: %q", got)
	}
	// the store beats the legacy file
	if err := setSecret("jira", "stored-token"); err != nil {
		t.Fatal(err)
	}
	if got := getSecret("jira", "JIRA_API_TOKEN"); got != "stored-token" {
		t.Fatalf("store: %q", got)
	}
	info, _ := os.Stat(fileSecrets{}.path("jira"))
	if !ownerOnly(info) {
		t.Fatalf("secret file mode %v", info.Mode().Perm())
	}
	// the environment beats everything
	t.Setenv("JIRA_API_TOKEN", "env-token")
	if got := getSecret("jira", "JIRA_API_TOKEN"); got != "env-token" {
		t.Fatalf("env: %q", got)
	}
	// delete removes store and legacy copies
	if err := deleteSecret("jira"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIRA_API_TOKEN", "")
	if got := getSecret("jira", "JIRA_API_TOKEN"); got != "" || fileExists(filepath.Join(tkHome(), "jira_token")) {
		t.Fatalf("after delete: %q", got)
	}
	if err := setSecret("bad name!", "x"); err == nil {
		t.Fatal("unsafe secret name accepted")
	}
	// import refuses when there's no OS store (here: the file store)
	if err := importLegacySecrets(); err == nil || !strings.Contains(err.Error(), "no OS secret store") {
		t.Fatalf("import: %v", err)
	}
}
