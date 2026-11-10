package main

import (
	"errors"
	"fmt"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

type pagedTradingFixture struct {
	tk.BasePlatformClient
	call func(map[string]any, any) error
}

func (p *pagedTradingFixture) CallAppResult(app, tool string, args map[string]any, out any) error {
	if app != "trading" || tool != "portfolio_research_snapshot" {
		return fmt.Errorf("unexpected call %s/%s", app, tool)
	}
	return p.call(args, out)
}

func TestTradingObservationAppliesOnlyACompleteRevision(t *testing.T) {
	platform := &pagedTradingFixture{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	globalCtx = ctx
	db := ctx.AppDB()
	id := mustCreatePortfolio(t, ctx, "Observed", []string{"equity"})
	if _, err := db.Exec(`INSERT INTO positions(project_id,portfolio_id,symbol,asset_class,qty,avg_cost)
		VALUES ('test-proj',?,'OLD','equity',1,100)`, id); err != nil {
		t.Fatal(err)
	}
	assigned := mustCreateFixedStrategy(t, ctx, "Assigned universe", "MSFT", .1)
	if _, err := dbAssignStrategy(db, &StrategyAssignment{ProjectID: "test-proj", PortfolioID: id, StrategyID: assigned, StrategyVersion: 1, ControlMode: "strategy"}); err != nil {
		t.Fatal(err)
	}
	requested := mustCreateFixedStrategy(t, ctx, "Requested universe", "NVDA", .1)
	request := map[string]any{"strategy_id": requested, "symbol": "AAPL"}

	var failure string
	platform.call = func(args map[string]any, out any) error {
		page := intArg(args, "page", 1)
		response := map[string]any{"version": 3, "source_install_id": 11, "project_id": "test-proj", "portfolio_id": id, "revision": "revision-a", "page": page}
		switch page {
		case 1:
			symbols, _ := args["symbols"].([]string)
			for _, want := range []string{"AAPL", "MSFT", "NVDA"} {
				if !contains(symbols, want) {
					t.Fatalf("scope omitted %s: %v", want, args)
				}
			}
			if args["paged"] != true {
				t.Fatal("first request did not negotiate pagination")
			}
			// The position appears before its security to check dependency
			// ordering across pages, not just within a page.
			response["tables"] = map[string]any{
				"portfolios": []map[string]any{{"id": id, "project_id": "test-proj", "name": "Observed", "starting_cash": 100000, "cash": 80000}},
				"positions":  []map[string]any{{"portfolio_id": id, "project_id": "test-proj", "symbol": "AAPL", "security_id": "new-security", "asset_class": "equity", "qty": 2, "avg_cost": 100}},
			}
		case 2:
			if args["revision"] != "revision-a" || int64Arg(args, "portfolio_id", 0) != id {
				t.Fatalf("unscoped continuation: %v", args)
			}
			security := "new-security"
			switch failure {
			case "missing":
				return errors.New("expired revision")
			case "mixed":
				response["revision"] = "revision-b"
			case "repeated":
				response["page"] = 1
			case "invalid row":
				security = "wrong-security"
			}
			response["done"] = true
			response["tables"] = map[string]any{"securities": []map[string]any{{"id": security, "asset_class": "equity", "source": "fixture"}}}
		default:
			t.Fatalf("unexpected page %d", page)
		}
		return decodeResearchValue(response, out)
	}

	for _, problem := range []string{"missing", "mixed", "repeated", "invalid row"} {
		failure = problem
		if err := syncTradingPortfolio(ctx, "test-proj", id, request); err == nil {
			t.Fatalf("accepted %s continuation", problem)
		}
		pf, err := dbGetPortfolio(db, "test-proj", id)
		if err != nil || pf.Cash != 100000 {
			t.Fatalf("invalid page changed cash: %+v, %v", pf, err)
		}
		positions, err := dbListPositions(db, id)
		if err != nil || len(positions) != 1 || positions[0].Symbol != "OLD" {
			t.Fatalf("invalid page changed holdings: %+v, %v", positions, err)
		}
		if _, err := db.Exec(`UPDATE trading_observation_failures SET retry_at=0 WHERE portfolio_id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	failure = ""
	if err := syncTradingPortfolio(ctx, "test-proj", id, request); err != nil {
		t.Fatal(err)
	}
	positions, err := dbListPositions(db, id)
	if err != nil || len(positions) != 1 || positions[0].Symbol != "AAPL" || positions[0].SecurityID != "new-security" {
		t.Fatalf("complete observation not installed: %+v, %v", positions, err)
	}
	pf, err := dbGetPortfolio(db, "test-proj", id)
	if err != nil || pf.Cash != 80000 {
		t.Fatalf("cash from completed observation not installed: %+v, %v", pf, err)
	}
}
