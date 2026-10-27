package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func paperTest(t *testing.T) (*sdk.AppCtx, int64) {
	t.Helper()
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Desk", []string{"crypto"})
	_, err := (&App{}).toolPaperConfigure(ctx, map[string]any{"portfolio_id": id, "manager_id": 11, "reviewer_id": 12, "executor_id": 13})
	if err != nil {
		t.Fatal(err)
	}
	paperTestMark(t, ctx, 100)
	return ctx, id
}

func paperTestMark(t *testing.T, ctx *sdk.AppCtx, price float64) {
	t.Helper()
	now := time.Now().UTC()
	m := &Mark{Symbol: "BTC-USD", AssetClass: "crypto", Price: price, Source: "fixture", MarkedAt: now.Format(time.RFC3339Nano), Instrument: defaultInstrument("BTC-USD", "crypto", "fixture", now)}
	if err := dbUpsertMark(ctx.AppDB(), m); err != nil {
		t.Fatal(err)
	}
}

func paperCall(id int64) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: id, ProjectID: "test-proj"})
}

func paperTestArgs(pf int64, id string) map[string]any {
	return map[string]any{"portfolio_id": pf, "decision_id": id, "symbol": "BTC-USD", "qty": 5.0, "stop_loss": 98.0, "take_profit": 104.0, "setup": "bounded-hypothesis-v1", "thesis": "An explicitly unvalidated paper experiment with a small planned loss budget.", "max_holding_minutes": 60}
}

func paperApproved(t *testing.T, ctx *sdk.AppCtx, pf int64, args map[string]any) *paperDecision {
	t.Helper()
	app := &App{}
	if _, err := app.toolPaperPropose(paperCall(11), ctx, args); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolPaperReview(paperCall(12), ctx, map[string]any{"portfolio_id": pf, "decision_id": args["decision_id"], "verdict": "approved", "reason": "Fresh quote and exact bounded terms independently checked; uncertain edge is acceptable for paper learning."})
	if err != nil {
		t.Fatal(err)
	}
	return out.(*paperDecision)
}

func paperSubmitArgs(d *paperDecision) map[string]any {
	return map[string]any{"portfolio_id": d.PortfolioID, "decision_id": d.Packet.DecisionID, "risk_review_ref": d.ReviewRef}
}

func TestPaperDeskRequiresAuthenticatedDistinctActorsAndImmutableApproval(t *testing.T) {
	ctx, pf := paperTest(t)
	app := &App{}
	args := paperTestArgs(pf, "identity")
	for _, actor := range []int64{0, 12, 13} {
		if _, err := app.toolPaperPropose(paperCall(actor), ctx, args); err == nil {
			t.Fatalf("actor %d proposed", actor)
		}
	}
	if _, err := app.toolPaperPropose(context.Background(), ctx, args); err == nil {
		t.Fatal("missing caller proposed")
	}
	if _, err := app.toolPaperPropose(sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 11, ProjectID: "other"}), ctx, args); err == nil {
		t.Fatal("cross-project proposal accepted")
	}
	d := paperApproved(t, ctx, pf, args)
	if d.ReviewerID != 12 || d.ManagerID != 11 || d.ReviewRef == "" || d.PacketHash == "" {
		t.Fatalf("approval=%#v", d)
	}
	if _, err := app.toolPaperPropose(paperCall(11), ctx, args); err != nil {
		t.Fatal("exact proposal retry", err)
	}
	args["qty"] = 4.0
	if _, err := app.toolPaperPropose(paperCall(11), ctx, args); err == nil {
		t.Fatal("immutable proposal changed")
	}
	if _, err := app.toolPaperReview(paperCall(11), ctx, map[string]any{"portfolio_id": pf, "decision_id": "identity", "verdict": "approved", "reason": "I approve my own proposal with no independent actor."}); err == nil {
		t.Fatal("manager self approved")
	}
	submit := paperSubmitArgs(d)
	submit["risk_review_ref"] = "forged"
	if _, err := app.toolPaperSubmit(paperCall(13), ctx, submit); err == nil {
		t.Fatal("forged approval submitted")
	}
	submit = paperSubmitArgs(d)
	submit["qty"] = 4.0
	if _, err := app.toolPaperSubmit(paperCall(13), ctx, submit); err == nil {
		t.Fatal("changed terms submitted")
	}
	if _, err := app.toolPaperSubmit(paperCall(12), ctx, paperSubmitArgs(d)); err == nil {
		t.Fatal("reviewer executed")
	}
	_, err := app.toolOrderPlace(ctx, map[string]any{"portfolio_id": pf, "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": 5.0, "rationale": d.Packet.Thesis, "decision_ref": "identity", "risk_review_ref": d.ReviewRef, "_desk_authorized": true})
	if err != nil {
		t.Fatal(err)
	}
	orders, _ := dbListOrders(ctx.AppDB(), pf, "all", 100)
	if len(orders) != 0 {
		t.Fatal("raw order bypassed paper approval")
	}
}

func TestPaperDeskRejectsOversizeLossBudgetExpiredAndDriftedEntries(t *testing.T) {
	for _, scenario := range []string{"notional", "loss", "expired", "drift"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, pf := paperTest(t)
			app := &App{}
			args := paperTestArgs(pf, scenario)
			if scenario == "notional" {
				args["qty"] = 11.0
			}
			if scenario == "loss" {
				args["stop_loss"] = 90.0
			}
			if scenario == "notional" || scenario == "loss" {
				if _, err := app.toolPaperPropose(paperCall(11), ctx, args); err == nil {
					t.Fatal("unbounded experiment proposed")
				}
				return
			}
			d := paperApproved(t, ctx, pf, args)
			if scenario == "expired" {
				ctx.AppDB().Exec(`UPDATE paper_decisions SET approval_expires_at=? WHERE portfolio_id=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), pf)
			} else {
				paperTestMark(t, ctx, 101)
			}
			if _, err := app.toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(d)); err == nil {
				t.Fatal("expired/drifted entry accepted")
			}
			orders, _ := dbListOrders(ctx.AppDB(), pf, "all", 100)
			if len(orders) != 0 {
				t.Fatal("invalid entry placed")
			}
		})
	}
}

func TestPaperDeskSubmitIsConcurrentIdempotentAndRecoversLinkingCrash(t *testing.T) {
	ctx, pf := paperTest(t)
	d := paperApproved(t, ctx, pf, paperTestArgs(pf, "retry"))
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := (&App{}).toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(d))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	orders, _ := dbListOrders(ctx.AppDB(), pf, "all", 100)
	if len(orders) != 1 {
		t.Fatalf("duplicate orders: %d", len(orders))
	}
	ctx.AppDB().Exec(`UPDATE paper_decisions SET order_id='',status='approved',approval_expires_at=? WHERE portfolio_id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), pf)
	out, err := (&App{}).toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(d))
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["order_id"] != orders[0].ID {
		t.Fatal("crash recovery changed order")
	}
	stored, _ := loadPaperDecision(ctx.AppDB(), pf, "retry")
	if stored.OrderID != orders[0].ID {
		t.Fatal("order link not recovered")
	}
}

func TestPaperDeskDurableExitsAndScorecard(t *testing.T) {
	for _, exitReason := range []string{"time_exit", "stop_loss", "take_profit"} {
		t.Run(exitReason, func(t *testing.T) {
			ctx, pf := paperTest(t)
			args := paperTestArgs(pf, exitReason)
			args["purpose"] = "rehearsal"
			d := paperApproved(t, ctx, pf, args)
			out, err := (&App{}).toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(d))
			if err != nil {
				t.Fatal(err)
			}
			oid := out.(map[string]any)["order_id"].(string)
			entry, _ := dbGetOrder(ctx.AppDB(), "test-proj", oid)
			if err = tryFill(globalEngine, entry); err != nil {
				t.Fatal(err)
			}
			// Recreate the engine as on restart: durable plan state survives it.
			globalEngine = &engine{db: ctx.AppDB(), provider: newMockProvider(), logger: ctx.Logger()}
			if err = paperDeskTick(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			switch exitReason {
			case "time_exit":
				ctx.AppDB().Exec(`UPDATE paper_decisions SET exit_at=? WHERE portfolio_id=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), pf)
			case "stop_loss":
				paperTestMark(t, ctx, 97)
			case "take_profit":
				paperTestMark(t, ctx, 105)
			}
			if err = paperDeskTick(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			d, err = loadPaperDecision(ctx.AppDB(), pf, exitReason)
			if err != nil {
				t.Fatal(err)
			}
			if d.ExitOrderID == "" || d.ExitReason != exitReason {
				t.Fatalf("exit not persisted: %#v", d)
			}
			exit, _ := dbGetOrder(ctx.AppDB(), "test-proj", d.ExitOrderID)
			// Recover an exit accepted just before a simulated process failure.
			ctx.AppDB().Exec(`UPDATE paper_decisions SET exit_order_id='',status='active' WHERE portfolio_id=?`, pf)
			if err = paperDeskTick(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			orders, _ := dbListOrders(ctx.AppDB(), pf, "all", 100)
			if len(orders) != 2 {
				t.Fatalf("duplicate exits: %d", len(orders))
			}
			if err = tryFill(globalEngine, exit); err != nil {
				t.Fatal(err)
			}
			if err = paperDeskTick(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			d, _ = loadPaperDecision(ctx.AppDB(), pf, exitReason)
			if d.Status != "closed" {
				t.Fatalf("plan not closed: %#v", d)
			}
			pos, _ := dbGetPosition(ctx.AppDB(), pf, "BTC-USD", "")
			if pos != nil && pos.Qty > 1e-9 {
				t.Fatal("position not flat")
			}
			score, err := (&App{}).toolPaperScorecard(ctx, map[string]any{"portfolio_id": pf})
			if err != nil {
				t.Fatal(err)
			}
			s := score.(map[string]any)
			if s["closed_experiments"] != 0 || s["experiment_net_pnl"] != 0.0 {
				t.Fatalf("rehearsal polluted performance: %#v", s)
			}
			ctx.AppDB().Exec(`UPDATE paper_decisions SET packet_json=json_set(packet_json,'$.purpose','experiment') WHERE portfolio_id=?`, pf)
			score, err = (&App{}).toolPaperScorecard(ctx, map[string]any{"portfolio_id": pf})
			if err != nil {
				t.Fatal(err)
			}
			s = score.(map[string]any)
			actual, _ := dbGetPortfolioAnyProject(ctx.AppDB(), pf)
			if s["closed_experiments"] != 1 || math.Abs(s["experiment_net_pnl"].(float64)-(actual.Cash-actual.StartingCash)) > 1e-6 {
				t.Fatalf("cashflow scorecard mismatch: score=%v cash=%v", s["experiment_net_pnl"], actual.Cash)
			}
		})
	}
}

func TestPaperDeskLimitCancellationAndStaleExit(t *testing.T) {
	ctx, pf := paperTest(t)
	args := paperTestArgs(pf, "cancel")
	args["type"] = "limit"
	args["limit_price"] = 99.0
	args["stop_loss"] = 98.0
	d := paperApproved(t, ctx, pf, args)
	out, err := (&App{}).toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(d))
	if err != nil {
		t.Fatal(err)
	}
	oid := fmt.Sprint(out.(map[string]any)["order_id"])
	o, _ := dbGetOrder(ctx.AppDB(), "test-proj", oid)
	if err = tryFill(globalEngine, o); err != nil {
		t.Fatal(err)
	}
	o, _ = dbGetOrder(ctx.AppDB(), "test-proj", oid)
	if o.Status != "working" {
		t.Fatal("nonmarketable limit unexpectedly filled")
	}
	if _, err = (&App{}).toolPaperCancel(paperCall(13), ctx, map[string]any{"portfolio_id": pf, "decision_id": "cancel", "reason": "rehearsal cancellation"}); err != nil {
		t.Fatal(err)
	}
	if err = paperDeskTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	d, _ = loadPaperDecision(ctx.AppDB(), pf, "cancel")
	if d.Status != "cancelled" {
		t.Fatalf("cancel state=%s", d.Status)
	}
}

func TestPaperDeskContextExcludesFormingBarAndComputesFacts(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	var bars []Bar
	start := now.Truncate(time.Hour).Add(-60 * time.Hour)
	for i := 0; i <= 60; i++ {
		p := float64(100 + i)
		bars = append(bars, Bar{T: start.Add(time.Duration(i) * time.Hour).Unix(), O: p, H: p + 1, L: p - 1, C: p, V: 10})
	}
	got, err := completedPaperContext(bars, now)
	if err != nil {
		t.Fatal(err)
	}
	if got["bar_seconds"] != int64(3600) || got["completed_bars"] != 60 || got["forming_bars_excluded"] != 1 || got["sma20"] != 149.5 || got["sma50"] != 134.5 {
		t.Fatalf("context=%#v", got)
	}
}

func TestPaperDeskHealthValuesOpenPositions(t *testing.T) {
	ctx, pf := paperTest(t)
	app := &App{}
	d := paperApproved(t, ctx, pf, paperTestArgs(pf, "health-valuation"))
	out, err := app.toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(d))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := dbGetOrder(ctx.AppDB(), "test-proj", fmt.Sprint(out.(map[string]any)["order_id"]))
	if err != nil {
		t.Fatal(err)
	}
	if err = tryFill(globalEngine, entry); err != nil {
		t.Fatal(err)
	}
	paperTestMark(t, ctx, 103)
	out, err = app.toolPaperHealth(ctx, map[string]any{"portfolio_id": pf})
	if err != nil {
		t.Fatal(err)
	}
	health := out.(map[string]any)
	positions := health["positions"].([]*Position)
	if len(positions) != 1 || positions[0].MarketPrice != 103 || positions[0].MarketValue != 515 || positions[0].UnrealizedPnL <= 0 {
		t.Fatalf("health lost position valuation: %#v", positions)
	}
	if math.Abs(health["equity"].(float64)-(health["cash"].(float64)+515)) > 1e-6 {
		t.Fatalf("health equity does not reconcile: %#v", health)
	}
}

func TestPaperDeskEveryPublicToolHasPermissionBoundary(t *testing.T) {
	m := (&App{}).Manifest()
	definitions := map[string]sdk.Tool{}
	for _, tool := range (&App{}).MCPTools() {
		definitions[tool.Name] = tool
	}
	for _, tool := range m.Provides.MCPTools {
		if tool.Requires == "" {
			t.Errorf("tool %s lacks permission boundary", tool.Name)
		}
		if strings.Contains(tool.ResourceFrom, "{arg.portfolio_id}") {
			definition, ok := definitions[tool.Name]
			if !ok {
				t.Errorf("permission boundary refers to unknown tool %s", tool.Name)
				continue
			}
			properties, _ := definition.InputSchema["properties"].(map[string]any)
			if _, ok := properties["portfolio_id"]; !ok {
				t.Errorf("tool %s requires portfolio_id for authorization but does not accept it", tool.Name)
			}
		}
	}
}
