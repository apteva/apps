package main

import (
	"context"
	_ "embed"
	"errors"
	sdk "github.com/apteva/app-sdk"
)

//go:embed apteva.yaml
var manifest string

type App struct{}

func (*App) Manifest() sdk.Manifest {
	m, e := sdk.ParseManifest([]byte(manifest))
	if e != nil {
		panic(e)
	}
	return *m
}
func (*App) OnMount(*sdk.AppCtx) error   { return nil }
func (*App) OnUnmount(*sdk.AppCtx) error { return nil }
func (*App) MCPTools() []sdk.Tool {
	return []sdk.Tool{{Name: "publish", Description: "Publish the fixed photography Pro signup. Repeated calls deliberately reuse the same event ID to test deduplication.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, HandlerCtx: func(ctx context.Context, a *sdk.AppCtx, args map[string]any) (any, error) {
		api := a.EventBusAPI()
		if api == nil {
			return nil, errors.New("event bus unavailable")
		}
		err := api.PublishAppEvent(a.CurrentProject(), "tier3-signup-1", "customer.signed_up", map[string]any{"page": "photography", "plan": "pro"})
		return map[string]any{"published": err == nil, "event_id": "tier3-signup-1"}, err
	}}}
}
func main()                                    { sdk.Run(&App{}) }
func (*App) HTTPRoutes() []sdk.Route           { return nil }
func (*App) Channels() []sdk.ChannelFactory    { return nil }
func (*App) Workers() []sdk.Worker             { return nil }
func (*App) EventHandlers() []sdk.EventHandler { return nil }
