package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestYahooQuoteUsesRegularMarketTimestamp(t *testing.T) {
	tradeTime := time.Date(2026, 7, 14, 15, 45, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{"chart": map[string]any{
			"error": nil,
			"result": []any{map[string]any{
				"meta": map[string]any{
					"symbol": "AAPL", "regularMarketPrice": 211.5,
					"regularMarketTime": tradeTime.Unix(),
				},
				"timestamp":  []int64{},
				"indicators": map[string]any{"quote": []any{}},
			}},
		}}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	yahoo := &yahooPublic{base: srv.URL, client: srv.Client(), sem: make(chan struct{}, 1)}
	mark, err := yahoo.Quote("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if mark.MarkedAt != tradeTime.Format(time.RFC3339) {
		t.Fatalf("marked_at = %q, want exchange timestamp %q", mark.MarkedAt, tradeTime.Format(time.RFC3339))
	}
}

func TestYahooBacktestBarsUsesDateBoundedRealHistory(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	wantTimes := []int64{
		time.Date(2026, 1, 5, 14, 30, 0, 0, time.UTC).Unix(),
		time.Date(2026, 1, 6, 14, 30, 0, 0, time.UTC).Unix(),
		time.Date(2026, 1, 7, 14, 30, 0, 0, time.UTC).Unix(),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("range") != "" {
			t.Errorf("range = %q, want date-bounded period query", r.URL.Query().Get("range"))
		}
		if r.URL.Query().Get("interval") != "1d" {
			t.Errorf("interval = %q, want 1d", r.URL.Query().Get("interval"))
		}
		period1, _ := strconv.ParseInt(r.URL.Query().Get("period1"), 10, 64)
		period2, _ := strconv.ParseInt(r.URL.Query().Get("period2"), 10, 64)
		if period1 != start.Unix() {
			t.Errorf("period1 = %d, want %d", period1, start.Unix())
		}
		if period2 != time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC).Unix() {
			t.Errorf("period2 = %d, want next midnight", period2)
		}
		response := map[string]any{"chart": map[string]any{
			"error": nil,
			"result": []any{map[string]any{
				"meta":      map[string]any{"symbol": "AAPL"},
				"timestamp": append(append([]int64{}, wantTimes...), time.Date(2026, 1, 8, 14, 30, 0, 0, time.UTC).Unix()),
				"indicators": map[string]any{"quote": []any{map[string]any{
					"open":   []float64{100, 101, 102, 103},
					"high":   []float64{102, 103, 104, 105},
					"low":    []float64{99, 100, 101, 102},
					"close":  []float64{101, 102, 103, 104},
					"volume": []float64{10, 20, 30, 40},
				}}},
			}},
		}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer srv.Close()

	yahoo := &yahooPublic{base: srv.URL, client: srv.Client(), sem: make(chan struct{}, 1)}
	bars, err := yahoo.BacktestBars("AAPL", "1d", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 3 {
		t.Fatalf("bars = %d, want 3 date-bounded rows", len(bars))
	}
	for i, bar := range bars {
		if bar.T != wantTimes[i] {
			t.Fatalf("bar[%d].T = %d, want %d", i, bar.T, wantTimes[i])
		}
	}
}

func TestYahooFourHourAggregationDoesNotCrossSessions(t *testing.T) {
	day1 := time.Date(2026, 1, 5, 14, 30, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	bars := []Bar{}
	for dayIndex, start := range []time.Time{day1, day2} {
		for i := 0; i < 6; i++ {
			price := float64(100 + dayIndex*10 + i)
			bars = append(bars, Bar{T: start.Add(time.Duration(i) * time.Hour).Unix(), O: price, H: price + 2, L: price - 1, C: price + 1, V: 10})
		}
	}

	got := aggregateYahooFourHourBars(bars)
	if len(got) != 4 {
		t.Fatalf("aggregated bars = %d, want two per session", len(got))
	}
	if got[0].O != 100 || got[0].C != 104 || got[0].H != 105 || got[0].L != 99 || got[0].V != 40 {
		t.Fatalf("first aggregate = %#v", got[0])
	}
	if time.Unix(got[2].T, 0).UTC().Format("2006-01-02") != day2.Format("2006-01-02") {
		t.Fatal("four-hour aggregation crossed trading sessions")
	}
}

func TestCompletedYahooStrategyBarsExcludeFormingBars(t *testing.T) {
	now := time.Date(2026, 7, 14, 15, 10, 0, 0, time.UTC)
	hourly := []Bar{
		{T: time.Date(2026, 7, 14, 13, 30, 0, 0, time.UTC).Unix(), C: 100},
		{T: time.Date(2026, 7, 14, 14, 30, 0, 0, time.UTC).Unix(), C: 101},
	}
	got := completedYahooStrategyBars(hourly, "1h", now)
	if len(got) != 1 || got[0].C != 100 {
		t.Fatalf("completed hourly bars = %#v, want only the 13:30 bar", got)
	}

	daily := []Bar{
		{T: time.Date(2026, 7, 13, 13, 30, 0, 0, time.UTC).Unix(), C: 99},
		{T: time.Date(2026, 7, 14, 13, 30, 0, 0, time.UTC).Unix(), C: 100},
	}
	got = completedYahooStrategyBars(daily, "1d", now)
	if len(got) != 1 || got[0].C != 99 {
		t.Fatalf("completed daily bars = %#v, want only the prior session", got)
	}
}

func TestBacktestProviderRoutesStockUniversesToYahoo(t *testing.T) {
	provider, source, err := backtestBarsProviderForSymbols([]string{"AAPL", "SPY"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*yahooPublic); !ok || source != "yahoo-finance" {
		t.Fatalf("provider = %T source = %q, want Yahoo", provider, source)
	}
	if _, _, err := backtestBarsProviderForSymbols([]string{"AAPL", "BTC-USD"}); err == nil || !strings.Contains(err.Error(), "single market calendar") {
		t.Fatalf("mixed universe error = %v", err)
	}
	if _, _, err := backtestBarsProviderForSymbols([]string{"POLY:test"}); err == nil {
		t.Fatal("expected unsupported Polymarket backtest error")
	}
}
