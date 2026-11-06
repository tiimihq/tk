package main

import (
	"testing"
	"time"
)

func at(day string) time.Time { t, _ := time.Parse("2006-01-02", day); return t.Add(12 * time.Hour) }

// Each case is checked against the provider's published per-million prices.
func TestPriceCents(t *testing.T) {
	cases := []struct {
		name string
		m    claudeMessage
		want string // cents; "" = unpriced
	}{
		// Opus 5.5: 1000 in × $4 + 10000 read × $0.20 + 2000 5m write × $5 + 3000 1h write × $8 + 500 out × $20
		{"opus 5.5 with both cache writes", claudeMessage{Model: "claude-opus-5-5", At: at("2026-11-03"), Input: 1000, CacheRead: 10000, CacheWrite: 5000, CacheWrite5m: 2000, Output: 500},
			"5.000000"}, // 0.004+0.002+0.010+0.024+0.010 = $0.050
		{"cache writes default to the 1-hour rate", claudeMessage{Model: "claude-sonnet-5-5", At: at("2026-11-03"), CacheWrite: 1_000_000},
			"400.000000"},
		{"dated model id matches", claudeMessage{Model: "claude-opus-5-20260901", At: at("2026-11-03"), Output: 1_000_000}, "2500.000000"},
		{"haiku 5.5 at or under 100k", claudeMessage{Model: "claude-haiku-5-5", At: at("2026-11-03"), Input: 100_000}, "1.000000"},
		{"haiku 5.5 over 100k", claudeMessage{Model: "claude-haiku-5-5", At: at("2026-11-03"), Input: 100_001}, "5.000050"},
		{"gpt-6-sol short context", claudeMessage{Provider: "openai", Model: "gpt-6-sol", At: at("2026-11-03"), Input: 200_000, CacheRead: 72_000, Output: 1000},
			"42.440000"}, // 0.4 + 0.0144 + 0.01
		{"gpt-6-sol over 272k", claudeMessage{Provider: "openai", Model: "gpt-6-sol", At: at("2026-11-03"), Input: 200_000, CacheRead: 72_001, Output: 1000},
			"84.380040"}, // 0.8 + 0.0288004 + 0.015 = $0.8438004
		{"gemini argon before the Dec 1 increase", claudeMessage{Provider: "google", Model: "gemini-4-argon", At: at("2026-11-30"), Input: 1_000_000}, "200.000000"},
		{"gemini argon after the Dec 1 increase", claudeMessage{Provider: "google", Model: "gemini-4-argon", At: at("2026-12-01"), Input: 1_000_000}, "400.000000"},
		{"gemini reported as models/<id>", claudeMessage{Provider: "google", Model: "models/gemini-3-flash-preview", At: at("2026-11-03"), Output: 1_000_000}, "300.000000"},
		{"gemini 3.1 pro over 200k", claudeMessage{Provider: "google", Model: "gemini-3.1-pro-preview", At: at("2026-11-03"), Input: 150_000, CacheRead: 50_001}, "62.000040"}, // 0.6 + 0.0200004
		{"fast mode is not modelled", claudeMessage{Model: "claude-opus-5-5", Tier: "fast", Output: 100}, ""},
		{"unknown model", claudeMessage{Model: "claude-mystery-9", Output: 100}, ""},
		{"right model, wrong provider", claudeMessage{Provider: "openai", Model: "claude-opus-5-5", Output: 100}, ""},
		{"cache write on a provider without write prices", claudeMessage{Provider: "openai", Model: "gpt-6-sol", CacheWrite: 10}, ""},
	}
	for _, c := range cases {
		got, _ := priceCents(c.m)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A batch never mixes priced and unpriced replies, so unknown cost stays visible.
func TestUnpricedRepliesGetTheirOwnBatch(t *testing.T) {
	group := []claudeMessage{
		{Key: "a", Model: "claude-opus-5-5", At: at("2026-11-03"), Output: 1000},
		{Key: "b", Model: "claude-opus-5-5", At: at("2026-11-03"), Output: 1000, Tier: "fast"},
	}
	parts := splitByPriced(group)
	if len(parts) != 2 || len(parts[0]) != 1 || len(parts[1]) != 1 {
		t.Fatalf("parts = %+v", parts)
	}
	s := &session{SessionID: "s", IssueID: "i", AgentID: "a"}
	_, priced := buildBatch(s, "claude-opus-5-5", parts[0])
	_, unpriced := buildBatch(s, "claude-opus-5-5", parts[1])
	if priced["costStatus"] != "estimated" || priced["costCents"] != "2.000000" || unpriced["costStatus"] != "unpriced" {
		t.Fatalf("priced=%v unpriced=%v", priced, unpriced)
	}
}
