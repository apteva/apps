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

// burst is the fixed activity: one new lead, a qualified lead below the bar,
// a qualified lead above it and a won deal. Event IDs are fixed so repeated
// calls are deduplicated by the bus.
var burst = []struct {
	id, topic string
	data      map[string]any
}{
	{"tier3-lead-created-1", "lead.created", map[string]any{"lead_email": "ana@example.test", "company": "Northwind"}},
	{"tier3-lead-qualified-low", "lead.qualified", map[string]any{"lead_email": "bob@example.test", "score": 41, "company": "Contoso"}},
	{"tier3-lead-qualified-high", "lead.qualified", map[string]any{"lead_email": "ana@example.test", "score": 92, "company": "Northwind"}},
	{"tier3-deal-won-1", "deal.won", map[string]any{"customer_email": "carl@example.test", "amount": 1200}},
}

func (*App) MCPTools() []sdk.Tool {
	return []sdk.Tool{{Name: "simulate", Description: "Simulate a burst of sales pipeline activity (four events). Repeated calls reuse the same event IDs.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, HandlerCtx: func(ctx context.Context, a *sdk.AppCtx, args map[string]any) (any, error) {
		api := a.EventBusAPI()
		if api == nil {
			return nil, errors.New("event bus unavailable")
		}
		for _, ev := range burst {
			if err := api.PublishAppEvent(a.CurrentProject(), ev.id, ev.topic, ev.data); err != nil {
				return nil, err
			}
		}
		return map[string]any{"published": len(burst)}, nil
	}}}
}
func main()                                    { sdk.Run(&App{}) }
func (*App) HTTPRoutes() []sdk.Route           { return nil }
func (*App) Channels() []sdk.ChannelFactory    { return nil }
func (*App) Workers() []sdk.Worker             { return nil }
func (*App) EventHandlers() []sdk.EventHandler { return nil }
