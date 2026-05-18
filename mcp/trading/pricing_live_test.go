package main

// Tier 1 tests for the live pricing path. Every external HTTP call
// is replaced with a stub server we run on 127.0.0.1; we never hit
// Binance or Polymarket for real here. That belongs in Tier 2 with
// an opt-in env flag.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── Binance public client ─────────────────────────────────────────

func TestBinancePublic_Quote_ParsesWireShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "BTCUSDT") {
			t.Errorf("expected BTCUSDT in query, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"symbol": "BTCUSDT",
			"lastPrice": "67842.10",
			"prevClosePrice": "66467.90",
			"priceChange": "1374.20",
			"priceChangePercent": "2.07",
			"volume": "418234.51",
			"quoteVolume": "28400000000.00"
		}`))
	}))
	defer srv.Close()
	b := &binancePublic{base: srv.URL, client: srv.Client()}

	mark, err := b.Quote("BTC-USD")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if mark.Symbol != "BTC-USD" {
		t.Errorf("symbol=%q, want BTC-USD", mark.Symbol)
	}
	if mark.AssetClass != "crypto" {
		t.Errorf("asset_class=%q, want crypto", mark.AssetClass)
	}
	if mark.Price != 67842.10 {
		t.Errorf("price=%v, want 67842.10", mark.Price)
	}
	if mark.PrevClose == nil || *mark.PrevClose != 66467.90 {
		t.Errorf("prev_close mismatch: %v", mark.PrevClose)
	}
	if mark.Volume24h == nil || *mark.Volume24h != 28_400_000_000 {
		t.Errorf("volume_24h mismatch: %v", mark.Volume24h)
	}
}

func TestBinancePublic_UniverseBatch_RoundTripsSymbols(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The wire query is symbols=["BTCUSDT","ETHUSDT",...]; just
		// confirm we sent the array form rather than asserting exact
		// encoding.
		if !strings.Contains(r.URL.RawQuery, "symbols=") {
			t.Errorf("expected symbols= in query, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"symbol":"BTCUSDT","lastPrice":"67000","prevClosePrice":"66000","quoteVolume":"100"},
			{"symbol":"ETHUSDT","lastPrice":"3400", "prevClosePrice":"3500", "quoteVolume":"50"}
		]`))
	}))
	defer srv.Close()
	b := &binancePublic{base: srv.URL, client: srv.Client()}

	out, err := b.UniverseBatch([]string{"BTC-USD", "ETH-USD", "UNKNOWN-USD"})
	if err != nil {
		t.Fatalf("universe: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 marks (UNKNOWN-USD skipped), got %d", len(out))
	}
	gotSymbols := map[string]bool{}
	for _, m := range out {
		gotSymbols[m.Symbol] = true
	}
	if !gotSymbols["BTC-USD"] || !gotSymbols["ETH-USD"] {
		t.Errorf("expected BTC-USD + ETH-USD, got %v", gotSymbols)
	}
}

func TestBinancePublic_Quote_ErrorsOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	b := &binancePublic{base: srv.URL, client: srv.Client()}

	if _, err := b.Quote("BTC-USD"); err == nil {
		t.Error("expected error on 503")
	}
}

func TestBinancePublic_Quote_RejectsUnknownInternalSymbol(t *testing.T) {
	b := newBinancePublic()
	if _, err := b.Quote("DOES-NOT-EXIST"); err == nil {
		t.Error("expected error for unmapped symbol")
	}
}

// ─── Polymarket public client ─────────────────────────────────────

func TestPolymarketPublic_Quote_ParsesGammaShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "btc-100k-2026") {
			t.Errorf("expected slug in query, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"slug": "btc-100k-2026",
			"question": "Will Bitcoin close above $100,000 by end of 2026?",
			"outcomes": "[\"Yes\",\"No\"]",
			"outcomePrices": "[\"0.78\",\"0.22\"]",
			"volume24hr": "14220000",
			"endDate": "2026-12-31T23:59:59Z"
		}]`))
	}))
	defer srv.Close()
	p := &polymarketPublic{base: srv.URL, client: srv.Client()}

	mark, err := p.Quote("POLY:btc-100k-2026")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if mark.AssetClass != "polymarket" {
		t.Errorf("asset_class=%q", mark.AssetClass)
	}
	if mark.Price != 0.78 {
		t.Errorf("yes price=%v, want 0.78", mark.Price)
	}
	if mark.NoPrice == nil || *mark.NoPrice != 0.22 {
		t.Errorf("no price mismatch: %v", mark.NoPrice)
	}
}

func TestPolymarketPublic_RejectsNonBinaryMarket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"slug":"x","outcomes":"[\"A\",\"B\",\"C\"]","outcomePrices":"[\"0.3\",\"0.5\",\"0.2\"]"}]`))
	}))
	defer srv.Close()
	p := &polymarketPublic{base: srv.URL, client: srv.Client()}

	if _, err := p.Quote("POLY:x"); err == nil {
		t.Error("expected error for non-YES/NO market")
	}
}

// ─── liveProvider composition ─────────────────────────────────────

// FailingBinance simulates an upstream that always errors. Used to
// verify liveProvider falls back to mock data on every error path.
type failingBinance struct{}

func (failingBinance) UniverseBatch(symbols []string) ([]*Mark, error) {
	return nil, http.ErrServerClosed
}
func (failingBinance) Quote(symbol string) (*Mark, error) {
	return nil, http.ErrServerClosed
}

func TestLiveProvider_FallsBackToMockOnCryptoError(t *testing.T) {
	mock := newMockProvider()
	lp := newLiveProvider(mock)
	// Replace clients with failing stubs.
	lp.crypto = &binancePublic{base: "http://127.0.0.1:0", client: &http.Client{Timeout: 50 * time.Millisecond}}

	mark, err := lp.Quote("BTC-USD")
	if err != nil {
		t.Fatalf("quote should fall back, not error: %v", err)
	}
	if mark == nil || mark.Symbol != "BTC-USD" {
		t.Errorf("fallback mark missing or wrong: %+v", mark)
	}
	// Health surface should record the error.
	snap := lp.Health()
	c, _ := snap["crypto"].(map[string]any)
	if c == nil {
		t.Fatal("crypto class missing from health snapshot")
	}
	if errs, _ := c["errors_60s"].(int); errs < 1 {
		t.Errorf("errors_60s=%v, expected ≥ 1", c["errors_60s"])
	}
}

// stubUnreachableYahoo points the Yahoo client at a closed TCP port so
// the equity dispatch falls through to mock predictably in tests. Tests
// that exercise the live-Yahoo path itself would use a real httptest
// server; these tests verify the mock-fallback path.
func stubUnreachableYahoo(lp *liveProvider) {
	lp.yahoo = &yahooPublic{
		base:   "http://127.0.0.1:0",
		client: &http.Client{Timeout: 50 * time.Millisecond},
		sem:    make(chan struct{}, 4),
	}
}

func TestLiveProvider_EquityFallsBackToMockWhenYahooDown(t *testing.T) {
	mock := newMockProvider()
	lp := newLiveProvider(mock)
	// v0.4.9 onward Yahoo is the equity default — point it at an
	// unreachable host so the equity path falls through to mock and
	// this test exercises the fallback contract.
	stubUnreachableYahoo(lp)

	mark, err := lp.Quote("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if mark.AssetClass != "equity" {
		t.Errorf("asset_class=%q", mark.AssetClass)
	}
	snap := lp.Health()
	c, _ := snap["equity"].(map[string]any)
	if c == nil || c["name"] != "mock" {
		t.Errorf("equity class should be mock after yahoo failure, got %v", c)
	}
}

func TestLiveProvider_HealthSurfaceCoversAllClasses(t *testing.T) {
	mock := newMockProvider()
	lp := newLiveProvider(mock)
	lp.crypto = &binancePublic{base: "http://127.0.0.1:0", client: &http.Client{Timeout: 50 * time.Millisecond}}
	lp.poly = &polymarketPublic{base: "http://127.0.0.1:0", client: &http.Client{Timeout: 50 * time.Millisecond}}
	stubUnreachableYahoo(lp)
	_ = lp.Universe()

	snap := lp.Health()
	for _, class := range []string{"crypto", "polymarket", "equity", "etf"} {
		if _, ok := snap[class]; !ok {
			t.Errorf("health snapshot missing class %q (got keys %v)", class, keys(snap))
		}
	}
	if eq, _ := snap["equity"].(map[string]any); eq == nil || eq["name"] != "mock" {
		t.Errorf("equity not stamped as mock after yahoo failure: %v", snap["equity"])
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLiveProvider_UniversePopulatesAllClassesEvenWhenLiveFails(t *testing.T) {
	mock := newMockProvider()
	lp := newLiveProvider(mock)
	// Both live clients point at unreachable hosts.
	lp.crypto = &binancePublic{base: "http://127.0.0.1:0", client: &http.Client{Timeout: 50 * time.Millisecond}}
	lp.poly = &polymarketPublic{base: "http://127.0.0.1:0", client: &http.Client{Timeout: 50 * time.Millisecond}}

	got := lp.Universe()
	classes := map[string]int{}
	for _, m := range got {
		classes[m.AssetClass]++
	}
	if classes["crypto"] == 0 {
		t.Error("expected crypto marks via fallback")
	}
	if classes["polymarket"] == 0 {
		t.Error("expected polymarket marks via fallback")
	}
	if classes["equity"] == 0 {
		t.Error("expected equity marks (always mock)")
	}
}

// ─── Cache + health window ────────────────────────────────────────

func TestMarkCache_HitsAndExpires(t *testing.T) {
	c := newMarkCache(50 * time.Millisecond)
	m := &Mark{Symbol: "X", Price: 1}
	c.put(m)
	if got := c.get("X"); got != m {
		t.Errorf("expected hit, got %+v", got)
	}
	time.Sleep(80 * time.Millisecond)
	if got := c.get("X"); got != nil {
		t.Errorf("expected expiry, got %+v", got)
	}
}

func TestProviderHealth_SlidingWindow(t *testing.T) {
	h := newProviderHealth()
	for i := 0; i < 5; i++ {
		h.note("crypto", http.ErrServerClosed)
	}
	snap := h.snapshot()
	c, _ := snap["crypto"].(map[string]any)
	if c == nil {
		t.Fatal("crypto missing")
	}
	if errs, _ := c["errors_60s"].(int); errs != 5 {
		t.Errorf("errors_60s=%v, want 5", errs)
	}
	if stale, _ := c["stale"].(bool); !stale {
		t.Error("expected stale=true (no OK ever recorded)")
	}
	h.ok("crypto", "binance-public")
	snap = h.snapshot()
	c = snap["crypto"].(map[string]any)
	if stale, _ := c["stale"].(bool); stale {
		t.Error("expected stale=false after ok()")
	}
	if c["name"] != "binance-public" {
		t.Errorf("name=%v", c["name"])
	}
}

// ─── newProvider("live") / market_source tool ─────────────────────

func TestNewProvider_LiveReturnsLiveProvider(t *testing.T) {
	p := newProvider("live")
	if _, ok := p.(*liveProvider); !ok {
		t.Errorf("newProvider(\"live\") = %T, want *liveProvider", p)
	}
}

func TestToolMarketSource_ReturnsHealthSnapshot(t *testing.T) {
	ctx := newTestCtx(t)
	out, err := (&App{}).toolMarketSource(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)
	providers, ok := got["providers"].(map[string]any)
	if !ok {
		t.Fatalf("missing providers in market_source: %v", got)
	}
	// Mock setup → we expect at least one class with name "mock".
	found := false
	for _, v := range providers {
		if c, _ := v.(map[string]any); c != nil && c["name"] == "mock" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one class to report name=mock in test ctx, got %v", providers)
	}
}
