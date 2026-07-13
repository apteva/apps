package main

// liveProvider — the production pricing path. Composes:
//
//   crypto      via binancePublic       (api.binance.com/api/v3)
//   polymarket  via polymarketPublic    (gamma-api.polymarket.com)
//   equity/etf  via Alpaca market data or Yahoo Finance
//
// Every Quote / Universe call goes through a per-symbol cache to
// keep the engine tick from hammering Binance or gamma-api. Provider
// failures are surfaced and never converted into synthetic executable marks.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	cacheTTL           = 30 * time.Second
	healthRecentWindow = 60 * time.Second
	staleAfter         = 90 * time.Second
)

type liveProvider struct {
	crypto *binancePublic
	poly   *polymarketPublic
	equity *alpacaMarketData // nil until SetPlatform is called from OnMount
	yahoo  *yahooPublic
	cache  *markCache
	health *providerHealth
}

func newLiveProvider() *liveProvider {
	return &liveProvider{
		crypto: newBinancePublic(),
		poly:   newPolymarketPublic(),
		yahoo:  newYahooPublic(),
		cache:  newMarkCache(cacheTTL),
		health: newProviderHealth(),
	}
}

// SetPlatform wires the alpaca-market-data path. Called from OnMount
// after globalCtx is set so the equity provider can dial the platform
// for the bound connection. Safe to call multiple times; subsequent
// calls swap the platform reference.
//
// Yahoo Finance has no platform dependency — it's set up in
// newLiveProvider directly. It runs as the equity fallback when no
// alpaca-market-data connection is bound (which is the default state
// on a fresh install).
func (p *liveProvider) SetPlatform(platform sdk.PlatformClient, logger sdk.Logger) {
	p.equity = newAlpacaMarketData(platform, logger)
}

// Quote — single-symbol fetch with cache and explicit provider errors.
func (p *liveProvider) Quote(symbol string) (*Mark, error) {
	if m := p.cache.get(symbol); m != nil {
		return m, nil
	}
	cls := inferAssetClass(symbol)
	switch cls {
	case "crypto":
		m, err := p.crypto.Quote(symbol)
		if err != nil {
			p.health.note("crypto", err)
			return nil, err
		}
		p.health.ok("crypto", "binance-public")
		p.cache.put(m)
		return m, nil
	case "polymarket":
		m, err := p.poly.Quote(symbol)
		if err != nil {
			p.health.note("polymarket", err)
			return nil, err
		}
		p.health.ok("polymarket", "polymarket-public")
		p.cache.put(m)
		return m, nil
	case "equity", "etf":
		// Equity routing: Alpaca > Yahoo. A provider outage leaves the
		// previous persisted mark untouched and blocks new symbol admission.
		// Alpaca wins when bound (paid SLA, fresher data, includes
		// pre/post-market). Otherwise Yahoo Finance — no auth, real
		// prices, works on first boot. If both providers fail, the quote
		// remains unavailable rather than being replaced with synthetic data.
		if p.equity != nil && p.equity.available() {
			m, err := p.equity.Quote(symbol)
			if err == nil {
				p.health.ok(cls, alpacaMarketDataSlug)
				p.cache.put(m)
				return m, nil
			}
			p.health.note(cls, err)
			// Fall through to Yahoo when Alpaca errors — usually a
			// transient network blip.
		}
		if m, err := p.yahoo.Quote(symbol); err == nil {
			p.health.ok(cls, "yahoo-finance")
			p.cache.put(m)
			return m, nil
		} else {
			p.health.note(cls, err)
		}
		return nil, fmt.Errorf("no live equity quote available for %s", symbol)
	default:
		return nil, fmt.Errorf("unsupported live asset class %q for %s", cls, symbol)
	}
}

// Universe — one batched HTTP call per asset class over the bootstrap
// symbols. Failed classes are omitted for this tick, leaving their last
// persisted marks untouched.
func (p *liveProvider) Universe() []*Mark {
	out := make([]*Mark, 0, 24)

	// Crypto — single batched ticker call.
	cryptoSyms := cryptoSymbolsKnown()
	if cMarks, err := p.crypto.UniverseBatch(cryptoSyms); err == nil && len(cMarks) > 0 {
		p.health.ok("crypto", "binance-public")
		for _, m := range cMarks {
			p.cache.put(m)
		}
		out = append(out, cMarks...)
	} else {
		if err != nil {
			p.health.note("crypto", err)
		}
	}

	// Polymarket — single batched markets call. Both error and an empty
	// result mark this class unavailable for the refresh.
	polySlugs := polymarketSymbolsKnown()
	pMarks, perr := p.poly.UniverseBatch(polySlugs)
	switch {
	case perr == nil && len(pMarks) > 0:
		p.health.ok("polymarket", "polymarket-public")
		for _, m := range pMarks {
			p.cache.put(m)
		}
		out = append(out, pMarks...)
	default:
		if perr != nil {
			p.health.note("polymarket", perr)
		} else {
			p.health.note("polymarket", fmt.Errorf("polymarket returned no active markets"))
		}
	}

	// Equity / ETF — Alpaca > Yahoo. Same dispatch order as
	// the per-symbol Quote path. Alpaca + Yahoo both take a list of
	// symbols and return marks; if either errors or returns fewer
	// symbols than asked, we fall down to the next tier.
	eqSyms := alpacaEquitySymbolsKnown()
	gotEquity := false
	if p.equity != nil && p.equity.available() {
		if eMarks, err := p.equity.UniverseBatch(eqSyms); err == nil && len(eMarks) > 0 {
			p.health.ok("equity", alpacaMarketDataSlug)
			p.health.ok("etf", alpacaMarketDataSlug)
			for _, m := range eMarks {
				p.cache.put(m)
			}
			out = append(out, eMarks...)
			gotEquity = true
		} else if err != nil {
			p.health.note("equity", err)
			p.health.note("etf", err)
		}
	}
	if !gotEquity {
		if eMarks, err := p.yahoo.UniverseBatch(eqSyms); err == nil && len(eMarks) > 0 {
			p.health.ok("equity", "yahoo-finance")
			p.health.ok("etf", "yahoo-finance")
			for _, m := range eMarks {
				p.cache.put(m)
			}
			out = append(out, eMarks...)
			gotEquity = true
		} else if err != nil {
			p.health.note("equity", err)
			p.health.note("etf", err)
		}
	}
	if !gotEquity {
		p.health.note("equity", fmt.Errorf("no live equity universe available"))
		p.health.note("etf", fmt.Errorf("no live ETF universe available"))
	}

	return out
}

// Bars routes history fetches by asset class. Crypto = Binance klines.
// Equity/etf = Yahoo Finance chart (no auth) — Alpaca stock_bars is a
// follow-up (needs the alpaca-market-data connection to be threaded
// through). Polymarket history remains unavailable until gamma
// prices-history is wired. Errors are returned so callers can show unavailable/stale state
// without presenting fabricated history.
func (p *liveProvider) Bars(symbol, rng string) ([]Bar, error) {
	cls := inferAssetClass(symbol)
	switch cls {
	case "crypto":
		bars, err := p.crypto.Bars(symbol, rng)
		if err != nil {
			p.health.note("crypto", err)
			return nil, err
		}
		p.health.ok("crypto", "binance-public")
		return bars, nil
	case "equity", "etf":
		bars, err := p.yahoo.Bars(symbol, rng)
		if err != nil || len(bars) == 0 {
			if err != nil {
				p.health.note(cls, err)
			}
			if err == nil {
				err = fmt.Errorf("empty Yahoo history for %s", symbol)
			}
			return nil, err
		}
		p.health.ok(cls, "yahoo-finance")
		return bars, nil
	default:
		return nil, fmt.Errorf("live history is not available for asset class %q", cls)
	}
}

func (p *liveProvider) StrategyBars(symbol, interval string, limit int) ([]Bar, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	if inferAssetClass(symbol) == "crypto" {
		interval = strings.ToLower(strings.TrimSpace(interval))
		dur, ok := strategyCadenceDuration(interval)
		if !ok {
			interval = "1h"
			dur = time.Hour
		}
		end := time.Now().UTC()
		start := end.Add(-dur * time.Duration(limit+2))
		bars, err := p.crypto.BacktestBars(symbol, interval, start, end, limit)
		if err != nil {
			p.health.note("crypto", err)
			return nil, err
		}
		p.health.ok("crypto", "binance-public")
		return bars, nil
	}
	return p.Bars(symbol, "3M")
}

// Health — read-only snapshot of per-class status.
func (p *liveProvider) Health() map[string]any { return p.health.snapshot() }

// polymarketSymbolsKnown is the small bootstrap set refreshed on first
// boot. Real installs accumulate additional persisted markets through
// explicit quote and watchlist requests.
func polymarketSymbolsKnown() []string {
	return []string{
		"POLY:btc-100k-2026",
		"POLY:fed-cut-march",
		"POLY:recession-2026",
	}
}

// ─── Cache ─────────────────────────────────────────────────────────

type cachedMark struct {
	mark *Mark
	at   time.Time
}

type markCache struct {
	mu   sync.RWMutex
	data map[string]cachedMark
	ttl  time.Duration
}

func newMarkCache(ttl time.Duration) *markCache {
	return &markCache{data: map[string]cachedMark{}, ttl: ttl}
}

func (c *markCache) get(symbol string) *Mark {
	c.mu.RLock()
	v, ok := c.data[symbol]
	c.mu.RUnlock()
	if !ok || time.Since(v.at) > c.ttl {
		return nil
	}
	return v.mark
}

func (c *markCache) put(m *Mark) {
	if m == nil {
		return
	}
	c.mu.Lock()
	c.data[m.Symbol] = cachedMark{mark: m, at: time.Now()}
	c.mu.Unlock()
}

// ─── Per-class health ─────────────────────────────────────────────

type classHealth struct {
	Name     string
	LastOKAt time.Time
	Errors   []time.Time // sliding 60s window of failure timestamps
}

type providerHealth struct {
	mu sync.RWMutex
	m  map[string]*classHealth
}

func newProviderHealth() *providerHealth {
	return &providerHealth{m: map[string]*classHealth{}}
}

func (h *providerHealth) ok(class, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.m[class]
	if c == nil {
		c = &classHealth{}
		h.m[class] = c
	}
	c.Name = name
	c.LastOKAt = time.Now()
}

func (h *providerHealth) note(class string, err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.m[class]
	if c == nil {
		c = &classHealth{}
		h.m[class] = c
	}
	now := time.Now()
	c.Errors = append(c.Errors, now)
	// Drop entries older than the window.
	cutoff := now.Add(-healthRecentWindow)
	keep := c.Errors[:0]
	for _, t := range c.Errors {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	c.Errors = keep
}

// snapshot returns a JSON-serialisable view used by /healthz/details
// + the market_source MCP tool.
func (h *providerHealth) snapshot() map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := map[string]any{}
	for class, c := range h.m {
		stale := false
		if !c.LastOKAt.IsZero() {
			stale = time.Since(c.LastOKAt) > staleAfter
		} else {
			stale = true
		}
		out[class] = map[string]any{
			"name":       c.Name,
			"last_ok_at": c.LastOKAt,
			"errors_60s": len(c.Errors),
			"stale":      stale,
		}
	}
	return out
}
