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
