package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"

	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

type App struct{}

// globalCtx — stashed here so HTTP handlers (which the SDK invokes
// without an AppCtx) can reach the DB. Same workaround crm + storage
// use; revisit if the SDK grows a request-scoped accessor.
var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnUnmount(ctx *sdk.AppCtx) error {
	if err := stopValidationWorkers(ctx.AppDB()); err != nil {
		return err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE backtest_runs SET status='paused' WHERE status='running' AND id IN (SELECT run_id FROM backtest_simulations)`); err != nil {
		return err
	}
	simulationWorkers.Lock()
	ids := []int64{}
	for id := range simulationWorkers.running {
		ids = append(ids, id)
	}
	simulationWorkers.Unlock()
	for _, id := range ids {
		if _, err := ctx.AppDB().Exec(`UPDATE backtest_runs SET status='paused' WHERE id=? AND status='running'`, id); err != nil {
			return err
		}
		waitSimulationWorker(id)
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// providerHealthSnapshot — read the live provider's per-class health
// when present; otherwise return a minimal "all-mock" view so the UI
// always gets a consistent shape.
func providerHealthSnapshot() map[string]any {
	var out struct {
		Providers map[string]any `json:"providers"`
	}
	if err := callTrading(globalCtx, "market_source", map[string]any{}, &out); err != nil {
		return map[string]any{"trading": map[string]any{"stale": true, "error": err.Error()}}
	}
	return out.Providers
}

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string, def int64) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return def
}

func floatArg(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return n
		}
	}
	return def
}

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// emit — fire-and-forget app-event publish. Goes through the SDK's
// httpEmitter to apteva-server, which fans out via SSE to any
// /api/app-events/trading subscribers (panel, desk SPA). Best-effort:
// errors are swallowed inside ctx.Emit, the app's DB stays the source
// of truth, and a UI reconnect with `since=` replays missed events.
//
// Call from anywhere — agent tool handlers (request-scoped), engine
// goroutines (background), HTTP handlers. Safe with no subscribers.
func emit(topic string, data any) {
	if globalCtx == nil {
		return
	}
	globalCtx.Emit(topic, data)
}

// schemaObject — JSON-schema {type:object, properties, required}.
// Same helper crm/storage have; will eventually move into the SDK.
func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		return map[string]any{"type": "object"}
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func httpJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func httpErr(w http.ResponseWriter, status int, msg string) {
	httpJSON(w, status, map[string]any{"error": msg})
}

func main() { sdk.Run(&App{}) }

// Compile-time consistency: silences "imported and not used" on context
// when builds drop the engine path.
var _ = context.Background
