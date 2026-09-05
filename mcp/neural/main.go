package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml migrations/*.sql
var embedded embed.FS

type App struct {
	db  *sql.DB
	ctx *sdk.AppCtx
	mu  sync.Mutex
}

func (a *App) Manifest() sdk.Manifest {
	raw, _ := embedded.ReadFile("apteva.yaml")
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	a.db = ctx.AppDB()
	a.ctx = ctx
	if a.db == nil {
		return fmt.Errorf("neural requires SQLite")
	}
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "trainer", Schedule: "@every 1s", Run: func(_ context.Context, ctx *sdk.AppCtx) error { return a.tick(ctx.CurrentProject()) }}}
}
func (a *App) emit(project, topic string, id int64) {
	if a.ctx != nil {
		a.ctx.EmitWithProject(topic, project, map[string]any{"id": id})
	}
}
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/rpc", Handler: a.handleRPC}, {Pattern: "/deployments/", Handler: a.handlePrediction}}
}
func (a *App) requestProject(r *http.Request) (string, error) {
	project := ""
	if a.ctx != nil {
		project = a.ctx.CurrentProject()
	}
	if project == "" {
		return "", fmt.Errorf("project-scoped install required")
	}
	if q := r.URL.Query().Get("project_id"); q != "" && q != project {
		return "", fmt.Errorf("project mismatch")
	}
	return project, nil
}
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func readJSON(w http.ResponseWriter, r *http.Request, out any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return fmt.Errorf("Content-Type must be application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
func (a *App) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", 405)
		return
	}
	project, err := a.requestProject(r)
	if err != nil {
		writeJSON(w, 403, map[string]any{"error": err.Error()})
		return
	}
	var req struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err = readJSON(w, r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	data, err := a.perform(project, req.Tool, req.Args)
	if err != nil {
		writeJSON(w, httpStatus(err), map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, data)
}
func (a *App) handlePrediction(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "deployments" || parts[2] != "predict" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	project, err := a.requestProject(r)
	if err != nil {
		writeJSON(w, 403, map[string]any{"error": err.Error()})
		return
	}
	args := map[string]any{}
	if err = readJSON(w, r, &args); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if args == nil {
		writeJSON(w, 400, map[string]any{"error": "expected an input object"})
		return
	}
	args["deployment_id"] = float64(id)
	data, err := a.perform(project, "predictions_create", args)
	if err != nil {
		writeJSON(w, httpStatus(err), map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, data)
}
func object(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func (a *App) MCPTools() []sdk.Tool {
	integer := map[string]any{"type": "integer", "minimum": 1}
	number := map[string]any{"type": "number", "minimum": -1, "maximum": 1}
	schemas := map[string]map[string]any{
		"experiments_list":      object(map[string]any{}),
		"experiments_create":    object(map[string]any{"name": map[string]any{"type": "string"}, "dataset": map[string]any{"type": "string", "enum": []string{"xor", "circles", "linear"}}, "hidden": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 2, "maximum": 12}, "minItems": 1, "maxItems": 2}, "learning_rate": map[string]any{"type": "number", "minimum": 0.0001, "maximum": 0.1}, "epochs": map[string]any{"type": "integer", "minimum": 10, "maximum": 2000}, "seed": map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647}}),
		"experiments_get":       object(map[string]any{"id": integer}, "id"),
		"experiments_control":   object(map[string]any{"id": integer, "action": map[string]any{"type": "string", "enum": []string{"start", "pause", "step"}}}, "id", "action"),
		"model_versions_create": object(map[string]any{"experiment_id": integer}, "experiment_id"),
		"model_versions_list":   object(map[string]any{}),
		"deployments_create":    object(map[string]any{"version_id": integer}, "version_id"),
		"deployments_list":      object(map[string]any{}),
		"predictions_create":    object(map[string]any{"experiment_id": integer, "deployment_id": integer, "x": number, "y": number}, "x", "y"),
	}
	schemas["predictions_create"]["oneOf"] = []any{
		map[string]any{"required": []string{"experiment_id"}},
		map[string]any{"required": []string{"deployment_id"}},
	}
	out := []sdk.Tool{}
	for _, decl := range a.Manifest().Provides.MCPTools {
		name := decl.Name
		out = append(out, sdk.Tool{Name: name, Description: decl.Description, InputSchema: schemas[name], Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			project := ctx.CurrentProject()
			if a.ctx != nil && a.ctx.CurrentProject() != "" && project != a.ctx.CurrentProject() {
				return nil, fmt.Errorf("project mismatch")
			}
			return a.perform(project, name, args)
		}})
	}
	return out
}
func main() { sdk.Run(&App{}) }
