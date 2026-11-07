package main

import (
	"strings"
	"testing"
)

func TestBriefKeepsNewestNotes(t *testing.T) {
	body := "## Notes\n"
	for _, n := range []string{"first: reproduced", "second: tried half-up, reverted", "third: plan written", "fourth: HANDOFF next step is the fix"} {
		body += "- " + n + strings.Repeat(" detail", 10) + "\n"
	}
	out := compactSummary(body, 200)
	if !strings.Contains(out, "HANDOFF next step") {
		t.Fatalf("newest note dropped:\n%s", out)
	}
	if strings.Contains(out, "first: reproduced") || !strings.Contains(out, "earlier notes: tk show") {
		t.Fatalf("oldest notes should give way, with a pointer:\n%s", out)
	}
	if full := compactSummary(body, 0); !strings.Contains(full, "first: reproduced") || !strings.Contains(full, "HANDOFF") {
		t.Fatalf("full view must keep every note:\n%s", full)
	}
	long := "## Notes\n- " + strings.Repeat("x", 500) + "\n"
	if out := compactSummary(long, 100); len(out) > 130 || !strings.HasSuffix(out, "…") {
		t.Fatalf("an over-long newest note should be cut: %d chars %q", len(out), out)
	}
}
