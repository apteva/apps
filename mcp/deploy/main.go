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
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: deploy
display_name: Deploy
version: 0.11.1
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

	// Phantom-route sweep: drop any routes in the routes-app DB that
	// claim to be owned by this deploy install but don't correspond
	// to a still-live deployment + release here. Cleans up stale
	// blocks left behind by pre-v0.10.0 instances (e.g., the
	// marcoschwartz.com Caddy block that survived the incident's
	// release teardown). Best-effort; logs the count.
	if n := a.sweepPhantomRoutes(ctx); n > 0 {
		ctx.Logger().Info("dropped phantom routes", "count", n)
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
			// Genuine orphan — the process didn't survive the restart,
			// or its port was taken by an unrelated process while we
			// were down (pidOwnsPort gate in Adopt rejects that).
			_ = dbUpdateRelease(globalCtx.AppDB(), r.ID, map[string]any{
				"status":     "stopped",
				"stopped_at": nowUTC(),
				"error":      "supervisor restarted; process did not survive",
			})
			_ = dbReleasePortLease(globalCtx.AppDB(), r.Port)
			_ = dbAppendReleaseEvent(globalCtx.AppDB(), r.ID, "stop", `{"reason":"orphan_not_alive"}`)
			// Cascade: a domain attached to this orphaned release still
			// has a route entry. Pre-v0.10.0 this was the *exact* shape
			// of the marcoschwartz.com incident — the route survived
			// the orphan's death and pointed the domain at a port that
			// another instance later bound.
			a.cascadeUnregisterRoute(r.DeploymentID)
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
	// Sub-mode: when invoked with --static-server, this binary is a
	// static file server for one release, not the deploy sidecar.
	// startStatic re-execs os.Args[0] with this flag so static
	// releases share the exact pid+port supervision pattern as
	// bun/node/go releases (and survive deploy.app restarts via
	// v0.7's Adopt). See static_server.go.
	if len(os.Args) > 1 && os.Args[1] == "--static-server" {
		runStaticServer(os.Args[2:])
		return
	}
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

	// Allocate port. Explicit PortHint is sticky-by-design; with no
	// hint, prefer the previous release's port for this deployment so
	// re-releases don't drift through the range (operator may have
	// written Caddy rules / firewall holes / docs against the old
	// port). Falls through to the range scan if that port can't be
	// claimed any more (operator changed something, port now held by
	// another tenant, etc.) — allocator's fail-loud only fires for
	// EXPLICIT hints.
	effectiveHint := d.PortHint
	if effectiveHint == 0 {
		if prev := a.previousReleasePort(d.ID); prev > 0 {
			effectiveHint = prev
		}
	}
	port, err := a.allocatePort(effectiveHint, rel.ID)
	if err != nil && effectiveHint != 0 && d.PortHint == 0 {
		// Implicit hint (previous release's port) lost — quietly
		// fall back to a range scan. Explicit hint failure bubbles
		// up; this branch only catches the convenience path.
		port, err = a.allocatePort(0, rel.ID)
	}
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
	// Defensive: tests can run the runtime without an apteva-server
	// context (no OnMount → no globalCtx, no AppDB). Skipping the DB
	// updates is safe — the test owns the lifecycle and isn't reading
	// status back.
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return
	}
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
		// Cascade: a crashed release's route would otherwise stay live
		// in the routes-app DB pointing at a now-freed port — the next
		// allocator pick lands on it and the wrong site is served.
		a.cascadeUnregisterRoute(rel.DeploymentID)
	}
	a.registry.Delete(releaseID)
}

// stopReleaseAuthoritative is the operator-facing stop. Unlike a
// bare runtime.Stop, this guarantees the port is actually free when
// it returns — by killing the process group of whatever owns the
// port, regardless of whether fleet's in-memory registry knows
// about it.
//
// Fixes the orphan class operators reported: registry.Get(rid) was
// nil (sidecar restart cleared the in-memory map; markStopped from
// a prior call already deleted the entry; etc.), runtime.Stop was a
// no-op, the actual process kept serving while the DB said stopped.
// Re-release then allocated a different port → two processes
// listening, both releases drifting from reality.
//
// Sequence:
//   1. runtime.Stop on the in-memory handle if present (graceful
//      cmd.Wait path with cancel + SIGTERM/SIGKILL escalation).
//   2. Probe the port. If still held, find the pid that owns it
//      (pid-tree-aware now), SIGTERM the whole pgrp, poll until
//      free, escalate to SIGKILL.
//   3. Return only when the port is genuinely free, or after the
//      hard fallback (with an error so the operator sees it).
//
// Stop is now synchronous from the operator's POV.
func (a *App) stopReleaseAuthoritative(rel *Release, grace time.Duration) error {
	if rel == nil {
		return nil
	}
	if rr := a.registry.Get(rel.ID); rr != nil {
		_ = a.runtime.Stop(rr)
	}
	if rel.Port <= 0 {
		return nil
	}
	if portFreeForServer(rel.Port) {
		return nil // happy path — registry stop did its job
	}
	// Port still held → find the pid + kill its process group. The
	// pid we know about (rel.PID) is often a wrapper; pid-by-port
	// gets us the actual owner. Then walk up to its pgrp leader so
	// the whole tree dies (bun wrapper → bun script, npm wrapper →
	// node, etc.).
	pid := findPidListeningOn(rel.Port)
	if pid <= 0 {
		// portFreeForServer says held but nothing in proc — corner
		// case (kernel TIME_WAIT? socket in another namespace?).
		// Treat as success; the next allocator pick will bind-probe.
		return nil
	}
	pgid, _ := syscall.Getpgid(pid)
	if pgid <= 0 {
		pgid = pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if portFreeForServer(rel.Port) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	for i := 0; i < 25; i++ { // 5s after SIGKILL
		if portFreeForServer(rel.Port) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("port %d still bound after SIGKILL (pid %d, pgrp %d)", rel.Port, pid, pgid)
}

func (a *App) markStopped(releaseID int64) {
	// Same defensive skip as markCrashed — see comment there.
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return
	}
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
		a.cascadeUnregisterRoute(rel.DeploymentID)
	}
	a.registry.Delete(releaseID)
}

// cascadeUnregisterRoute removes the routes-app entry for a
// deployment whose only release just transitioned out of "live".
// Without this, the route persists pointing at a stale (and likely
// re-allocatable) port — feeding straight into the cross-bleed bug
// the routes cross-check in allocatePort guards against. Best-effort
// — routes app missing → no-op.
func (a *App) cascadeUnregisterRoute(deploymentID int64) {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return
	}
	d, _ := dbGetDeploymentByID(globalCtx.AppDB(), deploymentID)
	if d == nil || d.Domain == "" {
		return
	}
	unregisterRouteForDeployment(globalCtx, a, d.Domain)
}

// sweepPhantomRoutes runs once on boot. It asks the routes app for
// every route owned by this install + owner_kind=deploy, then drops
// any whose hostname doesn't match a deployment with a live release
// in our DB. Catches:
//
//   - The marcoschwartz.com class of stale block (release destroyed
//     but the route never got cleaned by pre-v0.10.0 code).
//   - Routes that survived an OS-level migration / data restore
//     where the deploy DB came back but the routes DB was newer.
//   - Routes for deployments that were destroyed via deploy_destroy
//     before the cascade-cleanup code existed.
//
// Returns the count of routes dropped. Routes whose owner_kind
// isn't "deploy" — or whose owner_install_id is some other
// install's — are not touched.
func (a *App) sweepPhantomRoutes(ctx *sdk.AppCtx) int {
	if ctx == nil || !a.routesAvailable(ctx) {
		return 0
	}
	var resp struct {
		Routes []struct {
			Hostname       string `json:"hostname"`
			OwnerInstallID int64  `json:"owner_install_id"`
			OwnerKind      string `json:"owner_kind"`
		} `json:"routes"`
	}
	myID := myInstallID()
	if err := callRoutesTool(ctx, "routes_list", map[string]any{"owner_install_id": myID}, &resp); err != nil {
		return 0
	}
	// Live set: deployments under this install whose domain is set AND
	// whose current release is still alive. The route is only legitimate
	// when both halves hold.
	liveDomains := map[string]bool{}
	if globalCtx != nil && globalCtx.AppDB() != nil {
		ds, _ := dbListDeploymentsWithDomain(globalCtx.AppDB())
		for _, d := range ds {
			if d.CurrentReleaseID == nil {
				continue
			}
			rel, _ := dbGetRelease(globalCtx.AppDB(), *d.CurrentReleaseID)
			if rel == nil {
				continue
			}
			if rel.Status == "live" || rel.Status == "starting" {
				liveDomains[d.Domain] = true
			}
		}
	}
	dropped := 0
	for _, r := range resp.Routes {
		if r.OwnerInstallID != myID || r.OwnerKind != "deploy" {
			continue
		}
		if liveDomains[r.Hostname] {
			continue
		}
		if err := callRoutesTool(ctx, "routes_unregister", map[string]any{
			"hostname":         r.Hostname,
			"owner_install_id": myID,
		}, nil); err != nil {
			continue
		}
		emit("deploy.route.phantom_dropped", map[string]any{"hostname": r.Hostname})
		dropped++
	}
	return dropped
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
	a.cascadeUnregisterRoute(rel.DeploymentID)
}

// ─── Port allocator ───────────────────────────────────────────────

// previousReleasePort returns the port of the most recent release for
// deployment d, or 0 if none. Used as an implicit port_hint on
// re-release so a deployment with no explicit PortHint still keeps
// the same port across releases (sticky port without operator
// having to think about it).
func (a *App) previousReleasePort(deploymentID int64) int {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return 0
	}
	rels, err := dbListReleases(globalCtx.AppDB(), deploymentID, 1)
	if err != nil || len(rels) == 0 {
		return 0
	}
	return rels[0].Port
}

var portMu sync.Mutex // serialise probes; the lease table is the durable claim

func (a *App) allocatePort(hint int, releaseID int64) (int, error) {
	portMu.Lock()
	defer portMu.Unlock()

	held, err := dbHeldPorts(globalCtx.AppDB())
	if err != nil {
		return 0, err
	}
	listening := systemListeningPorts()
	targeted := routesTargetedPorts(globalCtx, a)

	// hintConflict spells out exactly why a port can't be taken —
	// silent fallback was the bug we're fixing here. Operators set
	// port_hint deliberately (sticky port across re-releases, Caddy
	// blocks they hand-wrote, allocator skipping the hinted port
	// shouldn't be invisible).
	hintConflict := func(p int) string {
		if held[p] {
			return "another release in this app holds the port lease"
		}
		if listening[p] {
			return "another process on the host is listening on this port (cross-instance bind)"
		}
		if targeted[p] {
			return "the routes app already maps a hostname to this port; allocating would hijack it"
		}
		if !portFreeForServer(p) {
			return "OS bind-probe failed (port in use or restricted)"
		}
		return ""
	}

	// Honor an explicit hint, OR fail loud. Auto-falling-back to a
	// random port in the range was the bug — re-releases would drift
	// off the operator's chosen port whenever an orphan held it,
	// then route configs hand-written for the old port broke.
	if hint > 0 {
		if reason := hintConflict(hint); reason != "" {
			return 0, fmt.Errorf("port_hint %d not available: %s — free it then retry, "+
				"or clear port_hint on the deployment to allow any free port in %d-%d",
				hint, reason, a.portRangeStart, a.portRangeEnd)
		}
		ok, err := dbAcquirePortLease(globalCtx.AppDB(), hint, releaseID)
		if err != nil {
			return 0, err
		}
		if ok {
			return hint, nil
		}
		// Race: lease was claimed between our checks and the INSERT.
		return 0, fmt.Errorf("port_hint %d lost a race to another concurrent release", hint)
	}

	// No hint → scan the configured range as before.
	for p := a.portRangeStart; p <= a.portRangeEnd; p++ {
		if hintConflict(p) != "" {
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
