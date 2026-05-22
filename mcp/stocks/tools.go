package main

// Tool implementations shared by the MCP handlers and the HTTP/REST
// mirror the panel uses. Every tool is a read: it serves from the TTL
// cache when fresh and otherwise fetches one /chart call from Yahoo,
// persisting the dividend history and refreshing the universe snapshot
// as a side effect.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Warming worker pacing. Each symbol costs ~2 Yahoo calls (chart +
// fundamentals); a 100-symbol batch every ~10m keeps total traffic
// (~1200/hr) under Yahoo's unofficial ~2k/hr ceiling while cycling the
// full S&P 1500 in a couple of hours. warmGuard stops a global install's
// per-project worker dispatches from stacking batches.
const (
	warmBatchSize = 80               // ~160 Yahoo calls/batch — drains within the 10m tick at the rate limit
	warmGuard     = 9 * time.Minute  // keep consecutive batches from overlapping
	hotTTL        = 15 * time.Minute // recently-viewed symbols are re-warmed this often
	hotWindow     = 24 * time.Hour   // "recently viewed" = opened within this window
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
		if _, werr := a.refresh(q, false, false); werr == nil {
			rows, err = a.st.searchUniverse(q, limit)
			if err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{"results": rows, "count": len(rows)}, nil
}

// toolList returns the filtered + sorted universe from the warmed
// snapshot. It does NOT fetch from Yahoo — the background worker keeps
// the snapshot fresh (the universe is too large to warm on every call).
func (a *App) toolList(args map[string]any) (any, error) {
	f := listFilter{
		Sector: strArg(args, "sector"),
		Sort:   orStr(strArg(args, "sort"), "name"),
		Limit:  intArg(args, "limit", 0), // 0 = the whole universe
	}
	if v, ok := floatArg(args, "min_yield"); ok {
		f.MinYield = &v
	}
	if v, ok := floatArg(args, "max_yield"); ok {
		f.MaxYield = &v
	}
	if v, ok := floatArg(args, "min_payout"); ok {
		f.MinPayout = &v
	}
	if v, ok := floatArg(args, "max_payout"); ok {
		f.MaxPayout = &v
	}
	if v, ok := floatArg(args, "min_pe"); ok {
		f.MinPE = &v
	}
	if v, ok := floatArg(args, "max_pe"); ok {
		f.MaxPE = &v
	}
	if v, ok := floatArg(args, "min_growth"); ok {
		f.MinGrowth = &v
	}
	if v, ok := floatArg(args, "max_growth"); ok {
		f.MaxGrowth = &v
	}
	rows, err := a.st.listUniverse(f)
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
	_ = a.st.touch(sym) // mark hot so the warmer keeps it fresh in the list
	if cached, ok := a.st.cacheGet("get:"+sym, a.ttl); ok {
		return cached, nil
	}

	res, err := a.refresh(sym, false, false)
	if err != nil {
		return nil, err
	}
	divs, _ := a.st.loadDividends(sym)
	yld := trailingYield(res.Dividends, res.Meta.Price)
	growth := dividendCAGR5(res.Dividends)
	prevClose, changePct := dayChange(res)

	// Fundamentals come from the crumb-gated quoteSummary endpoint;
	// degrade gracefully (blank P/E + payout) when it's unavailable.
	var pePtr, payoutPtr *float64
	if pe, payout, ferr := a.y.fundamentals(sym, false); ferr == nil {
		pePtr, payoutPtr = pe, payout
		_ = a.st.updateFundamentals(sym, pe, payout)
	}

	out := map[string]any{
		"symbol":                 sym,
		"name":                   res.Meta.Name,
		"exchange":               res.Meta.Exchange,
		"currency":               orStr(res.Meta.Currency, "USD"),
		"type":                   res.Meta.InstrumentType,
		"price":                  res.Meta.Price,
		"previous_close":         prevClose,
		"change":                 res.Meta.Price - prevClose,
		"change_pct":             changePct,
		"day_high":               res.Meta.DayHigh,
		"day_low":                res.Meta.DayLow,
		"fifty_two_week_high":    res.Meta.FiftyTwoWeekHigh,
		"fifty_two_week_low":     res.Meta.FiftyTwoWeekLow,
		"volume":                 res.Meta.Volume,
		"pe":                     pePtr,
		"payout_pct":             payoutPtr,
		"dividend_yield_pct":     yld,
		"dividend_growth_5y_pct": growth,
		"dividend_frequency":     dividendFrequency(res.Dividends),
		"last_dividend":          lastDividend(divs),
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

	res, err := a.y.fetchChart(sym, rng, interval, false, false)
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
	_ = a.st.touch(sym)

	if _, fresh := a.st.cacheGet("div:"+sym, a.ttl); !fresh {
		// Recent payments + an accurate price snapshot come from the 1y/1d
		// path; the deep history comes from range=max. Run both so the
		// summary is correct even when dividends is called in isolation
		// (no preceding list/get warm). Tolerate a failure if we already
		// have history on file.
		_, _ = a.refresh(sym, false, false)
		if _, err := a.refresh(sym, true, false); err != nil {
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

// toolSyncStatus reports background-warming progress + the fundamentals
// feed health, for the panel's sync strip and agent introspection.
func (a *App) toolSyncStatus(_ map[string]any) (any, error) {
	st, err := a.st.stats(a.ttl)
	if err != nil {
		return nil, err
	}
	fState, retryAt := a.y.fundamentalsState()
	a.warmMu.Lock()
	last := a.lastWarm
	a.warmMu.Unlock()

	out := map[string]any{
		"universe":              st.Universe,
		"warmed":                st.Warmed,
		"with_price":            st.WithPrice,
		"with_yield":            st.WithYield,
		"with_fundamentals":     st.WithFundamentals,
		"fresh":                 st.Fresh,
		"oldest_refresh":        st.OldestRefresh,
		"newest_refresh":        st.NewestRefresh,
		"fundamentals_state":    fState,
		"fundamentals_retry_at": retryAt,
		"batch_size":            warmBatchSize,
		"interval_seconds":      600,
	}
	if !last.IsZero() {
		u := last.Unix()
		out["last_batch"] = u
	}
	return out, nil
}

// ─── Watchlists ────────────────────────────────────────────────────

var errProjectRequired = errors.New("project_id required (pass _project_id on global installs)")

// watchlistRules is the saved filter blob (JSON) behind a dynamic
// watchlist. No constraints = a pure-manual list.
type watchlistRules struct {
	Sector    string   `json:"sector,omitempty"`
	MinYield  *float64 `json:"min_yield,omitempty"`
	MaxYield  *float64 `json:"max_yield,omitempty"`
	MinPayout *float64 `json:"min_payout,omitempty"`
	MaxPayout *float64 `json:"max_payout,omitempty"`
	MinPE     *float64 `json:"min_pe,omitempty"`
	MaxPE     *float64 `json:"max_pe,omitempty"`
	MinGrowth *float64 `json:"min_growth,omitempty"`
	MaxGrowth *float64 `json:"max_growth,omitempty"`
	Sort      string   `json:"sort,omitempty"`
}

func parseRules(raw string) watchlistRules {
	var r watchlistRules
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &r)
	}
	return r
}

func (r watchlistRules) hasConstraints() bool {
	return r.Sector != "" || r.MinYield != nil || r.MaxYield != nil ||
		r.MinPayout != nil || r.MaxPayout != nil || r.MinPE != nil || r.MaxPE != nil ||
		r.MinGrowth != nil || r.MaxGrowth != nil
}

func (r watchlistRules) toFilter() listFilter {
	return listFilter{
		Sector: r.Sector,
		MinYield: r.MinYield, MaxYield: r.MaxYield,
		MinPayout: r.MinPayout, MaxPayout: r.MaxPayout,
		MinPE: r.MinPE, MaxPE: r.MaxPE,
		MinGrowth: r.MinGrowth, MaxGrowth: r.MaxGrowth,
	}
}

// resolveProject picks the project for a watchlist op: the dispatch's
// project (env on project-scoped installs, _project_id on global) first,
// then an explicit project_id arg.
func resolveProject(ctx *sdk.AppCtx, args map[string]any) string {
	if ctx != nil {
		if p := ctx.CurrentProject(); p != "" {
			return p
		}
	}
	if v := strArg(args, "_project_id"); v != "" {
		return v
	}
	return strArg(args, "project_id")
}

// resolveWatchlist computes a list's members: (rule matches ∪ includes) −
// excludes. Include-pinned rows are flagged so the UI can tell pins from
// rule matches.
func (a *App) resolveWatchlist(w *watchlistRow, sort string) ([]instrumentRow, error) {
	rules := parseRules(w.Rules)
	set := map[string]struct{}{}
	if rules.hasConstraints() {
		f := rules.toFilter()
		rows, err := a.st.listUniverse(f)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			set[r.Symbol] = struct{}{}
		}
	}
	inc, exc, err := a.st.loadPins(w.ID)
	if err != nil {
		return nil, err
	}
	incSet := map[string]struct{}{}
	for _, sym := range inc {
		set[sym] = struct{}{}
		incSet[sym] = struct{}{}
	}
	for _, sym := range exc {
		delete(set, sym)
	}
	syms := make([]string, 0, len(set))
	for sym := range set {
		syms = append(syms, sym)
	}
	srt := sort
	if srt == "" {
		srt = rules.Sort
	}
	rows, err := a.st.instrumentsBySymbols(syms, srt)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if _, ok := incSet[rows[i].Symbol]; ok {
			rows[i].Pinned = true
		}
	}
	return rows, nil
}

func (a *App) toolWatchlistsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := resolveProject(ctx, args)
	if pid == "" {
		return nil, errProjectRequired
	}
	lists, err := a.st.watchlistList(pid)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(lists))
	for _, w := range lists {
		kind := "manual"
		if parseRules(w.Rules).hasConstraints() {
			kind = "dynamic"
		}
		out = append(out, map[string]any{
			"id": w.ID, "name": w.Name, "kind": kind,
			"rules": json.RawMessage(orStr(w.Rules, "{}")),
		})
	}
	return map[string]any{"watchlists": out}, nil
}

func (a *App) toolWatchlistSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := resolveProject(ctx, args)
	if pid == "" {
		return nil, errProjectRequired
	}
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	rules := "{}"
	if r, ok := args["rules"]; ok && r != nil {
		if b, err := json.Marshal(r); err == nil {
			rules = string(b)
		}
	}
	if id := intArg(args, "id", 0); id > 0 {
		w, err := a.st.watchlistByID(pid, int64(id))
		if err != nil {
			return nil, err
		}
		if w == nil {
			return nil, errors.New("watchlist not found")
		}
		if err := a.st.watchlistUpdate(pid, int64(id), name, rules); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "ok": true}, nil
	}
	id, err := a.st.watchlistCreate(pid, name, rules)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "ok": true}, nil
}

func (a *App) toolWatchlistDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := resolveProject(ctx, args)
	if pid == "" {
		return nil, errProjectRequired
	}
	id := intArg(args, "id", 0)
	if id <= 0 {
		return nil, errors.New("id required")
	}
	if err := a.st.watchlistDelete(pid, int64(id)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// toolWatchlistMember sets a symbol's membership in a list. state:
// "include" (force in), "exclude" (force out), "auto" (clear pin → back to
// rule-driven).
func (a *App) toolWatchlistMember(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := resolveProject(ctx, args)
	if pid == "" {
		return nil, errProjectRequired
	}
	id := intArg(args, "id", 0)
	sym := strings.ToUpper(strArg(args, "symbol"))
	state := strArg(args, "state")
	if id <= 0 || sym == "" {
		return nil, errors.New("id and symbol required")
	}
	w, err := a.st.watchlistByID(pid, int64(id))
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errors.New("watchlist not found")
	}
	switch state {
	case "include", "exclude":
		err = a.st.setPin(int64(id), sym, state)
	case "auto", "":
		err = a.st.removePin(int64(id), sym)
	default:
		return nil, errors.New("state must be include, exclude, or auto")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolWatchlistGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := resolveProject(ctx, args)
	if pid == "" {
		return nil, errProjectRequired
	}
	id := intArg(args, "id", 0)
	if id <= 0 {
		return nil, errors.New("id required")
	}
	w, err := a.st.watchlistByID(pid, int64(id))
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errors.New("watchlist not found")
	}
	rows, err := a.resolveWatchlist(w, strArg(args, "sort"))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"watchlist": map[string]any{"id": w.ID, "name": w.Name, "rules": json.RawMessage(orStr(w.Rules, "{}"))},
		"stocks":    rows,
		"count":     len(rows),
	}, nil
}

// ─── Warming / refresh ─────────────────────────────────────────────

// refresh fetches one /chart for a symbol, persists its dividends, and
// returns the parsed result. full=false pulls a 10y/1d window — the
// authoritative source for the universe snapshot (price, day change,
// trailing-12mo yield, and the 5-year dividend CAGR). 10y (not 5y) so the
// CAGR's baseline window — dividends paid 5–6 years ago — actually falls
// inside the fetched range. full=true pulls the deep dividend history
// (range=max) for the detail history table only: that monthly series can
// omit the most recent payments and isn't a reliable price source, so it
// never touches the snapshot. saveDividends is an idempotent upsert, so
// the DB ends up holding the union.
func (a *App) refresh(symbol string, full, bg bool) (*chartResult, error) {
	sym := strings.ToUpper(symbol)
	rng, interval := "10y", "1d"
	if full {
		rng, interval = "max", "1mo"
	}
	res, err := a.y.fetchChart(sym, rng, interval, true, bg)
	if err != nil {
		return nil, err
	}
	_ = a.st.ensureInstrument(sym, res.Meta.Name, res.Meta.Exchange, res.Meta.Currency)
	_ = a.st.saveDividends(sym, res.Dividends)

	if !full && plausiblePrice(res) {
		_, changePct := dayChange(res)
		_ = a.st.updateSnapshot(sym, res.Meta.Price, changePct,
			trailingYield(res.Dividends, res.Meta.Price), dividendCAGR5(res.Dividends))
	}
	return res, nil
}

// warmOne refreshes a single symbol's price/yield/growth snapshot and its
// P/E + payout fundamentals. Best-effort — partial failures are fine.
func (a *App) warmOne(sym string) {
	if _, err := a.refresh(sym, false, true); err != nil {
		return
	}
	if pe, payout, err := a.y.fundamentals(sym, true); err == nil {
		_ = a.st.updateFundamentals(sym, pe, payout)
	}
}

// warmBatch refreshes one paced batch of the stalest symbols, sem-capped.
// Guarded by lastWarm so concurrent dispatches (a global install's
// per-project worker ticks, or boot + first tick overlapping) don't
// multiply Yahoo traffic.
func (a *App) warmBatch(ctx context.Context) {
	a.warmMu.Lock()
	if time.Since(a.lastWarm) < warmGuard {
		a.warmMu.Unlock()
		return
	}
	a.lastWarm = time.Now()
	a.warmMu.Unlock()

	syms, err := a.st.warmCandidates(warmBatchSize, a.ttl, hotTTL, hotWindow)
	if err != nil || len(syms) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, sym := range syms {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			a.y.sem <- struct{}{}
			defer func() { <-a.y.sem }()
			a.warmOne(s)
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
	// No real equity/ETF yield approaches this; a value above the guard
	// means the price input was a transient bad read — report unknown
	// rather than poisoning the snapshot with nonsense.
	if y > 60 {
		return nil
	}
	return &y
}

// dayChange returns the prior-day close and the day-over-day % change.
// Yahoo only populates meta.previousClose on intraday ranges, so on the
// 1y/1d series it's 0 — fall back to the second-to-last daily bar, which
// is the genuine prior close (NOT chartPreviousClose, which is ≈1y ago
// and would turn this into a 1-year return).
func dayChange(res *chartResult) (prevClose float64, changePct float64) {
	prev := res.Meta.PreviousClose
	if prev <= 0 && len(res.Bars) >= 2 {
		prev = res.Bars[len(res.Bars)-2].C
	}
	if prev <= 0 {
		return 0, 0
	}
	return prev, pctChange(res.Meta.Price, prev)
}

// plausiblePrice rejects the transient near-zero / wildly-off quote Yahoo
// occasionally returns under a concurrent burst, so a bad read can't
// poison the universe snapshot. A real quote sits within a sane band of
// the latest bar close.
func plausiblePrice(res *chartResult) bool {
	p := res.Meta.Price
	if p <= 0 {
		return false
	}
	n := len(res.Bars)
	if n == 0 {
		return true
	}
	last := res.Bars[n-1].C
	if last <= 0 {
		return true
	}
	r := p / last
	return r > 0.5 && r < 2.0
}

// dividendCAGR5 is the 5-year compound annual growth rate of the
// trailing-12mo dividend per share, computed from the payment history.
// nil when there isn't enough history (no payments ~5 years ago) — so it
// sorts/filters as "unknown" rather than a misleading 0.
func dividendCAGR5(divs []Dividend) *float64 {
	windowSum := func(yearsAgo int) float64 {
		hi := time.Now().AddDate(-yearsAgo, 0, 0).Unix()
		lo := time.Now().AddDate(-yearsAgo-1, 0, 0).Unix()
		var s float64
		for _, d := range divs {
			if d.ExDate < hi && d.ExDate >= lo {
				s += d.Amount
			}
		}
		return s
	}
	ttm, base := windowSum(0), windowSum(5)
	if ttm <= 0 || base <= 0 {
		return nil
	}
	g := (math.Pow(ttm/base, 1.0/5) - 1) * 100
	return &g
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
// Growth is computed from the per-share total in a trailing-12mo window
// ending N years ago: growth_pct is year-over-year, cagr_5y_pct is the
// 5-year compound annual growth rate.
func dividendSummary(historyNewestFirst []Dividend, price float64) map[string]any {
	// windowSum totals the dividends paid in the 12-month window ending
	// yearsAgo years before now (yearsAgo=0 → trailing 12 months).
	windowSum := func(yearsAgo int) float64 {
		hi := time.Now().AddDate(-yearsAgo, 0, 0).Unix()
		lo := time.Now().AddDate(-yearsAgo-1, 0, 0).Unix()
		var s float64
		for _, d := range historyNewestFirst {
			if d.ExDate < hi && d.ExDate >= lo {
				s += d.Amount
			}
		}
		return s
	}
	ttm := windowSum(0)
	prevTTM := windowSum(1)

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
	if g := dividendCAGR5(historyNewestFirst); g != nil {
		out["cagr_5y_pct"] = *g
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
