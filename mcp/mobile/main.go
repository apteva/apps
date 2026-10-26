package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestBytes []byte

//go:embed migrations/001_init.sql
var migrationSQL string

type App struct {
	executor Executor
	local    *engine
	project  string
	ctx      *sdk.AppCtx
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--runner" {
		if err := runRunner(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	sdk.Run(&App{})
}
func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil || ctx.CurrentProject() == "" {
		return errors.New("Mobile requires a database and a project-scoped installation")
	}
	a.ctx = ctx
	a.project = ctx.CurrentProject()
	cfg := ctx.Config()
	if raw := cfg.Get("runner_url"); raw != "" {
		client, err := newRemote(raw, cfg.Get("runner_token"))
		if err != nil {
			return err
		}
		a.executor = client
		return nil
	}
	d := newDrivers(runnerConfig{AppiumURL: cfg.Get("appium_url"), DevicesJSON: cfg.Get("devices_json"), AndroidDriver: cfg.Get("android_driver"), ArtifactDir: cfg.Get("artifact_dir"), LockDir: cfg.Get("lock_dir")})
	local, err := newEngine(ctx.AppDB(), ctx.DataDir(), d)
	if err != nil {
		return err
	}
	a.local = local
	a.executor = local
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.local != nil {
		a.local.Shutdown()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "test_notifications", Schedule: "@every 5s", Run: func(ctx context.Context, app *sdk.AppCtx) error {
		if a.executor == nil {
			return nil
		}
		rows, err := app.AppDB().Query(`SELECT run_id FROM mobile_notifications WHERE project_id=? AND notified=0 LIMIT 100`, a.project)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, rid := range ids {
			v, err := a.executor.Execute(ctx, a.project, "mobile_test_get", map[string]any{"run_id": rid})
			if err != nil {
				continue
			}
			var out struct {
				Run TestRun `json:"run"`
			}
			if decodeMap(v, &out) != nil || out.Run.Status == "queued" || out.Run.Status == "running" {
				continue
			}
			if _, err = app.AppDB().Exec(`UPDATE mobile_notifications SET notified=1 WHERE run_id=?`, rid); err != nil {
				return err
			}
			app.EmitWithProject("mobile.test.completed", a.project, map[string]any{"run_id": rid, "status": out.Run.Status, "failure_kind": out.Run.FailureKind})
		}
		return nil
	}}}
}

func (a *App) call(ctx context.Context, tool string, args map[string]any) (any, error) {
	if a.executor == nil {
		return nil, errors.New("Mobile is not mounted")
	}
	result, err := a.executor.Execute(ctx, a.project, tool, args)
	if err != nil {
		return nil, err
	}
	if tool == "mobile_test_run" {
		var out struct {
			RunID string `json:"run_id"`
		}
		if err := decodeMap(result, &out); err != nil {
			return nil, err
		}
		_, err = a.ctx.AppDB().Exec(`INSERT OR IGNORE INTO mobile_notifications(run_id,project_id) VALUES (?,?)`, out.RunID, a.project)
		if err != nil {
			return nil, err
		}
	}
	if tool == "mobile_stream" {
		var details map[string]any
		if err := decodeMap(result, &details); err != nil {
			return nil, err
		}
		details["screenshot_url"] = "/api/apps/mobile/api/screen/" + url.PathEscape(str(args, "session_id")) + "?project_id=" + url.QueryEscape(a.project)
		result = details
	}
	return result, nil
}

func schema(properties map[string]any, required ...string) map[string]any {
	result := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}
func stringSchema() map[string]any { return map[string]any{"type": "string"} }
func boolSchema() map[string]any   { return map[string]any{"type": "boolean"} }
func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
func coordsSchema() map[string]any {
	return map[string]any{"type": "number", "minimum": 0, "maximum": 1}
}
func merge(a, b map[string]any) map[string]any {
	m := map[string]any{}
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}
func actionFields() map[string]any {
	return map[string]any{"action": enumSchema("navigate", "tap", "swipe", "text", "key", "tap_element", "context"), "session_id": stringSchema(), "url": stringSchema(), "x": coordsSchema(), "y": coordsSchema(), "to_x": coordsSchema(), "to_y": coordsSchema(), "text": stringSchema(), "key": enumSchema("home", "back", "enter"), "using": enumSchema("accessibility id", "id", "xpath", "css selector"), "value": stringSchema(), "context": stringSchema()}
}
func (a *App) MCPTools() []sdk.Tool {
	deviceFields := map[string]any{"platform": enumSchema("android", "ios"), "physical": boolSchema()}
	sessionFields := merge(deviceFields, map[string]any{"action": enumSchema("open", "list", "get", "renew", "close", "recover"), "device_id": stringSchema(), "session_id": stringSchema(), "ttl_seconds": map[string]any{"type": "integer", "minimum": 30, "maximum": 3600}, "mode": enumSchema("screen", "structured"), "confirm": boolSchema()})
	definitions := []struct {
		name, description string
		input             map[string]any
	}{
		{"mobile_devices", "List phones, backend capabilities and setup diagnostics.", schema(deviceFields)},
		{"mobile_session", "Reserve one phone exclusively; list/get/renew/close sessions. Close preserves data. Recover requires confirm=true after manually stopping an orphaned Appium session.", schema(sessionFields, "action")},
		{"mobile_observe", "See a phone screenshot as image content, optionally including the UI hierarchy or browser source.", schema(map[string]any{"session_id": stringSchema(), "source": boolSchema()}, "session_id")},
		{"mobile_action", "Navigate and interact with a mobile session. Coordinates are normalized 0..1. Element actions require Appium.", schema(actionFields(), "session_id", "action")},
		{"mobile_app", "Install APK/IPA from the runner artifact directory; launch, stop or uninstall an app. Uninstall requires confirm=true.", schema(map[string]any{"session_id": stringSchema(), "action": enumSchema("install", "launch", "stop", "uninstall"), "app_id": stringSchema(), "artifact": stringSchema(), "confirm": boolSchema()}, "session_id", "action")},
		{"mobile_stream", "Get screenshot polling details for a live session. This release provides polling rather than video.", schema(map[string]any{"session_id": stringSchema()}, "session_id")},
		{"mobile_test_run", "Start a deterministic smoke test. Steps execute sequentially with screenshots; params hold action arguments. assert_text/wait_for_text match UI source; timeout_ms is bounded to 30 seconds. Returns run_id.", schema(map[string]any{"device_id": stringSchema(), "name": stringSchema(), "build_ref": stringSchema(), "mode": enumSchema("screen", "structured"), "steps": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": schema(map[string]any{"action": enumSchema("navigate", "tap", "swipe", "text", "key", "tap_element", "context", "install", "launch", "stop", "screenshot", "assert_text", "wait_for_text"), "label": stringSchema(), "params": map[string]any{"type": "object", "additionalProperties": true}}, "action")}}, "device_id", "steps")},
		{"mobile_test_get", "Get a durable test report by run_id, or list recent runs when omitted.", schema(map[string]any{"run_id": stringSchema()})},
		{"mobile_test_cancel", "Request cancellation of a queued or running test.", schema(map[string]any{"run_id": stringSchema()}, "run_id")},
	}
	tools := make([]sdk.Tool, 0, len(definitions))
	for _, def := range definitions {
		name := def.name
		tools = append(tools, sdk.Tool{Name: name, Description: def.description, InputSchema: def.input, HandlerCtx: func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			if app.CurrentProject() != a.project {
				return nil, problem("forbidden", "project does not match this Mobile installation")
			}
			v, err := a.call(ctx, name, args)
			if err != nil {
				return nil, err
			}
			if name == "mobile_observe" {
				var obs Observation
				if err := decodeMap(v, &obs); err != nil {
					return nil, err
				}
				return sdk.MCPResult{Content: []sdk.MCPContent{{Type: "image", Data: obs.Image, MIMEType: obs.MIME}}, StructuredContent: map[string]any{"width": obs.Width, "height": obs.Height, "source": obs.Source}}, nil
			}
			return v, nil
		}})
	}
	return tools
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/api/call", Handler: a.handleCall}, {Pattern: "/api/screen/", Handler: a.handleScreen}, {Pattern: "/api/artifacts/", Handler: a.handleArtifact}}
}
func (a *App) requestProject(r *http.Request) error {
	if a.project == "" {
		return errors.New("Mobile is not mounted")
	}
	if pid := r.URL.Query().Get("project_id"); pid != "" && pid != a.project {
		return problem("forbidden", "project does not match this Mobile installation")
	}
	return nil
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return origin == "https://"+r.Host || origin == "http://"+r.Host
}
func (a *App) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", 405)
		return
	}
	if !sameOrigin(r) {
		writeFailure(w, problem("forbidden", "cross-origin requests are not allowed"))
		return
	}
	if err := a.requestProject(r); err != nil {
		writeFailure(w, err)
		return
	}
	var req struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeFailure(w, problem("invalid_argument", "invalid JSON request"))
		return
	}
	result, err := a.call(r.Context(), req.Tool, req.Args)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, result)
}
func (a *App) handleScreen(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 405)
		return
	}
	if err := a.requestProject(r); err != nil {
		writeFailure(w, err)
		return
	}
	sid := strings.TrimPrefix(r.URL.Path, "/api/screen/")
	v, err := a.call(r.Context(), "mobile_observe", map[string]any{"session_id": sid})
	if err != nil {
		writeFailure(w, err)
		return
	}
	var obs Observation
	if err = decodeMap(v, &obs); err != nil {
		writeFailure(w, err)
		return
	}
	b, err := base64.StdEncoding.DecodeString(obs.Image)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writePNG(w, b)
}
func (a *App) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 405)
		return
	}
	if err := a.requestProject(r); err != nil {
		writeFailure(w, err)
		return
	}
	aid := strings.TrimPrefix(r.URL.Path, "/api/artifacts/")
	reader, ok := a.executor.(interface {
		Evidence(context.Context, string, string) ([]byte, error)
	})
	if !ok {
		writeFailure(w, errors.New("artifact reader is unavailable"))
		return
	}
	b, err := reader.Evidence(r.Context(), a.project, aid)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writePNG(w, b)
}
func writePNG(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func writeFailure(w http.ResponseWriter, err error) {
	code := errorCode(err)
	status := http.StatusBadGateway
	switch code {
	case "invalid_argument":
		status = 400
	case "forbidden":
		status = 403
	case "not_found":
		status = 404
	case "device_unavailable", "session_inactive", "session_expired":
		status = 409
	case "unsupported", "setup_required", "invalid_configuration":
		status = 422
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Code: code, Message: err.Error()})
}
func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
