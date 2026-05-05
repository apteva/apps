// Live Link app — give a locally-installed Apteva instance a public
// HTTPS URL.
//
// Two modes, picked via the `mode` config field:
//
//   - quick  (v0.1, default): Cloudflare Quick Tunnel. Anonymous, free,
//     fresh https://<random>.trycloudflare.com URL on every start. No
//     account or token required. Best for dev and one-off shares.
//
//   - named  (v0.2): a stable URL on a Cloudflare zone the operator
//     owns (e.g. https://tunnel.example.com). Requires an API token
//     with Cloudflare Tunnel:Edit + DNS:Edit, an account_id, and a
//     zone_id. The app uses those to create-or-adopt a cfd_tunnel,
//     PUT its ingress to point hostname → target_url, and upsert a
//     proxied CNAME → <tunnel_id>.cfargotunnel.com. Restarts reuse
//     the same tunnel + URL; uninstall reverses both steps via the
//     expose_destroy tool.
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
	// up.
	if _, err := ctx.AppDB().Exec(
		`UPDATE runs SET status = 'orphaned',
		                 finished_at = COALESCE(finished_at, CURRENT_TIMESTAMP),
		                 exit_reason = CASE WHEN exit_reason = '' THEN 'sidecar restarted' ELSE exit_reason END
		 WHERE status = 'running'`); err != nil {
		ctx.Logger().Warn("reconcile running runs", "err", err)
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
		"gateway", os.Getenv("APTEVA_GATEWAY_URL"))
	return nil
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
	mode := a.resolveMode(ctx)
	out["mode"] = string(mode)
	if mode == ModeNamed {
		// hostname is non-secret operator config — safe to surface.
		out["hostname"] = strings.TrimSpace(ctx.Config().Get("hostname"))
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
		"mode":       string(a.resolveMode(ctx)),
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

func (a *App) start(ctx *sdk.AppCtx) (string, error) {
	target := a.resolveTargetURL(ctx)
	if target == "" {
		return "", errors.New("no target URL — set target_url in app config or APTEVA_GATEWAY_URL in the env")
	}

	// Resolve the binary first so a missing/unsupported install
	// surfaces *before* we write a run row — keeps history clean of
	// "failed in 5ms because cloudflared wasn't installed yet" rows
	// that just confused the user. Synchronous download is fine; the
	// "Starting…" button covers it.
	binary, err := resolveBinary(ctx.Config().Get("cloudflared_path"), ctx.DataDir(), false, ctx.Logger().Info)
	if err != nil {
		return "", err
	}

	mode := a.resolveMode(ctx)
	params := StartParams{Binary: binary, Target: target, Mode: mode}

	if mode == ModeNamed {
		// Idempotent: creates the tunnel + DNS record on first start,
		// adopts the existing pair on subsequent starts. The CF API
		// calls happen *before* we insert the run row, so a config
		// error doesn't leave a "failed before launch" row behind.
		nt, err := a.ensureNamedTunnel(ctx)
		if err != nil {
			return "", err
		}
		params.Token = nt.TunnelToken
		params.Hostname = nt.Hostname
	}

	runID, err := dbInsertRun(ctx.AppDB(), "cloudflared", target, string(mode))
	if err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}
	params.RunID = runID

	if err := a.mgr.Start(params); err != nil {
		// Surface the reason in the run row so history shows *why*
		// the start attempt failed.
		_, _ = ctx.AppDB().Exec(
			`UPDATE runs SET status = 'failed', finished_at = CURRENT_TIMESTAMP, exit_reason = ?
			 WHERE id = ?`, err.Error(), runID)
		return "", err
	}
	return target, nil
}

// resolveMode picks quick vs named, defaulting to quick when the
// `mode` config field is empty or unset. Anything else is rejected at
// start time (we don't silently fall back).
func (a *App) resolveMode(ctx *sdk.AppCtx) Mode {
	if ctx == nil {
		return ModeQuick
	}
	v := strings.ToLower(strings.TrimSpace(ctx.Config().Get("mode")))
	switch v {
	case "", "quick":
		return ModeQuick
	case "named":
		return ModeNamed
	default:
		// Unknown value — fall back to quick rather than 500ing on a
		// typo. The status endpoint shows the resolved mode so the
		// operator can tell something didn't take.
		return ModeQuick
	}
}

// ensureNamedTunnel returns a usable named tunnel: from the local DB
// if one already exists for the configured hostname, otherwise creates
// it on Cloudflare (or adopts an existing CF tunnel with the same
// name) and persists the credentials. Idempotent — safe to call on
// every start.
func (a *App) ensureNamedTunnel(ctx *sdk.AppCtx) (*NamedTunnel, error) {
	cfg := ctx.Config()
	hostname := strings.TrimSpace(cfg.Get("hostname"))
	apiToken := strings.TrimSpace(cfg.Get("cf_api_token"))
	accountID := strings.TrimSpace(cfg.Get("cf_account_id"))
	zoneID := strings.TrimSpace(cfg.Get("cf_zone_id"))
	if hostname == "" || apiToken == "" || accountID == "" || zoneID == "" {
		return nil, errors.New("named mode requires hostname, cf_api_token, cf_account_id, and cf_zone_id in app config")
	}

	// Local cache hit: nothing to call CF for.
	if existing, err := dbGetNamedTunnel(ctx.AppDB(), hostname); err == nil && existing != nil {
		return existing, nil
	}

	cf := newCFClient(apiToken)
	name := namedTunnelPrefix + sanitizeForTunnelName(hostname)

	tun, err := cf.findTunnelByName(accountID, name)
	if err != nil {
		return nil, fmt.Errorf("cf find tunnel: %w", err)
	}
	if tun == nil {
		tun, err = cf.createTunnel(accountID, name)
		if err != nil {
			return nil, fmt.Errorf("cf create tunnel: %w", err)
		}
	} else if tun.Token == "" {
		// Adopting an existing tunnel: CF's list endpoint doesn't
		// return the connector token. Without it we can't `run`, so
		// fail loudly rather than silently re-creating (which would
		// abandon the old tunnel and need a manual cleanup).
		return nil, fmt.Errorf("a tunnel named %q already exists in this account but its connector token isn't available via the API; delete it in the Cloudflare dashboard or pick a different hostname", name)
	}

	target := a.resolveTargetURL(ctx)
	if err := cf.putTunnelConfig(accountID, tun.ID, hostname, target); err != nil {
		return nil, fmt.Errorf("cf put tunnel config: %w", err)
	}
	dnsRecordID, err := cf.upsertDNSCNAME(zoneID, hostname, tun.ID)
	if err != nil {
		return nil, fmt.Errorf("cf upsert dns: %w", err)
	}

	nt := &NamedTunnel{
		Hostname:    hostname,
		TunnelID:    tun.ID,
		TunnelToken: tun.Token,
		AccountID:   accountID,
		ZoneID:      zoneID,
		DNSRecordID: dnsRecordID,
	}
	if err := dbInsertNamedTunnel(ctx.AppDB(), nt); err != nil {
		return nil, fmt.Errorf("persist named tunnel: %w", err)
	}
	return nt, nil
}

// destroyNamedTunnel reverses ensureNamedTunnel: delete CNAME, delete
// CF tunnel, drop the local row. Returns whether anything was
// destroyed (false = no named tunnel to destroy). CF errors are
// returned as-is; callers can retry, since the local row only gets
// dropped after CF acknowledges both deletes.
func (a *App) destroyNamedTunnel(ctx *sdk.AppCtx) (bool, error) {
	cfg := ctx.Config()
	hostname := strings.TrimSpace(cfg.Get("hostname"))
	if hostname == "" {
		return false, nil
	}
	nt, err := dbGetNamedTunnel(ctx.AppDB(), hostname)
	if err != nil || nt == nil {
		return false, nil
	}
	apiToken := strings.TrimSpace(cfg.Get("cf_api_token"))
	if apiToken == "" {
		return false, errors.New("cf_api_token missing — cannot reach Cloudflare to destroy the tunnel")
	}
	cf := newCFClient(apiToken)
	if err := cf.deleteDNSRecord(nt.ZoneID, nt.DNSRecordID); err != nil {
		return false, fmt.Errorf("cf delete dns: %w", err)
	}
	if err := cf.deleteTunnel(nt.AccountID, nt.TunnelID); err != nil {
		return false, fmt.Errorf("cf delete tunnel: %w", err)
	}
	if err := dbDeleteNamedTunnel(ctx.AppDB(), hostname); err != nil {
		return false, fmt.Errorf("drop named tunnel row: %w", err)
	}
	return true, nil
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
