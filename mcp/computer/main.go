// Computer app — MCP browser runtime.
//
// Sessions opened via these tools are owned by this sidecar: an
// in-memory map keyed by sidecar-generated session_id holds the
// browser.Computer value, and an idle reaper closes anything not
// touched in 30 minutes. Attaching to a session the agent opened in
// core is not needed in the app-only model: browser_session opens or
// resumes app-owned sessions, and computer_use drives them.
package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────
// Embedded so a built sidecar binary still self-describes if loaded
// without its source tree. Keep in sync with apteva.yaml — the
// platform reads the on-disk yaml at install time. main_test.go
// enforces drift on the load-bearing fields.

const manifestYAML = `schema: apteva-app/v1
name: computer
display_name: Computer
version: 0.7.4
description: |
  Watch and steer browser sessions. v0.7.4 fixes click coordinates
  in the enlarged object-fit browser preview.
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.read_credentials
  integrations:
    - role: browserbase
      kind: integration
      required: false
      compatible_slugs: [browserbase]
    - role: steel
      kind: integration
      required: false
      compatible_slugs: [steel]
    - role: browser-engine
      kind: integration
      required: false
      compatible_slugs: [browser-engine]
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: browser_session
      description: "Open, resume, list, inspect, or close app-owned browser sessions. Args: action, session_id?, backend?, backend_session_id?, url?, context_id?, persist?, timeout?, proxy?, proxy_country?, viewport?."
    - name: computer_use
      description: "Drive an app-owned browser session. Args: session_id, action, coordinate?, label?, text?, key?, direction?, amount?, duration?. Returns screenshot bytes for visual actions."
    - name: computer_context_create
      description: "Create or import an app-managed browser context. Args: name, backend?, provider_context_id?, persist_default?, metadata?, auto_create_provider?."
    - name: computer_context_list
      description: "List app-managed browser contexts. Args: backend?."
    - name: computer_context_get
      description: "Fetch one browser context by id or backend+name."
    - name: computer_context_update
      description: "Update browser context metadata/defaults. Args: id, name?, provider_context_id?, persist_default?, metadata?."
    - name: computer_context_delete
      description: "Delete or unlink an app-managed browser context. Args: id, delete_provider?."
    - name: browser_open
      description: "Compatibility alias for browser_session(action=open)."
    - name: browser_list
      description: "Compatibility alias for browser_session(action=list)."
    - name: browser_screenshot
      description: "Compatibility alias for computer_use(action=screenshot)."
    - name: browser_close
      description: "Compatibility alias for browser_session(action=close)."
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
db:
  driver: sqlite
  path: /data/computer.db
  migrations: migrations/
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
	comp         backends.Computer
	backend      string
	appContextID string
	contextName  string
	persist      bool
	openedAt     time.Time
	lastUsed     time.Time
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
	if ctx.AppDB() == nil {
		return errors.New("computer requires a db block")
	}
	a.reg = &registry{m: map[string]*session{}}
	globalCtx = ctx
	go a.reaper(ctx)
	ctx.Logger().Info("computer mounted", "tools", len(a.MCPTools()), "idle_ttl", idleTTL.String())
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

// HTTPRoutes — endpoints the dashboard panel uses to list, open,
// steer, and close sessions. UI bundles under /ui/* are served by the
// platform's static handler; /health is auto-registered by the SDK.
// All routes are reachable through the platform proxy at
// /api/apps/computer/<pattern>.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/contexts", Handler: a.handleContextsCollection},
		{Method: http.MethodPost, Pattern: "/contexts", Handler: a.handleContextsCollection},
		{Method: http.MethodGet, Pattern: "/contexts/{id}", Handler: a.handleContextItem},
		{Method: http.MethodPatch, Pattern: "/contexts/{id}", Handler: a.handleContextItem},
		{Method: http.MethodDelete, Pattern: "/contexts/{id}", Handler: a.handleContextItem},
		{Method: http.MethodGet, Pattern: "/sessions", Handler: a.handleListSessions},
		{Method: http.MethodPost, Pattern: "/sessions", Handler: a.handleOpenSession},
		{Method: http.MethodDelete, Pattern: "/sessions/{id}", Handler: a.handleCloseSession},
		{Method: http.MethodPost, Pattern: "/sessions/{id}/use", Handler: a.handleComputerUse},
		// /sessions/{id}/screenshot returns the raw PNG inline (not the
		// base64-wrapped MCP-tool shape) so the panel's <img src> can
		// poll it directly for a cheap "live" view.
		{Method: http.MethodGet, Pattern: "/sessions/{id}/screenshot", Handler: a.handleSessionScreenshot},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "browser_session",
			Description: "Session lifecycle for app-owned browsers. Actions: open, resume, status, close, list. " +
				"Open/resume args: backend? (local|browserbase|steel|browser-engine|service), url?, context_id?, persist?, " +
				"backend_session_id? (provider attach), timeout?, proxy?, proxy_country?, viewport?. " +
				"Returns {session_id, backend_session_id, backend, current_url, context_id, debug_url, width, height}.",
			InputSchema: schemaObject(map[string]any{
				"action":             map[string]any{"type": "string", "enum": []string{"open", "resume", "status", "close", "list"}},
				"session_id":         map[string]any{"type": "string", "description": "App session id for status/close; for resume, accepted as provider session id when backend_session_id is omitted."},
				"backend_session_id": map[string]any{"type": "string", "description": "Provider session id to attach/resume for Browserbase or Browser Engine."},
				"backend":            map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel", "browser-engine", "service"}},
				"url":                map[string]any{"type": "string"},
				"context_id":         map[string]any{"type": "string", "description": "App context id preferred; legacy raw provider context ids still work."},
				"context_name":       map[string]any{"type": "string"},
				"provider_context_id": map[string]any{
					"type": "string",
				},
				"auto_create_context": map[string]any{"type": "boolean"},
				"persist":             map[string]any{"type": "boolean"},
				"timeout":             map[string]any{"type": "integer"},
				"proxy":               map[string]any{"type": "boolean"},
				"proxy_country":       map[string]any{"type": "string"},
				"viewport": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"width":  map[string]any{"type": "integer"},
						"height": map[string]any{"type": "integer"},
					},
				},
			}, []string{"action"}),
			Handler: a.toolBrowserSession,
		},
		{
			Name: "computer_use",
			Description: "Drive a browser session opened by browser_session. Actions: screenshot, click, double_click, type, key, scroll, wait. " +
				"Args: session_id, action, coordinate? (\"x,y\"), label? (SoM label), text?, key?, direction?, amount?, duration?. " +
				"Returns a binary screenshot envelope plus current_url, width, height.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "string"},
				"action":     map[string]any{"type": "string", "enum": []string{"screenshot", "click", "double_click", "type", "key", "scroll", "wait"}},
				"coordinate": map[string]any{"type": "string"},
				"label":      map[string]any{"type": "integer"},
				"text":       map[string]any{"type": "string"},
				"key":        map[string]any{"type": "string"},
				"direction":  map[string]any{"type": "string"},
				"amount":     map[string]any{"type": "integer"},
				"duration":   map[string]any{"type": "integer"},
			}, []string{"session_id", "action"}),
			Handler: a.toolComputerUse,
		},
		{
			Name:        "computer_context_create",
			Description: "Create or import an app-managed browser context. Browserbase creates a provider Context immediately; local/browser-engine use the app id as provider id; Steel materializes the profile id on first persisted session.",
			InputSchema: schemaObject(map[string]any{
				"name":                 map[string]any{"type": "string"},
				"backend":              map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel", "browser-engine"}},
				"provider_context_id":  map[string]any{"type": "string"},
				"persist_default":      map[string]any{"type": "boolean"},
				"metadata":             map[string]any{"type": "object"},
				"auto_create_provider": map[string]any{"type": "boolean"},
			}, []string{"name"}),
			Handler: a.toolContextCreate,
		},
		{
			Name:        "computer_context_list",
			Description: "List app-managed browser contexts. Args: backend?.",
			InputSchema: schemaObject(map[string]any{
				"backend": map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel", "browser-engine"}},
			}, nil),
			Handler: a.toolContextList,
		},
		{
			Name:        "computer_context_get",
			Description: "Fetch one app-managed browser context by id, or by backend+name.",
			InputSchema: schemaObject(map[string]any{
				"id":      map[string]any{"type": "string"},
				"backend": map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel", "browser-engine"}},
				"name":    map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolContextGet,
		},
		{
			Name:        "computer_context_update",
			Description: "Update context name, provider id, persist default, or metadata. Args: id, name?, provider_context_id?, persist_default?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"id":                  map[string]any{"type": "string"},
				"name":                map[string]any{"type": "string"},
				"provider_context_id": map[string]any{"type": "string"},
				"persist_default":     map[string]any{"type": "boolean"},
				"metadata":            map[string]any{"type": "object"},
			}, []string{"id"}),
			Handler: a.toolContextUpdate,
		},
		{
			Name:        "computer_context_delete",
			Description: "Delete or unlink an app-managed browser context. Args: id, delete_provider? (Browserbase only; default false).",
			InputSchema: schemaObject(map[string]any{
				"id":              map[string]any{"type": "string"},
				"delete_provider": map[string]any{"type": "boolean"},
			}, []string{"id"}),
			Handler: a.toolContextDelete,
		},
		{
			Name: "browser_open",
			Description: "Compatibility alias for browser_session(action=open). Args: backend? (local|browserbase|steel|browser-engine, default per APTEVA_BROWSER_BACKEND env then \"local\"), " +
				"url? (navigate after open), viewport? ({width:int, height:int}, default 1600x800). " +
				"Returns {session_id, backend, current_url, width, height}. " +
				"Session owned by this sidecar until browser_close or 30-minute idle reaper.",
			InputSchema: schemaObject(map[string]any{
				"backend":      map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel", "browser-engine", "service"}},
				"url":          map[string]any{"type": "string"},
				"context_id":   map[string]any{"type": "string"},
				"context_name": map[string]any{"type": "string"},
				"provider_context_id": map[string]any{
					"type": "string",
				},
				"auto_create_context": map[string]any{"type": "boolean"},
				"persist":             map[string]any{"type": "boolean"},
				"timeout":             map[string]any{"type": "integer"},
				"proxy":               map[string]any{"type": "boolean"},
				"proxy_country":       map[string]any{"type": "string"},
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
				"Returns {png_b64, current_url, width, height}.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "string"},
			}, []string{"session_id"}),
			Handler: a.toolBrowserScreenshot,
		},
		{
			Name:        "browser_close",
			Description: "Close a session opened by this app. Args: session_id. Idempotent — unknown ids return {closed:false}.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "string"},
			}, []string{"session_id"}),
			Handler: a.toolBrowserClose,
		},
	}
}

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolContextCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rec, err := a.createContextRecord(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"context": rec}, nil
}

func (a *App) toolContextList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := appDB(ctx)
	if db == nil {
		return nil, fmt.Errorf("context catalog unavailable")
	}
	rows, err := dbListContexts(db, stringArg(args, "backend"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"contexts": rows}, nil
}

func (a *App) toolContextGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := appDB(ctx)
	if db == nil {
		return nil, fmt.Errorf("context catalog unavailable")
	}
	var rec *ComputerContext
	var err error
	if id := stringArg(args, "id"); id != "" {
		rec, err = dbGetContext(db, id)
	} else {
		backend := stringArg(args, "backend")
		if backend == "" {
			backend = "local"
		}
		name := stringArg(args, "name")
		if name == "" {
			return nil, fmt.Errorf("id or name required")
		}
		rec, err = dbGetContextByName(db, backend, name)
	}
	if err != nil {
		if errors.Is(err, errContextNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"found": true, "context": rec}, nil
}

func (a *App) toolContextUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := appDB(ctx)
	if db == nil {
		return nil, fmt.Errorf("context catalog unavailable")
	}
	id := stringArg(args, "id")
	if id == "" {
		return nil, fmt.Errorf("id required")
	}
	fields := map[string]any{}
	if name := strings.TrimSpace(stringArg(args, "name")); name != "" {
		fields["name"] = name
	}
	if providerID := stringArg(args, "provider_context_id"); providerID != "" {
		fields["provider_context_id"] = providerID
	}
	if v, ok := boolArg(args, "persist_default"); ok {
		fields["persist_default"] = v
	}
	if raw, ok := args["metadata"]; ok {
		metadataJSON, err := marshalMetadata(raw)
		if err != nil {
			return nil, err
		}
		fields["metadata_json"] = metadataJSON
	}
	rec, err := dbUpdateContext(db, id, fields)
	if err != nil {
		return nil, err
	}
	return map[string]any{"context": rec}, nil
}

func (a *App) toolContextDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := appDB(ctx)
	if db == nil {
		return nil, fmt.Errorf("context catalog unavailable")
	}
	id := stringArg(args, "id")
	if id == "" {
		return nil, fmt.Errorf("id required")
	}
	rec, err := dbGetContext(db, id)
	if err != nil {
		if errors.Is(err, errContextNotFound) {
			return map[string]any{"deleted": false}, nil
		}
		return nil, err
	}
	providerDeleted := false
	if boolArgDefault(args, "delete_provider", false) && rec.ProviderContextID != "" {
		if err := deleteProviderContext(ctx, rec.Backend, rec.ProviderContextID); err != nil {
			return nil, err
		}
		providerDeleted = true
	}
	if err := dbDeleteContext(db, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "provider_deleted": providerDeleted}, nil
}

func (a *App) createContextRecord(ctx *sdk.AppCtx, args map[string]any) (*ComputerContext, error) {
	db := appDB(ctx)
	if db == nil {
		return nil, fmt.Errorf("context catalog unavailable")
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return nil, fmt.Errorf("name required")
	}
	backend := stringArg(args, "backend")
	if backend == "" {
		backend = os.Getenv("APTEVA_BROWSER_BACKEND")
	}
	backend = normalizeBackend(backend)
	providerID := stringArg(args, "provider_context_id")
	persistDefault := boolArgDefault(args, "persist_default", true)
	autoCreateProvider := boolArgDefault(args, "auto_create_provider", true)

	if providerID == "" && autoCreateProvider {
		id, err := createProviderContext(ctx, backend)
		if err != nil {
			return nil, err
		}
		providerID = id
	}
	if providerID == "" && (backend == "local" || backend == "browser-engine") {
		providerID = newContextID()
	}

	metadataJSON, err := marshalMetadata(args["metadata"])
	if err != nil {
		return nil, err
	}
	autoCreated := boolArgDefault(args, "auto_created", false)
	return dbCreateContext(db, contextCreateInput{
		Name:              name,
		Backend:           backend,
		ProviderContextID: providerID,
		PersistDefault:    persistDefault,
		AutoCreated:       autoCreated,
		MetadataJSON:      metadataJSON,
	})
}

func (a *App) toolBrowserSession(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	action := stringArg(args, "action")
	if action == "" {
		return nil, fmt.Errorf("action required")
	}
	switch action {
	case "open":
		return a.openBrowserSession(ctx, args, false)
	case "resume":
		return a.openBrowserSession(ctx, args, true)
	case "list":
		return map[string]any{"sessions": a.listSessions()}, nil
	case "status":
		id := stringArg(args, "session_id")
		if id == "" {
			return nil, fmt.Errorf("session_id required")
		}
		sess, ok := a.reg.get(id)
		if !ok {
			return nil, fmt.Errorf("session %s not found", id)
		}
		return a.sessionOutput(id, sess), nil
	case "close":
		return a.toolBrowserClose(ctx, args)
	default:
		return nil, fmt.Errorf("unknown browser_session action %q", action)
	}
}

func (a *App) toolBrowserOpen(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.openBrowserSession(ctx, args, false)
}

type resolvedContext struct {
	AppContextID         string
	ContextName          string
	Backend              string
	ProviderContextID    string
	Persist              bool
	CreateProviderOnOpen bool
}

func (a *App) resolveSessionContext(ctx *sdk.AppCtx, backend string, args map[string]any, resume bool) (resolvedContext, error) {
	persist := boolArgDefault(args, "persist", true)
	out := resolvedContext{Persist: persist}
	if resume {
		return out, nil
	}

	rawProviderID := firstNonEmpty(stringArg(args, "provider_context_id"), stringArg(args, "provider_context"))
	contextIDArg := stringArg(args, "context_id")
	contextName := strings.TrimSpace(stringArg(args, "context_name"))
	autoCreate := boolArgDefault(args, "auto_create_context", false)

	db := appDB(ctx)
	if db == nil {
		out.ProviderContextID = firstNonEmpty(rawProviderID, contextIDArg)
		return out, nil
	}

	var rec *ComputerContext
	var err error
	if contextIDArg != "" {
		rec, err = dbGetContext(db, contextIDArg)
		if err != nil && !errors.Is(err, errContextNotFound) {
			return out, fmt.Errorf("get context %q: %w", contextIDArg, err)
		}
		if err != nil {
			rec = nil
		}
	}
	if rec == nil && contextName != "" {
		rec, err = dbGetContextByName(db, backend, contextName)
		if err != nil && !errors.Is(err, errContextNotFound) {
			return out, fmt.Errorf("get context %q/%q: %w", backend, contextName, err)
		}
		if err != nil {
			rec = nil
		}
	}
	if rec == nil && rawProviderID != "" {
		rec, err = dbGetContextByProviderID(db, backend, rawProviderID)
		if err != nil && !errors.Is(err, errContextNotFound) {
			return out, fmt.Errorf("get provider context %q/%q: %w", backend, rawProviderID, err)
		}
		if err != nil {
			rec = nil
		}
	}
	if rec == nil && autoCreate {
		name := firstNonEmpty(contextName, contextIDArg)
		if name == "" {
			return out, fmt.Errorf("context_name required when auto_create_context=true")
		}
		rec, err = a.createContextRecord(ctx, map[string]any{
			"name":                 name,
			"backend":              backend,
			"provider_context_id":  rawProviderID,
			"persist_default":      persist,
			"auto_create_provider": true,
			"auto_created":         true,
		})
		if err != nil {
			return out, err
		}
	}
	if rec != nil {
		out.AppContextID = rec.ID
		out.ContextName = rec.Name
		out.Backend = rec.Backend
		out.ProviderContextID = rec.ProviderContextID
		out.Persist = boolArgDefault(args, "persist", rec.PersistDefault)
		if out.ProviderContextID == "" && backend == "steel" {
			out.CreateProviderOnOpen = true
		}
		return out, nil
	}

	out.ProviderContextID = firstNonEmpty(rawProviderID, contextIDArg)
	return out, nil
}

func (a *App) openBrowserSession(ctx *sdk.AppCtx, args map[string]any, resume bool) (any, error) {
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

	rc, err := a.resolveSessionContext(ctx, backend, args, resume)
	if err != nil {
		return nil, err
	}
	if rc.Backend != "" {
		backend = rc.Backend
	}

	cfg := backendConfig(ctx, args, backend, width, height)
	comp, err := newBackend(cfg)
	if err != nil {
		return nil, fmt.Errorf("backend %q open failed: %w", backend, err)
	}
	if comp == nil {
		return nil, fmt.Errorf("backend %q unknown", backend)
	}

	backendSessionID := firstNonEmpty(
		stringArg(args, "backend_session_id"),
		stringArg(args, "provider_session_id"),
	)
	if resume && backendSessionID == "" {
		// Compatibility with the old browser_session(resume,
		// session_id=provider-session) convention. Once the app
		// returns its own session_id, that id is used for status/close
		// and computer_use.
		backendSessionID = stringArg(args, "session_id")
	}
	openOpts := backends.OpenOptions{
		URL:           stringArg(args, "url"),
		ContextID:     rc.ProviderContextID,
		CreateContext: rc.CreateProviderOnOpen,
		Persist:       rc.Persist,
		SessionID:     backendSessionID,
		Timeout:       intArg(args, "timeout"),
		Proxy:         boolPtrArg(args, "proxy"),
		ProxyCountry:  stringArg(args, "proxy_country"),
	}
	if opener, ok := comp.(backends.SessionOpener); ok {
		if err := opener.OpenSession(openOpts); err != nil {
			_ = comp.Close()
			return nil, fmt.Errorf("OpenSession: %w", err)
		}
	} else {
		if openOpts.ContextID != "" || openOpts.SessionID != "" || openOpts.Proxy != nil {
			_ = comp.Close()
			return nil, fmt.Errorf("backend %q does not support context_id/session_id/proxy", backend)
		}
		if openOpts.URL != "" {
			if _, err := comp.Execute(backends.Action{Type: "navigate", URL: openOpts.URL}); err != nil {
				_ = comp.Close()
				return nil, fmt.Errorf("navigate: %w", err)
			}
		}
	}

	if rc.AppContextID != "" {
		providerID := contextID(comp)
		if providerID != "" && providerID != rc.ProviderContextID && ctx != nil && ctx.AppDB() != nil {
			updated, err := dbUpdateContext(ctx.AppDB(), rc.AppContextID, map[string]any{"provider_context_id": providerID})
			if err != nil {
				_ = comp.Close()
				return nil, fmt.Errorf("update context provider id: %w", err)
			}
			rc.ProviderContextID = updated.ProviderContextID
		}
		if ctx != nil {
			dbTouchContext(ctx.AppDB(), rc.AppContextID)
		}
	}

	id := newSessionID()
	now := time.Now()
	a.reg.put(id, &session{
		comp:         comp,
		backend:      backend,
		appContextID: rc.AppContextID,
		contextName:  rc.ContextName,
		persist:      rc.Persist,
		openedAt:     now,
		lastUsed:     now,
	})

	if ctx != nil {
		ctx.Logger().Info("browser_open", "session_id", id, "backend", backend)
	}
	return a.sessionOutput(id, &session{comp: comp, backend: backend, appContextID: rc.AppContextID, contextName: rc.ContextName, persist: rc.Persist, openedAt: now, lastUsed: now}), nil
}

// sessionInfo is the shape each row in browser_list / /api/sessions
// reports. Kept tight: session_id + provenance + the URLs the
// operator needs to identify or open the session.
type sessionInfo struct {
	SessionID        string `json:"session_id"`
	BackendSessionID string `json:"backend_session_id,omitempty"`
	Backend          string `json:"backend"`
	ContextID        string `json:"context_id,omitempty"`
	AppContextID     string `json:"app_context_id,omitempty"`
	ContextName      string `json:"context_name,omitempty"`
	Persist          bool   `json:"persist"`
	CurrentURL       string `json:"current_url"`
	DebugURL         string `json:"debug_url,omitempty"`
	StreamURL        string `json:"stream_url,omitempty"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	OpenedAt         string `json:"opened_at"`
	LastUsedAt       string `json:"last_used_at"`
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
		id           string
		comp         backends.Computer
		backend      string
		appContextID string
		contextName  string
		persist      bool
		opened       time.Time
		used         time.Time
	}
	a.reg.mu.Lock()
	rows := make([]frozen, 0, len(a.reg.m))
	for id, s := range a.reg.m {
		rows = append(rows, frozen{id: id, comp: s.comp, backend: s.backend, appContextID: s.appContextID, contextName: s.contextName, persist: s.persist, opened: s.openedAt, used: s.lastUsed})
	}
	a.reg.mu.Unlock()

	out := make([]sessionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.sessionInfo(r.id, &session{comp: r.comp, backend: r.backend, appContextID: r.appContextID, contextName: r.contextName, persist: r.persist, openedAt: r.opened, lastUsed: r.used}))
	}
	return out
}

func (a *App) sessionInfo(id string, s *session) sessionInfo {
	disp := s.comp.DisplaySize()
	return sessionInfo{
		SessionID:        id,
		BackendSessionID: backendSessionID(s.comp),
		Backend:          s.backend,
		ContextID:        contextID(s.comp),
		AppContextID:     s.appContextID,
		ContextName:      s.contextName,
		Persist:          s.persist,
		CurrentURL:       currentURL(s.comp),
		DebugURL:         debugURL(s.comp),
		StreamURL:        streamURL(s.comp),
		Width:            disp.Width,
		Height:           disp.Height,
		OpenedAt:         s.openedAt.UTC().Format(time.RFC3339),
		LastUsedAt:       s.lastUsed.UTC().Format(time.RFC3339),
	}
}

func (a *App) sessionOutput(id string, s *session) map[string]any {
	info := a.sessionInfo(id, s)
	return map[string]any{
		"session_id":         info.SessionID,
		"backend_session_id": info.BackendSessionID,
		"backend":            info.Backend,
		"context_id":         info.ContextID,
		"app_context_id":     info.AppContextID,
		"context_name":       info.ContextName,
		"persist":            info.Persist,
		"current_url":        info.CurrentURL,
		"debug_url":          info.DebugURL,
		"stream_url":         info.StreamURL,
		"width":              info.Width,
		"height":             info.Height,
		"opened_at":          info.OpenedAt,
		"last_used_at":       info.LastUsedAt,
	}
}

func (a *App) toolBrowserScreenshot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := a.toolComputerUse(ctx, mapWithDefault(args, "action", "screenshot"))
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	return map[string]any{
		"png_b64":     m["screenshot_b64"],
		"screenshot":  m["screenshot"],
		"current_url": m["current_url"],
		"width":       m["width"],
		"height":      m["height"],
	}, nil
}

func (a *App) toolComputerUse(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := stringArg(args, "session_id")
	if id == "" {
		return nil, fmt.Errorf("session_id required")
	}
	action := stringArg(args, "action")
	if action == "" {
		return nil, fmt.Errorf("action required")
	}
	if action == "navigate" {
		return nil, fmt.Errorf("use browser_session(action=open, url=...) to navigate")
	}
	sess, ok := a.reg.get(id)
	if !ok {
		return nil, fmt.Errorf("session %s not found (may have been reaped or never opened by this sidecar)", id)
	}

	act := backends.Action{
		Type:      action,
		Label:     intArg(args, "label"),
		Text:      stringArg(args, "text"),
		Key:       stringArg(args, "key"),
		Direction: stringArg(args, "direction"),
		Amount:    intArg(args, "amount"),
		Duration:  intArg(args, "duration"),
	}
	act.X, act.Y = coordinateArg(args)

	var shot []byte
	var err error
	if action == "screenshot" {
		shot, err = sess.comp.Screenshot()
	} else {
		shot, err = sess.comp.Execute(act)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	disp := sess.comp.DisplaySize()
	mime := imageMIME(shot)
	return map[string]any{
		"text":           fmt.Sprintf("Success: %s action completed. Screenshot attached.", action),
		"screenshot":     binaryEnvelope(shot, mime),
		"screenshot_b64": base64.StdEncoding.EncodeToString(shot),
		"mime_type":      mime,
		"session_id":     id,
		"current_url":    currentURL(sess.comp),
		"width":          disp.Width,
		"height":         disp.Height,
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
		if ctx != nil {
			ctx.Logger().Warn("browser_close underlying Close error", "session_id", id, "err", err.Error())
		}
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

func newContextID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ctx_" + hex.EncodeToString(b[:])
}

func appDB(ctx *sdk.AppCtx) *sql.DB {
	if ctx == nil {
		return nil
	}
	return ctx.AppDB()
}

func normalizeBackend(backend string) string {
	if backend == "" {
		backend = "local"
	}
	return backend
}

func marshalMetadata(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("metadata: %w", err)
	}
	if string(raw) == "null" {
		return "{}", nil
	}
	return string(raw), nil
}

func createProviderContext(ctx *sdk.AppCtx, backend string) (string, error) {
	switch backend {
	case "browserbase":
		return createBrowserbaseContext(ctx)
	case "local", "browser-engine":
		return newContextID(), nil
	case "steel":
		// Steel creates a profile id from the first session opened with
		// persistProfile=true. The app row is created now and updated
		// with that provider id after browser_session(open) returns.
		return "", nil
	default:
		return "", fmt.Errorf("backend %q does not support managed contexts", backend)
	}
}

func deleteProviderContext(ctx *sdk.AppCtx, backend, providerID string) error {
	switch backend {
	case "browserbase":
		return deleteBrowserbaseContext(ctx, providerID)
	case "local", "browser-engine", "steel":
		return fmt.Errorf("provider deletion is not implemented for backend %q; delete without delete_provider to unlink the app row", backend)
	default:
		return fmt.Errorf("backend %q does not support managed contexts", backend)
	}
}

func createBrowserbaseContext(ctx *sdk.AppCtx) (string, error) {
	fields := integrationFields(ctx, "browserbase")
	apiKey := firstNonEmpty(fields["api_key"], fields["BROWSERBASE_API_KEY"], os.Getenv("BROWSERBASE_API_KEY"))
	projectID := firstNonEmpty(fields["project_id"], fields["BROWSERBASE_PROJECT_ID"], os.Getenv("BROWSERBASE_PROJECT_ID"))
	if apiKey == "" {
		return "", fmt.Errorf("browserbase: api_key is required to create a context")
	}
	body := map[string]any{}
	if projectID != "" {
		body["projectId"] = projectID
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", "https://api.browserbase.com/v1/contexts", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BB-API-Key", apiKey)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("browserbase create context: HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("browserbase create context decode: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("browserbase create context: empty id")
	}
	return out.ID, nil
}

func deleteBrowserbaseContext(ctx *sdk.AppCtx, providerID string) error {
	fields := integrationFields(ctx, "browserbase")
	apiKey := firstNonEmpty(fields["api_key"], fields["BROWSERBASE_API_KEY"], os.Getenv("BROWSERBASE_API_KEY"))
	if apiKey == "" {
		return fmt.Errorf("browserbase: api_key is required to delete a context")
	}
	req, err := http.NewRequest("DELETE", "https://api.browserbase.com/v1/contexts/"+providerID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-BB-API-Key", apiKey)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("browserbase delete context: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func backendConfig(ctx *sdk.AppCtx, args map[string]any, backend string, width, height int) backends.Config {
	cfg := backends.Config{Type: backend, Width: width, Height: height}
	switch backend {
	case "browserbase":
		fields := integrationFields(ctx, "browserbase")
		cfg.APIKey = firstNonEmpty(fields["api_key"], fields["BROWSERBASE_API_KEY"], os.Getenv("BROWSERBASE_API_KEY"))
		cfg.ProjectID = firstNonEmpty(fields["project_id"], fields["BROWSERBASE_PROJECT_ID"], os.Getenv("BROWSERBASE_PROJECT_ID"))
		cfg.KeepAlive = boolArgDefault(args, "keep_alive", false)
		cfg.Region = stringArg(args, "region")
		cfg.Timeout = intArg(args, "timeout")
		if boolArgDefault(args, "solve_captchas", false) {
			cfg.SolveCaptchas = true
		}
	case "steel":
		fields := integrationFields(ctx, "steel")
		cfg.APIKey = firstNonEmpty(fields["token"], fields["api_key"], fields["STEEL_API_KEY"], os.Getenv("STEEL_API_KEY"))
		cfg.ProxyURL = firstNonEmpty(stringArg(args, "proxy_url"), os.Getenv("STEEL_PROXY_URL"))
		cfg.UseProxy = boolArgDefault(args, "use_proxy", false)
		cfg.BlockAds = boolArgDefault(args, "block_ads", false)
		cfg.SolveCaptcha = boolArgDefault(args, "solve_captcha", false)
		cfg.Region = stringArg(args, "region")
		cfg.Timeout = intArg(args, "timeout")
		cfg.UserAgent = stringArg(args, "user_agent")
	case "browser-engine":
		fields := integrationFields(ctx, "browser-engine")
		cfg.APIKey = firstNonEmpty(fields["BROWSER_API_KEY"], fields["api_key"], fields["token"], os.Getenv("BROWSER_API_KEY"), os.Getenv("NEXT_PUBLIC_BROWSER_API_KEY"))
		cfg.URL = firstNonEmpty(stringArg(args, "backend_url"), fields["BROWSER_API_URL"], fields["base_url"], os.Getenv("BROWSER_API_URL"))
		cfg.InitialURL = stringArg(args, "initial_url")
		cfg.UserAgent = stringArg(args, "user_agent")
		cfg.Timeout = intArg(args, "timeout")
		cfg.ProxyEnabled = boolArgDefault(args, "proxy_enabled", false)
		cfg.ProxyCountry = stringArg(args, "proxy_country")
		cfg.BrowserProjectID = intArg(args, "browser_project_id")
	case "local":
		cfg.ProxyURL = firstNonEmpty(stringArg(args, "proxy_url"), os.Getenv("APTEVA_LOCAL_PROXY_URL"))
	case "service":
		cfg.URL = firstNonEmpty(stringArg(args, "backend_url"), os.Getenv("APTEVA_BROWSER_SERVICE_URL"))
	}
	return cfg
}

func integrationFields(ctx *sdk.AppCtx, role string) map[string]string {
	if ctx == nil {
		return nil
	}
	b := ctx.IntegrationFor(role)
	if b == nil || b.ConnectionID == 0 {
		return nil
	}
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(b.ConnectionID)
	if err != nil || creds == nil {
		if err != nil {
			ctx.Logger().Warn("integration credentials unavailable", "role", role, "err", err.Error())
		}
		return nil
	}
	return creds.Fields
}

func currentURL(c backends.Computer) string {
	if si, ok := c.(backends.SessionInfo); ok {
		return si.CurrentURL()
	}
	return ""
}

func backendSessionID(c backends.Computer) string {
	if si, ok := c.(backends.SessionInfo); ok {
		return si.SessionID()
	}
	return ""
}

func contextID(c backends.Computer) string {
	if ci, ok := c.(backends.ContextInfo); ok {
		return ci.ContextID()
	}
	return ""
}

// debugURL pulls the backend's debug URL via the anonymous interface
// each concrete backend (local Chrome, browserbase, steel,
// browserengine) implements. Returns "" if the backend doesn't
// expose one. Operators use this to attach DevTools / open the
// vendor's live viewer.
func debugURL(c backends.Computer) string {
	if dbg, ok := c.(interface{ DebugURL() string }); ok {
		return dbg.DebugURL()
	}
	return ""
}

func streamURL(c backends.Computer) string {
	if stream, ok := c.(interface{ StreamURL() string }); ok {
		return stream.StreamURL()
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

func boolArgDefault(args map[string]any, k string, def bool) bool {
	if v, ok := boolArg(args, k); ok {
		return v
	}
	return def
}

func boolPtrArg(args map[string]any, k string) *bool {
	if v, ok := boolArg(args, k); ok {
		return &v
	}
	return nil
}

func boolArg(args map[string]any, k string) (bool, bool) {
	switch v := args[k].(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.Trim(v, `"`)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func coordinateArg(args map[string]any) (int, int) {
	if coord := strings.TrimSpace(stringArg(args, "coordinate")); coord != "" {
		parts := strings.Split(coord, ",")
		if len(parts) == 2 {
			x, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			y, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
			return x, y
		}
	}
	return intArg(args, "x"), intArg(args, "y")
}

func mapWithDefault(args map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for key, val := range args {
		out[key] = val
	}
	if _, ok := out[k]; !ok {
		out[k] = v
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func imageMIME(b []byte) string {
	if len(b) >= 8 && b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4e && b[3] == 0x47 {
		return "image/png"
	}
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return "image/jpeg"
	}
	return "application/octet-stream"
}

func binaryEnvelope(b []byte, mime string) map[string]any {
	return map[string]any{
		"_binary":  true,
		"base64":   base64.StdEncoding.EncodeToString(b),
		"mimeType": mime,
		"size":     len(b),
	}
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

func (a *App) handleContextsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"backend": r.URL.Query().Get("backend")}
		out, err := a.toolContextList(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			httpErr(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		out, err := a.toolContextCreate(globalCtx, body)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleContextItem(w http.ResponseWriter, r *http.Request) {
	id := pathContextID(r.URL.Path)
	if id == "" {
		httpErr(w, http.StatusBadRequest, "context id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolContextGet(globalCtx, map[string]any{"id": id})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, out)
	case http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			httpErr(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		body["id"] = id
		out, err := a.toolContextUpdate(globalCtx, body)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, out)
	case http.MethodDelete:
		out, err := a.toolContextDelete(globalCtx, map[string]any{
			"id":              id,
			"delete_provider": r.URL.Query().Get("delete_provider"),
		})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

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
	if stringArg(body, "action") == "" {
		body["action"] = "open"
	}
	out, err := a.toolBrowserSession(globalCtx, body)
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

func (a *App) handleComputerUse(w http.ResponseWriter, r *http.Request) {
	id := pathSessionID(r.URL.Path, "/use")
	if id == "" {
		httpErr(w, http.StatusBadRequest, "session id required")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
		httpErr(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	body["session_id"] = id
	out, err := a.toolComputerUse(globalCtx, body)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

// handleSessionScreenshot streams the session's current screenshot inline.
// Path is /sessions/{id}/screenshot — strip the prefix + suffix to
// get id. Returns 404 if the session is unknown; the panel will then
// stop polling that id on its own when the session disappears from
// the list.
func (a *App) handleSessionScreenshot(w http.ResponseWriter, r *http.Request) {
	rest := pathSessionID(r.URL.Path, "/screenshot")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "session id required")
		return
	}
	sess, ok := a.reg.get(rest)
	if !ok {
		httpErr(w, http.StatusNotFound, "session not found")
		return
	}
	shot, err := sess.comp.Screenshot()
	if err != nil {
		httpErr(w, http.StatusBadGateway, "screenshot: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", imageMIME(shot))
	// No-cache so the panel's cache-busting query-string isn't even
	// needed — but we set it anyway for proxies that don't pass it
	// through.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(shot)
}

func pathSessionID(path, suffix string) string {
	rest := strings.TrimPrefix(path, "/sessions/")
	rest = strings.TrimSuffix(rest, suffix)
	return strings.Trim(rest, "/")
}

func pathContextID(path string) string {
	rest := strings.TrimPrefix(path, "/contexts/")
	return strings.Trim(rest, "/")
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
