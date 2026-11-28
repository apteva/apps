package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func newTestCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	return newTestCtxWith(t, newFixtureTrading(t))
}

// newTestCtxWith wires a Strategies AppCtx to a fake Trading. Strategies'
// database holds only Strategies data; account state lives in the fake's.
func newTestCtxWith(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	fake := platform.(tradingFixtureOwner).fixture()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_INSTALL_ID", "10"), tk.WithPlatform(platform))
	fake.ctx = ctx
	globalCtx = ctx
	resetTradingBackoff()
	provider := newMockProvider()
	globalEngine = &engine{db: ctx.AppDB(), provider: provider, logger: ctx.Logger()}
	fake.engine = &engine{db: fake.db, provider: provider, logger: ctx.Logger()}
	for _, m := range provider.Universe() {
		_ = dbUpsertMark(fake.db, m)
	}
	return ctx
}

func mustCreatePortfolio(t *testing.T, ctx *sdk.AppCtx, name string, classes []string) int64 {
	t.Helper()
	id, err := dbCreatePortfolio(tradingDB(ctx), &Portfolio{
		ProjectID: "test-proj", Name: name, AllowedClasses: classes,
		StartingCash: 100_000,
	})
	if err != nil {
		t.Fatalf("create portfolio: %v", err)
	}
	return id
}
