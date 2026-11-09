package main

import (
	"context"
	"encoding/json"
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

func TestPaperDeskExpandedUniverseExitsOnlyApprovedFills(t *testing.T) {
	for _, tc := range []struct {
		symbol, class, outcome, trigger string
		partial                         bool
	}{
		{symbol: "AAPL", class: "equity", trigger: "stop_loss"},
		{symbol: "AAPL", class: "equity", trigger: "take_profit"},
		{symbol: "AAPL", class: "equity", trigger: "time_exit"},
		{symbol: "VOO", class: "etf", trigger: "take_profit", partial: true},
		{symbol: "POLY:test-event", class: "polymarket", outcome: "no", trigger: "stop_loss"},
		{symbol: "POLY:test-event", class: "polymarket", outcome: "no", trigger: "take_profit"},
		{symbol: "POLY:test-event", class: "polymarket", outcome: "no", trigger: "time_exit", partial: true},
	} {
		t.Run(tc.symbol+"/"+tc.trigger, func(t *testing.T) {
			ctx := newTestCtx(t)
			db := ctx.AppDB()
			pf := mustCreatePortfolio(t, ctx, "Expanded desk", []string{tc.class})
			app := &App{}
			if _, err := app.toolPaperConfigure(ctx, map[string]any{"portfolio_id": pf, "manager_id": 11, "reviewer_id": 12, "executor_id": 13}); err != nil {
				t.Fatal(err)
			}
			profile := defaultVenueProfile("simulation", tc.class)
			profile.SessionPolicy = "continuous"
			if err := dbUpsertVenueProfile(db, &profile); err != nil {
				t.Fatal(err)
			}
			entryPrice, stop, target, step := 100.0, 98.0, 104.0, 1.0
			if tc.outcome != "" {
				entryPrice, stop, target, step = .30, .25, .35, .01
			}
			setPrice := func(price float64) {
				t.Helper()
				now := time.Now().UTC()
				m := &Mark{Symbol: tc.symbol, AssetClass: tc.class, Source: "fixture", Price: price, MarkedAt: now.Format(time.RFC3339Nano), Instrument: defaultInstrument(tc.symbol, tc.class, "fixture", now)}
				if tc.outcome != "" {
					m.Price, m.NoPrice = 1-price, ptr(price)
					m.BidPrice, m.AskPrice = ptr(m.Price-.01), ptr(m.Price+.01)
					m.Instrument.QuoteCurrency = "USDC"
					m.Instrument.ExpiresAt = now.Add(48 * time.Hour).Format(time.RFC3339)
				}
				if err := dbUpsertMark(db, m); err != nil {
					t.Fatal(err)
				}
			}
			setPrice(entryPrice)
			outcome := strings.ToUpper(tc.outcome)
			if err := dbInsertPositionRaw(db, "test-proj", pf, tc.symbol, tc.class, outcome, 3, entryPrice); err != nil {
				t.Fatal(err)
			}
			if tc.outcome != "" {
				if err := dbInsertPositionRaw(db, "test-proj", pf, tc.symbol, tc.class, "YES", 7, 1-entryPrice); err != nil {
					t.Fatal(err)
				}
			}
			args := paperTestArgs(pf, "expanded-exit")
			args["symbol"], args["stop_loss"], args["take_profit"], args["max_holding_minutes"] = tc.symbol, stop, target, 1
			if tc.outcome != "" {
				if _, err := app.toolPaperPropose(paperCall(11), ctx, args); err == nil || !strings.Contains(err.Error(), "explicit yes or no") {
					t.Fatalf("proposal without outcome: %v", err)
				}
				args["outcome"] = tc.outcome
			}
			proposal, err := app.toolPaperPropose(paperCall(11), ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			d := proposal.(*paperDecision)
			if d.Packet.ReferencePrice != entryPrice || d.Packet.Outcome != tc.outcome {
				t.Fatalf("incorrect reviewed leg: %+v", d.Packet)
			}
			if _, err = app.toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(d)); err == nil {
				t.Fatal("unapproved submission succeeded")
			}
			raw, err := app.toolOrderPlace(ctx, paperEntryArgs(pf, d.Packet))
			if err != nil || raw.(map[string]any)["code"] != "desk_approval_required" {
				t.Fatalf("raw order was not rejected: %v, %v", raw, err)
			}
			d = paperApproved(t, ctx, pf, args)
			accepted, err := app.toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(d))
			if err != nil {
				t.Fatal(err)
			}
			entryID, _ := accepted.(map[string]any)["order_id"].(string)
			getOrder := func(id string) *Order {
				t.Helper()
				o, err := dbGetOrder(db, "test-proj", id)
				if err != nil {
					t.Fatalf("read order %q: %v", id, err)
				}
				return o
			}
			entry := getOrder(entryID)
			filledQty := entry.Qty
			if tc.partial {
				filledQty = 2
				paperTestRecordPartialFill(t, ctx, entry, filledQty, entryPrice)
			} else if err = tryFill(globalEngine, entry); err != nil {
				t.Fatal(err)
			}
			entry = getOrder(entryID)
			if entry.FilledQty != filledQty || entry.AssetClass != tc.class || entry.Outcome != outcome || math.Abs(entry.AvgFillPrice/entryPrice-1) > .005 {
				t.Fatalf("incorrect entry fill: %+v", entry)
			}
			loadPlan := func() *paperDecision {
				t.Helper()
				plan, err := loadPaperDecision(db, pf, d.Packet.DecisionID)
				if err != nil {
					t.Fatal(err)
				}
				return plan
			}
			tick := func() {
				t.Helper()
				if err := paperDeskTick(context.Background(), ctx); err != nil {
					t.Fatal(err)
				}
			}
			if tc.trigger == "time_exit" {
				if _, err := db.Exec(`UPDATE fills SET filled_at=? WHERE order_id=?`, time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano), entryID); err != nil {
					t.Fatal(err)
				}
			} else {
				tick()
				if plan := loadPlan(); plan.ExitOrderID != "" || plan.Status != "active" {
					t.Fatalf("unexpected exit before trigger: %+v", plan)
				}
				if tc.trigger == "stop_loss" {
					setPrice(stop - step)
				} else {
					setPrice(target + step)
				}
			}
			if tc.symbol == "AAPL" && tc.trigger == "stop_loss" {
				profile.Status = "maintenance"
				if err := dbUpsertVenueProfile(db, &profile); err != nil {
					t.Fatal(err)
				}
				tick()
				if plan := loadPlan(); plan.ExitReason != tc.trigger || plan.ExitOrderID != "" || plan.LastError == "" {
					t.Fatalf("blocked stop was not persisted: %+v", plan)
				}
				profile.Status = "active"
				if err := dbUpsertVenueProfile(db, &profile); err != nil {
					t.Fatal(err)
				}
				globalEngine = &engine{db: db, provider: newMockProvider(), logger: ctx.Logger()}
				setPrice(entryPrice)
			}
			tick()
			plan := loadPlan()
			if plan.ExitReason != tc.trigger || plan.ExitOrderID == "" {
				t.Fatalf("exit was not scheduled: %+v", plan)
			}
			exit := getOrder(plan.ExitOrderID)
			if exit.Side != "sell" || exit.Qty != filledQty || exit.Outcome != outcome || (tc.partial && getOrder(entryID).Status != "cancelled") {
				t.Fatalf("exit did not match the filled entry: %+v", exit)
			}
			if tc.partial {
				paperTestRecordPartialFill(t, ctx, exit, 1, entryPrice)
			}
			// Start a new engine and run the exit worker again with the persisted orders.
			globalEngine = &engine{db: db, provider: newMockProvider(), logger: ctx.Logger()}
			tick()
			if err := tryFill(globalEngine, getOrder(exit.ID)); err != nil {
				t.Fatal(err)
			}
			tick()
			tick()
			orders, err := dbListOrders(db, pf, "all", 100)
			if plan := loadPlan(); err != nil || len(orders) != 2 || plan.Status != "closed" || plan.ExitOrderID != exit.ID {
				t.Fatalf("restart did not close exactly one exit: plan=%+v, orders=%d, err=%v", plan, len(orders), err)
			}
			if exit := getOrder(exit.ID); exit.Status != "filled" || exit.FilledQty != filledQty {
				t.Fatalf("incorrect exit fill: %+v", exit)
			}
			assertHeld := func(leg string, want float64) {
				t.Helper()
				pos, err := dbGetPosition(db, pf, tc.symbol, leg)
				if err != nil || pos == nil || pos.Qty != want {
					t.Fatalf("unrelated %q holdings changed: %+v, err=%v", leg, pos, err)
				}
			}
			assertHeld(outcome, 3)
			if tc.outcome != "" {
				assertHeld("YES", 7)
			}
		})
	}
}

func paperTestRecordPartialFill(t *testing.T, ctx *sdk.AppCtx, order *Order, qty, price float64) {
	t.Helper()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := dbInsertFill(tx, "test-proj", order.ID, order.PortfolioID, qty, price, 0); err != nil {
		t.Fatal(err)
	}
	if err := dbApplyFill(tx, order.PortfolioID, "test-proj", order, qty, price); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE orders SET filled_qty=?,avg_fill_price=? WHERE id=?`, qty, price, order.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
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

func TestPaperDeskDiscoversResearchOutsideEntryPolicyAndAdmitsExpandedCrypto(t *testing.T) {
	ctx, pfID := paperTest(t)
	app := &App{}
	pf, err := dbGetPortfolio(ctx.AppDB(), "test-proj", pfID)
	if err != nil {
		t.Fatal(err)
	}
	policy := defaultUniversePolicy(pf)
	policy.SelectionMode = "symbol_allowlist"
	policy.IncludeSymbols = []string{"BTC-USD"}
	if _, err := dbUpsertPortfolioUniversePolicy(ctx.AppDB(), pf, *policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for symbol, class := range map[string]string{"SOL-USD": "crypto", "POLY:test": "polymarket"} {
		mark := &Mark{
			Symbol: symbol, AssetClass: class, Price: 100, Source: "fixture",
			MarkedAt: now.Format(time.RFC3339Nano), Instrument: defaultInstrument(symbol, class, "fixture", now),
		}
		if class == "polymarket" {
			mark.Price, mark.NoPrice = 0.45, ptr(0.55)
		}
		if err := dbUpsertMark(ctx.AppDB(), mark); err != nil {
			t.Fatal(err)
		}
	}
	discovery, err := app.toolPaperUniverse(ctx, map[string]any{"portfolio_id": pfID, "asset_class": "polymarket"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(discovery)
	if err != nil || !strings.Contains(string(raw), "POLY:test") || !strings.Contains(string(raw), "not allowed by this portfolio") {
		t.Fatalf("prediction-market discovery = %s, %v", raw, err)
	}
	observation := map[string]any{"portfolio_id": pfID, "symbol": "POLY:test", "observation_id": "event-1", "setup": "event-research", "verdict": "hold", "reason": "Resolution rules are under independent review."}
	if _, err := app.toolPaperObserve(ctx, observation); err != nil {
		t.Fatal(err)
	}
	proposal := paperTestArgs(pfID, "broader-desk")
	proposal["symbol"] = "SOL-USD"
	if _, err := app.toolPaperPropose(paperCall(11), ctx, proposal); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("narrow policy should reject SOL: %v", err)
	}
	policy.SelectionMode, policy.IncludeSymbols = "all_allowed_classes", nil
	if _, err := dbUpsertPortfolioUniversePolicy(ctx.AppDB(), pf, *policy); err != nil {
		t.Fatal(err)
	}
	decision := paperApproved(t, ctx, pfID, proposal)
	if _, err := app.toolPaperSubmit(paperCall(13), ctx, paperSubmitArgs(decision)); err != nil {
		t.Fatal(err)
	}
	stored, err := loadPaperDecision(ctx.AppDB(), pfID, decision.Packet.DecisionID)
	if err != nil || stored.OrderID == "" {
		t.Fatalf("expanded paper order missing: %v, %v", stored, err)
	}
	proposal["decision_id"] = "overlap"
	if _, err := app.toolPaperPropose(paperCall(11), ctx, proposal); err == nil || !strings.Contains(err.Error(), "already owns") {
		t.Fatalf("overlapping plan should be rejected: %v", err)
	}
}
