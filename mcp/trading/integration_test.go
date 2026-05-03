//go:build integration

package main

// Tier 2 — real binary, real HTTP. Boot the sidecar, talk MCP + REST.
// Validates the SDK wiring + the engine's tick loop end-to-end. Same
// pattern as apps/mcp/crm and apps/mcp/storage.
//
// Run with:  go test -tags integration ./...

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{"tick_seconds": "2"}),
	)
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

// Full path: create portfolio via REST → place order via MCP → wait one
// tick → assert order is filled, position opened, fill journal landed.
func TestSidecar_OrderFillRoundTrip(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"tick_seconds":   "1",
			"bootstrap_demo": "false", // keep portfolio_id=1 deterministic for this test
		}),
	)

	// Create portfolio.
	var createOut map[string]any
	resp := sc.POST("/portfolios", map[string]any{
		"name": "IT", "mandate": "integration test", "allowed_classes": []string{"equity", "etf"},
		"starting_cash": 50_000,
	}, &createOut)
	if resp.Status != 201 {
		t.Fatalf("create portfolio: status=%d body=%s", resp.Status, string(resp.Body))
	}
	pfID := int64(createOut["portfolio_id"].(float64))

	// Place a market order via MCP. Required: portfolio_id, rationale ≥ 30.
	r := sc.MCP("order_place", map[string]any{
		"portfolio_id": pfID,
		"symbol":       "AAPL",
		"side":         "buy",
		"type":         "market",
		"qty":          5,
		"rationale":    "starter equity position — Tier 2 integration test should fill on next tick.",
	})
	if r["status"] != "working" {
		t.Fatalf("order_place status=%v full=%v", r["status"], r)
	}
	orderID, _ := r["order_id"].(string)
	if !strings.HasPrefix(orderID, "o-") {
		t.Fatalf("order_id format unexpected: %v", r)
	}

	// Wait for the engine tick (configured to 1s in this run).
	time.Sleep(2200 * time.Millisecond)

	// Assert the order moved to filled.
	var ordersOut map[string]any
	sc.GET("/portfolios/1/orders?status=filled", &ordersOut)
	orders, _ := ordersOut["orders"].([]any)
	if len(orders) != 1 {
		t.Fatalf("expected 1 filled order, got %d (raw: %v)", len(orders), ordersOut)
	}
	first, _ := orders[0].(map[string]any)
	if first["id"] != orderID {
		t.Errorf("filled order id=%v, want %s", first["id"], orderID)
	}
	if px, _ := first["avg_fill_price"].(float64); px <= 0 {
		t.Errorf("avg_fill_price not set on filled order: %v", first)
	}

	// Assert position was opened.
	var posOut map[string]any
	sc.GET("/portfolios/1/positions", &posOut)
	pos, _ := posOut["positions"].([]any)
	if len(pos) != 1 {
		t.Fatalf("expected 1 position, got %d", len(pos))
	}
	p0, _ := pos[0].(map[string]any)
	if p0["symbol"] != "AAPL" || p0["qty"].(float64) != 5 {
		t.Errorf("position wrong: %v", p0)
	}

	// Assert journal has the fill row + the auto-written rationale row.
	var jOut map[string]any
	sc.GET("/portfolios/1/journal?limit=20", &jOut)
	entries, _ := jOut["entries"].([]any)
	kinds := map[string]int{}
	for _, e := range entries {
		em := e.(map[string]any)
		kinds[em["kind"].(string)]++
	}
	if kinds["fill"] < 1 {
		t.Errorf("expected ≥ 1 fill journal entry, got %d (raw: %v)", kinds["fill"], jOut)
	}
	if kinds["rationale"] < 1 {
		t.Errorf("expected ≥ 1 rationale journal entry, got %d", kinds["rationale"])
	}
}

// Limit order at a far-away price stays working across two ticks.
func TestSidecar_LimitDoesNotFillUnlessCrossed(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"tick_seconds":   "1",
			"bootstrap_demo": "false",
		}),
	)
	var createOut map[string]any
	sc.POST("/portfolios", map[string]any{
		"name": "IT-Limit", "mandate": "integration", "allowed_classes": []string{"equity"},
		"starting_cash": 10_000,
	}, &createOut)
	pfID := int64(createOut["portfolio_id"].(float64))

	// AAPL marks ~ $224. A limit at $50 is unreachable.
	r := sc.MCP("order_place", map[string]any{
		"portfolio_id": pfID,
		"symbol":       "AAPL", "side": "buy", "type": "limit",
		"qty": 1, "limit_price": 50,
		"rationale": "deliberate non-crossing limit — should remain working across multiple ticks.",
	})
	if r["status"] != "working" {
		t.Fatalf("expected working, got %v", r)
	}
	time.Sleep(2200 * time.Millisecond)

	var ordersOut map[string]any
	sc.GET("/portfolios/1/orders?status=working", &ordersOut)
	orders, _ := ordersOut["orders"].([]any)
	if len(orders) != 1 {
		t.Fatalf("expected limit to stay working, got %d working orders", len(orders))
	}
}

// Polymarket sanity — YES buy fills against the YES mark, position
// records outcome=YES.
func TestSidecar_PolymarketYesFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"tick_seconds":   "1",
			"bootstrap_demo": "false",
		}),
	)
	var createOut map[string]any
	sc.POST("/portfolios", map[string]any{
		"name": "IT-Poly", "allowed_classes": []string{"polymarket"},
		"starting_cash": 5_000,
	}, &createOut)
	pfID := int64(createOut["portfolio_id"].(float64))

	r := sc.MCP("order_place", map[string]any{
		"portfolio_id": pfID,
		"symbol":       "POLY:btc-100k-2026",
		"side":         "yes", "type": "market", "qty": 100,
		"rationale":    "small starter — confirming polymarket YES side fills end to end.",
	})
	if r["status"] != "working" {
		t.Fatalf("expected working, got %v", r)
	}
	time.Sleep(2200 * time.Millisecond)

	var posOut map[string]any
	sc.GET("/portfolios/1/positions", &posOut)
	pos, _ := posOut["positions"].([]any)
	if len(pos) != 1 {
		t.Fatalf("expected 1 poly position, got %d", len(pos))
	}
	p0, _ := pos[0].(map[string]any)
	if p0["outcome"] != "YES" {
		t.Errorf("outcome=%v, want YES", p0["outcome"])
	}
}

// /healthz/details should reflect that the engine is actually ticking
// in a spawned-binary deployment — that's the regression guard for
// the failure mode where workers were silently not running.
func TestSidecar_HealthzDetailsShowsTicks(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{"tick_seconds": "1"}),
	)
	// Give the engine ~3 ticks worth of wall time.
	time.Sleep(3500 * time.Millisecond)
	var got map[string]any
	resp := sc.GET("/healthz/details", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, string(resp.Body))
	}
	ticks, _ := got["ticks"].(float64)
	if ticks < 2 {
		t.Errorf("expected ≥ 2 ticks after 3.5s, got %v (full: %v)", ticks, got)
	}
	marks, _ := got["last_marks_refreshed"].(float64)
	if marks <= 0 {
		t.Errorf("expected last_marks_refreshed > 0, got %v", marks)
	}
}

// Concurrent REST order-placement — the original failure pattern that
// surfaced in Tier 3 (agent fires order_place rapidly while engine
// ticks; before the SetMaxOpenConns(1) fix this raced and orders got
// silently dropped). Post-fix every concurrent order persists.
func TestSidecar_ConcurrentRESTOrderPlacement(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"tick_seconds":   "1",
			"bootstrap_demo": "false",
		}),
	)
	var createOut map[string]any
	resp := sc.POST("/portfolios", map[string]any{
		"name":            "Concurrent",
		"mandate":         "concurrent placement test",
		"allowed_classes": []string{"equity"},
		"starting_cash":   100_000,
	}, &createOut)
	if resp.Status != 201 {
		t.Fatalf("create portfolio: status=%d body=%s", resp.Status, string(resp.Body))
	}
	pfID := int64(createOut["portfolio_id"].(float64))

	const N = 12
	var wg sync.WaitGroup
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := sc.MCP("order_place", map[string]any{
				"portfolio_id": pfID,
				"symbol":       "AAPL",
				"side":         "buy",
				"type":         "limit",
				"qty":          1,
				"limit_price":  50, // unreachable — every order should land working
				"rationale":    "concurrent REST placement — must persist as working without SQL contention errors.",
			})
			if r["status"] != "working" {
				errCh <- fmtErr2("placement #%d status=%v full=%v", i, r["status"], r)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("%v", err)
	}

	// Now read all working orders — should be N.
	var ordersOut map[string]any
	sc.GET("/portfolios/1/orders?status=working&limit=100", &ordersOut)
	orders, _ := ordersOut["orders"].([]any)
	if len(orders) != N {
		t.Errorf("expected %d working orders, got %d (orders body: %v)", N, len(orders), ordersOut)
	}
}

// End-to-end fill on the spawned binary, fast tick, with metrics
// confirming the engine actually did the work.
func TestSidecar_FillSurfacedInMetrics(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{"tick_seconds": "1"}),
	)
	var createOut map[string]any
	sc.POST("/portfolios", map[string]any{
		"name": "FillProbe", "allowed_classes": []string{"equity"}, "starting_cash": 50_000,
	}, &createOut)
	pfID := int64(createOut["portfolio_id"].(float64))

	r := sc.MCP("order_place", map[string]any{
		"portfolio_id": pfID,
		"symbol":       "NVDA", "side": "buy", "type": "market", "qty": 1,
		"rationale":    "metrics regression — confirm fills_total increments after one tick on the live binary.",
	})
	if r["status"] != "working" {
		t.Fatalf("place: %v", r)
	}
	time.Sleep(2300 * time.Millisecond)

	var details map[string]any
	sc.GET("/healthz/details", &details)
	if got, _ := details["fills_this_run"].(float64); got < 1 {
		t.Errorf("fills_this_run=%v after 2.3s, want ≥ 1 (full: %v)", got, details)
	}
}

// Test helper for concurrent test errors — separate from the T1 helper
// to avoid build-tag pollution.
func fmtErr2(format string, args ...any) error {
	return &fmtError2{format: format, args: args}
}

type fmtError2 struct {
	format string
	args   []any
}

func (e *fmtError2) Error() string {
	// strings.Replacer hack — good enough for test output, no need
	// to pull in fmt.
	out := strings.NewReplacer("%d", "?", "%v", "?").Replace(e.format)
	_ = e.args
	return out
}

// ─── Auto-fill / bootstrap on spawned binary ───────────────────────

// TestSidecar_BootstrapAppliedAtFirstBoot — proof end-to-end that the
// auto-fill creates a portfolio + watchlist + welcome journal entry
// when the project is empty at install time. Spawned with the v0.2
// defaults (bootstrap_demo=true, mock pricing for offline test
// reliability).
func TestSidecar_BootstrapAppliedAtFirstBoot(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"pricing_provider":    "mock",     // deterministic for tests
			"tick_seconds":        "1",
			"bootstrap_demo":      "true",
			"bootstrap_watchlist": "BTC-USD,ETH-USD,SOL-USD",
			"starting_cash":       "75000",
		}),
	)

	// Give OnMount a moment to finish.
	time.Sleep(800 * time.Millisecond)

	// One portfolio appeared.
	var pfs map[string]any
	resp := sc.GET("/portfolios", &pfs)
	if resp.Status != 200 {
		t.Fatalf("/portfolios status=%d body=%s", resp.Status, string(resp.Body))
	}
	rows, _ := pfs["portfolios"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 portfolio after bootstrap, got %d", len(rows))
	}
	row := rows[0].(map[string]any)
	if got := row["name"]; got != "Demo Crypto" {
		t.Errorf("name=%v, want Demo Crypto", got)
	}
	if got, _ := row["cash"].(float64); got != 75_000 {
		t.Errorf("cash=%v, want 75000", got)
	}
	wl, _ := row["watchlist"].([]any)
	if len(wl) != 3 {
		t.Errorf("watchlist size=%d, want 3 (got %v)", len(wl), wl)
	}

	// Welcome journal entry.
	var j map[string]any
	sc.GET("/portfolios/1/journal?kind=note&limit=5", &j)
	entries, _ := j["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("expected 1 welcome journal entry, got %d", len(entries))
	}
}

// TestSidecar_BootstrapSkippedOnFlagOff — the `bootstrap_demo: "false"`
// escape hatch.
func TestSidecar_BootstrapSkippedOnFlagOff(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"pricing_provider": "mock",
			"bootstrap_demo":   "false",
		}),
	)
	time.Sleep(500 * time.Millisecond)
	var pfs map[string]any
	sc.GET("/portfolios", &pfs)
	rows, _ := pfs["portfolios"].([]any)
	if len(rows) != 0 {
		t.Errorf("expected 0 portfolios with bootstrap_demo=false, got %d", len(rows))
	}
}

// TestSidecar_BootstrapIdempotentOnRestart — restart the binary;
// bootstrap should NOT create a second portfolio.
func TestSidecar_BootstrapIdempotentOnRestart(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := dataDir + "/trading.db"

	// First boot.
	sc1 := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"pricing_provider": "mock",
			"bootstrap_demo":   "true",
		}),
		tk.WithEnv("DB_PATH", dbPath),
	)
	time.Sleep(500 * time.Millisecond)
	var pfs1 map[string]any
	sc1.GET("/portfolios", &pfs1)
	rows1, _ := pfs1["portfolios"].([]any)
	if len(rows1) != 1 {
		t.Fatalf("first boot: expected 1 portfolio, got %d", len(rows1))
	}
	sc1.Stop()

	// Second boot — same DB path.
	sc2 := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"pricing_provider": "mock",
			"bootstrap_demo":   "true",
		}),
		tk.WithEnv("DB_PATH", dbPath),
	)
	time.Sleep(500 * time.Millisecond)
	var pfs2 map[string]any
	sc2.GET("/portfolios", &pfs2)
	rows2, _ := pfs2["portfolios"].([]any)
	if len(rows2) != 1 {
		t.Errorf("second boot: expected 1 portfolio (idempotent), got %d", len(rows2))
	}
}

// TestSidecar_HealthzIncludesProviders — /healthz/details exposes a
// per-class providers map, even in mock mode.
func TestSidecar_HealthzIncludesProviders(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{"pricing_provider": "mock", "tick_seconds": "1"}),
	)
	time.Sleep(1500 * time.Millisecond)
	var got map[string]any
	resp := sc.GET("/healthz/details", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	provs, ok := got["providers"].(map[string]any)
	if !ok {
		t.Fatalf("providers missing in /healthz/details: %v", got)
	}
	for _, class := range []string{"crypto", "polymarket", "equity"} {
		if _, ok := provs[class]; !ok {
			t.Errorf("provider class %q missing", class)
		}
	}
}

// ─── Optional live-network smoke test ──────────────────────────────
//
// Gated on T2_LIVE=1 so CI without internet still passes. Run with:
//   T2_LIVE=1 go test -tags integration -count=1 -run TestSidecar_LiveBinanceFetchesRealBTC ./...
func TestSidecar_LiveBinanceFetchesRealBTC(t *testing.T) {
	if os.Getenv("T2_LIVE") != "1" {
		t.Skip("set T2_LIVE=1 to enable live-network smoke test")
	}
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"pricing_provider": "live",
			"tick_seconds":     "2",
			"bootstrap_demo":   "false",
		}),
	)
	// Wait two ticks so the live universe call has run.
	time.Sleep(5 * time.Second)
	var q map[string]any
	resp := sc.GET("/quotes/BTC-USD", &q)
	if resp.Status != 200 {
		t.Fatalf("/quotes/BTC-USD: %d body=%s", resp.Status, string(resp.Body))
	}
	price, _ := q["price"].(float64)
	if price < 100 {
		t.Errorf("BTC price=%v, expected real price (>>100); is live wired up?", price)
	}
	// /healthz/details should report binance-public for crypto.
	var h map[string]any
	sc.GET("/healthz/details", &h)
	provs, _ := h["providers"].(map[string]any)
	c, _ := provs["crypto"].(map[string]any)
	if c == nil || c["name"] != "binance-public" {
		t.Errorf("crypto provider=%v, want binance-public", c)
	}
}
