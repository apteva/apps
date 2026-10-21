package sdk

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Run is the one-line entrypoint for app sidecars: read the manifest,
// open the app DB, run migrations, mount HTTP + MCP, start workers,
// block until SIGTERM. Apps' main() is typically just:
//
//	package main
//
//	import sdk "github.com/apteva/app-sdk"
//
//	func main() { sdk.Run(&MyApp{}) }
//
// The framework reads APTEVA_APP_TOKEN, APTEVA_GATEWAY_URL,
// APTEVA_INSTALL_ID, APTEVA_PROJECT_ID, APTEVA_APP_CONFIG (encoded
// JSON) from env — the platform injects all of these.
func Run(app App) {
	lifetime, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := runApp(lifetime, app); err != nil {
		log.Fatalf("apteva-app: %v", err)
	}
}

func runApp(lifetime context.Context, app App) error {
	manifest := app.Manifest()
	if err := ValidateManifest(&manifest); err != nil {
		return err
	}
	logger := newDefaultLogger(manifest.Name)
	logger.Info("starting", "name", manifest.Name, "version", manifest.Version)
	budget := manifest.Runtime.StartupTimeoutSeconds
	if budget == 0 {
		budget = 60
	}
	startupCtx, cancelStartup := context.WithTimeout(lifetime, time.Duration(budget)*time.Second)
	defer cancelStartup()
	status := newStartupStatus()
	port := manifest.Runtime.Port
	if port == 0 {
		port = 8080
	}
	if v := os.Getenv("APTEVA_APP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}
	host := manifest.Runtime.BindHost
	if v := os.Getenv("APTEVA_BIND_HOST"); v != "" {
		host = v
	}
	if host == "" {
		host = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{Handler: status, ReadHeaderTimeout: 5 * time.Second}
	httpDone := make(chan error, 1)
	go func() { httpDone <- srv.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	go func() { <-startupCtx.Done(); status.failIfInitializing() }()
	status.progress("database", 0, 0)
	var databases *appDatabases
	if manifest.DB != nil {
		databases, err = openAppDatabasesContext(startupCtx, manifest.DB, logger)
		if err != nil {
			status.failIfInitializing()
			return fmt.Errorf("open db: %w", err)
		}
		defer databases.Close()
	}
	cancelCh := make(chan struct{})
	var cancelOnce sync.Once
	closeApp := func() { cancelOnce.Do(func() { close(cancelCh) }) }
	defer closeApp()
	// Preserve cancellation for apps using the existing Done channel in OnMount.
	go func() {
		select {
		case <-lifetime.Done():
			closeApp()
		case <-cancelCh:
		}
	}()
	outboundToken := appOutboundToken()
	ctx := &AppCtx{manifest: &manifest, cfg: readConfigEnv(),
		platform: newHTTPPlatformClient(os.Getenv("APTEVA_GATEWAY_URL"), outboundToken),
		logger:   logger, cancel: cancelCh,
		emitter:        newHTTPEmitter(os.Getenv("APTEVA_GATEWAY_URL"), outboundToken, logger),
		startupContext: startupCtx, startup: status}
	if databases != nil {
		ctx.db, ctx.readDB, ctx.dbState = databases.writer, databases.reader, databases.state
	}
	status.progress("mounting", 0, 0)
	if err := app.OnMount(ctx); err != nil {
		status.failIfInitializing()
		return fmt.Errorf("OnMount: %w", err)
	}
	if err := startupCtx.Err(); err != nil {
		status.failIfInitializing()
		return err
	}
	if databases != nil {
		if _, err := databases.writer.ExecContext(startupCtx, "PRAGMA busy_timeout=30000"); err != nil {
			status.failIfInitializing()
			return err
		}
		databases.writer.SetConnMaxLifetime(5 * time.Minute)
		databases.writer.SetConnMaxIdleTime(2 * time.Minute)
	}
	mux := http.NewServeMux()
	mountAppRoutes(mux, app, ctx)
	mountFrameworkRoutes(mux, app, ctx)
	if !status.ready(startupCtx, withTokenAuth(mux, manifest.Provides.HTTPRoutes...)) {
		return fmt.Errorf("initialization deadline expired")
	}
	cancelStartup()
	logger.Info("ready", "port", port)
	workerCtx, workerCancel := context.WithCancel(lifetime)
	defer workerCancel()
	var wg sync.WaitGroup
	for _, w := range app.Workers() {
		wg.Add(1)
		go func(w Worker) { defer wg.Done(); runWorker(workerCtx, w, ctx, logger) }(w)
	}
	select {
	case <-lifetime.Done():
	case err = <-httpDone:
	}
	workerCancel()
	closeApp()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	unmounted := make(chan error, 1)
	go func() { unmounted <- app.OnUnmount(ctx) }()
	select {
	case err := <-unmounted:
		if err != nil {
			logger.Warn("OnUnmount error", "err", err)
		}
	case <-shutdownCtx.Done():
	}
	workersDone := make(chan struct{})
	go func() { wg.Wait(); close(workersDone) }()
	select {
	case <-workersDone:
	case <-shutdownCtx.Done():
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func appOutboundToken() string {
	if token := os.Getenv("APTEVA_OUTBOUND_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("APTEVA_APP_TOKEN")
}

// publicRoutePaths collects the patterns of every NoAuth route that
// HTTPRoutes() declared. withTokenAuth consults this slice on every
// request to decide whether to skip the bearer-token check. Populated
// once per process at mountAppRoutes time; reads after that are
// lock-free.
var publicRoutePaths []string

func mountAppRoutes(mux *http.ServeMux, app App, ctx *AppCtx) {
	for _, r := range app.HTTPRoutes() {
		if r.NoAuth {
			publicRoutePaths = append(publicRoutePaths, r.Pattern)
		}
		method := r.Method
		pattern := r.Pattern
		handler := r.Handler
		// Use Go 1.22+'s method-prefixed pattern syntax when the route
		// declares a method. Without this, two routes on the same path
		// with different methods (e.g. GET + DELETE on "/instances/",
		// a very common pattern) panic at boot because the underlying
		// ServeMux treats them as conflicting registrations.
		muxPattern := pattern
		if method != "" {
			muxPattern = method + " " + pattern
		}
		mux.HandleFunc(muxPattern, func(w http.ResponseWriter, req *http.Request) {
			handler(w, req)
			_ = ctx
		})
	}
}

// matchesPublicRoute returns true when the request path satisfies any
// NoAuth route pattern. Literal exact/subtree routes retain their historical
// behavior, while patterns containing wildcards use http.ServeMux itself so
// routes such as /v1/devices/{id}/test match exactly as they do at dispatch.
func matchesPublicRoute(path string) bool {
	return matchesPublicRoutePatterns(path, publicRoutePaths)
}

func matchesPublicRoutePatterns(path string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			p = "/"
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if strings.Contains(p, "{") {
			matcher := cachedPublicRouteMatcher(p)
			if matcher == nil {
				continue
			}
			_, matchedPattern := matcher.Handler(&http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: path},
			})
			if matchedPattern != "" {
				return true
			}
		} else if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) {
				return true
			}
		} else if path == p {
			return true
		}
	}
	return false
}

var publicRouteMatcherCache sync.Map

func cachedPublicRouteMatcher(pattern string) (matcher *http.ServeMux) {
	if cached, ok := publicRouteMatcherCache.Load(pattern); ok {
		matcher, _ = cached.(*http.ServeMux)
		return matcher
	}
	defer func() {
		if recover() != nil {
			matcher = nil
		}
	}()
	compiled := http.NewServeMux()
	compiled.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	actual, _ := publicRouteMatcherCache.LoadOrStore(pattern, compiled)
	matcher, _ = actual.(*http.ServeMux)
	return matcher
}

// mountFrameworkRoutes wires the platform-mandated endpoints every app
// must expose: /health (orchestrator), /mcp (agent calls), /events
// (platform pushes events). If a ./ui directory exists, /ui/* serves
// it as static files so the dashboard can iframe panel HTML directly.
func mountFrameworkRoutes(mux *http.ServeMux, app App, ctx *AppCtx) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"status":"ready"}`))
	})

	// Live manifest — apteva-server polls this so the dashboard's
	// "update available" detector can see the running sidecar's version
	// rather than the snapshot frozen in apps.manifest_json at install
	// time. Same shape as the parsed manifest stored server-side.
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, _ *http.Request) {
		m := app.Manifest()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&m)
	})

	// Static UI bundle. The default lookup is ./ui; apps that ship UI
	// override by setting APTEVA_UI_DIR.
	uiDir := os.Getenv("APTEVA_UI_DIR")
	if uiDir == "" {
		uiDir = "ui"
	}
	if info, err := os.Stat(uiDir); err == nil && info.IsDir() {
		mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir(uiDir))))
	}

	// MCP endpoint — JSON-RPC over HTTP. Minimal implementation: list
	// tools / call tool. Apps may override by declaring no tools and
	// mounting their own /mcp route in HTTPRoutes(), but most won't
	// need to.
	mcp := newMCPHandler(app, ctx)
	mux.Handle("/mcp", mcp)
	mux.Handle(InternalAppBatchPath, newInternalAppBatchHandler(mcp))
	if source, ok := app.(FileReferenceSource); ok {
		mux.Handle(FileReferenceReadPath, fileReferenceReadHandler(source))
	}

	// Event ingestion — the platform POSTs platform events here and
	// the framework dispatches to the app's EventHandlers.
	//
	// The handler's AppCtx has CurrentProject() pinned to the event's
	// project_id, so global apps subscribing to a multi-project topic
	// stream see each event in the right project context without
	// hand-threading.
	mux.HandleFunc("/events", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var ev Event
		if err := json.NewDecoder(req.Body).Decode(&ev); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		eventName := ev.Name()
		if ev.Event == "" {
			ev.Event = eventName
		}
		if ev.Topic == "" {
			ev.Topic = eventName
		}
		evCtx := ctx
		if ev.ProjectID != "" {
			evCtx = ctx.WithProject(ev.ProjectID)
		}
		matched := 0
		for _, h := range app.EventHandlers() {
			handlerEvent := h.Event
			if handlerEvent == "" {
				handlerEvent = h.Topic
			}
			if handlerEvent != eventName && handlerEvent != "*" {
				continue
			}
			matched++
			if err := h.Handler(evCtx, ev); err != nil {
				evCtx.Logger().Warn("event handler error", "event", eventName, "err", err)
				http.Error(w, "event handler failed", http.StatusInternalServerError)
				return
			}
		}
		if matched == 0 && eventName == AgentEventLifecycleEvent {
			http.Error(w, "agent lifecycle event handler required", http.StatusUnprocessableEntity)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// runWorker drives one Worker entry — periodic if Schedule is set,
// one-shot otherwise. Cancellation honours ctx.
//
// Project dispatch. Workers run once per project per tick:
//
//   - Project-scoped install (APTEVA_PROJECT_ID set): a single-project
//     list — the worker behaves identically to pre-dispatch SDK.
//   - Global install (APTEVA_PROJECT_ID empty): the SDK calls
//     ListProjects() once per tick and runs the worker body once per
//     returned project, each invocation getting an AppCtx whose
//     CurrentProject() returns that project's id.
//
// Apps that read os.Getenv("APTEVA_PROJECT_ID") still see "" under
// global — env can't be set per-goroutine — but ctx.CurrentProject()
// returns the dispatched project. Migrating an app to global-safe
// is therefore a search-and-replace from env to CurrentProject.
func runWorker(ctx context.Context, w Worker, app *AppCtx, logger Logger) {
	dispatch := func(ctx context.Context) {
		projects := dispatchProjects(app, logger)
		for _, pid := range projects {
			scoped := app
			if pid != "" {
				scoped = app.WithProject(pid)
			}
			if err := w.Run(ctx, scoped); err != nil && ctx.Err() == nil {
				logger.Warn("worker tick error", "name", w.Name, "project", pid, "err", err)
			}
		}
	}

	if w.Schedule == "" {
		// One-shot. Run once per project.
		dispatch(ctx)
		return
	}
	interval, err := parseSchedule(w.Schedule)
	if err != nil {
		logger.Warn("worker schedule parse failed; not running", "name", w.Name, "schedule", w.Schedule, "err", err)
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			dispatch(ctx)
		}
	}
}

// dispatchProjects decides which projects this tick should run for.
//
//   - APTEVA_PROJECT_ID set → singleton, that project.
//   - empty (global install) → ListProjects() from the platform.
//     A failed call yields a single empty-project tick so a
//     transient platform outage doesn't entirely silence the
//     worker; the worker body sees CurrentProject()=="" and can
//     choose to skip or proceed.
func dispatchProjects(app *AppCtx, logger Logger) []string {
	if app == nil {
		return []string{""}
	}
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
		return []string{pid}
	}
	if app.platform == nil {
		return []string{""}
	}
	projects, err := app.platform.ListProjects()
	if err != nil {
		logger.Warn("ListProjects failed; running once with empty project", "err", err)
		return []string{""}
	}
	if len(projects) == 0 {
		// No projects yet (fresh install, operator hasn't created
		// any). One empty-project tick keeps the worker alive
		// without doing per-project work.
		return []string{""}
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		if p.ID == "" {
			continue
		}
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 {
		return []string{""}
	}
	return ids
}

// parseSchedule supports the "@every <duration>" syntax for now. Real
// cron expressions can be added later via robfig/cron.
func parseSchedule(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	const prefix = "@every "
	if strings.HasPrefix(s, prefix) {
		return time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(s, prefix)))
	}
	return 0, fmt.Errorf("only '@every <duration>' schedules supported; got %q", s)
}

// withTokenAuth gates every non-/health request on a header that
// matches APTEVA_APP_TOKEN. The platform injects the token in env at
// boot and uses it on every callback. Without this, anyone reaching
// the sidecar's port could call its tools.
//
// Three carve-outs let apps serve signed / public URLs / provider
// webhooks:
//
//  1. /health — the orchestrator's liveness probe never has a token.
//  2. Explicit Route.NoAuth or manifest no_auth routes — signed downloads
//     must opt in on their exact route and validate the signature in their handler.
//     Query parameters never relax authentication on protected routes.
//  3. /webhooks/* — provider-callback endpoints (SES/SNS, Twilio,
//     Stripe, GitHub, etc.). The provider signs the request payload
//     with their own scheme (SNS X.509, Twilio HMAC-SHA1, Stripe
//     signed payload, etc.); the handler MUST verify that signature.
//     This carve-out exists because external providers don't have
//     our APTEVA_APP_TOKEN and can't be made to use one — their
//     authenticity comes from per-provider request signing.
func withTokenAuth(h http.Handler, declaredRoutes ...RouteSpec) http.Handler {
	expected := os.Getenv("APTEVA_APP_TOKEN")
	// Keep manifest declarations local to this handler. Framework-served UI
	// files have no HTTPRoutes() entry, but the gateway already honors their
	// manifest no_auth declarations and preserves the caller's bearer token.
	declaredRoutes = append([]RouteSpec(nil), declaredRoutes...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			h.ServeHTTP(w, r)
			return
		}
		if expected == "" {
			// Dev mode — no token configured. Still serve, but warn.
			h.ServeHTTP(w, r)
			return
		}
		// Webhook pass-through. Handler is responsible for verifying
		// the provider's signature on the payload — see comment block
		// above.
		if strings.HasPrefix(r.URL.Path, "/webhooks/") {
			h.ServeHTTP(w, r)
			return
		}
		// Per-route NoAuth carve-out. Apps mark UPnP/DLNA wire
		// endpoints (or anything else where the LAN itself is the
		// auth boundary) by setting Route.NoAuth=true; mountAppRoutes
		// collected those paths into publicRoutePaths.
		if matchesPublicRoute(r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		for _, route := range declaredRoutes {
			if route.NoAuth && (route.Method == "" || strings.EqualFold(route.Method, r.Method)) &&
				matchesPublicRoutePatterns(r.URL.Path, []string{route.Prefix}) {
				h.ServeHTTP(w, r)
				return
			}
		}
		got := r.Header.Get("Authorization")
		if got != "Bearer "+expected {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// readConfigEnv decodes APTEVA_APP_CONFIG (JSON object of string → string)
// the platform injects at boot. Empty or missing → empty config.
func readConfigEnv() Config {
	raw := os.Getenv("APTEVA_APP_CONFIG")
	if raw == "" {
		return Config{}
	}
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		log.Printf("apteva-app: APTEVA_APP_CONFIG malformed (%v) — using empty", err)
		return Config{}
	}
	return c
}

// --- minimal MCP handler ----------------------------------------------------

type mcpHandler struct {
	tools     []Tool
	toolIndex map[string]int
	app       App
	ctx       *AppCtx
}

// HeaderBoundCallerInstallID is server-owned identity for calls traversing the
// authenticated app-to-app bridge. Network clients must never be allowed to
// supply it through an ordinary app MCP proxy.
const HeaderBoundCallerInstallID = "X-Apteva-Bound-Caller-Install-ID"
const HeaderBoundCallerAppName = "X-Apteva-Bound-Caller-App-Name"

// HeaderAppCallResult asks the platform callback bridge to unwrap the
// conventional MCP result before returning it to CallAppResult. It is only
// honored on an authenticated sidecar callback; it is not an agent-facing
// protocol feature.
const HeaderAppCallResult = "X-Apteva-App-Call-Result"

// AppResultJSON is a negotiated success-only direct JSON representation. An
// absent response header means legacy MCP (including all protocol/tool errors).
// It must never be inferred from JSON field names in application data.
const HeaderAppResultFormat = "X-Apteva-App-Result-Format"
const AppResultJSON = "json-v1"

func newMCPHandler(app App, ctx *AppCtx) *mcpHandler {
	tools := app.MCPTools()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	toolIndex := make(map[string]int, len(tools))
	for i := range tools {
		toolIndex[tools[i].Name] = i
	}
	return &mcpHandler{tools: tools, toolIndex: toolIndex, app: app, ctx: ctx}
}

// callTool is the single tool execution path shared by external MCP and the
// authenticated internal batch transport. Keeping exposure, grant, project,
// context and encoding logic here prevents the fast path from weakening or
// drifting away from ordinary app-to-app calls.
func (h *mcpHandler) callTool(ctx context.Context, caller *Caller, boundApp bool, name string, args map[string]any) ([]byte, *mcpError) {
	index, ok := h.toolIndex[name]
	if !ok {
		return nil, &mcpError{Code: -32601, Message: "tool not found: " + name}
	}
	matched := &h.tools[index]
	if h.toolExposure(*matched) == ToolExposureAppOnly && !boundApp {
		// Deliberately match the unknown-tool response. Agent and direct MCP
		// clients must not be able to enumerate private app APIs.
		return nil, &mcpError{Code: -32601, Message: "tool not found: " + name}
	}
	if args == nil {
		args = map[string]any{}
	}
	if caller != nil {
		spec := h.toolSpec(name)
		if spec != nil && spec.Requires != "" {
			resource, err := substituteResource(spec.ResourceFrom, args)
			if err != nil {
				return nil, &mcpError{Code: -32602, Message: err.Error()}
			}
			if !caller.Allows(spec.Requires, resource) {
				return nil, &mcpError{Code: -32000, Message: (&ErrForbidden{Permission: spec.Requires, Resource: resource}).Error()}
			}
		}
	}

	callCtx := WithCaller(ctx, caller)
	handlerCtx := h.ctx
	if pid, ok := args["_project_id"].(string); ok && pid != "" {
		handlerCtx = h.ctx.WithProject(pid)
	}
	var (
		res any
		err error
	)
	switch {
	case matched.HandlerCtx != nil:
		res, err = matched.HandlerCtx(callCtx, handlerCtx, args)
	case matched.Handler != nil:
		res, err = matched.Handler(handlerCtx, args)
	default:
		return nil, &mcpError{Code: -32603, Message: "tool " + name + ": no handler registered"}
	}
	if err != nil {
		return nil, &mcpError{Code: -32000, Message: err.Error()}
	}
	body, err := marshalJSONNoHTMLEscape(res)
	if err != nil {
		return nil, &mcpError{Code: -32000, Message: "encode result: " + err.Error()}
	}
	return body, nil
}

type mcpRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type mcpResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *mcpError `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *mcpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if raw := r.Header.Get(HeaderAppCallDeadline); raw != "" && r.Header.Get(HeaderBoundCallerInstallID) != "" {
		millis, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "invalid app deadline", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithDeadline(r.Context(), time.UnixMilli(millis))
		defer cancel()
		r = r.WithContext(ctx)
		if ctx.Err() != nil {
			http.Error(w, "app deadline exceeded", http.StatusGatewayTimeout)
			return
		}
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req mcpRequest
	decoder := json.NewDecoder(r.Body)
	if policy, ok := h.app.(JSONNumberPreservingApp); ok && policy.PreserveJSONNumbers() {
		decoder.UseNumber()
	}
	if err := decoder.Decode(&req); err != nil {
		writeMCPErr(w, nil, -32700, "parse error")
		return
	}
	switch req.Method {
	case "initialize":
		// MCP handshake — clients (apteva-core's MCP transport
		// included) send this first and refuse to load tools when
		// the server returns -32601. Reply with the standard
		// initialize response so tools/list works on the next call.
		writeMCP(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]any{
				"name":    h.ctx.Manifest().Name,
				"version": h.ctx.Manifest().Version,
			},
		})

	case "notifications/initialized":
		// Notification, no response expected. Some clients send it
		// after `initialize`; just ack with an empty success.
		writeMCP(w, req.ID, map[string]any{})

	case "tools/list":
		out := make([]map[string]any, 0, len(h.tools))
		for _, t := range h.tools {
			if h.toolExposure(t) == ToolExposureAppOnly {
				continue
			}
			entry := map[string]any{
				"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema,
			}
			meta := h.toolMeta(t)
			if len(meta) > 0 {
				entry["_meta"] = meta
			}
			out = append(out, entry)
		}
		writeMCP(w, req.ID, map[string]any{"tools": out})

	case "tools/call":
		name, _ := req.Params["name"].(string)
		args, _ := req.Params["arguments"].(map[string]any)
		body, callErr := h.callTool(r.Context(), h.buildCaller(r), strings.TrimSpace(r.Header.Get(HeaderBoundCallerInstallID)) != "", name, args)
		if callErr != nil {
			writeMCPErr(w, req.ID, callErr.Code, callErr.Message)
			return
		}
		if r.Header.Get(HeaderAppResultFormat) == AppResultJSON && r.Header.Get(HeaderBoundCallerInstallID) != "" {
			w.Header().Set(HeaderAppResultFormat, AppResultJSON)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		writeMCP(w, req.ID, map[string]any{"content": []map[string]any{
			{"type": "text", "text": string(body)},
		}})

	default:
		writeMCPErr(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// buildCaller materializes a Caller from server-owned request headers.
// It returns nil only when neither an agent nor delegated-user identity
// is present. Apps can therefore fail closed for delegated users without
// trusting caller-supplied tool arguments.
//
// X-Apteva-Caller-Agent is the canonical post-rename header name;
// the legacy X-Apteva-Caller-Instance still works during the
// deprecation window. Whichever the upstream forwards wins.
func (h *mcpHandler) buildCaller(r *http.Request) *Caller {
	raw := r.Header.Get("X-Apteva-Caller-Agent")
	if raw == "" {
		raw = r.Header.Get("X-Apteva-Caller-Instance")
	}
	subjectType := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-Type"))
	subjectID := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-ID"))
	subjectEmail := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-Email"))
	organizationID := strings.TrimSpace(r.Header.Get("X-Apteva-Organization-ID"))
	organizationSlug := strings.TrimSpace(r.Header.Get("X-Apteva-Organization-Slug"))
	projectID := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	if r.Header.Get(HeaderTrustedPrincipal) != "" || r.Header.Get(HeaderTrustedPrincipalSignature) != "" {
		// When the signed representation is present it is authoritative. Never
		// fall back to unsigned subject headers if verification fails.
		subjectType, subjectID, subjectEmail, organizationID, organizationSlug, projectID = "", "", "", "", "", ""
		if principal, err := PrincipalFromRequest(r); err == nil && principal != nil {
			subjectType = principal.SubjectType
			subjectID = principal.SubjectID
			subjectEmail = principal.SubjectEmail
			organizationID = principal.OrganizationID
			organizationSlug = principal.OrganizationSlug
			projectID = principal.ProjectID
			if subjectType == "" && principal.UserID > 0 {
				subjectType = "user"
				subjectID = strconv.FormatInt(principal.UserID, 10)
				subjectEmail = principal.Email
			}
		}
	}
	boundAppID, _ := strconv.ParseInt(strings.TrimSpace(r.Header.Get(HeaderBoundCallerInstallID)), 10, 64)
	boundAppName := strings.TrimSpace(r.Header.Get(HeaderBoundCallerAppName))
	if raw == "" && (subjectType == "" || subjectID == "") && (boundAppID <= 0 || boundAppName == "") {
		return nil
	}
	var (
		id   int64
		resp = &GrantsResponse{DefaultEffect: "allow"}
	)
	if raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return nil
		}
		id = parsed
		if grants, _ := h.ctx.platform.GetGrants(id); grants != nil {
			resp = grants
		}
	}
	return &Caller{
		AgentID:          id,
		ThreadID:         strings.TrimSpace(r.Header.Get("X-Apteva-Caller-Thread")),
		ThreadRole:       strings.TrimSpace(r.Header.Get("X-Apteva-Caller-Thread-Role")),
		ToolCallID:       strings.TrimSpace(r.Header.Get("X-Apteva-Tool-Call-ID")),
		ProjectID:        projectID,
		AppInstallID:     boundAppID,
		AppName:          boundAppName,
		SubjectType:      subjectType,
		SubjectID:        subjectID,
		SubjectEmail:     subjectEmail,
		OrganizationID:   organizationID,
		OrganizationSlug: organizationSlug,
		Grants:           resp.Grants,
		DefaultEffect:    resp.DefaultEffect,
		Resources:        h.ctx.Manifest().Provides.Resources,
	}
}

func (h *mcpHandler) toolMeta(tool Tool) map[string]any {
	merged := make(map[string]any, len(tool.Meta))
	for key, value := range tool.Meta {
		merged[key] = value
	}
	if spec := h.toolSpec(tool.Name); spec != nil {
		for key, value := range spec.Meta {
			if _, exists := merged[key]; !exists {
				merged[key] = value
			}
		}
	}
	return merged
}

// toolSpec finds the manifest spec for a runtime tool by name.
// Returns nil when the manifest doesn't list it (e.g. dynamic tools
// not declared up front) — those tools skip the gate.
func (h *mcpHandler) toolSpec(name string) *MCPToolSpec {
	specs := h.ctx.Manifest().Provides.MCPTools
	for i := range specs {
		if specs[i].Name == name {
			return &specs[i]
		}
	}
	return nil
}

func (h *mcpHandler) toolExposure(tool Tool) ToolExposure {
	if tool.Exposure != "" {
		return tool.Exposure
	}
	if spec := h.toolSpec(tool.Name); spec != nil && spec.Exposure != "" {
		return spec.Exposure
	}
	return ToolExposurePublic
}

func writeMCP(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = encodeJSONNoHTMLEscape(w, mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeMCPErr(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = encodeJSONNoHTMLEscape(w, mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: msg}})
}

func marshalJSONNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeJSONNoHTMLEscape(&buf, v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func encodeJSONNoHTMLEscape(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// watchDBInode logs a critical warning whenever the DB file's inode
// changes — i.e. somebody replaced the file under us. The connection
// pool drains itself via SetConnMaxLifetime; this watchdog exists so
// the swap event is loud + observable instead of showing up as
// silent SQLITE_READONLY_DBMOVED errors on the next write.
//
// Runs forever once the DB is open. Cheap (one stat every 30s) and
// the goroutine's lifetime is bounded by the process. No-ops when
// the path can't be stat'd (file gone temporarily during an atomic
// rename); the next tick will catch the replacement.
func watchDBInode(path string, logger Logger) {
	watchDBInodeChanges(path, 30*time.Second, logger, nil, nil)
}

func watchDBInodeChanges(path string, interval time.Duration, logger Logger, stop <-chan struct{}, onChange func()) {
	original, ok := statInode(path)
	if !ok {
		return
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		current, ok := statInode(path)
		if !ok {
			continue
		}
		if current != original {
			if logger != nil {
				logger.Warn("DB file inode changed underneath running sidecar; check for backup restore / deploy script / volume remount that replaced the file",
					"path", path, "original_inode", original, "current_inode", current)
			}
			if onChange != nil {
				onChange()
			}
			// Update so we only log once per swap (next swap re-fires).
			original = current
		}
	}
}

// --- DB open + migrations ---------------------------------------------------

const defaultSQLiteReadConns = 4

type appDatabases struct {
	writer    *sql.DB
	reader    *sql.DB
	state     *appDBState
	stopWatch chan struct{}
	closeOnce sync.Once
}

func (d *appDatabases) Close() error {
	if d == nil {
		return nil
	}
	var first error
	d.closeOnce.Do(func() {
		close(d.stopWatch)
		if d.reader != nil {
			first = d.reader.Close()
		}
		if d.writer != nil {
			if err := d.writer.Close(); first == nil {
				first = err
			}
		}
	})
	return first
}

func openAppDatabases(cfg *DBConfig, logger Logger) (*appDatabases, error) {
	return openAppDatabasesContext(context.Background(), cfg, logger)
}
func openAppDatabasesContext(ctx context.Context, cfg *DBConfig, logger Logger) (*appDatabases, error) {
	writer, err := openAppWriterDBContext(ctx, cfg, logger, false)
	if err != nil {
		return nil, err
	}
	path := appDBPath(cfg)
	reader, err := openAppReadDBContext(ctx, path, defaultSQLiteReadConns)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	state := &appDBState{}
	state.generation.Store(1)
	dbs := &appDatabases{
		writer: writer, reader: reader, state: state, stopWatch: make(chan struct{}),
	}
	go watchDBInodeChanges(path, 5*time.Second, logger, dbs.stopWatch, dbs.handleReplacement)
	return dbs, nil
}

func (d *appDatabases) handleReplacement() {
	if d == nil {
		return
	}
	d.state.generation.Add(1)
	forceRecycleDBPool(d.writer, 1)
	forceRecycleDBPool(d.reader, defaultSQLiteReadConns)
}

func appDBPath(cfg *DBConfig) string {
	if v := os.Getenv("DB_PATH"); v != "" {
		return v
	}
	return cfg.Path
}

func openAppReadDB(path string, maxConns int) (*sql.DB, error) {
	return openAppReadDBContext(context.Background(), path, maxConns)
}
func openAppReadDBContext(ctx context.Context, path string, maxConns int) (*sql.DB, error) {
	if maxConns < 1 {
		maxConns = defaultSQLiteReadConns
	}
	db, err := sql.Open("sqlite", "file:"+path+
		"?mode=ro"+
		"&_pragma=query_only(on)"+
		"&_pragma=busy_timeout(30000)"+
		"&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open read-only app db %s: %w", path, err)
	}
	var queryOnly, busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify read-only app db %s: %w", path, err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify read-only app db %s: %w", path, err)
	}
	if queryOnly != 1 || busyTimeout != 30000 {
		_ = db.Close()
		return nil, fmt.Errorf("verify read-only app db %s: query_only=%d busy_timeout=%d", path, queryOnly, busyTimeout)
	}
	return db, nil
}

func forceRecycleDBPool(db *sql.DB, maxIdle int) {
	if db == nil {
		return
	}
	// Closing every idle connection is immediate. Active connections are
	// discarded as soon as they return because their lifetime is now one
	// nanosecond. Once the old pool fully drains, restore the normal lifetime
	// and idle capacity so a live restore does not cause permanent churn.
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(time.Nanosecond)
	reset := func() {
		db.SetConnMaxLifetime(5 * time.Minute)
		db.SetMaxIdleConns(maxIdle)
	}
	if db.Stats().InUse == 0 {
		reset()
		return
	}
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.NewTimer(30 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case <-ticker.C:
				if db.Stats().InUse == 0 {
					reset()
					return
				}
			case <-timeout.C:
				// Keep aggressive recycling if a continuously used stale
				// connection never drains; correctness wins over reuse.
				return
			}
		}
	}()
}

func openAppDB(cfg *DBConfig, logger Logger) (*sql.DB, error) {
	return openAppWriterDB(cfg, logger, true)
}

func openAppWriterDB(cfg *DBConfig, logger Logger, startWatcher bool) (*sql.DB, error) {
	return openAppWriterDBContext(context.Background(), cfg, logger, startWatcher)
}
func openAppWriterDBContext(ctx context.Context, cfg *DBConfig, logger Logger, startWatcher bool) (*sql.DB, error) {
	if cfg.Driver != "sqlite" && cfg.Driver != "" {
		return nil, fmt.Errorf("only sqlite supported in this SDK; got %q", cfg.Driver)
	}
	// DB_PATH env wins over the manifest's path. The platform sets
	// it per-install to avoid two installs of the same app writing
	// to the manifest's hard-coded path. The testkit also relies on
	// this so spawned sidecars in tests don't share /data/<app>.db.
	path := appDBPath(cfg)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	// Safety pragmas must use modernc.org/sqlite's `_pragma=` DSN syntax —
	// the mattn/go-sqlite3 forms `_journal_mode=…` and `_foreign_keys=…` are
	// silently ignored by modernc, which is how `ON DELETE CASCADE` and WAL
	// were no-ops in prod for months before this was caught. `_pragma=` runs
	// the listed pragma on every new pool connection, which matters because
	// `foreign_keys` and `busy_timeout` are per-connection state in SQLite.
	//
	// busy_timeout bumped from 5_000ms → 30_000ms in v0.25.0. Storage +
	// media in particular had a steady drip of SQLITE_BUSY surfacing as
	// agent-visible "retry" steps when concurrent writers (event handler
	// + periodic ticker + agent tool calls) all hit the per-app DB at
	// once. 5s was tight under heavy bursts; 30s absorbs the realistic
	// worst case (a slow write while a multi-row UPDATE runs) without
	// changing app-level retry behaviour. A genuinely deadlocked DB
	// would just take 30s instead of 5s to surface — acceptable for the
	// rare case in exchange for invisible-to-the-operator contention
	// in the common one.
	db, err := sql.Open("sqlite", "file:"+path+
		"?_pragma=journal_mode(WAL)"+
		"&_pragma=busy_timeout(30000)"+
		"&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	// Verify the pragmas actually took. The DSN claims them, but if the
	// driver is swapped, the syntax drifts, or someone refactors the open
	// path, we want a loud failure here rather than another months-long
	// silent no-op. Each PRAGMA query may land on a different pool
	// connection — that's fine, all conns are configured the same way.
	if err := assertSQLitePragmas(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("openAppDB %s: %w", path, err)
	}
	// Serialize ALL access through a single connection. SQLite permits
	// exactly one writer at a time (even in WAL); database/sql otherwise
	// opens an unbounded pool under concurrency, so an app's event
	// handler + periodic ticker + agent tool call can each grab their
	// own connection and contend on the single-writer lock — the losers
	// wait up to busy_timeout, then surface SQLITE_BUSY as agent-visible
	// "retry" steps. With MaxOpenConns(1), writes queue in Go and take
	// their turn instead of racing the lock. The 30s busy_timeout above
	// stays as belt-and-suspenders for the cross-process case (a backup
	// restore touching the file mid-write).
	//
	// Compatible with the conn-recycling below: a single connection is
	// still recycled by SetConnMaxLifetime, so the inode-poisoning
	// recovery (SQLITE_READONLY_DBMOVED) keeps working — the lone conn
	// drains within the lifetime cap.
	db.SetMaxOpenConns(1)

	// Recycle pooled connections so they can't pin a stale inode forever.
	//
	// Background: when something (a backup-app live restore, an rsync, a
	// volume remount, a manual cp) replaces the DB file under the running
	// sidecar, every existing connection becomes poisoned and SQLite
	// returns SQLITE_READONLY_DBMOVED (extended code 1032) on every write.
	// The connection pool happily hands out the poisoned conns forever
	// because nothing about them looks broken to database/sql — Ping
	// passes, only writes fail.
	//
	// Capping the lifetime at 5 minutes means a poisoned pool fully drains
	// within at most 5 minutes of the swap; new connections are opened by
	// modernc.org/sqlite via a fresh open(2), which resolves the path to
	// the new inode. Idle cap of 2 minutes makes sure even quiet apps
	// recycle in a reasonable window.
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	// SQLite's busy handler does not promptly observe context cancellation.
	// During initialization keep one connection, with a short lock wait; restore
	// normal runtime settings only after OnMount completes.
	if ctx.Done() != nil {
		db.SetConnMaxLifetime(0)
		db.SetConnMaxIdleTime(0)
		if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=1000"); err != nil {
			db.Close()
			return nil, err
		}
	}
	// Inode watchdog. Every 30s, stat the DB path and compare the inode
	// to the one captured at open time. When the inode changes the pool
	// is poisoned (see SQLITE_READONLY_DBMOVED above); the conn-lifetime
	// cap will drain it within ~5 min, but we want loud + immediate
	// signal so operators can correlate the swap. We log a critical
	// warning rather than crashing so in-flight requests can complete;
	// the pool recycles itself within the cap window.
	if startWatcher {
		go watchDBInode(path, logger)
	}
	migrationsDir := cfg.Migrations
	// APTEVA_MIGRATIONS_DIR overrides the manifest path the same way
	// DB_PATH overrides cfg.Path. The platform points it at the absolute
	// migrations dir inside the cloned source tree so apps don't have to
	// know where their source landed on disk — relative manifest paths
	// like "migrations/" only work when CWD happens to be the source
	// dir, which it isn't for spawned sidecars.
	if v := os.Getenv("APTEVA_MIGRATIONS_DIR"); v != "" {
		migrationsDir = v
	}
	if migrationsDir != "" {
		if err := runMigrationsContext(ctx, db, migrationsDir, logger); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// assertSQLitePragmas reads back the safety pragmas openAppDB tries to
// set and returns an error if any didn't take. Catches the class of
// bugs where DSN syntax silently mismatches the driver — historically
// modernc.org/sqlite ignored every mattn-style `_journal_mode=…`,
// `_busy_timeout=…`, `_foreign_keys=…` query param in our DSN, so FK
// enforcement and WAL were both off in prod for months.
func assertSQLitePragmas(db *sql.DB) error {
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("PRAGMA journal_mode: %w", err)
	}
	if strings.ToLower(journal) != "wal" {
		return fmt.Errorf("journal_mode=%q, want wal", journal)
	}
	var busy int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		return fmt.Errorf("PRAGMA busy_timeout: %w", err)
	}
	if busy < 1000 {
		return fmt.Errorf("busy_timeout=%d, want >=1000ms", busy)
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("PRAGMA foreign_keys: %w", err)
	}
	if fk != 1 {
		return fmt.Errorf("foreign_keys=%d, want 1 — ON DELETE CASCADE will silently no-op", fk)
	}
	return nil
}

func runMigrations(db *sql.DB, dir string, logger Logger) error {
	return runMigrationsContext(context.Background(), db, dir, logger)
}
func runMigrationsContext(ctx context.Context, db *sql.DB, dir string, logger Logger) error {
	if err := retryStartupBusy(ctx, func() error {
		_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _migrations (
		filename TEXT PRIMARY KEY,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
		return err
	}); err != nil {
		return fmt.Errorf("create _migrations: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		var seen string
		err := db.QueryRowContext(ctx, "SELECT filename FROM _migrations WHERE filename = ?", f).Scan(&seen)
		if err == nil {
			continue // already applied
		}
		if err != sql.ErrNoRows {
			return err
		}
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		if err := retryStartupBusy(ctx, func() error { return applyMigrationContext(ctx, db, f, string(body)) }); err != nil {
			return err
		}
		logger.Info("applied migration", "file", f)
	}
	return nil
}

// --- default logger ---------------------------------------------------------

type defaultLogger struct{ tag string }

func newDefaultLogger(tag string) Logger { return &defaultLogger{tag: tag} }

func (l *defaultLogger) Info(msg string, kv ...any)  { l.emit("INFO", msg, kv) }
func (l *defaultLogger) Warn(msg string, kv ...any)  { l.emit("WARN", msg, kv) }
func (l *defaultLogger) Error(msg string, kv ...any) { l.emit("ERR", msg, kv) }

func (l *defaultLogger) emit(level, msg string, kv []any) {
	parts := []string{fmt.Sprintf("[%s] [%s] %s", level, l.tag, msg)}
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", kv[i], kv[i+1]))
	}
	log.Println(strings.Join(parts, " "))
}
