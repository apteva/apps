// Composer - multi-clip video compositions rendered locally (or on
// Composer's private renderer component). Asset sources accept storage:N /
// mediastudio:N / https URLs, plus AI asset specs materialized via
// Media Studio; output lands back in storage (or the sidecar's local
// cache when storage is unbound).
//
// Architecture:
//   - compositions.go     canonical Edit JSON + validator
//   - executor.go         Executor interface + selectExecutor ladder
//   - exec_local.go       bundled ffmpeg + filter_complex translator
//   - exec_component.go   V1 ffmpeg plans for the renderer component
//   - component.go        renderer component client, durable job records
//   - component_transfer.go job-scoped Storage relay for renderer uploads
//   - tools.go            MCP tool surface
//   - dispatch.go         HTTP routes + render orchestration
//   - cache.go            local fallback when storage is unbound
package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: composer
display_name: Composer
version: 0.11.3
description: |
  v0.11.3 makes browser captures independent of host fonts and reports
  renderer readiness at deploy. The renderer bundles Inter and Go Mono with its
  own fontconfig, so a minimal Linux host no longer renders blank text, and a
  browser job fails explicitly if those fonts cannot be installed. Mount starts
  provisioning Chrome headless shell and Node.js in the background;
  capabilities report browser_status (ready, missing system libraries, fonts)
  before the first render.

  v0.11.0 replaces the Instances SSH render path with Composer's private
  renderer component (apteva.composer-renderer/v1), deployed and placed by
  Server: on the Composer host or on any execution backend, such as an
  Instances host. With execution_mode=component, V1 FFmpeg renders, browser V2
  renders and web-canvas-1 procedures run on the renderer, which provisions
  pinned FFmpeg, Chrome headless shell and Node.js builds, reports progress, and
  uploads results through a job-scoped Storage relay; Composer's platform
  token never leaves the sidecar. Native V2 renders and script procedures stay
  local. Installs that had render_host_id set keep offloading and fail
  explicitly until the renderer is deployed. Composer no longer depends on the
  Instances app.

  v0.10.0 makes web-canvas-1 procedures executable. A procedure is an HTML
  page that draws any time t through setTime(t) (or composer.onTime), reading
  parameters, canvas, timeline, and input asset URLs from window.composer.
  Local scripts and stylesheets are inlined into one document; the renderer
  captures it frame by frame in headless Chrome, and the panel's JSON tab
  previews the identical document in a sandboxed iframe with play/scrub and an
  editable parameters field, so timing changes need no render.
  v0.9.3 fixes procedure_create and procedure_get asset examples to use the
  selected procedure revision's actual target instead of hardcoding clip.
  Composition, audio, and still procedure examples now pass target validation.
  v0.9.2 fixes V1 text rendering for straight apostrophes and actual line
  breaks in both clip text and timed text tracks, locally and remotely.
  Text is escaped across both FFmpeg parsers, with expansion disabled to
  preserve literal percent expressions and backslashes.
  v0.9.1 fixes remote rendering through Instances by invoking Bash explicitly.
  Bash arrays and pipefail now work when Instances executes commands with sh,
  while quoted scripts preserve literal values and propagate failures.
  v0.9.0 adds trusted local procedural clips. Immutable Python,
  Bun/TypeScript, and Go procedures consume named Storage or AI-generated
  inputs, produce validated media artifacts, and become ordinary cached clips.
  Execution uses a shared job/result contract with cancellation, timeouts,
  bounded logs, media probing, and path/output limits. Isolated execution is
  intentionally deferred and local execution can be disabled per installation.
  v0.8.0 adds saved audio, image-video and full-video output presets sharing
  one composition and master, selective generation, durable asset reuse,
  independent previews/history, excerpts and safe artifact adoption.
  v0.7.5 hardens project isolation and remote output validation, protects saved
  drafts with revision checks, and polls existing AI jobs without duplicate
  refresh requests. Save & render persists the current draft first. Split clips,
  zero volume, examples, looping previews, and mobile drawers behave correctly.
  Rendering supports active cancellation, bounded chunk uploads, durable cache
  fallback with Range requests, and silent remote video inputs. V2 holds final
  keyframes, waits for browser images/fonts, and rejects unsupported content.
  Small native elements allocate only their own bounds instead of a full frame.

  Multi-clip video compositions with a structured timeline panel,
  universal generated-asset clip editing, first-class AI avatar clips,
  AI music soundtrack quick-add, timed AI audio clips, audio-only renders,
  and AI-backed clip/soundtrack sources generated through Media Studio.
  Rendering applies opacity, crop, rounded corners, shadows, and camera
  keyframes consistently with preview. Native V2 shapes support gradients,
  borders, ellipses, and structured shadows. Text wraps and fits within padded
  boxes with safe-area diagnostics. FFmpeg/native jobs report measured progress,
  remote shell values preserve special characters, and diagnostics redact
  credentials before they are stored or emitted.
  Composer shows audio-only edits as visible audio timelines, lists
  compositions by active project without blocking on render metadata, probes
  generated Storage assets itself for actual duration, reads local Storage
  blobs directly for local ffmpeg renders when available, and keeps signed
  URLs for remote renderers. The panel uses valid block-level timeline
  markup plus explicit inline pixel layout for lanes, clips, gaps, and the
  playhead so short clips and silence clips render consistently even in
  narrow app frames and production host CSS. AI image clips carry explicit
  size metadata so portrait/landscape stills are generated at the intended
  composition aspect through Media Studio. ElevenLabs AI voice clips default
  to conservative cloned-voice settings unless a clip overrides them, and
  sequential clips sharing a voice/model get ElevenLabs request-id continuity
  when Media Studio returns provider request IDs, falling back to text context.
  The panel lists compositions with lightweight summaries and loads full
  timeline/render details only when a composition is selected, so projects with
  many saved compositions stay responsive. Preview playback is single-owner:
  switching compositions stops the live preview, render media is no longer
  nested inside an opener button, and the full-size render viewer does not
  autoplay over an already playing inline preview. Composer also supports
  first-class overlay/text tracks with rich typography fields, stroke/shadow
  styling, UI editing, and animation presets rendered after visual
  concatenation. Typewriter and word-by-word overlay presets render as real
  time-gated reveal steps instead of a full-text fade.
  The stable V1 renderer supports Shotstack-style layered visual tracks:
  the first visual track is the fullscreen base track, and additional visual
  tracks render timed image/video PIP or B-roll layers using clip-level fit,
  position, offset, width, height, scale, opacity, z_index, and layout alias
  fields. Overlay/PIP video audio is muted by default unless source_audio is
  explicitly set to keep.
  Stable V1 screen-recording clips can reuse one video or audio source through
  source_start/source_end ranges. Visual sources support normalized crop
  rectangles and eased source-space pan/zoom keyframes in both local and remote
  ffmpeg renders. Editable timeline markers preserve browser and recorder
  events, including optional active-region metadata, and the panel exposes the
  new ranges, crops, camera motion, and markers.
  ElevenLabs narration continuity is planned in timeline order across silence
  and SFX, uses stable text context for cache identity, and shares one
  server-side generation path between the panel and renderer. Provider request
  IDs are persisted and cleared with edited generation state, partial voice
  settings inherit conservative defaults, and generated TTS clips receive
  consistent loudness normalization without trimming natural pauses.
  Reserved fit_generated_keep_start visual slots retain their planned length
  while actual generated duration is recorded separately. Base visual tracks
  honor explicit clip starts on an absolute background canvas whenever gaps,
  overlaps, or out-of-order clips prevent safe concatenation; contiguous and
  legacy implicit tracks keep the efficient concat path.
  Renders locally via ffmpeg, on the private renderer component, or against a
  bound render_executor integration.
  Live chat cards expose evolving compositions and durable render progress.
  Render rows are created before AI materialization, asynchronous submissions
  continue through generation, rendering, upload, and QA without a second
  render call, and interrupted queued work resumes after sidecar restarts.
  V1 text rendering is deterministic across executors: Composer bundles Inter
  Regular, Medium, Semibold, and Bold plus Go Mono Regular and Bold, resolves
  every requested family and weight to an explicit font file, and serves the
  same bytes to local and remote render hosts. Validation and render QA report
  family substitutions and unsupported letter spacing instead of silently
  depending on host Fontconfig packages or proprietary fonts.
  Local Storage resolution decodes files_get's wrapped found/file response and
  reads disk-backed blobs directly when present, avoiding unnecessary fallback
  to signed public URLs for local ffmpeg renders.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.apps.call
  apps:
    - { name: storage, version: ">=0.10.13" }
    - { name: media-studio, version: ">=0.10.62", optional: true }
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
    # Renderer uploads authenticate with a job-scoped grant, not a platform
    # credential; the handler verifies the grant.
    - prefix: /renderer-io/
      no_auth: true
  mcp_tools:
    - { name: composition_outputs }
    - { name: composition_output_update }
    - { name: composition_output_master }
    - { name: composition_output_render }
    - { name: composition_output_history }
    - { name: composition_output_adopt }
    - { name: composition_output_estimate }
    - { name: procedure_create }
    - { name: procedure_revision_create }
    - { name: procedure_get }
    - { name: procedure_list }
    - { name: procedure_validate }
    - { name: composition_create }
    - { name: composition_update }
    - { name: composition_validate }
    - { name: composition_examples }
    - { name: composition_get }
    - { name: composition_list }
    - { name: composition_delete }
    - name: composition_render
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events: [composition.rendered, composition.failed, render.cancelled]
          match: { render_id: "$result.render_id" }
          expires_after: 24h
    - { name: render_status }
    - { name: render_cancel }
    - { name: asset_inspect }
    - { name: asset_search }
  ui_panels:
    - slot: project.page
      label: Composer
      icon: film
      entry: /ui/ComposerPanel.mjs
  ui_components:
    - name: composition-card
      entry: /ui/CompositionCard.mjs
      slots: [chat.message_attachment]
      props_schema: { type: object, required: [composition_id], properties: { composition_id: { type: integer } } }
      preview_props: { preview: true, composition_id: 1 }
    - name: render-card
      entry: /ui/RenderCard.mjs
      slots: [chat.message_attachment]
      props_schema: { type: object, required: [render_id], properties: { render_id: { type: integer } } }
      preview_props: { preview: true, render_id: 1 }
  publishes:
    - { name: procedure.created, description: "A reusable procedure was created." }
    - { name: procedure.revised, description: "A new immutable procedure revision was published." }
    - { name: composition.created, description: "A composition was created." }
    - { name: composition.updated, description: "A composition changed." }
    - { name: composition.deleted, description: "A composition was deleted." }
    - { name: render.queued, description: "A render was queued." }
    - { name: render.progress, description: "A render changed phase or progress." }
    - { name: composition.rendered, description: "A composition render completed." }
    - { name: composition.failed, description: "A composition render failed." }
    - { name: render.cancelled, description: "A render was cancelled." }
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
  - name: execution_mode
    type: text
    default: local
    label: Render execution
    description: "local renders in the Composer sidecar. component sends V1 FFmpeg renders, browser V2 renders and web-canvas-1 procedures to Composer's private renderer, placed by Server (deploy it through this installation's Components API first). Unavailable renderer calls fail explicitly; they never fall back to local rendering."
  - name: ffmpeg_path
    type: text
    default: "ffmpeg"
    label: ffmpeg binary
    description: PATH lookup for local renders. The renderer component provisions its own pinned FFmpeg.
  - name: ffprobe_path
    type: text
    default: "ffprobe"
    label: ffprobe binary
  - name: procedural_execution_enabled
    type: toggle
    default: "true"
    label: Trusted local procedural execution
    description: Run procedure source directly in the Composer sidecar. Only enable for trusted procedure authors; process isolation is deferred.
  - name: procedural_python_path
    type: text
    default: "python3"
    label: Python binary for procedures
  - name: procedural_bun_path
    type: text
    default: "bun"
    label: Bun binary for procedures
  - name: procedural_go_path
    type: text
    default: "go"
    label: Go binary for procedures
  - name: procedural_timeout_seconds
    type: text
    default: "900"
    label: Procedure timeout (seconds)
  - name: procedural_max_output_bytes
    type: text
    default: "1073741824"
    label: Maximum procedure artifact bytes
components:
  renderer:
    protocol: apteva.composer-renderer/v1
    required: false
    capabilities:
      passive_start: true
      drain: true
      replica_safe: false
      worker_coordination: false
      routing: key
    manifest:
      schema: apteva-app/v1
      name: composer-renderer
      display_name: Composer Renderer
      version: 0.11.3
      description: Private, bounded Composer render worker (FFmpeg and headless-browser capture). Placement is managed by Server owned components.
      scopes: [project, global]
      requires:
        permissions: [net.egress]
      provides:
        http_routes:
          - prefix: /_apteva/runtime/app/
        mcp_tools:
          - name: capabilities
            description: Report available render dependencies, execution limits and the gateway this worker reaches.
          - name: submit
            description: Submit an idempotent, deadline-bounded render plan from the owning Composer installation.
          - name: get
            description: Inspect a render attempt (status and progress) without replaying it.
          - name: cancel
            description: Cancel an attempt and confirm process-group termination.
          - name: acknowledge
            description: Compact a render receipt after the parent durably records it, retaining the idempotency tombstone.
      runtime:
        kind: source
        source:
          repo: github.com/apteva/apps
          ref: 566b3d8baf2574d42676de6e17cb57c12a202caf
          entry: mcp/composer/renderer
        port: 8080
        health_check: /health
        startup_timeout_seconds: 120
        package:
          app_capabilities:
            passive_start: true
            drain: true
            replica_safe: false
            worker_coordination: false
            routing: key
      config_schema:
        - name: work_capacity
          type: text
          default: "2"
          description: Weighted render capacity; browser captures weigh 2, encodes 1.
        - name: ffmpeg_path
          type: text
          default: ffmpeg
          description: FFmpeg executable on the execution target. A pinned static build is provisioned on Linux when missing.
        - name: ffprobe_path
          type: text
          default: ffprobe
          description: FFprobe executable on the execution target.
        - name: chrome_path
          type: text
          default: ""
          description: Chrome/Chromium executable for browser capture. Empty uses a local browser or provisions the pinned Chrome headless shell on Linux.
        - name: node_path
          type: text
          default: ""
          description: Node.js 22+ executable for browser capture. Empty uses node on PATH or provisions the pinned Node.js build on Linux.
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct {
	outputRenderer   func(context.Context, *sdk.AppCtx, outputSnapshot, string) (Result, error)
	procedureRunner  procedureRunner
	renderPoolCancel context.CancelFunc
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
		return errors.New("composer requires a db block")
	}
	if err := recoverOutputAttempts(ctx); err != nil {
		return err
	}
	globalCtx = ctx
	if n, err := recoverInterruptedComposerRenders(ctx.AppDB()); err != nil {
		return errors.New("recover interrupted renders: " + err.Error())
	} else if n > 0 {
		ctx.Logger().Warn("requeued interrupted Composer renders", "count", n)
	}
	a.renderPoolCancel = startComposerRenderPool(ctx)
	ctx.Logger().Info("composer mounted",
		"ffmpeg_path", ffmpegPath(),
		"component_rendering", componentRenderingEnabled(ctx),
	)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.renderPoolCancel != nil {
		a.renderPoolCancel()
	}
	return nil
}
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

func browserChromePath() string {
	if globalCtx != nil {
		if v := strings.TrimSpace(globalCtx.Config().Get("browser_chrome_path")); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(os.Getenv("CHROME_BIN")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("CHROMIUM_BIN")); v != "" {
		return v
	}
	return ""
}

func browserNodePath() string {
	if v := strings.TrimSpace(os.Getenv("NODE_BIN")); v != "" {
		return v
	}
	return "node"
}

func procedureExecutable(ctx *sdk.AppCtx, runtime string) string {
	key, envName, fallback := "", "", ""
	switch runtime {
	case "python-3.13-media":
		key, envName, fallback = "procedural_python_path", "COMPOSER_PROCEDURAL_PYTHON_PATH", "python3"
	case "bun-1-media":
		key, envName, fallback = "procedural_bun_path", "COMPOSER_PROCEDURAL_BUN_PATH", "bun"
	case "go-1.25-media":
		key, envName, fallback = "procedural_go_path", "COMPOSER_PROCEDURAL_GO_PATH", "go"
	}
	if ctx != nil && key != "" {
		if value := strings.TrimSpace(ctx.Config().Get(key)); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value
	}
	return fallback
}

func proceduralExecutionEnabled(ctx *sdk.AppCtx) bool {
	value := ""
	if ctx != nil {
		value = ctx.Config().Get("procedural_execution_enabled")
	}
	if env := strings.TrimSpace(os.Getenv("COMPOSER_PROCEDURAL_EXECUTION_ENABLED")); env != "" {
		value = env
	}
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "" || (value != "0" && value != "false" && value != "no" && value != "off")
}

func proceduralTimeout(ctx *sdk.AppCtx) time.Duration {
	seconds := int64(900)
	value := ""
	if ctx != nil {
		value = ctx.Config().Get("procedural_timeout_seconds")
	}
	if env := strings.TrimSpace(os.Getenv("COMPOSER_PROCEDURAL_TIMEOUT_SECONDS")); env != "" {
		value = env
	}
	if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && parsed > 0 {
		seconds = parsed
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func proceduralMaxOutputBytes(ctx *sdk.AppCtx) int64 {
	limit := int64(1 << 30)
	value := ""
	if ctx != nil {
		value = ctx.Config().Get("procedural_max_output_bytes")
	}
	if env := strings.TrimSpace(os.Getenv("COMPOSER_PROCEDURAL_MAX_OUTPUT_BYTES")); env != "" {
		value = env
	}
	if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && parsed > 0 {
		limit = parsed
	}
	return limit
}

func composerV2Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("COMPOSER_V2_ENABLED")))
	if v == "" {
		return true
	}
	return v != "0" && v != "false" && v != "no" && v != "off"
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
