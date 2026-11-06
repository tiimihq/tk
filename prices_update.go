package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Keeping prices current. Prices change, so tk records tokens as the permanent
// fact and dollars as an estimate at the time of the work. Price sources, first
// match wins:
//   1. ~/.tk/prices.json      your own rates (negotiated, enterprise)
//   2. ~/.tk/prices/litellm.json  LiteLLM's community price list, downloaded
//      only when you run `tk cost update` (ccusage uses the same list)
//   3. the verified built-in table (pricing.go), also the offline fallback
// A downloaded price more than 5x away from the verified one is ignored with a
// warning: that's more likely a data error than a real price change.

const defaultPricesURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

func pricesDir() string         { return filepath.Join(tkHome(), "prices") }
func liteLLMPath() string       { return filepath.Join(pricesDir(), "litellm.json") }
func liteLLMMetaPath() string   { return filepath.Join(pricesDir(), "litellm.meta.json") }
func userPricesPath() string    { return filepath.Join(tkHome(), "prices.json") }
func builtinPricesDate() string { return "2026-11-03" }

type pricesMeta struct {
	URL       string `json:"url"`
	FetchedAt string `json:"fetchedAt"`
	SHA256    string `json:"sha256"`
	Models    int    `json:"models"`
}

// ---- loading (once per process) ----

var (
	priceMu    sync.Mutex
	priceStamp string // modification times of the price files when last loaded
	liteLLM    map[string]map[string]any
	liteMeta   pricesMeta
	userRates  map[string]userRate
)

// userRate is one entry of ~/.tk/prices.json: USD per million tokens.
type userRate struct {
	Provider string `json:"provider"`
	In       string `json:"input"`
	Cached   string `json:"cacheRead"`
	Write5m  string `json:"cacheWrite5m"`
	Write1h  string `json:"cacheWrite1h"`
	Out      string `json:"output"`
}

// loadPriceSources (re)loads the price files whenever they change on disk, so a
// long-running tkd picks up `tk cost update` and edits to ~/.tk/prices.json.
func loadPriceSources() {
	priceMu.Lock()
	defer priceMu.Unlock()
	stamp := ""
	for _, p := range []string{liteLLMPath(), liteLLMMetaPath(), userPricesPath()} {
		if info, err := os.Stat(p); err == nil {
			stamp += p + info.ModTime().String() + strconv.FormatInt(info.Size(), 10) + ";"
		}
	}
	if stamp == priceStamp && (liteLLM != nil || userRates != nil || stamp == "") {
		return
	}
	priceStamp, liteLLM, liteMeta, userRates = stamp, nil, pricesMeta{}, nil
	if raw, err := os.ReadFile(liteLLMPath()); err == nil {
		_ = json.Unmarshal(raw, &liteLLM)
	}
	if raw, err := os.ReadFile(liteLLMMetaPath()); err == nil {
		_ = json.Unmarshal(raw, &liteMeta)
	}
	if raw, err := os.ReadFile(userPricesPath()); err == nil {
		_ = json.Unmarshal(raw, &userRates)
	}
}

// resetPriceSources forgets loaded sources (tests switch TK_HOME).
func resetPriceSources() {
	priceMu.Lock()
	defer priceMu.Unlock()
	priceStamp, liteLLM, liteMeta, userRates = "-", nil, pricesMeta{}, nil
}

// ---- pricing from the sources ----

// priceFromSources prices a reply from your overrides or the downloaded list;
// ok=false means neither knows the model and the built-in table should be used.
func priceFromSources(m claudeMessage) (cents string, version string, ok bool) {
	loadPriceSources()
	model := normalizeModel(m.Model)
	if r, found := userRates[model]; found && (r.Provider == "" || r.Provider == providerFor(m)) {
		c, ok := centsFor(m, usdPrice{r.In, r.Cached, r.Write5m, r.Write1h, r.Out})
		return c, "user-prices", ok
	}
	e := liteLLMEntry(providerFor(m), model)
	if e == nil {
		return "", "", false
	}
	p, ok := liteLLMPrice(e, m)
	if !ok {
		return "", "", false
	}
	if built := findRate(providerFor(m), m.Model, m.At); built != nil {
		if farApart(built.base.in, p.in) {
			return "", "", false // implausible; the verified table wins
		}
		// A price change the verified table knows about, dated after the list was
		// downloaded, applies to replies from that date: the list can't know it yet.
		if fetched, err := time.Parse(time.RFC3339, liteMeta.FetchedAt); err == nil && built.from != "" {
			if from, _ := time.Parse("2006-01-02", built.from); from.After(fetched) && !m.At.Before(from) {
				return "", "", false
			}
		}
	}
	c, ok := centsFor(m, p)
	return c, "litellm-" + firstNonEmpty(strings.SplitN(liteMeta.FetchedAt, "T", 2)[0], "snapshot"), ok
}

func normalizeModel(model string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
}

func liteLLMEntry(provider, model string) map[string]any {
	if liteLLM == nil {
		return nil
	}
	want := map[string][]string{"anthropic": {"anthropic"}, "openai": {"openai"}, "google": {"gemini", "vertex_ai-language-models"}}[provider]
	for _, key := range []string{model, "gemini/" + model, provider + "/" + model} {
		e := liteLLM[key]
		if e == nil {
			continue
		}
		p, _ := e["litellm_provider"].(string)
		for _, w := range want {
			if p == w {
				return e
			}
		}
	}
	return nil
}

var aboveRe = regexp.MustCompile(`^input_cost_per_token_above_(\d+)k_tokens$`)

// liteLLMPrice picks the fields for this reply: its prompt-size tier, its
// service tier (priority) and Anthropic's 1-hour cache writes.
func liteLLMPrice(e map[string]any, m claudeMessage) (usdPrice, bool) {
	suffix := ""
	switch m.Tier {
	case "", "standard":
	case "priority":
		suffix = "_priority"
	default: // fast mode: not in the list
		return usdPrice{}, false
	}
	tier := ""
	prompt := m.Input + m.CacheRead + m.CacheWrite
	best := int64(0)
	for k := range e {
		if sm := aboveRe.FindStringSubmatch(k); sm != nil {
			if n, _ := strconv.ParseInt(sm[1], 10, 64); prompt > n*1000 && n*1000 > best {
				best, tier = n*1000, "_above_"+sm[1]+"k_tokens"
			}
		}
	}
	get := func(base string) string {
		for _, k := range []string{base + tier + suffix, base + suffix, base + tier, base} {
			if v, ok := e[k].(float64); ok {
				return perMillion(v)
			}
		}
		return ""
	}
	p := usdPrice{in: get("input_cost_per_token"), cached: get("cache_read_input_token_cost"), out: get("output_cost_per_token")}
	if providerFor(m) == "anthropic" { // only Anthropic charges cache writes
		p.write5m = get("cache_creation_input_token_cost")
		p.write1h = firstNonEmpty(get("cache_creation_input_token_cost_above_1hr"), p.write5m)
	}
	if p.in == "" || p.out == "" {
		return usdPrice{}, false
	}
	if p.cached == "" {
		p.cached = p.in
	}
	return p, true
}

func perMillion(perToken float64) string {
	r := new(big.Rat).SetFloat64(perToken)
	if r == nil {
		return ""
	}
	return new(big.Rat).Mul(r, million).FloatString(6)
}

// farApart reports whether two USD prices differ by more than 5x.
func farApart(a, b string) bool {
	x, ok1 := new(big.Rat).SetString(a)
	y, ok2 := new(big.Rat).SetString(b)
	if !ok1 || !ok2 || x.Sign() <= 0 || y.Sign() <= 0 {
		return false
	}
	ratio := new(big.Rat).Quo(x, y)
	return ratio.Cmp(big.NewRat(5, 1)) > 0 || ratio.Cmp(big.NewRat(1, 5)) < 0
}

// ---- tk cost update ----

func cmdCostUpdate() error {
	url := firstNonEmpty(os.Getenv("TK_PRICES_URL"), defaultPricesURL)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("downloading prices: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading prices: HTTP %d from %s", resp.StatusCode, url)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var list map[string]map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("price list from %s is not valid JSON: %w", url, err)
	}
	priced := 0
	for _, e := range list {
		if _, ok := e["input_cost_per_token"].(float64); ok {
			priced++
		}
	}
	if priced < 100 {
		return fmt.Errorf("price list from %s has only %d priced models; not using it", url, priced)
	}
	sum := sha256.Sum256(raw)
	meta := pricesMeta{URL: url, FetchedAt: time.Now().UTC().Format(time.RFC3339), SHA256: hex.EncodeToString(sum[:]), Models: priced}
	if err := os.MkdirAll(pricesDir(), 0o700); err != nil {
		return err
	}
	if err := writeBackedUp(liteLLMPath(), nil, raw); err != nil {
		return err
	}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := writeBackedUp(liteLLMMetaPath(), nil, append(mb, '\n')); err != nil {
		return err
	}
	resetPriceSources()
	loadPriceSources()
	fmt.Printf("Prices updated: %d models from %s\n", priced, url)
	fmt.Println(comparePrices())
	return nil
}

// comparePrices lists, for every model in the verified table, whether the
// downloaded list agrees: the quickest way to spot a price change.
func comparePrices() string {
	type row struct{ model, note string }
	var rows []row
	seen := map[string]bool{}
	now := time.Now()
	for _, r := range rates {
		k := r.provider + "/" + r.model
		if seen[k] {
			continue
		}
		seen[k] = true
		built := findRate(r.provider, r.model, now)
		e := liteLLMEntry(r.provider, r.model)
		if e == nil {
			rows = append(rows, row{r.model, "not in the downloaded list (verified price used)"})
			continue
		}
		p, ok := liteLLMPrice(e, claudeMessage{Provider: r.provider, Model: r.model})
		if !ok {
			rows = append(rows, row{r.model, "no usable price in the list (verified price used)"})
			continue
		}
		var diffs []string
		for _, f := range []struct{ name, a, b string }{{"input", built.base.in, p.in}, {"cache read", built.base.cached, p.cached}, {"output", built.base.out, p.out}} {
			if !sameUSD(f.a, f.b) {
				note := fmt.Sprintf("%s $%s → $%s", f.name, trimUSD(f.a), trimUSD(f.b))
				if farApart(f.a, f.b) {
					note += " (over 5x: ignored)"
				}
				diffs = append(diffs, note)
			}
		}
		if len(diffs) == 0 {
			rows = append(rows, row{r.model, "same as verified"})
		} else {
			rows = append(rows, row{r.model, "CHANGED: " + strings.Join(diffs, ", ")})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].model < rows[j].model })
	var b strings.Builder
	b.WriteString("Verified table (" + builtinPricesDate() + ") vs downloaded list, per million tokens:")
	for _, r := range rows {
		fmt.Fprintf(&b, "\n  %-36s %s", r.model, r.note)
	}
	return b.String()
}

func sameUSD(a, b string) bool {
	x, ok1 := new(big.Rat).SetString(a)
	y, ok2 := new(big.Rat).SetString(b)
	return ok1 && ok2 && x.Cmp(y) == 0
}

func trimUSD(s string) string {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return s
	}
	out := strings.TrimRight(strings.TrimRight(r.FloatString(4), "0"), ".")
	return out
}

// priceSourceNote describes where prices come from, with a staleness hint.
func priceSourceNote() string {
	loadPriceSources()
	if liteMeta.FetchedAt != "" {
		t, _ := time.Parse(time.RFC3339, liteMeta.FetchedAt)
		note := fmt.Sprintf("prices: downloaded list from %s", t.Local().Format("2006-01-02"))
		if time.Since(t) > 30*24*time.Hour {
			note += " (over 30 days old: tk cost update)"
		}
		return note
	}
	built, _ := time.Parse("2006-01-02", builtinPricesDate())
	note := "prices: verified table from " + builtinPricesDate()
	if time.Since(built) > 60*24*time.Hour {
		note += " (over 60 days old: tk cost update)"
	}
	return note
}
