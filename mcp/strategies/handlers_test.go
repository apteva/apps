package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func newTestCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	platform := &fixtureTrading{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_INSTALL_ID", "10"), tk.WithPlatform(platform))
	platform.ctx = ctx
	globalCtx = ctx

	globalEngine = &engine{
		db:       ctx.AppDB(),
		provider: newMockProvider(),
		logger:   ctx.Logger(),
	}
	for _, m := range globalEngine.provider.Universe() {
		_ = dbUpsertMark(ctx.AppDB(), m)
	}
	return ctx
}

func mustCreatePortfolio(t *testing.T, ctx *sdk.AppCtx, name string, classes []string) int64 {
	t.Helper()
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
		ProjectID: "test-proj", Name: name, AllowedClasses: classes,
		StartingCash: 100_000,
	})
	if err != nil {
		t.Fatalf("create portfolio: %v", err)
	}
	return id
}
