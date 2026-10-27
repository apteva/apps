package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWaitBacktestAgentSettled_UsesTelemetryQuietWindow(t *testing.T) {
	queried := false
	since := time.Now().Add(-10 * time.Second)
	quietEvent := time.Now().Add(-backtestAgentQuietWindow - time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/telemetry" {
			t.Fatalf("path=%s, want /api/telemetry", r.URL.Path)
		}
		if got := r.URL.Query().Get("agent_id"); got != "42" {
			t.Fatalf("agent_id=%s, want 42", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer dev-7" {
			t.Fatalf("authorization=%q, want app token", got)
		}
		queried = true
		_ = json.NewEncoder(w).Encode([]backtestTelemetryEvent{{
			ID:      "ev-1",
			AgentID: 42,
			Type:    "llm.done",
			Time:    quietEvent,
		}})
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "dev-7")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ok := waitBacktestAgentSettled(ctx, &BacktestRun{EnvironmentAgentID: 42}, since)
	if !ok {
		t.Fatal("waitBacktestAgentSettled returned false")
	}
	if !queried {
		t.Fatal("telemetry endpoint was not queried")
	}
}

func TestBacktestIntervalEstimates(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	cases := map[string]int{
		"5m":  156,
		"15m": 52,
		"1h":  14,
		"4h":  4,
		"1d":  2,
		"1w":  1,
	}
	for interval, want := range cases {
		got := estimateBacktestSteps(start, end, interval)
		if got != want {
			t.Fatalf("estimateBacktestSteps(%s)=%d, want %d", interval, got, want)
		}
	}
}

func TestNormalizeBacktestInterval(t *testing.T) {
	got, err := normalizeBacktestInterval("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1d" {
		t.Fatalf("empty interval=%q, want 1d", got)
	}
	if _, err := normalizeBacktestInterval("30s"); err == nil {
		t.Fatal("unsupported interval accepted")
	}
}

func TestBacktestReplayTimeIntraday(t *testing.T) {
	run := &BacktestRun{StartAt: "2026-01-05", Interval: "15m"}
	got := backtestReplayTime(run, 27)
	want := time.Date(2026, 1, 5, 6, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("replay time=%s, want %s", got, want)
	}
}
