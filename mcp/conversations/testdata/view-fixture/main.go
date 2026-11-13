package main

import (
	_ "embed"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

//go:embed apteva.yaml
var manifest string

type App struct {
	mu     sync.Mutex
	values map[string]int
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifest))
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) OnMount(*sdk.AppCtx) error {
	a.values = map[string]int{"dispatch": 0, "inventory": 0}
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: "GET", Pattern: "/records/", Handler: func(w http.ResponseWriter, r *http.Request) {
			a.mu.Lock()
			defer a.mu.Unlock()
			id := strings.TrimPrefix(r.URL.Path, "/records/")
			value, ok := a.values[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": id, "value": value})
		}},
		{Method: "GET", Pattern: "/frame", Handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><body><button onclick="this.textContent='Frame state retained'">Frame control</button></body></html>`))
		}},
	}
}
func (a *App) view(id string) map[string]any {
	install, _ := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
	return map[string]any{"app": "view-fixture", "name": "preview-card", "resource": sdk.AppResourceRef{InstallID: install, ResourceType: "preview.document", ResourceID: id}, "props": map[string]any{"record_id": id, "title": "Preview " + id}}
}
func (a *App) MCPTools() []sdk.Tool {
	schema := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "enum": []string{"dispatch", "inventory"}}}, "required": []string{"id"}}
	return []sdk.Tool{
		{Name: "open", Description: "Read an existing resource and return its canonical view. Present it once using Conversations, preserving resource and props unchanged. The resource is live and follows future advances.", InputSchema: schema, Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			id, _ := args["id"].(string)
			a.mu.Lock()
			defer a.mu.Unlock()
			return map[string]any{"view": a.view(id), "value": a.values[id]}, nil
		}},
		{Name: "advance", Description: "Advance the SAME resource by one. Reuse its existing resource view; do not open another resource or resend its card just for a normal follow-up.", InputSchema: schema, Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			id, _ := args["id"].(string)
			a.mu.Lock()
			defer a.mu.Unlock()
			a.values[id]++
			return map[string]any{"value": a.values[id], "view": a.view(id)}, nil
		}},
	}
}
func main() { sdk.Run(&App{}) }
