package main

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func appCall(app string, install int64, project string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{AppName: app, AppInstallID: install, ProjectID: project})
}

func callTool(t *testing.T, call context.Context, ctx *sdk.AppCtx, name string, args map[string]any, out any) error {
	t.Helper()
	result, err := tradingTool(t, name).HandlerCtx(call, ctx, args)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
	return nil
}

func TestAppReadToolsAreAppOnly(t *testing.T) {
	for _, name := range []string{"portfolio_state", "portfolio_policy_check", "research_reference", "fills_feed"} {
		if tool := tradingTool(t, name); tool.Exposure != sdk.ToolExposureAppOnly {
			t.Errorf("%s must be app-only", name)
		}
	}
}

func TestPortfolioStateScopedToCallingAppProject(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Automated", []string{"crypto"})
	args := map[string]any{"portfolio_id": id, "symbols": []any{"btc-usd"}}
	if err := callTool(t, sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 3, ProjectID: "test-proj"}), ctx, "portfolio_state", args, nil); err == nil {
		t.Fatal("agent caller read app-only portfolio state")
	}
	if err := callTool(t, appCall("strategies", 10, "other-proj"), ctx, "portfolio_state", map[string]any{"portfolio_id": id}, nil); err == nil {
		t.Fatal("caller read a portfolio outside its project")
	}
	var state PortfolioState
	if err := callTool(t, appCall("strategies", 10, "test-proj"), ctx, "portfolio_state", args, &state); err != nil {
		t.Fatal(err)
	}
	if state.Portfolio == nil || state.Portfolio.ID != id || state.Equity != 100_000 || state.Marks["BTC-USD"] == nil {
		t.Fatalf("incomplete state: %+v", state)
	}
	if _, ok := state.Profiles["BTC-USD"]; !ok {
		t.Fatal("missing venue profile for requested symbol")
	}
}

func TestOrderPlaceIfRevisionRejectsStaleStateAndChains(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Automated", []string{"crypto"})
	call := appCall("strategies", 10, "test-proj")
	var state PortfolioState
	if err := callTool(t, call, ctx, "portfolio_state", map[string]any{"portfolio_id": id, "symbols": []any{"BTC-USD", "ETH-USD"}}, &state); err != nil {
		t.Fatal(err)
	}
	order := func(symbol, key string, revision int64) map[string]any {
		var out map[string]any
		if err := callTool(t, call, ctx, "order_place", map[string]any{"portfolio_id": id, "symbol": symbol, "side": "buy", "type": "market", "qty": 0.01,
			"rationale": "Automated rebalance sized from one portfolio_state read.", "idempotency_key": key, "if_revision": revision}, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := order("BTC-USD", "one", state.Revision)
	if first["status"] != "working" {
		t.Fatalf("first order: %v", first)
	}
	next := int64(first["revision"].(float64))
	if next <= state.Revision {
		t.Fatalf("order insert did not move the revision: %d -> %d", state.Revision, next)
	}
	if stale := order("ETH-USD", "stale", state.Revision); stale["status"] != "rejected" || stale["code"] != "stale_state" {
		t.Fatalf("stale order accepted: %v", stale)
	}
	if chained := order("ETH-USD", "two", next); chained["status"] != "working" {
		t.Fatalf("chained order rejected: %v", chained)
	}
}

func TestFillsFeedReturnsOnlyCallerOrders(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Shared", []string{"crypto"})
	place := func(call context.Context, key string) string {
		var out map[string]any
		if err := callTool(t, call, ctx, "order_place", map[string]any{"portfolio_id": id, "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": 0.01,
			"rationale": "Paper order used to check the fills feed boundary.", "idempotency_key": key, "external_strategy_id": 7, "external_strategy_version": 2}, &out); err != nil {
			t.Fatal(err)
		}
		o, err := dbGetOrder(ctx.AppDB(), "test-proj", out["order_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if err := tryFill(globalEngine, o); err != nil {
			t.Fatal(err)
		}
		return o.ID
	}
	mine := []string{place(appCall("strategies", 10, "test-proj"), "a"), place(appCall("strategies", 10, "test-proj"), "b")}
	place(appCall("other-app", 11, "test-proj"), "c")
	place(sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 4, ProjectID: "test-proj"}), "d")

	var page struct {
		Fills      []FeedFill `json:"fills"`
		NextFillID int64      `json:"next_fill_id"`
		HasMore    bool       `json:"has_more"`
	}
	call := appCall("strategies", 10, "test-proj")
	if err := callTool(t, call, ctx, "fills_feed", map[string]any{"limit": 1}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Fills) != 1 || !page.HasMore || page.Fills[0].OrderID != mine[0] || page.Fills[0].ExternalStrategyID != 7 || page.Fills[0].ExternalStrategyVersion != 2 {
		t.Fatalf("first page: %+v", page)
	}
	if err := callTool(t, call, ctx, "fills_feed", map[string]any{"after_fill_id": page.NextFillID, "limit": 10}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Fills) != 1 || page.HasMore || page.Fills[0].OrderID != mine[1] {
		t.Fatalf("second page leaked other fills: %+v", page)
	}
	if err := callTool(t, appCall("other-app", 11, "test-proj"), ctx, "fills_feed", map[string]any{"include_legacy": true}, nil); err == nil {
		t.Fatal("non-Strategies caller read legacy strategy fills")
	}
	legacyOrder := place(sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 5, ProjectID: "test-proj"}), "legacy")
	if _, err := ctx.AppDB().Exec(`UPDATE orders SET origin_app='',strategy_id=42,strategy_version=3 WHERE id=?`, legacyOrder); err != nil {
		t.Fatal(err)
	}
	if err := callTool(t, call, ctx, "fills_feed", map[string]any{"after_fill_id": page.NextFillID, "include_legacy": true}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Fills) != 1 || page.Fills[0].OrderID != legacyOrder || page.Fills[0].ExternalStrategyID != 42 || page.Fills[0].ExternalStrategyVersion != 3 {
		t.Fatalf("legacy fills: %+v", page)
	}
	if err := callTool(t, appCall("strategies", 10, "other-proj"), ctx, "fills_feed", map[string]any{}, nil); err == nil {
		t.Fatal("fills feed served a caller from another project")
	}
}

func TestPortfolioPolicyCheckUsesTradingUniverse(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Allowlist", []string{"crypto"})
	pf, err := dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbUpsertPortfolioUniversePolicy(ctx.AppDB(), pf, PortfolioUniversePolicy{SelectionMode: "symbol_allowlist", IncludeSymbols: []string{"BTC-USD"}, EnforcementEnabled: true}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Violations map[string]*UniverseViolation `json:"violations"`
	}
	if err := callTool(t, appCall("strategies", 10, "test-proj"), ctx, "portfolio_policy_check", map[string]any{"portfolio_id": id, "symbols": []any{"BTC-USD", "ETH-USD"}}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Violations["BTC-USD"] != nil || out.Violations["ETH-USD"] == nil || out.Violations["ETH-USD"].Code != "universe_not_allowed" {
		t.Fatalf("violations: %+v", out.Violations)
	}
	var state PortfolioState
	if err := callTool(t, appCall("strategies", 10, "test-proj"), ctx, "portfolio_state", map[string]any{"portfolio_id": id, "symbols": []any{"ETH-USD"}}, &state); err != nil {
		t.Fatal(err)
	}
	if state.Violations["ETH-USD"] == nil {
		t.Fatal("portfolio_state omitted the universe violation")
	}
}

func TestResearchReferencePagesInTableOrder(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	for _, day := range []string{"2026-01-05", "2026-01-06", "2026-01-07"} {
		if _, err := db.Exec(`INSERT INTO exchange_sessions(venue,session_date,session_type,open_at,close_at,status,source) VALUES('US_EQUITY',?,'regular',?,?,'open','alpaca')`, day, day+"T14:30:00Z", day+"T21:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO securities(id,asset_class,name,source) VALUES('sec-aapl','equity','Apple','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO security_listings(security_id,venue,symbol,source) VALUES('sec-aapl','XNAS','AAPL','test')`); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Tables      map[string][]map[string]any `json:"tables"`
		SecurityIDs map[string]string           `json:"security_ids"`
		NextCursor  string                      `json:"next_cursor"`
		Status      map[string]any              `json:"status"`
	}
	if err := callTool(t, appCall("strategies", 10, "test-proj"), ctx, "research_reference", map[string]any{"symbols": []any{"AAPL"}, "start": "2026-01-05", "end": "2026-01-06"}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Tables["exchange_sessions"]) != 2 || len(page.Tables["security_listings"]) != 1 || page.SecurityIDs["AAPL"] != "sec-aapl" || page.NextCursor != "" || page.Status == nil {
		t.Fatalf("reference page: %+v", page)
	}
	if err := callTool(t, appCall("strategies", 10, "test-proj"), ctx, "research_reference", map[string]any{"symbols": []any{"AAPL"}, "start": "2026-01-05", "end": "2026-01-06", "cursor": "bad"}, nil); err == nil {
		t.Fatal("accepted an invalid cursor")
	}
}
