package main

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
