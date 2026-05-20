// Apteva Simulator v0.1 — iOS + Android sims, controllable from Code.
//
// Architecture: one sidecar with two backends gated by host capability.
// Android needs the Android SDK (adb, emulator, gradle, JDK); iOS needs
// macOS with Xcode and idb_companion. sims_capabilities reports
// availability + hints when something is missing — sims_run short-
// circuits with host_unsupported when the requested platform isn't
// usable on the current host.
//
// The Code app calls sims_run as the entry point — pass a tarball of
// the repo's source plus framework=android|ios, get back a sim_id and
// a stream_url. The browser opens the WS at stream_url to see the
// device live and forward input.
//
// Disk layout (per-install data dir):
//
//   /data/simulator.db              app DB
//   /data/artifacts/<sha>.apk       built APKs (android)
//   /data/artifacts/<sha>.app/      built .app bundles (ios)
//   /data/sim-logs/<sim_run_id>.log per-run build/install/launch log
//   /data/boot-logs/<sim_id>.log    per-sim boot log (emulator stdout)
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────
//
// Mirrors apteva.yaml at the app root. The on-disk file is what the
// platform's source-installer registers with on first install; the
// embedded copy here is what the running sidecar validates against
// at boot via sdk.Run. Keep them in lockstep — manifest_test.go
// catches drift in the tool list, but the rest is on us.

const manifestYAML = `schema: apteva-app/v1
name: simulator
display_name: Apteva Simulator
version: 0.1.0
description: |
  iOS and Android simulators on demand. Boot a device, build a repo's
  source into an artifact, install + launch on a headless emulator or
  Simulator, stream the screen back to the browser, relay touch + key
  input. Called by the Code app on repos_dev_start for mobile repos;
  usable standalone for any flow that produces a mobile artifact.
author: Apteva
tags: [mobile, simulator, emulator, ios, android, dev]
scopes: [project]
min_apteva_version: "0.10.0"
requires:
  permissions:
    - db.write.app
    - platform.apps.call
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: sims_capabilities, description: "Report host-level support for android + ios." }
    - { name: sims_list,         description: "List sims known to this project." }
    - { name: sims_boot,         description: "Boot an emulator (android) or Simulator (ios). Auto-creates the device if none matches." }
    - { name: sims_shutdown,     description: "Shut down a sim. Idempotent." }
    - { name: sims_build,        description: "Build a repo source archive into an APK or .app bundle." }
    - { name: sims_install,      description: "Install a previously-built artifact onto a booted sim." }
    - { name: sims_launch,       description: "Launch an installed bundle on a booted sim." }
    - { name: sims_run,          description: "Composite — boot, build, install, launch, mint stream URL." }
    - { name: sims_input,        description: "Forward a tap/swipe/key/text event to a booted sim." }
    - { name: sims_logs,         description: "Tail device console (logcat / idb log)." }
    - { name: sims_screenshot,   description: "One-off PNG capture from a booted sim." }
    - { name: sims_stream_url,   description: "Mint a short-lived WebSocket URL for live streaming + input." }
  ui_panels:
    - { slot: project.page, label: "Simulator", icon: smartphone, entry: /ui/SimulatorPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/simulator
  port: 8090
  health_check: /health
db:
  driver: sqlite
  path: /data/simulator.db
  migrations: migrations/
config_schema:
  - { name: android_image,         type: text, label: "Android system image",   description: "SDK Manager-style identifier used by auto-created AVDs.", default: "system-images;android-34;google_apis;x86_64" }
  - { name: android_device_type,   type: text, label: "Android device profile", description: "avdmanager device profile name.", default: "pixel_6" }
  - { name: emulator_extra_args,   type: text, label: "Extra emulator args",    description: "Appended verbatim to every emulator boot.", default: "-no-window -no-audio -no-snapshot-save" }
  - { name: gradle_extra_args,     type: text, label: "Extra gradle args",      description: "Appended to every gradle build.", default: "" }
  - { name: ios_runtime,           type: text, label: "Default iOS runtime",    description: "simctl runtime id (e.g. iOS-17-5). Empty = newest installed.", default: "" }
  - { name: ios_device_type,       type: text, label: "Default iOS device",     description: "simctl device-type id.", default: "iPhone-15-Pro" }
  - { name: xcodebuild_extra_args, type: text, label: "Extra xcodebuild args",  description: "Appended to every xcodebuild invocation.", default: "" }
  - { name: idb_companion_path,    type: text, label: "idb_companion path",     description: "Override the PATH lookup. Empty = use PATH.", default: "" }
  - { name: max_concurrent_sims,   type: text, label: "Max booted sims",        description: "Global cap across android + ios.", default: "2" }
  - { name: stream_codec,          type: text, label: "Stream codec",           description: "h264 is the only supported value in v0.1.", default: "h264" }
upgrade_policy: auto-patch
`

// ─── App ────────────────────────────────────────────────────────────

type App struct {
	dataDir      string
	artifactsDir string
	simLogsDir   string
	bootLogsDir  string
	sup          *simSupervisor
	// appCtx is stashed at OnMount so the NoAuth /stream/ WebSocket
	// route — which the SDK invokes without a per-call AppCtx — can
	// reach the DB + platform client.
	appCtx *sdk.AppCtx
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
		return errors.New("simulator requires a db block")
	}
	dataDir := ctx.DataDir()
	if dataDir == "" {
		// Older platforms without APTEVA_DATA_DIR — let the host opt
		// us into a custom path via env, then fall back to /data which
		// will only work in container deployments.
		if env := os.Getenv("SIMULATOR_DATA_DIR"); env != "" {
			dataDir = env
		} else {
			dataDir = "/data"
		}
	}
	a.dataDir = dataDir
	a.artifactsDir = filepath.Join(dataDir, "artifacts")
	a.simLogsDir = filepath.Join(dataDir, "sim-logs")
	a.bootLogsDir = filepath.Join(dataDir, "boot-logs")
	for _, d := range []string{a.artifactsDir, a.simLogsDir, a.bootLogsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	a.appCtx = ctx
	a.sup = newSimSupervisor(a, dataDir)
	if err := a.sup.reconcileOrphans(ctx); err != nil {
		ctx.Logger().Warn("sim orphan reconcile failed", "err", err)
	}

	ctx.Logger().Info("simulator mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"data_dir", dataDir,
		"artifacts_dir", a.artifactsDir)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.sup != nil {
		a.sup.stopAll()
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// HTTPRoutes — sidecar HTTP surface. /api/sims/* are the read endpoints
// the standalone SimulatorPanel.mjs hits; /stream/<sim_id> is the
// WebSocket the live-stream client opens; /artifacts/<token> is the
// signed artifact download reserved for future cross-host installs.
//
// Both /stream/ and /artifacts/ are NoAuth because they're already
// gated by short-lived tokens (sims_stream_url mints the ws_token; the
// artifact download path is a future hook, no handler yet).
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/health", Handler: a.handleHealth},
		// Live screen stream. NoAuth because the ws_token query param
		// minted by sims_stream_url is the bearer; handleStream
		// validates it before upgrading. See stream.go.
		{Pattern: "/stream/", Handler: a.handleStream, NoAuth: true},
		// Standalone-panel read/action endpoints. See handlers.go.
		{Pattern: "/api/capabilities", Handler: a.handleCapabilities},
		{Pattern: "/api/sims", Handler: a.handleSimsList},
		{Pattern: "/api/sims/boot", Handler: a.handleSimsBootHTTP},
		{Pattern: "/api/sims/", Handler: a.handleSimItem},
	}
}

// MCPTools — registered in tools.go. The split keeps main.go focused
// on boot/lifecycle and concentrates schema declarations next to the
// handlers that use them.

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true,"app":"simulator","version":"0.1.0"}`))
}

func main() { sdk.Run(&App{}) }
