// Live Link app v0.1 — give a locally-installed Apteva instance a
// public HTTPS URL.
//
//   ┌──────────────┐  POST /start   ┌──────────────────────┐
//   │  dashboard   │ ─────────────► │  live-link app       │
//   │  (toggle)    │                │   spawns cloudflared │
//   └──────────────┘                │   parses public URL  │
//                                   └──────┬───────────────┘
//                                          │
//                                          ▼
//                                  https://<random>.trycloudflare.com
//                                          │
//                                          ▼
//                                  Cloudflare edge → user's apteva-server
//
// v0.1 ships a single provider (Cloudflare Quick Tunnel) so the
// experience is "install → click → public URL", with no account or
// token. Future versions add named tunnels, ngrok, and automatic
// PUBLIC_URL rewrite for OAuth callbacks.
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
	// Surface the resolved target so the UI can show it pre-flight,
	// even before the user clicks start.
	out["resolved_target"] = a.resolveTargetURL(ctx)
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
			Description: "Start a public tunnel and return the assigned trycloudflare.com URL. Idempotent — if a tunnel is already running, returns the existing one.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolStart,
		},
		{
			Name:        "expose_stop",
			Description: "Stop the running tunnel, if any. No-op when idle.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolStop,
		},
		{
			Name:        "expose_status",
			Description: "Report whether a tunnel is currently running and its public URL.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolStatus,
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

func (a *App) toolStatus(_ *sdk.AppCtx, _ map[string]any) (any, error) {
	snap := a.mgr.Snapshot()
	return map[string]any{
		"status":     snap.Status,
		"public_url": snap.PublicURL,
		"target_url": snap.TargetURL,
		"run_id":     snap.RunID,
		"last_error": snap.LastError,
	}, nil
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

	runID, err := dbInsertRun(ctx.AppDB(), "cloudflared", target)
	if err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}

	if err := a.mgr.Start(binary, target, runID); err != nil {
		// Surface the reason in the run row so history shows *why*
		// the start attempt failed.
		_, _ = ctx.AppDB().Exec(
			`UPDATE runs SET status = 'failed', finished_at = CURRENT_TIMESTAMP, exit_reason = ?
			 WHERE id = ?`, err.Error(), runID)
		return "", err
	}
	return target, nil
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
	TargetURL  string `json:"target_url"`
	PublicURL  string `json:"public_url"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Status     string `json:"status"`
	ExitReason string `json:"exit_reason,omitempty"`
}

// ─── DB helpers ────────────────────────────────────────────────────

func dbInsertRun(db *sql.DB, provider, target string) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO runs (provider, target_url, status) VALUES (?, ?, 'running')`,
		provider, target)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func dbListRuns(db *sql.DB, limit int) ([]*Run, error) {
	rows, err := db.Query(
		`SELECT id, provider, target_url, public_url, started_at,
		        COALESCE(finished_at,''), status, exit_reason
		 FROM runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r := &Run{}
		if err := rows.Scan(&r.ID, &r.Provider, &r.TargetURL, &r.PublicURL,
			&r.StartedAt, &r.FinishedAt, &r.Status, &r.ExitReason); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
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
