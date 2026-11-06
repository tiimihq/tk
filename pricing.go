package main

import (
	"math/big"
	"strings"
	"time"
)

// API list prices, used to estimate what each agent reply would cost at the
// provider's standard rates. Developers on a subscription don't pay these per
// token, so tk calls them "API-equivalent" and records them as estimates.
//
// Checked 2026-11-03 on each provider's own page:
//   Anthropic  https://platform.claude.com/docs/en/about-claude/pricing
//   OpenAI     https://developers.openai.com/api/docs/pricing  (standard tier)
//   Google     https://aistudio.google.com/docs/pricing        (paid tier)
//
// Not modelled: batch/flex/priority/fast tiers (replies marked fast or priority
// stay unpriced), Gemini cache storage per hour, server-side tool fees.

// usdPrice is USD per million tokens. Cache writes are Anthropic-only (OpenAI and
// Google cache implicitly and charge only for cached reads).
type usdPrice struct{ in, cached, write5m, write1h, out string }

type rate struct {
	provider string
	model    string // exact id; a dated variant (model-2026...) also matches
	from     string // YYYY-MM-DD the price takes effect ("" = before any change)
	over     int64  // prompt size above which `high` applies (0 = no tier)
	base     usdPrice
	high     usdPrice
	version  string
}

const (
	anthropicV = "anthropic-list-2026-11-03"
	openaiV    = "openai-standard-2026-11-03"
	googleV    = "google-paid-2026-11-03"
)

var rates = []rate{
	// Anthropic: input, cache read, 5m write, 1h write, output
	{provider: "anthropic", model: "claude-opus-5-5", base: usdPrice{"4", "0.20", "5", "8", "20"}, version: anthropicV},
	{provider: "anthropic", model: "claude-opus-5", base: usdPrice{"5", "0.50", "6.25", "10", "25"}, version: anthropicV},
	{provider: "anthropic", model: "claude-sonnet-5-5", base: usdPrice{"2", "0.10", "2.50", "4", "10"}, version: anthropicV},
	{provider: "anthropic", model: "claude-sonnet-5", base: usdPrice{"2", "0.20", "2.50", "4", "10"}, version: anthropicV},
	{provider: "anthropic", model: "claude-haiku-5-5", over: 100_000,
		base: usdPrice{"0.10", "0.01", "0.125", "0.20", "0.50"}, high: usdPrice{"0.50", "0.05", "0.625", "1", "2.50"}, version: anthropicV},
	{provider: "anthropic", model: "claude-fable-5-1", base: usdPrice{"10", "0.25", "12.50", "20", "50"}, version: anthropicV},
	{provider: "anthropic", model: "claude-fable-5", base: usdPrice{"10", "1", "12.50", "20", "50"}, version: anthropicV},

	// OpenAI: input, cached input, output; long context above 272K input tokens
	{provider: "openai", model: "gpt-6-astra", over: 272_000, base: usdPrice{in: "10", cached: "1", out: "50"}, high: usdPrice{in: "20", cached: "2", out: "75"}, version: openaiV},
	{provider: "openai", model: "gpt-6-sol", over: 272_000, base: usdPrice{in: "2", cached: "0.20", out: "10"}, high: usdPrice{in: "4", cached: "0.40", out: "15"}, version: openaiV},
	{provider: "openai", model: "gpt-6.1-sol", over: 272_000, base: usdPrice{in: "2", cached: "0.10", out: "10"}, high: usdPrice{in: "4", cached: "0.20", out: "15"}, version: openaiV},
	{provider: "openai", model: "gpt-6.1-terra", over: 272_000, base: usdPrice{in: "1", cached: "0.05", out: "5"}, high: usdPrice{in: "2", cached: "0.10", out: "7.50"}, version: openaiV},
	{provider: "openai", model: "gpt-6-luna", over: 272_000, base: usdPrice{in: "0.10", cached: "0.01", out: "0.50"}, high: usdPrice{in: "0.20", cached: "0.02", out: "0.75"}, version: openaiV},
	{provider: "openai", model: "gpt-5.3-codex", base: usdPrice{in: "1.75", cached: "0.175", out: "14"}, version: openaiV},

	// Google: input, cached (context caching), output incl. thinking
	{provider: "google", model: "gemini-4-argon", base: usdPrice{in: "2", cached: "0.10", out: "10"}, version: googleV},
	{provider: "google", model: "gemini-4-argon", from: "2026-12-01", base: usdPrice{in: "4", cached: "0.20", out: "20"}, version: googleV},
	{provider: "google", model: "gemini-3.8-flash", base: usdPrice{in: "0.75", cached: "0.075", out: "3.75"}, version: googleV},
	{provider: "google", model: "gemini-3.8-flash", from: "2027-01-01", base: usdPrice{in: "1.50", cached: "0.15", out: "7.50"}, version: googleV},
	{provider: "google", model: "gemini-3.6-flash", base: usdPrice{in: "0.75", cached: "0.075", out: "3.75"}, version: googleV},
	{provider: "google", model: "gemini-3.6-flash", from: "2027-01-01", base: usdPrice{in: "1.50", cached: "0.15", out: "7.50"}, version: googleV},
	{provider: "google", model: "gemini-3.5-flash-lite", base: usdPrice{in: "0.30", cached: "0.03", out: "2.50"}, version: googleV},
	{provider: "google", model: "gemini-3.1-pro-preview", over: 200_000, base: usdPrice{in: "2", cached: "0.20", out: "12"}, high: usdPrice{in: "4", cached: "0.40", out: "18"}, version: googleV},
	{provider: "google", model: "gemini-3.1-pro-preview-customtools", over: 200_000, base: usdPrice{in: "2", cached: "0.20", out: "12"}, high: usdPrice{in: "4", cached: "0.40", out: "18"}, version: googleV},
	{provider: "google", model: "gemini-3.1-flash-lite", base: usdPrice{in: "0.25", cached: "0.025", out: "1.50"}, version: googleV},
	{provider: "google", model: "gemini-3-flash-preview", base: usdPrice{in: "0.50", cached: "0.05", out: "3"}, version: googleV},
}

const pricingEvidence = "provider list prices checked 2026-11-03 (platform.claude.com/docs/en/about-claude/pricing, " +
	"developers.openai.com/api/docs/pricing, aistudio.google.com/docs/pricing); standard tier; " +
	"API-equivalent estimate, may be covered by a subscription; not an invoice"

// providerFor maps a log's provider to the price list it uses. OpenCode reports
// its own provider ids (anthropic, openai, google, openrouter, ...).
func providerFor(m claudeMessage) string {
	switch p := strings.ToLower(m.Provider); p {
	case "", "anthropic":
		return "anthropic"
	case "openai":
		return "openai"
	case "google", "gemini", "vertex", "google-vertex":
		return "google"
	default:
		return p
	}
}

// findRate returns the price in force for the model at time at.
func findRate(provider, model string, at time.Time) *rate {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "models/") // Gemini sometimes reports "models/<id>"
	var best *rate
	for i := range rates {
		r := &rates[i]
		if r.provider != provider || !(model == r.model || strings.HasPrefix(model, r.model+"-20")) {
			continue
		}
		if r.from != "" {
			if from, _ := time.Parse("2006-01-02", r.from); at.Before(from) {
				continue
			}
		}
		if best == nil || r.from > best.from {
			best = r
		}
	}
	return best
}

var million = big.NewRat(1_000_000, 1)

// priceCents returns the reply's estimated cost in US cents as an exact decimal
// string, and the price list version; "" when the model or tier isn't priced.
func priceCents(m claudeMessage) (string, string) {
	if c, v, ok := priceFromSources(m); ok { // your overrides, then the downloaded list
		return c, v
	}
	if m.Tier != "" && m.Tier != "standard" { // fast / priority replies cost more; not in the built-in table
		return "", ""
	}
	r := findRate(providerFor(m), m.Model, m.At)
	if r == nil {
		return "", ""
	}
	p := r.base
	if r.over > 0 && m.Input+m.CacheRead+m.CacheWrite > r.over {
		p = r.high
	}
	c, ok := centsFor(m, p)
	if !ok {
		return "", ""
	}
	return c, r.version
}

// centsFor applies USD-per-million prices to a reply's tokens, exactly.
func centsFor(m claudeMessage, p usdPrice) (string, bool) {
	write1h := m.CacheWrite - m.CacheWrite5m
	total := new(big.Rat)
	for _, part := range []struct {
		tokens int64
		usd    string
	}{{m.Input, p.in}, {m.CacheRead, p.cached}, {m.CacheWrite5m, p.write5m}, {write1h, p.write1h}, {m.Output, p.out}} {
		if part.tokens == 0 {
			continue
		}
		usd, ok := new(big.Rat).SetString(part.usd)
		if !ok { // e.g. a cache write on a provider with no write price
			return "", false
		}
		total.Add(total, new(big.Rat).Mul(usd, big.NewRat(part.tokens, 1)))
	}
	cents := new(big.Rat).Quo(new(big.Rat).Mul(total, big.NewRat(100, 1)), million)
	return cents.FloatString(6), true
}
