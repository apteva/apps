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
	"context"
	"errors"
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
	polyDiscoveryTTL   = 30 * time.Second
	polyMissingTTL     = 15 * time.Minute
	polyMaxBackoff     = 15 * time.Minute
)

type liveProvider struct {
	crypto           *binancePublic
	poly             *polymarketPublic
	equity           *alpacaMarketData // nil until SetPlatform is called from OnMount
	yahoo            *yahooPublic
	cache            *markCache
	health           *providerHealth
	now              func() time.Time
	polyMu           sync.Mutex
	polyInFlight     bool
	polyFailures     int
	polyRetryAt      time.Time
	polyDiscoveredAt time.Time
	polyDiscovered   []*Mark
	polyMissing      map[string]time.Time
}

func newLiveProvider() *liveProvider {
	return &liveProvider{
		crypto:      newBinancePublic(),
		poly:        newPolymarketPublic(),
		yahoo:       newYahooPublic(),
		cache:       newMarkCache(cacheTTL),
		health:      newProviderHealth(),
		now:         time.Now,
		polyMissing: map[string]time.Time{},
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
func (p *liveProvider) SetPlatform(platform sdk.PlatformClient, logger sdk.Logger, feed string) {
	p.equity = newAlpacaMarketData(platform, logger, feed)
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
		m, err = p.normalizeProviderMark("crypto", "binance-public", m)
		if err != nil {
			return nil, err
		}
		p.health.ok("crypto", "binance-public")
		p.cache.put(m)
		return m, nil
	case "polymarket":
		if p.polyKnownMissing(symbol) {
			return nil, &polymarketMarketNotFoundError{slug: stripPolyPrefix(symbol)}
		}
		if retryAt, ok := p.beginPolyAttempt(); !ok {
			return nil, fmt.Errorf("polymarket refresh deferred until %s", retryAt.UTC().Format(time.RFC3339))
		}
		m, err := p.poly.Quote(symbol)
		if err != nil {
			var notFound *polymarketMarketNotFoundError
			if errors.As(err, &notFound) {
				p.endPolyNeutral()
				p.rememberPolyMissing(symbol)
				return nil, err
			}
			p.endPolyFailure(err)
			return nil, err
		}
		m, err = p.normalizeProviderMark("polymarket", "polymarket-public", m)
		if err != nil {
			p.endPolyFailure(err)
			return nil, err
		}
		p.endPolySuccess()
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
				if m, err = p.normalizeProviderMark(cls, alpacaMarketDataSlug, m); err == nil {
					p.health.ok(cls, alpacaMarketDataSlug)
					p.cache.put(m)
					return m, nil
				}
			}
			p.health.note(cls, err)
			// Fall through to Yahoo when Alpaca errors — usually a
			// transient network blip.
		}
		if m, err := p.yahoo.Quote(symbol); err == nil {
			if m, err = p.normalizeProviderMark(cls, "yahoo-finance", m); err == nil {
				p.health.ok(m.AssetClass, "yahoo-finance")
				p.cache.put(m)
				return m, nil
			}
			p.health.note(cls, err)
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
	if cMarks, err := p.crypto.UniverseBatch(cryptoSyms); err == nil {
		cMarks, err = p.normalizeProviderMarks("crypto", "binance-public", cMarks)
		if err != nil {
			p.health.note("crypto", err)
		}
		if len(cMarks) > 0 {
			p.health.ok("crypto", "binance-public")
			for _, m := range cMarks {
				p.cache.put(m)
			}
			out = append(out, cMarks...)
		}
	} else {
		if err != nil {
			p.health.note("crypto", err)
		}
	}

	// Polymarket — single batched markets call. Both error and an empty
	// result mark this class unavailable for the refresh.
	pMarks, attempted, perr := p.discoverPolymarket()
	switch {
	case perr == nil && len(pMarks) > 0:
		pMarks, perr = p.normalizeProviderMarks("polymarket", "polymarket-public", pMarks)
		if perr != nil {
			p.health.note("polymarket", perr)
		}
		for _, m := range pMarks {
			p.cache.put(m)
		}
		out = append(out, pMarks...)
	case attempted:
		if perr != nil {
			// discoverPolymarket recorded the provider failure and backoff.
		} else {
			p.endPolyFailure(fmt.Errorf("polymarket returned no active markets"))
		}
	}

	// Equity / ETF — Alpaca > Yahoo. Same dispatch order as
	// the per-symbol Quote path. Alpaca + Yahoo both take a list of
	// symbols and return marks; if either errors or returns fewer
	// symbols than asked, we fall down to the next tier.
	eqSyms := alpacaEquitySymbolsKnown()
	gotEquity := false
	if p.equity != nil && p.equity.available() {
		if eMarks, err := p.equity.UniverseBatch(eqSyms); err == nil {
			eMarks, err = p.normalizeProviderMarks("equity", alpacaMarketDataSlug, eMarks)
			if err != nil {
				p.health.note("equity", err)
				p.health.note("etf", err)
			}
			if len(eMarks) > 0 {
				p.health.ok("equity", alpacaMarketDataSlug)
				p.health.ok("etf", alpacaMarketDataSlug)
				for _, m := range eMarks {
					p.cache.put(m)
				}
				out = append(out, eMarks...)
				gotEquity = true
			}
		} else if err != nil {
			p.health.note("equity", err)
			p.health.note("etf", err)
		}
	}
	if !gotEquity {
		if eMarks, err := p.yahoo.UniverseBatch(eqSyms); err == nil {
			eMarks, err = p.normalizeProviderMarks("equity", "yahoo-finance", eMarks)
			if err != nil {
				p.health.note("equity", err)
				p.health.note("etf", err)
			}
			if len(eMarks) > 0 {
				p.health.ok("equity", "yahoo-finance")
				p.health.ok("etf", "yahoo-finance")
				for _, m := range eMarks {
					p.cache.put(m)
				}
				out = append(out, eMarks...)
				gotEquity = true
			}
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
		bars, err = normalizeBars(symbol, "binance-public", bars)
		if err != nil {
			p.health.note("crypto", err)
			return nil, err
		}
		p.health.ok("crypto", "binance-public")
		return bars, nil
	case "equity", "etf":
		if p.equity != nil && p.equity.available() {
			bars, err := p.equity.Bars(symbol, rng)
			if err == nil && len(bars) > 0 {
				p.health.ok(cls, alpacaMarketDataSlug)
				return bars, nil
			}
			p.health.note(cls, err)
		}
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
		bars, err = normalizeBars(symbol, "yahoo-finance", bars)
		if err != nil {
			p.health.note(cls, err)
			return nil, err
		}
		p.health.ok(cls, "yahoo-finance")
		return bars, nil
	default:
		return nil, fmt.Errorf("live history is not available for asset class %q", cls)
	}
}

func (p *liveProvider) normalizeProviderMark(class, source string, mark *Mark) (*Mark, error) {
	now := time.Now().UTC()
	if p.now != nil {
		now = p.now().UTC()
	}
	normalized, err := normalizeMark(source, mark, now)
	if err != nil {
		p.health.note(class, fmt.Errorf("%s quality check: %w", source, err))
		return nil, err
	}
	return normalized, nil
}

func (p *liveProvider) normalizeProviderMarks(class, source string, marks []*Mark) ([]*Mark, error) {
	out := make([]*Mark, 0, len(marks))
	var lastErr error
	for _, mark := range marks {
		normalized, err := p.normalizeProviderMark(class, source, mark)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, normalized)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func (p *liveProvider) StrategyBars(symbol, interval string, limit int) ([]Bar, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	class := inferAssetClass(symbol)
	if class == "crypto" {
		interval = strings.ToLower(strings.TrimSpace(interval))
		dur, ok := strategyCadenceDuration(interval)
		if !ok {
			interval = "1h"
			dur = time.Hour
		}
		now := time.Now
		if p.now != nil {
			now = p.now
		}
		end := strategyClosedCandleBoundary(now().UTC(), interval)
		start := end.Add(-dur * time.Duration(limit))
		// Binance treats endTime as inclusive. Stop one millisecond before
		// the boundary so the currently forming candle can never be returned.
		end = end.Add(-time.Millisecond)
		bars, err := p.crypto.BacktestBars(symbol, interval, start, end, limit)
		if err != nil {
			p.health.note("crypto", err)
			return nil, err
		}
		bars, err = normalizeBars(symbol, "binance-public", bars)
		if err != nil {
			p.health.note("crypto", err)
			return nil, err
		}
		p.health.ok("crypto", "binance-public")
		return bars, nil
	}
	if class == "equity" || class == "etf" {
		interval = strings.ToLower(strings.TrimSpace(interval))
		dur, ok := strategyCadenceDuration(interval)
		if !ok {
			interval = "1d"
			dur = 24 * time.Hour
		}
		now := time.Now
		if p.now != nil {
			now = p.now
		}
		end := now().UTC()
		lookbackFactor := time.Duration(6)
		if interval == "1d" || interval == "1w" {
			lookbackFactor = 2
		}
		start := end.Add(-dur * time.Duration(limit) * lookbackFactor)
		if p.equity != nil && p.equity.available() {
			bars, err := p.equity.BacktestBarsContext(context.Background(), symbol, interval, start, end, limit)
			if err != nil {
				p.health.note(class, err)
				return nil, err
			}
			bars = completedYahooStrategyBars(bars, interval, end)
			if len(bars) < limit {
				err := fmt.Errorf("Alpaca returned %d completed %s bars for %s; need %d", len(bars), interval, symbol, limit)
				p.health.note(class, err)
				return nil, err
			}
			p.health.ok(class, alpacaMarketDataSlug)
			return bars[len(bars)-limit:], nil
		}
		bars, err := p.yahoo.BacktestBars(symbol, interval, start, end, 1000)
		if err != nil {
			p.health.note(class, err)
			return nil, err
		}
		bars, err = normalizeBars(symbol, "yahoo-finance", bars)
		if err != nil {
			p.health.note(class, err)
			return nil, err
		}
		bars = completedYahooStrategyBars(bars, interval, end)
		if len(bars) < limit {
			err := fmt.Errorf("Yahoo returned %d completed %s bars for %s; need %d", len(bars), interval, symbol, limit)
			p.health.note(class, err)
			return nil, err
		}
		p.health.ok(class, "yahoo-finance")
		return bars[len(bars)-limit:], nil
	}
	return p.Bars(symbol, "3M")
}

func completedYahooStrategyBars(bars []Bar, interval string, now time.Time) []Bar {
	now = now.UTC()
	out := make([]Bar, 0, len(bars))
	if interval == "1d" {
		location, err := time.LoadLocation("America/New_York")
		if err != nil {
			location = time.UTC
		}
		nowDate := now.In(location).Format("2006-01-02")
		for _, bar := range bars {
			if time.Unix(bar.T, 0).In(location).Format("2006-01-02") < nowDate {
				out = append(out, bar)
			}
		}
		return out
	}
	dur, ok := strategyCadenceDuration(interval)
	if !ok {
		dur = 24 * time.Hour
	}
	for _, bar := range bars {
		if !time.Unix(bar.T, 0).UTC().Add(dur).After(now) {
			out = append(out, bar)
		}
	}
	return out
}

func strategyClosedCandleBoundary(now time.Time, interval string) time.Time {
	now = now.UTC()
	if strings.EqualFold(strings.TrimSpace(interval), "1w") {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		daysSinceMonday := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -daysSinceMonday)
	}
	dur, ok := strategyCadenceDuration(strings.ToLower(strings.TrimSpace(interval)))
	if !ok || dur <= 0 {
		dur = time.Hour
	}
	return now.Truncate(dur)
}

// Health — read-only snapshot of per-class status.
func (p *liveProvider) Health() map[string]any { return p.health.snapshot() }

func (p *liveProvider) discoverPolymarket() ([]*Mark, bool, error) {
	now := p.now()
	p.polyMu.Lock()
	if len(p.polyDiscovered) > 0 && now.Sub(p.polyDiscoveredAt) < polyDiscoveryTTL {
		marks := append([]*Mark(nil), p.polyDiscovered...)
		p.polyMu.Unlock()
		return marks, false, nil
	}
	if p.polyInFlight || now.Before(p.polyRetryAt) {
		p.polyMu.Unlock()
		return nil, false, nil
	}
	p.polyInFlight = true
	p.polyMu.Unlock()

	marks, err := p.poly.ActiveMarkets(25)
	if err != nil {
		p.endPolyFailure(err)
		return nil, true, err
	}
	if len(marks) == 0 {
		return nil, true, nil
	}
	p.polyMu.Lock()
	p.polyInFlight = false
	p.polyFailures = 0
	p.polyRetryAt = time.Time{}
	p.polyDiscoveredAt = now
	p.polyDiscovered = append([]*Mark(nil), marks...)
	p.polyMu.Unlock()
	p.health.ok("polymarket", "polymarket-public")
	return marks, true, nil
}

func (p *liveProvider) beginPolyAttempt() (time.Time, bool) {
	now := p.now()
	p.polyMu.Lock()
	defer p.polyMu.Unlock()
	if p.polyInFlight {
		return now.Add(time.Second), false
	}
	if now.Before(p.polyRetryAt) {
		return p.polyRetryAt, false
	}
	p.polyInFlight = true
	return time.Time{}, true
}

func (p *liveProvider) endPolyNeutral() {
	p.polyMu.Lock()
	p.polyInFlight = false
	p.polyMu.Unlock()
}

func (p *liveProvider) endPolySuccess() {
	p.polyMu.Lock()
	p.polyInFlight = false
	p.polyFailures = 0
	p.polyRetryAt = time.Time{}
	p.polyMu.Unlock()
	p.health.ok("polymarket", "polymarket-public")
}

func (p *liveProvider) endPolyFailure(err error) {
	p.polyMu.Lock()
	p.polyInFlight = false
	p.polyFailures++
	exponent := p.polyFailures - 1
	if exponent > 5 {
		exponent = 5
	}
	delay := 30 * time.Second * time.Duration(1<<exponent)
	if delay > polyMaxBackoff {
		delay = polyMaxBackoff
	}
	p.polyRetryAt = p.now().Add(delay)
	retryAt := p.polyRetryAt
	p.polyMu.Unlock()
	p.health.noteRetry("polymarket", err, retryAt)
}

func (p *liveProvider) polyKnownMissing(symbol string) bool {
	key := strings.ToUpper(symbol)
	p.polyMu.Lock()
	defer p.polyMu.Unlock()
	until := p.polyMissing[key]
	if until.IsZero() || !p.now().Before(until) {
		delete(p.polyMissing, key)
		return false
	}
	return true
}

func (p *liveProvider) rememberPolyMissing(symbol string) {
	p.polyMu.Lock()
	p.polyMissing[strings.ToUpper(symbol)] = p.now().Add(polyMissingTTL)
	p.polyMu.Unlock()
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
	Name        string
	LastOKAt    time.Time
	Errors      []time.Time // sliding 60s window of failure timestamps
	LastError   string
	LastErrorAt time.Time
	RetryAt     time.Time
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
	c.RetryAt = time.Time{}
}

func (h *providerHealth) note(class string, err error) {
	h.noteRetry(class, err, time.Time{})
}

func (h *providerHealth) noteRetry(class string, err error, retryAt time.Time) {
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
	c.LastError = err.Error()
	c.LastErrorAt = now
	c.RetryAt = retryAt
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
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]any{}
	now := time.Now()
	for class, c := range h.m {
		cutoff := now.Add(-healthRecentWindow)
		keep := c.Errors[:0]
		for _, recordedAt := range c.Errors {
			if recordedAt.After(cutoff) {
				keep = append(keep, recordedAt)
			}
		}
		c.Errors = keep
		stale := false
		if !c.LastOKAt.IsZero() {
			stale = time.Since(c.LastOKAt) > staleAfter
		} else {
			stale = true
		}
		out[class] = map[string]any{
			"name":          c.Name,
			"last_ok_at":    c.LastOKAt,
			"errors_60s":    len(c.Errors),
			"stale":         stale,
			"last_error":    c.LastError,
			"last_error_at": c.LastErrorAt,
			"retry_at":      c.RetryAt,
		}
	}
	return out
}
