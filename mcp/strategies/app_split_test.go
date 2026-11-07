package main

import (
	"context"
	"encoding/json"
	"errors"
	tk "github.com/apteva/app-sdk/testkit"
	"reflect"
	"testing"
)

func TestStrategiesAppHasTradingDependencyAndNoExecutionSurface(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if len(m.Requires.Apps) != 1 || m.Requires.Apps[0].Name != "trading" || m.Requires.Apps[0].Optional {
		t.Fatal("Trading dependency is not required")
	}
	for _, tool := range app.MCPTools() {
		switch tool.Name {
		case "order_place", "order_cancel", "portfolio_create", "portfolio_risk_update", "portfolio_arm_live", "brokers_list":
			t.Fatalf("execution tool exposed by Strategies: %s", tool.Name)
		}
	}
	for _, permission := range m.Requires.Permissions {
		if permission == "net.egress" || permission == "platform.connections.execute" || permission == "platform.connections.read_credentials" {
			t.Fatalf("Strategies can access brokers directly: %s", permission)
		}
	}
	ctx := newTestCtx(t)
	for _, tool := range app.MCPTools() {
		if tool.Name == "replay_order_place" || tool.Name == "replay_order_cancel" {
			if _, err := tool.HandlerCtx(context.Background(), ctx, map[string]any{}); err == nil {
				t.Fatal("replay tool accepted a non-replay call")
			}
		}
	}
}

type missingTrading struct {
	tk.BasePlatformClient
	orders int
}

func (p *missingTrading) CallAppResult(app, tool string, args map[string]any, out any) error {
	if tool == "order_place" {
		p.orders++
	}
	return errors.New("Trading unavailable")
}
func TestStrategySubmissionFailsWhenTradingObservationFails(t *testing.T) {
	platform := &missingTrading{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	globalCtx = ctx
	_, err := (&App{}).toolOrderPlace(ctx, map[string]any{"portfolio_id": 1, "strategy_id": 1})
	if err == nil || platform.orders != 0 {
		t.Fatal("strategy submitted an order without a current portfolio observation")
	}
}

type legacyTradingFixture struct {
	fixtureTrading
	legacy researchSnapshot
}

func (p *legacyTradingFixture) CallAppResult(app, tool string, args map[string]any, out any) error {
	if tool == "research_legacy_export" {
		return decodeResearchValue(p.legacy, out)
	}
	return p.fixtureTrading.CallAppResult(app, tool, args, out)
}
func TestLegacyResearchImportsPausedAndOnlyOnce(t *testing.T) {
	platform := &legacyTradingFixture{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	globalCtx = ctx
	platform.ctx = ctx
	id := mustCreatePortfolio(t, ctx, "Preserved portfolio", []string{"crypto"})
	definition := map[string]any{"universe": []string{"BTC-USD"}, "cadence": "1d", "rules": []any{map[string]any{"allocate": []any{map[string]any{"symbol": "BTC-USD", "weight": .1}}}}}
	raw, _ := json.Marshal(definition)
	platform.legacy = researchSnapshot{Version: 1, SourceInstallID: 11, ProjectID: "test-proj", Tables: map[string][]map[string]any{
		"strategies":                     {{"id": int64(42), "project_id": "test-proj", "name": "Legacy strategy", "definition_json": string(raw), "version": int64(2), "status": "active"}},
		"strategy_versions":              {{"strategy_id": int64(42), "version": int64(2), "definition_json": string(raw)}},
		"portfolio_strategy_assignments": {{"id": int64(8), "project_id": "test-proj", "portfolio_id": id, "strategy_id": int64(42), "strategy_version": int64(2), "control_mode": "strategy", "status": "active"}},
	}}
	result, err := (&App{}).importLegacyResearch(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["status"] != "imported" {
		t.Fatal(result)
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), "test-proj", 42)
	if err != nil || strategy.Version != 2 {
		t.Fatalf("lost saved strategy version: %#v %v", strategy, err)
	}
	var status string
	if err = ctx.AppDB().QueryRow(`SELECT status FROM portfolio_strategy_assignments WHERE id=8`).Scan(&status); err != nil || status != "paused" {
		t.Fatalf("assignment resumed during migration: %s %v", status, err)
	}
	result, err = (&App{}).importLegacyResearch(ctx, map[string]any{})
	if err != nil || result.(map[string]any)["status"] != "already_imported" {
		t.Fatalf("import is not idempotent: %#v %v", result, err)
	}
}

func TestStrategyRetriesKeepThePersistedOrderIntent(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Retry portfolio", []string{"crypto"})
	sid := mustCreateFixedStrategy(t, ctx, "Retry strategy", "BTC-USD", .01)
	args := map[string]any{"portfolio_id": id, "strategy_id": sid, "assignment_id": int64(1), "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": .001, "rationale": "A small allocation testing the exact persisted order intent.", "idempotency_key": "durable-intent"}
	if _, err := (&App{}).toolOrderPlace(ctx, args); err != nil {
		t.Fatal(err)
	}
	args["qty"] = .002
	args["rationale"] = "This changed proposal must not replace an already submitted intent."
	if _, err := (&App{}).toolOrderPlace(ctx, args); err != nil {
		t.Fatal(err)
	}
	platform := ctx.PlatformAPI().(*fixtureTrading)
	if len(platform.orderRequests) != 2 || !reflect.DeepEqual(platform.orderRequests[0], platform.orderRequests[1]) {
		t.Fatalf("retry changed a persisted submission: %#v", platform.orderRequests)
	}
	if _, exists := platform.orderRequests[0]["strategy_id"]; exists {
		t.Fatal("local strategy ID leaked into Trading's strategy namespace")
	}
}
func TestStrategySchedulerDoesNotRunInsideReplay(t *testing.T) {
	platform := &missingTrading{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform), tk.WithEnv("APTEVA_ENVIRONMENT_ID", "replay-test"))
	globalCtx = ctx
	for _, worker := range (&App{}).Workers() {
		if err := worker.Run(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
	}
	if platform.orders != 0 {
		t.Fatal("historical replay ran the normal execution scheduler")
	}
}
