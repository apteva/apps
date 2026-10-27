package main

import (
	"testing"
	"time"
)

func TestStrategyRunClaimIsExactlyOncePerSignalBar(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "claim-test", []string{"equity"})
	barAt := time.Date(2026, 9, 3, 14, 30, 0, 0, time.UTC)
	event := &StrategyRunEvent{ProjectID: "test-proj", PortfolioID: portfolioID, AssignmentID: 77, StrategyID: 8, StrategyVersion: 2, SignalBarAt: barAt}
	claimed, err := dbClaimStrategyRun(ctx.AppDB(), event, []any{}, map[string]float64{"AAPL": 1})
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = dbClaimStrategyRun(ctx.AppDB(), event, []any{}, map[string]float64{"AAPL": 1})
	if err != nil || claimed {
		t.Fatalf("duplicate claim = %v, %v", claimed, err)
	}
	if err := dbFinishStrategyRun(ctx.AppDB(), event.AssignmentID, barAt, "orders_submitted", []string{"order-1"}, nil); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM strategy_run_events WHERE assignment_id = 77`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "orders_submitted" {
		t.Fatalf("status = %q", status)
	}
}
