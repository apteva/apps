package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestBinanceBacktestBarsPaginatesBeyondSingleRequest(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	const count = 2505
	end := start.Add((count - 1) * time.Hour)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		startMS, err := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		if err != nil {
			t.Fatalf("startTime: %v", err)
		}
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || limit <= 0 || limit > 1000 {
			t.Fatalf("limit = %q", r.URL.Query().Get("limit"))
		}
		index := int(time.UnixMilli(startMS).Sub(start) / time.Hour)
		rows := make([][]any, 0, limit)
		for i := index; i < count && len(rows) < limit; i++ {
			at := start.Add(time.Duration(i) * time.Hour)
			rows = append(rows, []any{
				at.UnixMilli(), "100", "102", "99", "101", "10",
			})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()

	client := &binancePublic{base: srv.URL, client: srv.Client()}
	bars, err := client.BacktestBarsContext(context.Background(), "BTC-USD", "1h", start, end, count)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
	if len(bars) != count {
		t.Fatalf("bars = %d, want %d", len(bars), count)
	}
	if bars[0].T != start.Unix() || bars[len(bars)-1].T != end.Unix() {
		t.Fatalf("range = %s to %s", time.Unix(bars[0].T, 0), time.Unix(bars[len(bars)-1].T, 0))
	}
}

func TestBinanceBacktestBarsRetriesTransientPageFailure(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode([][]any{
			{start.UnixMilli(), "100", "102", "99", "101", "10"},
		})
	}))
	defer srv.Close()

	client := &binancePublic{base: srv.URL, client: srv.Client()}
	bars, err := client.BacktestBarsContext(context.Background(), "BTC-USD", "1h", start, start, 1)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(bars) != 1 {
		t.Fatalf("requests=%d bars=%d, want 2/1", requests, len(bars))
	}
}

func TestYahooBacktestBarsPagesLongHourlyRangeWithoutOverlap(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)
	var mu sync.Mutex
	var periods [][2]int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		period1, _ := strconv.ParseInt(r.URL.Query().Get("period1"), 10, 64)
		period2, _ := strconv.ParseInt(r.URL.Query().Get("period2"), 10, 64)
		mu.Lock()
		periods = append(periods, [2]int64{period1, period2})
		mu.Unlock()
		response := map[string]any{"chart": map[string]any{
			"error": nil,
			"result": []any{map[string]any{
				"meta":      map[string]any{"symbol": "AAPL"},
				"timestamp": []int64{period1},
				"indicators": map[string]any{"quote": []any{map[string]any{
					"open": []float64{100}, "high": []float64{102}, "low": []float64{99},
					"close": []float64{101}, "volume": []float64{10},
				}}},
			}},
		}}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := &yahooPublic{base: srv.URL, client: srv.Client(), sem: make(chan struct{}, 1)}
	bars, err := client.BacktestBarsContext(context.Background(), "AAPL", "1h", start, end, 8760)
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 5 || len(bars) != 5 {
		t.Fatalf("periods=%d bars=%d, want 5/5", len(periods), len(bars))
	}
	for i := 1; i < len(periods); i++ {
		if periods[i-1][1] != periods[i][0] {
			t.Fatalf("page %d ended at %d but page %d began at %d", i-1, periods[i-1][1], i, periods[i][0])
		}
	}
}

func TestValidateBacktestHistoryAcceptsLeapYearHourlyRange(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	const count = 8784
	bars := make([]Bar, 0, count)
	for i := 0; i < count; i++ {
		bars = append(bars, Bar{T: start.Add(time.Duration(i) * time.Hour).Unix(), O: 100, H: 102, L: 99, C: 101})
	}
	got, err := validateBacktestHistory("BTC-USD", "binance-public", "1h", start, end, bars, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != count {
		t.Fatalf("bars = %d, want %d", len(got), count)
	}
	if estimateBacktestBarsLimit(start, end, "1h") != count {
		t.Fatalf("estimated bars = %d, want %d", estimateBacktestBarsLimit(start, end, "1h"), count)
	}
}

func TestValidateBacktestHistoryRejectsInternalCryptoGap(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []Bar{
		{T: start.Unix(), C: 100},
		{T: start.Add(2 * time.Hour).Unix(), C: 101},
	}
	_, err := validateBacktestHistory("BTC-USD", "binance-public", "1h", start, start, bars, start.Add(24*time.Hour))
	if err == nil {
		t.Fatal("expected an internal-gap error")
	}
}

func TestValidateBacktestHistoryAcceptsLatestCompletedCurrentDayBar(t *testing.T) {
	start := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 27, 16, 37, 0, 0, time.UTC)
	bars := []Bar{
		{T: start.Unix(), C: 100},
		{T: start.Add(time.Hour).Unix(), C: 101},
		{T: start.Add(2 * time.Hour).Unix(), C: 102},
		{T: start.Add(3 * time.Hour).Unix(), C: 103},
		{T: start.Add(4 * time.Hour).Unix(), C: 104}, // Still forming at 16:37.
	}
	got, err := validateBacktestHistory("BTC-USD", "binance-public", "1h", start, start, bars, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[len(got)-1].T != start.Add(3*time.Hour).Unix() {
		t.Fatalf("completed bars = %#v", got)
	}
}

func TestResolveBacktestRangeRejectsInvalidInput(t *testing.T) {
	if _, _, err := resolveBacktestRange("not-a-date", "2025-01-01"); err == nil {
		t.Fatal("expected invalid start error")
	}
	if _, _, err := resolveBacktestRange("2025-02-01", "2025-01-01"); err == nil {
		t.Fatal("expected reversed range error")
	}
}

func TestDefaultBacktestBudgetSupportsYearScaleIntradayData(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	fourSymbols := estimateBacktestMarketRows("binance-public", start, end, "5m", 4)
	if fourSymbols > defaultBacktestMaxMarketRows {
		t.Fatalf("four-symbol year estimate %d exceeds default budget %d", fourSymbols, defaultBacktestMaxMarketRows)
	}
	fiveSymbols := estimateBacktestMarketRows("binance-public", start, end, "5m", 5)
	if fiveSymbols <= defaultBacktestMaxMarketRows {
		t.Fatalf("five-symbol year estimate %d should require an explicit budget increase", fiveSymbols)
	}
}

func TestRealProvidersSupportYearScaleHourlyBacktests(t *testing.T) {
	if os.Getenv("RUN_TRADING_PROVIDER_TESTS") != "1" {
		t.Skip("set RUN_TRADING_PROVIDER_TESTS=1 for real Binance/Yahoo history checks")
	}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)

	t.Run("crypto", func(t *testing.T) {
		bars, err := newBinancePublic().BacktestBarsContext(context.Background(), "BTC-USD", "1h", start, end, 8760)
		if err != nil {
			t.Fatal(err)
		}
		if len(bars) != 8760 {
			t.Fatalf("BTC hourly bars = %d, want 8760", len(bars))
		}
		t.Logf("loaded %d completed BTC-USD hourly bars", len(bars))
	})
	t.Run("equity", func(t *testing.T) {
		bars, steps, source, err := captureBacktestMarketBars(context.Background(), []string{"AAPL"}, "1h", start, end)
		if err != nil {
			t.Fatal(err)
		}
		if source != "yahoo-finance" || steps < 1500 || len(bars) != steps {
			t.Fatalf("source=%s AAPL hourly steps=%d rows=%d", source, steps, len(bars))
		}
		t.Logf("captured %d completed AAPL hourly replay steps", steps)
	})
	t.Run("capture-and-persist", func(t *testing.T) {
		marketBars, steps, source, err := captureBacktestMarketBars(context.Background(), []string{"BTC-USD", "ETH-USD"}, "1h", start, end)
		if err != nil {
			t.Fatal(err)
		}
		if source != "binance-public" || steps != 8760 || len(marketBars) != 2*steps {
			t.Fatalf("source=%s steps=%d rows=%d", source, steps, len(marketBars))
		}
		ctx := newTestCtx(t)
		portfolioID := mustCreatePortfolio(t, ctx, "year-scale", []string{"crypto"})
		strategyID, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
			ProjectID: "test-proj", Name: "year-scale fixed strategy", Status: "active", Version: 1,
			Definition: map[string]any{
				"universe":        []any{"BTC-USD", "ETH-USD"},
				"cadence":         "1h",
				"rebalance_every": 24,
				"rules": []any{map[string]any{
					"name":     "balanced crypto",
					"allocate": []any{map[string]any{"symbol": "BTC-USD", "weight": 0.5}, map[string]any{"symbol": "ETH-USD", "weight": 0.5}},
				}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		runID, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
			ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID, StrategyVersion: 1, RunKind: "strategy",
			Name: "year-scale", Status: "queued", Symbols: []string{"BTC-USD", "ETH-USD"},
			StartAt: "2025-01-01", EndAt: "2025-12-31", Interval: "1h",
			StartingCash: 100000, TotalSteps: steps,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := dbReplaceBacktestMarketBars(ctx.AppDB(), runID, marketBars); err != nil {
			t.Fatal(err)
		}
		var stored int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM backtest_market_bars WHERE run_id = ?`, runID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != len(marketBars) {
			t.Fatalf("stored rows = %d, want %d", stored, len(marketBars))
		}
		lastMarks, err := dbBacktestMarketMarks(ctx.AppDB(), runID, steps, []string{"BTC-USD", "ETH-USD"})
		if err != nil || len(lastMarks) != 2 {
			t.Fatalf("last replay step marks=%d err=%v", len(lastMarks), err)
		}
		run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if _, err := runStrategyBacktestToEnd(run); err != nil {
			t.Fatal(err)
		}
		completed, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Status != "completed" || completed.CurrentStep != steps {
			t.Fatalf("completed status=%s step=%d/%d", completed.Status, completed.CurrentStep, steps)
		}
		t.Logf("captured and persisted %d rows, then executed %d replay steps in %s", stored, steps, time.Since(started))
	})
}
