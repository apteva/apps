package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func newHardeningCtx(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	globalCtx = ctx
	globalEngine = &engine{db: ctx.AppDB(), provider: newMockProvider(), logger: ctx.Logger(), platform: platform}
	for _, mark := range globalEngine.provider.Universe() {
		if err := dbUpsertMark(ctx.AppDB(), mark); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}
