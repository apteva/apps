// Streaming v0.1 — live ingest + HLS packaging.
//
// One streaming sidecar runs N concurrent stream sessions. Each session
// owns one ffmpeg child process (`-listen 1` RTMP receiver → `-c copy`
// HLS segmenter, optional second output for record.mp4). Segments and
// recordings live under DataDir()/streams/<id>/ and are served by this
// sidecar's own NoAuth + token-gated HTTP routes.
//
// Resource model:
//   - streams_create allocates a port from rtmp_port_range, generates
//     keys, starts ffmpeg listening (status=idle).
//   - The first publisher push flips status=live (the runner scrapes
//     ffmpeg's stderr for the first bitrate line).
//   - streams_stop or graceful publisher disconnect → status=ended,
//     recording finalized.
//   - Crash → watchdog flips status=errored, port is freed.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// ─── Embedded manifest ────────────────────────────────────────────
//
// Mirrored from apteva.yaml so the running binary is self-describing
// for `streaming --help` etc. CRM keeps the same pattern; the two can
// drift slightly (config_schema is on disk only) — manifest_test guards
// the parts that matter.

const manifestYAML = `schema: apteva-app/v1
name: streaming
display_name: Streaming
version: 0.1.0
description: |
  Live ingest + HLS packaging for sibling Apteva apps. v0.1 is fully
  standalone: segments and recordings live on the sidecar's local
  data dir.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: streams_create,        description: "Allocate a stream — returns ingest_url, playback_url, stream_key, playback_token." }
    - { name: streams_get,           description: "Full state snapshot." }
    - { name: streams_list,          description: "Filter by status, owner_app, owner_tag." }
    - { name: streams_stop,          description: "Graceful stop; finalize recording; emit stream.ended." }
    - { name: streams_delete,        description: "Tear down + delete segments + recording." }
    - { name: streams_rotate_key,    description: "Generate new stream_key (kills active session)." }
    - { name: streams_get_metrics,   description: "Lightweight metrics for the dashboard polling lane." }
    - { name: streams_replay_url,    description: "Once status=ended, returns the replay URL." }
    - { name: streams_load_test,     description: "Synthetic load generator — simulate N concurrent viewers." }
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/streaming }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/streaming.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ──────────────────────────────────────────────────────────

type App struct {
	// Active runners — one per status=live (or pre-live idle) row.
	// Mutex-guarded; the watchdog and tool handlers both touch it.
	runners   map[int64]*streamRunner
	runnersMu sync.Mutex

	// Port allocator backed by config rtmp_port_range. Initialized at
	// OnMount.
	ports *portAllocator

	// In-memory anonymous-viewer tracker. The aggregate counts are
	// persisted to streams.current_viewers + peak_viewers by the
	// viewer-counter worker; per-cookie state is never persisted.
	viewers *viewerTracker

	// runnerFactory creates a streamRunner. Tests inject a fake that
	// doesn't actually exec ffmpeg; production uses newFFmpegRunner.
	runnerFactory func(opts runnerOpts) (*streamRunner, error)
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
		return errors.New("streaming requires a db block")
	}
	if ctx.DataDir() == "" {
		return errors.New("streaming requires APTEVA_DATA_DIR (or DB_PATH) so segments have somewhere to live")
	}

	// Stash the ctx for HTTP handlers — same pattern CRM uses.
	globalCtx = ctx
	globalApp = a

	// Initialize the port allocator from config.
	rangeStr := strings.TrimSpace(ctx.Config().Get("rtmp_port_range"))
	if rangeStr == "" {
		rangeStr = "1935-1965"
	}
	pa, err := newPortAllocator(rangeStr)
	if err != nil {
		return fmt.Errorf("rtmp_port_range %q: %w", rangeStr, err)
	}
	a.ports = pa

	a.runners = map[int64]*streamRunner{}
	a.viewers = newViewerTracker()
	if a.runnerFactory == nil {
		a.runnerFactory = newFFmpegRunner
	}

	// Reconciler: any row left as status=live across a restart is dead.
	// Mark errored, free the port (free-list is fresh anyway).
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams
		 SET status='errored', error='sidecar restarted; runner lost', ended_at = CURRENT_TIMESTAMP
		 WHERE status='live' OR status='idle'`); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	ctx.Logger().Info("streaming mounted",
		"data_dir", ctx.DataDir(),
		"rtmp_port_range", rangeStr,
		"max_concurrent", a.maxConcurrent(ctx))
	return nil
}

func (a *App) OnUnmount(ctx *sdk.AppCtx) error {
	// Stop every active runner gracefully so recordings get finalized.
	a.runnersMu.Lock()
	runners := make([]*streamRunner, 0, len(a.runners))
	for _, r := range a.runners {
		runners = append(runners, r)
	}
	a.runnersMu.Unlock()
	for _, r := range runners {
		_ = r.stop(5 * time.Second)
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "viewer-counter",
			Schedule: "@every 10s",
			Run:      a.runViewerCounter,
		},
		{
			Name:     "runner-watchdog",
			Schedule: "@every 5s",
			Run:      a.runWatchdog,
		},
	}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Public playback — token-gated via ?t=<playback_token>.
		// NoAuth because viewers don't have an APTEVA_APP_TOKEN.
		{Pattern: "/streams/", Handler: a.handlePlayback, NoAuth: true},

		// Heartbeat from players. NoAuth, identified by viewer_id cookie.
		{Pattern: "/heartbeat/", Handler: a.handleHeartbeat, NoAuth: true},

		// Admin REST mirror for the (future) panel + CLI tooling.
		{Pattern: "/admin/streams", Handler: a.handleAdminStreams},
		{Pattern: "/admin/streams/", Handler: a.handleAdminStreamItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "streams_create",
			Description: "Allocate a stream — returns ingest_url, playback_url, stream_key, playback_token. Args: name, owner_app?, owner_tag?, record? (default true), visibility? (signed|public, default signed), retention_days? (default 30).",
			InputSchema: schemaObject(map[string]any{
				"name":           map[string]any{"type": "string"},
				"owner_app":      map[string]any{"type": "string"},
				"owner_tag":      map[string]any{"type": "string"},
				"record":         map[string]any{"type": "boolean"},
				"visibility":     map[string]any{"type": "string"},
				"retention_days": map[string]any{"type": "integer"},
			}, []string{"name"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "streams_get",
			Description: "Full state: status, current_bitrate, current_fps, resolution, viewer_count, peak_viewers. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolGet,
		},
		{
			Name:        "streams_list",
			Description: "Filter by status, owner_app, owner_tag. Args: status?, owner_app?, owner_tag?, limit? (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"status":    map[string]any{"type": "string"},
				"owner_app": map[string]any{"type": "string"},
				"owner_tag": map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "streams_stop",
			Description: "Graceful stop. SIGINT the publisher's ffmpeg child, finalize recording, set status=ended. Idempotent. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolStop,
		},
		{
			Name:        "streams_delete",
			Description: "Tear down listener and delete segments + recording from disk. Idempotent. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolDelete,
		},
		{
			Name:        "streams_rotate_key",
			Description: "Generate new stream_key (kills active session). Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolRotateKey,
		},
		{
			Name:        "streams_get_metrics",
			Description: "Lightweight metrics: current_bitrate_kbps, current_fps, viewer_count, peak_viewers, total_viewer_seconds, uptime_seconds, dropped_frames. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolGetMetrics,
		},
		{
			Name:        "streams_replay_url",
			Description: "Returns replay URLs once status=ended. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolReplayURL,
		},
		{
			Name:        "streams_load_test",
			Description: "Synthetic load generator — N concurrent viewers fetch manifest+segments. Returns p50/p95/p99 ttfb, served bitrate, refusals, http_5xx, segments_late. Args: id, viewers? (default 50, max 2000), duration_seconds? (default 30, max 300).",
			InputSchema: schemaObject(map[string]any{
				"id":               map[string]any{"type": "integer"},
				"viewers":          map[string]any{"type": "integer"},
				"duration_seconds": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolLoadTest,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution (CRM's pattern) ───────────────────────────

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
	return "", errors.New("project_id required in query string when install scope=global")
}

// ─── Domain types ─────────────────────────────────────────────────

type Stream struct {
	ID                 int64   `json:"id"`
	ProjectID          string  `json:"project_id,omitempty"`
	Name               string  `json:"name"`
	OwnerApp           string  `json:"owner_app,omitempty"`
	OwnerTag           string  `json:"owner_tag,omitempty"`
	IngestProtocol     string  `json:"ingest_protocol"`
	IngestPort         int     `json:"ingest_port,omitempty"`
	IngestURL          string  `json:"ingest_url,omitempty"`
	StreamKey          string  `json:"stream_key,omitempty"`
	PlaybackURL        string  `json:"playback_url,omitempty"`
	PlaybackToken      string  `json:"playback_token,omitempty"`
	Visibility         string  `json:"visibility"`
	Status             string  `json:"status"`
	Record             bool    `json:"record"`
	RetentionDays      int     `json:"retention_days"`
	StoragePrefix      string  `json:"storage_prefix"`
	RecordingPath      string  `json:"recording_path,omitempty"`
	CurrentBitrateKbps int     `json:"current_bitrate_kbps,omitempty"`
	CurrentFPS         float64 `json:"current_fps,omitempty"`
	Resolution         string  `json:"resolution,omitempty"`
	DroppedFrames      int     `json:"dropped_frames,omitempty"`
	CurrentViewers     int     `json:"current_viewers"`
	PeakViewers        int     `json:"peak_viewers"`
	TotalViewerSeconds int     `json:"total_viewer_seconds"`
	CreatedAt          string  `json:"created_at"`
	StartedAt          string  `json:"started_at,omitempty"`
	EndedAt            string  `json:"ended_at,omitempty"`
	Error              string  `json:"error,omitempty"`
}

// Event kinds — stored as TEXT, no SQL CHECK, so adding new kinds is
// purely a Go-side change.
const (
	EventKindCreated             = "created"
	EventKindStarted             = "started"
	EventKindPublisherDisconnect = "publisher_disconnect"
	EventKindBitrateDrop         = "bitrate_drop"
	EventKindEnded               = "ended"
	EventKindErrored             = "errored"
	EventKindRecordingFinalized  = "recording_finalized"
	EventKindKeyRotated          = "key_rotated"
)

// ─── Shared helpers ───────────────────────────────────────────────

// globalCtx and globalApp are stashed at OnMount time so HTTP handlers
// — which the SDK invokes without an AppCtx — can reach them. Same
// pattern CRM uses; v0.2 of the SDK should grow a request-scoped hook.
var (
	globalCtx *sdk.AppCtx
	globalApp *App
)

func (a *App) maxConcurrent(ctx *sdk.AppCtx) int {
	if ctx == nil {
		return 4
	}
	if v := ctx.Config().Get("max_concurrent_streams"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4
}

func (a *App) hlsSegmentSeconds(ctx *sdk.AppCtx) int {
	if v := ctx.Config().Get("hls_segment_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 30 {
			return n
		}
	}
	return 4
}

func (a *App) hlsWindowSegments(ctx *sdk.AppCtx) int {
	if v := ctx.Config().Get("hls_window_segments"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}

func (a *App) viewerIdleSeconds(ctx *sdk.AppCtx) int {
	if v := ctx.Config().Get("viewer_idle_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 5 {
			return n
		}
	}
	return 30
}

func (a *App) ffmpegPath(ctx *sdk.AppCtx) string {
	if v := strings.TrimSpace(ctx.Config().Get("ffmpeg_path")); v != "" {
		return v
	}
	return "ffmpeg"
}

// publicURL returns the base URL viewers use to reach this sidecar.
// Resolved from the platform's PublicURL (settable via Settings →
// Server) with a localhost fallback. The sidecar's actual listen port
// is not on the public URL — apteva-server reverse-proxies under
// /api/apps/streaming/.
func (a *App) publicURL(ctx *sdk.AppCtx) string {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return ""
	}
	id, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || id == nil {
		return ""
	}
	return strings.TrimRight(id.PublicURL, "/")
}

// ─── Tiny utilities ───────────────────────────────────────────────

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	if v, ok := args[key].(string); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		// LLMs frequently emit numeric ids as quoted strings.
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func boolArg(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// randomToken returns a URL-safe random string of at least 32 chars.
func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal per Go docs.
		panic("rand.Read: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// streamDataDir returns the absolute path where this stream's segments
// and recording live: <DataDir>/<storage_prefix>/.
func streamDataDir(ctx *sdk.AppCtx, storagePrefix string) string {
	return filepath.Join(ctx.DataDir(), storagePrefix)
}

// emitStreamEvent records an audit row + fires a platform event.
// Best-effort — logs but doesn't bubble.
func emitStreamEvent(ctx *sdk.AppCtx, s *Stream, kind, body string, detail map[string]any) {
	if ctx == nil || ctx.AppDB() == nil || s == nil {
		return
	}
	var detailJSON sql.NullString
	if len(detail) > 0 {
		raw, _ := json.Marshal(detail)
		detailJSON = sql.NullString{String: string(raw), Valid: true}
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO stream_events (project_id, stream_id, kind, body, source_detail)
		 VALUES (?, ?, ?, ?, ?)`,
		s.ProjectID, s.ID, kind, nullStr(body), detailJSON); err != nil {
		ctx.Logger().Warn("emit stream event: db insert failed", "kind", kind, "err", err)
	}
	ctx.Emit("stream."+kind, map[string]any{
		"id":        s.ID,
		"owner_app": s.OwnerApp,
		"owner_tag": s.OwnerTag,
	})
}

// ─── HTTP utilities ───────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// withDeadline wraps a context with a timeout, returning a new context
// and a cancel function the caller MUST defer.
func withDeadline(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}
