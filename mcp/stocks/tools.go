package main

// Tool implementations shared by the MCP handlers and the HTTP/REST
// mirror the panel uses. Every tool is a read: it serves from the TTL
// cache when fresh and otherwise fetches one /chart call from Yahoo,
// persisting the dividend history and refreshing the universe snapshot
// as a side effect.

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// toolSearch matches the local universe first; if nothing matches and the
// query looks like a single ticker, it tries Yahoo and auto-adds the
// symbol on success, so the universe grows as it's used.
func (a *App) toolSearch(args map[string]any) (any, error) {
	q := strArg(args, "query")
	if q == "" {
		return nil, errors.New("query required")
	}
	limit := intArg(args, "limit", 20)

	rows, err := a.st.searchUniverse(q, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 && looksLikeTicker(q) {
		if _, werr := a.refresh(q, false); werr == nil {
			rows, err = a.st.searchUniverse(q, limit)
			if err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{"results": rows, "count": len(rows)}, nil
}

// toolList warms any stale universe snapshots (bounded, concurrent) so
// price/change/yield are fresh, then returns the filtered + sorted list.
func (a *App) toolList(args map[string]any) (any, error) {
	stale, err := a.st.staleSymbols(a.ttl)
	if err != nil {
		return nil, err
	}
	a.warmMany(stale)

	sector := strArg(args, "sector")
	sortBy := orStr(strArg(args, "sort"), "name")
	limit := intArg(args, "limit", 100)
	var minYield *float64
	if f, ok := floatArg(args, "min_yield"); ok {
		minYield = &f
	}
	rows, err := a.st.listUniverse(sector, sortBy, minYield, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stocks": rows, "count": len(rows)}, nil
}

// toolGet returns one stock's full snapshot. TTL-cached as get:<symbol>.
func (a *App) toolGet(args map[string]any) (any, error) {
	sym := strings.ToUpper(strArg(args, "symbol"))
	if sym == "" {
		return nil, errors.New("symbol required")
	}
	if cached, ok := a.st.cacheGet("get:"+sym, a.ttl); ok {
		return cached, nil
	}

	res, err := a.refresh(sym, false)
	if err != nil {
		return nil, err
	}
	divs, _ := a.st.loadDividends(sym)
	yld := trailingYield(res.Dividends, res.Meta.Price)

	out := map[string]any{
		"symbol":              sym,
		"name":                res.Meta.Name,
		"exchange":            res.Meta.Exchange,
		"currency":            orStr(res.Meta.Currency, "USD"),
		"type":                res.Meta.InstrumentType,
		"price":               res.Meta.Price,
		"previous_close":      res.Meta.PreviousClose,
		"change":              res.Meta.Price - res.Meta.PreviousClose,
		"change_pct":          pctChange(res.Meta.Price, res.Meta.PreviousClose),
		"day_high":            res.Meta.DayHigh,
		"day_low":             res.Meta.DayLow,
		"fifty_two_week_high": res.Meta.FiftyTwoWeekHigh,
		"fifty_two_week_low":  res.Meta.FiftyTwoWeekLow,
		"volume":              res.Meta.Volume,
		"dividend_yield_pct":  yld,
		"dividend_frequency":  dividendFrequency(res.Dividends),
		"last_dividend":       lastDividend(divs),
	}
	_ = a.st.cacheSet("get:"+sym, out)
	return out, nil
}

// toolChart returns OHLCV bars for a range/interval. TTL-cached as
// chart:<symbol>:<range>:<interval>.
func (a *App) toolChart(args map[string]any) (any, error) {
	sym := strings.ToUpper(strArg(args, "symbol"))
	if sym == "" {
		return nil, errors.New("symbol required")
	}
	rng := normalizeRange(strArg(args, "range"))
	interval := normalizeInterval(strArg(args, "interval"))
	key := "chart:" + sym + ":" + rng + ":" + interval
	if cached, ok := a.st.cacheGet(key, a.ttl); ok {
		return cached, nil
	}

	res, err := a.y.fetchChart(sym, rng, interval, false)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"symbol":   sym,
		"range":    rng,
		"interval": interval,
		"currency": orStr(res.Meta.Currency, "USD"),
		"bars":     res.Bars,
	}
	_ = a.st.cacheSet(key, out)
	return out, nil
}

// toolDividends returns full payment history + a computed summary. The
// history is persisted; div:<symbol> is the freshness marker.
func (a *App) toolDividends(args map[string]any) (any, error) {
	sym := strings.ToUpper(strArg(args, "symbol"))
	if sym == "" {
		return nil, errors.New("symbol required")
	}

	if _, fresh := a.st.cacheGet("div:"+sym, a.ttl); !fresh {
		if _, err := a.refresh(sym, true); err != nil {
			// Tolerate the fetch failing if we already have history on file.
			if existing, _ := a.st.loadDividends(sym); len(existing) == 0 {
				return nil, err
			}
		}
		_ = a.st.cacheSet("div:"+sym, map[string]any{"ok": true})
	}

	history, err := a.st.loadDividends(sym)
	if err != nil {
		return nil, err
	}
	var price float64
	for _, r := range a.snapshotPrice(sym) {
		price = r
	}
	return map[string]any{
		"symbol":  sym,
		"summary": dividendSummary(history, price),
		"history": history,
	}, nil
}

// ─── Warming / refresh ─────────────────────────────────────────────

// refresh fetches one /chart for a symbol, persists dividends, refreshes
// the universe snapshot, and returns the parsed result. full=true pulls
// the entire dividend history (range=max); full=false pulls a 1y window
// (enough for price, day change, and trailing-12mo yield).
func (a *App) refresh(symbol string, full bool) (*chartResult, error) {
	sym := strings.ToUpper(symbol)
	rng, interval := "1y", "1d"
	if full {
		rng, interval = "max", "1mo"
	}
	res, err := a.y.fetchChart(sym, rng, interval, true)
	if err != nil {
		return nil, err
	}
	_ = a.st.ensureInstrument(sym, res.Meta.Name, res.Meta.Exchange, res.Meta.Currency)
	_ = a.st.saveDividends(sym, res.Dividends)

	yld := trailingYield(res.Dividends, res.Meta.Price)
	_ = a.st.updateSnapshot(sym, res.Meta.Price, pctChange(res.Meta.Price, res.Meta.PreviousClose), yld)
	return res, nil
}

// warmMany refreshes a set of symbols concurrently (sem-capped), ignoring
// per-symbol failures — a single dead ticker shouldn't fail a list call.
func (a *App) warmMany(symbols []string) {
	var wg sync.WaitGroup
	for _, sym := range symbols {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			a.y.sem <- struct{}{}
			defer func() { <-a.y.sem }()
			_, _ = a.refresh(s, false)
		}(sym)
	}
	wg.Wait()
}

// snapshotPrice returns the last-warmed price for a symbol (0..1 element)
// — a tiny helper so toolDividends can compute yield without another
// fetch.
func (a *App) snapshotPrice(sym string) []float64 {
	rows, err := a.st.searchUniverse(sym, 1)
	if err != nil || len(rows) == 0 || rows[0].Price == nil {
		return nil
	}
	return []float64{*rows[0].Price}
}

// ─── Pure computation ──────────────────────────────────────────────

func pctChange(price, prev float64) float64 {
	if prev == 0 {
		return 0
	}
	return (price - prev) / prev * 100
}

// trailingYield is the sum of dividends with an ex-date in the last 365
// days, as a percentage of current price. Returns nil when there are no
// recent dividends or no price (yield genuinely unknown).
func trailingYield(divs []Dividend, price float64) *float64 {
	if price <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(-1, 0, 0).Unix()
	var ttm float64
	for _, d := range divs {
		if d.ExDate >= cutoff {
			ttm += d.Amount
		}
	}
	if ttm == 0 {
		return nil
	}
	y := ttm / price * 100
	return &y
}

// dividendFrequency infers a cadence label from the count of payments in
// the trailing 12 months.
func dividendFrequency(divs []Dividend) string {
	cutoff := time.Now().AddDate(-1, 0, 0).Unix()
	n := 0
	for _, d := range divs {
		if d.ExDate >= cutoff {
			n++
		}
	}
	switch {
	case n == 0:
		return "none"
	case n >= 11:
		return "monthly"
	case n >= 3:
		return "quarterly"
	case n == 2:
		return "semi-annual"
	default:
		return "annual"
	}
}

func lastDividend(historyNewestFirst []Dividend) any {
	if len(historyNewestFirst) == 0 {
		return nil
	}
	d := historyNewestFirst[0]
	return map[string]any{"ex_date": d.ExDate, "amount": d.Amount}
}

// dividendSummary rolls up the payment history for the dividends tool.
func dividendSummary(historyNewestFirst []Dividend, price float64) map[string]any {
	cutoff := time.Now().AddDate(-1, 0, 0).Unix()
	prevCutoff := time.Now().AddDate(-2, 0, 0).Unix()
	var ttm, prevTTM float64
	for _, d := range historyNewestFirst {
		switch {
		case d.ExDate >= cutoff:
			ttm += d.Amount
		case d.ExDate >= prevCutoff:
			prevTTM += d.Amount
		}
	}
	out := map[string]any{
		"trailing_12mo": ttm,
		"frequency":     dividendFrequency(historyNewestFirst),
		"payments":      len(historyNewestFirst),
	}
	if price > 0 && ttm > 0 {
		out["yield_pct"] = ttm / price * 100
	}
	if prevTTM > 0 {
		out["growth_pct"] = (ttm - prevTTM) / prevTTM * 100
	}
	if len(historyNewestFirst) > 0 {
		out["latest"] = map[string]any{
			"ex_date": historyNewestFirst[0].ExDate,
			"amount":  historyNewestFirst[0].Amount,
		}
	}
	return out
}

// ─── Input normalization ───────────────────────────────────────────

func normalizeRange(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "1mo", "6mo", "1y", "5y", "max":
		return strings.ToLower(r)
	default:
		return "1y"
	}
}

func normalizeInterval(i string) string {
	switch strings.ToLower(strings.TrimSpace(i)) {
	case "1d", "1wk", "1mo":
		return strings.ToLower(i)
	default:
		return "1d"
	}
}

// looksLikeTicker is a cheap guard so search only spends a Yahoo call on
// inputs that could plausibly be a symbol (e.g. "NVDA", "BRK-B").
func looksLikeTicker(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 6 {
		return false
	}
	for _, r := range q {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
