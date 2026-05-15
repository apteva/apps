// Apteva Deploy v0.1 — local-first builds and runtime supervision.
//
// Disk layout:
//   /data/deploy.db                       metadata (deployments, builds, releases)
//   /data/builds/<build_id>/src/          unpacked source for the build
//   /data/builds/<build_id>/dist/         build output (binary or static files)
//   /data/builds/<build_id>/build.log     captured build stdout/stderr
//   /data/releases/<release_id>/runtime.log  child-process stdout/stderr
//
// Architecture:
//   - SourceFetcher  → unpacks Deployment.Source into /data/builds/<id>/src/
//   - Builder        → runs framework toolchain → /data/builds/<id>/dist/
//   - Runtime        → spawns + supervises the live release process
//   - SupervisorRegistry → in-memory map of running cmds for stop/restart
//
// Future runtimes (DockerRuntime, SSHRuntime) plug in behind the
// Runtime interface — no schema or surface changes required.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: deploy
display_name: Deploy
version: 0.7.0
description: Local-first builds and runtime supervision for Apteva projects.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  integrations:
    - role: code
      kind: app
      required: true
      compatible_app_names: [code]
      label: Code app
      hint: Install the Code app to host repositories the Deploy app builds.
    - role: domains
      kind: app
      required: false
      compatible_app_names: [domains]
      label: Domains app
      hint: Install the Domains app to attach a custom domain to a deployment.
    - role: certs
      kind: app
      required: false
      compatible_app_names: [certs]
      label: Certs app
      hint: Install the Certs app to auto-issue Let's Encrypt certs on attach.
    - role: routes
      kind: app
      required: false
      compatible_app_names: [routes]
      label: Routes app
      hint: Install the Routes app to publish deployments at public hostnames via the platform's host router.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: deploy_init,    description: "Bind a source to a new deployment." }
    - { name: deploy_list,    description: "List deployments in this project." }
    - { name: deploy_get,     description: "Full deployment detail + builds + current release." }
    - { name: deploy_build,   description: "Fetch source, run the framework build; returns build_id." }
    - { name: deploy_release, description: "Promote a build to live." }
    - { name: deploy_status,  description: "Current build + release state, URL, last 10 builds." }
    - { name: deploy_logs,    description: "Tail build or runtime logs." }
    - { name: deploy_stop,    description: "Stop the live release." }
    - { name: deploy_destroy, description: "Stop, drop, delete artifacts." }
    - { name: deploy_attach_domain, description: "Attach an FQDN to a deployment via the Domains app." }
    - { name: deploy_detach_domain, description: "Clear a deployment's domain link." }
    - { name: deploy_list_routes, description: "Server-side: live deployments as a route table for the host-based proxy. Polled by apteva-server's host-router; not for agents." }
  ui_panels:
    - { slot: project.page, label: "Deploy", icon: rocket, entry: /ui/DeployPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/deploy
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/deploy.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct {
	dataDir   string
	runtime   Runtime
	registry  *SupervisorRegistry

	cfg       sourceConfig

	portRangeStart int
	portRangeEnd   int
	maxBuilds      int

	buildSem     chan struct{} // throttle concurrent builds
	watchdogStop chan struct{} // closed on unmount; pid-owns-port poller
}

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
		return errors.New("deploy app requires a db block")
	}
	globalCtx = ctx

	// Resolution order:
	//   1. DEPLOY_DATA_DIR — explicit operator override.
	//   2. ctx.DataDir() — preferred; the platform's writable per-install
	//      dir (also where AppDB lives). Works on every host the SDK
	//      supports, container or not.
	//   3. "/data" — legacy container default. Reached only when running
	//      against an old platform that hasn't been upgraded; mkdir will
	//      fail with a clear error elsewhere if /data isn't writable.
	if env := os.Getenv("DEPLOY_DATA_DIR"); env != "" {
		a.dataDir = env
	} else if dd := ctx.DataDir(); dd != "" {
		a.dataDir = dd
	} else {
		a.dataDir = "/data"
	}
	for _, sub := range []string{"builds", "releases"} {
		if err := os.MkdirAll(filepath.Join(a.dataDir, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	a.cfg = sourceConfig{
		ProjectID: os.Getenv("APTEVA_PROJECT_ID"),
	}
	// Port range resolution: env (per-instance override) > config block
	// (manifest knob) > built-in default. Two apteva-servers on the
	// same host must use disjoint ranges to avoid cross-instance port
	// collisions — DEPLOY_RELEASE_PORT_RANGE_START/END is the operator
	// knob for that. Mirrors the code app's CODE_DEV_PORT_RANGE_*.
	a.portRangeStart = atoiOr(os.Getenv("DEPLOY_RELEASE_PORT_RANGE_START"), atoiOr(configOr(ctx, "port_range_start", "7100"), 7100))
	a.portRangeEnd = atoiOr(os.Getenv("DEPLOY_RELEASE_PORT_RANGE_END"), atoiOr(configOr(ctx, "port_range_end", "7999"), 7999))
	a.maxBuilds = atoiOr(configOr(ctx, "max_build_concurrency", "2"), 2)
	if a.maxBuilds < 1 {
		a.maxBuilds = 1
	}
	a.buildSem = make(chan struct{}, a.maxBuilds)
	a.registry = NewSupervisorRegistry()
	a.runtime = NewLocalRuntime(a.dataDir, a)

	ctx.Logger().Info("deploy mounted",
		"data_dir", a.dataDir,
		"port_range", fmt.Sprintf("%d-%d", a.portRangeStart, a.portRangeEnd),
		"max_build_concurrency", a.maxBuilds,
	)

	// Releases the DB still thinks are "live"/"starting" were spawned
	// by a previous instance of this sidecar. Re-adopt the ones whose
	// processes survived (the common case across an app upgrade); mark
	// the rest stopped so the panel reflects reality.
	if err := a.reconcileReleases(); err != nil {
		ctx.Logger().Warn("release reconciliation failed", "err", err)
	}

	// Watchdog promotes slow-bind "starting" releases and demotes
	// "live" releases whose port owner changed (cross-instance port
	// theft). Tick interval is 60s; pidOwnsPort is a no-op true on
	// non-Linux, so this is harmless on dev hosts.
	a.watchdogStop = make(chan struct{})
	go a.runWatchdog(a.watchdogStop)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	// Deliberately do NOT stop supervised releases here. They're
	// spawned in their own process groups and aren't signalled when
	// this sidecar exits, so they outlive an app upgrade; the next
	// OnMount re-adopts them via reconcileReleases. Killing them here
	// would take every deployment down on every upgrade.
	//
	// Caveat: "static" framework releases run in-process (no child
	// process), so they genuinely die with the sidecar and need a
	// re-release to come back — reconcileReleases marks those stopped.
	if a.watchdogStop != nil {
		close(a.watchdogStop)
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// reconcileReleases runs on boot. For every release the DB still
// considers running, try to re-adopt the underlying process: if it
// survived (an app upgrade left it running in its own process group),
// pull it back into the registry so stop/destroy/route still work.
// If the process is gone, mark the release stopped.
func (a *App) reconcileReleases() error {
	rows, err := dbListLiveReleases(globalCtx.AppDB())
	if err != nil {
		return err
	}
	for _, r := range rows {
		rr, adoptErr := a.runtime.Adopt(r.ID, r.PID, r.Port)
		if adoptErr != nil {
			// Genuine orphan — the process didn't survive the restart.
			_ = dbUpdateRelease(globalCtx.AppDB(), r.ID, map[string]any{
				"status":     "stopped",
				"stopped_at": nowUTC(),
				"error":      "supervisor restarted; process did not survive",
			})
			_ = dbReleasePortLease(globalCtx.AppDB(), r.Port)
			_ = dbAppendReleaseEvent(globalCtx.AppDB(), r.ID, "stop", `{"reason":"orphan_not_alive"}`)
			continue
		}
		a.registry.Put(rr)
		// Mark "starting" — the watchdog (next tick) will promote to
		// "live" only after pidOwnsPort confirms the recorded pid is
		// still the one holding the port. The old code unconditionally
		// stamped "live" here, which is exactly how an adopted-orphan
		// scenario poisoned a domain on prod.
		_ = dbUpdateRelease(globalCtx.AppDB(), r.ID, map[string]any{"status": "starting"})
		_ = dbAppendReleaseEvent(globalCtx.AppDB(), r.ID, "adopted", fmt.Sprintf(`{"pid":%d,"port":%d}`, r.PID, r.Port))
		// Run an immediate probe so a healthy adopt doesn't sit in
		// "starting" for up to 60s waiting for the watchdog.
		go a.probeReady(r.ID, r.PID, r.Port, 5*time.Second)
		globalCtx.Logger().Info("re-adopted release", "release_id", r.ID, "pid", r.PID, "port", r.Port)
	}
	return nil
}

// ─── Routes ────────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/deployments", Handler: a.handleDeploymentsCollection},
		{Pattern: "/api/deployments/", Handler: a.handleDeploymentItem},
		{Pattern: "/api/builds/", Handler: a.handleBuildItem},
		{Pattern: "/api/releases/", Handler: a.handleReleaseItem},
		{Pattern: "/api/_meta", Handler: a.handleMeta},
	}
}

// handleMeta exposes whether the optional Domains and Certs apps are
// installed + registered domains and per-deployment cert status. The
// panel calls this once per load (and on relevant events) so the UI
// never has to talk to other apps directly.
func (a *App) handleMeta(w http.ResponseWriter, r *http.Request) {
	domAvail := a.domainsAvailable(globalCtx)
	certsOn := a.certsAvailable(globalCtx)
	// Project context is needed when Domains/Certs are global-scoped.
	// Soft failure: if it's missing we still report availability, just
	// with empty domains/certs lists rather than 400ing the panel.
	projectID, _ := resolveProjectFromRequest(r)
	out := map[string]any{
		"domains_available": domAvail,
		"certs_available":   certsOn,
		"domains":           []any{},
		"public_host":       configOr(globalCtx, "public_host", ""),
		"certs":             map[string]any{}, // fqdn → {status, expires_at, error}
	}
	if domAvail {
		var resp struct {
			Domains []struct {
				Name string `json:"name"`
			} `json:"domains"`
		}
		if err := callDomainsTool(globalCtx, projectID, "domain_list", map[string]any{}, &resp); err == nil {
			names := make([]map[string]any, 0, len(resp.Domains))
			for _, d := range resp.Domains {
				names = append(names, map[string]any{"name": d.Name})
			}
			out["domains"] = names
		}
	}
	if certsOn {
		// One cert_list call is cheaper than one cert_get per
		// deployment with a domain; fold into a {fqdn → status} map.
		var resp struct {
			Certs []struct {
				FQDN      string `json:"fqdn"`
				Status    string `json:"status"`
				ExpiresAt string `json:"expires_at,omitempty"`
				Error     string `json:"error,omitempty"`
			} `json:"certs"`
		}
		if err := callCertsTool(globalCtx, projectID, "cert_list", map[string]any{}, &resp); err == nil {
			byFQDN := make(map[string]any, len(resp.Certs))
			for _, c := range resp.Certs {
				byFQDN[c.FQDN] = map[string]any{
					"status":     c.Status,
					"expires_at": c.ExpiresAt,
					"error":      c.Error,
				}
			}
			out["certs"] = byFQDN
		}
	}
	httpJSON(w, out)
}

func main() {
	app := &App{}
	wrapped := wrapApp{app: app}
	sdk.Run(&wrapped)
}

// ─── wrapApp shim ─────────────────────────────────────────────────
//
// Mirrors the tasks app: capture *AppCtx in OnMount before HTTP
// routes start serving. Same trick — superseded once the SDK threads
// ctx through HandlerFunc directly.

type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest             { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error      { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error      { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route            { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool               { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory     { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker              { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler  { return w.app.EventHandlers() }

// ─── Project resolution (mirrors code/storage pattern) ────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string")
}

// ─── Build & release coordination (called from tools + handlers) ──
//
// runBuild does the entire build lifecycle: fetch source → builder →
// persist artifact path. Returns when the build finishes (success or
// failure). The caller decides whether to release immediately.

func (a *App) runBuild(d *Deployment) (*Build, error) {
	// Pick framework.
	fw := d.Framework

	// First persist a pending build row so the panel can render it
	// while source fetch happens.
	build, err := dbCreateBuild(globalCtx.AppDB(), d.ID, fw, d.BuildCmd)
	if err != nil {
		return nil, err
	}

	// Concurrency throttle.
	a.buildSem <- struct{}{}
	defer func() { <-a.buildSem }()

	emit("deploy.build.started", map[string]any{"deployment_id": d.ID, "build_id": build.ID})

	buildDir := filepath.Join(a.dataDir, "builds", strconv.FormatInt(build.ID, 10))
	srcDir := filepath.Join(buildDir, "src")
	distDir := filepath.Join(buildDir, "dist")
	logPath := filepath.Join(buildDir, "build.log")
	for _, p := range []string{srcDir, distDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return a.failBuild(build, "mkdir: "+err.Error()), nil
		}
	}
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return a.failBuild(build, "open log: "+err.Error()), nil
	}
	defer logF.Close()
	fmt.Fprintf(logF, "=== build %d for deployment %d (%s) at %s ===\n", build.ID, d.ID, d.Name, nowUTC())
	startedAt := nowUTC()
	_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{
		"status":     "running",
		"started_at": startedAt,
		"log_path":   logPath,
	})

	startTime := time.Now()

	// Fetch source.
	cfg := a.cfg
	cfg.ProjectID = d.ProjectID
	if err := fetchSource(globalCtx, d, srcDir, cfg); err != nil {
		fmt.Fprintf(logF, "fetch source failed: %v\n", err)
		return a.failBuild(build, "fetch source: "+err.Error()), nil
	}
	sha, err := hashTree(srcDir)
	if err != nil {
		// Non-fatal — keep going without short-circuit ability.
		sha = ""
	}

	// Detect framework if needed.
	if fw == "" {
		fw = detectFramework(srcDir)
		if fw == "" {
			return a.failBuild(build, "framework not detected; set framework on the deployment"), nil
		}
		fmt.Fprintf(logF, "auto-detected framework: %s\n", fw)
	}

	builder, err := builderFor(fw)
	if err != nil {
		return a.failBuild(build, err.Error()), nil
	}

	entrypoint, err := builder.Build(srcDir, distDir, BuildOverrides{
		BuildCmd: d.BuildCmd,
		StartCmd: d.StartCmd,
		Env:      parseEnvJSON(d.EnvJSON),
	}, logF)
	if err != nil {
		return a.failBuild(build, err.Error()), nil
	}

	durMs := time.Since(startTime).Milliseconds()
	size, _ := dirSize(distDir)
	fmt.Fprintf(logF, "=== build succeeded in %dms, artifact=%s, entrypoint=%q ===\n", durMs, distDir, entrypoint)

	_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{
		"status":        "succeeded",
		"finished_at":   nowUTC(),
		"duration_ms":   durMs,
		"exit_code":     0,
		"source_sha":    sha,
		"artifact_path": distDir,
		"artifact_size": size,
		// fw may have been auto-detected after the row was created
		// with the deployment's empty framework field — without
		// persisting it here, runRelease's resolveCommand falls into
		// the default case and fails with "no default start command".
		"framework": fw,
	})
	// Stash entrypoint via the build's (framework-chosen) BuildCmd
	// metadata is not enough — the runtime needs entrypoint at
	// release time. Fastest path: re-derive at release time from
	// the artifact (binary at dist/app, or empty for static).
	emit("deploy.build.succeeded", map[string]any{
		"deployment_id": d.ID, "build_id": build.ID,
		"framework": fw, "duration_ms": durMs, "size": size,
	})
	return dbGetBuild(globalCtx.AppDB(), build.ID)
}

func (a *App) failBuild(b *Build, msg string) *Build {
	_ = dbUpdateBuild(globalCtx.AppDB(), b.ID, map[string]any{
		"status":      "failed",
		"finished_at": nowUTC(),
		"error":       msg,
	})
	emit("deploy.build.failed", map[string]any{
		"deployment_id": b.DeploymentID, "build_id": b.ID, "error": msg,
	})
	out, _ := dbGetBuild(globalCtx.AppDB(), b.ID)
	return out
}

// runRelease starts a supervised process for the build and atomically
// stops the deployment's previous current release.
func (a *App) runRelease(d *Deployment, b *Build) (*Release, error) {
	if b.Status != "succeeded" {
		return nil, fmt.Errorf("build %d not succeeded (status=%s)", b.ID, b.Status)
	}

	// Stop the previous live release first so we don't double-bind.
	if d.CurrentReleaseID != nil {
		if rr := a.registry.Get(*d.CurrentReleaseID); rr != nil {
			_ = a.runtime.Stop(rr)
			_ = dbReleasePortLease(globalCtx.AppDB(), rr.Port)
			a.registry.Delete(rr.ReleaseID)
		}
		_ = dbUpdateRelease(globalCtx.AppDB(), *d.CurrentReleaseID, map[string]any{
			"status":     "stopped",
			"stopped_at": nowUTC(),
		})
		// Unregister the route while we're between releases. The new
		// release only re-registers once probeReady confirms it owns
		// its port. Without this, during the rebuild gap the route
		// keeps pointing at a dead (and potentially-recycled) port —
		// the smaller-blast-radius cousin of the false-live incident.
		// promoteToLive will register the fresh target when the new
		// release verifies.
		if d.Domain != "" {
			unregisterRouteForDeployment(globalCtx, a, d.Domain)
		}
	}

	rel, err := dbCreateRelease(globalCtx.AppDB(), d.ID, b.ID)
	if err != nil {
		return nil, err
	}

	// Allocate port.
	port, err := a.allocatePort(d.PortHint, rel.ID)
	if err != nil {
		_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
			"status": "failed", "error": err.Error(),
		})
		return nil, err
	}

	// Resolve entrypoint from artifact layout.
	entrypoint := ""
	if b.Framework == "go" {
		entrypoint = filepath.Join(b.ArtifactPath, "app")
	}

	envMap := parseEnvJSON(d.EnvJSON)
	spec := ReleaseSpec{
		ReleaseID:    rel.ID,
		DeploymentID: d.ID,
		Name:         d.Name,
		Framework:    b.Framework,
		ArtifactDir:  b.ArtifactPath,
		Entrypoint:   entrypoint,
		StartCmd:     d.StartCmd,
		Port:         port,
		Env:          envMap,
	}

	rr, err := a.runtime.Start(spec)
	if err != nil {
		_ = dbReleasePortLease(globalCtx.AppDB(), port)
		_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
			"status": "failed", "error": err.Error(),
		})
		emit("deploy.release.failed", map[string]any{
			"deployment_id": d.ID, "release_id": rel.ID, "error": err.Error(),
		})
		return nil, err
	}

	a.registry.Put(rr)
	// Status starts at "starting" — only probeReady's pidOwnsPort
	// success flips it to "live", at which point current_release is
	// set and the route is registered. This prevents the false-live
	// + wrong-target-route class of bug: a release that crashes
	// during startup, or whose port gets stolen, never reaches the
	// pointer or the host router. Today's "5s probe, then mark live
	// no matter what" behavior is what poisoned marcoschwartz.com.
	_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
		"status":     "starting",
		"port":       port,
		"pid":        rr.PID,
		"started_at": nowUTC(),
		"log_path":   a.runtime.LogPath(rel.ID),
	})
	_ = dbAppendReleaseEvent(globalCtx.AppDB(), rel.ID, "start", "{}")
	emit("deploy.release.started", map[string]any{
		"deployment_id": d.ID, "release_id": rel.ID, "port": port, "pid": rr.PID,
	})
	return dbGetRelease(globalCtx.AppDB(), rel.ID)
}

// markCrashed is called by the runtime when a supervised process
// exits unexpectedly.
func (a *App) markCrashed(releaseID int64, cause error) {
	_ = dbUpdateRelease(globalCtx.AppDB(), releaseID, map[string]any{
		"status":     "crashed",
		"stopped_at": nowUTC(),
		"error":      cause.Error(),
	})
	rel, _ := dbGetRelease(globalCtx.AppDB(), releaseID)
	if rel != nil {
		_ = dbReleasePortLease(globalCtx.AppDB(), rel.Port)
		_ = dbAppendReleaseEvent(globalCtx.AppDB(), releaseID, "crash", fmt.Sprintf(`{"error":%q}`, cause.Error()))
		emit("deploy.release.crashed", map[string]any{
			"deployment_id": rel.DeploymentID, "release_id": releaseID, "error": cause.Error(),
		})
	}
	a.registry.Delete(releaseID)
}

func (a *App) markStopped(releaseID int64) {
	_ = dbUpdateRelease(globalCtx.AppDB(), releaseID, map[string]any{
		"status":     "stopped",
		"stopped_at": nowUTC(),
	})
	rel, _ := dbGetRelease(globalCtx.AppDB(), releaseID)
	if rel != nil {
		_ = dbReleasePortLease(globalCtx.AppDB(), rel.Port)
		_ = dbAppendReleaseEvent(globalCtx.AppDB(), releaseID, "stop", "{}")
		emit("deploy.release.stopped", map[string]any{
			"deployment_id": rel.DeploymentID, "release_id": releaseID,
		})
	}
	a.registry.Delete(releaseID)
}

// probeReady waits up to timeout for `pid` to be the process holding
// a LISTEN socket on `port`. On success it promotes the release to
// live (setting current_release_id, registering the route). On
// timeout it leaves the release in "starting" — slow-bootstrap apps
// catch up on the next watchdog tick, never-bind apps stay starting
// until the supervisor reports a crash. The release is NEVER marked
// "live" without pid-owns-port verification.
//
// Authoritative test is pidOwnsPort (procfs on linux, always-true
// stub elsewhere). The TCP dial used to be the only check; relying
// on "something answers" is what let a co-located apteva instance's
// process poison marcoschwartz.com.
func (a *App) probeReady(releaseID int64, pid, port int, timeout time.Duration) {
	// Tests instantiate LocalRuntime directly with a zero-value App
	// and no globalCtx, so probeReady becomes a no-op there. In a
	// real run OnMount always sets globalCtx before runtime.Start can
	// be called.
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pidOwnsPort(pid, port) {
			a.promoteToLive(releaseID, pid, port)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = dbAppendReleaseEvent(globalCtx.AppDB(), releaseID, "health_pending",
		fmt.Sprintf(`{"reason":"port_not_owned_after_%s","pid":%d,"port":%d}`, timeout, pid, port))
}

// promoteToLive flips a "starting" release to "live", sets the
// deployment's current_release pointer, and registers the route. It
// is idempotent — calling it twice for the same release is a no-op
// after the first call, which is what makes the watchdog safe to
// run alongside probeReady.
func (a *App) promoteToLive(releaseID int64, pid, port int) {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return
	}
	rel, err := dbGetRelease(globalCtx.AppDB(), releaseID)
	if err != nil || rel == nil {
		return
	}
	if rel.Status == "live" {
		// Already promoted; just refresh the health stamp.
		_ = dbUpdateRelease(globalCtx.AppDB(), releaseID, map[string]any{"last_health_at": nowUTC()})
		return
	}
	if rel.Status != "starting" {
		// Stopped/crashed/failed releases can't be promoted; the
		// supervisor path that set the terminal state wins.
		return
	}
	_ = dbUpdateRelease(globalCtx.AppDB(), releaseID, map[string]any{
		"status":         "live",
		"last_health_at": nowUTC(),
	})
	_ = dbAppendReleaseEvent(globalCtx.AppDB(), releaseID, "health_ok", "{}")
	_ = dbSetCurrentRelease(globalCtx.AppDB(), rel.DeploymentID, &releaseID)
	emit("deploy.release.live", map[string]any{
		"deployment_id": rel.DeploymentID, "release_id": releaseID, "port": port, "pid": pid,
	})
	d, _ := dbGetDeploymentByID(globalCtx.AppDB(), rel.DeploymentID)
	if d != nil && d.Domain != "" {
		registerRouteForDeployment(globalCtx, a, d)
	}
}

// runWatchdog re-checks pidOwnsPort for every release in the
// supervisor registry once per tick. Two jobs:
//
//   - Promote "starting" releases whose process took longer than the
//     5s probe (slow Node/Java boot, anything with heavy init) — they
//     bind eventually, the next tick catches them, the route appears.
//   - Demote "live" releases whose port no longer belongs to the
//     recorded pid: a co-located apteva instance reclaimed the port,
//     or the kernel recycled the pid after a crash + new bind. Mark
//     crashed, release the port lease, unregister the route. This is
//     the watchdog half of the false-live incident class.
//
// Tick interval is 60s — small enough that domain-collision blast
// radius is bounded, large enough that procfs scans don't matter.
// pidOwnsPort is a no-op true on non-Linux, so this is a free tick.
func (a *App) runWatchdog(stop <-chan struct{}) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			a.watchdogTick()
		}
	}
}

func (a *App) watchdogTick() {
	if globalCtx == nil || globalCtx.AppDB() == nil || a.registry == nil {
		return
	}
	for _, rr := range a.registry.All() {
		if rr.PID <= 0 || rr.Port <= 0 {
			continue
		}
		owns := pidOwnsPort(rr.PID, rr.Port)
		rel, _ := dbGetRelease(globalCtx.AppDB(), rr.ReleaseID)
		if rel == nil {
			continue
		}
		switch rel.Status {
		case "starting":
			if owns {
				a.promoteToLive(rr.ReleaseID, rr.PID, rr.Port)
			}
		case "live":
			if !owns {
				a.markPortOwnerChanged(rel)
			}
		}
	}
}

// markPortOwnerChanged tears down a release whose port is no longer
// ours. Differs from a clean stop in that we cannot Stop the
// supervisor — the process the supervisor handle points at may still
// be alive, just no longer bound (and signalling its process group
// could kill an unrelated process if the pid was recycled). Safer to
// release our claim and let the operator restart, plus unregister the
// route so the domain stops resolving to the new owner.
func (a *App) markPortOwnerChanged(rel *Release) {
	_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
		"status":     "crashed",
		"stopped_at": nowUTC(),
		"error":      fmt.Sprintf("port owner changed: pid %d no longer holds :%d", rel.PID, rel.Port),
	})
	_ = dbReleasePortLease(globalCtx.AppDB(), rel.Port)
	_ = dbAppendReleaseEvent(globalCtx.AppDB(), rel.ID, "crash", `{"reason":"port_owner_changed"}`)
	emit("deploy.release.crashed", map[string]any{
		"deployment_id": rel.DeploymentID, "release_id": rel.ID,
		"reason": "port_owner_changed",
	})
	a.registry.Delete(rel.ID)
	d, _ := dbGetDeploymentByID(globalCtx.AppDB(), rel.DeploymentID)
	if d != nil && d.Domain != "" {
		unregisterRouteForDeployment(globalCtx, a, d.Domain)
	}
}

// ─── Port allocator ───────────────────────────────────────────────

var portMu sync.Mutex // serialise probes; the lease table is the durable claim

func (a *App) allocatePort(hint int, releaseID int64) (int, error) {
	portMu.Lock()
	defer portMu.Unlock()

	tried := map[int]bool{}
	candidates := []int{}
	if hint > 0 {
		candidates = append(candidates, hint)
	}
	for p := a.portRangeStart; p <= a.portRangeEnd; p++ {
		if !tried[p] {
			candidates = append(candidates, p)
			tried[p] = true
		}
	}

	held, err := dbHeldPorts(globalCtx.AppDB())
	if err != nil {
		return 0, err
	}
	// Cross-instance check: a co-located apteva-server's process may
	// hold a port we'd otherwise consider free. portFreeForServer's
	// bind-probe catches it too, but only with a TOCTOU window —
	// querying the kernel's listen table first shrinks that window to
	// the time between this read and the supervised process bind.
	// On non-Linux this returns an empty map; we fall back to
	// bind-probe alone, which is fine for single-tenant dev hosts.
	listening := systemListeningPorts()
	for _, p := range candidates {
		if held[p] {
			continue
		}
		if listening[p] {
			continue
		}
		if !portFreeForServer(p) {
			continue
		}
		ok, err := dbAcquirePortLease(globalCtx.AppDB(), p, releaseID)
		if err != nil {
			return 0, err
		}
		if ok {
			return p, nil
		}
	}
	return 0, errors.New("no free port in configured range")
}

// portFreeForServer probes whether a port is genuinely free for an
// HTTP server to bind to. Binding `127.0.0.1:p` alone is not enough:
// many real servers (Next.js, Express, Python http.server, …) bind
// to the wildcards 0.0.0.0 and/or [::], and macOS holds *:7000 and
// *:5000 for AirPlay Receiver — those listeners can coexist with a
// 127.0.0.1-only bind on the same kernel, so the older smoke test
// said "free" and our supervised process then crashed with
// EADDRINUSE.
//
// We test both wildcards. If either bind fails, the port can't be
// trusted, so skip it. SO_REUSEADDR is left at Go's default (off on
// macOS for tcp), so this matches the strictness real servers see.
func portFreeForServer(p int) bool {
	addrs := []string{
		fmt.Sprintf("0.0.0.0:%d", p),
		fmt.Sprintf("[::]:%d", p),
	}
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		_ = ln.Close()
	}
	return true
}

// ─── Misc helpers ─────────────────────────────────────────────────

func configOr(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v := ctx.Config().Get(key); v != "" {
		return v
	}
	return def
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func parseEnvJSON(s string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return out
	}
	for k, v := range raw {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// emit broadcasts an event over the platform bus, if available.
func emit(topic string, data any) {
	if globalCtx == nil {
		return
	}
	globalCtx.Emit(topic, data)
}
