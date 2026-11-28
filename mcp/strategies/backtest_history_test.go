package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestResolveBacktestRangeRejectsInvalidInput(t *testing.T) {
	if _, _, err := resolveBacktestRange("not-a-date", "2025-01-01"); err == nil {
		t.Fatal("expected invalid start error")
	}
	if _, _, err := resolveBacktestRange("2025-02-01", "2025-01-01"); err == nil {
		t.Fatal("expected reversed range error")
	}
}
