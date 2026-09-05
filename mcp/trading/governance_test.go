package main

import (
	"database/sql"
	"math"
	"testing"
	"time"
)

func governanceStrategyDefinition(symbols ...string) map[string]any {
	return map[string]any{
		"universe": symbols,
		"cadence":  "1d",
		"rules": []any{map[string]any{
			"name": "allocate", "allocate": []any{map[string]any{"symbol": symbols[0], "weight": 1.0}},
		}},
	}
}

func mustCreateTestStrategy(t *testing.T, ctx interface{ AppDB() *sql.DB }, symbols ...string) int64 {
	t.Helper()
	id, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID: "test-proj", Name: "Governed strategy", Status: "active", Definition: governanceStrategyDefinition(symbols...), Version: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPortfolioUniverseAllowlistRejectsCommonOrderPath(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	portfolioID := mustCreatePortfolio(t, ctx, "universe", []string{"crypto"})
	if _, err := app.toolPortfolioUniverseUpdate(ctx, map[string]any{
		"portfolio_id": portfolioID, "selection_mode": "symbol_allowlist", "include_symbols": []any{"ETH-USD"},
	}); err != nil {
		t.Fatal(err)
	}
	place := func(symbol string) map[string]any {
		out, err := app.toolOrderPlace(ctx, map[string]any{
			"portfolio_id": portfolioID, "symbol": symbol, "side": "buy", "type": "market", "qty": 0.01,
			"rationale": "Verify that the codified portfolio universe gates every order path.",
		})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	if got := place("BTC-USD"); got["code"] != "universe_not_allowed" {
		t.Fatalf("outside allowlist=%#v", got)
	}
	if _, err := app.toolPortfolioUniverseUpdate(ctx, map[string]any{
		"portfolio_id": portfolioID, "include_symbols": []any{"BTC-USD"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := place("BTC-USD"); got["status"] != "working" {
		t.Fatalf("inside allowlist=%#v", got)
	}
}

func TestPortfolioUniverseLetsExistingHoldingExitButNotOversell(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	portfolioID := mustCreatePortfolio(t, ctx, "restricted exit", []string{"equity"})
	if _, err := ctx.AppDB().Exec(`INSERT INTO positions(project_id,portfolio_id,symbol,asset_class,qty,avg_cost)
		VALUES('test-proj',?,'AAPL','equity',10,100)`, portfolioID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPortfolioUniverseUpdate(ctx, map[string]any{
		"portfolio_id": portfolioID, "selection_mode": "symbol_allowlist", "include_symbols": []any{"MSFT"},
	}); err != nil {
		t.Fatal(err)
	}
	placeSell := func(qty float64) map[string]any {
		out, err := app.toolOrderPlace(ctx, map[string]any{
			"portfolio_id": portfolioID, "symbol": "AAPL", "side": "sell", "type": "limit", "qty": qty,
			"limit_price": 100.0, "rationale": "Exit a legacy holding after its symbol left the mandate universe.",
		})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	if got := placeSell(6); got["status"] != "working" {
		t.Fatalf("reducing sell=%#v", got)
	}
	if got := placeSell(5); got["code"] != "universe_not_allowed" {
		t.Fatalf("oversell with reserved exit=%#v", got)
	}
}

func TestReferenceUniverseAndStrategyAssignmentAreEnforced(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	portfolioID := mustCreatePortfolio(t, ctx, "reference universe", []string{"equity"})
	today := time.Now().UTC().Format("2006-01-02")
	if _, err := ctx.AppDB().Exec(`INSERT INTO securities(id,asset_class,name,status,source) VALUES('sec-aapl','equity','Apple','active','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO security_listings(security_id,venue,symbol,valid_from,active,source) VALUES('sec-aapl','NASDAQ','AAPL',?,1,'test')`, today); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO universe_memberships(universe_id,security_id,valid_from,source) VALUES('quality-us','sec-aapl',?,'test')`, today); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPortfolioUniverseUpdate(ctx, map[string]any{
		"portfolio_id": portfolioID, "selection_mode": "reference_universe", "reference_universe_id": "quality-us", "require_active_listing": true,
	}); err != nil {
		t.Fatal(err)
	}
	portfolio, _ := dbGetPortfolio(ctx.AppDB(), "test-proj", portfolioID)
	if violation, err := portfolioUniverseViolation(ctx.AppDB(), portfolio, "AAPL"); err != nil || violation != nil {
		t.Fatalf("member rejected: violation=%#v err=%v", violation, err)
	}
	if violation, err := portfolioUniverseViolation(ctx.AppDB(), portfolio, "MSFT"); err != nil || violation == nil || violation.Code != "universe_not_member" {
		t.Fatalf("nonmember result: violation=%#v err=%v", violation, err)
	}
	strategyID := mustCreateTestStrategy(t, ctx, "MSFT")
	if _, err := app.toolStrategyAssign(ctx, map[string]any{"portfolio_id": portfolioID, "strategy_id": strategyID}); err == nil {
		t.Fatal("strategy outside portfolio universe was assigned")
	}
}

func TestStrategyScorecardPersistsEvaluationAndGatesPromotion(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
		ProjectID: "test-proj", Name: "paper gate", AllowedClasses: []string{"crypto"}, StartingCash: 100_000,
		Mode: "paper", ExecutionEnvironment: "broker_paper", BrokerSlug: "alpaca-trading",
	})
	if err != nil {
		t.Fatal(err)
	}
	strategyID := mustCreateTestStrategy(t, ctx, "BTC-USD")
	policy := defaultScorecardPolicy(portfolioID, strategyID, "test-proj")
	policy.RequireOutOfSample = false
	policy.EnforcementEnabled = true
	policy.Criteria = []ScorecardCriterion{
		{Metric: "return_pct", Operator: "min", Threshold: 5, Required: true},
		{Metric: "max_drawdown_abs_pct", Operator: "max", Threshold: 10, Required: true},
	}
	policy, err = dbUpsertStrategyScorecardPolicy(ctx.AppDB(), *policy)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID, StrategyVersion: 1,
		RunKind: "strategy", Name: "passing", Status: "completed", Symbols: []string{"BTC-USD"},
		StartAt: "2026-01-01", EndAt: "2026-03-01", Interval: "1d", StartingCash: 100_000, TotalSteps: 2,
		ReferenceManifest: map[string]any{"manifest_sha256": "dataset-123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []*BacktestSnapshot{
		{RunID: runID, Step: 0, Equity: 100_000, Cash: 100_000},
		{RunID: runID, Step: 1, Equity: 96_000, Cash: 96_000},
		{RunID: runID, Step: 2, Equity: 110_000, Cash: 110_000},
	} {
		if err := dbUpsertBacktestSnapshot(ctx.AppDB(), snapshot); err != nil {
			t.Fatal(err)
		}
	}
	evaluation, err := evaluateAndStoreStrategyScorecard(ctx.AppDB(), "test-proj", portfolioID, strategyID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !evaluation.Passed || evaluation.DatasetSHA256 != "" || evaluation.PolicyHash == "" || evaluation.PolicyHash != policy.PolicyHash || math.Abs(evaluation.Metrics["return_pct"]-10) > 1e-9 {
		t.Fatalf("evaluation=%#v", evaluation)
	}
	rows, err := dbListStrategyScorecardEvaluations(ctx.AppDB(), "test-proj", portfolioID, strategyID, 10)
	if err != nil || len(rows) != 1 || rows[0].BacktestRunID != runID {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	portfolio, _ := dbGetPortfolio(ctx.AppDB(), "test-proj", portfolioID)
	if allowed, _ := scorecardAllowsExecution(ctx.AppDB(), portfolio, strategyID); allowed {
		t.Fatal("broker paper execution allowed before promotion")
	}
	for _, stage := range []string{"paper_candidate", "paper"} {
		policy, err = promoteStrategyScorecard(ctx.AppDB(), policy, stage)
		if err != nil {
			t.Fatalf("promote %s: %v", stage, err)
		}
	}
	if allowed, reason := scorecardAllowsExecution(ctx.AppDB(), portfolio, strategyID); !allowed {
		t.Fatalf("paper stage blocked: %s", reason)
	}
	_, err = promoteStrategyScorecard(ctx.AppDB(), policy, "live")
	if err == nil {
		t.Fatal("promotion skipped live_candidate")
	}
	policy.Criteria[0].Threshold = 50
	policy, err = dbUpsertStrategyScorecardPolicy(ctx.AppDB(), *policy)
	if err != nil {
		t.Fatal(err)
	}
	if policy.PromotionStage != "research" {
		t.Fatalf("policy change did not reset promotion stage: %s", policy.PromotionStage)
	}
	if policy.PolicyHash == evaluation.PolicyHash {
		t.Fatal("material policy change retained the old policy hash")
	}
	if _, err = promoteStrategyScorecard(ctx.AppDB(), policy, "live_candidate"); err == nil {
		t.Fatal("promotion reused an evaluation from a superseded scorecard policy")
	}
	policy, err = promoteStrategyScorecard(ctx.AppDB(), policy, "suspended")
	if err != nil || policy.PromotionStage != "suspended" {
		t.Fatalf("suspend=%#v err=%v", policy, err)
	}
	if allowed, _ := scorecardAllowsExecution(ctx.AppDB(), portfolio, strategyID); allowed {
		t.Fatal("suspended scorecard allowed execution")
	}
}

func TestScorecardRequiresOutOfSampleWhenConfigured(t *testing.T) {
	policy := defaultScorecardPolicy(1, 1, "test-proj")
	policy.Criteria = []ScorecardCriterion{{Metric: "return_pct", Operator: "min", Threshold: 0, Required: true}}
	checks, passed := evaluateScorecardMetrics(policy, map[string]float64{"return_pct": 12}, "in_sample")
	if passed || len(checks) != 2 || checks[0].Passed {
		t.Fatalf("in-sample scorecard passed: %#v", checks)
	}
	if _, passed := evaluateScorecardMetrics(policy, map[string]float64{"return_pct": 12}, "out_of_sample"); !passed {
		t.Fatal("out-of-sample scorecard failed")
	}
}
