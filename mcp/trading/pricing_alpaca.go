package main

// Alpaca Market Data — live equity / ETF quotes for live portfolios
// bound to Alpaca. Unlike binancePublic (which calls the venue
// directly, no auth), Alpaca requires an API key. We route through
// the bound alpaca-market-data integration via ExecuteIntegrationTool
// so credentials stay in the platform; the trading sidecar never
// handles raw keys.
//
// When the connection isn't bound, liveProvider uses Yahoo Finance.
// If neither real provider succeeds, the quote remains unavailable.

import (
	"context"
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
	mu      sync.Mutex
	connID  int64
	connAt  time.Time
	connTTL time.Duration
	feed    string
}

func newAlpacaMarketData(platform sdk.PlatformClient, logger sdk.Logger, feed string) *alpacaMarketData {
	feed = strings.ToLower(strings.TrimSpace(feed))
	if feed == "" {
		feed = "auto"
	}
	return &alpacaMarketData{
		platform: platform,
		logger:   logger,
		connTTL:  60 * time.Second,
		feed:     feed,
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
	if a.feed != "auto" {
		args["feed"] = a.feed
	}
	res, err := executeAlpacaToolWithRetry(context.Background(), a.platform, connID, "stock_snapshots", args)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("stock_snapshots failed: %s", string(safeBytes(res)))
	}
	marks, err := parseAlpacaSnapshots(res.Data)
	if err != nil {
		return nil, err
	}
	for _, mark := range marks {
		mark.Feed = a.feed
	}
	return marks, nil
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
		Size  float64 `json:"s"`
		Time  string  `json:"t"`
	}
	type alpacaQuote struct {
		BidPrice float64 `json:"bp"`
		AskPrice float64 `json:"ap"`
		BidSize  float64 `json:"bs"`
		AskSize  float64 `json:"as"`
		Time     string  `json:"t"`
	}
	type alpacaBar struct {
		Open   float64 `json:"o"`
		High   float64 `json:"h"`
		Low    float64 `json:"l"`
		Close  float64 `json:"c"`
		Volume float64 `json:"v"`
		Time   string  `json:"t"`
	}
	type alpacaSnap struct {
		LatestTrade  *alpacaTrade `json:"latestTrade"`
		LatestQuote  *alpacaQuote `json:"latestQuote"`
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

	out := make([]*Mark, 0, len(snaps))
	for sym, s := range snaps {
		price := 0.0
		markedAt := ""
		switch {
		case s.LatestTrade != nil && s.LatestTrade.Price > 0:
			price = s.LatestTrade.Price
			markedAt = s.LatestTrade.Time
		case s.MinuteBar != nil && s.MinuteBar.Close > 0:
			price = s.MinuteBar.Close
			markedAt = s.MinuteBar.Time
		case s.DailyBar != nil && s.DailyBar.Close > 0:
			price = s.DailyBar.Close
			markedAt = s.DailyBar.Time
		}
		if price <= 0 {
			continue
		}
		mk := &Mark{
			Symbol:        strings.ToUpper(sym),
			AssetClass:    inferAssetClass(sym),
			Price:         price,
			MarkedAt:      markedAt,
			TimestampKind: "exchange",
			Source:        alpacaMarketDataSlug,
			VolumeUnit:    "shares",
			Feed:          "auto",
		}
		if s.LatestTrade != nil {
			if s.LatestTrade.Price > 0 {
				mk.LastTradePrice = ptr(s.LatestTrade.Price)
			}
			if s.LatestTrade.Size > 0 {
				mk.LastTradeSize = ptr(s.LatestTrade.Size)
			}
		}
		if s.LatestQuote != nil {
			if s.LatestQuote.BidPrice > 0 {
				mk.BidPrice = ptr(s.LatestQuote.BidPrice)
			}
			if s.LatestQuote.AskPrice > 0 {
				mk.AskPrice = ptr(s.LatestQuote.AskPrice)
			}
			if s.LatestQuote.BidSize > 0 {
				mk.BidSize = ptr(s.LatestQuote.BidSize)
			}
			if s.LatestQuote.AskSize > 0 {
				mk.AskSize = ptr(s.LatestQuote.AskSize)
			}
			mk.QuoteAt = s.LatestQuote.Time
		}
		mk.Instrument = defaultInstrument(sym, mk.AssetClass, alpacaMarketDataSlug, time.Now().UTC())
		mk.Instrument.ProviderSymbol = strings.ToUpper(sym)
		mk.Instrument.Exchange = "ALPACA_US"
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

type alpacaBarsEnvelope struct {
	Bars          map[string][]alpacaWireBar `json:"bars"`
	NextPageToken string                     `json:"next_page_token"`
}

type alpacaWireBar struct {
	Time       string  `json:"t"`
	Open       float64 `json:"o"`
	High       float64 `json:"h"`
	Low        float64 `json:"l"`
	Close      float64 `json:"c"`
	Volume     float64 `json:"v"`
	TradeCount int64   `json:"n"`
	VWAP       float64 `json:"vw"`
}

func (a *alpacaMarketData) Bars(symbol, rng string) ([]Bar, error) {
	timeframe, start := alpacaRange(strings.ToUpper(strings.TrimSpace(rng)), time.Now().UTC())
	return a.BacktestBarsContext(context.Background(), symbol, timeframe, start, time.Now().UTC(), 10000)
}

func alpacaRange(rng string, now time.Time) (string, time.Time) {
	switch rng {
	case "5D":
		return "30m", now.AddDate(0, 0, -7)
	case "1M":
		return "4h", now.AddDate(0, -1, 0)
	case "3M":
		return "4h", now.AddDate(0, -3, 0)
	case "1Y":
		return "1d", now.AddDate(-1, 0, 0)
	case "ALL":
		return "1w", now.AddDate(-10, 0, 0)
	default:
		return "5m", now.AddDate(0, 0, -1)
	}
}

func alpacaTimeframe(interval string) (string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "5m":
		return "5Min", false, nil
	case "15m":
		return "15Min", false, nil
	case "30m":
		return "30Min", false, nil
	case "1h":
		return "1Hour", false, nil
	case "4h":
		return "1Hour", true, nil
	case "1d":
		return "1Day", false, nil
	case "1w":
		return "1Week", false, nil
	default:
		return "", false, fmt.Errorf("alpaca: unsupported interval %q", interval)
	}
}

func (a *alpacaMarketData) BacktestBarsContext(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]Bar, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	connID, ok := a.resolveConnection()
	if !ok {
		return nil, fmt.Errorf("alpaca-market-data not bound")
	}
	timeframe, aggregateFourHour, err := alpacaTimeframe(interval)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500000 {
		return nil, fmt.Errorf("alpaca: invalid bar limit %d", limit)
	}
	canonical := strings.ToUpper(strings.TrimSpace(symbol))
	pageToken := ""
	rawLimit := limit
	if aggregateFourHour {
		rawLimit = min(limit*4, 500000)
	}
	out := make([]Bar, 0, min(rawLimit, 10000))
	for len(out) < rawLimit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pageLimit := min(10000, rawLimit-len(out))
		args := map[string]any{
			"symbols": canonical, "timeframe": timeframe,
			"start": start.UTC().Format(time.RFC3339), "end": end.UTC().Format(time.RFC3339),
			"limit": pageLimit, "adjustment": "all", "sort": "asc",
		}
		if a.feed != "auto" {
			args["feed"] = a.feed
		}
		if pageToken != "" {
			args["page_token"] = pageToken
		}
		res, callErr := executeAlpacaToolWithRetry(ctx, a.platform, connID, "stock_bars", args)
		if callErr != nil {
			return nil, callErr
		}
		if res == nil || !res.Success {
			return nil, fmt.Errorf("stock_bars failed: %s", string(safeBytes(res)))
		}
		var envelope alpacaBarsEnvelope
		if err := json.Unmarshal(res.Data, &envelope); err != nil {
			return nil, fmt.Errorf("decode alpaca bars: %w", err)
		}
		for _, raw := range envelope.Bars[canonical] {
			at, err := time.Parse(time.RFC3339Nano, raw.Time)
			if err != nil {
				return nil, fmt.Errorf("alpaca bar timestamp: %w", err)
			}
			out = append(out, Bar{T: at.Unix(), O: raw.Open, H: raw.High, L: raw.Low, C: raw.Close, V: raw.Volume})
		}
		if envelope.NextPageToken == "" || envelope.NextPageToken == pageToken {
			break
		}
		pageToken = envelope.NextPageToken
	}
	normalized, err := normalizeBars(canonical, alpacaMarketDataSlug, out)
	if err != nil {
		return nil, err
	}
	if aggregateFourHour {
		normalized = aggregateYahooFourHourBars(normalized)
	}
	if len(normalized) > limit {
		normalized = normalized[len(normalized)-limit:]
	}
	return normalized, nil
}

func executeAlpacaToolWithRetry(ctx context.Context, platform sdk.PlatformClient, connID int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	var lastResult *sdk.ExecuteResult
	var lastErr error
	backoff := 150 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lastResult, lastErr = platform.ExecuteIntegrationTool(connID, tool, args)
		if lastErr == nil && lastResult != nil && (lastResult.Success || (lastResult.Status != 429 && lastResult.Status < 500)) {
			return lastResult, nil
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}
	return lastResult, lastErr
}

func safeBytes(res *sdk.ExecuteResult) []byte {
	if res == nil {
		return nil
	}
	return res.Data
}

// alpacaEquitySymbolsKnown — equity tickers we proactively fetch on
// each tick. The explicit mock provider and live bootstrap share this
// small seed, while real installs accumulate their own persisted set
// through quotes and watchlists.
func alpacaEquitySymbolsKnown() []string {
	out := make([]string, 0, 8)
	for _, s := range mockUniverse {
		if s.assetClass == "equity" || s.assetClass == "etf" {
			out = append(out, s.symbol)
		}
	}
	return out
}
