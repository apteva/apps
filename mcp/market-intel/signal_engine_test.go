package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type testProvider struct{}

func (testProvider) Name() string                                                { return "test" }
func (testProvider) Venue() string                                               { return "test-venue" }
func (testProvider) Universe(context.Context, int) ([]Instrument, error)         { return nil, nil }
func (testProvider) Bars(context.Context, string, string, int) ([]Candle, error) { return nil, nil }

type fixtureProvider struct{ bars []Candle }

func (fixtureProvider) Name() string  { return "fixture" }
func (fixtureProvider) Venue() string { return "fixture" }
func (p fixtureProvider) Universe(context.Context, int) ([]Instrument, error) {
	return []Instrument{{Symbol: "BTCUSDT", Canonical: "BTC-USD", AssetClass: "crypto", Bid: 149.9, Ask: 150, Last: 149.95, QuoteVolume24h: 1e8}}, nil
}
func (p fixtureProvider) Bars(context.Context, string, string, int) ([]Candle, error) {
	return p.bars, nil
}

func trendingCandles(n int) []Candle {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]Candle, 0, n)
	for i := 0; i < n; i++ {
		p := 100 + float64(i)*.7 + float64(i%4)*.08
		out = append(out, Candle{Time: start.Add(time.Duration(i) * time.Hour).Unix(), Open: p - .2, High: p + .5, Low: p - .6, Close: p, Volume: 1000 + float64(i*20), Closed: true})
	}
	return out
}

func TestSignalFeaturesIgnoreOpenCandleAndGenerateTrend(t *testing.T) {
	bars := trendingCandles(90)
	base, err := extractSignalFeatures(bars)
	if err != nil {
		t.Fatal(err)
	}
	bars = append(bars, Candle{Time: bars[len(bars)-1].Time + 3600, Open: 1, High: 10000, Low: 1, Close: 9000, Volume: 1e9, Closed: false})
	withOpen, err := extractSignalFeatures(bars)
	if err != nil {
		t.Fatal(err)
	}
	if base.Price != withOpen.Price || base.VolumeZ20 != withOpen.VolumeZ20 {
		t.Fatalf("open candle changed features: base=%+v open=%+v", base, withOpen)
	}
	found := false
	for _, c := range generateCandidates(base) {
		if c.Strategy == "trend_momentum" && c.Direction == "BUY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected BUY trend candidate: %+v", base)
	}
}

func TestBuildOpportunityAppliesCostsAndLiquidityGates(t *testing.T) {
	bars := trendingCandles(90)
	f, _ := extractSignalFeatures(bars)
	feed := &SignalFeed{ID: 1, Slug: "x", Interval: "1h", MinConfidence: .6, MinNetEdgeBps: 20}
	cand := SignalCandidate{Strategy: "test", Direction: "BUY", Score: .8, Confidence: .8, ExpectedMoveBps: 150, Rationale: []string{"test"}}
	good := Instrument{Symbol: "BTCUSDT", Canonical: "BTC-USD", AssetClass: "crypto", Bid: 100, Ask: 100.05, Last: 100.02, QuoteVolume24h: 1e8}
	s, ok := buildOpportunity(nil, "p", feed, 1, testProvider{}, good, bars, f, cand)
	if !ok {
		t.Fatal("expected liquid signal to pass")
	}
	if s.NetEdgeBps >= s.GrossEdgeBps || s.CostBps <= 0 || s.EntryPrice != good.Ask {
		t.Fatalf("bad cost model: %+v", s)
	}
	wide := good
	wide.Ask = 102
	if _, ok := buildOpportunity(nil, "p", feed, 1, testProvider{}, wide, bars, f, cand); ok {
		t.Fatal("wide spread must be rejected")
	}
	thin := good
	thin.QuoteVolume24h = 100
	if _, ok := buildOpportunity(nil, "p", feed, 1, testProvider{}, thin, bars, f, cand); ok {
		t.Fatal("illiquid instrument must be rejected")
	}
	stale := good
	stale.QuoteTime = time.Now().Add(-10 * time.Minute).Unix()
	if _, ok := buildOpportunity(nil, "p", feed, 1, testProvider{}, stale, bars, f, cand); ok {
		t.Fatal("stale quote must be rejected")
	}
}

func TestBinanceProviderNormalizesUniverseAndClosedBars(t *testing.T) {
	now := time.UnixMilli(1769945000000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ticker/24hr":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","lastPrice":"100","bidPrice":"99.9","askPrice":"100.1","quoteVolume":"9000000"},{"symbol":"USDCUSDT","lastPrice":"1","bidPrice":".99","askPrice":"1.01","quoteVolume":"99999999"}]`))
		case "/klines":
			_, _ = w.Write([]byte(`[[1769940000000,"99","101","98","100","12",1769943599999],[1769943600000,"100","102","99","101","13",1769947199999]]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := NewBinancePublicProvider()
	p.BaseURL = srv.URL
	p.Client = srv.Client()
	p.Now = func() time.Time { return now }
	u, err := p.Universe(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(u) != 1 || u[0].Canonical != "BTC-USD" {
		t.Fatalf("universe=%+v", u)
	}
	bars, err := p.Bars(context.Background(), "BTCUSDT", "1h", 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || !bars[0].Closed || bars[1].Closed {
		t.Fatalf("bars=%+v", bars)
	}
}

func TestBinanceProviderLive(t *testing.T) {
	if os.Getenv("RUN_MARKET_INTEL_LIVE") != "1" {
		t.Skip("set RUN_MARKET_INTEL_LIVE=1")
	}
	p := NewBinancePublicProvider()
	u, err := p.Universe(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(u) == 0 {
		t.Fatal("empty live universe")
	}
	bars, err := p.Bars(context.Background(), u[0].Symbol, "1h", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractSignalFeatures(bars); err != nil {
		t.Fatalf("live bars unusable: %v", err)
	}
}

func TestSignalPipelineLive(t *testing.T) {
	if os.Getenv("RUN_MARKET_INTEL_LIVE") != "1" {
		t.Skip("set RUN_MARKET_INTEL_LIVE=1")
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("live-project"), tk.WithConfig(map[string]string{"scan_universe_limit": "1"}))
	feed, err := ensureDefaultFeed(ctx.AppDB(), "live-project")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := (&App{}).scanProvider(context.Background(), ctx, "live-project", feed, NewBinancePublicProvider())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Universe != 1 || summary.Scanned != 1 {
		t.Fatalf("unexpected live summary: %+v", summary)
	}
}

func newSignalTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, name := range []string{"migrations/001_init.sql", "migrations/002_signal_pipeline.sql"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(b)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSignalPersistenceDedupAndEvaluation(t *testing.T) {
	db := newSignalTestDB(t)
	feed, err := ensureDefaultFeed(db, "p1")
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	s := SignalOpportunity{PublicID: "sig_test", ProjectID: "p1", FeedID: feed.ID, Provider: "test", Venue: "test", ProviderSymbol: "BTCUSDT", CanonicalSymbol: "BTC-USD", AssetClass: "crypto", Strategy: "trend_momentum", Direction: "BUY", Interval: "1h", SignalTime: 123, ValidUntil: past, EntryPrice: 100, BidPrice: 99.9, AskPrice: 100, StopLoss: 98, Target1: 103, Target2: 105, Confidence: .75, Score: .8, ExpectedMoveBps: 100, GrossEdgeBps: 100, CostBps: 25, NetEdgeBps: 75, SpreadBps: 10, QuoteVolume24h: 1e8, Rationale: []string{"x"}, Provenance: map[string]any{"source": "test"}}
	inserted, err := insertSignal(db, &s)
	if err != nil || !inserted {
		t.Fatalf("insert: %v %v", inserted, err)
	}
	s.PublicID = "sig_duplicate"
	inserted, err = insertSignal(db, &s)
	if err != nil || inserted {
		t.Fatalf("dedupe: %v %v", inserted, err)
	}
	n, err := evaluateExpiredSignals(db, "p1", "test", map[string]float64{"BTCUSDT": 102})
	if err != nil || n != 1 {
		t.Fatalf("evaluate: %d %v", n, err)
	}
	rows, err := listSignals(db, "p1", "", "evaluated", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %+v %v", rows, err)
	}
	if rows[0].Outcome != "win" || rows[0].RealizedReturnBps == nil || *rows[0].RealizedReturnBps < 190 {
		t.Fatalf("outcome=%+v", rows[0])
	}
	metrics, err := signalMetrics(db, "p1", feed.Slug)
	if err != nil || metrics["wins"].(int64) != 1 {
		t.Fatalf("metrics=%+v err=%v", metrics, err)
	}
}

func TestScannerPersistsDedupesAndEmits(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("scan-project"), tk.WithEmitter(rec), tk.WithConfig(map[string]string{"round_trip_fee_bps": "10", "slippage_bps": "2"}))
	feed, err := ensureDefaultFeed(ctx.AppDB(), "scan-project")
	if err != nil {
		t.Fatal(err)
	}
	p := fixtureProvider{bars: trendingCandles(100)}
	app := &App{}
	first, err := app.scanProvider(context.Background(), ctx, "scan-project", feed, p)
	if err != nil {
		t.Fatal(err)
	}
	if first.Emitted == 0 {
		t.Fatalf("expected emitted signal: %+v", first)
	}
	second, err := app.scanProvider(context.Background(), ctx, "scan-project", feed, p)
	if err != nil {
		t.Fatal(err)
	}
	if second.Emitted != 0 {
		t.Fatalf("same closed candle must dedupe: %+v", second)
	}
	events := rec.EventsByTopic("signal.created")
	if len(events) != first.Emitted {
		t.Fatalf("events=%d emitted=%d", len(events), first.Emitted)
	}
	rows, err := listSignals(ctx.AppDB(), "scan-project", "", "open", 20)
	if err != nil || len(rows) != first.Emitted {
		t.Fatalf("rows=%d emitted=%d err=%v", len(rows), first.Emitted, err)
	}
}

func TestBacktestIsWalkForwardAndCostAdjusted(t *testing.T) {
	bars := trendingCandles(140)
	r, err := backtestBars("BTCUSDT", "1h", "test", "trend_momentum", bars, 25)
	if err != nil {
		t.Fatal(err)
	}
	if r.Signals == 0 || len(r.Trades) == 0 {
		t.Fatalf("expected replayed trades: %+v", r)
	}
	for _, tr := range r.Trades {
		if tr.SignalTime >= bars[len(bars)-1].Time {
			t.Fatalf("signal used future bar: %+v", tr)
		}
	}
	if len(r.Methodology) < 3 {
		t.Fatalf("missing audit methodology: %+v", r.Methodology)
	}
}
