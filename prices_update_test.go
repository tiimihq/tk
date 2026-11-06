package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakePriceList(t *testing.T, models map[string]map[string]any, filler int) *httptest.Server {
	t.Helper()
	list := map[string]any{}
	for i := 0; i < filler; i++ {
		list[fmt.Sprintf("filler-%d", i)] = map[string]any{"input_cost_per_token": 1e-6, "output_cost_per_token": 2e-6, "litellm_provider": "other"}
	}
	for k, v := range models {
		list[k] = v
	}
	b, _ := json.Marshal(list)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) }))
	t.Cleanup(srv.Close)
	return srv
}

func withPriceHome(t *testing.T) {
	t.Helper()
	t.Setenv("TK_HOME", t.TempDir())
	resetPriceSources()
	t.Cleanup(resetPriceSources)
}

func TestCostUpdateAndPriceSources(t *testing.T) {
	withPriceHome(t)
	srv := fakePriceList(t, map[string]map[string]any{
		// Sonnet 5.5 got cheaper upstream: the list should win and the update should say so.
		"claude-sonnet-5-5": {"litellm_provider": "anthropic", "input_cost_per_token": 1.5e-6, "output_cost_per_token": 7.5e-6,
			"cache_read_input_token_cost": 0.075e-6, "cache_creation_input_token_cost": 1.875e-6, "cache_creation_input_token_cost_above_1hr": 3e-6},
		"gpt-6-sol": {"litellm_provider": "openai", "input_cost_per_token": 2e-6, "output_cost_per_token": 10e-6, "cache_read_input_token_cost": 0.2e-6,
			"input_cost_per_token_above_272k_tokens": 4e-6, "output_cost_per_token_above_272k_tokens": 15e-6, "cache_read_input_token_cost_above_272k_tokens": 0.4e-6,
			"input_cost_per_token_priority": 4e-6, "output_cost_per_token_priority": 20e-6, "cache_read_input_token_cost_priority": 0.4e-6},
		// A data error: 10x the verified price. Must be ignored.
		"claude-opus-5-5": {"litellm_provider": "anthropic", "input_cost_per_token": 40e-6, "output_cost_per_token": 200e-6, "cache_read_input_token_cost": 2e-6},
		// A model the verified table doesn't know.
		"gpt-7-preview": {"litellm_provider": "openai", "input_cost_per_token": 3e-6, "output_cost_per_token": 12e-6},
	}, 150)
	t.Setenv("TK_PRICES_URL", srv.URL)

	out := captureStdout(t, func() {
		if err := cmdCostUpdate(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "claude-sonnet-5-5") || !strings.Contains(out, "CHANGED: input $2 → $1.5") ||
		!strings.Contains(out, "over 5x: ignored") || !strings.Contains(out, "gpt-6-sol                            same as verified") {
		t.Fatalf("update report:\n%s", out)
	}
	if !fileExists(liteLLMMetaPath()) {
		t.Fatal("metadata not saved")
	}

	at := at("2026-11-03")
	cases := []struct {
		name, wantCents, wantSource string
		m                           claudeMessage
	}{
		{"list price wins for a changed model", "1.500000", "litellm-", claudeMessage{Model: "claude-sonnet-5-5", At: at, Input: 10_000}},
		{"1-hour cache write from the list", "3.000000", "litellm-", claudeMessage{Model: "claude-sonnet-5-5", At: at, CacheWrite: 10_000}},
		{"5-minute cache write from the list", "1.875000", "litellm-", claudeMessage{Model: "claude-sonnet-5-5", At: at, CacheWrite: 10_000, CacheWrite5m: 10_000}},
		{"long-prompt tier from the list", "120.000000", "litellm-", claudeMessage{Provider: "openai", Model: "gpt-6-sol", At: at, Input: 300_000}},
		{"priority tier from the list", "8.000000", "litellm-", claudeMessage{Provider: "openai", Model: "gpt-6-sol", At: at, Input: 20_000, Tier: "priority"}},
		{"implausible list price falls back to verified", "4.000000", "anthropic-list", claudeMessage{Model: "claude-opus-5-5", At: at, Input: 10_000}},
		{"model only the list knows", "3.000000", "litellm-", claudeMessage{Provider: "openai", Model: "gpt-7-preview", At: at, Input: 10_000}},
		{"fast mode still unpriced", "", "", claudeMessage{Model: "claude-sonnet-5-5", At: at, Input: 10, Tier: "fast"}},
	}
	for _, c := range cases {
		cents, src := priceCents(c.m)
		if cents != c.wantCents || !strings.HasPrefix(src, c.wantSource) {
			t.Errorf("%s: got %q from %q, want %q from %q", c.name, cents, src, c.wantCents, c.wantSource)
		}
	}

	// Your own prices beat everything.
	_ = os.WriteFile(userPricesPath(), []byte(`{"claude-sonnet-5-5":{"provider":"anthropic","input":"1","cacheRead":"0.1","cacheWrite5m":"1","cacheWrite1h":"2","output":"5"}}`), 0o600)
	resetPriceSources()
	if cents, src := priceCents(claudeMessage{Model: "claude-sonnet-5-5", At: at, Input: 10_000}); cents != "1.000000" || src != "user-prices" {
		t.Fatalf("override: %q from %q", cents, src)
	}
	if note := priceSourceNote(); !strings.Contains(note, "downloaded list") {
		t.Fatalf("source note = %q", note)
	}
}

func TestCostUpdateRejectsBadLists(t *testing.T) {
	withPriceHome(t)
	small := fakePriceList(t, nil, 20)
	t.Setenv("TK_PRICES_URL", small.URL)
	if err := cmdCostUpdate(); err == nil || !strings.Contains(err.Error(), "only 20 priced models") {
		t.Fatalf("tiny list accepted: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer bad.Close()
	t.Setenv("TK_PRICES_URL", bad.URL)
	if err := cmdCostUpdate(); err == nil {
		t.Fatal("non-JSON accepted")
	}
	if fileExists(filepath.Join(tkHome(), "prices", "litellm.json")) {
		t.Fatal("a rejected list must not be saved")
	}
	if note := priceSourceNote(); !strings.Contains(note, "verified table") {
		t.Fatalf("source note = %q", note)
	}
}

// Events keep the full token breakdown, so cost can be recomputed later.
func TestEventsKeepTokens(t *testing.T) {
	withPriceHome(t)
	s := &session{SessionID: "s", IssueID: "i", AgentID: "a"}
	_, ev := buildBatch(s, "claude-opus-5-5", []claudeMessage{
		{Key: "a", Model: "claude-opus-5-5", At: at("2026-11-03"), Input: 10, CacheRead: 100, CacheWrite: 30, CacheWrite5m: 10, Output: 5},
		{Key: "b", Model: "claude-opus-5-5", At: at("2026-11-03"), Input: 1, Output: 1},
	})
	tok, _ := ev["tokens"].(map[string]int64)
	if tok["freshInput"] != 11 || tok["cacheRead"] != 100 || tok["cacheWrite5m"] != 10 || tok["cacheWrite1h"] != 20 || tok["output"] != 6 || tok["replies"] != 2 {
		t.Fatalf("tokens = %v", tok)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	buf := make([]byte, 1<<20)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

// A dated increase the verified table knows (Gemini 4 Argon, Dec 1 2026) beats a
// list downloaded before it; a list downloaded after it is trusted again.
func TestDatedIncreaseBeatsOlderDownload(t *testing.T) {
	withPriceHome(t)
	srv := fakePriceList(t, map[string]map[string]any{
		"gemini/gemini-4-argon": {"litellm_provider": "gemini", "input_cost_per_token": 2e-6, "output_cost_per_token": 10e-6, "cache_read_input_token_cost": 0.1e-6},
	}, 150)
	t.Setenv("TK_PRICES_URL", srv.URL)
	captureStdout(t, func() {
		if err := cmdCostUpdate(); err != nil {
			t.Fatal(err)
		}
	})
	m := claudeMessage{Provider: "google", Model: "gemini-4-argon", Input: 1_000_000}
	m.At = at("2026-11-20")
	if c, src := priceCents(m); c != "200.000000" || !strings.HasPrefix(src, "litellm-") {
		t.Fatalf("before Dec 1: %q from %q", c, src)
	}
	m.At = at("2026-12-02")
	if c, src := priceCents(m); c != "400.000000" || src != googleV {
		t.Fatalf("after Dec 1 with a November download: %q from %q", c, src)
	}
}

// tkd runs for days: it must see a new price list without a restart.
func TestPriceFilesReloadWhenChanged(t *testing.T) {
	withPriceHome(t)
	m := claudeMessage{Model: "claude-sonnet-5-5", At: at("2026-11-03"), Input: 10_000}
	if _, src := priceCents(m); src != anthropicV {
		t.Fatalf("no files: %q", src)
	}
	_ = os.WriteFile(userPricesPath(), []byte(`{"claude-sonnet-5-5":{"input":"1","output":"5"}}`), 0o600)
	if c, src := priceCents(m); src != "user-prices" || c != "1.000000" {
		t.Fatalf("after writing overrides (no reset): %q from %q", c, src)
	}
}
