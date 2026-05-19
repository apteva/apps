// Computer v0.3 — first three MCP tools (browser_open / browser_screenshot
// / browser_close) on top of the existing UI surface.
//
// Sessions opened via these tools are owned by this sidecar: an
// in-memory map keyed by sidecar-generated session_id holds the
// computer.Computer value, and an idle reaper closes anything not
// touched in 30 minutes. Attaching to a session the agent opened in
// core is NOT yet supported — Browserbase/Steel resume comes with the
// next release, and local CDP attach needs core-side plumbing.
//
// The browser backends (local Chrome, Browserbase, Steel, Browser
// Engine) live in github.com/apteva/computer; we never duplicate any
// CDP / HTTP plumbing here. Backend choice comes from the per-call
// `backend` arg, falling back to APTEVA_BROWSER_BACKEND env, falling
// back to "local". Cloud credentials are read from env at open time
// (BROWSERBASE_API_KEY / BROWSERBASE_PROJECT_ID / STEEL_API_KEY) —
// the integration-bindings migration is a later refinement.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/computer"
	pkgcomputer "github.com/apteva/core/pkg/computer"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────
// Embedded so a built sidecar binary still self-describes if loaded
// without its source tree. Keep in sync with apteva.yaml — the
// platform reads the on-disk yaml at install time. main_test.go
// enforces drift on the load-bearing fields.

const manifestYAML = `schema: apteva-app/v1
name: computer
display_name: Computer
version: 0.3.3
description: |
  Watch and steer the agent's browser. v0.3 adds the first MCP tools
  (browser_open / browser_list / browser_screenshot / browser_close).
scopes: [project, global]
requires:
  permissions:
    - net.egress
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: browser_open
      description: "Open a browser session. Args: backend?, url?, viewport?. Returns {session_id, backend, current_url, width, height}."
    - name: browser_list
      description: "List sessions currently owned by this sidecar. Returns {sessions:[{session_id, backend, current_url, debug_url, opened_at, last_used_at}]}."
    - name: browser_screenshot
      description: "Capture a PNG of the session's current viewport. Args: session_id. Returns {png_b64, current_url, width, height}."
    - name: browser_close
      description: "Close a session opened by this app. Args: session_id. Idempotent."
  ui_panels:
    - slot: project.page
      label: Browsers
      icon: monitor
      entry: /ui/ComputerPanel.mjs
  ui_components:
    - name: browser-card
      entry: /ui/BrowserCard.mjs
      slots: [chat.message_attachment]
    - name: screenshot-with-som
      entry: /ui/ScreenshotCard.mjs
      slots: [chat.message_attachment]
    - name: live-view
      entry: /ui/LiveCard.mjs
      slots: [chat.message_attachment]
    - name: navigation-timeline
      entry: /ui/TimelineCard.mjs
      slots: [chat.message_attachment]
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/computer
  port: 8080
  health_check: /health
`

// newBackend is the factory the handlers use to construct a backend.
// Swapped by tests to inject a fake Computer without booting real
// Chrome / cloud sessions. Production path is backends.New verbatim.
var newBackend = backends.New

// idleTTL — sessions untouched for this long get reaped. Matches core's
// rough "agent abandoned the browser, free the resource" expectation;
// generous because cloud sessions (Browserbase/Steel) cost real money
// when leaked but a too-aggressive reaper would close mid-task sessions
// for callers that pause for human input.
const idleTTL = 30 * time.Minute
const reapInterval = 5 * time.Minute

// session is one open browser, owned by this sidecar.
type session struct {
	comp     pkgcomputer.Computer
	backend  string
	openedAt time.Time
	lastUsed time.Time
}

// registry holds open sessions across all callers in this sidecar
// process. The mutex protects the map only; per-session calls hit the
// underlying chromedp/CDP layers which serialize themselves.
type registry struct {
	mu sync.Mutex
	m  map[string]*session
}

func (r *registry) put(id string, s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[id] = s
}

func (r *registry) get(id string) (*session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[id]
	if ok {
		s.lastUsed = time.Now()
	}
	return s, ok
}

// remove returns the session (if any) and removes it from the map. The
// caller is responsible for closing it — keeping close-outside-the-lock
// avoids holding the registry mutex during slow tear-down.
func (r *registry) remove(id string) (*session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[id]
	if ok {
		delete(r.m, id)
	}
	return s, ok
}

// reapIdle closes and removes any session not touched within ttl.
// Returns the ids it reaped so the caller can log them.
func (r *registry) reapIdle(ttl time.Duration) []string {
	r.mu.Lock()
	cutoff := time.Now().Add(-ttl)
	var stale []*session
	var ids []string
	for id, s := range r.m {
		if s.lastUsed.Before(cutoff) {
			stale = append(stale, s)
			ids = append(ids, id)
			delete(r.m, id)
		}
	}
	r.mu.Unlock()
	for _, s := range stale {
		_ = s.comp.Close()
	}
	return ids
}

// ─── App ───────────────────────────────────────────────────────────

type App struct {
	reg *registry
}

// globalCtx is the AppCtx captured at OnMount. HTTP handlers need an
// AppCtx (logger, the same one the tool handlers use) and the SDK's
// Route.Handler is a plain http.HandlerFunc that doesn't carry one.
// Same pattern storage / screenshots / other sidecars use.
var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	a.reg = &registry{m: map[string]*session{}}
	globalCtx = ctx
	go a.reaper(ctx)
	ctx.Logger().Info("computer mounted", "tools", 4, "idle_ttl", idleTTL.String())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	// Best-effort close on shutdown. We don't lock for the whole sweep
	// — we're shutting down, racing is fine.
	if a.reg == nil {
		return nil
	}
	a.reg.mu.Lock()
	sessions := a.reg.m
	a.reg.m = map[string]*session{}
	a.reg.mu.Unlock()
	for _, s := range sessions {
		_ = s.comp.Close()
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// HTTPRoutes — three endpoints the dashboard panel uses to list +
// open + close sessions. UI bundles under /ui/* are served by the
// platform's static handler; /health is auto-registered by the SDK.
// All routes are reachable through the platform proxy at
// /api/apps/computer/<pattern>.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/sessions", Handler: a.handleListSessions},
		{Method: http.MethodPost, Pattern: "/sessions", Handler: a.handleOpenSession},
		{Method: http.MethodDelete, Pattern: "/sessions/{id}", Handler: a.handleCloseSession},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "browser_open",
			Description: "Open a new browser session. Args: backend? (local|browserbase|steel, default per APTEVA_BROWSER_BACKEND env then \"local\"), " +
				"url? (navigate after open), viewport? ({width:int, height:int}, default 1600x800). " +
				"Returns {session_id, backend, current_url, width, height}. " +
				"Session owned by this sidecar until browser_close or 30-minute idle reaper.",
			InputSchema: schemaObject(map[string]any{
				"backend": map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel"}},
				"url":     map[string]any{"type": "string"},
				"viewport": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"width":  map[string]any{"type": "integer"},
						"height": map[string]any{"type": "integer"},
					},
				},
			}, nil),
			Handler: a.toolBrowserOpen,
		},
		{
			Name:        "browser_list",
			Description: "List sessions currently owned by this sidecar. Returns {sessions:[{session_id, backend, current_url, debug_url, opened_at, last_used_at}]}.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolBrowserList,
		},
		{
			Name: "browser_screenshot",
			Description: "Capture a PNG of the session's current viewport. Args: session_id. " +
				"Returns {png_b64, current_url, width, height}. Full-page and SoM are not yet supported.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "string"},
			}, []string{"session_id"}),
			Handler: a.toolBrowserScreenshot,
		},
		{
			Name: "browser_close",
			Description: "Close a session opened by this app. Args: session_id. Idempotent — unknown ids return {closed:false}.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "string"},
			}, []string{"session_id"}),
			Handler: a.toolBrowserClose,
		},
	}
}

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolBrowserOpen(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	backend := stringArg(args, "backend")
	if backend == "" {
		backend = os.Getenv("APTEVA_BROWSER_BACKEND")
	}
	if backend == "" {
		backend = "local"
	}

	width, height := 0, 0
	if vp, ok := args["viewport"].(map[string]any); ok {
		width = intArg(vp, "width")
		height = intArg(vp, "height")
	}

	cfg := backendConfig(backend, width, height)
	comp, err := newBackend(cfg)
	if err != nil {
		return nil, fmt.Errorf("backend %q open failed: %w", backend, err)
	}
	if comp == nil {
		return nil, fmt.Errorf("backend %q unknown", backend)
	}

	opener, ok := comp.(pkgcomputer.SessionOpener)
	if !ok {
		_ = comp.Close()
		return nil, fmt.Errorf("backend %q does not support OpenSession", backend)
	}
	openOpts := pkgcomputer.OpenOptions{URL: stringArg(args, "url")}
	if err := opener.OpenSession(openOpts); err != nil {
		_ = comp.Close()
		return nil, fmt.Errorf("OpenSession: %w", err)
	}

	id := newSessionID()
	now := time.Now()
	a.reg.put(id, &session{
		comp:     comp,
		backend:  backend,
		openedAt: now,
		lastUsed: now,
	})

	disp := comp.DisplaySize()
	out := map[string]any{
		"session_id":  id,
		"backend":     backend,
		"current_url": currentURL(comp),
		"width":       disp.Width,
		"height":      disp.Height,
	}
	ctx.Logger().Info("browser_open", "session_id", id, "backend", backend)
	return out, nil
}

// sessionInfo is the shape each row in browser_list / /api/sessions
// reports. Kept tight: session_id + provenance + the URLs the
// operator needs to identify or open the session.
type sessionInfo struct {
	SessionID  string `json:"session_id"`
	Backend    string `json:"backend"`
	CurrentURL string `json:"current_url"`
	DebugURL   string `json:"debug_url,omitempty"`
	OpenedAt   string `json:"opened_at"`
	LastUsedAt string `json:"last_used_at"`
}

func (a *App) toolBrowserList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return map[string]any{"sessions": a.listSessions()}, nil
}

// listSessions snapshots the registry under the lock and projects
// each row into the public shape. Network/IO methods (DebugURL on
// browserbase reads cached state, local Chrome's reads chromedp
// state) are called outside the lock to avoid blocking other tools
// on a slow getter.
func (a *App) listSessions() []sessionInfo {
	type frozen struct {
		id      string
		comp    pkgcomputer.Computer
		backend string
		opened  time.Time
		used    time.Time
	}
	a.reg.mu.Lock()
	rows := make([]frozen, 0, len(a.reg.m))
	for id, s := range a.reg.m {
		rows = append(rows, frozen{id: id, comp: s.comp, backend: s.backend, opened: s.openedAt, used: s.lastUsed})
	}
	a.reg.mu.Unlock()

	out := make([]sessionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessionInfo{
			SessionID:  r.id,
			Backend:    r.backend,
			CurrentURL: currentURL(r.comp),
			DebugURL:   debugURL(r.comp),
			OpenedAt:   r.opened.UTC().Format(time.RFC3339),
			LastUsedAt: r.used.UTC().Format(time.RFC3339),
		})
	}
	return out
}

func (a *App) toolBrowserScreenshot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := stringArg(args, "session_id")
	if id == "" {
		return nil, fmt.Errorf("session_id required")
	}
	sess, ok := a.reg.get(id)
	if !ok {
		return nil, fmt.Errorf("session %s not found (may have been reaped or never opened by this sidecar)", id)
	}
	png, err := sess.comp.Screenshot()
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	disp := sess.comp.DisplaySize()
	return map[string]any{
		"png_b64":     base64.StdEncoding.EncodeToString(png),
		"current_url": currentURL(sess.comp),
		"width":       disp.Width,
		"height":      disp.Height,
	}, nil
}

func (a *App) toolBrowserClose(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := stringArg(args, "session_id")
	if id == "" {
		return nil, fmt.Errorf("session_id required")
	}
	sess, ok := a.reg.remove(id)
	if !ok {
		return map[string]any{"closed": false}, nil
	}
	if err := sess.comp.Close(); err != nil {
		ctx.Logger().Warn("browser_close underlying Close error", "session_id", id, "err", err.Error())
	}
	return map[string]any{"closed": true}, nil
}

// ─── Background reaper ─────────────────────────────────────────────

func (a *App) reaper(ctx *sdk.AppCtx) {
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ids := a.reg.reapIdle(idleTTL)
			for _, id := range ids {
				ctx.Logger().Info("reaped idle session", "session_id", id, "idle_ttl", idleTTL.String())
			}
		}
	}
}

// ─── Helpers ───────────────────────────────────────────────────────

func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "br_" + hex.EncodeToString(b[:])
}

func backendConfig(backend string, width, height int) backends.Config {
	cfg := backends.Config{Type: backend, Width: width, Height: height}
	switch backend {
	case "browserbase":
		cfg.APIKey = os.Getenv("BROWSERBASE_API_KEY")
		cfg.ProjectID = os.Getenv("BROWSERBASE_PROJECT_ID")
	case "steel":
		cfg.APIKey = os.Getenv("STEEL_API_KEY")
	}
	return cfg
}

func currentURL(c pkgcomputer.Computer) string {
	if si, ok := c.(pkgcomputer.SessionInfo); ok {
		return si.CurrentURL()
	}
	return ""
}

// debugURL pulls the backend's debug URL via the anonymous interface
// each concrete backend (local Chrome, browserbase, steel,
// browserengine) implements. Returns "" if the backend doesn't
// expose one. Operators use this to attach DevTools / open the
// vendor's live viewer.
func debugURL(c pkgcomputer.Computer) string {
	if dbg, ok := c.(interface{ DebugURL() string }); ok {
		return dbg.DebugURL()
	}
	return ""
}

func stringArg(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// ─── HTTP handlers ─────────────────────────────────────────────────

func (a *App) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sessions": a.listSessions()})
}

func (a *App) handleOpenSession(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
		httpErr(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	out, err := a.toolBrowserOpen(globalCtx, body)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/sessions/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		httpErr(w, http.StatusBadRequest, "session id required")
		return
	}
	out, err := a.toolBrowserClose(globalCtx, map[string]any{"session_id": id})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func main() { sdk.Run(&App{}) }
