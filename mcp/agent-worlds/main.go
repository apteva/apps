package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// The installer reads apteva.yaml; the running sidecar serves this same manifest.
//
//go:embed apteva.yaml
var manifestYAML []byte

const requestTimeout = 8 * time.Second

type App struct {
	ctx       *sdk.AppCtx
	catalogMu sync.Mutex
	catalog   map[string]topologyCache
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic(err)
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.TelemetryAPI() == nil {
		return errors.New("telemetry observation API unavailable")
	}
	a.ctx = ctx
	ctx.Logger().Info("Agent Worlds mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/sources", Handler: a.handleSources},
		{Pattern: "/api/scene", Handler: a.handleScene},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "worlds_sources", Description: "List live agent visualisation sources.", InputSchema: objectSchema(nil), Handler: func(app *sdk.AppCtx, _ map[string]any) (any, error) {
			return a.sources(context.Background(), app)
		}},
		{Name: "worlds_scene", Description: "Read a sanitized agent scene for one source.", InputSchema: objectSchema(map[string]any{"source": map[string]any{"type": "string"}}), Handler: func(app *sdk.AppCtx, args map[string]any) (any, error) {
			source, _ := args["source"].(string)
			return a.scene(context.Background(), app, source)
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
	sources, err := a.sources(r.Context(), a.ctx.WithUserSession(r))
	respond(w, sources, err)
}

func (a *App) handleScene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	scene, err := a.scene(viewerContext(r), a.ctx.WithUserSession(r), r.URL.Query().Get("source"))
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

func main() { sdk.Run(&App{}) }

// Cache topology by the viewer as well as project/agent. A browser session must
// never reuse metadata obtained on behalf of a different signed-in user.
type viewerKey struct{}

func viewerContext(r *http.Request) context.Context {
	identity := "install"
	if c, err := r.Cookie("session"); err == nil {
		identity = fmt.Sprintf("%x", sha256.Sum256([]byte(c.Value)))
	}
	return context.WithValue(r.Context(), viewerKey{}, identity)
}
