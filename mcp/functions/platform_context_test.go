package main

import (
	"context"
	"encoding/json"
	"errors"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// SDK project wrappers require the full optional AppContextClient interface.
// These fixtures only implement decoded single calls; raw/batch calls should
// fail explicitly rather than silently losing cancellation in a test.
type contextPlatformFixture struct{ tk.BasePlatformClient }

func (contextPlatformFixture) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, errors.New("raw context calls not implemented in this fixture")
}

func (contextPlatformFixture) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, errors.New("batch calls not implemented in this fixture")
}

var _ sdk.AppContextClient = (*nestedProtocolPlatform)(nil)
var _ sdk.AppContextClient = (*contextStreamGatePlatform)(nil)
