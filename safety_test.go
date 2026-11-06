package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanText(t *testing.T) {
	evil := "Fix login\x1b]0;pwned\x07\x1b[2J\u009b31m‮evil\nline two\tok"
	got := cleanText(evil)
	if strings.ContainsAny(got, "\x1b\x07\u009b‮") || !strings.Contains(got, "line two\tok") || !strings.HasPrefix(got, "Fix login") {
		t.Fatalf("cleanText = %q", got)
	}
	line := formatInboxLine(inboxItem{Identifier: "ENG-1", Title: "a\x1b[31mred", Status: "todo", Priority: "high"})
	if strings.Contains(line, "\x1b") {
		t.Fatalf("inbox line kept an escape: %q", line)
	}
}

func TestIssueKeysCantEscapeTkHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TK_HOME", home)
	for _, key := range []string{"../../escape", "..", "/etc/passwd", "a/b\\c", ""} {
		p := costStorePath(key)
		rel, err := filepath.Rel(filepath.Join(home, "costs"), p)
		if err != nil || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) {
			t.Fatalf("key %q -> %s escapes the costs folder", key, p)
		}
	}
	if err := costStoreAdd("../../escape", map[string]any{"idempotencyKey": "k"}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(filepath.Dir(home), "escape.json")) {
		t.Fatal("wrote outside TK_HOME")
	}
	entries, _ := os.ReadDir(filepath.Join(home, "costs"))
	if len(entries) == 0 {
		t.Fatal("cost record should be inside ~/.tk/costs")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/tk":     "/usr/local/bin/tk",
		"/Users/a b/tk":         "'/Users/a b/tk'",
		"/tmp/x$(rm -rf ~)/tk":  "'/tmp/x$(rm -rf ~)/tk'",
		"/tmp/it's/tk":          `'/tmp/it'\''s/tk'`,
		"/tmp/`whoami`;echo/tk": "'/tmp/`whoami`;echo/tk'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
