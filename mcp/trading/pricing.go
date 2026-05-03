package main

// Pricing provider — v0.1 mock only. Deterministic walks anchored to
// a hand-picked universe of equity / crypto / polymarket symbols, so
// the same scenario produces the same fills across runs.
//
// Hooking up a real provider (yfinance/coingecko/polymarket-clob) is
// a swap of the Provider interface — no other file needs to change.

import (
	"errors"
	"strings"
	"time"
)

type Provider interface {
	// Quote returns a single mark for one symbol. Used by market_quote.
	Quote(symbol string) (*Mark, error)
	// Universe returns every known symbol; the engine refreshes marks
	// for the union of (universe ∩ tracked_symbols).
	Universe() []*Mark
	// Bars returns OHLCV for equity/crypto, or YES probability history
	// for polymarket. Length depends on `range`.
	Bars(symbol, rng string) ([]Bar, error)
}

type Bar struct {
	T int64   `json:"t"`              // unix seconds
	O float64 `json:"o,omitempty"`
	H float64 `json:"h,omitempty"`
	L float64 `json:"l,omitempty"`
	C float64 `json:"c,omitempty"`
	V float64 `json:"v,omitempty"`
	// polymarket-only
	Yes float64 `json:"yes,omitempty"`
}

// ─── Mock provider — deterministic walks ───────────────────────────

type mockProvider struct {
	tick int64 // monotonically increasing; mutated each Universe() call
}

func newMockProvider() Provider { return &mockProvider{} }

// Universe — single source of truth for "what's quotable in v0.1".
// Mirrors the universe the desk UI used to ship with as mock data,
// so the visual story stays continuous as we wire up the real engine.
type seed struct {
	symbol     string
	assetClass string
	anchor     float64 // current price (or YES prob for polymarket)
	bias       int     // controls drift sign in walk
	noAnchor   float64 // polymarket NO; ignored otherwise
	volume     float64
}

var mockUniverse = []seed{
	// Equity
	{"AAPL",     "equity", 224.31,  1, 0,        38_412_000},
	{"NVDA",     "equity", 138.07,  2, 0,       211_900_000},
	{"MSFT",     "equity", 421.18, -1, 0,        19_800_000},
	{"TSLA",     "equity", 244.72, -2, 0,        88_710_000},
	{"GOOGL",    "equity", 167.92,  1, 0,        22_100_000},
	{"META",     "equity", 591.04,  1, 0,        14_200_000},
	// ETF
	{"SPY",      "etf",    567.41,  1, 0,        41_300_000},
	// Crypto
	{"BTC-USD",  "crypto", 67_842.10, 2, 0,  28_400_000_000},
	{"ETH-USD",  "crypto",  3_412.55,-1, 0,  12_700_000_000},
	{"SOL-USD",  "crypto",    218.73, 3, 0,   3_900_000_000},
	{"AVAX-USD", "crypto",     32.18, 1, 0,     402_000_000},
	{"DOGE-USD", "crypto",      0.142,-2, 0,  1_120_000_000},
	// Polymarket — anchor = YES prob
	{"POLY:fed-cut-march",     "polymarket", 0.32, -1, 0.68,   4_280_000},
	{"POLY:recession-2026",    "polymarket", 0.41,  1, 0.59,   8_910_000},
	{"POLY:btc-100k-2026",     "polymarket", 0.78,  3, 0.22,  14_220_000},
	{"POLY:trump-approval-50", "polymarket", 0.34, -1, 0.66,   2_180_000},
	{"POLY:openai-ipo-2026",   "polymarket", 0.18, -1, 0.82,     612_000},
	{"POLY:gpt5-2026",         "polymarket", 0.62,  2, 0.38,   1_840_000},
}

func (m *mockProvider) Universe() []*Mark {
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]*Mark, 0, len(mockUniverse))
	for _, s := range mockUniverse {
		// Tiny pseudo-random walk anchored to s.anchor; deterministic per
		// (symbol, tick).
		drift := walk(s.symbol, m.tick) * s.anchor * 0.0008 // ~8 bps per tick
		bias := float64(s.bias) * s.anchor * 0.00005
		price := s.anchor + drift + bias
		mk := &Mark{
			Symbol:     s.symbol,
			AssetClass: s.assetClass,
			Price:      price,
			MarkedAt:   now,
		}
		if s.assetClass == "polymarket" {
			no := 1 - price
			if price < 0.01 { price = 0.01; no = 0.99 }
			if price > 0.99 { price = 0.99; no = 0.01 }
			mk.Price = round4(price)
			mk.NoPrice = ptr(round4(no))
		}
		pc := s.anchor
		mk.PrevClose = &pc
		v := s.volume
		mk.Volume24h = &v
		out = append(out, mk)
	}
	m.tick++
	return out
}

func (m *mockProvider) Quote(symbol string) (*Mark, error) {
	for _, mk := range m.Universe() {
		if mk.Symbol == symbol {
			return mk, nil
		}
	}
	return nil, errors.New("symbol not in mock universe: " + symbol)
}

// Bars synthesises a longer series anchored to the symbol's current
// mark; polymarket returns YES probability history.
func (m *mockProvider) Bars(symbol, rng string) ([]Bar, error) {
	var anchor, noAnchor float64
	var class string
	for _, s := range mockUniverse {
		if s.symbol == symbol {
			anchor = s.anchor
			noAnchor = s.noAnchor
			class = s.assetClass
			break
		}
	}
	if anchor == 0 {
		return nil, errors.New("symbol not in mock universe: " + symbol)
	}
	n := bucketsForRange(rng)
	now := time.Now().UTC().Unix()
	step := stepForRange(rng) // seconds per bar
	out := make([]Bar, 0, n)
	for i := 0; i < n; i++ {
		t := now - int64(n-i)*step
		w := walk(symbol+rng, int64(i))
		if class == "polymarket" {
			yes := anchor + w*0.04
			if yes < 0.01 { yes = 0.01 }
			if yes > 0.99 { yes = 0.99 }
			out = append(out, Bar{T: t, Yes: round4(yes)})
		} else {
			c := anchor + w*anchor*0.005
			o := c - w*anchor*0.001
			h := c + 0.002*anchor
			l := c - 0.002*anchor
			out = append(out, Bar{T: t, O: o, H: h, L: l, C: c, V: 1_000_000})
		}
	}
	_ = noAnchor // reserved for richer poly history
	return out, nil
}

func bucketsForRange(rng string) int {
	switch strings.ToUpper(rng) {
	case "1D":  return 78
	case "5D":  return 130
	case "1M":  return 220
	case "3M":  return 320
	case "1Y":  return 540
	case "ALL": return 720
	default:    return 78
	}
}

func stepForRange(rng string) int64 {
	switch strings.ToUpper(rng) {
	case "1D":  return 5 * 60       // 5m bars
	case "5D":  return 30 * 60      // 30m
	case "1M":  return 4 * 3600     // 4h
	case "3M":  return 8 * 3600
	case "1Y":  return 24 * 3600
	case "ALL": return 24 * 3600
	default:    return 5 * 60
	}
}

// walk — deterministic [-1, +1] given a string seed and a tick index.
func walk(seed string, tick int64) float64 {
	var s int64
	for i := 0; i < len(seed); i++ {
		s = s*31 + int64(seed[i])
	}
	s += tick * 1664525
	s = s*1664525 + 1013904223
	u := uint32(s)
	return (float64(u)/float64(0xffffffff))*2 - 1
}

func ptr(v float64) *float64 { return &v }

func round4(v float64) float64 {
	return float64(int(v*10000+0.5)) / 10000
}

// inferAssetClass — used by tools to validate a symbol's class without
// consulting the provider. Symbol prefixes carry the type; bare symbols
// default to equity.
func inferAssetClass(symbol string) string {
	s := strings.ToUpper(symbol)
	if strings.HasPrefix(s, "POLY:") { return "polymarket" }
	if strings.HasSuffix(s, "-USD")   { return "crypto" }
	return "equity"
}
