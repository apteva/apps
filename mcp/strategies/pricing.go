package main

import "strings"

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
	T int64   `json:"t"` // unix seconds
	O float64 `json:"o,omitempty"`
	H float64 `json:"h,omitempty"`
	L float64 `json:"l,omitempty"`
	C float64 `json:"c,omitempty"`
	V float64 `json:"v,omitempty"`
	// polymarket-only
	Yes float64 `json:"yes,omitempty"`
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
	if strings.HasPrefix(s, "POLY:") {
		return "polymarket"
	}
	if strings.HasSuffix(s, "-USD") {
		return "crypto"
	}
	return "equity"
}

func canonicalSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	upper := strings.ToUpper(symbol)
	if strings.HasPrefix(upper, "POLY:") {
		payload := strings.TrimSpace(symbol[len("POLY:"):])
		if strings.HasPrefix(strings.ToLower(payload), "tokenid:") {
			return "POLY:tokenid:" + strings.TrimSpace(payload[len("tokenid:"):])
		}
		return "POLY:" + strings.ToLower(payload)
	}
	return upper
}
