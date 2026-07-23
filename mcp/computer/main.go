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
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"github.com/apteva/apps/mcp/computer/internal/browser/checkedinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/presentation"
	"github.com/apteva/apps/mcp/computer/internal/browser/selectinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/temporalinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/textinput"
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
version: 0.7.56
description: |
  Watch, steer, and replay hosted browser sessions. v0.7.56 keeps demo
  presentation cues attached to the exact structured-action control.
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
      description: "Open a fresh app-owned browser session, inspect it, close it, or switch its tabs. Args: action, session_id?, tab_id?, backend?, url?, context_id?, context_name?, auto_create_context?, persist?, timeout?, proxy?, proxy_country?, viewport?, presentation_mode?. Use presentation_mode=demo for a visible cursor, click feedback, human-paced typing, non-interactive cues for structured control changes, and longer holds in user-facing walkthroughs; fast is the default and preserves normal automation behavior. Presentation overlays never receive pointer events or replace the agent's underlying action. Usually omit viewport to use Computer's default desktop viewport, 1600x800. Pass viewport when a specific resolution is needed, for example mobile/tablet testing or a site-specific requirement. session_id is the app-owned live br_* handle for status/close/computer_use only. Always use action=open for new browsing work. To continue saved login and browser state, open a new session with context_id or context_name; do not reuse a prior session_id. For tab control, call browser_session(action=tabs) to list open tabs, then browser_session(action=switch_tab, tab_id=...) or browser_session(action=close_tab, tab_id=...). Do not use keyboard shortcuts such as Ctrl+Tab, Ctrl+PageDown, or Ctrl+1-9 to switch browser tabs. Browserbase honors timeout as max session lifetime. Prefer context_id from computer_context_list to reopen saved state; context_name works across backends when unique. For a reusable saved context, pass context_name with auto_create_context=true; omitted names are only a fallback and are auto-generated. Sessions consume local or cloud resources. When browser work is complete and the user did not explicitly ask to keep the browser open, close it with browser_session(action=close, session_id=...). Closing is especially important for Browserbase/Steel sessions and persisted contexts because it releases provider resources and lets context state flush cleanly."
    - name: computer_use
      description: "Drive an app-owned browser session. Default workflow: call action=screenshot first; screenshots contain Set-of-Mark numeric badges on interactive elements. To click, use action=click with label=N from the latest screenshot. label must be >= 1; do not pass 0. Prefer label over coordinate; use coordinate only for targets with no badge such as canvas or custom rendered widgets. Do not pass both; when both are present, coordinate wins. If the page asks to Browse, choose, attach, upload, or drop a file, use action=upload_file with selector or label plus source_url/base64/file_path; do not operate the native OS file picker. For any native select, dropdown, combobox, listbox, or multiselect, use action=select_option first with label/selector plus text/value or texts/values and optional mode=replace|add|remove|toggle; do not click options one by one or use keyboard navigation unless select_option fails. For checkboxes, radio buttons, and ARIA switches, use action=set_checked with label/selector plus checked=true|false instead of blind clicking. For long text fields, textareas, contenteditable editors, or message/post composers, use action=set_text with label/selector plus text instead of click + Control+A + type; use newline_mode=compact for public messages when blank paragraph gaps are not desired. For native date/time/datetime-local fields or text-like scheduler fields, use action=set_temporal with label/selector plus value such as 2026-07-01 or 11:00 AM. If a click opens exactly one new tab, Computer automatically follows it and reports switched_tab=true. For explicit tab control, call browser_session(action=tabs) to list tabs, then browser_session(action=switch_tab, tab_id=...) or browser_session(action=close_tab, tab_id=...); do not use Ctrl+Tab, Ctrl+PageDown, or Ctrl+1-9 for browser tab switching. Use action=key for page/editor commands such as Tab, Backspace, Control+A, Control+Z; use action=type only for short literal text and full date/time values such as 2026-06-05 or 08:00 PM. For action=scroll, amount is CSS pixels; use 200-500 for a small viewport move and omit amount for the 300px default. Use action=navigate with url, action=back for browser history, and action=reload to refresh; do not emulate these with Control+L, Alt+ArrowLeft, or F5. After scrolling, tab switching, selection, upload, checked-state changes, text changes, temporal-field changes, or navigation, take a fresh screenshot because labels are re-enumerated. Args: session_id, action, url? (navigate only), tab_id?, coordinate?, label?, selector?, checked?, source_url?, base64?, filename?, mime_type?, file_path?, text?, value?, texts?, values?, mode?, newline_mode?, key?, direction?, amount?, duration?, annotate? (screenshot only, default true), include_som? (screenshot only, default false). Returns screenshot bytes plus compact URL and state-change metadata; structured som targets are returned only for action=screenshot with include_som=true."
    - name: computer_context_create
      description: "Create or import an app-managed browser context. Args: name, backend?, provider_context_id?, persist_default?, metadata?, auto_create_provider?."
    - name: computer_context_list
      description: "List app-managed browser contexts. Omit backend, or pass backend=all, to see every saved context across providers. Use backend=default/auto for the Computer app default provider. Only pass a concrete backend when the user explicitly wants that provider."
    - name: computer_context_get
      description: "Fetch one browser context by id, by backend+name, or by unique name across all backends when backend is omitted."
    - name: computer_context_update
      description: "Update browser context metadata/defaults. Args: id, name?, provider_context_id?, persist_default?, metadata?."
    - name: computer_context_delete
      description: "Delete or unlink an app-managed browser context. Args: id, delete_provider?."
    - name: browser_open
      description: "Compatibility alias for browser_session(action=open). Pass presentation_mode=demo for visible, human-paced actions in live views and recordings."
    - name: browser_screenshot
      description: "Capture a clean PNG of the session viewport. Args: session_id, annotate? (default false; set true for Set-of-Mark labels), include_som? (default false; returns structured SoM targets only when true)."
    - name: browser_recording
      description: "Retrieve recording metadata for active or historical app-owned sessions. Browserbase and Steel return app-owned HLS playback URLs; other backends return status=unsupported. Args: session_id."
    - name: browser_close
      description: "Close a session opened by this app and release browser/provider resources. Use this when finished unless the user explicitly wants the session left open. Compatibility alias for browser_session(action=close)."
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
  publishes:
    - name: session.opened
      description: A browser session was opened or resumed by the Computer app.
      payload:
        session_id: string
        backend: string
        backend_session_id: string
        app_context_id: string
        context_name: string
        context_id: string
        persist: boolean
        current_url: string
    - name: session.closed
      description: A browser session was explicitly closed.
      payload:
        session_id: string
        backend: string
        backend_session_id: string
        recording_status: string
        close_reason: string
        app_context_id: string
        context_name: string
        persist: boolean
    - name: session.reaped
      description: A browser session was closed by the idle reaper.
      payload:
        session_id: string
        backend: string
        backend_session_id: string
        recording_status: string
        close_reason: string
        idle_seconds: integer
        app_context_id: string
        context_name: string
        persist: boolean
    - name: session.action
      description: A computer_use action completed successfully in a browser session.
      payload:
        session_id: string
        backend: string
        action: string
        label: integer
        coordinate: string
        text_length: integer
        key: string
        current_url: string
    - name: recording.ready
      description: A hosted browser session recording became playable.
      payload:
        session_id: string
        backend: string
        backend_session_id: string
        recording_status: string
    - name: recording.failed
      description: Hosted browser session recording retrieval failed.
      payload:
        session_id: string
        backend: string
        backend_session_id: string
        recording_status: string
        error: string
    - name: context.created
      description: An app-managed browser context was created or imported.
      payload:
        id: string
        name: string
        backend: string
        provider_context_id: string
        persist_default: boolean
        auto_created: boolean
    - name: context.updated
      description: An app-managed browser context was updated.
      payload:
        id: string
        name: string
        backend: string
        provider_context_id: string
        persist_default: boolean
        auto_created: boolean
    - name: context.deleted
      description: An app-managed browser context was deleted or unlinked.
      payload:
        id: string
        name: string
        backend: string
        provider_context_id: string
        provider_deleted: boolean
    - name: settings.updated
      description: Computer app provider settings were updated.
      payload:
        default_backend: string
        lock_backend: boolean
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
const maxUploadBytes = 100 * 1024 * 1024
const recordingProcessingWindow = 15 * time.Minute
const internalAppCallerHeader = "X-Apteva-Internal-App-Caller-ID"
const maxExtractChars = 200000
const defaultExtractChars = 50000
const maxExtractWaitMS = 10000

var errExtractSessionNotFound = errors.New("extraction session not found")
var errExtractUnsupported = errors.New("rendered DOM extraction unsupported")

type extractOptions struct {
	Formats     []string
	MaxChars    int
	Readability bool
	WaitMS      int
}

type extractRequest struct {
	Formats     []string `json:"formats,omitempty"`
	MaxChars    *int     `json:"max_chars,omitempty"`
	Readability *bool    `json:"readability,omitempty"`
	WaitMS      *int     `json:"wait_ms,omitempty"`
}

var sourceURLHTTPClient = http.DefaultClient
var sourceURLIPv4HTTPClient = &http.Client{Transport: ipv4OnlyTransport()}
var sourceURLPublicDNSHTTPClient = &http.Client{Transport: publicDNSTransport()}

// session is one open browser, owned by this sidecar.
type session struct {
	actionMu         sync.Mutex
	comp             backends.Computer
	backend          string
	presentation     backends.PresentationOptions
	backendSessionID string
	appContextID     string
	contextName      string
	initialURL       string
	persist          bool
	timeout          int
	openedAt         time.Time
	lastUsed         time.Time
}

type reapedSession struct {
	ID               string
	Backend          string
	BackendSessionID string
	ContextID        string
	AppContextID     string
	ContextName      string
	Persist          bool
	TimeoutSeconds   int
	CurrentURL       string
	Width            int
	Height           int
	OpenedAt         time.Time
	LastUsedAt       time.Time
	Idle             time.Duration
	ProviderExpired  bool
	sess             *session
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

// reapIdle removes sessions that have exceeded ttl. Closing and persistence
// belong to the app lifecycle layer because provider Close may clear metadata
// needed for historical recording lookup.
func (r *registry) reapIdle(ttl time.Duration) []string {
	reaped := r.reapIdleDetails(ttl)
	ids := make([]string, 0, len(reaped))
	for _, row := range reaped {
		ids = append(ids, row.ID)
	}
	return ids
}

func (r *registry) reapIdleDetails(ttl time.Duration) []reapedSession {
	type staleEntry struct {
		id              string
		s               *session
		providerExpired bool
	}
	r.mu.Lock()
	now := time.Now()
	cutoff := now.Add(-ttl)
	var stale []staleEntry
	for id, s := range r.m {
		providerExpired := s.backend != "local" && s.timeout > 0 && !s.openedAt.IsZero() && !now.Before(s.openedAt.Add(time.Duration(s.timeout)*time.Second))
		if s.lastUsed.Before(cutoff) || providerExpired {
			stale = append(stale, staleEntry{id: id, s: s, providerExpired: providerExpired})
			delete(r.m, id)
		}
	}
	r.mu.Unlock()
	reaped := make([]reapedSession, 0, len(stale))
	for _, entry := range stale {
		s := entry.s
		disp := s.comp.DisplaySize()
		reaped = append(reaped, reapedSession{
			ID:               entry.id,
			Backend:          s.backend,
			BackendSessionID: sessionBackendID(s),
			ContextID:        contextID(s.comp),
			AppContextID:     s.appContextID,
			ContextName:      s.contextName,
			Persist:          s.persist,
			TimeoutSeconds:   s.timeout,
			CurrentURL:       currentURL(s.comp),
			Width:            disp.Width,
			Height:           disp.Height,
			OpenedAt:         s.openedAt,
			LastUsedAt:       s.lastUsed,
			Idle:             now.Sub(s.lastUsed),
			ProviderExpired:  entry.providerExpired,
			sess:             s,
		})
	}
	return reaped
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
	interrupted, err := dbInterruptActiveSessions(ctx.AppDB(), time.Now())
	if err != nil {
		return fmt.Errorf("interrupt orphaned computer sessions: %w", err)
	}
	go a.reaper(ctx)
	ctx.Logger().Info("computer mounted", "tools", len(a.MCPTools()), "idle_ttl", idleTTL.String(), "interrupted_sessions", interrupted)
	return nil
}

func (a *App) OnUnmount(ctx *sdk.AppCtx) error {
	// Best-effort close on shutdown. We don't lock for the whole sweep
	// — we're shutting down, racing is fine.
	if a.reg == nil {
		return nil
	}
	a.reg.mu.Lock()
	sessions := a.reg.m
	a.reg.m = map[string]*session{}
	a.reg.mu.Unlock()
	for id, s := range sessions {
		s.actionMu.Lock()
		if _, err := a.finalizeSession(ctx, id, s, "interrupted", "app_unmount", "session.closed"); err != nil && ctx != nil {
			ctx.Logger().Warn("computer session shutdown failed", "session_id", id, "err", err.Error())
		}
		s.actionMu.Unlock()
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
		{Method: http.MethodGet, Pattern: "/settings", Handler: a.handleSettings},
		{Method: http.MethodPatch, Pattern: "/settings", Handler: a.handleSettings},
		{Method: http.MethodGet, Pattern: "/sessions", Handler: a.handleListSessions},
		{Method: http.MethodPost, Pattern: "/sessions", Handler: a.handleOpenSession},
		{Method: http.MethodDelete, Pattern: "/sessions/{id}", Handler: a.handleCloseSession},
		{Method: http.MethodGet, Pattern: "/sessions/{id}/tabs", Handler: a.handleSessionTabs},
		{Method: http.MethodPost, Pattern: "/sessions/{id}/tabs/{tab_id}/switch", Handler: a.handleSwitchTab},
		{Method: http.MethodDelete, Pattern: "/sessions/{id}/tabs/{tab_id}", Handler: a.handleCloseTab},
		{Method: http.MethodPost, Pattern: "/sessions/{id}/use", Handler: a.handleComputerUse},
		{Method: http.MethodPost, Pattern: "/internal/sessions/{id}/extract", Handler: a.handleInternalSessionExtract},
		{Method: http.MethodGet, Pattern: "/sessions/{id}/recording", Handler: a.handleRecordingMetadata},
		{Method: http.MethodGet, Pattern: "/sessions/{id}/recording/{stream_id}", Handler: a.handleRecordingPlaylist},
		{Method: http.MethodGet, Pattern: "/sessions/{id}/recording/{stream_id}/asset", Handler: a.handleRecordingAsset},
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
			Description: "Session lifecycle and tab control for app-owned browsers. Actions: open, status, close, tabs, switch_tab, close_tab. " +
				"Open args: backend? (local|browserbase|steel|browser-engine|service), url?, context_id?, persist?, " +
				"context_name?, auto_create_context?, timeout?, proxy?, proxy_country?, viewport?, presentation_mode? (fast|demo, default fast). " +
				"Use presentation_mode=demo for visible cursor/click feedback, human-paced typing, non-interactive structured-control cues, and longer holds in user-facing walkthroughs. " +
				"Usually omit viewport to use Computer's default desktop viewport, 1600x800. Pass viewport when a specific resolution is needed, for example mobile/tablet testing or a site-specific requirement. " +
				"session_id is the app-owned live br_* handle for status/close/computer_use and cannot reopen a closed session. " +
				"Always use action=open for new browsing work. To continue saved login and browser state, open a new session with context_id or context_name; do not reuse a prior session_id. " +
				"For tab control, call action=tabs first, then action=switch_tab with tab_id or action=close_tab with tab_id. " +
				"Do not use keyboard shortcuts such as Ctrl+Tab, Ctrl+PageDown, or Ctrl+1-9 to switch browser tabs. " +
				"Browserbase honors timeout as max session lifetime. " +
				"Prefer context_id returned by computer_context_list to reopen saved browser state; context_name works across providers when unique. " +
				"For a reusable saved context, pass context_name with auto_create_context=true; omitted names are only a fallback and are auto-generated. " +
				"Sessions consume local or cloud resources. When browser work is complete and the user did not explicitly ask to keep the browser open, close it with browser_session(action=close, session_id=...). " +
				"Closing is especially important for Browserbase/Steel sessions and persisted contexts because it releases provider resources and lets context state flush cleanly. " +
				"Returns {session_id, backend_session_id, backend, current_url, active_tab_id, tabs, context_id, debug_url, width, height}.",
			InputSchema: schemaObject(map[string]any{
				"action":            map[string]any{"type": "string", "enum": []string{"open", "status", "close", "tabs", "switch_tab", "close_tab"}},
				"session_id":        map[string]any{"type": "string", "description": "App-owned live br_* session id for status/close/computer_use. It cannot reopen a closed session; start a fresh session with action=open."},
				"tab_id":            map[string]any{"type": "string", "description": "Browser tab/page target id for switch_tab or close_tab."},
				"backend":           map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel", "browser-engine", "service"}},
				"presentation_mode": map[string]any{"type": "string", "enum": []string{"fast", "demo"}, "description": "Opt-in recording presentation layer. fast preserves normal automation behavior (default); demo shows a non-interactive cursor/click pulse and structured-control cues on CDP backends, types short text character by character, and holds visible states. Presentation overlays never dispatch input events or replace agent actions."},
				"url":               map[string]any{"type": "string"},
				"context_id":        map[string]any{"type": "string", "description": "App context id preferred; legacy raw provider context ids still work."},
				"context_name":      map[string]any{"type": "string", "description": "App-managed context name. Pass this when creating or reopening a reusable saved context."},
				"provider_context_id": map[string]any{
					"type": "string",
				},
				"auto_create_context": map[string]any{"type": "boolean", "description": "Create an app-managed context if no context_id/name/provider_context_id matches. For reusable contexts, also pass context_name; omitted names are auto-generated fallback names."},
				"persist":             map[string]any{"type": "boolean"},
				"timeout":             map[string]any{"type": "integer", "description": "Provider session max lifetime in seconds for cloud backends. Browserbase max is provider/plan bounded."},
				"proxy":               map[string]any{"type": "boolean"},
				"proxy_country":       map[string]any{"type": "string"},
				"viewport": map[string]any{
					"type":        "object",
					"description": "Optional. Usually omit to use Computer's default desktop viewport, 1600x800. Pass width/height when a specific resolution is needed.",
					"properties": map[string]any{
						"width":  map[string]any{"type": "integer", "description": "Viewport width in CSS pixels. Default is 1600 when viewport is omitted."},
						"height": map[string]any{"type": "integer", "description": "Viewport height in CSS pixels. Default is 800 when viewport is omitted."},
					},
				},
			}, []string{"action"}),
			Handler: a.toolBrowserSession,
		},
		{
			Name: "computer_use",
			Description: "Drive a browser session opened by browser_session. Default workflow: call action=screenshot first; screenshots contain Set-of-Mark numeric badges on interactive elements. " +
				"To click, use action=click with label=N from the latest screenshot. label must be >= 1; do not pass 0. Prefer label over coordinate; use coordinate only for targets with no badge such as canvas or custom rendered widgets. Do not pass both; when both are present, coordinate wins. " +
				"If the page asks to Browse, choose, attach, upload, or drop a file, use action=upload_file with selector or label plus source_url/base64/file_path; do not operate the native OS file picker. " +
				"For any native select, dropdown, combobox, listbox, or multiselect, use action=select_option first with label/selector plus text/value or texts/values and optional mode=replace|add|remove|toggle; do not click options one by one or use keyboard navigation unless select_option fails. " +
				"For checkboxes, radio buttons, and ARIA switches, use action=set_checked with label/selector plus checked=true|false instead of blind clicking. For long text fields, textareas, contenteditable editors, or message/post composers, use action=set_text with label/selector plus text instead of click + Control+A + type; use newline_mode=compact for public messages when blank paragraph gaps are not desired. For native date/time/datetime-local fields or text-like scheduler fields, use action=set_temporal with label/selector plus value such as 2026-07-01 or 11:00 AM. If the UI shows separate date and time fields, call set_temporal separately on each field; do not put a combined date-time string into the date field. " +
				"If a click opens exactly one new tab, Computer automatically follows it and reports switched_tab=true. For explicit tab control, call browser_session(action=tabs) to list tabs, then browser_session(action=switch_tab, tab_id=...) or browser_session(action=close_tab, tab_id=...). " +
				"Do not use Ctrl+Tab, Ctrl+PageDown, or Ctrl+1-9 for browser tab switching. Use action=key for page/editor commands such as Tab, Backspace, Control+A, Control+Z; use action=type only for short literal text and full date/time values such as 2026-06-05 or 08:00 PM. " +
				"For action=scroll, amount is CSS pixels; use 200-500 for a small viewport move and omit amount for the 300px default. " +
				"Use action=navigate with url for direct navigation, action=back for browser history, and action=reload to refresh the current page. Do not emulate these with Control+L, Alt+ArrowLeft, or F5. " +
				"After scrolling, tab switching, selection, upload, checked-state changes, text changes, temporal-field changes, or navigation, take a fresh screenshot because labels are re-enumerated. Actions: screenshot, navigate, back, reload, click, double_click, type, key, scroll, wait, upload_file, select_option, set_checked, set_text, set_temporal. " +
				"Args: session_id, action, url? (navigate only), tab_id?, coordinate? (\"x,y\"), label? (Set-of-Mark label), selector? (CSS selector), checked?, source_url?, base64?, filename?, mime_type?, file_path?, text?, value?, texts?, values?, mode?, newline_mode?, key?, direction?, amount?, duration?, annotate? (screenshot only, default true). " +
				"Returns a binary screenshot envelope plus compact current URL and state-change metadata. Full tabs and viewport metadata are available from browser_session.",
			InputSchema: schemaObject(map[string]any{
				"session_id":   map[string]any{"type": "string"},
				"action":       map[string]any{"type": "string", "enum": []string{"screenshot", "navigate", "back", "reload", "click", "double_click", "type", "key", "scroll", "wait", "upload_file", "select_option", "set_checked", "set_text", "set_temporal"}},
				"url":          map[string]any{"type": "string", "description": "Required for action=navigate. Absolute http(s) URL to load in the current tab."},
				"tab_id":       map[string]any{"type": "string", "description": "Optional active tab/page target to switch to before running the action."},
				"coordinate":   map[string]any{"type": "string"},
				"label":        map[string]any{"type": "integer", "minimum": 1, "description": "Positive Set-of-Mark target number shown as a colored badge in the latest screenshot. Prefer this over coordinate for click/double_click. Do not pass 0."},
				"selector":     map[string]any{"type": "string", "description": "For action=upload_file, select_option, set_checked, set_text, or set_temporal. CSS selector for the target input/select/combobox/dropzone/textbox, e.g. input#mainMedia or button[role=combobox]."},
				"checked":      map[string]any{"type": "boolean", "description": "For action=set_checked. Desired final checked state for a checkbox, radio button, ARIA checkbox, or ARIA switch."},
				"source_url":   map[string]any{"type": "string", "description": "For action=upload_file. HTTP(S) URL to download and upload."},
				"base64":       map[string]any{"type": "string", "description": "For action=upload_file. Base64 file content, optionally as a data URL."},
				"filename":     map[string]any{"type": "string", "description": "For action=upload_file with source_url/base64. Suggested filename."},
				"mime_type":    map[string]any{"type": "string", "description": "For action=upload_file with base64. MIME type hint."},
				"file_path":    map[string]any{"type": "string", "description": "For action=upload_file. Local app filesystem path; mainly for local/dev/manual use."},
				"text":         map[string]any{"type": "string", "description": "For action=type, short literal text. For action=set_text, the full text to put into an input, textarea, contenteditable editor, or message/post composer. For action=select_option, one option display text to select. For action=set_temporal, accepted as a fallback value. When focused on native date/time inputs, action=type can normalize full values like 2026-06-05, 08:00 PM, or 2026-06-05 08:00 PM, but set_temporal is safer when focus is unstable. For split date/time UIs, target the date field and time field in separate calls."},
				"value":        map[string]any{"type": "string", "description": "For action=select_option, one option value to select. For action=set_text, accepted as fallback text when text is omitted. For action=set_temporal, the value to write, such as 2026-07-01 for a date field, 11:00 AM for a time field, or 2026-07-01 11:00 AM only when the target is a single datetime field. For separate date and time fields, call set_temporal twice."},
				"texts":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "For action=select_option. Multiple option display texts."},
				"values":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "For action=select_option. Multiple option values."},
				"mode":         map[string]any{"type": "string", "enum": []string{"replace", "add", "remove", "toggle", "append"}, "description": "For action=select_option, replace is default and add/remove/toggle are intended for multiselect controls. For action=set_text, use replace (default) or append."},
				"newline_mode": map[string]any{"type": "string", "enum": []string{"preserve", "compact"}, "description": "For action=set_text. preserve keeps line breaks exactly; compact collapses repeated blank lines to single line breaks for public messages/composers."},
				"key":          map[string]any{"type": "string", "description": "For action=key. Page/editor command key such as Enter, Tab, Backspace, Escape, ArrowUp, Control+A, Control+Z, Meta+A, or Shift+Tab. Do not use action=type for command keys. Do not use Ctrl+Tab, Ctrl+PageDown, or Ctrl+1-9 for browser tab switching; call browser_session(action=tabs) then browser_session(action=switch_tab)."},
				"direction":    map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}, "description": "For action=scroll."},
				"amount":       map[string]any{"type": "integer", "description": "For action=scroll. CSS pixels, not wheel ticks. Defaults to 300 when omitted; use 200-500 for a small viewport move."},
				"duration":     map[string]any{"type": "integer"},
				"annotate":     map[string]any{"type": "boolean", "description": "For action=screenshot, include Set-of-Mark labels in the returned image. Defaults true for computer_use so agent click flow remains label-based."},
				"som":          map[string]any{"type": "boolean", "description": "Alias for annotate."},
				"include_som":  map[string]any{"type": "boolean", "description": "For action=screenshot. Opt-in structured Set-of-Mark targets in the response. Defaults false to keep MCP payloads small."},
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
			Description: "List app-managed browser contexts. Omit backend, or pass backend=all, to see every saved context across providers. Use backend=default/auto for the Computer app default provider. Only pass a concrete backend when the user explicitly wants that provider.",
			InputSchema: schemaObject(map[string]any{
				"backend": map[string]any{"type": "string", "enum": []string{"all", "default", "auto", "local", "browserbase", "steel", "browser-engine"}, "description": "Omit or use all to list all contexts. Use default/auto for the app default provider. Concrete providers filter and may hide contexts saved under another provider."},
			}, nil),
			Handler: a.toolContextList,
		},
		{
			Name:        "computer_context_get",
			Description: "Fetch one app-managed browser context by id, by backend+name, or by unique name across all backends when backend is omitted.",
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
			Description: "Compatibility alias for browser_session(action=open). Args: backend? (local|browserbase|steel|browser-engine, default from Computer app settings), " +
				"url? (navigate after open), context_name?, auto_create_context?, timeout?, viewport?, presentation_mode? (fast|demo, default fast). Use demo for visible, human-paced actions and non-interactive structured-control cues in live views and recordings. Usually omit viewport to use Computer's default desktop viewport, 1600x800. Pass viewport when a specific resolution is needed, for example mobile/tablet testing or a site-specific requirement. " +
				"Browserbase honors timeout as max session lifetime. " +
				"For a reusable saved context, pass context_name with auto_create_context=true; omitted names are only a fallback and are auto-generated. " +
				"Returns {session_id, backend, current_url, width, height}. " +
				"Session owned by this sidecar until browser_close or 30-minute idle reaper.",
			InputSchema: schemaObject(map[string]any{
				"backend":           map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel", "browser-engine", "service"}},
				"presentation_mode": map[string]any{"type": "string", "enum": []string{"fast", "demo"}, "description": "Opt-in recording presentation layer. fast preserves normal automation behavior (default); demo adds non-interactive visual cues and pacing without replacing agent actions."},
				"url":               map[string]any{"type": "string"},
				"context_id":        map[string]any{"type": "string"},
				"context_name":      map[string]any{"type": "string", "description": "App-managed context name. Pass this when creating or reopening a reusable saved context."},
				"provider_context_id": map[string]any{
					"type": "string",
				},
				"auto_create_context": map[string]any{"type": "boolean", "description": "Create an app-managed context if no context_id/name/provider_context_id matches. For reusable contexts, also pass context_name; omitted names are auto-generated fallback names."},
				"persist":             map[string]any{"type": "boolean"},
				"timeout":             map[string]any{"type": "integer", "description": "Provider session max lifetime in seconds for cloud backends. Browserbase max is provider/plan bounded."},
				"proxy":               map[string]any{"type": "boolean"},
				"proxy_country":       map[string]any{"type": "string"},
				"viewport": map[string]any{
					"type":        "object",
					"description": "Optional. Usually omit to use Computer's default desktop viewport, 1600x800. Pass width/height when a specific resolution is needed.",
					"properties": map[string]any{
						"width":  map[string]any{"type": "integer", "description": "Viewport width in CSS pixels. Default is 1600 when viewport is omitted."},
						"height": map[string]any{"type": "integer", "description": "Viewport height in CSS pixels. Default is 800 when viewport is omitted."},
					},
				},
			}, nil),
			Handler: a.toolBrowserOpen,
		},
		{
			Name: "browser_screenshot",
			Description: "Capture a clean PNG of the session's current viewport. Args: session_id, annotate? (default false; set true for Set-of-Mark labels), include_som? (default false; returns structured SoM targets only when true). " +
				"Returns {png_b64, current_url, width, height} and som only when include_som=true.",
			InputSchema: schemaObject(map[string]any{
				"session_id":  map[string]any{"type": "string"},
				"annotate":    map[string]any{"type": "boolean", "description": "Include Set-of-Mark labels in the image. Defaults false for clean capture/export screenshots."},
				"som":         map[string]any{"type": "boolean", "description": "Alias for annotate."},
				"include_som": map[string]any{"type": "boolean", "description": "Opt-in structured Set-of-Mark targets in the response. Defaults false to keep MCP payloads small."},
			}, []string{"session_id"}),
			Handler: a.toolBrowserScreenshot,
		},
		{
			Name:        "browser_recording",
			Description: "Retrieve provider-neutral playback metadata for an active or historical app-owned browser session. Browserbase and Steel recordings become playable after the session closes; recently closed sessions may return status=processing. Returns app-owned playlist URLs and never provider credentials or provider API URLs. Args: session_id.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "string", "description": "Durable app-owned br_* session id returned by browser_session(open)."},
			}, []string{"session_id"}),
			Handler: a.toolBrowserRecording,
		},
		{
			Name:        "browser_close",
			Description: "Close a session opened by this app and release browser/provider resources. Use this when finished unless the user explicitly wants the session left open. Args: session_id. Idempotent — unknown ids return {closed:false}.",
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
	requestedBackend := strings.TrimSpace(stringArg(args, "backend"))
	backend, err := resolveContextListBackend(ctx, requestedBackend)
	if err != nil {
		return nil, err
	}
	rows, err := dbListContexts(db, backend)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"contexts":          rows,
		"backend":           firstNonEmpty(backend, "all"),
		"requested_backend": requestedBackend,
	}
	if requestedBackend != "" && requestedBackend != "all" {
		allRows, err := dbListContexts(db, "")
		if err != nil {
			return nil, err
		}
		out["available_backends"] = contextBackends(allRows)
		out["total_contexts"] = len(allRows)
		if len(rows) == 0 && len(allRows) > 0 {
			out["other_contexts"] = allRows
			out["hint"] = fmt.Sprintf("No contexts found for backend %q. Omit backend or pass backend=all to list all saved contexts.", backend)
		}
	}
	return out, nil
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
		name := stringArg(args, "name")
		if name == "" {
			return nil, fmt.Errorf("id or name required")
		}
		backend := strings.TrimSpace(stringArg(args, "backend"))
		if backend != "" {
			rec, err = dbGetContextByName(db, backend, name)
		} else {
			matches, matchErr := dbGetContextsByName(db, name)
			if matchErr != nil {
				return nil, matchErr
			}
			switch len(matches) {
			case 0:
				err = errContextNotFound
			case 1:
				rec = matches[0]
			default:
				return map[string]any{
					"found":     false,
					"ambiguous": true,
					"contexts":  matches,
					"hint":      "Multiple contexts share this name. Pass context_id, or pass backend with name.",
				}, nil
			}
		}
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
	emitEvent(ctx, "context.updated", contextEventPayload(rec))
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
	payload := contextEventPayload(rec)
	payload["provider_deleted"] = providerDeleted
	emitEvent(ctx, "context.deleted", payload)
	return map[string]any{"deleted": true, "provider_deleted": providerDeleted}, nil
}

func (a *App) toolSettingsGet(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	settings, err := currentSettings(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"settings": settings}, nil
}

func (a *App) toolSettingsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := appDB(ctx)
	if db == nil {
		return nil, fmt.Errorf("computer settings unavailable")
	}
	settings, err := dbUpdateSettings(db, args)
	if err != nil {
		return nil, err
	}
	emitEvent(ctx, "settings.updated", settingsEventPayload(settings))
	return map[string]any{"settings": settings}, nil
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
		settings, err := currentSettings(ctx)
		if err != nil {
			return nil, err
		}
		backend = settings.DefaultBackend
	}
	backend = normalizeBackend(backend)
	if !isContextBackend(backend) {
		return nil, fmt.Errorf("backend %q does not support managed contexts", backend)
	}
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
	rec, err := dbCreateContext(db, contextCreateInput{
		Name:              name,
		Backend:           backend,
		ProviderContextID: providerID,
		PersistDefault:    persistDefault,
		AutoCreated:       autoCreated,
		MetadataJSON:      metadataJSON,
	})
	if err != nil {
		return nil, err
	}
	emitEvent(ctx, "context.created", contextEventPayload(rec))
	return rec, nil
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
		if id := strings.TrimSpace(stringArg(args, "session_id")); id != "" {
			sess, ok := a.reg.get(id)
			if !ok {
				return nil, fmt.Errorf("session_not_active: app session %s is no longer active; open a new browser session with context_id or context_name instead of resuming session_id", id)
			}
			return a.sessionOutput(id, sess), nil
		}
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
	case "tabs":
		return a.toolBrowserTabs(ctx, args)
	case "switch_tab":
		return a.toolBrowserSwitchTab(ctx, args)
	case "close_tab":
		return a.toolBrowserCloseTab(ctx, args)
	case "close":
		return a.toolBrowserClose(ctx, args)
	default:
		return nil, fmt.Errorf("unknown browser_session action %q", action)
	}
}

func (a *App) sessionTabController(args map[string]any) (string, *session, backends.TabController, error) {
	id := stringArg(args, "session_id")
	if id == "" {
		return "", nil, nil, fmt.Errorf("session_id required")
	}
	sess, ok := a.reg.get(id)
	if !ok {
		return "", nil, nil, fmt.Errorf("session %s not found", id)
	}
	tc, ok := sess.comp.(backends.TabController)
	if !ok {
		return "", nil, nil, fmt.Errorf("backend %q does not support tabs", sess.backend)
	}
	return id, sess, tc, nil
}

func (a *App) toolBrowserTabs(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id, _, tc, err := a.sessionTabController(args)
	if err != nil {
		return nil, err
	}
	tabs, err := tc.ListTabs()
	if err != nil {
		return nil, fmt.Errorf("tabs: %w", err)
	}
	return map[string]any{
		"session_id":    id,
		"active_tab_id": tc.ActiveTabID(),
		"tabs":          tabs,
		"tab_count":     len(tabs),
	}, nil
}

func (a *App) toolBrowserSwitchTab(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, sess, tc, err := a.sessionTabController(args)
	if err != nil {
		return nil, err
	}
	tabID := stringArg(args, "tab_id")
	if tabID == "" {
		return nil, fmt.Errorf("tab_id required")
	}
	if err := tc.SwitchTab(tabID); err != nil {
		return nil, fmt.Errorf("switch_tab: %w", err)
	}
	payload := a.sessionEventPayload(id, sess)
	payload["action"] = "switch_tab"
	payload["tab_id"] = tabID
	emitEvent(ctx, "session.action", payload)
	return a.sessionOutput(id, sess), nil
}

func (a *App) toolBrowserCloseTab(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, sess, tc, err := a.sessionTabController(args)
	if err != nil {
		return nil, err
	}
	tabID := stringArg(args, "tab_id")
	if tabID == "" {
		return nil, fmt.Errorf("tab_id required")
	}
	if err := tc.CloseTab(tabID); err != nil {
		return nil, fmt.Errorf("close_tab: %w", err)
	}
	payload := a.sessionEventPayload(id, sess)
	payload["action"] = "close_tab"
	payload["tab_id"] = tabID
	emitEvent(ctx, "session.action", payload)
	return a.sessionOutput(id, sess), nil
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
	explicitBackend := strings.TrimSpace(stringArg(args, "backend")) != ""
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
		if rec == nil && !explicitBackend {
			matches, matchErr := dbGetContextsByName(db, contextName)
			if matchErr != nil {
				return out, fmt.Errorf("get context %q: %w", contextName, matchErr)
			}
			switch len(matches) {
			case 1:
				rec = matches[0]
			case 0:
			default:
				return out, fmt.Errorf("context_name %q is ambiguous across backends; pass context_id or backend", contextName)
			}
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
		name := firstNonEmpty(contextName, contextIDArg, generatedContextName(backend, stringArg(args, "url")))
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

func generatedContextName(backend, rawURL string) string {
	host := "session"
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	host = strings.Trim(host, ".")
	host = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, host)
	host = strings.Trim(host, "-_")
	if host == "" {
		host = "session"
	}
	suffix := strings.TrimPrefix(newContextID(), "ctx_")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("auto-%s-%s-%s", normalizeBackend(backend), host, suffix)
}

func (a *App) openBrowserSession(ctx *sdk.AppCtx, args map[string]any, resume bool) (any, error) {
	backend, err := a.resolveBackend(ctx, args)
	if err != nil {
		return nil, err
	}
	presentationOptions, err := presentation.ForMode(strings.TrimSpace(stringArg(args, "presentation_mode")))
	if err != nil {
		return nil, err
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

	requestedBackendSessionID := firstNonEmpty(
		stringArg(args, "backend_session_id"),
		stringArg(args, "provider_session_id"),
	)
	if resume && requestedBackendSessionID == "" {
		return nil, fmt.Errorf("backend_session_id required for provider attach; to continue saved browser state, call browser_session(action=\"open\", context_id=...) or context_name=...")
	}

	cfg := backendConfig(ctx, args, backend, width, height)
	if usingProductionBackendFactory() {
		if err := validateBackendConfigured(cfg); err != nil {
			return nil, err
		}
	}
	comp, err := newBackend(cfg)
	if err != nil {
		return nil, fmt.Errorf("backend %q open failed: %w", backend, err)
	}
	if comp == nil {
		return nil, fmt.Errorf("backend %q unknown", backend)
	}

	openOpts := backends.OpenOptions{
		URL:           stringArg(args, "url"),
		ContextID:     rc.ProviderContextID,
		CreateContext: rc.CreateProviderOnOpen,
		Persist:       rc.Persist,
		SessionID:     requestedBackendSessionID,
		Timeout:       intArg(args, "timeout"),
		Proxy:         boolPtrArg(args, "proxy"),
		ProxyCountry:  stringArg(args, "proxy_country"),
	}
	if opener, ok := comp.(backends.SessionOpener); ok {
		if err := opener.OpenSession(openOpts); err != nil {
			_ = comp.Close()
			if resume && requestedBackendSessionID != "" {
				return nil, providerAttachError(backend, requestedBackendSessionID, err)
			}
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

	id := newSessionID()
	now := time.Now()
	sess := &session{
		comp:             comp,
		backend:          backend,
		presentation:     presentationOptions,
		backendSessionID: backendSessionID(comp),
		appContextID:     rc.AppContextID,
		contextName:      rc.ContextName,
		initialURL:       openOpts.URL,
		persist:          rc.Persist,
		timeout:          intArg(args, "timeout"),
		openedAt:         now,
		lastUsed:         now,
	}
	if recordingSupported(backend) && sess.backendSessionID == "" && usingProductionBackendFactory() {
		_ = comp.Close()
		return nil, fmt.Errorf("backend %q did not return a provider session id", backend)
	}
	if ctx == nil || ctx.AppDB() == nil {
		_ = comp.Close()
		return nil, errors.New("computer session history is unavailable")
	}
	if err := dbPutSession(ctx.AppDB(), sessionRecord(id, sess, "active", "", nil)); err != nil {
		_ = comp.Close()
		return nil, err
	}

	if rc.AppContextID != "" {
		providerID := contextID(comp)
		if providerID != "" && providerID != rc.ProviderContextID && ctx != nil && ctx.AppDB() != nil {
			updated, err := dbUpdateContext(ctx.AppDB(), rc.AppContextID, map[string]any{"provider_context_id": providerID})
			if err != nil {
				_, _ = a.finalizeSession(ctx, id, sess, "failed", "context_update_failed", "session.closed")
				return nil, fmt.Errorf("update context provider id: %w", err)
			}
			rc.ProviderContextID = updated.ProviderContextID
		}
		if ctx != nil {
			dbTouchContext(ctx.AppDB(), rc.AppContextID)
		}
	}
	a.reg.put(id, sess)

	if ctx != nil {
		ctx.Logger().Info("browser_open", "session_id", id, "backend", backend)
	}
	payload := a.sessionEventPayload(id, sess)
	payload["resume"] = resume
	emitEvent(ctx, "session.opened", payload)
	return a.sessionOutput(id, sess), nil
}

func (a *App) resolveBackend(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	settings, err := currentSettings(ctx)
	if err != nil {
		return "", err
	}
	requested := strings.TrimSpace(stringArg(args, "backend"))
	if requested == "" {
		return settings.DefaultBackend, nil
	}
	requested = normalizeBackend(requested)
	if !isSessionBackend(requested) {
		return "", fmt.Errorf("backend %q is not supported", requested)
	}
	if settings.LockBackend && requested != settings.DefaultBackend {
		if ctx != nil {
			ctx.Logger().Warn("computer provider override ignored",
				"requested_backend", requested,
				"default_backend", settings.DefaultBackend)
		}
		return settings.DefaultBackend, nil
	}
	return requested, nil
}

// sessionInfo is the shape each row in browser_list / /api/sessions
// reports. Kept tight: session_id + provenance + the URLs the
// operator needs to identify or open the session.
type sessionInfo struct {
	SessionID          string             `json:"session_id"`
	BackendSessionID   string             `json:"backend_session_id,omitempty"`
	Backend            string             `json:"backend"`
	PresentationMode   string             `json:"presentation_mode,omitempty"`
	Status             string             `json:"status"`
	RecordingSupported bool               `json:"recording_supported"`
	RecordingStatus    string             `json:"recording_status"`
	ContextID          string             `json:"context_id,omitempty"`
	AppContextID       string             `json:"app_context_id,omitempty"`
	ContextName        string             `json:"context_name,omitempty"`
	Persist            bool               `json:"persist"`
	TimeoutSeconds     int                `json:"timeout_seconds,omitempty"`
	ProviderExpiresAt  string             `json:"provider_expires_at,omitempty"`
	CurrentURL         string             `json:"current_url"`
	DebugURL           string             `json:"debug_url,omitempty"`
	StreamURL          string             `json:"stream_url,omitempty"`
	ActiveTabID        string             `json:"active_tab_id,omitempty"`
	Tabs               []backends.TabInfo `json:"tabs,omitempty"`
	TabCount           int                `json:"tab_count,omitempty"`
	Width              int                `json:"width"`
	Height             int                `json:"height"`
	OpenedAt           string             `json:"opened_at"`
	LastUsedAt         string             `json:"last_used_at"`
	ClosedAt           string             `json:"closed_at,omitempty"`
	CloseReason        string             `json:"close_reason,omitempty"`
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
		presentation backends.PresentationOptions
		appContextID string
		contextName  string
		persist      bool
		timeout      int
		opened       time.Time
		used         time.Time
	}
	a.reg.mu.Lock()
	rows := make([]frozen, 0, len(a.reg.m))
	for id, s := range a.reg.m {
		rows = append(rows, frozen{id: id, comp: s.comp, backend: s.backend, presentation: s.presentation, appContextID: s.appContextID, contextName: s.contextName, persist: s.persist, timeout: s.timeout, opened: s.openedAt, used: s.lastUsed})
	}
	a.reg.mu.Unlock()

	out := make([]sessionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.sessionInfo(r.id, &session{comp: r.comp, backend: r.backend, presentation: r.presentation, appContextID: r.appContextID, contextName: r.contextName, persist: r.persist, timeout: r.timeout, openedAt: r.opened, lastUsed: r.used}))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpenedAt > out[j].OpenedAt })
	return out
}

func (a *App) listUISessions(ctx *sdk.AppCtx, limit int) ([]sessionInfo, error) {
	active := a.listSessions()
	if ctx == nil || ctx.AppDB() == nil {
		return active, nil
	}
	history, err := dbListSessions(ctx.AppDB(), limit)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(active))
	for _, row := range active {
		seen[row.SessionID] = struct{}{}
	}
	out := make([]sessionInfo, 0, len(active)+len(history))
	out = append(out, active...)
	for _, row := range history {
		if _, ok := seen[row.ID]; ok || row.Status == "active" {
			continue
		}
		out = append(out, historicalSessionInfo(row))
	}
	return out, nil
}

func historicalSessionInfo(row *ComputerSession) sessionInfo {
	currentURL := row.CurrentURL
	if currentURL == "" {
		currentURL = row.InitialURL
	}
	closedAt := ""
	if row.ClosedAt != nil {
		closedAt = *row.ClosedAt
	}
	return sessionInfo{
		SessionID:          row.ID,
		BackendSessionID:   row.BackendSessionID,
		Backend:            row.Backend,
		Status:             row.Status,
		RecordingSupported: recordingSupported(row.Backend),
		RecordingStatus:    row.RecordingStatus,
		AppContextID:       row.AppContextID,
		ContextName:        row.ContextName,
		CurrentURL:         currentURL,
		Width:              row.Width,
		Height:             row.Height,
		OpenedAt:           row.OpenedAt,
		LastUsedAt:         row.UpdatedAt,
		ClosedAt:           closedAt,
		CloseReason:        row.CloseReason,
	}
}

func (a *App) sessionInfo(id string, s *session) sessionInfo {
	disp := s.comp.DisplaySize()
	providerExpiresAt := ""
	if s.timeout > 0 {
		providerExpiresAt = s.openedAt.Add(time.Duration(s.timeout) * time.Second).UTC().Format(time.RFC3339)
	}
	tabs := tabsFor(s.comp)
	return sessionInfo{
		SessionID:          id,
		BackendSessionID:   sessionBackendID(s),
		Backend:            s.backend,
		PresentationMode:   firstNonEmpty(s.presentation.Mode, "fast"),
		Status:             "active",
		RecordingSupported: recordingSupported(s.backend),
		RecordingStatus:    activeRecordingStatus(s.backend),
		ContextID:          contextID(s.comp),
		AppContextID:       s.appContextID,
		ContextName:        s.contextName,
		Persist:            s.persist,
		TimeoutSeconds:     s.timeout,
		ProviderExpiresAt:  providerExpiresAt,
		CurrentURL:         currentURL(s.comp),
		DebugURL:           debugURL(s.comp),
		StreamURL:          streamURL(s.comp),
		ActiveTabID:        activeTabID(s.comp),
		Tabs:               tabs,
		TabCount:           len(tabs),
		Width:              disp.Width,
		Height:             disp.Height,
		OpenedAt:           s.openedAt.UTC().Format(time.RFC3339),
		LastUsedAt:         s.lastUsed.UTC().Format(time.RFC3339),
	}
}

func (a *App) sessionOutput(id string, s *session) map[string]any {
	info := a.sessionInfo(id, s)
	return map[string]any{
		"session_id":                    info.SessionID,
		"backend_session_id":            info.BackendSessionID,
		"backend":                       info.Backend,
		"presentation_mode":             info.PresentationMode,
		"presentation_cursor_supported": info.Backend != "service",
		"status":                        info.Status,
		"recording_supported":           info.RecordingSupported,
		"recording_status":              info.RecordingStatus,
		"context_id":                    info.ContextID,
		"app_context_id":                info.AppContextID,
		"context_name":                  info.ContextName,
		"persist":                       info.Persist,
		"timeout_seconds":               info.TimeoutSeconds,
		"provider_expires_at":           info.ProviderExpiresAt,
		"current_url":                   info.CurrentURL,
		"debug_url":                     info.DebugURL,
		"stream_url":                    info.StreamURL,
		"active_tab_id":                 info.ActiveTabID,
		"tabs":                          info.Tabs,
		"tab_count":                     info.TabCount,
		"width":                         info.Width,
		"height":                        info.Height,
		"opened_at":                     info.OpenedAt,
		"last_used_at":                  info.LastUsedAt,
	}
}

func (a *App) toolBrowserScreenshot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	shotArgs := mapWithDefault(args, "action", "screenshot")
	if _, ok := shotArgs["annotate"]; !ok {
		if _, ok := shotArgs["som"]; !ok {
			shotArgs["annotate"] = false
		}
	}
	out, err := a.toolComputerUse(ctx, shotArgs)
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	res := map[string]any{
		"png_b64":     m["screenshot_b64"],
		"screenshot":  m["screenshot"],
		"current_url": m["current_url"],
		"width":       m["width"],
		"height":      m["height"],
	}
	if som, ok := m["som"]; ok {
		res["som"] = som
	}
	return res, nil
}

func (a *App) extractSessionDOM(sessionID string, opts extractOptions) (map[string]any, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("session id required")
	}
	sess, ok := a.reg.get(sessionID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errExtractSessionNotFound, sessionID)
	}
	sess.actionMu.Lock()
	defer sess.actionMu.Unlock()
	extractor, ok := sess.comp.(backends.DOMExtractor)
	if !ok {
		return nil, fmt.Errorf("%w for backend %q", errExtractUnsupported, sess.backend)
	}
	formats := opts.Formats
	if len(formats) == 0 {
		formats = []string{"text"}
	}
	responseLimit := opts.MaxChars
	if responseLimit <= 0 {
		responseLimit = defaultExtractChars
	}
	res, err := extractor.ExtractDOM(backends.ExtractOptions{
		Formats:     formats,
		MaxChars:    responseLimit,
		Readability: opts.Readability,
		WaitMS:      opts.WaitMS,
	})
	if err != nil {
		return nil, fmt.Errorf("rendered DOM extraction: %w", err)
	}
	fields := []extractResponseField{
		{Key: "session_id", Value: sessionID},
		{Key: "backend", Value: sess.backend},
		{Key: "current_url", Value: firstNonEmpty(res.URL, currentURL(sess.comp))},
	}
	fields = appendNonEmptyExtractField(fields, "title", res.Title)
	fields = appendNonEmptyExtractField(fields, "description", res.Description)
	fields = append(fields, extractResponseField{Key: "rendered", Value: res.Rendered})
	fields = appendNonEmptyExtractField(fields, "extraction_backend", res.ExtractionBackend)
	if bID := backendSessionID(sess.comp); bID != "" {
		fields = append(fields, extractResponseField{Key: "backend_session_id", Value: bID})
	}
	if tabID := activeTabID(sess.comp); tabID != "" {
		fields = append(fields, extractResponseField{Key: "active_tab_id", Value: tabID})
	}
	addedFormats := map[string]bool{}
	for _, format := range formats {
		outputKey := format
		if format == "json" {
			outputKey = "structured_data"
		}
		if addedFormats[outputKey] {
			continue
		}
		addedFormats[outputKey] = true
		switch format {
		case "text":
			fields = appendNonEmptyExtractField(fields, "text", res.Text)
		case "markdown":
			fields = appendNonEmptyExtractField(fields, "markdown", res.Markdown)
		case "html":
			fields = appendNonEmptyExtractField(fields, "html", res.HTML)
		case "links":
			fields = appendNonEmptyExtractField(fields, "links", res.Links)
		case "images":
			fields = appendNonEmptyExtractField(fields, "images", res.Images)
		case "regions":
			fields = appendNonEmptyExtractField(fields, "regions", res.Regions)
			disp := sess.comp.DisplaySize()
			fields = append(fields,
				extractResponseField{Key: "width", Value: disp.Width},
				extractResponseField{Key: "height", Value: disp.Height},
			)
		case "metadata":
			fields = appendNonEmptyExtractField(fields, "metadata", res.Metadata)
		case "structured_data", "json":
			fields = appendNonEmptyExtractField(fields, "structured_data", res.StructuredData)
		}
	}
	return limitExtractResponse(fields, responseLimit), nil
}

type extractResponseField struct {
	Key   string
	Value any
}

func appendNonEmptyExtractField(fields []extractResponseField, key string, value any) []extractResponseField {
	if value == nil {
		return fields
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.String, reflect.Array, reflect.Slice, reflect.Map:
		if rv.Len() == 0 {
			return fields
		}
	}
	return append(fields, extractResponseField{Key: key, Value: value})
}

// limitExtractResponse treats max_chars as a cap on the complete serialized
// response, not a separate allowance for every requested representation.
func limitExtractResponse(fields []extractResponseField, maxChars int) map[string]any {
	if maxChars <= 0 {
		maxChars = defaultExtractChars
	}
	full := make(map[string]any, len(fields))
	for _, field := range fields {
		full[field.Key] = field.Value
	}
	if jsonObjectSize(full) <= maxChars {
		return full
	}
	if maxChars < len(`{"truncated":true}`) {
		return map[string]any{}
	}

	out := map[string]any{"truncated": true}
	for _, field := range fields {
		out[field.Key] = field.Value
		if jsonObjectSize(out) <= maxChars {
			continue
		}
		delete(out, field.Key)
		if partial, ok := fitExtractValue(out, field.Key, field.Value, maxChars); ok {
			out[field.Key] = partial
		}
	}
	return out
}

func fitExtractValue(base map[string]any, key string, value any, maxChars int) (any, bool) {
	switch typed := value.(type) {
	case string:
		runes := []rune(typed)
		low, high := 0, len(runes)
		for low < high {
			mid := (low + high + 1) / 2
			base[key] = string(runes[:mid])
			if jsonObjectSize(base) <= maxChars {
				low = mid
			} else {
				high = mid - 1
			}
		}
		delete(base, key)
		if low == 0 {
			return nil, false
		}
		return string(runes[:low]), true
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for itemKey := range typed {
			keys = append(keys, itemKey)
		}
		sort.Strings(keys)
		partial := map[string]any{}
		for _, itemKey := range keys {
			partial[itemKey] = typed[itemKey]
			base[key] = partial
			if jsonObjectSize(base) > maxChars {
				delete(partial, itemKey)
			}
		}
		delete(base, key)
		if len(partial) == 0 {
			return nil, false
		}
		return partial, true
	}

	rv := reflect.ValueOf(value)
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return nil, false
	}
	low, high := 0, rv.Len()
	for low < high {
		mid := (low + high + 1) / 2
		partial := rv.Slice(0, mid).Interface()
		base[key] = partial
		if jsonObjectSize(base) <= maxChars {
			low = mid
		} else {
			high = mid - 1
		}
	}
	delete(base, key)
	if low == 0 {
		return nil, false
	}
	return rv.Slice(0, low).Interface(), true
}

func jsonObjectSize(value map[string]any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return maxExtractChars + 1
	}
	return len(encoded)
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
	sess, ok := a.reg.get(id)
	if !ok {
		return nil, computerUseFailure("session_not_active", id, nil, action,
			"app session is not active",
			"Open a new browser_session using context_id or context_name, then retry from a fresh screenshot.",
			nil)
	}
	sess.actionMu.Lock()
	defer sess.actionMu.Unlock()
	if action == "navigate" {
		if !validToolNavigationURL(stringArg(args, "url")) {
			return nil, computerUseFailure("invalid_navigation", id, sess, action,
				"navigate requires a usable absolute URL",
				"Pass action=navigate with an absolute http(s) URL in url.", nil)
		}
	}
	if (action == "navigate" || action == "back" || action == "reload") && sess.backend == "service" {
		return nil, computerUseFailure("backend_not_supported", id, sess, action,
			fmt.Sprintf("backend %q does not expose verifiable navigation", sess.backend),
			"Use local, Browserbase, Steel, or browser-engine for navigate, back, and reload.", nil)
	}
	if err := validateClickTargetArgs(action, args); err != nil {
		return nil, computerUseFailure("invalid_target", id, sess, action,
			err.Error(),
			"Call computer_use(action=\"screenshot\", som=true), then click a visible label >= 1, or pass coordinate=\"x,y\".",
			nil)
	}
	if err := validateSelectOptionArgs(action, args); err != nil {
		return nil, computerUseFailure("invalid_target", id, sess, action,
			err.Error(),
			"Call computer_use(action=\"screenshot\", som=true), then use action=select_option with label=N or selector plus text/value.",
			nil)
	}
	if err := validateSetCheckedArgs(action, args); err != nil {
		return nil, computerUseFailure("invalid_target", id, sess, action,
			err.Error(),
			"Call computer_use(action=\"screenshot\", som=true), then use action=set_checked with label=N or selector plus checked=true|false.",
			nil)
	}
	if err := validateSetTemporalArgs(action, args); err != nil {
		return nil, computerUseFailure("invalid_target", id, sess, action,
			err.Error(),
			"Call computer_use(action=\"screenshot\", som=true), then use action=set_temporal with label=N or selector plus value.",
			nil)
	}
	if err := validateSetTextArgs(action, args); err != nil {
		return nil, computerUseFailure("invalid_target", id, sess, action,
			err.Error(),
			"Call computer_use(action=\"screenshot\", som=true), then use action=set_text with label=N or selector plus text.",
			nil)
	}
	if tabID := stringArg(args, "tab_id"); tabID != "" {
		tc, ok := sess.comp.(backends.TabController)
		if !ok {
			return nil, fmt.Errorf("backend %q does not support tabs", sess.backend)
		}
		if err := tc.SwitchTab(tabID); err != nil {
			return nil, fmt.Errorf("switch tab %s: %w", tabID, err)
		}
	}

	act := backends.Action{
		Type:         action,
		Label:        intArg(args, "label"),
		Selector:     stringArg(args, "selector"),
		Text:         stringArg(args, "text"),
		Value:        stringArg(args, "value"),
		Texts:        stringSliceArg(args, "texts"),
		Values:       stringSliceArg(args, "values"),
		Mode:         stringArg(args, "mode"),
		NewlineMode:  stringArg(args, "newline_mode"),
		Key:          stringArg(args, "key"),
		Direction:    stringArg(args, "direction"),
		Amount:       intArg(args, "amount"),
		URL:          strings.TrimSpace(stringArg(args, "url")),
		Duration:     intArg(args, "duration"),
		Presentation: sess.presentation,
	}
	if checked, ok := boolArg(args, "checked"); ok {
		act.Checked = checked
	}
	act.X, act.Y = coordinateArg(args)
	// Some model/tool serializers populate an omitted optional integer with its
	// schema minimum (label=1). When the caller also supplied a coordinate, the
	// coordinate is the unambiguous explicit target and must win over that
	// synthetic label value.
	if (action == "click" || action == "double_click") && strings.TrimSpace(stringArg(args, "coordinate")) != "" {
		act.Label = 0
	}
	if (action == "click" || action == "double_click") && act.Label > 0 && !hasSetOfMarkLabel(sess.comp, act.Label) {
		return nil, computerUseFailure("invalid_target", id, sess, action,
			fmt.Sprintf("Set-of-Mark label %d is not present in the latest annotated screenshot", act.Label),
			"Take a fresh screenshot with annotate=true, then use one of its current labels.",
			nil)
	}
	var uploadMeta map[string]any
	var uploadCleanup func()
	if action == "upload_file" {
		if sess.backend != "local" && sess.backend != "browserbase" {
			return nil, computerUseFailure("backend_not_supported", id, sess, action,
				fmt.Sprintf("backend %q does not support upload_file yet", sess.backend),
				"Use local or Browserbase for file uploads, or ask the user to upload manually.",
				nil)
		}
		files, meta, cleanup, err := prepareUploadFiles(args)
		if err != nil {
			return nil, computerUseFailure("invalid_file_source", id, sess, action,
				err.Error(),
				uploadFileRecoverHint(args),
				nil)
		}
		act.Files = files
		uploadMeta = meta
		uploadCleanup = cleanup
		defer uploadCleanup()
	}
	if action == "select_option" && sess.backend != "local" && sess.backend != "browserbase" && sess.backend != "steel" && sess.backend != "browser-engine" {
		return nil, computerUseFailure("backend_not_supported", id, sess, action,
			fmt.Sprintf("backend %q does not support select_option yet", sess.backend),
			"Use local, Browserbase, Steel, or browser-engine for select_option, or use screenshot plus click/key fallback.",
			nil)
	}
	if (action == "set_checked" || action == "set_temporal" || action == "set_text") && sess.backend != "local" && sess.backend != "browserbase" {
		return nil, computerUseFailure("backend_not_supported", id, sess, action,
			fmt.Sprintf("backend %q does not support %s yet", sess.backend, action),
			"Use local or Browserbase for this DOM-targeted action, or use screenshot plus click/key/type fallback.",
			nil)
	}

	beforeURL := currentURL(sess.comp)
	beforeTabs := []backends.TabInfo(nil)
	if action == "click" || action == "double_click" {
		beforeTabs = tabsFor(sess.comp)
	}
	var shot []byte
	var err error
	screenshotSkipped := false
	includeSOM := action == "screenshot" && boolArgDefault(args, "include_som", false)
	annotate := annotateArg(args, true)
	if includeSOM {
		annotate = true
	}
	if action == "screenshot" {
		shot, err = screenshotWithOptions(sess.comp, annotate)
	} else if shouldSkipPostActionScreenshot(sess, action) {
		err = sess.comp.(backends.ActionOnlyExecutor).ExecuteAction(act)
		screenshotSkipped = true
	} else {
		shot, err = sess.comp.Execute(act)
	}
	if err != nil {
		if isSessionUnhealthyError(err) {
			return nil, a.closeUnhealthySession(ctx, id, sess, action, err)
		}
		if isActionTimeoutError(err) {
			return nil, computerUseFailure("action_timeout", id, sess, action,
				"browser action timed out",
				"Take a fresh screenshot. If the page is frozen, close this session and reopen from context.",
				err)
		}
		return nil, computerUseFailure("backend_error", id, sess, action,
			fmt.Sprintf("browser action failed: %v", err),
			"Take a fresh screenshot and retry once. If the session is stale, close it and reopen from context.",
			err)
	}
	tabEvent := tabFollowResult{}
	if len(beforeTabs) > 0 && (action == "click" || action == "double_click") {
		tabEvent = autoFollowNewTab(sess.comp, beforeTabs)
		if tabEvent.Switched && !screenshotSkipped {
			if reshot, serr := screenshotWithOptions(sess.comp, annotateArg(args, true)); serr == nil {
				shot = reshot
			} else if ctx != nil {
				ctx.Logger().Warn("new tab screenshot failed", "session_id", id, "err", serr.Error())
			}
		}
	}
	afterURL := currentURL(sess.comp)
	if action == "back" && !navigationURLChanged(beforeURL, afterURL) {
		return nil, computerUseFailure("navigation_ineffective", id, sess, action,
			"browser history did not move to a different URL",
			"Take a fresh screenshot and use action=navigate with an explicit URL if the destination is known.", nil)
	}
	if action == "navigate" && !navigationURLChanged(beforeURL, afterURL) && !navigationURLsEquivalent(act.URL, afterURL) {
		return nil, computerUseFailure("navigation_ineffective", id, sess, action,
			fmt.Sprintf("browser remained at %q", afterURL),
			"Check the URL, then retry action=navigate once or take a fresh screenshot for redirects and blockers.", nil)
	}
	disp := sess.comp.DisplaySize()
	payload := a.sessionActionPayload(id, sess, act, args)
	for k, v := range uploadMeta {
		payload[k] = v
	}
	if screenshotSkipped {
		payload["screenshot_available"] = false
		payload["post_action_screenshot"] = "skipped"
	}
	recovery := screenshotRecoveryFor(sess.comp)
	var selectResult *selectinput.Result
	if action == "select_option" {
		selectResult = selectResultFor(sess.comp)
	}
	var checkedResult *checkedinput.Result
	if action == "set_checked" {
		checkedResult = checkedResultFor(sess.comp)
	}
	var temporalResult *temporalinput.Result
	if action == "set_temporal" {
		temporalResult = temporalResultFor(sess.comp)
	}
	var textResult *textinput.SetResult
	if action == "set_text" {
		textResult = textResultFor(sess.comp)
	}
	mergeSelectResultPayload(payload, selectResult)
	mergeCheckedResultPayload(payload, checkedResult)
	mergeTemporalResultPayload(payload, temporalResult)
	mergeTextResultPayload(payload, textResult)
	mergeScreenshotRecoveryPayload(payload, recovery)
	mergeTabFollowPayload(payload, tabEvent)
	mergeNavigationDelta(payload, action, beforeURL, afterURL, act.URL)
	emitEvent(ctx, "session.action", payload)
	out := map[string]any{
		"session_id":  id,
		"current_url": afterURL,
	}
	if tabID := activeTabID(sess.comp); tabID != "" && (action == "screenshot" || tabEvent.Switched || stringArg(args, "tab_id") != "") {
		out["active_tab_id"] = tabID
	}
	if action == "screenshot" {
		out["width"] = disp.Width
		out["height"] = disp.Height
	}
	mergeNavigationDelta(out, action, beforeURL, afterURL, act.URL)
	if screenshotSkipped {
		out["text"] = fmt.Sprintf("Success: %s action dispatched. Call action=wait if needed, then action=screenshot for the visual state.", action)
		out["screenshot_available"] = false
		out["post_action_screenshot"] = "skipped"
		out["next_step"] = "Call computer_use(action=wait, duration=1000), then computer_use(action=screenshot)."
	} else {
		mime := imageMIME(shot)
		out["text"] = fmt.Sprintf("Success: %s action completed. Screenshot attached.", action)
		out["screenshot"] = binaryEnvelope(shot, mime)
		out["screenshot_b64"] = base64.StdEncoding.EncodeToString(shot)
		out["mime_type"] = mime
	}
	if includeSOM && !screenshotSkipped {
		out["som"] = setOfMarkFor(sess.comp)
	}
	for k, v := range uploadMeta {
		out[k] = v
	}
	mergeSelectResultPayload(out, selectResult)
	mergeCheckedResultPayload(out, checkedResult)
	mergeTemporalResultPayload(out, temporalResult)
	mergeTextResultPayload(out, textResult)
	mergeScreenshotRecoveryPayload(out, recovery)
	mergeTabFollowPayload(out, tabEvent)
	return out, nil
}

func (a *App) closeUnhealthySession(ctx *sdk.AppCtx, id string, sess *session, action string, cause error) error {
	a.reg.remove(id)
	extra := map[string]any{"action": action}
	if cause != nil {
		extra["error"] = cause.Error()
	}
	if _, err := a.finalizeSession(ctx, id, sess, "failed", "session_unhealthy", "session.closed", extra); err != nil && ctx != nil {
		ctx.Logger().Warn("persist unhealthy browser session", "session_id", id, "err", err.Error())
	}
	return computerUseFailure("session_unhealthy", id, sess, action,
		"browser session is no longer usable",
		reopenSessionRecoverHint(sess),
		cause)
}

func shouldSkipPostActionScreenshot(sess *session, action string) bool {
	if sess == nil || sess.backend != "browserbase" {
		return false
	}
	if os.Getenv("APTEVA_BROWSERBASE_SPLIT_ACTION_SCREENSHOT") != "1" {
		return false
	}
	switch action {
	case "click", "double_click", "wait":
	default:
		return false
	}
	_, ok := sess.comp.(backends.ActionOnlyExecutor)
	return ok
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
	sess.actionMu.Lock()
	defer sess.actionMu.Unlock()
	row, err := a.finalizeSession(ctx, id, sess, "closed", "explicit_close", "session.closed")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"closed":           true,
		"session_id":       id,
		"status":           row.Status,
		"recording_status": row.RecordingStatus,
	}, nil
}

type tabFollowResult struct {
	Switched   bool
	NewTabs    []backends.TabInfo
	AfterCount int
}

func autoFollowNewTab(comp backends.Computer, before []backends.TabInfo) tabFollowResult {
	tc, ok := comp.(backends.TabController)
	if !ok {
		return tabFollowResult{}
	}
	after, err := tc.ListTabs()
	if err != nil {
		return tabFollowResult{}
	}
	newTabs := newTabsSince(before, after)
	result := tabFollowResult{NewTabs: newTabs, AfterCount: len(after)}
	if len(newTabs) != 1 {
		return result
	}
	if err := tc.SwitchTab(newTabs[0].ID); err != nil {
		return result
	}
	result.Switched = true
	return result
}

func newTabsSince(before, after []backends.TabInfo) []backends.TabInfo {
	seen := map[string]bool{}
	for _, tab := range before {
		if tab.ID != "" {
			seen[tab.ID] = true
		}
	}
	out := make([]backends.TabInfo, 0)
	for _, tab := range after {
		if tab.ID == "" || seen[tab.ID] {
			continue
		}
		out = append(out, tab)
	}
	return out
}

func mergeTabFollowPayload(payload map[string]any, result tabFollowResult) {
	if len(result.NewTabs) > 0 {
		payload["new_tabs"] = result.NewTabs
		payload["tab_count"] = result.AfterCount
	}
	if result.Switched {
		payload["switched_tab"] = true
	}
}

func mergeNavigationDelta(payload map[string]any, action, beforeURL, afterURL, requestedURL string) {
	changed := navigationURLChanged(beforeURL, afterURL)
	if changed {
		payload["previous_url"] = beforeURL
		payload["url_changed"] = true
	}
	switch action {
	case "navigate":
		payload["navigation"] = "navigate"
		payload["requested_url"] = requestedURL
		if !changed {
			payload["url_changed"] = false
		}
	case "back":
		payload["navigation"] = "back"
		payload["previous_url"] = beforeURL
		payload["url_changed"] = changed
	case "reload":
		payload["navigation"] = "reload"
		payload["reloaded"] = true
		payload["url_changed"] = changed
	}
}

func navigationURLChanged(beforeURL, afterURL string) bool {
	if strings.TrimSpace(beforeURL) == "" || strings.TrimSpace(afterURL) == "" {
		return false
	}
	return !navigationURLsEquivalent(beforeURL, afterURL)
}

func navigationURLsEquivalent(left, right string) bool {
	normalize := func(raw string) string {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return strings.TrimSpace(raw)
		}
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		if (u.Scheme == "http" || u.Scheme == "https") && u.Path == "" {
			u.Path = "/"
		}
		return u.String()
	}
	return normalize(left) == normalize(right)
}

func validToolNavigationURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

type selectResultReporter interface {
	LastSelectResult() *selectinput.Result
}

type checkedResultReporter interface {
	LastCheckedResult() *checkedinput.Result
}

type temporalResultReporter interface {
	LastTemporalResult() *temporalinput.Result
}

type textResultReporter interface {
	LastTextResult() *textinput.SetResult
}

func selectResultFor(comp backends.Computer) *selectinput.Result {
	reporter, ok := comp.(selectResultReporter)
	if !ok {
		return nil
	}
	return reporter.LastSelectResult()
}

func checkedResultFor(comp backends.Computer) *checkedinput.Result {
	reporter, ok := comp.(checkedResultReporter)
	if !ok {
		return nil
	}
	return reporter.LastCheckedResult()
}

func temporalResultFor(comp backends.Computer) *temporalinput.Result {
	reporter, ok := comp.(temporalResultReporter)
	if !ok {
		return nil
	}
	return reporter.LastTemporalResult()
}

func textResultFor(comp backends.Computer) *textinput.SetResult {
	reporter, ok := comp.(textResultReporter)
	if !ok {
		return nil
	}
	return reporter.LastTextResult()
}

func mergeSelectResultPayload(payload map[string]any, result *selectinput.Result) {
	if result == nil {
		return
	}
	payload["select_kind"] = result.Kind
	payload["select_mode"] = result.Mode
	payload["select_multiple"] = result.Multiple
	if result.ControlText != "" {
		payload["select_control_text"] = result.ControlText
	}
	if len(result.Matched) > 0 {
		payload["select_matched"] = result.Matched
	}
	if len(result.Selected) > 0 {
		payload["select_selected"] = result.Selected
	}
	if len(result.Options) > 0 {
		payload["select_options"] = result.Options
	}
}

func mergeCheckedResultPayload(payload map[string]any, result *checkedinput.Result) {
	if result == nil {
		return
	}
	payload["checked_kind"] = result.Kind
	payload["checked"] = result.Checked
	payload["checked_previous"] = result.PreviousChecked
	payload["checked_changed"] = result.Changed
	if result.Selector != "" {
		payload["checked_selector"] = result.Selector
	}
	if result.Role != "" {
		payload["checked_role"] = result.Role
	}
	if result.Label != "" {
		payload["checked_label"] = result.Label
	}
}

func mergeTemporalResultPayload(payload map[string]any, result *temporalinput.Result) {
	if result == nil {
		return
	}
	payload["temporal_kind"] = result.Kind
	payload["temporal_value"] = result.Value
	payload["temporal_previous_value"] = result.PreviousValue
	payload["temporal_changed"] = result.Changed
	if result.Selector != "" {
		payload["temporal_selector"] = result.Selector
	}
	if result.Label != "" {
		payload["temporal_label"] = result.Label
	}
	if result.InputType != "" {
		payload["temporal_input_type"] = result.InputType
	}
}

func mergeTextResultPayload(payload map[string]any, result *textinput.SetResult) {
	if result == nil {
		return
	}
	payload["text_kind"] = result.Kind
	payload["text_value"] = result.Value
	payload["text_previous_value"] = result.PreviousValue
	payload["text_changed"] = result.Changed
	payload["text_mode"] = result.Mode
	payload["text_newline_mode"] = result.NewlineMode
	if result.Selector != "" {
		payload["text_selector"] = result.Selector
	}
	if result.Label != "" {
		payload["text_label"] = result.Label
	}
	if result.InputType != "" {
		payload["text_input_type"] = result.InputType
	}
}

func screenshotRecoveryFor(comp backends.Computer) *backends.ScreenshotRecoveryInfo {
	reporter, ok := comp.(backends.ScreenshotRecoveryReporter)
	if !ok {
		return nil
	}
	return reporter.LastScreenshotRecovery()
}

func setOfMarkFor(comp backends.Computer) []backends.SetOfMarkTarget {
	reporter, ok := comp.(backends.SetOfMarkReporter)
	if !ok {
		return nil
	}
	return reporter.LastSetOfMark()
}

func hasSetOfMarkLabel(comp backends.Computer, label int) bool {
	if label <= 0 {
		return false
	}
	for _, target := range setOfMarkFor(comp) {
		if target.Label == label {
			return true
		}
	}
	return false
}

func mergeScreenshotRecoveryPayload(payload map[string]any, info *backends.ScreenshotRecoveryInfo) {
	if info == nil || !info.Recovered {
		return
	}
	payload["screenshot_recovered"] = true
	payload["screenshot_recovery"] = info.Strategy
	if info.PreviousTabID != "" {
		payload["screenshot_recovery_previous_tab_id"] = info.PreviousTabID
	}
	if info.ActiveTabID != "" {
		payload["screenshot_recovery_active_tab_id"] = info.ActiveTabID
	}
	if info.URL != "" {
		payload["screenshot_recovery_url"] = info.URL
	}
	if info.Cause != "" {
		payload["screenshot_recovery_cause"] = info.Cause
	}
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
			rows := a.reapIdleSessions(ctx, idleTTL)
			for _, row := range rows {
				reason := "idle_timeout"
				if row.ProviderExpired {
					reason = "provider_timeout"
				}
				ctx.Logger().Info("reaped browser session", "session_id", row.ID, "reason", reason, "idle_ttl", idleTTL.String())
			}
		}
	}
}

func (a *App) reapIdleSessions(ctx *sdk.AppCtx, ttl time.Duration) []reapedSession {
	rows := a.reg.reapIdleDetails(ttl)
	for _, row := range rows {
		reason := "idle_timeout"
		if row.ProviderExpired {
			reason = "provider_timeout"
		}
		row.sess.actionMu.Lock()
		_, err := a.finalizeSession(ctx, row.ID, row.sess, "reaped", reason, "session.reaped", map[string]any{
			"idle_seconds": int(row.Idle.Seconds()),
			"reap_reason":  reason,
		})
		row.sess.actionMu.Unlock()
		if err != nil && ctx != nil {
			ctx.Logger().Warn("persist reaped browser session", "session_id", row.ID, "err", err.Error())
		}
	}
	return rows
}

// ─── Helpers ───────────────────────────────────────────────────────

func emitEvent(ctx *sdk.AppCtx, topic string, data map[string]any) {
	if ctx == nil {
		return
	}
	ctx.Emit(topic, data)
}

func recordingSupported(backend string) bool {
	return backend == "browserbase" || backend == "steel"
}

func activeRecordingStatus(backend string) string {
	if recordingSupported(backend) {
		return "recording"
	}
	return "unsupported"
}

func sessionBackendID(s *session) string {
	if s == nil {
		return ""
	}
	if s.backendSessionID != "" {
		return s.backendSessionID
	}
	return backendSessionID(s.comp)
}

func sessionRecord(id string, s *session, status, closeReason string, closedAt *time.Time) *ComputerSession {
	now := time.Now().UTC()
	row := &ComputerSession{
		ID:               id,
		Backend:          s.backend,
		BackendSessionID: sessionBackendID(s),
		AppContextID:     s.appContextID,
		ContextName:      s.contextName,
		InitialURL:       s.initialURL,
		CurrentURL:       currentURL(s.comp),
		Status:           status,
		CloseReason:      closeReason,
		RecordingStatus:  activeRecordingStatus(s.backend),
		OpenedAt:         s.openedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:        now.Format(time.RFC3339Nano),
	}
	if s.comp != nil {
		display := s.comp.DisplaySize()
		row.Width = display.Width
		row.Height = display.Height
	}
	if closedAt != nil {
		value := closedAt.UTC().Format(time.RFC3339Nano)
		row.ClosedAt = &value
		if recordingSupported(s.backend) {
			row.RecordingStatus = "processing"
		}
	}
	return row
}

func (a *App) finalizeSession(ctx *sdk.AppCtx, id string, s *session, status, closeReason, event string, extras ...map[string]any) (*ComputerSession, error) {
	if s == nil {
		return nil, fmt.Errorf("session %s is unavailable", id)
	}
	payload := a.sessionEventPayload(id, s)
	active := sessionRecord(id, s, "active", "", nil)
	var persistErr error
	if ctx == nil || ctx.AppDB() == nil {
		persistErr = errors.New("computer session history is unavailable")
	} else {
		persistErr = dbPutSession(ctx.AppDB(), active)
	}
	if err := s.comp.Close(); err != nil && ctx != nil {
		ctx.Logger().Warn("browser_close underlying Close error", "session_id", id, "err", err.Error())
	}
	closedAt := time.Now().UTC()
	row := *active
	row.Status = status
	row.CloseReason = closeReason
	row.UpdatedAt = closedAt.Format(time.RFC3339Nano)
	closedValue := closedAt.Format(time.RFC3339Nano)
	row.ClosedAt = &closedValue
	if recordingSupported(row.Backend) {
		row.RecordingStatus = "processing"
	} else {
		row.RecordingStatus = "unsupported"
	}
	if ctx != nil && ctx.AppDB() != nil {
		if err := dbPutSession(ctx.AppDB(), &row); err != nil {
			persistErr = errors.Join(persistErr, err)
		}
	}
	payload["status"] = row.Status
	payload["close_reason"] = row.CloseReason
	payload["recording_supported"] = recordingSupported(row.Backend)
	payload["recording_status"] = row.RecordingStatus
	payload["closed_at"] = closedValue
	for _, extra := range extras {
		for key, value := range extra {
			payload[key] = value
		}
	}
	emitEvent(ctx, event, payload)
	return &row, persistErr
}

func (a *App) sessionEventPayload(id string, s *session) map[string]any {
	info := a.sessionInfo(id, s)
	return map[string]any{
		"session_id":          info.SessionID,
		"backend_session_id":  info.BackendSessionID,
		"backend":             info.Backend,
		"presentation_mode":   info.PresentationMode,
		"status":              info.Status,
		"recording_supported": info.RecordingSupported,
		"recording_status":    info.RecordingStatus,
		"context_id":          info.ContextID,
		"app_context_id":      info.AppContextID,
		"context_name":        info.ContextName,
		"persist":             info.Persist,
		"timeout_seconds":     info.TimeoutSeconds,
		"provider_expires_at": info.ProviderExpiresAt,
		"current_url":         info.CurrentURL,
		"active_tab_id":       info.ActiveTabID,
		"tabs":                info.Tabs,
		"tab_count":           info.TabCount,
		"width":               info.Width,
		"height":              info.Height,
		"opened_at":           info.OpenedAt,
		"last_used_at":        info.LastUsedAt,
	}
}

func (a *App) sessionActionPayload(id string, s *session, act backends.Action, args map[string]any) map[string]any {
	payload := a.sessionEventPayload(id, s)
	payload["action"] = act.Type
	switch act.Type {
	case "click", "double_click":
		if act.Label > 0 {
			payload["label"] = act.Label
		}
		if hasCoordinateArg(args) {
			payload["coordinate"] = fmt.Sprintf("%d,%d", act.X, act.Y)
		}
	case "type":
		payload["text_length"] = len([]rune(act.Text))
	case "key":
		if act.Key != "" {
			payload["key"] = act.Key
		}
	case "scroll":
		if act.Direction != "" {
			payload["direction"] = act.Direction
		}
		if act.Amount != 0 {
			payload["amount"] = act.Amount
		}
	case "wait":
		if act.Duration != 0 {
			payload["duration"] = act.Duration
		}
	case "upload_file":
		if act.Selector != "" {
			payload["selector"] = act.Selector
		}
		if act.Label > 0 {
			payload["label"] = act.Label
		}
		if hasCoordinateArg(args) {
			payload["coordinate"] = fmt.Sprintf("%d,%d", act.X, act.Y)
		}
	case "select_option":
		if act.Selector != "" {
			payload["selector"] = act.Selector
		}
		if act.Label > 0 {
			payload["label"] = act.Label
		}
		if hasCoordinateArg(args) {
			payload["coordinate"] = fmt.Sprintf("%d,%d", act.X, act.Y)
		}
		if act.Text != "" {
			payload["text"] = act.Text
		}
		if act.Value != "" {
			payload["value"] = act.Value
		}
		if len(act.Texts) > 0 {
			payload["texts"] = act.Texts
		}
		if len(act.Values) > 0 {
			payload["values"] = act.Values
		}
		if act.Mode != "" {
			payload["mode"] = act.Mode
		}
	case "set_checked":
		if act.Selector != "" {
			payload["selector"] = act.Selector
		}
		if act.Label > 0 {
			payload["label"] = act.Label
		}
		if hasCoordinateArg(args) {
			payload["coordinate"] = fmt.Sprintf("%d,%d", act.X, act.Y)
		}
		payload["checked_requested"] = act.Checked
	case "set_temporal":
		if act.Selector != "" {
			payload["selector"] = act.Selector
		}
		if act.Label > 0 {
			payload["label"] = act.Label
		}
		if hasCoordinateArg(args) {
			payload["coordinate"] = fmt.Sprintf("%d,%d", act.X, act.Y)
		}
		if act.Value != "" {
			payload["value"] = act.Value
		} else if act.Text != "" {
			payload["text"] = act.Text
		}
	case "set_text":
		if act.Selector != "" {
			payload["selector"] = act.Selector
		}
		if act.Label > 0 {
			payload["label"] = act.Label
		}
		if hasCoordinateArg(args) {
			payload["coordinate"] = fmt.Sprintf("%d,%d", act.X, act.Y)
		}
		if act.Text != "" {
			payload["text"] = act.Text
		} else if act.Value != "" {
			payload["value"] = act.Value
		}
		if act.Mode != "" {
			payload["mode"] = act.Mode
		}
		if act.NewlineMode != "" {
			payload["newline_mode"] = act.NewlineMode
		}
	case "screenshot":
		payload["annotate"] = annotateArg(args, true)
	}
	return payload
}

func reapedSessionEventPayload(row reapedSession) map[string]any {
	return map[string]any{
		"session_id":          row.ID,
		"backend_session_id":  row.BackendSessionID,
		"backend":             row.Backend,
		"context_id":          row.ContextID,
		"app_context_id":      row.AppContextID,
		"context_name":        row.ContextName,
		"persist":             row.Persist,
		"timeout_seconds":     row.TimeoutSeconds,
		"provider_expires_at": providerExpiresAt(row.OpenedAt, row.TimeoutSeconds),
		"current_url":         row.CurrentURL,
		"width":               row.Width,
		"height":              row.Height,
		"opened_at":           row.OpenedAt.UTC().Format(time.RFC3339),
		"last_used_at":        row.LastUsedAt.UTC().Format(time.RFC3339),
		"idle_seconds":        int(row.Idle.Seconds()),
	}
}

func contextEventPayload(rec *ComputerContext) map[string]any {
	if rec == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":                  rec.ID,
		"name":                rec.Name,
		"backend":             rec.Backend,
		"provider_context_id": rec.ProviderContextID,
		"persist_default":     rec.PersistDefault,
		"auto_created":        rec.AutoCreated,
		"created_at":          rec.CreatedAt,
		"updated_at":          rec.UpdatedAt,
		"last_used_at":        rec.LastUsedAt,
	}
}

func providerExpiresAt(opened time.Time, timeoutSeconds int) string {
	if timeoutSeconds <= 0 {
		return ""
	}
	return opened.Add(time.Duration(timeoutSeconds) * time.Second).UTC().Format(time.RFC3339)
}

func settingsEventPayload(settings ComputerSettings) map[string]any {
	return map[string]any{
		"default_backend": settings.DefaultBackend,
		"lock_backend":    settings.LockBackend,
	}
}

func resolveContextListBackend(ctx *sdk.AppCtx, requested string) (string, error) {
	requested = strings.TrimSpace(strings.ToLower(requested))
	switch requested {
	case "", "all", "*":
		return "", nil
	case "default", "auto":
		settings, err := currentSettings(ctx)
		if err != nil {
			return "", err
		}
		if !isContextBackend(settings.DefaultBackend) {
			return "", nil
		}
		return settings.DefaultBackend, nil
	default:
		backend := normalizeBackend(requested)
		if !isContextBackend(backend) {
			return "", fmt.Errorf("backend %q does not support managed contexts", requested)
		}
		return backend, nil
	}
}

func contextBackends(rows []*ComputerContext) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, row := range rows {
		if row == nil || row.Backend == "" || seen[row.Backend] {
			continue
		}
		seen[row.Backend] = true
		out = append(out, row.Backend)
	}
	sort.Strings(out)
	return out
}

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

func currentSettings(ctx *sdk.AppCtx) (ComputerSettings, error) {
	db := appDB(ctx)
	if db == nil {
		return defaultComputerSettings(), nil
	}
	return dbGetSettings(db)
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
		cfg.KeepAlive = false
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

func usingProductionBackendFactory() bool {
	return reflect.ValueOf(newBackend).Pointer() == reflect.ValueOf(backends.New).Pointer()
}

func validateBackendConfigured(cfg backends.Config) error {
	switch cfg.Type {
	case "browserbase":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("backend_not_configured: browserbase api_key is required")
		}
	case "steel":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("backend_not_configured: steel api_key is required")
		}
	case "browser-engine":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("backend_not_configured: browser-engine api_key is required")
		}
	case "service":
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("backend_not_configured: service backend_url is required")
		}
	case "local":
		if err := validateLocalChromeConfigured(); err != nil {
			return err
		}
	}
	return nil
}

func validateLocalChromeConfigured() error {
	if chromeBin := strings.TrimSpace(os.Getenv("CHROME_BIN")); chromeBin != "" {
		if _, err := os.Stat(chromeBin); err != nil {
			return fmt.Errorf("backend_not_configured: local Chrome binary %q is not available: %w", chromeBin, err)
		}
		return nil
	}
	candidates := []string{"google-chrome", "chromium-browser", "chromium", "chrome"}
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		)
	case "windows":
		return nil
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "/") {
			if _, err := os.Stat(candidate); err == nil {
				return nil
			}
			continue
		}
		if _, err := exec.LookPath(candidate); err == nil {
			return nil
		}
	}
	return fmt.Errorf("backend_not_configured: local Chrome is not installed or not in PATH; use browserbase or configure CHROME_BIN")
}

func providerAttachError(backend, providerSessionID string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Invalid Session ID"),
		strings.Contains(msg, "TIMED_OUT"),
		strings.Contains(msg, "not RUNNING"),
		strings.Contains(msg, "not attachable"):
		return fmt.Errorf("provider_session_expired: %s provider session %s is not attachable; open a new browser session with context_id or context_name instead: %w", backend, providerSessionID, err)
	default:
		return fmt.Errorf("OpenSession: %w", err)
	}
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

func tabsFor(c backends.Computer) []backends.TabInfo {
	tc, ok := c.(backends.TabController)
	if !ok {
		return nil
	}
	tabs, err := tc.ListTabs()
	if err != nil {
		return nil
	}
	return tabs
}

func activeTabID(c backends.Computer) string {
	if tc, ok := c.(backends.TabController); ok {
		return tc.ActiveTabID()
	}
	return ""
}

func stringArg(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func stringSliceArg(args map[string]any, k string) []string {
	switch v := args[k].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil
		}
		var parsed []string
		if strings.HasPrefix(s, "[") && json.Unmarshal([]byte(s), &parsed) == nil {
			return parsed
		}
		return []string{s}
	default:
		return nil
	}
}

func intArg(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
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

func annotateArg(args map[string]any, def bool) bool {
	if v, ok := boolArg(args, "annotate"); ok {
		return v
	}
	if v, ok := boolArg(args, "som"); ok {
		return v
	}
	return def
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

func hasClickTargetArg(args map[string]any) bool {
	if intArg(args, "label") > 0 {
		return true
	}
	return hasCoordinateArg(args)
}

func validateClickTargetArgs(action string, args map[string]any) error {
	if action != "click" && action != "double_click" {
		return nil
	}
	if rawArgPresent(args, "label") && intArg(args, "label") <= 0 {
		return fmt.Errorf("label=%d is not clickable; label must be a positive label from the latest screenshot", intArg(args, "label"))
	}
	if !hasClickTargetArg(args) {
		return fmt.Errorf("%s requires label=N from the latest screenshot, or coordinate=\"x,y\" for targets without a badge", action)
	}
	return nil
}

func validateSelectOptionArgs(action string, args map[string]any) error {
	if action != "select_option" {
		return nil
	}
	if strings.TrimSpace(stringArg(args, "selector")) == "" && intArg(args, "label") <= 0 && !hasCoordinateArg(args) {
		return fmt.Errorf("select_option requires label=N, selector, or coordinate=\"x,y\"")
	}
	if strings.TrimSpace(stringArg(args, "text")) == "" &&
		strings.TrimSpace(stringArg(args, "value")) == "" &&
		len(stringSliceArg(args, "texts")) == 0 &&
		len(stringSliceArg(args, "values")) == 0 {
		return fmt.Errorf("select_option requires text/texts or value/values")
	}
	return nil
}

func validateSetCheckedArgs(action string, args map[string]any) error {
	if action != "set_checked" {
		return nil
	}
	if strings.TrimSpace(stringArg(args, "selector")) == "" && intArg(args, "label") <= 0 && !hasCoordinateArg(args) {
		return fmt.Errorf("set_checked requires label=N, selector, or coordinate=\"x,y\"")
	}
	if rawArgPresent(args, "label") && intArg(args, "label") <= 0 {
		return fmt.Errorf("label=%d is not valid; label must be a positive label from the latest screenshot", intArg(args, "label"))
	}
	if _, ok := boolArg(args, "checked"); !ok {
		return fmt.Errorf("set_checked requires checked=true or checked=false")
	}
	return nil
}

func validateSetTemporalArgs(action string, args map[string]any) error {
	if action != "set_temporal" {
		return nil
	}
	if strings.TrimSpace(stringArg(args, "selector")) == "" && intArg(args, "label") <= 0 && !hasCoordinateArg(args) {
		return fmt.Errorf("set_temporal requires label=N, selector, or coordinate=\"x,y\"")
	}
	if rawArgPresent(args, "label") && intArg(args, "label") <= 0 {
		return fmt.Errorf("label=%d is not valid; label must be a positive label from the latest screenshot", intArg(args, "label"))
	}
	if strings.TrimSpace(stringArg(args, "value")) == "" && strings.TrimSpace(stringArg(args, "text")) == "" {
		return fmt.Errorf("set_temporal requires value")
	}
	return nil
}

func validateSetTextArgs(action string, args map[string]any) error {
	if action != "set_text" {
		return nil
	}
	if strings.TrimSpace(stringArg(args, "selector")) == "" && intArg(args, "label") <= 0 && !hasCoordinateArg(args) {
		return fmt.Errorf("set_text requires label=N, selector, or coordinate=\"x,y\"")
	}
	if rawArgPresent(args, "label") && intArg(args, "label") <= 0 {
		return fmt.Errorf("label=%d is not valid; label must be a positive label from the latest screenshot", intArg(args, "label"))
	}
	if stringArg(args, "text") == "" && stringArg(args, "value") == "" {
		return fmt.Errorf("set_text requires text")
	}
	mode := strings.ToLower(strings.TrimSpace(stringArg(args, "mode")))
	if mode != "" && mode != "replace" && mode != "append" {
		return fmt.Errorf("set_text mode must be replace or append")
	}
	newlineMode := strings.ToLower(strings.TrimSpace(stringArg(args, "newline_mode")))
	if newlineMode != "" && newlineMode != "preserve" && newlineMode != "compact" {
		return fmt.Errorf("set_text newline_mode must be preserve or compact")
	}
	return nil
}

func hasCoordinateArg(args map[string]any) bool {
	if strings.TrimSpace(stringArg(args, "coordinate")) != "" {
		return true
	}
	if _, ok := args["x"]; ok {
		_, yOK := args["y"]
		return yOK
	}
	if _, ok := args["y"]; ok {
		_, xOK := args["x"]
		return xOK
	}
	return false
}

func rawArgPresent(args map[string]any, key string) bool {
	_, ok := args[key]
	return ok
}

func isSessionUnhealthyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	unhealthy := []string{
		"context canceled",
		"target closed",
		"browser has disconnected",
		"websocket: close",
		"no active session",
	}
	for _, needle := range unhealthy {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func isActionTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "action_timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "timed out")
}

func computerUseFailure(code, id string, sess *session, action, message, recover string, cause error) error {
	parts := []string{fmt.Sprintf("%s: %s", code, message)}
	if id != "" {
		parts = append(parts, "session_id="+id)
	}
	if action != "" {
		parts = append(parts, "action="+action)
	}
	if sess != nil {
		if sess.backend != "" {
			parts = append(parts, "backend="+sess.backend)
		}
		if sess.appContextID != "" {
			parts = append(parts, "context_id="+sess.appContextID)
		}
		if sess.contextName != "" {
			parts = append(parts, fmt.Sprintf("context_name=%q", sess.contextName))
		}
	}
	if recover != "" {
		parts = append(parts, "recover="+recover)
	}
	if cause != nil {
		parts = append(parts, fmt.Sprintf("cause=%q", cause.Error()))
	}
	return errors.New(strings.Join(parts, "; "))
}

func reopenSessionRecoverHint(sess *session) string {
	if sess != nil {
		if sess.appContextID != "" {
			return fmt.Sprintf("Call browser_session(action=\"open\", context_id=\"%s\"), then take a fresh screenshot before retrying.", sess.appContextID)
		}
		if sess.contextName != "" {
			return fmt.Sprintf("Call browser_session(action=\"open\", context_name=\"%s\"), then take a fresh screenshot before retrying.", sess.contextName)
		}
	}
	return "Open a new browser_session using the same context_id or context_name, then take a fresh screenshot before retrying."
}

func uploadFileRecoverHint(args map[string]any) string {
	if strings.TrimSpace(stringArg(args, "source_url")) != "" {
		return "The Computer app could not fetch source_url. Use a globally reachable URL, retry if the URL is temporary, or pass base64/file_path instead."
	}
	return "Pass exactly one of source_url, base64, or file_path. For base64, also pass filename."
}

func prepareUploadFiles(args map[string]any) ([]string, map[string]any, func(), error) {
	filePath := strings.TrimSpace(stringArg(args, "file_path"))
	sourceURL := strings.TrimSpace(stringArg(args, "source_url"))
	b64 := strings.TrimSpace(stringArg(args, "base64"))
	sources := 0
	for _, s := range []string{filePath, sourceURL, b64} {
		if s != "" {
			sources++
		}
	}
	if sources != 1 {
		return nil, nil, func() {}, fmt.Errorf("upload_file requires exactly one of source_url, base64, or file_path")
	}
	if filePath != "" {
		info, err := os.Stat(filePath)
		if err != nil {
			return nil, nil, func() {}, fmt.Errorf("file_path: %w", err)
		}
		if info.IsDir() {
			return nil, nil, func() {}, fmt.Errorf("file_path %q is a directory", filePath)
		}
		return []string{filePath}, uploadFileMeta(filePath, info.Size(), stringArg(args, "mime_type"), "file_path"), func() {}, nil
	}
	if sourceURL != "" {
		return prepareUploadFileFromURL(sourceURL, args)
	}
	return prepareUploadFileFromBase64(b64, args)
}

func prepareUploadFileFromURL(rawURL string, args map[string]any) ([]string, map[string]any, func(), error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, nil, func() {}, fmt.Errorf("source_url must be http or https")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	resp, err := fetchUploadSourceURL(ctx, rawURL)
	if err != nil {
		return nil, nil, func() {}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, func() {}, fmt.Errorf("source_url HTTP %d", resp.StatusCode)
	}
	filename := firstNonEmpty(stringArg(args, "filename"), filenameFromContentDisposition(resp.Header.Get("Content-Disposition")), filepath.Base(u.Path), "upload.bin")
	filename = safeUploadFilename(filename)
	tmpPath, cleanup, err := createUploadTempPath(filename)
	if err != nil {
		return nil, nil, func() {}, err
	}
	tmp, err := os.Create(tmpPath)
	if err != nil {
		cleanup()
		return nil, nil, func() {}, err
	}
	limited := io.LimitReader(resp.Body, maxUploadBytes+1)
	n, copyErr := io.Copy(tmp, limited)
	closeErr := tmp.Close()
	if copyErr != nil {
		cleanup()
		return nil, nil, func() {}, copyErr
	}
	if closeErr != nil {
		cleanup()
		return nil, nil, func() {}, closeErr
	}
	if n > maxUploadBytes {
		cleanup()
		return nil, nil, func() {}, fmt.Errorf("source_url file exceeds %d bytes", maxUploadBytes)
	}
	mimeType := firstNonEmpty(stringArg(args, "mime_type"), resp.Header.Get("Content-Type"))
	return []string{tmpPath}, uploadFileMeta(filename, n, mimeType, "source_url"), cleanup, nil
}

func fetchUploadSourceURL(ctx context.Context, rawURL string) (*http.Response, error) {
	resp, err := doUploadSourceGET(ctx, sourceURLHTTPClient, rawURL)
	if err == nil {
		return resp, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	ipv4Resp, ipv4Err := doUploadSourceGET(ctx, sourceURLIPv4HTTPClient, rawURL)
	if ipv4Err == nil {
		return ipv4Resp, nil
	}
	publicResp, publicErr := doUploadSourceGET(ctx, sourceURLPublicDNSHTTPClient, rawURL)
	if publicErr == nil {
		return publicResp, nil
	}
	return nil, fmt.Errorf("%w; IPv4 retry failed: %v; public DNS retry failed: %v", err, ipv4Err, publicErr)
}

func doUploadSourceGET(ctx context.Context, client *http.Client, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func ipv4OnlyTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tr := base.Clone()
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", address)
	}
	return tr
}

func publicDNSTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tr := base.Clone()
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}
			if conn, err := dialer.DialContext(ctx, "udp", "1.1.1.1:53"); err == nil {
				return conn, nil
			}
			return dialer.DialContext(ctx, "udp", "8.8.8.8:53")
		},
	}
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  resolver,
	}
	tr.DialContext = dialer.DialContext
	return tr
}

func prepareUploadFileFromBase64(raw string, args map[string]any) ([]string, map[string]any, func(), error) {
	filename := safeUploadFilename(firstNonEmpty(stringArg(args, "filename"), "upload.bin"))
	mimeType := stringArg(args, "mime_type")
	if strings.HasPrefix(raw, "data:") {
		if comma := strings.Index(raw, ","); comma >= 0 {
			header := raw[:comma]
			raw = raw[comma+1:]
			if strings.HasPrefix(header, "data:") {
				if semi := strings.Index(header, ";"); semi > len("data:") {
					mimeType = firstNonEmpty(mimeType, header[len("data:"):semi])
				}
			}
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("base64 decode: %w", err)
	}
	if len(decoded) > maxUploadBytes {
		return nil, nil, func() {}, fmt.Errorf("base64 file exceeds %d bytes", maxUploadBytes)
	}
	tmpPath, cleanup, err := createUploadTempPath(filename)
	if err != nil {
		return nil, nil, func() {}, err
	}
	tmp, err := os.Create(tmpPath)
	if err != nil {
		cleanup()
		return nil, nil, func() {}, err
	}
	if _, err := tmp.Write(decoded); err != nil {
		_ = tmp.Close()
		cleanup()
		return nil, nil, func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, nil, func() {}, err
	}
	return []string{tmpPath}, uploadFileMeta(filename, int64(len(decoded)), mimeType, "base64"), cleanup, nil
}

func createUploadTempPath(filename string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "apteva-upload-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	return filepath.Join(dir, filename), cleanup, nil
}

func uploadFileMeta(filename string, size int64, mimeType, source string) map[string]any {
	return map[string]any{
		"uploaded":    true,
		"filename":    filepath.Base(filename),
		"size_bytes":  size,
		"mime_type":   strings.TrimSpace(strings.Split(mimeType, ";")[0]),
		"file_source": source,
	}
}

func filenameFromContentDisposition(v string) string {
	if v == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(v)
	if err != nil {
		return ""
	}
	return params["filename"]
}

func safeUploadFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "/" || name == "" {
		name = "upload.bin"
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	name = strings.Trim(name, ".-")
	if name == "" {
		return "upload.bin"
	}
	return name
}

func sessionUnhealthyEventPayload(id string, sess *session, action string, cause error) map[string]any {
	payload := map[string]any{
		"session_id":   id,
		"action":       action,
		"close_reason": "session_unhealthy",
	}
	if sess == nil {
		return payload
	}
	payload["backend"] = sess.backend
	payload["backend_session_id"] = backendSessionID(sess.comp)
	payload["context_id"] = contextID(sess.comp)
	payload["app_context_id"] = sess.appContextID
	payload["context_name"] = sess.contextName
	payload["persist"] = sess.persist
	payload["timeout_seconds"] = sess.timeout
	payload["provider_expires_at"] = providerExpiresAt(sess.openedAt, sess.timeout)
	payload["opened_at"] = sess.openedAt.UTC().Format(time.RFC3339)
	payload["last_used_at"] = sess.lastUsed.UTC().Format(time.RFC3339)
	if sess.comp != nil {
		disp := sess.comp.DisplaySize()
		payload["width"] = disp.Width
		payload["height"] = disp.Height
	}
	if cause != nil {
		payload["error"] = cause.Error()
	}
	return payload
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

func screenshotWithOptions(comp backends.Computer, annotate bool) ([]byte, error) {
	if c, ok := comp.(backends.ScreenshotWithOptions); ok {
		return c.ScreenshotWithOptions(backends.ScreenshotOptions{Annotate: annotate})
	}
	return comp.Screenshot()
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

func appCtxForRequest(r *http.Request, args map[string]any) *sdk.AppCtx {
	ctx := globalCtx
	if ctx == nil {
		return nil
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		projectID = strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	}
	if projectID == "" && args != nil {
		projectID = firstNonEmpty(stringArg(args, "_project_id"), stringArg(args, "project_id"))
	}
	if projectID == "" {
		return ctx
	}
	return ctx.WithProject(projectID)
}

func (a *App) handleContextsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"backend": r.URL.Query().Get("backend")}
		out, err := a.toolContextList(appCtxForRequest(r, args), args)
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
		out, err := a.toolContextCreate(appCtxForRequest(r, body), body)
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
		args := map[string]any{"id": id}
		out, err := a.toolContextGet(appCtxForRequest(r, args), args)
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
		out, err := a.toolContextUpdate(appCtxForRequest(r, body), body)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, out)
	case http.MethodDelete:
		args := map[string]any{
			"id":              id,
			"delete_provider": r.URL.Query().Get("delete_provider"),
		}
		out, err := a.toolContextDelete(appCtxForRequest(r, args), args)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolSettingsGet(appCtxForRequest(r, nil), nil)
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
		out, err := a.toolSettingsUpdate(appCtxForRequest(r, body), body)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleListSessions(w http.ResponseWriter, r *http.Request) {
	rows, err := a.listUISessions(appCtxForRequest(r, nil), 100)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"sessions": rows})
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
	out, err := a.toolBrowserSession(appCtxForRequest(r, body), body)
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
	args := map[string]any{"session_id": id}
	out, err := a.toolBrowserClose(appCtxForRequest(r, args), args)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleSessionTabs(w http.ResponseWriter, r *http.Request) {
	id := pathSessionID(r.URL.Path, "/tabs")
	if id == "" {
		httpErr(w, http.StatusBadRequest, "session id required")
		return
	}
	args := map[string]any{"action": "tabs", "session_id": id}
	out, err := a.toolBrowserSession(appCtxForRequest(r, args), args)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleSwitchTab(w http.ResponseWriter, r *http.Request) {
	id, tabID := pathSessionTabID(r.URL.Path, "/switch")
	if id == "" || tabID == "" {
		httpErr(w, http.StatusBadRequest, "session id and tab id required")
		return
	}
	args := map[string]any{"action": "switch_tab", "session_id": id, "tab_id": tabID}
	out, err := a.toolBrowserSession(appCtxForRequest(r, args), args)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleCloseTab(w http.ResponseWriter, r *http.Request) {
	id, tabID := pathSessionTabID(r.URL.Path, "")
	if id == "" || tabID == "" {
		httpErr(w, http.StatusBadRequest, "session id and tab id required")
		return
	}
	args := map[string]any{"action": "close_tab", "session_id": id, "tab_id": tabID}
	out, err := a.toolBrowserSession(appCtxForRequest(r, args), args)
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
	out, err := a.toolComputerUse(appCtxForRequest(r, body), body)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleInternalSessionExtract(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get(internalAppCallerHeader)) == "" {
		httpErr(w, http.StatusForbidden, "internal app caller required")
		return
	}
	sessionID := pathInternalExtractSessionID(r.URL.Path)
	if sessionID == "" {
		httpErr(w, http.StatusBadRequest, "session id required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body extractRequest
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		httpErr(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		httpErr(w, http.StatusBadRequest, "bad JSON body: expected one JSON object")
		return
	}
	opts, err := normalizeExtractOptions(body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := a.extractSessionDOM(sessionID, opts)
	if err != nil {
		switch {
		case errors.Is(err, errExtractSessionNotFound):
			httpErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, errExtractUnsupported):
			httpErr(w, http.StatusNotImplemented, err.Error())
		default:
			httpErr(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	writeJSON(w, out)
}

func normalizeExtractOptions(body extractRequest) (extractOptions, error) {
	opts := extractOptions{Readability: true}
	if body.MaxChars != nil {
		opts.MaxChars = *body.MaxChars
	}
	if opts.MaxChars < 0 {
		opts.MaxChars = 0
	} else if opts.MaxChars > maxExtractChars {
		opts.MaxChars = maxExtractChars
	}
	if body.Readability != nil {
		opts.Readability = *body.Readability
	}
	if body.WaitMS != nil {
		opts.WaitMS = *body.WaitMS
	}
	if opts.WaitMS < 0 {
		opts.WaitMS = 0
	} else if opts.WaitMS > maxExtractWaitMS {
		opts.WaitMS = maxExtractWaitMS
	}

	seen := make(map[string]struct{}, len(body.Formats))
	for _, raw := range body.Formats {
		format := strings.ToLower(strings.TrimSpace(raw))
		switch format {
		case "text", "markdown", "html", "links", "images", "regions", "metadata", "structured_data", "json":
		default:
			return extractOptions{}, fmt.Errorf("unsupported extraction format %q", raw)
		}
		if _, ok := seen[format]; ok {
			continue
		}
		seen[format] = struct{}{}
		opts.Formats = append(opts.Formats, format)
	}
	return opts, nil
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
	sess.actionMu.Lock()
	defer sess.actionMu.Unlock()
	annotate := boolArgDefault(map[string]any{
		"annotate": r.URL.Query().Get("annotate"),
		"som":      r.URL.Query().Get("som"),
	}, "annotate", false)
	if r.URL.Query().Get("annotate") == "" && r.URL.Query().Get("som") != "" {
		annotate = boolArgDefault(map[string]any{"som": r.URL.Query().Get("som")}, "som", false)
	}
	shot, err := screenshotWithOptions(sess.comp, annotate)
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

func pathInternalExtractSessionID(path string) string {
	const prefix = "/internal/sessions/"
	const suffix = "/extract"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func pathSessionTabID(path, suffix string) (string, string) {
	rest := strings.TrimPrefix(path, "/sessions/")
	rest = strings.TrimSuffix(rest, suffix)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 3 || parts[1] != "tabs" {
		return "", ""
	}
	return parts[0], parts[2]
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
