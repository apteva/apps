package main

// Alpaca Market Data — live equity / ETF quotes for live portfolios
// bound to Alpaca. Unlike binancePublic (which calls the venue
// directly, no auth), Alpaca requires an API key. We route through
// the bound alpaca-market-data integration via ExecuteIntegrationTool
// so credentials stay in the platform; the trading sidecar never
// handles raw keys.
//
// When the connection isn't bound, the liveProvider falls back to mock
// equity walks. Pre-trade cash checks then use mock prices and we log
// loudly — fine for paper, not safe for live agents trading real money.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const alpacaMarketDataSlug = "alpaca-market-data"

type alpacaMarketData struct {
	platform sdk.PlatformClient
	logger   sdk.Logger

	// Lookup the connection once per TTL window — operator binds /
	// unbinds rarely; the cost of a /connections list per quote is
	// avoidable noise on the platform.
	mu       sync.Mutex
	connID   int64
	connAt   time.Time
	connTTL  time.Duration
}

func newAlpacaMarketData(platform sdk.PlatformClient, logger sdk.Logger) *alpacaMarketData {
	return &alpacaMarketData{
		platform: platform,
		logger:   logger,
		connTTL:  60 * time.Second,
	}
}

// available reports whether the operator has bound an alpaca-market-data
// connection. Used by liveProvider to decide between live-equity and
// mock-equity per quote.
func (a *alpacaMarketData) available() bool {
	_, ok := a.resolveConnection()
	return ok
}

// resolveConnection — cached lookup. Returns (id, true) if bound,
// (0, false) otherwise. Cache miss is silent; lifecycle logs go through
// the call-site (logger noise per quote is too much).
func (a *alpacaMarketData) resolveConnection() (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connID != 0 && time.Since(a.connAt) < a.connTTL {
		return a.connID, true
	}
	if a.platform == nil {
		return 0, false
	}
	conns, err := a.platform.ListConnections(sdk.ConnectionFilter{AppSlug: alpacaMarketDataSlug})
	if err != nil {
		return 0, false
	}
	for _, c := range conns {
		if c.Status != "" && c.Status != "active" && c.Status != "connected" {
			continue
		}
		a.connID = c.ID
		a.connAt = time.Now()
		return c.ID, true
	}
	a.connID = 0
	return 0, false
}

// UniverseBatch — pull snapshots for many tickers in one HTTP call.
// Returns a Mark per symbol that came back populated. Symbols that
// Alpaca couldn't resolve (delisted, typoed) are silently absent.
func (a *alpacaMarketData) UniverseBatch(symbols []string) ([]*Mark, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	connID, ok := a.resolveConnection()
	if !ok {
		return nil, fmt.Errorf("alpaca-market-data not bound")
	}
	// Alpaca caps `symbols` at ~50 per call on the snapshot endpoint;
	// our equity universe is well under that, so a single call is fine.
	args := map[string]any{
		"symbols": strings.Join(symbols, ","),
	}
	res, err := a.platform.ExecuteIntegrationTool(connID, "stock_snapshots", args)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("stock_snapshots failed: %s", string(safeBytes(res)))
	}
	return parseAlpacaSnapshots(res.Data)
}

// Quote — single-symbol convenience over UniverseBatch.
func (a *alpacaMarketData) Quote(symbol string) (*Mark, error) {
	marks, err := a.UniverseBatch([]string{symbol})
	if err != nil {
		return nil, err
	}
	for _, m := range marks {
		if strings.EqualFold(m.Symbol, symbol) {
			return m, nil
		}
	}
	return nil, fmt.Errorf("alpaca snapshot: no data for %s", symbol)
}

// parseAlpacaSnapshots — Alpaca returns either:
//
//	{"AAPL": {snap}, "MSFT": {snap}}
//	{"snapshots": {"AAPL": {snap}, ...}}
//
// We try the flat form first (current API response), unwrap if needed.
// Latest trade price is the "current price" used by the engine; daily
// bars give prev_close for the % change panels.
func parseAlpacaSnapshots(raw json.RawMessage) ([]*Mark, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty snapshots response")
	}
	type alpacaTrade struct {
		Price float64 `json:"p"`
		Time  string  `json:"t"`
	}
	type alpacaBar struct {
		Open   float64 `json:"o"`
		High   float64 `json:"h"`
		Low    float64 `json:"l"`
		Close  float64 `json:"c"`
		Volume float64 `json:"v"`
	}
	type alpacaSnap struct {
		LatestTrade  *alpacaTrade `json:"latestTrade"`
		MinuteBar    *alpacaBar   `json:"minuteBar"`
		DailyBar     *alpacaBar   `json:"dailyBar"`
		PrevDailyBar *alpacaBar   `json:"prevDailyBar"`
	}

	// Try wrapped {"snapshots": {...}} first.
	var wrapped struct {
		Snapshots map[string]alpacaSnap `json:"snapshots"`
	}
	var snaps map[string]alpacaSnap
	if jerr := json.Unmarshal(raw, &wrapped); jerr == nil && len(wrapped.Snapshots) > 0 {
		snaps = wrapped.Snapshots
	} else {
		if jerr := json.Unmarshal(raw, &snaps); jerr != nil {
			return nil, fmt.Errorf("decode snapshots: %w", jerr)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]*Mark, 0, len(snaps))
	for sym, s := range snaps {
		price := 0.0
		switch {
		case s.LatestTrade != nil && s.LatestTrade.Price > 0:
			price = s.LatestTrade.Price
		case s.MinuteBar != nil && s.MinuteBar.Close > 0:
			price = s.MinuteBar.Close
		case s.DailyBar != nil && s.DailyBar.Close > 0:
			price = s.DailyBar.Close
		}
		if price <= 0 {
			continue
		}
		mk := &Mark{
			Symbol:     strings.ToUpper(sym),
			AssetClass: inferAssetClass(sym),
			Price:      price,
			MarkedAt:   now,
		}
		if s.PrevDailyBar != nil && s.PrevDailyBar.Close > 0 {
			pc := s.PrevDailyBar.Close
			mk.PrevClose = &pc
		}
		if s.DailyBar != nil && s.DailyBar.Volume > 0 {
			v := s.DailyBar.Volume
			mk.Volume24h = &v
		}
		out = append(out, mk)
	}
	return out, nil
}

func safeBytes(res *sdk.ExecuteResult) []byte {
	if res == nil {
		return nil
	}
	return res.Data
}

// alpacaEquitySymbolsKnown — equity tickers we proactively fetch on
// each tick. Pull from the mock universe so universe ∩ tracked stays
// consistent across paper + live without a separate config knob. Real
// installs will accumulate their own set via watchlists; that's
// handled by liveProvider.Quote for symbols outside this list.
func alpacaEquitySymbolsKnown() []string {
	out := make([]string, 0, 8)
	for _, s := range mockUniverse {
		if s.assetClass == "equity" || s.assetClass == "etf" {
			out = append(out, s.symbol)
		}
	}
	return out
}
