package main

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestRiskProfilesAndResolvedPolicy(t *testing.T) {
	ctx := newTestCtx(t)
	recorded := installRecorder(t)
	id := mustCreatePortfolio(t, ctx, "Risk profile", []string{"equity"})
	app := &App{}
	profiles, err := app.toolRiskProfilesList(ctx, nil)
	if err != nil || len(profiles.(map[string]any)["profiles"].([]PortfolioRiskPolicy)) != 3 {
		t.Fatalf("profiles = %#v, err=%v", profiles, err)
	}
	out, err := app.toolPortfolioRiskUpdate(ctx, map[string]any{"portfolio_id": id, "risk_level": "balanced"})
	if err != nil {
		t.Fatal(err)
	}
	policy := out.(map[string]any)["policy"].(*PortfolioRiskPolicy)
	if policy.RiskLevel != "balanced" || policy.MaxPositionPct != 25 || policy.MaxDailyLossPct != 3 {
		t.Fatalf("balanced policy = %#v", policy)
	}
	if len(recorded.byTopic("risk.policy.changed")) != 1 {
		t.Fatalf("topics = %v", recorded.topics())
	}
	got, err := app.toolPortfolioRiskGet(ctx, map[string]any{"portfolio_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["state"].(*PortfolioRiskState).HighWaterEquity != 100_000 {
		t.Fatalf("risk state = %#v", got)
	}
}

func TestEngineMaxDrawdownPolicyHaltsPortfolio(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Drawdown halt", []string{"equity"})
	portfolio, _ := dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	_, err := dbUpsertPortfolioRiskPolicy(ctx.AppDB(), portfolio, PortfolioRiskPolicy{
		RiskLevel: "custom", MaxDailyLossPct: 50, MaxDrawdownPct: 5,
		MaxPositionPct: 100, MaxGrossExposurePct: 100, MaxOrderPct: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbUpdatePortfolioRiskState(ctx.AppDB(), portfolio, 110_000); err != nil {
		t.Fatal(err)
	}
	recorded := installRecorder(t)
	if err := markTick(nil, ctx); err != nil {
		t.Fatal(err)
	}
	portfolio, _ = dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	if portfolio.Status != "halted" {
		t.Fatalf("status = %s", portfolio.Status)
	}
	if len(recorded.byTopic("risk.limit.breached")) != 1 || len(recorded.byTopic("portfolio.performance.updated")) != 1 {
		t.Fatalf("topics = %v", recorded.topics())
	}
}

func TestPreTradeRiskRejectsOrderAndStackedPosition(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Pretrade", []string{"equity"})
	if err := dbUpsertMark(ctx.AppDB(), &Mark{Symbol: "AAPL", AssetClass: "equity", Price: 100, MarkedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	_, err := app.toolPortfolioRiskUpdate(ctx, map[string]any{
		"portfolio_id": id, "risk_level": "custom", "max_daily_loss_pct": 5,
		"max_drawdown_pct": 20, "max_position_pct": 15, "max_gross_exposure_pct": 50, "max_order_pct": 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	place := func(qty float64) map[string]any {
		out, placeErr := app.toolOrderPlace(ctx, map[string]any{
			"portfolio_id": id, "symbol": "AAPL", "side": "buy", "type": "limit", "limit_price": 100,
			"qty": qty, "rationale": "Measured entry that satisfies the test rationale requirement.",
		})
		if placeErr != nil {
			t.Fatal(placeErr)
		}
		return out.(map[string]any)
	}
	if got := place(130); got["code"] != "risk_max_order" {
		t.Fatalf("oversized order = %#v", got)
	}
	if got := place(100); got["status"] != "working" {
		t.Fatalf("first order = %#v", got)
	}
	if got := place(60); got["code"] != "risk_max_position" {
		t.Fatalf("stacked order = %#v", got)
	}
}

func TestConcurrentOrdersCannotRacePastPositionLimit(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Concurrent risk", []string{"equity"})
	if err := dbUpsertMark(ctx.AppDB(), &Mark{Symbol: "AAPL", AssetClass: "equity", Price: 100, MarkedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil { t.Fatal(err) }
	app := &App{}
	_, err := app.toolPortfolioRiskUpdate(ctx, map[string]any{
		"portfolio_id": id, "risk_level": "custom", "max_daily_loss_pct": 5, "max_drawdown_pct": 20,
		"max_position_pct": 15, "max_gross_exposure_pct": 100, "max_order_pct": 20,
	})
	if err != nil { t.Fatal(err) }
	var wg sync.WaitGroup
	results := make(chan map[string]any, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, placeErr := app.toolOrderPlace(ctx, map[string]any{
				"portfolio_id": id, "symbol": "AAPL", "side": "buy", "type": "limit", "limit_price": 100,
				"qty": 100, "rationale": "Concurrent risk-gate test with enough rationale for order placement.",
			})
			if placeErr != nil { t.Errorf("place: %v", placeErr); return }
			results <- out.(map[string]any)
		}()
	}
	wg.Wait(); close(results)
	working, rejected := 0, 0
	for result := range results {
		if result["status"] == "working" { working++ }
		if result["code"] == "risk_max_position" { rejected++ }
	}
	if working != 1 || rejected != 1 { t.Fatalf("working=%d rejected=%d", working, rejected) }
}

func TestRiskHighWaterDrawdown(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Drawdown", []string{"equity"})
	portfolio, _ := dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	if _, err := dbUpdatePortfolioRiskState(ctx.AppDB(), portfolio, 100_000); err != nil {
		t.Fatal(err)
	}
	state, err := dbUpdatePortfolioRiskState(ctx.AppDB(), portfolio, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if state.HighWaterEquity != 100_000 || math.Abs(state.CurrentDrawdownPct-(-10)) > 1e-9 {
		t.Fatalf("state = %#v", state)
	}
	state, err = dbUpdatePortfolioRiskState(ctx.AppDB(), portfolio, 110_000)
	if err != nil {
		t.Fatal(err)
	}
	if state.HighWaterEquity != 110_000 || state.CurrentDrawdownPct != 0 {
		t.Fatalf("new high state = %#v", state)
	}
}

func TestPortfolioObjectiveTracksPercentProgress(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Objective", []string{"equity"})
	app := &App{}
	out, err := app.toolPortfolioObjectiveCreate(ctx, map[string]any{
		"portfolio_id": id, "name": "Quarter target", "metric": "period_return_pct", "target_pct": 10.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := out.(map[string]any)["objective"].(*PortfolioObjective)
	if created.BaselineEquity == nil || *created.BaselineEquity != 100_000 {
		t.Fatalf("objective = %#v", created)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE portfolios SET cash=105000 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	listed, err := app.toolPortfolioObjectivesList(ctx, map[string]any{"portfolio_id": id})
	if err != nil {
		t.Fatal(err)
	}
	objective := listed.(map[string]any)["objectives"].([]*PortfolioObjective)[0]
	if objective.ActualPct == nil || math.Abs(*objective.ActualPct-5) > 1e-9 {
		t.Fatalf("actual = %#v", objective)
	}
	if objective.ProgressPct == nil || math.Abs(*objective.ProgressPct-50) > 1e-9 || objective.Achieved {
		t.Fatalf("progress = %#v", objective)
	}
	updated, err := app.toolPortfolioObjectiveUpdate(ctx, map[string]any{
		"portfolio_id": id, "objective_id": created.ID, "status": "archived",
	})
	if err != nil || updated.(map[string]any)["objective"].(*PortfolioObjective).Status != "archived" {
		t.Fatalf("updated = %#v, err=%v", updated, err)
	}
}
