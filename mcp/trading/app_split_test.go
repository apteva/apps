package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func tradingTool(t *testing.T, name string) sdk.Tool {
	t.Helper()
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("missing tool %s", name)
	return sdk.Tool{}
}
func TestTradingAppOwnsExecutionWithoutResearchSurface(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		if isResearchTool(tool.Name) && tool.Exposure != sdk.ToolExposureAppOnly {
			t.Errorf("research tool exposed in Trading: %s", tool.Name)
		}
	}
	for _, route := range (&App{}).HTTPRoutes() {
		if strings.HasPrefix(route.Pattern, "/strategies") || strings.HasPrefix(route.Pattern, "/backtests") || strings.HasPrefix(route.Pattern, "/validations") {
			t.Errorf("research route exposed: %s", route.Pattern)
		}
	}
	for _, name := range []string{"order_place", "order_cancel", "portfolio_get", "account_summary", "positions_list"} {
		tradingTool(t, name)
	}
}
func TestTradingAppOrderAttributionAndRetryIsolation(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Agent portfolio", []string{"crypto"})
	tool := tradingTool(t, "order_place")
	args := map[string]any{"portfolio_id": id, "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": 0.001, "rationale": "A small paper trade testing the separate execution boundary.", "idempotency_key": "same-intent", "decision_ref": "decision-1", "external_strategy_id": 123, "external_strategy_version": 2}
	caller := sdk.WithCaller(context.Background(), &sdk.Caller{AppName: "strategies", AppInstallID: 10, ProjectID: "test-proj"})
	first, err := tool.HandlerCtx(caller, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.HandlerCtx(caller, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	oid := first.(map[string]any)["order_id"].(string)
	if second.(map[string]any)["order_id"] != oid {
		t.Fatal("retry duplicated an order")
	}
	order, err := dbGetOrder(ctx.AppDB(), "test-proj", oid)
	if err != nil {
		t.Fatal(err)
	}
	if order.OriginApp != "strategies" || order.OriginInstallID != 10 || order.ExternalStrategyID != 123 || order.ExternalStrategyVersion != 2 || order.DecisionRef != "decision-1" {
		t.Fatalf("missing order attribution: %#v", order)
	}
	other, err := tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{AppName: "strategies", AppInstallID: 20, ProjectID: "test-proj"}), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if other.(map[string]any)["order_id"] == oid {
		t.Fatal("different app installation reused another installation's order")
	}
	args["external_strategy_version"] = 3
	if _, err = tool.HandlerCtx(caller, ctx, args); err == nil {
		t.Fatal("retry changed the order's decision provenance")
	}
	if err = dbSetPortfolioStatus(ctx.AppDB(), id, "paused"); err != nil {
		t.Fatal(err)
	}
	args["idempotency_key"] = "new-intent"
	rejected, err := tool.HandlerCtx(caller, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.(map[string]any)["status"] != "rejected" {
		t.Fatal("integration bypassed portfolio pause")
	}
}
func TestTradingAgentCannotForgeAppAttribution(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Direct agents", []string{"crypto"})
	result, err := tradingTool(t, "order_place").HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7, ProjectID: "test-proj"}), ctx, map[string]any{"portfolio_id": id, "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": .001, "rationale": "Direct agent order without any saved strategy or strategy installation.", "_origin_app": "strategies", "_origin_install_id": 99})
	if err != nil {
		t.Fatal(err)
	}
	order, err := dbGetOrder(ctx.AppDB(), "test-proj", result.(map[string]any)["order_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if order.OriginApp != "" || order.OriginInstallID != 0 {
		t.Fatal("agent forged application provenance")
	}
}
func TestTradingResearchExportIsProjectScopedAndAppOnly(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Legacy portfolio", []string{"crypto"})
	sid := mustCreateFixedStrategy(t, ctx, "Saved research", "BTC-USD", .1)
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{ProjectID: "test-proj", PortfolioID: id, StrategyID: sid, StrategyVersion: 1, ControlMode: "strategy"}); err != nil {
		t.Fatal(err)
	}
	_, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "other-project", Name: "Private", Definition: map[string]any{}, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := exportResearchSnapshot(ctx, map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tables["strategies"]) != 1 || len(snap.Tables["strategy_versions"]) != 1 || len(snap.Tables["portfolio_strategy_assignments"]) != 1 {
		t.Fatalf("incomplete or cross-project export: %#v", snap.Tables)
	}
	if _, err = exportResearchSnapshot(ctx, map[string]any{"portfolio_id": id}, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"portfolio_research_snapshot", "research_legacy_export", "market_bars_capture"} {
		tool := tradingTool(t, name)
		if tool.Exposure != sdk.ToolExposureAppOnly {
			t.Fatal("internal tool exposed")
		}
		if _, err = tool.HandlerCtx(context.Background(), ctx, map[string]any{"portfolio_id": id}); err == nil {
			t.Fatal("accepted an unauthenticated app caller")
		}
	}
}
