package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// newHardeningCtx backs a scripted platform with the fake Trading for every
// call the script does not handle itself.
func newHardeningCtx(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	if p, ok := platform.(*eventAgentPlatform); ok && p.PlatformClient == nil {
		p.PlatformClient = newFixtureTrading(t)
	}
	ctx := newTestCtxWith(t, platform)
	globalEngine.platform = platform
	return ctx
}

func (p *eventAgentPlatform) fixture() *fixtureTrading { return p.PlatformClient.(*fixtureTrading) }
