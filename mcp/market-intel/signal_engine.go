package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type scanSummary struct {
	Provider   string `json:"provider"`
	Universe   int    `json:"universe"`
	Scanned    int    `json:"scanned"`
	Emitted    int    `json:"emitted"`
	Rejected   int    `json:"rejected"`
	Evaluated  int    `json:"evaluated"`
	DurationMS int64  `json:"duration_ms"`
}

func (a *App) signalProviders(ctx *sdk.AppCtx) []MarketDataProvider {
	providers := []MarketDataProvider{NewBinancePublicProvider()}
	if ctx != nil && ctx.Config() != nil {
		slug := strings.TrimSpace(ctx.Config().Get("paid_provider_slug"))
		if slug != "" {
			venue := strings.TrimSpace(ctx.Config().Get("paid_provider_venue"))
			if venue == "" {
				venue = slug
			}
			providers = append(providers, &IntegrationProviderAdapter{Slug: slug, VenueName: venue, UniverseTool: orString(ctx.Config().Get("paid_provider_universe_tool"), "list_instruments"), BarsTool: orString(ctx.Config().Get("paid_provider_bars_tool"), "get_bars"), Client: newPlatformSourceClient(ctx)})
		}
	}
	return providers
}

func (a *App) runSignalScan(ctx context.Context, app *sdk.AppCtx, pid string) ([]scanSummary, error) {
	if pid == "" {
		return nil, errors.New("project id required for signal scan")
	}
	feed, err := ensureDefaultFeed(app.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	summaries := []scanSummary{}
	var errs []string
	for _, p := range a.signalProviders(app) {
		s, e := a.scanProvider(ctx, app, pid, feed, p)
		summaries = append(summaries, s)
		if e != nil {
			errs = append(errs, e.Error())
		}
	}
	if len(errs) > 0 {
		return summaries, errors.New(strings.Join(errs, "; "))
	}
	return summaries, nil
}

func (a *App) scanProvider(ctx context.Context, app *sdk.AppCtx, pid string, feed *SignalFeed, p MarketDataProvider) (summary scanSummary, retErr error) {
	start := time.Now()
	summary.Provider = p.Name()
	runID, err := startSignalRun(app.AppDB(), pid, p.Name(), feed.Slug)
	if err != nil {
		return summary, err
	}
	defer func() {
		summary.DurationMS = time.Since(start).Milliseconds()
		finishSignalRun(app.AppDB(), runID, summary.Universe, summary.Scanned, summary.Emitted, summary.Rejected, retErr)
		status := "healthy"
		if retErr != nil {
			status = "error"
		}
		upsertProviderHealth(app.AppDB(), pid, p.Name(), status, int(summary.DurationMS), summary.Universe, retErr)
	}()
	limit := configInt(app, "scan_universe_limit", 20, 1, 100)
	universe, err := p.Universe(ctx, limit)
	if err != nil {
		return summary, err
	}
	summary.Universe = len(universe)
	prices := map[string]float64{}
	for _, inst := range universe {
		prices[inst.Symbol] = inst.Last
	}
	if n, e := evaluateExpiredSignals(app.AppDB(), pid, p.Name(), prices); e == nil {
		summary.Evaluated = n
	}
	for _, inst := range universe {
		select {
		case <-ctx.Done():
			return summary, ctx.Err()
		default:
		}
		bars, e := p.Bars(ctx, inst.Symbol, feed.Interval, 250)
		if e != nil {
			summary.Rejected++
			continue
		}
		features, e := extractSignalFeatures(bars)
		if e != nil {
			summary.Rejected++
			continue
		}
		summary.Scanned++
		for _, candidate := range generateCandidates(features) {
			signal, ok := buildOpportunity(app, pid, feed, runID, p, inst, bars, features, candidate)
			if !ok {
				summary.Rejected++
				continue
			}
			inserted, e := insertSignal(app.AppDB(), &signal)
			if e != nil {
				return summary, e
			}
			if inserted {
				summary.Emitted++
				app.EmitWithProject("signal.created", pid, signal)
			}
		}
	}
	return summary, nil
}

func buildOpportunity(app *sdk.AppCtx, pid string, feed *SignalFeed, runID int64, p MarketDataProvider, inst Instrument, bars []Candle, f SignalFeatures, c SignalCandidate) (SignalOpportunity, bool) {
	if inst.QuoteTime > 0 && time.Since(time.Unix(inst.QuoteTime, 0)) > 2*time.Minute {
		return SignalOpportunity{}, false
	}
	mid := (inst.Bid + inst.Ask) / 2
	if mid <= 0 {
		return SignalOpportunity{}, false
	}
	spread := (inst.Ask - inst.Bid) / mid * 10000
	fee := configFloat(app, "round_trip_fee_bps", 20, 0, 500)
	slippage := configFloat(app, "slippage_bps", 5, 0, 500) + spread*.5
	cost := fee + spread + slippage
	liquidityPenalty := 0.0
	if inst.QuoteVolume24h < 5_000_000 {
		liquidityPenalty = .08
	}
	confidence := clamp(c.Confidence-liquidityPenalty-math.Min(spread/500, .12), 0, 1)
	net := c.ExpectedMoveBps - cost
	if confidence < feed.MinConfidence || net < feed.MinNetEdgeBps || spread > configFloat(app, "max_spread_bps", 35, 1, 1000) || inst.QuoteVolume24h < configFloat(app, "min_quote_volume_24h", 2_000_000, 0, 1e15) {
		return SignalOpportunity{}, false
	}
	entry := inst.Ask
	if c.Direction == "SELL" {
		entry = inst.Bid
	}
	risk := math.Max(f.ATR14*1.5, entry*.004)
	stop, target1, target2 := entry-risk, entry+risk*1.5, entry+risk*2.5
	if c.Direction == "SELL" {
		stop, target1, target2 = entry+risk, entry-risk*1.5, entry-risk*2.5
	}
	last := bars[len(bars)-1]
	for !last.Closed && len(bars) > 1 {
		bars = bars[:len(bars)-1]
		last = bars[len(bars)-1]
	}
	now := time.Now().UTC()
	return SignalOpportunity{PublicID: newSignalID(), ProjectID: pid, FeedID: feed.ID, FeedSlug: feed.Slug, RunID: runID, Provider: p.Name(), Venue: p.Venue(), ProviderSymbol: inst.Symbol, CanonicalSymbol: inst.Canonical, AssetClass: inst.AssetClass, Strategy: c.Strategy, Direction: c.Direction, Interval: feed.Interval, SignalTime: last.Time, GeneratedAt: now, ValidUntil: now.Add(6 * time.Hour), EntryPrice: entry, BidPrice: inst.Bid, AskPrice: inst.Ask, StopLoss: stop, Target1: target1, Target2: target2, Confidence: confidence, Score: c.Score, ExpectedMoveBps: c.ExpectedMoveBps, GrossEdgeBps: c.ExpectedMoveBps, CostBps: cost, NetEdgeBps: net, SpreadBps: spread, QuoteVolume24h: inst.QuoteVolume24h, Rationale: c.Rationale, Features: f, Provenance: map[string]any{"provider": p.Name(), "venue": p.Venue(), "quote_time": inst.QuoteTime, "bars": len(bars), "last_closed_bar": last.Time, "confidence_method": "heuristic_v1_unvalidated", "execution_note": executionNote(c.Direction, inst.AssetClass), "cost_model": map[string]any{"round_trip_fee_bps": fee, "spread_bps": spread, "slippage_bps": slippage}}, Status: "open"}, true
}

func executionNote(direction, assetClass string) string {
	if direction == "SELL" && assetClass == "crypto" {
		return "bearish signal: spot holders may reduce; opening a short requires a margin or derivatives venue"
	}
	return "directional research signal; execution venue and account constraints remain the subscriber's responsibility"
}

func newSignalID() string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		return fmt.Sprintf("sig_%d", time.Now().UnixNano())
	}
	return "sig_" + hex.EncodeToString(b)
}
func scanProjectID() string { return strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")) }
func configInt(ctx *sdk.AppCtx, key string, def, min, max int) int {
	if ctx != nil && ctx.Config() != nil {
		if n, e := strconv.Atoi(strings.TrimSpace(ctx.Config().Get(key))); e == nil && n >= min && n <= max {
			return n
		}
	}
	return def
}
func configFloat(ctx *sdk.AppCtx, key string, def, min, max float64) float64 {
	if ctx != nil && ctx.Config() != nil {
		if n, e := strconv.ParseFloat(strings.TrimSpace(ctx.Config().Get(key)), 64); e == nil && n >= min && n <= max {
			return n
		}
	}
	return def
}
func orString(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return strings.TrimSpace(v)
}
