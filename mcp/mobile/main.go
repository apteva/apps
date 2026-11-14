package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestBytes []byte

// Keep the panel available when a packaged runtime has no populated ./ui dir.
// Exact asset routes coexist with the SDK's optional directory handler.
//
//go:embed ui/*.mjs ui/*.mjs.map ui/icon.svg
var embeddedUI embed.FS

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationSQL is every migration in filename order. All statements are
// idempotent so a standalone runner can apply them on each boot.
var migrationSQL = func() string {
	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		panic(err)
	}
	sort.Strings(entries)
	var b strings.Builder
	for _, name := range entries {
		data, err := migrationFiles.ReadFile(name)
		if err != nil {
			panic(err)
		}
		b.Write(data)
		b.WriteString("\n")
	}
	return b.String()
}()

type App struct {
	executor Executor
	local    *engine
	project  string
	ctx      *sdk.AppCtx
	uploadMu sync.Mutex
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
	d := newDrivers(runnerConfig{AppiumURL: cfg.Get("appium_url"), DevicesJSON: cfg.Get("devices_json"), AndroidDriver: cfg.Get("android_driver"), IOSDriver: cfg.Get("ios_driver"), IOSTeamID: cfg.Get("ios_team_id"), ArtifactDir: cfg.Get("artifact_dir"), LockDir: cfg.Get("lock_dir")})
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
	}}, {Name: "recording_uploads", Schedule: "@every 15s", Run: a.recordingUploadWorker}}
}

func (a *App) call(ctx context.Context, tool string, args map[string]any) (any, error) {
	if a.executor == nil {
		return nil, errors.New("Mobile is not mounted")
	}
	args, early, err := a.prepareRecordCall(tool, args)
	if err != nil || early != nil {
		return early, err
	}
	result, err := a.executor.Execute(ctx, a.project, tool, args)
	if err != nil {
		return nil, err
	}
	if tool == "mobile_record" || tool == "mobile_record_export" {
		result = a.decorateRecordings(result)
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
		{"mobile_power", "Read or change an Android ADB phone's persistent Stay awake while charging setting. action=status (default) or set with enabled=true|false. Requires an active exclusive session. Enabling covers AC, USB, wireless and dock power; unplugged the phone uses its normal screen timeout. Closing Mobile/session keeps the preference. Manual locking still works.", schema(map[string]any{"session_id": stringSchema(), "action": enumSchema("status", "set"), "enabled": boolSchema()}, "session_id")},
		{"mobile_setup", "Inspect native iPhone setup on the Mac hosting Mobile. action=status (default) discovers Apple teams without requiring an existing certificate; select_team(team_id) remembers a discovered team; open_xcode opens Xcode for Apple Account sign-in. Mobile automatically creates signing assets when a device session opens. Phone trust, Developer Mode and any Apple sign-in/2FA remain operator steps.", schema(map[string]any{"action": enumSchema("status", "select_team", "open_xcode"), "team_id": stringSchema()})},
		{"mobile_devices", "List phones, backend capabilities and setup diagnostics.", schema(deviceFields)},
		{"mobile_session", "Reserve one phone exclusively; list/get/renew/close sessions. Close preserves data. Recover requires confirm=true after manually stopping an orphaned Appium session.", schema(sessionFields, "action")},
		{"mobile_observe", "See a phone screenshot as image content, optionally including the UI hierarchy or browser source.", schema(map[string]any{"session_id": stringSchema(), "source": boolSchema()}, "session_id")},
		{"mobile_action", "Navigate and interact with a mobile session. Coordinates are normalized 0..1. tap_element needs Appium or the native iOS driver (using=accessibility id or id); context needs Appium.", schema(actionFields(), "session_id", "action")},
		{"mobile_app", "Install, launch, stop, uninstall or check an app. install takes source=artifact (APK/IPA in the runner artifact directory, default), store (Play Store / App Store) or beta (TestFlight; Play testing tracks via the Play listing). Store and beta installs tap Install/Update and return result=installing or already_installed; poll action=status until installed=true. iOS store needs store_id; TestFlight needs app_name or store_id. flow overrides the built-in store steps. Uninstall requires confirm=true.", schema(map[string]any{"session_id": stringSchema(), "action": enumSchema("install", "launch", "stop", "uninstall", "status"), "source": enumSchema("artifact", "store", "beta"), "app_id": stringSchema(), "app_name": stringSchema(), "store_id": stringSchema(), "artifact": stringSchema(), "confirm": boolSchema(), "flow": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "object", "additionalProperties": true}}}, "session_id", "action")},
		{"mobile_stream", "Get screenshot polling details for a live session. Use mobile_record for video.", schema(map[string]any{"session_id": stringSchema()}, "session_id")},
		{"mobile_record", "Record a session's screen to video. start(session_id, quality=auto|standard|high, audio?, max_seconds? ≤1800) → recording; mark(recording_id, name) adds a timestamped marker; stop(recording_id or session_id) finishes and returns metadata; get/list(status?, run_id?, kind?) include file_url and storage upload state; delete(recording_id, confirm=true) removes the runner copy; presets lists export presets; upload(recording_id) retries the Storage copy. high = scrcpy (Android) or USB capture on a Mac (iOS), both with audio. iOS Simulators record full resolution through simctl, without audio.", schema(map[string]any{"action": enumSchema("start", "stop", "mark", "get", "list", "delete", "presets", "upload"), "session_id": stringSchema(), "recording_id": stringSchema(), "quality": enumSchema("auto", "standard", "high"), "audio": boolSchema(), "max_seconds": map[string]any{"type": "integer", "minimum": 5, "maximum": maxRecordSeconds}, "name": stringSchema(), "status": stringSchema(), "run_id": stringSchema(), "kind": enumSchema("capture", "export"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}, "confirm": boolSchema()}, "action")},
		{"mobile_record_export", "Export a ready recording for a destination. preset (raw, app_store_preview, play_promo, social_vertical, social_square, or one from export_presets_json) or custom (a preset object); variant picks the size (app_store_preview: iphone, iphone_home_large, iphone_home_medium, ipad, ipad_9_7, mac, apple_tv); orientation and fit (pad|crop|stretch) override; trim with from/to seconds or from_mark/to_mark. Runs in the background; poll mobile_record(get) until ready, then read checks and valid.", schema(map[string]any{"recording_id": stringSchema(), "preset": stringSchema(), "custom": map[string]any{"type": "object", "additionalProperties": true}, "variant": stringSchema(), "orientation": enumSchema("portrait", "landscape"), "fit": enumSchema("pad", "crop", "stretch"), "from": map[string]any{"type": "number", "minimum": 0}, "to": map[string]any{"type": "number", "minimum": 0}, "from_mark": stringSchema(), "to_mark": stringSchema()}, "recording_id")},
		{"mobile_test_run", "Start a deterministic smoke test. Steps execute sequentially with screenshots; params hold action arguments. assert_text/wait_for_text match UI source; timeout_ms is bounded to 30 seconds. wait pauses params.ms (≤30000); record_mark adds params.name as a video marker; wait_for_app polls until params.app_id is installed (timeout_ms ≤300000); install accepts params.source=store|beta. record=true or {quality, audio} films the whole run with a marker per step (report recording_id). Returns run_id.", schema(map[string]any{"device_id": stringSchema(), "name": stringSchema(), "build_ref": stringSchema(), "mode": enumSchema("screen", "structured"), "record": map[string]any{"type": []string{"boolean", "object"}}, "steps": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": schema(map[string]any{"action": enumSchema("navigate", "tap", "swipe", "text", "key", "tap_element", "context", "install", "launch", "stop", "screenshot", "assert_text", "wait_for_text", "wait", "record_mark", "wait_for_app"), "label": stringSchema(), "params": map[string]any{"type": "object", "additionalProperties": true}}, "action")}}, "device_id", "steps")},
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
	routes := []sdk.Route{{Pattern: "/api/call", Handler: a.handleCall}, {Pattern: "/api/screen/", Handler: a.handleScreen}, {Pattern: "/api/artifacts/", Handler: a.handleArtifact}, {Pattern: "/api/recordings/", Handler: a.handleRecording}}
	assets := http.FileServer(http.FS(embeddedUI))
	serveAsset := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".mjs.map") {
			w.Header().Set("Content-Type", "application/json")
		}
		assets.ServeHTTP(w, r)
	}
	for _, path := range []string{"/ui/MobilePanel.mjs", "/ui/MobilePanel.mjs.map", "/ui/icon.svg"} {
		routes = append(routes, sdk.Route{Pattern: path, Method: http.MethodGet, Handler: serveAsset})
	}
	return routes
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
	case "device_unavailable", "session_inactive", "session_expired", "setup_in_progress":
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
