// Composer v0.1 — multi-clip video compositions rendered locally (or
// on a render host via instances). Asset sources accept storage:N /
// mediastudio:N / https URLs; output lands back in storage (or the
// sidecar's local cache when storage is unbound).
//
// Architecture:
//   - compositions.go     canonical Edit JSON + validator
//   - executor.go         Executor interface + selectExecutor ladder
//   - exec_local.go       bundled ffmpeg + filter_complex translator
//   - exec_remote.go      SSH-exec on a host managed by `instances`
//   - tools.go            MCP tool surface
//   - dispatch.go         HTTP routes + render orchestration
//   - cache.go            local fallback when storage is unbound
package main

import (
	"database/sql"
	"errors"
	"os"
	"strconv"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: composer
display_name: Composer
version: 0.1.0
description: |
  Multi-clip video compositions. Renders locally via ffmpeg, on a
  render host via instances, or against a bound render_executor
  integration.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.apps.call
  apps:
    - { name: storage, version: ">=0.9.0" }
    - { name: instances, version: ">=0.2.0", optional: true }
    - { name: media-studio, version: ">=0.5.0", optional: true }
  integrations:
    - role: render_executor
      kind: integration
      compatible_slugs: [shotstack, creatomate, json2video]
      capabilities: [video.compose]
      tools: { video.compose: queue_render }
      required: false
      label: "Render backend (optional)"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: composition_create }
    - { name: composition_update }
    - { name: composition_get }
    - { name: composition_list }
    - { name: composition_delete }
    - { name: composition_render }
    - { name: render_status }
    - { name: asset_inspect }
  ui_panels:
    - slot: project.page
      label: Composer
      icon: film
      entry: /ui/ComposerPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/composer
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/composer.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("composer requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("composer mounted",
		"ffmpeg_path", ffmpegPath(),
		"render_host_id", renderHostID(),
	)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// --- install-config helpers --------------------------------------

// ffmpegPath returns the executable name/path to invoke. Defaults to
// "ffmpeg" — relies on $PATH containing the bundled binary in
// containerized deploys and the system one in local dev.
func ffmpegPath() string {
	if v := os.Getenv("FFMPEG_PATH"); v != "" {
		return v
	}
	return "ffmpeg"
}

// ffprobePath mirrors ffmpegPath for the probe utility.
func ffprobePath() string {
	if v := os.Getenv("FFPROBE_PATH"); v != "" {
		return v
	}
	return "ffprobe"
}

// renderHostID reads the optional install-config field. When > 0,
// renders SSH-execute on that instance via the `instances` app
// instead of running locally.
func renderHostID() int64 {
	v := os.Getenv("RENDER_HOST_ID")
	if v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// --- generic arg helpers (mirror media-studio's set) -------------

func strArg(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}

func int64Arg(m map[string]any, key string, def int64) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return def
}

// quiet "imported and not used"
var _ = sql.Drivers
