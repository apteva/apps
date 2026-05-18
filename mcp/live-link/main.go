// Live Link app — give a locally-installed Apteva instance a public
// HTTPS URL.
//
// Two modes, switched in-panel — no config knob needed:
//
//   - quick  (v0.1, default): Cloudflare Quick Tunnel. Anonymous, free,
//     fresh https://<random>.trycloudflare.com URL on every start. No
//     account or token required. Best for dev and one-off shares.
//
//   - named  (v0.3): a stable URL on a Cloudflare zone the operator
//     owns (e.g. https://tunnel.example.com). Requires the cloudflare
//     integration to be bound at install time. The panel calls
//     list_zones via the integration to populate a zone picker; the
//     operator enters a subdomain; /named/configure asks the platform
//     proxy to create-or-adopt a cfd_tunnel, configure ingress, and
//     upsert a proxied CNAME → <tunnel_id>.cfargotunnel.com. The
//     platform handles credential injection — the app never sees a
//     raw token. Restarts reuse the same tunnel + URL.
//
// Mode is determined by DB state: if a row exists in named_tunnels,
// the app is in named mode; otherwise quick. Switching is panel-driven
// (configure → named, destroy → quick); no install-time config field.
//
// In both modes, runs are persisted to runs(...) for "was the tunnel
// up at X?" history, with a mode column so the UI can render the
// distinction.
package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// manifestYAML is the on-disk apteva.yaml, embedded at compile time.
// Keeping the binary's view of the manifest and the file the registry
// fetches identical removes a whole class of drift bugs (e.g. the
// dashboard rendering a config_schema field the sidecar doesn't know
// about).
//
//go:embed apteva.yaml
var manifestYAML string

// Default forwarded URL when the operator doesn't override target_url.
// Matches apteva-server's default port. The platform also injects
// APTEVA_GATEWAY_URL pointing at itself, which we prefer when set.
const defaultTargetURL = "http://localhost:5280"

// namedTunnelPrefix scopes the auto-generated CF tunnel name so an
// operator running multiple Apteva installs doesn't collide. The
// install-id-or-host suffix is appended at create time.
const namedTunnelPrefix = "apteva-live-link-"

type App struct {
	mgr *Manager

	// providers holds the strategy implementations available to this
	// install. v0.4.0 ships two; future providers (self-vps, ngrok,
	// tailscale-funnel) append to this slice. activeProvider() picks
	// the right one per request based on DB state. App methods like
	// ensureNamedTunnel still exist as test-stable entrypoints; they
	// are now implemented as thin facades that the named provider
	// also delegates to.
	providers []Provider
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
		return errors.New("live-link requires a db block")
	}
	globalCtx = ctx

	// Reconcile DB state with reality: any 'running' rows from a
	// previous sidecar life are dead — the subprocess died with us.
	// Mark them orphaned so the UI doesn't lie about a tunnel being
	// up. orphanedCount tells us whether the previous life ended
	// while a tunnel was up — the auto-restart trigger below.
	var orphanedCount int64
	res, err := ctx.AppDB().Exec(
		`UPDATE runs SET status = 'orphaned',
		                 finished_at = COALESCE(finished_at, CURRENT_TIMESTAMP),
		                 exit_reason = CASE WHEN exit_reason = '' THEN 'sidecar restarted' ELSE exit_reason END
		 WHERE status = 'running'`)
	if err != nil {
		ctx.Logger().Warn("reconcile running runs", "err", err)
	} else if res != nil {
		orphanedCount, _ = res.RowsAffected()
	}

	// Build the provider registry. Order matters: activeProvider()
	// asks each non-quick provider whether it's Configured() and
	// returns the first match; quick is the fallback default. Adding
	// future providers means inserting them ahead of quick.
	//
	// Precedence today (first Configured wins):
	//   1. cloudflare-named — operator pinned a Cloudflare zone + hostname
	//   2. ngrok            — operator bound the ngrok integration
	//   3. cloudflare-quick — default fallback, anonymous trycloudflare URL
	//
	// Operators with both Cloudflare-named AND ngrok bound get
	// cloudflare-named (more "intentional"). They can flip by destroying
	// the named tunnel.
	a.providers = []Provider{
		&cloudflareNamedProvider{app: a},
		&ngrokProvider{app: a},
		&cloudflareQuickProvider{app: a},
	}

	// Manager wires its lifecycle callbacks back into the DB.
	a.mgr = NewManager(
		func(runID int64, url string) {
			if _, err := ctx.AppDB().Exec(
				`UPDATE runs SET public_url = ? WHERE id = ?`, url, runID); err != nil {
				ctx.Logger().Warn("persist public_url", "err", err, "run_id", runID)
			}
			ctx.Emit("tunnel.url", map[string]any{"run_id": runID, "url": url})
		},
		func(runID int64, reason string, status Status) {
			finalStatus := "stopped"
			if status == StatusFailed {
				finalStatus = "failed"
			}
			if _, err := ctx.AppDB().Exec(
				`UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP,
				                 exit_reason = ?
				 WHERE id = ?`, finalStatus, reason, runID); err != nil {
				ctx.Logger().Warn("persist run exit", "err", err, "run_id", runID)
			}
			ctx.Emit("tunnel.exit", map[string]any{"run_id": runID, "status": finalStatus, "reason": reason})
		},
	)

	ctx.Logger().Info("live-link mounted",
		"gateway", os.Getenv("APTEVA_GATEWAY_URL"),
		"orphaned_runs", orphanedCount)

	// 2-second delay lets the local HTTP listener finish wiring up
	// and (in named mode) the platform's integration cache settle
	// before we hit ExecuteIntegrationTool.
	if trigger := autoRestartTrigger(ctx, orphanedCount); trigger != "" {
		go func() {
			time.Sleep(2 * time.Second)
			// Skip if something else (a fast operator click) already
			// brought the manager up — start() would just spawn a
			// duplicate run row marked failed.
			if a.mgr != nil && a.mgr.Snapshot().Status == StatusRunning {
				return
			}
			if _, err := a.start(ctx); err != nil {
				ctx.Logger().Warn("auto-restart failed", "trigger", trigger, "err", err.Error())
			} else {
				ctx.Logger().Info("auto-restart succeeded", "trigger", trigger)
			}
		}()
	}
	return nil
}

// autoRestartTrigger returns a non-empty reason for why the sidecar
// should bring the tunnel back on this boot, or "" when it should
// stay idle. Pure read of DB state + config so it's unit-testable
// without spawning cloudflared.
//
// Two trigger paths reflect operator *intent*, not the cleanliness
// of the previous shutdown:
//
//   - named-tunnel-persists: a named_tunnels row exists. Operator
//     configured a stable URL; bring it back whether the previous
//     sidecar died cleanly (SIGTERM → OnUnmount → Stop →
//     status='stopped' in DB) or unexpectedly (SIGKILL / crash →
//     status='running' → just orphaned above). Without this, a
//     clean restart of apteva-server would silently leave the URL
//     down — breaking the "stable URL across reboots" promise.
//
//   - orphan-detected: quick mode (no persistent state). Restart
//     only when the previous sidecar was serving when it died.
//     Operator who clicks Stop in quick mode shouldn't get a fresh
//     trycloudflare URL on next boot.
func autoRestartTrigger(ctx *sdk.AppCtx, orphanedCount int64) string {
	if !shouldAutoRestartOnBoot(ctx) {
		return ""
	}
	if nt, _ := dbFirstNamedTunnel(ctx.AppDB()); nt != nil {
		return "named-tunnel-persists"
	}
	if orphanedCount > 0 {
		return "orphan-detected"
	}
	return ""
}

// shouldAutoRestartOnBoot reads the operator's preference. Default is
// true — most operators install live-link to keep a URL up; they
// don't want to click "Go live" after every laptop sleep / server
// reboot. Recognizes "false" / "0" / "no" as opt-out.
func shouldAutoRestartOnBoot(ctx *sdk.AppCtx) bool {
	v := strings.ToLower(strings.TrimSpace(ctx.Config().Get("auto_restart_on_boot")))
	if v == "" {
		return true
	}
	return v != "false" && v != "0" && v != "no" && v != "off"
}

func (a *App) OnUnmount(ctx *sdk.AppCtx) error {
	// Best-effort: kill any in-flight tunnel before we exit so we
	// don't leak a cloudflared subprocess.
	if a.mgr != nil {
		_ = a.mgr.Stop()
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/status", Handler: a.handleStatus},
		{Pattern: "/start", Handler: a.handleStart},
		{Pattern: "/stop", Handler: a.handleStop},
		{Pattern: "/runs", Handler: a.handleRuns},
		{Pattern: "/install", Handler: a.handleInstall},
		{Pattern: "/destroy", Handler: a.handleDestroy},
		{Pattern: "/named/zones", Handler: a.handleNamedZones},
		{Pattern: "/named/configure", Handler: a.handleNamedConfigure},
		{Pattern: "/named/current", Handler: a.handleNamedCurrent},
	}
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	snap := a.mgr.Snapshot()
	out := map[string]any{
		"status":     snap.Status,
		"public_url": snap.PublicURL,
		"target_url": snap.TargetURL,
		"run_id":     snap.RunID,
		"last_error": snap.LastError,
	}
	if !snap.StartedAt.IsZero() {
		out["started_at"] = snap.StartedAt.UTC().Format(time.RFC3339)
	}
	// Surface the resolved target + mode so the UI can show the
	// pre-flight state before the user clicks start.
	out["resolved_target"] = a.resolveTargetURL(ctx)
	mode := a.currentMode(ctx)
	out["mode"] = string(mode)
	// v0.4: surface the new provider name alongside the legacy mode
	// field. Panel can switch over to provider once it ships
	// non-cloudflare options; v0.3 panels stay on mode without
	// breaking.
	out["provider"] = a.activeProviderName(ctx)
	// Always surface bound-integration booleans + the configured
	// hostname (if any), regardless of active provider, so the panel
	// can render the right config CTAs without a separate roundtrip.
	// Each `*_bound` is true when the corresponding integration role
	// has a non-nil binding on this install.
	out["cloudflare_bound"] = ctx.IntegrationFor("cloudflare") != nil
	out["ngrok_bound"] = ctx.IntegrationFor("ngrok") != nil
	if nt, _ := dbFirstNamedTunnel(ctx.AppDB()); nt != nil {
		out["hostname"] = nt.Hostname
	}
	// ngrok's reserved-domain config — surfaced for the panel's
	// "currently configured" hint when the active provider is ngrok.
	if v := strings.TrimSpace(ctx.Config().Get("ngrok_domain")); v != "" {
		out["ngrok_domain"] = v
	}
	httpJSON(w, out)
}

func (a *App) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	url, err := a.start(ctx)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"started": true, "target_url": url})
}

func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := a.mgr.Stop(); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"stopped": true})
}

// handleInstall force-downloads cloudflared into the install's data
// dir, even if a usable copy already exists. Powers the "Reinstall
// binary" link in the UI — handy when Cloudflare ships a fix and the
// operator wants to refresh without waiting for the next manual
// upgrade. Refuses to run while a tunnel is up (the running process
// would still be the old binary anyway).
func (a *App) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	if a.mgr.Snapshot().Status == StatusRunning {
		httpErr(w, http.StatusConflict, "stop the tunnel before reinstalling")
		return
	}
	dataDir := ctx.DataDir()
	path, err := resolveBinary(ctx.Config().Get("cloudflared_path"), dataDir, true, ctx.Logger().Info)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"installed": true, "path": path})
}

// handleDestroy tears down the named tunnel: deletes the CF-side
// tunnel + DNS record, then drops the local row. Refuses while a
// tunnel is up — operator must stop it first. No-op for installs
// that never created a named tunnel.
func (a *App) handleDestroy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.mgr.Snapshot().Status == StatusRunning {
		httpErr(w, http.StatusConflict, "stop the tunnel before destroying it")
		return
	}
	ctx := getAppCtx(r)
	destroyed, err := a.destroyNamedTunnel(ctx)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"destroyed": destroyed})
}

func (a *App) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	runs, err := dbListRuns(ctx.AppDB(), limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"runs": runs})
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "expose_start",
			Description: "Start a public tunnel and return its URL. In quick mode the URL is a fresh trycloudflare.com subdomain; in named mode it's the configured stable hostname. Idempotent — if a tunnel is already running, returns the existing one.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolStart,
		},
		{
			Name:        "expose_stop",
			Description: "Stop the running tunnel, if any. No-op when idle. Named tunnels persist on Cloudflare — only the local connector stops; use expose_destroy to remove them entirely.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolStop,
		},
		{
			Name:        "expose_status",
			Description: "Report whether a tunnel is currently running, its public URL, and which mode (quick/named) is configured.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolStatus,
		},
		{
			Name:        "expose_destroy",
			Description: "Tear down the named tunnel: delete it on Cloudflare and remove the CNAME record. Refuses while the tunnel is running. No-op when no named tunnel was ever created. Quick-mode tunnels have nothing to destroy.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolDestroy,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolStart(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	// If a tunnel is already up, return its current state instead of
	// erroring — agents calling this idempotently shouldn't have to
	// special-case the second call.
	if snap := a.mgr.Snapshot(); snap.Status == StatusRunning {
		return waitForURL(a.mgr, snap.RunID), nil
	}
	if _, err := a.start(ctx); err != nil {
		return nil, err
	}
	// Block briefly for the URL — the agent's caller will almost
	// always want the URL in the response, not just "started".
	return waitForURL(a.mgr, a.mgr.Snapshot().RunID), nil
}

func (a *App) toolStop(_ *sdk.AppCtx, _ map[string]any) (any, error) {
	if err := a.mgr.Stop(); err != nil {
		return nil, err
	}
	return map[string]any{"stopped": true}, nil
}

func (a *App) toolStatus(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	snap := a.mgr.Snapshot()
	return map[string]any{
		"status":     snap.Status,
		"public_url": snap.PublicURL,
		"target_url": snap.TargetURL,
		"run_id":     snap.RunID,
		"last_error": snap.LastError,
		"mode":       string(a.currentMode(ctx)),
		"provider":   a.activeProviderName(ctx),
	}, nil
}

func (a *App) toolDestroy(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	if a.mgr.Snapshot().Status == StatusRunning {
		return nil, errors.New("stop the tunnel before destroying it")
	}
	destroyed, err := a.destroyNamedTunnel(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"destroyed": destroyed}, nil
}

// waitForURL polls the manager for up to 15s, hoping cloudflared
// emits its assigned URL in time. Real-world latency is 1-3s; we time
// out gracefully with whatever we have so the agent gets a useful
// answer either way (just status=running, public_url="" if slow).
func waitForURL(mgr *Manager, runID int64) map[string]any {
	deadline := time.Now().Add(15 * time.Second)
	for {
		snap := mgr.Snapshot()
		if snap.PublicURL != "" || snap.RunID != runID || snap.Status != StatusRunning {
			return map[string]any{
				"status":     snap.Status,
				"public_url": snap.PublicURL,
				"target_url": snap.TargetURL,
				"run_id":     snap.RunID,
				"last_error": snap.LastError,
			}
		}
		if time.Now().After(deadline) {
			return map[string]any{
				"status":     snap.Status,
				"public_url": "",
				"target_url": snap.TargetURL,
				"run_id":     snap.RunID,
				"note":       "tunnel started but URL not yet assigned; check /status shortly",
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ─── Start logic shared by HTTP + MCP entrypoints ───────────────────

// start picks the active provider and delegates. v0.3's mode-based
// dispatch is now inside the providers themselves — see
// provider_cloudflare.go. The shape (returns target URL or error,
// idempotent in concert with mgr.Snapshot()) is unchanged.
func (a *App) start(ctx *sdk.AppCtx) (string, error) {
	p := a.activeProvider(ctx)
	if p == nil {
		return "", errors.New("no provider available — provider registry not initialized")
	}
	return p.Start(ctx)
}

// currentMode reports which legacy v0.3-shape mode is active. v0.4
// introduced the Provider abstraction; Mode survives only because
// existing v0.3 panel callers (and their tests) read it. New callers
// should prefer activeProviderName(ctx).
//
// Implementation derives from the provider registry: a non-empty
// providers slice is the single source of truth. If providers haven't
// been wired (e.g. some tests instantiate App{} directly without
// OnMount), fall back to the v0.3 DB-state read so those tests
// continue to pass.
func (a *App) currentMode(ctx *sdk.AppCtx) Mode {
	if ctx == nil || ctx.AppDB() == nil {
		return ModeQuick
	}
	if a != nil && len(a.providers) > 0 {
		if a.activeProviderName(ctx) == providerNameNamed {
			return ModeNamed
		}
		return ModeQuick
	}
	// Fallback for tests that build &App{} without OnMount.
	if nt, _ := dbFirstNamedTunnel(ctx.AppDB()); nt != nil {
		return ModeNamed
	}
	return ModeQuick
}

// cfConnectionID returns the bound cloudflare integration's
// connection id, or an actionable error if the operator hasn't bound
// one yet. Named mode is unusable without it.
func (a *App) cfConnectionID(ctx *sdk.AppCtx) (int64, error) {
	bound := ctx.IntegrationFor("cloudflare")
	if bound == nil {
		return 0, errors.New("named mode requires the cloudflare integration — bind a connection in app settings")
	}
	if bound.ConnectionID == 0 {
		return 0, errors.New("cloudflare integration is bound but has no connection id (binding may be in progress)")
	}
	return bound.ConnectionID, nil
}

// ensureNamedTunnel creates-or-adopts the CF tunnel + CNAME for the
// given hostname/zone, persisting the result in named_tunnels. All
// CF traffic goes through ctx.PlatformAPI().ExecuteIntegrationTool,
// so the app never holds a raw API token.
func (a *App) ensureNamedTunnel(ctx *sdk.AppCtx, hostname, zoneID string) (*NamedTunnel, error) {
	hostname = strings.TrimSpace(hostname)
	zoneID = strings.TrimSpace(zoneID)
	if hostname == "" || zoneID == "" {
		return nil, errors.New("hostname and zone_id are required")
	}
	if existing, err := dbGetNamedTunnel(ctx.AppDB(), hostname); err == nil && existing != nil {
		return existing, nil
	}

	connID, err := a.cfConnectionID(ctx)
	if err != nil {
		return nil, err
	}
	tunnelName := namedTunnelPrefix + sanitizeForTunnelName(hostname)

	tun, err := cfFindTunnel(ctx, connID, tunnelName)
	if err != nil {
		return nil, fmt.Errorf("cf find tunnel: %w", err)
	}
	var tunnelID, token string
	if tun != nil {
		tunnelID = tun.ID
		// Adopting: list_tunnels doesn't return the token, fetch it
		// separately via get_tunnel_token.
		token, err = cfGetTunnelToken(ctx, connID, tunnelID)
		if err != nil {
			return nil, fmt.Errorf("cf get tunnel token: %w", err)
		}
	} else {
		created, err := cfCreateTunnel(ctx, connID, tunnelName)
		if err != nil {
			return nil, fmt.Errorf("cf create tunnel: %w", err)
		}
		tunnelID, token = created.ID, created.Token
	}

	target := a.resolveTargetURL(ctx)
	if err := cfPutTunnelConfig(ctx, connID, tunnelID, hostname, target); err != nil {
		return nil, fmt.Errorf("cf put tunnel config: %w", err)
	}
	recordID, err := cfUpsertCNAME(ctx, connID, zoneID, hostname, tunnelID+".cfargotunnel.com")
	if err != nil {
		return nil, fmt.Errorf("cf upsert dns: %w", err)
	}

	nt := &NamedTunnel{
		Hostname:    hostname,
		TunnelID:    tunnelID,
		TunnelToken: token,
		// account_id is no longer carried by the app — the platform
		// substitutes it from the connection's stored creds. Keep the
		// column for schema-compat but write empty.
		AccountID:   "",
		ZoneID:      zoneID,
		DNSRecordID: recordID,
	}
	if err := dbInsertNamedTunnel(ctx.AppDB(), nt); err != nil {
		return nil, fmt.Errorf("persist named tunnel: %w", err)
	}
	return nt, nil
}

// destroyNamedTunnel reverses ensureNamedTunnel for whatever named
// tunnel is currently configured (at most one row in v0.3). Returns
// whether anything was destroyed. CF errors are returned as-is; the
// local row only gets dropped after CF acknowledges both deletes,
// so retries pick up where they left off.
func (a *App) destroyNamedTunnel(ctx *sdk.AppCtx) (bool, error) {
	nt, err := dbFirstNamedTunnel(ctx.AppDB())
	if err != nil || nt == nil {
		return false, nil
	}
	connID, err := a.cfConnectionID(ctx)
	if err != nil {
		return false, err
	}
	if err := cfDeleteDNSRecord(ctx, connID, nt.ZoneID, nt.DNSRecordID); err != nil {
		return false, fmt.Errorf("cf delete dns: %w", err)
	}
	if err := cfDeleteTunnel(ctx, connID, nt.TunnelID); err != nil {
		return false, fmt.Errorf("cf delete tunnel: %w", err)
	}
	if err := dbDeleteNamedTunnel(ctx.AppDB(), nt.Hostname); err != nil {
		return false, fmt.Errorf("drop named tunnel row: %w", err)
	}
	return true, nil
}

// ─── Named-mode HTTP handlers ──────────────────────────────────────

func (a *App) handleNamedZones(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	connID, err := a.cfConnectionID(ctx)
	if err != nil {
		httpErr(w, http.StatusFailedDependency, err.Error())
		return
	}
	zones, err := cfListZones(ctx, connID)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, map[string]any{"zones": zones})
}

// handleNamedConfigure sets the active hostname for named mode. Body
// is {zone_id, hostname}. If a different tunnel was previously
// configured, it's destroyed first so we don't accumulate orphans on
// CF. The reconciliation isn't atomic — a network failure in the
// middle leaves the old row gone but the new one in flight; the
// operator just retries with the same args (ensureNamedTunnel is
// idempotent on hostname).
func (a *App) handleNamedConfigure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.mgr.Snapshot().Status == StatusRunning {
		httpErr(w, http.StatusConflict, "stop the tunnel before reconfiguring")
		return
	}
	var body struct {
		ZoneID   string `json:"zone_id"`
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ctx := getAppCtx(r)

	// If the operator is changing hostnames, tear down the old one.
	if existing, _ := dbFirstNamedTunnel(ctx.AppDB()); existing != nil && existing.Hostname != strings.TrimSpace(body.Hostname) {
		if _, err := a.destroyNamedTunnel(ctx); err != nil {
			httpErr(w, http.StatusBadGateway, "could not tear down previous tunnel: "+err.Error())
			return
		}
	}

	nt, err := a.ensureNamedTunnel(ctx, body.Hostname, body.ZoneID)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, map[string]any{
		"hostname":  nt.Hostname,
		"tunnel_id": nt.TunnelID,
		"zone_id":   nt.ZoneID,
	})
}

func (a *App) handleNamedCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	nt, _ := dbFirstNamedTunnel(ctx.AppDB())
	if nt == nil {
		httpJSON(w, map[string]any{"configured": false})
		return
	}
	httpJSON(w, map[string]any{
		"configured": true,
		"hostname":   nt.Hostname,
		"zone_id":    nt.ZoneID,
		"tunnel_id":  nt.TunnelID,
	})
}

// sanitizeForTunnelName turns a hostname into a CF-tunnel-name-safe
// suffix: lowercase, dots → hyphens, drop anything that's not
// alphanumeric or hyphen. CF tunnel names must be 1-32 chars; we trim
// to 24 to leave room for the prefix.
func sanitizeForTunnelName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// resolveTargetURL picks the URL cloudflared should forward to, in
// order of preference:
//
//  1. target_url in the app's install config (operator override)
//  2. APTEVA_GATEWAY_URL env (the platform tells us where it is)
//  3. defaultTargetURL — apteva-server's default port on localhost
func (a *App) resolveTargetURL(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if v := strings.TrimSpace(ctx.Config().Get("target_url")); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(os.Getenv("APTEVA_GATEWAY_URL")); v != "" {
		return v
	}
	return defaultTargetURL
}

// ─── Domain types ──────────────────────────────────────────────────

type Run struct {
	ID         int64  `json:"id"`
	Provider   string `json:"provider"`
	Mode       string `json:"mode"`
	TargetURL  string `json:"target_url"`
	PublicURL  string `json:"public_url"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Status     string `json:"status"`
	ExitReason string `json:"exit_reason,omitempty"`
}

// NamedTunnel mirrors a row in the named_tunnels table — the
// persistent CF-side state for one stable hostname.
type NamedTunnel struct {
	ID          int64
	Hostname    string
	TunnelID    string
	TunnelToken string
	AccountID   string
	ZoneID      string
	DNSRecordID string
	CreatedAt   string
}

// ─── DB helpers ────────────────────────────────────────────────────

func dbInsertRun(db *sql.DB, provider, target, mode string) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO runs (provider, target_url, status, mode)
		 VALUES (?, ?, 'running', ?)`,
		provider, target, mode)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func dbListRuns(db *sql.DB, limit int) ([]*Run, error) {
	rows, err := db.Query(
		`SELECT id, provider, target_url, public_url, started_at,
		        COALESCE(finished_at,''), status, exit_reason, mode
		 FROM runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r := &Run{}
		if err := rows.Scan(&r.ID, &r.Provider, &r.TargetURL, &r.PublicURL,
			&r.StartedAt, &r.FinishedAt, &r.Status, &r.ExitReason, &r.Mode); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func dbGetNamedTunnel(db *sql.DB, hostname string) (*NamedTunnel, error) {
	row := db.QueryRow(
		`SELECT id, hostname, tunnel_id, tunnel_token, account_id, zone_id,
		        dns_record_id, created_at
		 FROM named_tunnels WHERE hostname = ?`, hostname)
	nt := &NamedTunnel{}
	if err := row.Scan(&nt.ID, &nt.Hostname, &nt.TunnelID, &nt.TunnelToken,
		&nt.AccountID, &nt.ZoneID, &nt.DNSRecordID, &nt.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return nt, nil
}

// dbFirstNamedTunnel returns whichever named_tunnels row was inserted
// most recently — there's at most one in v0.3 (one hostname per
// install), but using ORDER BY id DESC LIMIT 1 keeps the lookup
// resilient if a future version supports multiple.
func dbFirstNamedTunnel(db *sql.DB) (*NamedTunnel, error) {
	row := db.QueryRow(
		`SELECT id, hostname, tunnel_id, tunnel_token, account_id, zone_id,
		        dns_record_id, created_at
		 FROM named_tunnels ORDER BY id DESC LIMIT 1`)
	nt := &NamedTunnel{}
	if err := row.Scan(&nt.ID, &nt.Hostname, &nt.TunnelID, &nt.TunnelToken,
		&nt.AccountID, &nt.ZoneID, &nt.DNSRecordID, &nt.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return nt, nil
}

func dbInsertNamedTunnel(db *sql.DB, nt *NamedTunnel) error {
	_, err := db.Exec(
		`INSERT INTO named_tunnels (hostname, tunnel_id, tunnel_token,
		                            account_id, zone_id, dns_record_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		nt.Hostname, nt.TunnelID, nt.TunnelToken,
		nt.AccountID, nt.ZoneID, nt.DNSRecordID)
	return err
}

func dbDeleteNamedTunnel(db *sql.DB, hostname string) error {
	_, err := db.Exec(`DELETE FROM named_tunnels WHERE hostname = ?`, hostname)
	return err
}

// ─── globalCtx + http helpers (mirrors backup app's pattern) ───────

var globalCtx *sdk.AppCtx

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
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
