package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

type App struct {
	httpClient        *http.Client
	eventHubs         *appEventHubManager
	ctx               *sdk.AppCtx
	mutationMu        sync.Mutex
	streams           streamRegistry
	maintenanceCancel context.CancelFunc
	maintenanceDone   chan struct{}
	requestSlots      chan struct{}
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("api requires a db block")
	}
	a.ctx = ctx
	a.requestSlots = make(chan struct{}, 256)
	if a.httpClient == nil {
		a.httpClient = gatewayHTTPClient()
	}
	a.eventHubs = newAppEventHubManager(os.Getenv("APTEVA_GATEWAY_URL"), outboundAppToken(), a.httpClient)
	if err := reconcileBrowserOriginPolicies(ctx); err != nil {
		// API data remains authoritative and stable registration keys are
		// retried on the next mount. Do not make the Gateway unavailable when
		// the platform policy endpoint is temporarily unavailable.
		ctx.Logger().Warn("browser-origin reconciliation incomplete", "error", err)
	}
	a.startMaintenance(ctx)
	ctx.Logger().Info("api mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.maintenanceCancel != nil {
		a.maintenanceCancel()
		<-a.maintenanceDone
	}
	a.streams.closeAll()
	if a.ctx != nil {
		stopLogSink(a.ctx.AppDB())
		releaseRouteCache(a.ctx.AppDB())
	}
	if a.eventHubs != nil {
		a.eventHubs.close()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/apis", Handler: a.handleAPIs},
		{Pattern: "/apis/", Handler: a.handleAPIItem},
		{Pattern: "/tools/call", Handler: a.handleToolsCall},
		{Pattern: "/gw/", Handler: a.handleGateway, NoAuth: true},
	}
}

func main() { sdk.Run(&App{}) }

func projectFromArgs(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	trusted := strings.TrimSpace(ctx.CurrentProject())
	if trusted == "" {
		return "", errors.New("authenticated project context required")
	}
	for _, key := range []string{"project_id", "_project_id"} {
		if value, exists := args[key]; exists {
			s, ok := value.(string)
			if !ok || (strings.TrimSpace(s) != "" && strings.TrimSpace(s) != trusted) {
				return "", errors.New("project_id does not match authenticated scope")
			}
		}
	}
	return trusted, nil
}

// Management calls arrive through the token-authenticated platform proxy.
// Public gateway URLs may select a project, but cannot override a pinned install.
func (a *App) projectFromRequest(r *http.Request) (string, error) {
	pinned := strings.TrimSpace(a.ctx.CurrentProject())
	trusted := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	requested := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pinned != "" {
		if trusted != "" && trusted != pinned || requested != "" && requested != pinned {
			return "", errors.New("project_id does not match install")
		}
		return pinned, nil
	}
	if trusted != "" {
		if requested != "" && requested != trusted {
			return "", errors.New("project_id does not match authenticated scope")
		}
		return trusted, nil
	}
	if requested != "" {
		return requested, nil
	}
	return "", errors.New("project_id required")
}

func (a *App) managementContext(r *http.Request, args map[string]any) (*sdk.AppCtx, error) {
	pid, err := a.projectFromRequest(r)
	if err != nil {
		return nil, err
	}
	ctx := a.ctx.WithProject(pid)
	if _, err := projectFromArgs(ctx, args); err != nil {
		return nil, err
	}
	args["project_id"] = pid
	return ctx, nil
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func corsPolicySchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Explicit browser CORS policy. Wildcard origins are rejected. Route policies override API defaults.",
		"properties": map[string]any{
			"enabled":        map[string]any{"type": "boolean"},
			"origins":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"allow_methods":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"allow_headers":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"expose_headers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"credentials":    map[string]any{"type": "boolean"},
			"max_age":        map[string]any{"type": "integer", "minimum": 0, "maximum": 86400},
		},
	}
}

func stringArg(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func boolArg(args map[string]any, key string, def bool) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		case json.Number:
			i, _ := n.Int64()
			return int(i)
		}
	}
	return def
}

func jsonTextArg(args map[string]any, key string, def string) (string, error) {
	v, ok := args[key]
	if !ok {
		return def, nil
	}
	if v == nil {
		return "", fmt.Errorf("%s must not be null", key)
	}
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return def, nil
		}
		if !json.Valid([]byte(s)) {
			return "", fmt.Errorf("%s must be valid JSON", key)
		}
		return s, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
