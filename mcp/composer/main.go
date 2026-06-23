// Composer v0.3.0 - multi-clip video compositions rendered locally (or
// on a render host via instances). Asset sources accept storage:N /
// mediastudio:N / https URLs, plus AI asset specs materialized via
// Media Studio; output lands back in storage (or the sidecar's local
// cache when storage is unbound).
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
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: composer
display_name: Composer
version: 0.3.33
description: |
  Multi-clip video compositions with a structured timeline panel,
  universal generated-asset clip editing, first-class AI avatar clips,
  AI music soundtrack quick-add, timed AI audio clips, audio-only renders,
  and AI-backed clip/soundtrack sources generated through Media Studio.
  Composer shows audio-only edits as visible audio timelines, lists
  compositions by active project without blocking on render metadata, probes
  generated Storage assets itself for actual duration, reads local Storage
  blobs directly for local ffmpeg renders when available, and keeps signed
  URLs for remote renderers. The panel uses valid block-level timeline
  markup plus explicit inline pixel layout for lanes, clips, gaps, and the
  playhead so short clips and silence clips render consistently even in
  narrow app frames and production host CSS. AI image clips carry explicit
  size metadata so portrait/landscape stills are generated at the intended
  composition aspect through Media Studio.
  Renders locally via ffmpeg, on a render host via instances, or against a
  bound render_executor integration.
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
    - { name: media-studio, version: ">=0.10.14", optional: true }
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
    - { name: asset_search }
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
config_schema:
  - name: render_host_id
    type: select_from_app
    app: instances
    discovery:
      route: /api/instances
      response_path: instances
      value_field: id
      label_field: name
    fallback: text
    default: "0"
    label: Remote render host
    description: |
      Pick a host from the Instances app inventory to offload Composer
      ffmpeg renders. 0 means local sidecar rendering. Remote renders
      download signed input URLs on the selected host and upload the
      finished composition back to Storage.
  - name: ffmpeg_path
    type: text
    default: "ffmpeg"
    label: ffmpeg binary
    description: PATH lookup by default for local renders. Remote renders
      bootstrap a static ffmpeg on the selected host when needed.
  - name: ffprobe_path
    type: text
    default: "ffprobe"
    label: ffprobe binary
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
		"render_host_id", renderHostID(ctx),
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
	if globalCtx != nil {
		if v := strings.TrimSpace(globalCtx.Config().Get("ffmpeg_path")); v != "" {
			return v
		}
	}
	if v := os.Getenv("FFMPEG_PATH"); v != "" {
		return v
	}
	return "ffmpeg"
}

// ffprobePath mirrors ffmpegPath for the probe utility.
func ffprobePath() string {
	if globalCtx != nil {
		if v := strings.TrimSpace(globalCtx.Config().Get("ffprobe_path")); v != "" {
			return v
		}
	}
	if v := os.Getenv("FFPROBE_PATH"); v != "" {
		return v
	}
	return "ffprobe"
}

// renderHostID reads the optional install-config field. When > 0,
// renders SSH-execute on that instance via the `instances` app
// instead of running locally.
func renderHostID(ctx *sdk.AppCtx) int64 {
	v := ""
	if ctx != nil {
		v = strings.TrimSpace(ctx.Config().Get("render_host_id"))
	}
	if v == "" {
		v = strings.TrimSpace(os.Getenv("RENDER_HOST_ID"))
	}
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

func boolArg(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func strArrayArg(m map[string]any, key string) []string {
	out := []string{}
	switch arr := m[key].(type) {
	case []any:
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, arr...)
	}
	return out
}

// quiet "imported and not used"
var _ = sql.Drivers
