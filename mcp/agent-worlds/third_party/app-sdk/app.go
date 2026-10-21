package sdk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// App is the single interface every Apteva app implements. Every facet
// is optional — return nil/zero to opt out. The platform calls methods
// in this order at boot:
//
//	Manifest  →  (server runs migrations)  →  OnMount  →  HTTPRoutes,
//	MCPTools, Channels, Workers, EventHandlers  (collected for mounting)
//
// Per-instance lifecycle hooks fire as instances bind/unbind to this
// app's installs at runtime, not at boot.
type App interface {
	Manifest() Manifest

	// OnMount runs once after the app DB is open + migrations have run.
	// Returning an error aborts the sidecar boot.
	OnMount(ctx *AppCtx) error

	// OnUnmount runs on graceful shutdown. Stop workers, flush state.
	OnUnmount(ctx *AppCtx) error

	HTTPRoutes() []Route
	MCPTools() []Tool
	Channels() []ChannelFactory
	Workers() []Worker
	EventHandlers() []EventHandler
}

// Route — single HTTP endpoint. The platform reverse-proxies to it
// under /apps/<name>/<pattern>.
//
// NoAuth opts the route out of the SDK's token-auth gate. Use it for
// endpoints that need to be reachable by parties that don't carry an
// APTEVA_APP_TOKEN — e.g. UPnP/DLNA wire endpoints accessed by smart
// TVs over the LAN, or any other "the LAN itself is the auth" model.
// The route handler becomes responsible for whatever access control
// is appropriate. Same exposure model as the existing /webhooks/*
// prefix carve-out, just opt-in per route.
type Route struct {
	Method  string // "" = any
	Pattern string // mux-style, may include params
	Handler http.HandlerFunc
	NoAuth  bool // bypass withTokenAuth for this route
}

// Tool — one MCP tool exposed by the app. The framework wires these
// into a single MCP endpoint at the sidecar's /mcp.
//
// Set Handler for the original install-scoped signature; set
// HandlerCtx to receive a per-call context.Context carrying the
// Caller (instance id + grants). Exactly one of Handler / HandlerCtx
// must be set; HandlerCtx wins if both are.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON schema
	Meta        map[string]any // MCP _meta extension fields
	// Exposure controls who may discover and invoke this tool. Empty and
	// ToolExposurePublic preserve the historical behavior: the tool is listed
	// to agents and may also be called by a bound app. ToolExposureAppOnly keeps
	// the tool off the agent MCP surface and accepts calls only through the
	// platform's authenticated app-to-app bridge.
	Exposure   ToolExposure
	Handler    ToolHandler
	HandlerCtx ToolHandlerCtx
}

// ToolExposure separates the agent-facing MCP catalog from private app-to-app
// capabilities without introducing a second RPC mechanism.
type ToolExposure string

const (
	ToolExposurePublic  ToolExposure = "public"
	ToolExposureAppOnly ToolExposure = "app_only"
)

// ToolHandler is the per-call handler. ctx is the app context (DB,
// platform client, logger). The map is the raw arguments; return the
// MCP result as a JSON-encodable value.
type ToolHandler func(ctx *AppCtx, args map[string]any) (any, error)

// ToolHandlerCtx is the per-call handler with a context.Context that
// carries the Caller — the calling agent's instance id + grants.
// Pull the Caller via sdk.CallerFrom(ctx). When the framework can't
// determine a Caller (header missing, older platform), CallerFrom
// returns nil and Caller methods treat that as full access.
type ToolHandlerCtx func(ctx context.Context, app *AppCtx, args map[string]any) (any, error)

// Worker — long-lived goroutine the framework supervises. Schedule is
// declarative ("@every 5m" / cron) for periodic workers; leave empty
// for one-shot run-to-completion workers.
type Worker struct {
	Name     string
	Schedule string
	Run      func(ctx context.Context, app *AppCtx) error
}

// EventHandler — subscription to a platform event. Platform pushes events to
// /apps/<name>/events; framework dispatches to handlers. Returning an error
// produces a non-2xx response so durable platform deliveries can be retried.
// Handlers receiving an Event with DeliveryID must deduplicate it before
// applying business mutations and return nil for an already-processed ID.
type EventHandler struct {
	Topic   string // e.g. "instance.message", "connection.created"; kept for compatibility
	Event   string // preferred event name; falls back to Topic when empty
	Handler func(ctx *AppCtx, event Event) error
}

type Event struct {
	// DeliveryID is a stable platform delivery identifier. When present, event
	// handlers must process it idempotently because the platform may retry after
	// a timeout or lost response. Agent lifecycle events use Core's transition ID.
	DeliveryID      string         `json:"delivery_id,omitempty"`
	Event           string         `json:"event,omitempty"`
	Topic           string         `json:"topic,omitempty"` // compatibility alias for Event
	SourceApp       string         `json:"source_app,omitempty"`
	SourceInstallID int64          `json:"source_install_id,omitempty"`
	InstanceID      int64          `json:"instance_id,omitempty"`
	ProjectID       string         `json:"project_id,omitempty"`
	Data            map[string]any `json:"data,omitempty"`
}

func (e Event) Name() string {
	if e.Event != "" {
		return e.Event
	}
	return e.Topic
}

// ChannelFactory builds an inbound/outbound channel adapter for one
// install. The platform calls Build() with the install's config to
// obtain a Channel ready to receive/send. Channels are advanced —
// most apps won't use this.
type ChannelFactory interface {
	Name() string
	Build(ctx *AppCtx, config map[string]string) (Channel, error)
}

// Channel — symmetric duplex adapter. Send pushes outbound; the
// framework calls Receive when an inbound message arrives via the
// app's webhook route.
type Channel interface {
	Send(ctx context.Context, msg ChannelMessage) error
	Receive(ctx context.Context, msg ChannelMessage) error
}

type ChannelMessage struct {
	From string         `json:"from"`
	Text string         `json:"text"`
	Meta map[string]any `json:"meta,omitempty"`
}

// AppCtx is the only handle into the platform an app gets. Apps never
// import apteva-server directly — they call into AppCtx and the
// framework round-trips to the platform over HTTP using the app's
// short-lived APTEVA_APP_TOKEN.
//
// Project context. AppCtx carries an optional "current project" — the
// project the dispatch is running for. For project-scoped installs the
// SDK seeds it from APTEVA_PROJECT_ID at boot, so CurrentProject()
// returns that value across every worker tick / event handler / tool
// call. For global installs APTEVA_PROJECT_ID is empty; the SDK's
// worker dispatcher derives one AppCtx per project per tick via
// WithProject, so the worker body sees the project it's currently
// operating on. App code should always prefer ctx.CurrentProject()
// over reading the env directly — that's the single hook that makes
// a global install behave correctly.
type AppCtx struct {
	startupContext context.Context
	startup        *startupStatus
	manifest       *Manifest
	cfg            Config
	db             *sql.DB
	readDB         *sql.DB
	dbState        *appDBState
	platform       PlatformClient
	logger         Logger
	cancel         <-chan struct{}
	emitter        Emitter
	currentProject string // "" until WithProject is called or env seeds it
}

type appDBState struct {
	generation atomic.Uint64
}

// AppDB is the app's private database handle, opened by the framework
// after migrations ran. Always non-nil if the manifest declares a db
// block; otherwise nil — the app should null-check.
func (c *AppCtx) AppDB() *sql.DB { return c.db }

// AppReadDB returns the app's read-only database pool. Production SQLite
// apps get a separate pool so reads can run concurrently without waiting for
// AppDB's serialized writer connection. Hand-built test contexts and apps
// without a separate read pool fall back to AppDB for compatibility.
func (c *AppCtx) AppReadDB() *sql.DB {
	if c == nil {
		return nil
	}
	if c.readDB != nil {
		return c.readDB
	}
	return c.db
}

// AppDBGeneration changes whenever the SDK detects that the database file
// was replaced underneath the sidecar. Apps that cache database-derived
// metadata can compare this value and invalidate stale entries after a live
// restore. Zero means no managed database lifecycle is available.
func (c *AppCtx) AppDBGeneration() uint64 {
	if c == nil || c.dbState == nil {
		return 0
	}
	return c.dbState.generation.Load()
}

// PlatformAPI returns a typed client for the small set of platform
// operations apps may legitimately need (read connection, send to
// channel, list instances). Every call is permission-checked server-
// side against the manifest's declared permissions.
func (c *AppCtx) PlatformAPI() PlatformClient { return c.platform }

// ThreadAPI returns the optional generic thread-addressing client. It stays
// separate from PlatformClient so extending thread lifecycle operations does
// not force every existing app test double to change.
func (c *AppCtx) ThreadAPI() ThreadClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		if _, available := scoped.inner.(ThreadClient); !available {
			return nil
		}
	}
	client, _ := c.platform.(ThreadClient)
	return client
}

// AgentEventsAPI returns the optional durable tracked-agent-event capability.
// It stays separate from PlatformClient so existing app test doubles do not
// need to implement it. The default HTTP-backed client supports it.
func (c *AppCtx) AgentEventsAPI() AgentEventClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		agentEvents, _ := scoped.inner.(AgentEventClient)
		return agentEvents
	}
	agentEvents, _ := c.platform.(AgentEventClient)
	return agentEvents
}

// RuntimeAPI returns the optional isolated-runtime control client. It is kept
// separate from PlatformClient because runtime orchestration is a privileged,
// specialist surface and adding it to PlatformClient would break every app
// test stub whenever the runtime contract grows.
//
// The default HTTP-backed AppCtx always provides this client. Tests or custom
// contexts backed by a PlatformClient stub may return nil.
func (c *AppCtx) RuntimeAPI() RuntimeClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		if _, available := scoped.inner.(RuntimeClient); !available {
			return nil
		}
	}
	runtimeAPI, _ := c.platform.(RuntimeClient)
	return runtimeAPI
}

// AgentToolsAPI returns the optional capability for attaching the calling
// app's MCP tools, and selected declared app dependencies, to an agent. It is
// separate from PlatformClient so existing app test doubles remain source
// compatible. Authorization and target scoping are enforced by the server.
func (c *AppCtx) AgentToolsAPI() AgentToolsClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		agentTools, _ := scoped.inner.(AgentToolsClient)
		return agentTools
	}
	agentTools, _ := c.platform.(AgentToolsClient)
	return agentTools
}

// PlatformBackupAPI returns the optional full-platform backup capability.
// Keeping this privileged streaming surface separate from PlatformClient
// avoids breaking every app test double when backup operations evolve.
func (c *AppCtx) PlatformBackupAPI() PlatformBackupClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		backupAPI, _ := scoped.inner.(PlatformBackupClient)
		return backupAPI
	}
	backupAPI, _ := c.platform.(PlatformBackupClient)
	return backupAPI
}

// BrowserOriginsAPI returns the optional server-managed browser-origin
// capability. It is separate from PlatformClient so adding this feature does
// not break existing apps or test doubles that implement PlatformClient.
//
// The default HTTP-backed AppCtx provides it when connected to a server that
// exposes /api/apps/callback/cors-origins. Older/custom clients return nil.
func (c *AppCtx) BrowserOriginsAPI() BrowserOriginClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		originAPI, _ := scoped.inner.(BrowserOriginClient)
		return originAPI
	}
	originAPI, _ := c.platform.(BrowserOriginClient)
	return originAPI
}

// BrowserOriginPolicyAPI returns the optional policy-aware browser-origin
// capability. It complements BrowserOriginsAPI without expanding
// PlatformClient or BrowserOriginClient, preserving compatibility with apps
// and test doubles compiled against the origin-only API.
func (c *AppCtx) BrowserOriginPolicyAPI() BrowserOriginPolicyClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		policyAPI, _ := scoped.inner.(BrowserOriginPolicyClient)
		return policyAPI
	}
	policyAPI, _ := c.platform.(BrowserOriginPolicyClient)
	return policyAPI
}

// ProjectTemplatesAPI returns the optional read-only project-template
// capability. WithProject scopes the returned client so an app cannot
// accidentally request a sibling project's templates.
func (c *AppCtx) ProjectTemplatesAPI() ProjectTemplateClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		inner, available := scoped.inner.(ProjectTemplateClient)
		if !available {
			return nil
		}
		return &projectScopedTemplateClient{inner: inner, projectID: scoped.projectID}
	}
	templates, _ := c.platform.(ProjectTemplateClient)
	return templates
}

// ReplaceBrowserOrigins atomically replaces the exact browser origins owned by
// registrationKey. Apps should use a stable key such as an OAuth client id and
// call this after creating or updating that client. Passing an empty slice
// removes the registration.
func (c *AppCtx) ReplaceBrowserOrigins(registrationKey string, origins []string) (*BrowserOriginRegistration, error) {
	api := c.BrowserOriginsAPI()
	if api == nil {
		return nil, errors.New("platform does not support browser-origin registration")
	}
	return api.ReplaceBrowserOrigins(registrationKey, origins)
}

// ReplaceBrowserOriginPolicy atomically replaces an app-owned origin set and
// its preflight policy. Use BrowserPreflightApp when the sidecar must decide
// route-specific methods or headers; BrowserPreflightPlatform is the safe
// default for ordinary public clients. Credentials is enforced by the
// platform, including as an upper bound on delegated app responses.
func (c *AppCtx) ReplaceBrowserOriginPolicy(registrationKey string, policy BrowserOriginPolicy) (*BrowserOriginRegistration, error) {
	api := c.BrowserOriginPolicyAPI()
	if api == nil {
		return nil, errors.New("platform does not support browser-origin policies")
	}
	return api.ReplaceBrowserOriginPolicy(registrationKey, policy)
}

// DeleteBrowserOrigins removes every origin owned by registrationKey. It is
// idempotent and should be called when the corresponding app client is disabled
// or deleted.
func (c *AppCtx) DeleteBrowserOrigins(registrationKey string) error {
	api := c.BrowserOriginsAPI()
	if api == nil {
		return errors.New("platform does not support browser-origin registration")
	}
	return api.DeleteBrowserOrigins(registrationKey)
}

// ListBrowserOriginRegistrations returns the complete install-scoped registry.
// Apps can use this during OnMount to reconcile client records created before
// the platform supported live origin registration.
func (c *AppCtx) ListBrowserOriginRegistrations() ([]BrowserOriginRegistration, error) {
	api := c.BrowserOriginsAPI()
	if api == nil {
		return nil, errors.New("platform does not support browser-origin registration")
	}
	return api.ListBrowserOriginRegistrations()
}

// PlatformInfo is the shortcut for c.PlatformAPI().PlatformInfo() —
// the same convenience pattern as ctx.GetInstance / WhoAmI. Returns
// the current platform-level facts (public_url, version) with the
// HTTP impl's brief in-memory cache, so apps can call this freely
// from a hot loop without DOSing apteva-server. Use this anywhere
// a sidecar previously read os.Getenv("APTEVA_PUBLIC_URL") — env
// is captured at spawn time and goes stale; this is hot-refreshed.
func (c *AppCtx) PlatformInfo() (*PlatformInfo, error) {
	if c == nil || c.platform == nil {
		return nil, errors.New("AppCtx has no platform client (test stub?)")
	}
	return c.platform.PlatformInfo()
}

// Manifest is the parsed apteva.yaml the app shipped — readable for
// runtime introspection (e.g. emitting a custom /version page).
func (c *AppCtx) Manifest() *Manifest { return c.manifest }

// Config is the user's filled-in install configuration (the
// config_schema fields). Decrypted by the platform before the app
// sees it.
func (c *AppCtx) Config() Config { return c.cfg }

// Logger is a structured logger pre-tagged with the app's name and
// install id.
func (c *AppCtx) Logger() Logger { return c.logger }

// DataDir returns the writable per-install directory the platform
// reserved for this app's persistent files (anything beyond AppDB —
// uploaded blobs, cloned repos, generated artifacts, …). Apps should
// route their on-disk state through this instead of hardcoding paths
// like "/data/foo": that works in container deployments where /data is
// a volume, but breaks every other host (macOS dev laptops, custom
// installer paths, anyone running apteva-server outside Docker).
//
// Resolution order:
//
//  1. APTEVA_DATA_DIR — preferred; the platform sets it to
//     <persistentRoot>/<install_id>/, the same dir the SDK opens
//     AppDB in.
//  2. dirname(DB_PATH) — graceful fallback for older platforms that
//     ship before APTEVA_DATA_DIR was introduced. Same physical
//     directory, just derived after the fact.
//  3. "" — neither is set; the manifest probably has no db block and
//     the platform spawned without setting APTEVA_DATA_DIR. Apps
//     needing a writable dir should treat this as a hard error and
//     refuse to mount, rather than silently picking a default that
//     won't exist.
func (c *AppCtx) DataDir() string {
	if v := os.Getenv("APTEVA_DATA_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		return filepath.Dir(v)
	}
	return ""
}

// Done returns a channel that closes when the platform asks the
// sidecar to shut down. Long-running workers should select on it.
func (c *AppCtx) Done() <-chan struct{} { return c.cancel }

// CurrentProject returns the project the current dispatch is running
// for. Resolution order:
//
//  1. The value set via WithProject (worker tick, event handler, tool
//     call with _project_id resolved by the framework)
//  2. APTEVA_PROJECT_ID env (project-scoped installs)
//  3. "" — global install with no per-call project context
//
// App code should call this in place of os.Getenv("APTEVA_PROJECT_ID")
// — it's the single API that makes global installs behave correctly.
// A returned "" means "the framework didn't dispatch this call against
// any project"; for global apps that's a bug to flag, not silently
// no-op.
func (c *AppCtx) CurrentProject() string {
	if c == nil {
		return ""
	}
	if c.currentProject != "" {
		return c.currentProject
	}
	return os.Getenv("APTEVA_PROJECT_ID")
}

// WithProject returns an AppCtx whose CurrentProject() returns the
// given project id, leaving every other field shared with the parent.
// Use it to pin a project on cross-call boundaries (e.g. the SDK's
// worker dispatcher does this once per project per tick). The
// PlatformClient handle is wrapped so subsequent CallApp / CallAppResult
// invocations auto-thread `_project_id` into the downstream args and ordinary
// thread creation defaults to the same project — global apps' workers don't
// have to thread project context manually.
//
// Passing "" reverts to env-driven behaviour.
func (c *AppCtx) WithProject(projectID string) *AppCtx {
	if c == nil {
		return nil
	}
	cp := *c
	cp.currentProject = projectID
	cp.platform = wrapPlatformWithProject(c.platform, projectID)
	return &cp
}

// WithUserSession returns a request-scoped context whose platform callbacks
// retain the signed-in browser user's session as well as this app's identity.
// The server validates both credentials and still enforces app permissions.
// Use only for work on behalf of an incoming HTTP request; never retain it as
// the mounted context or use it for independent background work. Without a
// session cookie the app's existing service identity is preserved.
func (c *AppCtx) WithUserSession(r *http.Request) *AppCtx {
	if c == nil || r == nil {
		return c
	}
	cookie, err := r.Cookie("session")
	if err != nil || cookie.Value == "" {
		return c
	}
	cp := *c
	cp.platform = platformWithUserSession(c.platform, cookie.Value)
	return &cp
}

// IntegrationFor returns the binding for a role declared in the
// manifest's requires.integrations. Returns nil when:
//
//   - the role isn't declared in the manifest (caller bug)
//   - the role is declared but the operator didn't bind it (optional
//     dep skipped, or required dep install in flight)
//   - the bound target was uninstalled (status=degraded)
//
// Apps should null-check on every call and degrade gracefully when
// nil — that's the whole point of optional deps. Required deps
// being nil means the install is misconfigured; surface a clean
// error to the agent rather than crashing.
//
// The lookup hits the platform's /whoami once per AppCtx and caches
// the bindings for the process lifetime. Since bindings can change
// at runtime (operator rebinds, optional dep newly available), the
// cache TTL is short — sub-second — so the next call after a
// rebind picks up the change without a sidecar restart.
func (c *AppCtx) IntegrationFor(role string) *BoundIntegration {
	bounds := c.IntegrationsFor(role)
	if len(bounds) == 0 {
		return nil
	}
	for _, b := range bounds {
		if b != nil && b.IsDefault {
			return b
		}
	}
	return bounds[0]
}

// IntegrationsFor returns every binding for a role declared in the
// manifest's requires.integrations. Single-binding roles return a
// one-element slice. Multi-binding roles return the operator-selected
// targets in binding order; the default target, when present, is marked
// with IsDefault and is what IntegrationFor returns.
func (c *AppCtx) IntegrationsFor(role string) []*BoundIntegration {
	if c == nil || c.platform == nil {
		return nil
	}
	bindings, manifest := c.bindingsAndManifest()
	if manifest == nil {
		return nil
	}
	var dep *IntegrationDep
	for i := range manifest.Requires.Integrations {
		if manifest.Requires.Integrations[i].Role == role {
			dep = &manifest.Requires.Integrations[i]
			break
		}
	}
	if dep == nil {
		return nil
	}
	raw, ok := bindings[role]
	if !ok || raw == nil {
		return nil
	}
	ids, defaultID := bindingIDs(raw)
	if len(ids) == 0 {
		return nil
	}
	out := make([]*BoundIntegration, 0, len(ids))
	for _, id := range ids {
		bound := c.boundIntegrationForID(dep, role, id)
		if bound == nil {
			continue
		}
		bound.IsDefault = id == defaultID
		out = append(out, bound)
	}
	if len(out) > 0 && defaultID == 0 {
		out[0].IsDefault = true
	}
	return out
}

func (c *AppCtx) boundIntegrationForID(dep *IntegrationDep, role string, id int64) *BoundIntegration {
	if id <= 0 {
		return nil
	}
	bound := &BoundIntegration{
		Role: role,
		Kind: dep.Kind,
	}
	if bound.Kind == "" {
		bound.Kind = "integration"
	}
	if bound.Kind == "app" {
		bound.InstallID = id
		// AppName resolution: best-effort GetInstance lookup.
		// Falls through with empty AppName when the platform
		// doesn't have a fast path; consumers fall back to the
		// install id, which is what authorization gates on anyway.
		if inst, err := c.platform.GetInstance(id); err == nil && inst != nil {
			bound.AppName = inst.Name
		}
	} else {
		bound.ConnectionID = id
		// Resolve the connection's app_slug so app code can do
		// provider-specific normalization without a separate
		// GetConnection round-trip. This is one /api/apps/callback/
		// connections/:id call per IntegrationFor invocation;
		// ToolFor uses it to map logical capabilities to upstream
		// tool names. Best-effort — leave AppSlug empty on error
		// and let app code fall back to its own defaults.
		if conn, err := c.platform.GetConnection(id); err == nil && conn != nil {
			bound.AppSlug = conn.AppSlug
		}
	}
	// Build ToolFor closure that maps logical capability → upstream tool name
	// per the manifest's tools map.
	tools := dep.Tools
	bound.ToolFor = func(capability string) string {
		if tools == nil {
			return capability
		}
		if t, ok := tools[capability]; ok {
			return t
		}
		return capability
	}
	return bound
}

func bindingIDs(raw any) ([]int64, int64) {
	if id, ok := bindingInt64(raw); ok && id > 0 {
		return []int64{id}, id
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, 0
	}
	ids := make([]int64, 0)
	seen := map[int64]bool{}
	switch v := m["ids"].(type) {
	case []any:
		for _, item := range v {
			if id, ok := bindingInt64(item); ok && id > 0 && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	case []int64:
		for _, id := range v {
			if id > 0 && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	case []float64:
		for _, id := range v {
			n := int64(id)
			if n > 0 && !seen[n] {
				ids = append(ids, n)
				seen[n] = true
			}
		}
	}
	defaultID := int64(0)
	if id, ok := bindingInt64(m["default_id"]); ok && seen[id] {
		defaultID = id
	}
	return ids, defaultID
}

func bindingInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n > 0
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// BoundIntegration describes a role's currently-bound target. See
// AppCtx.IntegrationFor.
type BoundIntegration struct {
	Role         string
	Kind         string // "integration" | "app"
	ConnectionID int64  // when Kind=integration
	InstallID    int64  // when Kind=app
	AppName      string // when Kind=app — resolved best-effort
	AppSlug      string // when Kind=integration — fetched on demand from PlatformAPI.GetConnection
	IsDefault    bool   // true when this is the default binding for a multi role
	// ToolFor maps a logical capability ("image.generate") to the
	// upstream tool name ("generate_image" for openai-api). Falls
	// back to the capability string when no mapping is declared.
	ToolFor func(capability string) string
}

// bindingsAndManifest fetches the install's bindings + manifest. The
// httpPlatformClient caches both for the AppCtx's lifetime; tests
// can stub via a custom PlatformClient.
func (c *AppCtx) bindingsAndManifest() (map[string]any, *Manifest) {
	if c.manifest == nil || c.platform == nil {
		return nil, c.manifest
	}
	// httpPlatformClient.WhoAmI returns identity + bindings. Pull
	// fresh on every call — the cache + revalidation lives in the
	// platform client itself, not here.
	id, err := c.platform.WhoAmI()
	if err != nil || id == nil {
		return nil, c.manifest
	}
	return id.Bindings, c.manifest
}

// GetAgent fetches the platform agent (formerly "instance") identified
// by id, going through the SDK's PlatformClient. This is a thin
// convenience that delegates to PlatformAPI().GetInstance — adding it
// to AppCtx instead of the PlatformClient interface keeps every
// PlatformClient stub in app test suites valid without forcing a
// method-add migration. New app code should prefer ctx.GetAgent(id)
// over the older ctx.PlatformAPI().GetInstance(id); the latter stays
// available until Phase 4.
func (c *AppCtx) GetAgent(id int64) (*PlatformAgent, error) {
	if c == nil || c.platform == nil {
		return nil, errors.New("no platform client")
	}
	return c.platform.GetInstance(id)
}

// Emit publishes an event onto the platform's app-event bus. Topic is
// app-relative (e.g. "file.added") — the platform stamps the app
// prefix from the install token before fanning out. Data must be
// JSON-encodable; pass nil if the topic alone is enough signal.
//
// The event is tagged with c.CurrentProject() as its project_id —
// so emits from a per-request-scoped ctx (tool calls, event handlers,
// worker ticks) automatically carry the right project even when the
// install is global. Apps that need to emit on behalf of a different
// project (e.g. storage processes a file in project X from a global
// install's HTTP handler) should use EmitWithProject directly.
//
// Fire-and-forget: a 100ms timeout caps the publish, errors are
// logged but never bubbled to the caller. UI fanout is best-effort
// and the app's own DB is the source of truth — a missed event is a
// reconnect-with-since= away from being recovered by the dashboard.
//
// Safe to call from any goroutine. Safe to call when no subscribers
// are connected (the platform's ring buffer holds the last 256
// events per (app, project) for fast reconnect replay).
func (c *AppCtx) Emit(topic string, data any) {
	if c == nil || c.emitter == nil {
		return
	}
	c.emitter.EmitWithProject(topic, c.CurrentProject(), data)
}

// EmitWithProject publishes an event tagged with an explicit
// project_id, ignoring c.CurrentProject(). Use this when the project
// the event belongs to is a property of the *data*, not the dispatch
// context — e.g. storage's file event handlers run from an
// unscoped ctx (HTTP route) but the file's project_id is on the row.
//
// For project-scoped installs the platform enforces the install's
// pinned project on the server side, so a project_id argument that
// disagrees is silently overridden (you can't spoof outside your
// scope). Global installs may emit for any project the install's
// owner has access to.
//
// Passing projectID="" emits a project-less event — lands on the
// wildcard lane only. Use sparingly; most domain events belong to
// a project.
func (c *AppCtx) EmitWithProject(topic, projectID string, data any) {
	if c == nil || c.emitter == nil {
		return
	}
	c.emitter.EmitWithProject(topic, projectID, data)
}

// Emitter is the indirection between AppCtx and the HTTP-based emit
// implementation in run.go. Exposed so app-sdk/testkit can stub it
// with an in-memory recorder for assertions.
//
// EmitWithProject carries an explicit project_id on the wire. The
// HTTP transport encodes it into the emit body so the platform
// stamps the (app, project) lane correctly even when the install is
// global. Passing projectID="" yields a project-less event.
type Emitter interface {
	EmitWithProject(topic, projectID string, data any)
}

// SetEmitter swaps the AppCtx's event emitter — only valid for
// test-built contexts (NewAppCtxForTest). Production code never
// needs this; the framework wires the HTTP emitter automatically.
// Unexported field access through this method keeps the testkit
// from reaching into unexported state directly.
func (c *AppCtx) SetEmitter(e Emitter) {
	if c == nil {
		return
	}
	c.emitter = e
}

// NewAppCtxForTest constructs an *AppCtx for use by the testkit
// package and its callers. Production code never needs this — the
// framework builds AppCtx in Run(). Exposed so app-sdk/testkit can
// hand-craft a context backed by an in-memory database, with no
// platform client and a no-op logger.
//
// Pass nil for any of: manifest (gets a zero-value pointer),
// platform (tests that don't call platform), logger (uses the
// silent default). cfg may be nil — becomes empty Config.
func NewAppCtxForTest(manifest *Manifest, db *sql.DB, cfg Config, platform PlatformClient, logger Logger) *AppCtx {
	if manifest == nil {
		manifest = &Manifest{}
	}
	if cfg == nil {
		cfg = Config{}
	}
	if logger == nil {
		logger = silentLogger{}
	}
	state := &appDBState{}
	state.generation.Store(1)
	return &AppCtx{
		manifest: manifest,
		cfg:      cfg,
		db:       db,
		readDB:   db,
		dbState:  state,
		platform: platform,
		logger:   logger,
		cancel:   make(chan struct{}),
	}
}

// SetAppReadDBForTest replaces a test context's read pool and returns a
// function that simulates database replacement by advancing its generation.
// Production apps must not call this; the SDK owns both handles in Run.
func (c *AppCtx) SetAppReadDBForTest(readDB *sql.DB) func() {
	if c == nil {
		return func() {}
	}
	c.readDB = readDB
	if c.dbState == nil {
		c.dbState = &appDBState{}
		c.dbState.generation.Store(1)
	} else {
		c.dbState.generation.Add(1)
	}
	return func() { c.dbState.generation.Add(1) }
}

// silentLogger drops every message — used as the test default so
// `go test -v` output isn't drowned in app boot logs.
type silentLogger struct{}

func (silentLogger) Info(string, ...any)  {}
func (silentLogger) Warn(string, ...any)  {}
func (silentLogger) Error(string, ...any) {}

// Config — typed access to user-supplied install configuration.
type Config map[string]string

func (c Config) Get(name string) string { return c[name] }
func (c Config) Has(name string) bool   { _, ok := c[name]; return ok }

// Logger — minimal structured logging interface so apps don't have to
// import a logger package. Framework provides the default impl.
type Logger interface {
	Info(msg string, keyvals ...any)
	Warn(msg string, keyvals ...any)
	Error(msg string, keyvals ...any)
}

// PlatformClient — the small RPC surface apps may use to interact
// with the platform. Each method is permission-gated; calling without
// the matching declared permission returns an error 403.
type PlatformClient interface {
	// Connections
	GetConnection(id int64) (*PlatformConnection, error)
	ListConnections(filter ConnectionFilter) ([]PlatformConnection, error)

	// Agents — GetAgent is the canonical post-rename name. GetInstance
	// stays as an alias so apps + test stubs that haven't migrated
	// keep compiling; both return the same struct (PlatformAgent is a
	// type alias for PlatformInstance).
	GetInstance(id int64) (*PlatformInstance, error)
	GetAgent(id int64) (*PlatformAgent, error)
	SendEvent(instanceID int64, message string) error

	// Channels
	SendToChannel(channelName, projectID, message string) error

	// Self
	WhoAmI() (*InstallIdentity, error)

	// Integration execution. Calls a tool on a connection bound to
	// this install at install time. Credentials never leave the
	// platform; the response shape mirrors /connections/:id/execute.
	// Authorization: the install must declare
	// platform.connections.execute and connID must be in the
	// install's integration_bindings.
	ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*ExecuteResult, error)

	// App-to-app call. Invokes an MCP tool on a sibling app the
	// install was bound to. Authorization: the install must declare
	// platform.apps.call and appName must be in a kind=app binding.
	//
	// Returns the raw JSON-RPC envelope from the target sidecar's
	// /mcp endpoint:
	//
	//   {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text",
	//    "text":"<tool's actual JSON return>"}]}}
	//
	// Callers that just want the tool's return shape should prefer
	// CallAppResult (added in v0.1.8) which unwraps that envelope
	// for them. CallApp stays for callers that already unwrap
	// themselves and for non-tool /mcp methods (resources/list, etc.).
	CallApp(appName, tool string, input map[string]any) (json.RawMessage, error)

	// CallAppResult is CallApp + automatic JSON-RPC envelope unwrap.
	// Decodes result.content[0].text directly into out so callers
	// can use the tool's natural shape:
	//
	//   var got struct{ Files []File `json:"files"` }
	//   if err := api.CallAppResult("storage", "files_list", args, &got); err != nil {
	//     return err
	//   }
	//   // got.Files is the tool's `files` array.
	//
	// Falls back to decoding the raw response when the envelope is
	// missing — covers the "the platform short-circuited an
	// unwrapped response" path some test/mocked clients use.
	CallAppResult(appName, tool string, input map[string]any, out any) error

	// App-initiated OAuth. Creates a pending connection owned by this
	// install (created_via=app_install, owner_app_install_id=<this>),
	// returns the authorize URL the app should hand the user, and the
	// connection id to poll. After the human completes the dance,
	// apteva-server 302s the browser back to ReturnURL with
	// ?conn_id=<id>&status=ok. Authorization: install must declare
	// platform.oauth.start.
	StartOAuth(req OAuthStartRequest) (*OAuthStartResult, error)

	// Disconnect a connection this install owns. Authorization: install
	// must declare platform.connections.manage AND the connection must
	// have owner_app_install_id matching this install (apps cannot
	// touch operator-managed connections or other apps' connections).
	DisconnectConnection(connID int64) error

	// List connections owned by this install. Returns only rows where
	// owner_app_install_id matches; never operator rows. Authorization:
	// install must declare platform.connections.read or .manage.
	ListOwnedConnections() ([]PlatformConnection, error)

	// GetGrants returns the per-(this install, instanceID) policy the
	// operator wrote for the calling agent. Used by the SDK's MCP
	// handler to gate tool calls. Returns a zero-value response (no
	// rules, default allow) when the platform doesn't yet implement
	// the endpoint — back-compat with older servers.
	GetGrants(instanceID int64) (*GrantsResponse, error)

	// GetConnectionCredentials returns the decrypted credentials for a
	// bound connection. Use this when the integration runner's
	// tool-call surface isn't expressive enough — multipart uploads,
	// presigned URLs, range GETs against an S3-compatible backend, etc.
	//
	// Requirements (all enforced server-side):
	//   1. Manifest declares platform.connections.read_credentials.
	//   2. The connection's slug is in some requires.integrations[].
	//      compatible_slugs entry on this install's manifest.
	//   3. The connection ID is in the install's integration_bindings
	//      for one of those roles (operator actually bound it).
	GetConnectionCredentials(id int64) (*ConnectionCredentials, error)

	// EnsureIntegrationWebhook registers or reconciles an external webhook
	// for a connection bound to this install. The platform constructs the
	// public callback URL, calls the provider, and stores the provider's
	// signing secret encrypted. Provider credentials and signing secrets
	// are never returned to the app.
	EnsureIntegrationWebhook(req IntegrationWebhookEnsureRequest) (*IntegrationWebhookStatus, error)

	// VerifyIntegrationWebhook verifies a raw provider payload against the
	// encrypted signing secret owned by the platform. Event contains the
	// unchanged, verified provider event JSON.
	VerifyIntegrationWebhook(req IntegrationWebhookVerifyRequest) (*IntegrationWebhookVerifyResult, error)

	// ListProjects returns the projects this install can dispatch
	// against. For project-scoped installs that's a singleton list
	// holding the install's pinned project. For global installs it's
	// every project the install's owner has access to. The SDK's
	// worker loop calls this once per tick to fan workers out per
	// project; apps that hand-roll dispatch (custom polling, custom
	// SSE subscriptions) can call it too.
	//
	// Authorization: no manifest permission required — every install
	// is allowed to enumerate its own projection. The list is filtered
	// server-side by ownership.
	ListProjects() ([]PlatformProject, error)

	// SpawnRealtimeThread creates a realtime (voice/audio) sub-thread
	// in core and returns an audio bridge URL the app can dial to
	// pipe PCM frames in both directions. Realtime threads are
	// first-class apteva sub-threads — they have a directive, tools,
	// MCPs, and participate in send/done with main exactly like any
	// other worker — but their conversation runs over a bidirectional
	// WebSocket session with the model instead of discrete Chat()
	// calls.
	//
	// Authorization: install must declare platform.realtime.spawn.
	// The Config.RealtimeEnabled master switch must also be on; when
	// off the call returns a 403 with a clear "feature disabled"
	// message that the caller should surface verbatim to the agent.
	//
	// The returned public WebSocket URL already includes a single-use,
	// thread-scoped token. Dial it as-is; apps do not need the core API
	// key and must not replace its host with loopback. Tokens expire
	// after 5 minutes if unused.
	SpawnRealtimeThread(req RealtimeSpawnRequest) (*RealtimeSpawnResult, error)

	// RenewRealtimeAudioBridge rotates the single-use audio capability for an
	// existing realtime thread without replacing its provider session.
	RenewRealtimeAudioBridge(agentID int64, threadID string) (*RealtimeSpawnResult, error)

	// KillThread terminates a thread inside an agent. Used by apps that own
	// the lifecycle of a sub-thread (telephony app ending a call,
	// channelchat ending a chat session). Returns nil if the thread
	// doesn't exist — idempotent. Authorization: install must declare
	// platform.realtime.spawn (the kill capability is implicit in
	// the spawn capability — you can only kill threads you can spawn).
	KillThread(agentID int64, threadID string) error

	// PlatformInfo returns the small bag of platform-level facts the
	// apteva-server is willing to share with sidecars — currently
	// just public_url. Sidecars used to read this via the
	// APTEVA_PUBLIC_URL env var, but env is captured at spawn time:
	// operators changing public_url in settings had to restart every
	// sidecar to pick it up. This call hits the platform on a brief
	// in-memory cache (60s by default in the HTTP impl) so apps see
	// updated values within a minute of an operator change.
	//
	// Authorization: no manifest permission required — public_url
	// is not sensitive (it's the URL operators publish for inbound
	// webhooks / external recipients to reach the platform).
	//
	// Use ctx.PlatformInfo() as the convenience shortcut.
	PlatformInfo() (*PlatformInfo, error)

	// Ingress
	//
	// Server-native ingress exposes public hostnames through
	// apteva-server's own host router and managed certificate flow.
	// Domains/DNS remain external: callers that need the platform to
	// create DNS records should call the Domains app first, then expose
	// the hostname here. Exact hostnames can be certified through
	// HTTP-01 when DNS already points at the Apteva server; wildcard
	// hostnames require future DNS-01 delegation.
	//
	// Authorization: ExposeIngress/UnexposeIngress require
	// platform.ingress.write. ListIngressRoutes requires
	// platform.ingress.read or .write and is scoped to routes owned by
	// this install. Route responses include server-native certificate
	// status, so apps never need to bind or query a certificate app.
	ExposeIngress(req IngressExposeRequest) (*IngressRoute, error)
	UnexposeIngress(hostname string) error
	ListIngressRoutes() ([]IngressRoute, error)

	// DNS
	//
	// Platform DNS is the delegated capability surface for apps that
	// need records without owning registrar/provider credentials. The
	// backing authority may be local Domains, Fleet, or a parent
	// hosting controller. Callers declare platform.dns.read/.write;
	// the server validates every write against available grants before
	// forwarding it.
	ListDomainGrants() ([]DomainGrant, error)
	UpsertDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error)
	DeleteDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error)

	// Environments
	//
	// These methods expose the platform's isolated test/backtest
	// environment surface to apps that are explicitly installed with
	// environment permissions. Environments are control-plane objects:
	// apps that merely run inside one should usually only read
	// CurrentEnvironmentID() and rely on the SDK's automatic
	// X-Apteva-Environment-Id forwarding. Apps that orchestrate
	// evaluations or backtests can create an environment, seed/call
	// tools inside it, spawn a cloned agent, inspect results, snapshot
	// state, then destroy it.
	//
	// Authorization: read methods require platform.environments.read
	// or .manage; SeedEnvironment/CallEnvironmentApp require
	// platform.environments.call or .manage; create/snapshot/agent
	// lifecycle/destroy require platform.environments.manage.
	ListEnvironments() ([]EnvironmentSummary, error)
	CreateEnvironment(req EnvironmentCreateRequest) (*EnvironmentSummary, error)
	GetEnvironment(id string) (*EnvironmentSummary, error)
	DestroyEnvironment(id string) error
	SeedEnvironment(id string, calls []EnvironmentSeedCall, seedBaseDir string) ([]json.RawMessage, error)
	CallEnvironmentApp(environmentID, appName, tool string, input map[string]any) (json.RawMessage, error)
	CallEnvironmentAppResult(environmentID, appName, tool string, input map[string]any, out any) error
	SnapshotEnvironment(environmentID string, req EnvironmentSnapshotRequest) (*EnvironmentSnapshot, error)
	ListEnvironmentAgents(environmentID string) ([]EnvironmentAgent, error)
	SpawnEnvironmentAgent(environmentID string, req EnvironmentAgentSpawnRequest) (*EnvironmentAgent, error)
	StopEnvironmentAgent(environmentID string, agentOrAlias string) error
}

type IntegrationURLPropertyStatus struct {
	Integration      string `json:"integration"`
	Property         string `json:"property"`
	Ready            bool   `json:"ready"`
	ConfiguredPrefix string `json:"configured_prefix"`
	Fingerprint      string `json:"fingerprint"`
	State            struct {
		HostingStatus                string `json:"hosting_status"`
		RelayStatus                  string `json:"relay_status"`
		OperatorConfirmedAt          string `json:"operator_confirmed_at"`
		LastSuccessfulProviderPullAt string `json:"last_successful_provider_pull_at"`
		LastTestAt                   string `json:"last_test_at"`
	} `json:"state"`
}

// IntegrationURLPropertyClient is an optional extension implemented by the
// SDK's HTTP client. Keeping it separate preserves compatibility with apps
// that provide their own PlatformClient test doubles.
type IntegrationURLPropertyClient interface {
	GetIntegrationURLProperty(connID int64, property string) (*IntegrationURLPropertyStatus, error)
}

// GetIntegrationURLProperty reads the optional provider URL-delivery
// readiness capability without forcing every PlatformClient implementation
// to grow a method. Older/custom clients return a clear unsupported error.
func GetIntegrationURLProperty(client PlatformClient, connID int64, property string) (*IntegrationURLPropertyStatus, error) {
	extended, ok := client.(IntegrationURLPropertyClient)
	if !ok {
		return nil, errors.New("server/SDK does not support integration URL properties")
	}
	return extended.GetIntegrationURLProperty(connID, property)
}

// AgentDirectoryClient is the optional agent-enumeration surface.
// Server authorization requires the platform.instances.read permission.
// projectID filters to one project; empty lists every agent the install
// may see (project-scoped installs are always pinned to their own
// project server-side regardless of the argument). Platform-owned
// helper agents are never included.
type AgentDirectoryClient interface {
	ListAgents(projectID string) ([]PlatformAgent, error)
}

// ListAgentsVia reads the optional agent-directory capability without
// forcing every PlatformClient implementation to grow a method.
// Older/custom clients return a clear unsupported error.
func ListAgentsVia(client PlatformClient, projectID string) ([]PlatformAgent, error) {
	directory, ok := client.(AgentDirectoryClient)
	if !ok {
		return nil, errors.New("server/SDK does not support agent listing")
	}
	return directory.ListAgents(projectID)
}

// ThreadClient is the generic, optional thread-addressing surface. Thread ids
// are opaque; applications own any creator/assignee/executor relationships.
type ThreadClient interface {
	SendThreadEvent(target ThreadRef, message any) error
	SpawnThread(req ThreadSpawnRequest) (*ThreadSpawnResult, error)
	KillThread(agentID int64, threadID string) error
}

// ThreadProfileClient is the optional reconciliation surface for apps that
// own long-lived threads. EnsureThread atomically reconciles the desired
// directive/tools/MCP profile and queues stable events. Older servers may
// return an unsupported error; callers should fall back to SpawnThread.
type ThreadProfileClient interface {
	EnsureThread(req ThreadEnsureRequest) (*ThreadEnsureResult, error)
}

// PlatformBackupClient is the privileged, streaming platform-backup surface.
// Server authorization requires a global, active, admin-owned installation
// with the matching approved permission for each operation.
type PlatformBackupClient interface {
	// OpenPlatformSnapshot streams a snapshot response. The caller must close
	// the returned reader. Cancellation of ctx aborts the transfer.
	OpenPlatformSnapshot(ctx context.Context) (io.ReadCloser, error)
	// RestorePlatformSnapshot streams a gzip snapshot into the platform. Pass
	// -1 when the content length is unknown and chunked transfer is required.
	RestorePlatformSnapshot(ctx context.Context, body io.Reader, size int64) (map[string]any, error)
}

// PlatformBackupRecoveryClient adds portable recovery without changing existing
// PlatformBackupClient implementations or application test doubles.
type PlatformBackupRecoveryClient interface {
	PlatformBackupClient
	OpenPlatformSnapshotWithPassphrase(context.Context, string) (io.ReadCloser, error)
	RestorePlatformSnapshotWithPassphrase(context.Context, io.Reader, int64, string) (map[string]any, error)
}

// BrowserOriginRegistration is one app-owned public-client origin set. Key is
// an opaque, stable identifier chosen by the app (typically an OAuth/API client
// id). Origins contain exact HTTP(S) origins; wildcard registrations are not
// supported by the platform.
type BrowserOriginRegistration struct {
	Key         string               `json:"key"`
	Origins     []string             `json:"origins"`
	Preflight   BrowserPreflightMode `json:"preflight"`
	Credentials bool                 `json:"credentials"`
}

// UnmarshalJSON preserves the behavior of servers released before browser
// origin policies: omitted policy fields mean platform-managed preflight with
// credentialed requests allowed.
func (r *BrowserOriginRegistration) UnmarshalJSON(data []byte) error {
	type wireRegistration struct {
		Key         string               `json:"key"`
		Origins     []string             `json:"origins"`
		Preflight   BrowserPreflightMode `json:"preflight"`
		Credentials *bool                `json:"credentials"`
	}
	var wire wireRegistration
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.Key = wire.Key
	r.Origins = wire.Origins
	r.Preflight = wire.Preflight
	if r.Preflight == "" {
		r.Preflight = BrowserPreflightPlatform
	}
	r.Credentials = true
	if wire.Credentials != nil {
		r.Credentials = *wire.Credentials
	}
	return nil
}

// BrowserPreflightMode selects who answers an authorized browser preflight.
type BrowserPreflightMode string

const (
	// BrowserPreflightPlatform lets Apteva answer OPTIONS generically.
	BrowserPreflightPlatform BrowserPreflightMode = "platform"
	// BrowserPreflightApp delegates OPTIONS to the app after Apteva verifies
	// that the request origin belongs to the registration.
	BrowserPreflightApp BrowserPreflightMode = "app"
)

// BrowserOriginPolicy is the complete desired policy for one app-owned
// browser client. Origins must be exact HTTP(S) origins; wildcards are not
// supported. An empty Preflight value defaults to BrowserPreflightPlatform.
type BrowserOriginPolicy struct {
	Origins     []string             `json:"origins"`
	Preflight   BrowserPreflightMode `json:"preflight"`
	Credentials bool                 `json:"credentials"`
}

// BrowserOriginClient is the optional live CORS registration surface exposed
// by current Apteva servers. The platform scopes every operation to the
// authenticated app install and only applies the origins to that app's proxy
// routes. It does not modify the operator's global CORS_ORIGIN configuration.
type BrowserOriginClient interface {
	ReplaceBrowserOrigins(registrationKey string, origins []string) (*BrowserOriginRegistration, error)
	DeleteBrowserOrigins(registrationKey string) error
	ListBrowserOriginRegistrations() ([]BrowserOriginRegistration, error)
}

// BrowserOriginPolicyClient is the optional extension for apps that need
// route-aware preflight handling or explicit credential control. Keeping it
// separate from BrowserOriginClient avoids breaking existing implementations.
type BrowserOriginPolicyClient interface {
	ReplaceBrowserOriginPolicy(registrationKey string, policy BrowserOriginPolicy) (*BrowserOriginRegistration, error)
}

// PlatformInfo is the small "what does the operator have configured at
// the platform level" bag. Returned from PlatformClient.PlatformInfo
// and ctx.PlatformInfo(). Add fields here when there's a new
// platform-level fact apps need to read at runtime — keep it small;
// per-app config_schema still owns per-app knobs.
type PlatformInfo struct {
	AsyncResultNotifications *AsyncResultCapabilities `json:"async_result_notifications,omitempty"`
	// FileReferences advertises the optional durable attachment contract.
	FileReferences        string          `json:"file_references,omitempty"`
	FileReferenceDispatch map[string]bool `json:"file_reference_dispatch,omitempty"`
	// PublicURL is the apteva-server's externally-reachable base URL.
	// Used by sidecars to construct webhooks, signed URLs handed to
	// third parties, etc. May be "" on local-only installs that
	// never configured a public hostname. Equivalent to (but
	// freshest-read of) the APTEVA_PUBLIC_URL env var.
	PublicURL string `json:"public_url"`
	// Version is the apteva-server's release version (e.g.
	// "0.17.5"). Useful for capability gates ("only call X when
	// platform >= 0.18"). Optional — older platforms may return "".
	Version string `json:"version,omitempty"`
}

// PlatformProject is the project descriptor PlatformClient.ListProjects
// returns. Apps that only fan out work need ID; apps that generate
// human-facing or LLM-facing output can use Name/Description as light
// context for the current project.
type PlatformProject struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// IngressExposeRequest is the body for PlatformClient.ExposeIngress.
// Target must be an absolute http://, https://, or app:// URL.
type IngressExposeRequest struct {
	// Mode selects ordinary HTTP ingress or an SNI-routed forward proxy.
	// forward_proxy requires an app-owned target and native TLS.
	Mode      string `json:"mode,omitempty"`
	Hostname  string `json:"hostname"`
	Target    string `json:"target"`
	ProjectID string `json:"project_id,omitempty"`
	OwnerKind string `json:"owner_kind,omitempty"`
	CertFQDN  string `json:"cert_fqdn,omitempty"`
	AllowHTTP bool   `json:"allow_http,omitempty"`
	TLSMode   string `json:"tls_mode,omitempty"` // "auto" (default) or "off"
	TLS       string `json:"tls,omitempty"`      // alias accepted by older callers
}

// IngressRoute is one server-native hostname exposure owned by an app
// install.
type IngressRoute struct {
	Mode           string                    `json:"mode,omitempty"`
	ID             int64                     `json:"id"`
	Hostname       string                    `json:"hostname"`
	Target         string                    `json:"target"`
	ProjectID      string                    `json:"project_id"`
	OwnerInstallID int64                     `json:"owner_install_id"`
	OwnerKind      string                    `json:"owner_kind"`
	CertFQDN       string                    `json:"cert_fqdn,omitempty"`
	AllowHTTP      bool                      `json:"allow_http"`
	TLSMode        string                    `json:"tls_mode"`
	Status         string                    `json:"status"`
	Certificate    *IngressCertificateStatus `json:"certificate,omitempty"`
	CreatedAt      string                    `json:"created_at"`
	UpdatedAt      string                    `json:"updated_at"`
}

// IngressCertificateStatus reports the server-native certificate state
// for an exposed route. "not_cached" means issuance will be triggered
// by the first TLS handshake; "live" includes the cached certificate's
// validity metadata.
type IngressCertificateStatus struct {
	FQDN      string `json:"fqdn"`
	Status    string `json:"status"`
	NotBefore string `json:"not_before,omitempty"`
	NotAfter  string `json:"not_after,omitempty"`
	Serial    string `json:"serial,omitempty"`
	Issuer    string `json:"issuer,omitempty"`
	Error     string `json:"error,omitempty"`
}

// DomainGrant is one DNS zone/prefix the platform is willing to
// manage on behalf of the calling app's instance.
type DomainGrant struct {
	ID          string   `json:"id,omitempty"`
	Domain      string   `json:"domain"`
	Wildcard    bool     `json:"wildcard"`
	Status      string   `json:"status,omitempty"`
	Source      string   `json:"source,omitempty"`
	Actions     []string `json:"actions,omitempty"`
	RecordTypes []string `json:"record_types,omitempty"`
}

// DNSRecordRequest is the generic platform-DNS record mutation shape.
// Name is relative to Domain; use "@" for the zone apex.
type DNSRecordRequest struct {
	ProjectID string `json:"project_id,omitempty"`
	Domain    string `json:"domain"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value,omitempty"`
	TTL       int    `json:"ttl,omitempty"`
}

type DNSRecordResult struct {
	OK      bool   `json:"ok"`
	Action  string `json:"action,omitempty"`
	Backend string `json:"backend,omitempty"`
	Domain  string `json:"domain,omitempty"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Error   string `json:"error,omitempty"`
}

// EnvironmentMode controls how the environment edge proxy handles
// outbound network calls that do not match an explicit allowlist/mock.
type EnvironmentMode string

const (
	EnvironmentModeBlock       EnvironmentMode = "block"
	EnvironmentModePassthrough EnvironmentMode = "passthrough"
	EnvironmentModeMock        EnvironmentMode = "mock"
	EnvironmentModeRecord      EnvironmentMode = "record"
	EnvironmentModeReplay      EnvironmentMode = "replay"
)

// EnvironmentCreateRequest is the body for PlatformClient.CreateEnvironment.
// Apps should prefer AppInstallIDs when cloning real sidecar installs;
// Apps is the legacy source-dir sandbox path.
type EnvironmentCreateRequest struct {
	ID                  string                       `json:"id,omitempty"`
	ProjectID           string                       `json:"project_id,omitempty"`
	GatewayURL          string                       `json:"gateway_url,omitempty"`
	Apps                []string                     `json:"apps,omitempty"`
	AppInstallIDs       []int64                      `json:"app_install_ids,omitempty"`
	ConnectionIDs       []int64                      `json:"connection_ids,omitempty"`
	Mode                EnvironmentMode              `json:"mode,omitempty"`
	AllowSuffixes       []string                     `json:"allow_suffixes,omitempty"`
	Mocks               []EnvironmentHTTPMock        `json:"mocks,omitempty"`
	IntegrationFixtures []EnvironmentIntegrationMock `json:"integration_fixtures,omitempty"`
	SeedPlan            []EnvironmentSeedCall        `json:"seed_plan,omitempty"`
	SeedBaseDir         string                       `json:"seed_base_dir,omitempty"`
	SnapshotID          string                       `json:"snapshot_id,omitempty"`
}

type EnvironmentSummary struct {
	ID          string                        `json:"id"`
	ProjectID   string                        `json:"project_id"`
	Mode        EnvironmentMode               `json:"mode"`
	ProxyURL    string                        `json:"proxy_url"`
	Apps        map[string]EnvironmentAppInfo `json:"apps"`
	Connections []EnvironmentConnectionInfo   `json:"connections"`
	Agents      []EnvironmentAgent            `json:"agents"`
}

type EnvironmentAppInfo struct {
	URL       string `json:"url"`
	MCPURL    string `json:"mcp_url"`
	DataDir   string `json:"data_dir"`
	Kind      string `json:"kind"`
	InstallID int64  `json:"install_id,omitempty"`
}

type EnvironmentConnectionInfo struct {
	ID        int64  `json:"id"`
	AppSlug   string `json:"app_slug"`
	AppName   string `json:"app_name"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ProjectID string `json:"project_id"`
}

type EnvironmentAgent struct {
	AgentID       int64     `json:"agent_id"`
	SourceAgentID int64     `json:"source_agent_id"`
	SourceName    string    `json:"source_name"`
	Alias         string    `json:"alias"`
	Port          int       `json:"port"`
	CreatedAt     time.Time `json:"created_at"`
}

type EnvironmentAgentSpawnRequest struct {
	SourceAgentID int64  `json:"source_agent_id"`
	Directive     string `json:"directive,omitempty"`
	Alias         string `json:"alias,omitempty"`
}

type EnvironmentSeedCall struct {
	App   string         `json:"app"`
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input,omitempty"`
	File  string         `json:"file,omitempty"`
}

type EnvironmentHTTPMock struct {
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Method  string            `json:"method"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type EnvironmentIntegrationMock struct {
	App    string `json:"app"`
	Tool   string `json:"tool"`
	Status int    `json:"status,omitempty"`
	Data   any    `json:"data,omitempty"`
}

type EnvironmentSnapshotRequest struct {
	SnapshotID  string `json:"snapshot_id,omitempty"`
	Description string `json:"description,omitempty"`
}

type EnvironmentSnapshot struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Description string    `json:"description,omitempty"`
	Apps        []string  `json:"apps"`
	HasAgent    bool      `json:"has_agent"`
	HasCassette bool      `json:"has_cassette"`
	CreatedAt   time.Time `json:"created_at"`
}

// GrantsResponse is what GetGrants returns. DefaultEffect is the
// install-wide fallback when no rule matches; Grants is the rule list
// for this (install, instance) pair. Empty rules + default "allow" =
// "this agent has full access" — the back-compat default.
type GrantsResponse struct {
	DefaultEffect string  `json:"default_effect"`
	Grants        []Grant `json:"grants"`
}

// OAuthStartRequest is the body for PlatformClient.StartOAuth.
type OAuthStartRequest struct {
	// IntegrationSlug — catalog slug ("twitter-api", "facebook-graph").
	IntegrationSlug string `json:"integration_slug"`
	// ReturnURL — where the platform 302s the browser after the OAuth
	// callback completes. Must be on the same host as the dashboard;
	// usually a path under the app's own /api/apps/<name>/... surface.
	ReturnURL string `json:"return_url"`
	// Name — a display label for the connection (e.g. "Twitter for
	// @marcoschwartz"). Optional; falls back to the integration name.
	Name string `json:"name,omitempty"`
	// ProjectID — scope. Defaults to the install's project when empty.
	ProjectID string `json:"project_id,omitempty"`
}

// OAuthStartResult is the response from PlatformClient.StartOAuth.
type OAuthStartResult struct {
	ConnectionID int64  `json:"connection_id"`
	AuthorizeURL string `json:"authorize_url"`
	ExpiresAt    string `json:"expires_at"`
}

// RealtimeTurnDetection configures provider-neutral realtime turn taking.
// Providers map the fields they support to their native session settings.
// A nil config preserves the provider's default behavior.
type RealtimeTurnDetection struct {
	Profile           string `json:"profile,omitempty"`
	StartSensitivity  string `json:"start_sensitivity,omitempty"`
	PrefixPaddingMS   int    `json:"prefix_padding_ms,omitempty"`
	EndSensitivity    string `json:"end_sensitivity,omitempty"`
	SilenceDurationMS int    `json:"silence_duration_ms,omitempty"`
	Interruption      string `json:"interruption,omitempty"`
}

// RealtimeCapabilityMode makes a realtime thread's app capability surface
// explicit. It governs Tools and MCP together; Core-owned coordination tools
// such as send/done/pace remain available according to Core policy.
type RealtimeCapabilityMode string

const (
	// RealtimeCapabilitiesInheritAgent asks the platform to resolve the target
	// agent's spawnable MCP surface. Tools and MCP must otherwise be empty.
	RealtimeCapabilitiesInheritAgent RealtimeCapabilityMode = "inherit_agent"
	// RealtimeCapabilitiesExplicit uses exactly the supplied Tools and MCP
	// lists. Empty lists deliberately mean no app capabilities.
	RealtimeCapabilitiesExplicit RealtimeCapabilityMode = "explicit"
	// RealtimeCapabilitiesNone disables all app tools and MCPs.
	RealtimeCapabilitiesNone RealtimeCapabilityMode = "none"
)

// RealtimeCallContext is trusted call metadata supplied separately from the
// model directive. The platform serializes it into a data-only system context;
// callers must never interpolate these values into Directive themselves.
type RealtimeCallContext struct {
	CallID         string `json:"call_id"`
	Direction      string `json:"direction,omitempty"`
	Provider       string `json:"provider,omitempty"`
	ProviderCallID string `json:"provider_call_id,omitempty"`
	RouteID        string `json:"route_id,omitempty"`
	FromNumber     string `json:"from_number,omitempty"`
	ToNumber       string `json:"to_number,omitempty"`
	ForwardedFrom  string `json:"forwarded_from,omitempty"`
	IngressPath    string `json:"ingress_path,omitempty"`
}

// RealtimeAudioTerminalReason is the structured reason a realtime audio
// bridge ended. It is suitable for both terminal JSON messages and WebSocket
// close reasons.
type RealtimeAudioTerminalReason string

const (
	RealtimeTerminalCallerDone        RealtimeAudioTerminalReason = "caller_done"
	RealtimeTerminalCarrierError      RealtimeAudioTerminalReason = "carrier_error"
	RealtimeTerminalAudioDisconnected RealtimeAudioTerminalReason = "audio_disconnected"
	RealtimeTerminalServerShutdown    RealtimeAudioTerminalReason = "server_shutdown"
	RealtimeTerminalStopped           RealtimeAudioTerminalReason = "stopped"
)

const RealtimeAudioTerminalMessageType = "audio.terminal"

// RealtimeAudioTerminalMessage is the terminal control frame contract for an
// audio bridge. Binary frames remain PCM audio; this message is JSON text.
type RealtimeAudioTerminalMessage struct {
	Type      string                      `json:"type"`
	Reason    RealtimeAudioTerminalReason `json:"reason"`
	CloseCode int                         `json:"close_code,omitempty"`
	Detail    string                      `json:"detail,omitempty"`
}

// RealtimeSpawnRequest is the body for PlatformClient.SpawnRealtimeThread.
// Creates a realtime (voice/audio) thread in core that the app can
// bridge audio into. The audio bridge runs over a separate
// token-authenticated WebSocket proxy — the result carries the full
// URL the app uses to connect.
type RealtimeSpawnRequest struct {
	// AgentID — the calling agent's instance id. The realtime thread
	// is spawned INSIDE this agent (as a sub-thread of main), so
	// the agent can address it via send/done. Apps using
	// ToolHandlerCtx should pull this from CallerFrom(ctx).AgentID
	// — the framework forwards the caller's agent via
	// X-Apteva-Caller-Agent automatically.
	AgentID int64 `json:"agent_id"`
	// ThreadID — unique id for the new thread within the agent's
	// thread tree. Conventionally namespaced by app (e.g.
	// "tel-<call_id>" for the telephony app).
	ThreadID string `json:"thread_id"`
	// Directive — the system instructions the realtime model runs with.
	// Same shape as a normal sub-thread directive.
	Directive string `json:"directive"`
	// Voice — realtime voice id (e.g. "marin"). Empty = provider default.
	Voice string `json:"voice,omitempty"`
	// Provider — explicit realtime provider name (e.g. "openai-realtime").
	// Empty = pool default.
	Provider string `json:"provider,omitempty"`
	// CapabilityMode explicitly controls how Tools and MCP are interpreted.
	// New callers should always set it. Omission retains the legacy behavior:
	// omitted Tools+MCP inherit the agent; otherwise they are explicit.
	CapabilityMode RealtimeCapabilityMode `json:"capability_mode,omitempty"`
	// Tools — app tool allowlist using full prefixed names. Interpretation is
	// controlled by CapabilityMode.
	Tools []string `json:"tools,omitempty"`
	// MCP — app MCP server names. Interpretation is controlled by
	// CapabilityMode.
	MCP []string `json:"mcp,omitempty"`
	// CallContext carries trusted call metadata separately from Directive.
	CallContext *RealtimeCallContext `json:"call_context,omitempty"`
	// TurnDetection selects a provider-neutral realtime VAD/turn-taking
	// profile and optional overrides. Nil preserves provider defaults.
	TurnDetection *RealtimeTurnDetection `json:"turn_detection,omitempty"`
	// Ephemeral deletes persisted session history when the caller-owned thread
	// is killed. Use this for temporary conversations such as phone calls.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// InitialMessage is injected only after the audio bridge connects, then
	// immediately requests a provider response. Telephony uses this for the
	// opening greeting so audio cannot be generated before a listener exists.
	InitialMessage string `json:"initial_message,omitempty"`
	// BridgeDisconnectTTLSeconds stops an abandoned realtime thread when its
	// audio bridge remains disconnected. Zero disables automatic cleanup.
	BridgeDisconnectTTLSeconds int `json:"bridge_disconnect_ttl_seconds,omitempty"`
}

// RealtimeSpawnResult is the response from PlatformClient.SpawnRealtimeThread.
type RealtimeSpawnResult struct {
	// Status — "created" (new thread) or "exists" (id was already
	// live; spawn was a no-op).
	Status string `json:"status"`
	// ThreadID — echoed from the request, useful when the caller
	// generated the id but wants to thread it through async code.
	ThreadID string `json:"id"`
	// AudioBridgeURL — full WebSocket URL the app's audio bridge
	// should dial as-is. Includes the public server host plus agent,
	// thread, and token query params; the app should NOT reconstruct it. Empty for "exists"
	// status — the original spawner already holds the token.
	AudioBridgeURL string `json:"audio_bridge_url,omitempty"`
	// AudioToken — single-use, returned for symmetry with
	// AudioBridgeURL in case the app wants to dial through a
	// different host (e.g. proxy). Single-use.
	AudioToken string `json:"audio_token,omitempty"`
	// Capabilities separates grants, connected/registered tools, and confirmed
	// provider presentation. Older cores report unknown readiness with grants.
	Capabilities *RealtimeCapabilities `json:"capabilities,omitempty"`
	// Deprecated compatibility projections. These are populated only when
	// Capabilities.Verified(): EffectiveTools mirrors PresentedTools and
	// EffectiveMCP mirrors ConnectedMCP. Unknown verification yields nil.
	EffectiveTools []string `json:"effective_tools"`
	EffectiveMCP   []string `json:"effective_mcp"`
	// True only for a confirmed active-session presentation snapshot. Use
	// MissingPresentedTools to additionally verify app-required tools.
	CapabilitiesVerified bool `json:"capabilities_verified"`
}

func validateRealtimeCapabilityMode(mode RealtimeCapabilityMode, tools, mcp []string) error {
	switch mode {
	case "":
		return nil // legacy compatibility; Server resolves nil versus explicit values.
	case RealtimeCapabilitiesExplicit:
		return nil
	case RealtimeCapabilitiesInheritAgent, RealtimeCapabilitiesNone:
		if len(tools) != 0 || len(mcp) != 0 {
			return fmt.Errorf("capability_mode %q cannot include tools or mcp", mode)
		}
		return nil
	default:
		return fmt.Errorf("invalid realtime capability_mode %q", mode)
	}
}

// ExecuteResult mirrors apteva-server's response shape for integration
// tool executions. data is the upstream API's response body parsed
// from JSON; status is the HTTP status the upstream returned.
//
// Headers carries a small allowlisted set of response headers the
// server forwards from the upstream (Location, Content-Type, Etag,
// Last-Modified). Apps that need to follow a redirect-style flow
// (YouTube resumable upload init returns the session URL only via
// Location) can read it here. Empty when the server is older than
// the introduction of header forwarding.
type ExecuteResult struct {
	Success bool              `json:"success"`
	Status  int               `json:"status"`
	Data    json.RawMessage   `json:"data"`
	Headers map[string]string `json:"headers,omitempty"`
}

// ConnectionCredentials is the decrypted credential bundle for a
// connection this install has bound. Returned by
// PlatformClient.GetConnectionCredentials. Fields keys mirror the
// catalog entry's auth.credential_fields[].name — slug-specific
// endpoint construction is the caller's job.
type ConnectionCredentials struct {
	ConnectionID int64             `json:"id"`
	Slug         string            `json:"slug"`
	Fields       map[string]string `json:"fields"`
	FetchedAt    time.Time         `json:"fetched_at"`
}

// ConnectionPublicConfig contains only fields explicitly marked public by the
// integration catalog. Secret credentials and generated webhook secrets are
// never included.
type ConnectionPublicConfig struct {
	ConnectionID int64             `json:"id"`
	Slug         string            `json:"slug"`
	Fields       map[string]string `json:"fields"`
	FetchedAt    time.Time         `json:"fetched_at"`
}

// ConnectionPublicConfigClient is an optional platform capability. Keeping it
// separate from PlatformClient avoids forcing unrelated app test doubles to
// implement a method they never use.
type ConnectionPublicConfigClient interface {
	GetConnectionPublicConfig(id int64) (*ConnectionPublicConfig, error)
}

// GetConnectionPublicConfig reads the safe public configuration for a bound
// integration when the connected platform supports the capability.
func GetConnectionPublicConfig(client PlatformClient, id int64) (*ConnectionPublicConfig, error) {
	reader, ok := client.(ConnectionPublicConfigClient)
	if !ok {
		return nil, errors.New("platform does not support public integration config")
	}
	return reader.GetConnectionPublicConfig(id)
}

type IntegrationWebhookEnsureRequest struct {
	ConnectionID int64    `json:"connection_id"`
	Role         string   `json:"role"`
	CallbackPath string   `json:"callback_path"`
	Events       []string `json:"events"`
}

type IntegrationWebhookStatus struct {
	ID           int64     `json:"id"`
	ConnectionID int64     `json:"connection_id"`
	Role         string    `json:"role"`
	Provider     string    `json:"provider"`
	CallbackURL  string    `json:"callback_url"`
	ExternalID   string    `json:"external_id,omitempty"`
	Status       string    `json:"status"`
	LastError    string    `json:"last_error,omitempty"`
	RegisteredAt time.Time `json:"registered_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type IntegrationWebhookVerifyRequest struct {
	Role      string `json:"role"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type IntegrationWebhookVerifyResult struct {
	Provider string          `json:"provider"`
	Event    json.RawMessage `json:"event"`
}

type PlatformConnection struct {
	ID                   int64                  `json:"id"`
	AppSlug              string                 `json:"app_slug"`
	Name                 string                 `json:"name"`
	Status               string                 `json:"status"`
	ProjectID            string                 `json:"project_id"`
	CredentialManagement string                 `json:"credential_management,omitempty"`
	ExportPolicy         ConnectionExportPolicy `json:"export_policy,omitempty"`
}

// ConnectionExportPolicy controls whether an integration connection's raw
// credential bundle may ever leave the platform. Existing connections retain
// the legacy bound-app policy. Managed connections created through the SDK are
// always ExportNever.
type ConnectionExportPolicy string

const (
	ExportBoundApp ConnectionExportPolicy = "bound_app"
	ExportNever    ConnectionExportPolicy = "never"
)

// ManagedConnectionRequest creates or updates one app-owned, non-exportable
// integration connection. Key is the durable idempotency key within the
// calling app installation. Repeating a request with the same key updates the
// same row instead of creating a duplicate.
type ManagedConnectionRequest struct {
	Key       string            `json:"idempotency_key"`
	AppSlug   string            `json:"app_slug"`
	Name      string            `json:"name"`
	AuthType  string            `json:"auth_type,omitempty"`
	ProjectID string            `json:"project_id,omitempty"`
	Fields    map[string]string `json:"fields"`
}

// ManagedConnectionRotation replaces the complete encrypted credential bundle
// for a managed connection. Public fields must be included again when they are
// still meant to be visible through GetConnectionPublicConfig.
type ManagedConnectionRotation struct {
	Fields map[string]string `json:"fields"`
}

// ManagedConnectionClient is optional so adding managed credentials does not
// force existing PlatformClient test doubles or older custom clients to grow
// new methods. The SDK's HTTP client implements it on supporting servers.
type ManagedConnectionClient interface {
	EnsureManagedConnection(req ManagedConnectionRequest) (*PlatformConnection, error)
	RotateManagedConnection(id int64, req ManagedConnectionRotation) (*PlatformConnection, error)
	RevokeManagedConnection(id int64) error
}

func EnsureManagedConnection(client PlatformClient, req ManagedConnectionRequest) (*PlatformConnection, error) {
	manager, ok := client.(ManagedConnectionClient)
	if !ok {
		return nil, errors.New("platform does not support managed connections")
	}
	return manager.EnsureManagedConnection(req)
}

func RotateManagedConnection(client PlatformClient, id int64, req ManagedConnectionRotation) (*PlatformConnection, error) {
	manager, ok := client.(ManagedConnectionClient)
	if !ok {
		return nil, errors.New("platform does not support managed connections")
	}
	return manager.RotateManagedConnection(id, req)
}

func RevokeManagedConnection(client PlatformClient, id int64) error {
	manager, ok := client.(ManagedConnectionClient)
	if !ok {
		return errors.New("platform does not support managed connections")
	}
	return manager.RevokeManagedConnection(id)
}

// ManagedTenantEnrollmentRequest creates a short-lived, one-time enrollment
// ticket. TenantID is the controller's durable idempotency key (normally the
// SaaS account or installation id); the returned Ticket is shown only through
// this privileged API and must be delivered to the tenant as a secret file.
type ManagedTenantEnrollmentRequest struct {
	TenantID  string `json:"tenant_id"`
	AccountID string `json:"account_id,omitempty"`
	ExpiresIn int64  `json:"expires_in_seconds,omitempty"`
}

type ManagedTenantEnrollment struct {
	TenantID  string    `json:"tenant_id"`
	AccountID string    `json:"account_id,omitempty"`
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ManagedTenantRequest registers a durable tenant identity for controllers
// that push desired state directly to a remote Apteva instance. Unlike an
// enrollment, this does not mint a polling identity or one-time ticket.
type ManagedTenantRequest struct {
	TenantID  string `json:"tenant_id"`
	AccountID string `json:"account_id,omitempty"`
}

type ManagedTenant struct {
	TenantID  string    `json:"tenant_id"`
	AccountID string    `json:"account_id,omitempty"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ManagedGrantConstraints is evaluated both by the tenant before forwarding
// and by the controller before execution. FixedInput pins exact values,
// AllowedValues limits a field to an allow-list, and DeniedFields rejects the
// presence of fields an app must never control.
type ManagedGrantConstraints struct {
	FixedInput    map[string]any      `json:"fixed_input,omitempty"`
	AllowedValues map[string][]string `json:"allowed_values,omitempty"`
	DeniedFields  []string            `json:"denied_fields,omitempty"`
}

type ManagedConnectionGrantRequest struct {
	TenantID     string                  `json:"tenant_id"`
	GrantID      string                  `json:"grant_id"`
	ConnectionID int64                   `json:"connection_id"`
	AppSlug      string                  `json:"app_slug"`
	ProjectID    string                  `json:"project_id,omitempty"`
	AllowedTools []string                `json:"allowed_tools,omitempty"`
	PublicFields map[string]string       `json:"public_fields,omitempty"`
	Constraints  ManagedGrantConstraints `json:"constraints,omitempty"`
}

type ManagedConnectionGrant struct {
	TenantID     string                  `json:"tenant_id"`
	GrantID      string                  `json:"grant_id"`
	ConnectionID int64                   `json:"connection_id"`
	AppSlug      string                  `json:"app_slug"`
	ProjectID    string                  `json:"project_id,omitempty"`
	Status       string                  `json:"status"`
	AllowedTools []string                `json:"allowed_tools,omitempty"`
	PublicFields map[string]string       `json:"public_fields,omitempty"`
	Constraints  ManagedGrantConstraints `json:"constraints,omitempty"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

// ManagedConnectionGrantDelivery is the secret-bearing envelope a trusted
// controller delivers to a remote Apteva instance. It contains a restricted
// grant token, never the parent connection's provider credentials.
type ManagedConnectionGrantDelivery struct {
	TenantID          string                  `json:"tenant_id"`
	GrantID           string                  `json:"grant_id"`
	ConnectionID      int64                   `json:"parent_connection_id"`
	AppSlug           string                  `json:"app_slug"`
	ProjectID         string                  `json:"project_id,omitempty"`
	ControllerToken   string                  `json:"controller_token"`
	ControllerExecute string                  `json:"controller_execute_url"`
	AllowedTools      []string                `json:"allowed_tools,omitempty"`
	PublicFields      map[string]string       `json:"public_fields,omitempty"`
	Constraints       ManagedGrantConstraints `json:"constraints,omitempty"`
}

type ManagedBundleApp struct {
	Key          string            `json:"key"`
	ManifestURL  string            `json:"manifest_url,omitempty"`
	ManifestYAML string            `json:"manifest_yaml,omitempty"`
	ProjectID    string            `json:"project_id,omitempty"`
	Config       map[string]string `json:"config,omitempty"`
	Bindings     map[string]string `json:"bindings,omitempty"` // role -> grant_id
}

type ManagedTenantBundleRequest struct {
	TenantID string             `json:"tenant_id"`
	BundleID string             `json:"bundle_id"`
	Apps     []ManagedBundleApp `json:"apps"`
}

type ManagedTenantBundle struct {
	TenantID  string             `json:"tenant_id"`
	BundleID  string             `json:"bundle_id"`
	Revision  int64              `json:"revision"`
	Status    string             `json:"status"`
	LastError string             `json:"last_error,omitempty"`
	Apps      []ManagedBundleApp `json:"apps"`
	UpdatedAt time.Time          `json:"updated_at"`
}

// ManagedProvisioningApplyRequest is the idempotent desired-state envelope
// accepted by a remote Apteva server's /api/provisioning/apply endpoint.
// RequestID is durable across retries. Bundle revisions must increase when
// desired app configuration changes.
type ManagedProvisioningApplyRequest struct {
	RequestID       string                           `json:"request_id"`
	TenantID        string                           `json:"tenant_id"`
	Grants          []ManagedConnectionGrantDelivery `json:"grants,omitempty"`
	RevokedGrantIDs []string                         `json:"revoked_grant_ids,omitempty"`
	Bundle          *ManagedTenantBundle             `json:"bundle,omitempty"`
}

type ManagedProvisioningApplyResult struct {
	RequestID   string           `json:"request_id"`
	TenantID    string           `json:"tenant_id"`
	Status      string           `json:"status"`
	Connections map[string]int64 `json:"connections,omitempty"`
	BundleID    string           `json:"bundle_id,omitempty"`
	Revision    int64            `json:"revision,omitempty"`
}

// ManagedTenantClient is optional so ordinary apps and older PlatformClient
// test doubles remain source compatible.
type ManagedTenantClient interface {
	EnsureManagedTenant(req ManagedTenantRequest) (*ManagedTenant, error)
	CreateManagedTenantEnrollment(req ManagedTenantEnrollmentRequest) (*ManagedTenantEnrollment, error)
	EnsureManagedConnectionGrant(req ManagedConnectionGrantRequest) (*ManagedConnectionGrant, error)
	GetManagedConnectionGrantDelivery(tenantID, grantID string) (*ManagedConnectionGrantDelivery, error)
	RevokeManagedConnectionGrant(tenantID, grantID string) error
	EnsureManagedTenantBundle(req ManagedTenantBundleRequest) (*ManagedTenantBundle, error)
	GetManagedTenantBundle(tenantID, bundleID string) (*ManagedTenantBundle, error)
}

func ManagedTenants(client PlatformClient) (ManagedTenantClient, error) {
	manager, ok := client.(ManagedTenantClient)
	if !ok {
		return nil, errors.New("platform does not support managed tenants")
	}
	return manager, nil
}

type ConnectionFilter struct {
	ProjectID string
	AppSlug   string
}

type PlatformInstance struct {
	// Optional server-owned visual identity. Older servers leave these empty.
	Icon string `json:"icon,omitempty"`
	IconColor string `json:"icon_color,omitempty"`
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Mode            string `json:"mode"`
	ProjectID       string `json:"project_id"`
	DefaultThreadID string `json:"default_thread_id,omitempty"`
	// AttachedToCaller reports whether this agent has the calling
	// install's app bound. It is populated by ListAgents responses and
	// by single-agent fetches for runtime-local agents, whose runtime
	// membership is itself the attachment boundary.
	AttachedToCaller bool `json:"attached_to_caller,omitempty"`
}

// ThreadRef is the stable, generic address of a thread inside an agent.
// It intentionally contains no role/kind field.
type ThreadRef struct {
	AgentID  int64  `json:"agent_id"`
	ThreadID string `json:"thread_id"`
}

// ThreadEvent is an idempotent initial event submitted while spawning a
// thread. ID must be stable across retries. Message uses the same payload
// contract as SendThreadEvent (a string or supported content-part array).
type ThreadEvent struct {
	ID      string `json:"id"`
	Message any    `json:"message"`
}

// ThreadEventReceipt reports which initial events Core newly queued and which
// were already present under the same stable IDs. Both outcomes are success.
type ThreadEventReceipt struct {
	Accepted   []string `json:"accepted,omitempty"`
	Duplicates []string `json:"duplicates,omitempty"`
}

// ThreadSpawnRequest creates an ordinary durable thread through the platform.
// Apps may persist their own creator/assignee/executor relationships using the
// returned ThreadRef; those relationships are not platform thread types.
type ThreadSpawnRequest struct {
	AgentID  int64  `json:"agent_id"`
	ThreadID string `json:"thread_id"`
	// ProjectID optionally binds this thread to a validated platform project.
	// Global agents such as platform assistants use it to keep app tool calls
	// scoped to the project that originated the thread. The platform, not the
	// model or tool arguments, remains authoritative for this value.
	ProjectID       string        `json:"project_id,omitempty"`
	DirectiveSuffix string        `json:"directive_suffix,omitempty"`
	Tools           []string      `json:"tools,omitempty"`
	MCP             []string      `json:"mcp,omitempty"`
	Ephemeral       bool          `json:"ephemeral,omitempty"`
	Events          []ThreadEvent `json:"events,omitempty"`
}

type ThreadSpawnResult struct {
	Status string             `json:"status"`
	Thread ThreadRef          `json:"thread"`
	Events ThreadEventReceipt `json:"events,omitempty"`
}

type ThreadEnsureRequest struct {
	ThreadSpawnRequest
	// ProfileHash is an app-computed stable hash of the complete desired
	// directive/tool/MCP profile. The platform may use it to avoid an inspect
	// and update round trip when the live profile already matches.
	ProfileHash string `json:"profile_hash,omitempty"`
}

type ThreadEnsureResult struct {
	Status      string             `json:"status"`
	Thread      ThreadRef          `json:"thread"`
	Events      ThreadEventReceipt `json:"events,omitempty"`
	ProfileHash string             `json:"profile_hash,omitempty"`
	Reconciled  bool               `json:"reconciled,omitempty"`
}

// PlatformAgent is the canonical type name for the entity formerly
// called PlatformInstance. The platform's user-facing concept is an
// "agent" (a running, named, threadable apteva-core child process);
// internal code is converging on that vocabulary across server,
// dashboard, SDK, and apps. This alias lets new app code reference
// PlatformAgent while existing apps using PlatformInstance keep
// compiling unchanged — both names denote the same struct.
//
// Phase 4 of the rename (one release after every consumer has had
// time to switch) drops the PlatformInstance name; until then both
// are first-class. Use PlatformAgent in new code.
type PlatformAgent = PlatformInstance

// InstallIdentity — what the app is and where it sits, returned by
// WhoAmI(). Useful when an app wants to log its own install id without
// re-reading env vars.
type InstallIdentity struct {
	AppName   string `json:"app_name"`
	Version   string `json:"version"`
	InstallID int64  `json:"install_id"`
	ProjectID string `json:"project_id"`
	// ProjectName is the operator-set human label for the project
	// (Settings → Projects → Name). Empty for global installs.
	// Apps that surface human-readable references to the project
	// (LLM system prompts, panel headers, generated documents)
	// should use this rather than the opaque ProjectID.
	ProjectName string `json:"project_name,omitempty"`
	// ProjectDescription is the operator-set free-text description
	// for the project (Settings → Projects → Description). Empty
	// when the operator hasn't filled it in. Useful as context for
	// LLM-using apps — e.g. media's describer prepends it to the
	// system prompt so generated descriptions land in the right
	// register ("internal team standups", "cooking show clips").
	ProjectDescription string       `json:"project_description,omitempty"`
	Permissions        []Permission `json:"permissions"`
	// Bindings carries the install's integration_bindings JSON —
	// keys are role names declared in the manifest, values are
	// connection_id (kind=integration) or install_id (kind=app),
	// or null when the operator declined an optional dep.
	Bindings map[string]any `json:"bindings"`
	// PublicURL is the platform's externally-reachable base URL
	// (e.g. "https://agents.example.com") — admin-editable from
	// Settings → Server, falls back to the PUBLIC_URL env var, falls
	// back to "" when unset (dev/local installs). Apps that mint
	// shareable links should prepend this rather than reading
	// APTEVA_PUBLIC_URL directly so settings changes propagate
	// through WhoAmI's sub-second cache without a sidecar restart.
	// Empty string means "no absolute URL configured" — apps should
	// fall back to relative paths and document the limitation.
	PublicURL string `json:"public_url,omitempty"`
}

// JSONNumberPreservingApp is an optional MCP decoding policy. Opted-in apps
// receive json.Number for every numeric input, including nested values and
// defaults, so large integers and precise decimals are not rounded before a
// handler sees them. The default remains float64 for existing applications.
// Handlers opting in must support json.Number wherever they accept numbers.
type JSONNumberPreservingApp interface{ PreserveJSONNumbers() bool }

// EmitWithProjectAck supports durable app outboxes without changing Emitter.
// Unsupported emitters return an error rather than claiming delivery.
func (c *AppCtx) EmitWithProjectAck(ctx context.Context, topic, projectID string, data any) error {
	if c == nil || c.emitter == nil {
		return errors.New("event emitter unavailable")
	}
	if e, ok := c.emitter.(interface {
		EmitWithProjectAck(context.Context, string, string, any) error
	}); ok {
		return e.EmitWithProjectAck(ctx, topic, projectID, data)
	}
	return errors.New("event emitter does not support acknowledgments")
}

// StartupContext is canceled on initialization timeout or process shutdown.
// It is for OnMount and migration work, not long-lived background workers.
func (c *AppCtx) StartupContext() context.Context {
	if c != nil && c.startupContext != nil {
		return c.startupContext
	}
	return context.Background()
}

// ReportStartupProgress publishes bounded counters on the initializing health
// response. Do not include credentials, customer data or table names in phase.
func (c *AppCtx) ReportStartupProgress(phase string, completed, total int64) {
	if c != nil && c.startup != nil {
		c.startup.progress(phase, completed, total)
	}
}
