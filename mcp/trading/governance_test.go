package main

import (
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

func TestReferenceUniverseIsEnforced(t *testing.T) {
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
}
