package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// The installer reads apteva.yaml; the running sidecar serves this same manifest.
//
//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	ctx    *sdk.AppCtx
	cancel context.CancelFunc
	mu     sync.RWMutex
	events []sdk.TelemetryStreamEvent
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic(err)
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	a.ctx = ctx
	if _, err := sdk.ListAgentsVia(ctx.PlatformAPI(), ctx.CurrentProject()); err != nil {
		return err
	}
	if ctx.RuntimeAPI() == nil {
		return errors.New("runtime API unavailable")
	}
	if tc, ok := ctx.PlatformAPI().(sdk.TelemetryClient); ok {
		streamCtx, cancel := context.WithCancel(context.Background())
		a.cancel = cancel
		go a.collect(streamCtx, tc)
	}
	ctx.Logger().Info("Agent Worlds mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.cancel != nil {
		a.cancel()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) collect(ctx context.Context, tc sdk.TelemetryClient) {
	sub := sdk.TelemetrySubscription{Events: []string{
		"thread.spawn", "thread.done", "thread.message", "thread.renamed",
		"tool.call", "tool.result", "tool.before", "tool.after",
		"llm.start", "llm.done", "llm.error", "iteration.done", "event.received",
		"execution.waiting", "execution.released", "execution.cancelled",
	}}
	ch, err := tc.SubscribeTelemetry(ctx, sub)
	if err != nil {
		a.ctx.Logger().Warn("Agent Worlds telemetry unavailable", "error", err)
		return
	}
	for ev := range ch {
		a.mu.Lock()
		a.events = append(a.events, ev)
		if len(a.events) > 600 {
			a.events = append([]sdk.TelemetryStreamEvent(nil), a.events[len(a.events)-600:]...)
		}
		a.mu.Unlock()
	}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/sources", Handler: a.handleSources},
		{Pattern: "/api/scene", Handler: a.handleScene},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "worlds_sources", Description: "List live agent visualisation sources.", InputSchema: objectSchema(nil), Handler: func(_ *sdk.AppCtx, _ map[string]any) (any, error) { return a.sources() }},
		{Name: "worlds_scene", Description: "Read a sanitized agent scene for one source.", InputSchema: objectSchema(map[string]any{"source": map[string]any{"type": "string"}}), Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			source, _ := args["source"].(string)
			return a.scene(source)
		}},
	}
}

func objectSchema(properties map[string]any) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": properties}
}

func (a *App) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	sources, err := a.sources()
	respond(w, sources, err)
}

func (a *App) handleScene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	scene, err := a.scene(r.URL.Query().Get("source"))
	respond(w, scene, err)
}

func respond(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func main() {
	sdk.Run(&App{})
}
