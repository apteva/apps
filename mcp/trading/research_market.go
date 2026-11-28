package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	defaultBacktestMaxMarketRows = 500_000
	backtestHistoryFetchTimeout  = 5 * time.Minute
	backtestHistoryConcurrency   = 4
)

var ()

func (a *App) toolBacktestMarketStep(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if sdk.CurrentEnvironmentID() == "" {
		return nil, errors.New("replay mutation requires isolated environment")
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID := int64Arg(args, "portfolio_id", 0)
	runID := int64Arg(args, "run_id", 0)
	step := intArg(args, "step", 0)
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID)
	if err != nil || pf.Mode == "live" || !portfolioUsesBacktestPricing(ctx.AppDB(), portfolioID) {
		return nil, errors.New("isolated replay portfolio required")
	}
	at, err := time.Parse(time.RFC3339Nano, strArg(args, "replay_at"))
	if err != nil || step <= 0 || runID <= 0 {
		return nil, errors.New("run_id, positive step and valid replay_at required")
	}
	raw, ok := args["prices"].([]any)
	if !ok || len(raw) == 0 {
		return nil, errors.New("replay prices required")
	}
	bytes, _ := json.Marshal(raw)
	digest := fmt.Sprintf("%x", sha256.Sum256(bytes))
	var marks []*Mark
	seen := map[string]bool{}
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("invalid replay row")
		}
		symbol := canonicalSymbol(strArg(row, "symbol"))
		price := anyFloat(row["price"])
		if symbol == "" || seen[symbol] || !finite(price) || price <= 0 {
			return nil, errors.New("invalid or duplicate replay price")
		}
		seen[symbol] = true
		class := inferAssetClass(symbol)
		m := &Mark{Symbol: symbol, AssetClass: class, Price: price, Source: "backtest", MarkedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if class == "polymarket" {
			m.NoPrice = ptr(1 - price)
		}
		marks = append(marks, m)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var oldRun int64
	var oldStep int
	var oldDigest, oldAt string
	err = tx.QueryRow(`SELECT run_id,step,digest,replay_at FROM replay_steps WHERE portfolio_id=?`, portfolioID).Scan(&oldRun, &oldStep, &oldDigest, &oldAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && oldRun == runID && oldStep == step && oldDigest == digest && oldAt == at.Format(time.RFC3339Nano) {
		return map[string]any{"updated": 0, "replayed": true}, nil
	}
	if (oldRun != 0 && oldRun != runID) || step != oldStep+1 {
		return nil, errors.New("replay sequence conflict")
	}
	if oldAt != "" {
		previous, _ := time.Parse(time.RFC3339Nano, oldAt)
		if !at.After(previous) {
			return nil, errors.New("replay clock must advance")
		}
	}
	for _, m := range marks {
		if err := dbUpsertMarkExec(tx, m); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO replay_steps(portfolio_id,run_id,step,digest,replay_at) VALUES(?,?,?,?,?) ON CONFLICT(portfolio_id) DO UPDATE SET step=excluded.step,digest=excluded.digest,replay_at=excluded.replay_at`, portfolioID, runID, step, digest, at.Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	emit("trading.backtest.market_step", map[string]any{"portfolio_id": portfolioID, "step": step, "prices": raw})
	return map[string]any{"updated": len(marks), "prices": raw}, nil
}

func captureBacktestMarketBarsAdjusted(parent context.Context, symbols []string, interval string, startAt, endAt time.Time, adjustmentMode string) ([]*BacktestMarketBar, int, string, error) {
	symbols = cleanSymbols(symbols)
	if len(symbols) == 0 {
		return nil, 0, "", errors.New("at least one symbol required")
	}
	if endAt.Before(startAt) {
		return nil, 0, "", errors.New("backtest end date must be on or after start date")
	}
	provider, source, err := backtestBarsProviderForSymbols(symbols)
	if err != nil {
		return nil, 0, "", err
	}
	adjustmentMode, err = normalizeBacktestAdjustmentMode(adjustmentMode)
	if err != nil {
		return nil, 0, "", err
	}
	if _, ok := provider.(historicalAdjustedBacktestBarsProvider); !ok && adjustmentMode != "provider_adjusted" {
		return nil, 0, "", fmt.Errorf("market source %s cannot guarantee adjustment mode %s", source, adjustmentMode)
	}
	limit := estimateBacktestBarsLimit(startAt, endAt, interval)
	if limit <= 0 {
		return nil, 0, "", errors.New("date range must contain at least one market bar")
	}
	estimatedRows := estimateBacktestMarketRows(source, startAt, endAt, interval, len(symbols))
	if maxRows := configuredBacktestMaxMarketRows(); maxRows > 0 && estimatedRows > maxRows {
		return nil, 0, "", fmt.Errorf(
			"backtest would load approximately %d market rows, above this install's %d-row safety budget; shorten the range, reduce the universe, use a coarser interval, or raise backtest_max_market_rows",
			estimatedRows, maxRows)
	}

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, backtestHistoryFetchTimeout)
	defer cancel()
	type fetchResult struct {
		symbol string
		bars   []Bar
		err    error
	}
	results := make(chan fetchResult, len(symbols))
	sem := make(chan struct{}, backtestHistoryConcurrency)
	var wg sync.WaitGroup
	for _, symbol := range symbols {
		symbol := symbol
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- fetchResult{symbol: symbol, err: ctx.Err()}
				return
			}
			var bars []Bar
			var err error
			if adjusted, ok := provider.(historicalAdjustedBacktestBarsProvider); ok {
				bars, err = adjusted.BacktestBarsContextAdjustment(ctx, symbol, interval, startAt, inclusiveBacktestEnd(endAt, interval), limit, adjustmentMode)
			} else {
				bars, err = provider.BacktestBarsContext(ctx, symbol, interval, startAt, inclusiveBacktestEnd(endAt, interval), limit)
			}
			if err == nil {
				bars, err = validateBacktestHistory(symbol, source, interval, startAt, endAt, bars, time.Now().UTC())
			}
			results <- fetchResult{symbol: symbol, bars: bars, err: err}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	fetched := make(map[string][]Bar, len(symbols))
	actualRows := 0
	for result := range results {
		if result.err != nil {
			cancel()
			return nil, 0, source, fmt.Errorf("fetch %s bars: %w", result.symbol, result.err)
		}
		actualRows += len(result.bars)
		if maxRows := configuredBacktestMaxMarketRows(); maxRows > 0 && actualRows > maxRows {
			cancel()
			return nil, 0, source, fmt.Errorf("backtest fetched more than the %d-row market-data safety budget", maxRows)
		}
		fetched[result.symbol] = result.bars
	}

	bySymbol := map[string]map[int64]Bar{}
	commonTimes := map[int64]bool{}
	for i, symbol := range symbols {
		bars := fetched[symbol]
		if len(bars) == 0 {
			return nil, 0, source, fmt.Errorf("fetch %s bars: no bars returned", symbol)
		}
		rows := map[int64]Bar{}
		for _, bar := range bars {
			if bar.C <= 0 {
				continue
			}
			rows[bar.T] = bar
		}
		if len(rows) == 0 {
			return nil, 0, source, fmt.Errorf("fetch %s bars: no priced bars returned", symbol)
		}
		bySymbol[symbol] = rows
		if i == 0 {
			for t := range rows {
				commonTimes[t] = true
			}
			continue
		}
		for t := range commonTimes {
			if _, ok := rows[t]; !ok {
				delete(commonTimes, t)
			}
		}
	}
	times := make([]int64, 0, len(commonTimes))
	for t := range commonTimes {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if len(times) == 0 {
		return nil, 0, source, errors.New("market bars have no common timestamps across symbols")
	}
	minRows := 0
	for _, rows := range bySymbol {
		if minRows == 0 || len(rows) < minRows {
			minRows = len(rows)
		}
	}
	if minRows > 0 && len(times)*100 < minRows*95 {
		return nil, 0, source, fmt.Errorf(
			"market histories are not sufficiently aligned across symbols: only %d of at least %d timestamps are common",
			len(times), minRows)
	}
	out := make([]*BacktestMarketBar, 0, len(times)*len(symbols))
	for step, t := range times {
		for _, symbol := range symbols {
			bar := bySymbol[symbol][t]
			out = append(out, &BacktestMarketBar{
				Step: step + 1, Symbol: symbol, AssetClass: inferAssetClass(symbol), T: t,
				O: nonZeroBarPrice(bar.O, bar.C), H: nonZeroBarPrice(bar.H, bar.C),
				L: nonZeroBarPrice(bar.L, bar.C), C: bar.C, V: bar.V, Source: source,
				VolumeUnit: historicalVolumeUnit(source, inferAssetClass(symbol)), TimestampKind: "exchange",
			})
		}
	}
	return out, len(times), source, nil
}

func normalizeBacktestAdjustmentMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "provider_adjusted":
		return "provider_adjusted", nil
	case "raw":
		return "raw", nil
	case "split_adjusted":
		return "split_adjusted", nil
	case "price_return":
		return "price_return", nil
	case "total_return":
		return "total_return", nil
	default:
		return "", fmt.Errorf("adjustment_mode must be provider_adjusted|raw|split_adjusted|price_return|total_return")
	}
}

type historicalBacktestBarsProvider interface {
	BacktestBarsContext(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]Bar, error)
}

type historicalAdjustedBacktestBarsProvider interface {
	BacktestBarsContextAdjustment(ctx context.Context, symbol, interval string, start, end time.Time, limit int, adjustmentMode string) ([]Bar, error)
}

func backtestBarsProviderForSymbols(symbols []string) (historicalBacktestBarsProvider, string, error) {
	group := ""
	for _, symbol := range symbols {
		class := inferAssetClass(symbol)
		next := ""
		switch class {
		case "crypto":
			next = "crypto"
		case "equity", "etf":
			next = "stocks"
		default:
			return nil, "", fmt.Errorf("real backtest bars are not available for %s (%s)", symbol, class)
		}
		if group != "" && group != next {
			return nil, "", errors.New("real backtests require a single market calendar; do not mix crypto with equity/ETF symbols")
		}
		group = next
	}
	switch group {
	case "crypto":
		return newBinancePublic(), "binance-public", nil
	case "stocks":
		if globalEngine != nil {
			if live, ok := globalEngine.provider.(*liveProvider); ok && live.equity != nil && live.equity.available() {
				return live.equity, alpacaMarketDataSlug, nil
			}
		}
		return newYahooPublic(), "yahoo-finance", nil
	default:
		return nil, "", errors.New("no supported backtest symbols")
	}
}

func estimateBacktestBarsLimit(startAt, endAt time.Time, interval string) int {
	if endAt.Before(startAt) {
		return 0
	}
	duration := backtestIntervalDuration(interval)
	if duration <= 0 {
		duration = 24 * time.Hour
	}
	end := inclusiveBacktestEnd(endAt, interval)
	return int(end.Sub(startAt)/duration) + 1
}

func estimateBacktestMarketRows(source string, startAt, endAt time.Time, interval string, symbols int) int {
	if symbols <= 0 {
		return 0
	}
	perSymbol := estimateBacktestBarsLimit(startAt, endAt, interval)
	if source == "yahoo-finance" {
		perSymbol = estimateBacktestSteps(startAt, endAt, interval)
	}
	if perSymbol > math.MaxInt/symbols {
		return math.MaxInt
	}
	return perSymbol * symbols
}

func configuredBacktestMaxMarketRows() int {
	if globalCtx == nil {
		return defaultBacktestMaxMarketRows
	}
	raw := strings.TrimSpace(globalCtx.Config().Get("backtest_max_market_rows"))
	if raw == "" {
		return defaultBacktestMaxMarketRows
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultBacktestMaxMarketRows
	}
	return value
}

func validateBacktestHistory(symbol, source, interval string, startAt, endAt time.Time, bars []Bar, now time.Time) ([]Bar, error) {
	duration := backtestIntervalDuration(interval)
	if duration <= 0 {
		return nil, fmt.Errorf("unsupported backtest interval %q", interval)
	}
	normalized, err := normalizeBars(symbol, source, bars)
	if err != nil {
		return nil, err
	}
	if source == "yahoo-finance" {
		normalized = completedYahooStrategyBars(normalized, interval, now)
	} else {
		completed := normalized[:0]
		for _, bar := range normalized {
			if !time.Unix(bar.T, 0).UTC().Add(duration).After(now) {
				completed = append(completed, bar)
			}
		}
		normalized = completed
	}
	if len(normalized) == 0 {
		return nil, errors.New("provider returned no completed priced bars")
	}

	first := time.Unix(normalized[0].T, 0).UTC()
	last := time.Unix(normalized[len(normalized)-1].T, 0).UTC()
	startTolerance := duration
	endTolerance := duration
	if source == "yahoo-finance" {
		startTolerance = 7 * 24 * time.Hour
		endTolerance = 7 * 24 * time.Hour
		if interval == "1w" {
			startTolerance = 14 * 24 * time.Hour
			endTolerance = 14 * 24 * time.Hour
		}
	}
	if first.After(startAt.UTC().Add(startTolerance)) {
		return nil, fmt.Errorf(
			"history begins at %s, later than requested start %s; this provider does not cover the full range",
			first.Format(time.RFC3339), startAt.UTC().Format(time.RFC3339))
	}
	targetEnd := inclusiveBacktestEnd(endAt, interval)
	if targetEnd.After(now) {
		targetEnd = strategyClosedCandleBoundary(now, interval)
	}
	if last.Before(targetEnd.Add(-endTolerance)) {
		return nil, fmt.Errorf(
			"history ends at %s, earlier than requested end %s; this provider returned incomplete data",
			last.Format(time.RFC3339), targetEnd.UTC().Format(time.RFC3339))
	}
	if source == "binance-public" {
		for i := 1; i < len(normalized); i++ {
			previous := time.Unix(normalized[i-1].T, 0).UTC()
			current := time.Unix(normalized[i].T, 0).UTC()
			if current.Sub(previous) > duration+time.Second {
				return nil, fmt.Errorf(
					"history has a gap between %s and %s",
					previous.Format(time.RFC3339), current.Format(time.RFC3339))
			}
		}
	}
	return normalized, nil
}

func inclusiveBacktestEnd(endAt time.Time, interval string) time.Time {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m", "5m", "15m", "1h", "4h":
		return time.Date(endAt.Year(), endAt.Month(), endAt.Day(), 23, 59, 59, 0, time.UTC)
	default:
		return endAt
	}
}

func nonZeroBarPrice(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func estimateBacktestSteps(start, end time.Time, interval string) int {
	if end.Before(start) {
		return 0
	}
	days := int(end.Sub(start).Hours()/24) + 1
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m", "5m", "15m", "1h", "4h":
		return days * backtestStepsPerSession(interval)
	case "1w":
		return int(math.Ceil(float64(days) / 7))
	default:
		return days
	}
}

func normalizeBacktestInterval(interval string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(interval))
	if v == "" {
		v = "1d"
	}
	switch v {
	case "1m", "5m", "15m", "1h", "4h", "1d", "1w":
		return v, nil
	default:
		return "", fmt.Errorf("unsupported backtest interval %q; use 1m, 5m, 15m, 1h, 4h, 1d, or 1w", interval)
	}
}

func backtestStepsPerSession(interval string) int {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m":
		return 390
	case "5m":
		return 78 // 6.5h US-style regular session.
	case "15m":
		return 26
	case "1h":
		return 7
	case "4h":
		return 2
	default:
		return 1
	}
}

func backtestIntervalDuration(interval string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "4h":
		return 4 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func cleanSymbols(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, symbol := range in {
		s := strings.ToUpper(strings.TrimSpace(symbol))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func anyFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return n
	default:
		return 0
	}
}
