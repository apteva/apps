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
version: 0.3.0
description: |
  Live ingest + HLS packaging for sibling Apteva apps. Fully
  standalone: segments and recordings live on the sidecar's local
  data dir.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
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
    - { name: streams_rotate_key,    description: "Generate new stream_key; optionally rotate playback_token + signing secret." }
    - { name: streams_get_metrics,   description: "Lightweight metrics for the dashboard polling lane." }
    - { name: streams_replay_url,    description: "Once status=ended, returns the replay URL." }
    - { name: streams_signed_url,    description: "Expiring signed playback/replay URL, scoped to its kind." }
    - { name: streams_set_url_policy, description: "Require expiring signed URLs for a stream." }
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

	// pending counts creates/rotations that have passed the
	// max_concurrent_streams check but haven't registered their runner
	// yet. Also guarded by runnersMu: the check and the reservation
	// have to be ONE critical section or two concurrent creates both
	// see room and the cap is exceeded.
	pending int

	// Port allocator backed by config rtmp_port_range. Initialized at
	// OnMount.
	ports *portAllocator

	// In-memory anonymous-viewer tracker. The aggregate counts are
	// persisted to streams.current_viewers + peak_viewers by the
	// viewer-counter worker; per-cookie state is never persisted.
	viewers *viewerTracker

	// throttle rate-limits heartbeats per source IP so a client can't
	// inflate the viewer counters by looping beats with fresh ids.
	throttle *viewerThrottle

	// playback caches the gating slice of each stream row so the media
	// handlers don't queue behind the DB's single connection.
	playback *playbackCache

	// Cached platform identity — WhoAmI is an HTTP round-trip and the
	// list paths would otherwise make two per row.
	identityMu     sync.Mutex
	identityURL    string
	identityExpiry time.Time

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
	a.throttle = newViewerThrottle(a.maxViewersPerIP(ctx))
	a.playback = newPlaybackCache(playbackCacheTTL)
	if a.runnerFactory == nil {
		a.runnerFactory = newFFmpegRunner
	}

	if err := a.reconcileOrphans(ctx); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	ctx.Logger().Info("streaming mounted",
		"data_dir", ctx.DataDir(),
		"rtmp_port_range", rangeStr,
		"max_concurrent", a.maxConcurrent(ctx))
	return nil
}

// reconcileOrphans finalizes every row the previous process left
// mid-flight.
//
// v0.2 did this with a bare UPDATE, which made it a THIRD end-of-stream
// path alongside streams_stop and the watchdog — and the one that
// didn't write recording_path or the VOD playlist. Since OnUnmount
// stopped ffmpeg without touching the DB at all, the ordinary sequence
// for a restart during a recorded webinar was: ffmpeg exits cleanly and
// writes a complete mp4 → nothing records that → next boot marks the
// row errored with no recording_path → streams_replay_url says
// "available: false" forever while a perfectly playable file sits on
// disk waiting for the retention sweeper to delete it. That is exactly
// the stranded-recording bug v0.2 set out to eliminate, surviving on
// the one path it didn't touch.
//
// Now every terminal transition in the app goes through
// finalizeStream, which picks up a complete recording wherever it
// finds one. OnUnmount finalizes gracefully ahead of a clean shutdown,
// so what reaches this function is a hard kill — hence `errored`.
func (a *App) reconcileOrphans(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id FROM streams WHERE status IN ('live','idle')`)
	if err != nil {
		return err
	}
	type orphan struct {
		id  int64
		pid string
	}
	orphans := []orphan{}
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.pid); err == nil {
			orphans = append(orphans, o)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, o := range orphans {
		if _, err := a.finalizeStream(ctx, o.pid, o.id, finalizeOpts{
			status: "errored",
			errMsg: "sidecar restarted; runner lost",
		}); err != nil {
			ctx.Logger().Warn("reconcile: finalize", "id", o.id, "err", err)
		}
	}
	if len(orphans) > 0 {
		ctx.Logger().Info("reconciled orphaned streams", "count", len(orphans))
	}
	return nil
}

func (a *App) OnUnmount(ctx *sdk.AppCtx) error {
	// Stop every active runner gracefully so recordings get finalized,
	// then write that outcome to the DB.
	//
	// Two things v0.2 got wrong here. It stopped runners SERIALLY, each
	// with the full per-stream finalize grace (60s by default, up to an
	// hour by config) — so a box at max_concurrent_streams took minutes
	// to shut down, the platform's shutdown budget expired long before
	// that, and the SIGKILL landed on ffmpeg children still rewriting
	// their moov atom. The generous grace that exists to protect those
	// recordings was the reason they got truncated. And it never wrote
	// anything to the DB, so even the recordings that survived were
	// stranded (see reconcileOrphans).
	//
	// Stop them concurrently against one shared budget, then finalize.
	a.runnersMu.Lock()
	runners := make(map[int64]*streamRunner, len(a.runners))
	for id, r := range a.runners {
		runners[id] = r
	}
	a.runners = map[int64]*streamRunner{}
	a.runnersMu.Unlock()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		stopErrs = map[int64]error{}
	)
	for id, r := range runners {
		wg.Add(1)
		go func(id int64, r *streamRunner) {
			defer wg.Done()
			err := r.stop(a.finalizeGrace(ctx, r.record))
			a.ports.release(r.port)
			mu.Lock()
			stopErrs[id] = err
			mu.Unlock()
		}(id, r)
	}
	wg.Wait()

	// The DB writes stay serial — they contend on the app DB's single
	// connection anyway, and the slow part was the waiting, not this.
	for id := range runners {
		var pid string
		if err := ctx.AppDB().QueryRow(
			`SELECT project_id FROM streams WHERE id = ?`, id).Scan(&pid); err != nil || pid == "" {
			continue
		}
		// A runner that had already crashed before we asked it to stop
		// must not be recorded as a clean end just because shutdown is
		// what finally noticed. classifyExit only returns nil for an
		// exit we requested.
		opts := finalizeOpts{status: "ended"}
		if err := stopErrs[id]; err != nil {
			opts = finalizeOpts{status: "errored", errMsg: err.Error()}
		}
		if _, err := a.finalizeStream(ctx, pid, id, opts); err != nil {
			ctx.Logger().Warn("unmount: finalize", "id", id, "err", err)
		}
		a.viewers.drop(id)
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// viewerCounterInterval is both the viewer-counter's schedule and the
// watch-time each of its ticks credits. One constant so the two can't
// drift — v0.2 scheduled "@every 10s" and separately hardcoded a ×10
// in the total_viewer_seconds arithmetic.
const viewerCounterInterval = 10 * time.Second

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "viewer-counter",
			Schedule: fmt.Sprintf("@every %s", viewerCounterInterval),
			Run:      a.runViewerCounter,
		},
		{
			Name:     "runner-watchdog",
			Schedule: "@every 5s",
			Run:      a.runWatchdog,
		},
		{
			// Enforces retention_days on terminal streams. Hourly is
			// plenty for a day-granularity policy.
			Name:     "retention-sweeper",
			Schedule: "@every 1h",
			Run:      a.runRetention,
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
			Description: "Generate new stream_key (kills the active session). Only for idle|live streams unless rotate_playback_token is set, which also rotates playback_token + the URL signing secret — that one works on ended streams too and instantly invalidates every outstanding playback/replay URL. Args: id, rotate_playback_token? (default false).",
			InputSchema: schemaObject(map[string]any{
				"id":                    map[string]any{"type": "integer"},
				"rotate_playback_token": map[string]any{"type": "boolean"},
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
			Name:        "streams_signed_url",
			Description: "Expiring signed playback URL. Returns {url} — fully formed, including the HMAC signature and (on global installs) project_id. Use for replay links a consumer app wants to expire. kind=heartbeat returns the signed viewer-heartbeat endpoint, which a stream with require_signed_urls=true also needs. Args: id, expires_in_seconds, kind? (hls|mp4|heartbeat, default hls).",
			InputSchema: schemaObject(map[string]any{
				"id":                 map[string]any{"type": "integer"},
				"expires_in_seconds": map[string]any{"type": "integer"},
				"kind":               map[string]any{"type": "string"},
			}, []string{"id", "expires_in_seconds"}),
			Handler: a.toolSignedURL,
		},
		{
			Name:        "streams_set_url_policy",
			Description: "Require expiring signed URLs for a stream. With require_signed_urls=true a bare ?t=<playback_token> stops working — every request must also carry a valid exp+sig pair from streams_signed_url. Args: id, require_signed_urls.",
			InputSchema: schemaObject(map[string]any{
				"id":                  map[string]any{"type": "integer"},
				"require_signed_urls": map[string]any{"type": "boolean"},
			}, []string{"id", "require_signed_urls"}),
			Handler: a.toolSetURLPolicy,
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
	ID             int64  `json:"id"`
	ProjectID      string `json:"project_id,omitempty"`
	Name           string `json:"name"`
	OwnerApp       string `json:"owner_app,omitempty"`
	OwnerTag       string `json:"owner_tag,omitempty"`
	IngestProtocol string `json:"ingest_protocol"`
	IngestPort     int    `json:"ingest_port,omitempty"`
	IngestURL      string `json:"ingest_url,omitempty"`
	StreamKey      string `json:"stream_key,omitempty"`
	PlaybackURL    string `json:"playback_url,omitempty"`
	HeartbeatURL   string `json:"heartbeat_url,omitempty"`
	PlaybackToken  string `json:"playback_token,omitempty"`
	// PlaybackURLExpiresAt is the unix expiry of the signature on
	// PlaybackURL/HeartbeatURL, or 0 when they carry none. Consumers
	// under require_signed_urls refresh on this rather than waiting
	// for playback to 404.
	PlaybackURLExpiresAt int64 `json:"playback_url_expires_at,omitempty"`
	// URLSigningSecret never leaves the sidecar — it's the HMAC key
	// behind streams_signed_url. Callers get signed URLs, not keys.
	URLSigningSecret   string  `json:"-"`
	RequireSignedURLs  bool    `json:"require_signed_urls"`
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
	PrunedAt           string  `json:"pruned_at,omitempty"`
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
	EventKindURLPolicyChanged    = "url_policy_changed"
)

// On-disk filenames inside a stream's data dir.
const (
	indexPlaylistFile  = "index.m3u8"  // live, rolling window
	replayPlaylistFile = "replay.m3u8" // VOD, written at finalize
	recordingFile      = "record.mp4"
	// segmentLogFile records each segment's REAL duration as it rolls
	// out of the live window, so finalize can write an accurate VOD
	// manifest. Not servable — validPlaybackFilename rejects it.
	segmentLogFile = "segments.log"
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

// hlsWindowSegments bounds the LIVE playlist.
//
// v0.1 defaulted to 0 (= -hls_list_size 0 = keep every entry), so the
// manifest every viewer re-fetched every ~2s grew for the whole life
// of the stream: a 2h stream at 4s segments is ~1800 entries / ~80KB,
// which at scale is tens of MB/s of pure manifest traffic. A small
// rolling window is what live HLS is supposed to look like; replay
// comes from the VOD playlist finalize writes, not from the live one.
// 0 still means unbounded for anyone who wants the old behavior.
func (a *App) hlsWindowSegments(ctx *sdk.AppCtx) int {
	if v := ctx.Config().Get("hls_window_segments"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 10
}

// finalizeGrace is how long stop() waits for ffmpeg to close its
// outputs before escalating to SIGTERM and then SIGKILL.
//
// v0.1 used a flat 7s (2s on the delete/rotate paths). `-movflags
// +faststart` rewrites the ENTIRE mp4 on close to move the moov atom
// to the front — a 2h 4Mbps recording is several GB and rewriting it
// takes far longer than 7s on any disk, so the old grace SIGKILLed
// ffmpeg mid-rewrite and truncated the recording. Streams that aren't
// recording have nothing to flush and keep a short grace.
func (a *App) finalizeGrace(ctx *sdk.AppCtx, record bool) time.Duration {
	if !record {
		return 5 * time.Second
	}
	n := 60
	if ctx != nil {
		if v := strings.TrimSpace(ctx.Config().Get("finalize_grace_seconds")); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed >= 5 && parsed <= 3600 {
				n = parsed
			}
		}
	}
	return time.Duration(n) * time.Second
}

// stopRunner stops a runner with the right finalization grace and
// returns its port to the pool.
func (a *App) stopRunner(ctx *sdk.AppCtx, r *streamRunner) {
	if r == nil {
		return
	}
	_ = r.stop(a.finalizeGrace(ctx, r.record))
	a.ports.release(r.port)
}

// ─── Concurrency slots ────────────────────────────────────────────
//
// v0.1 checked len(a.runners) against max_concurrent_streams in one
// critical section and inserted the runner in another, with a port
// allocation and an ffmpeg spawn in between — so two concurrent
// creates both saw room and both started. reserveSlot books the slot
// in the same critical section as the check.

// reserveSlot books one concurrency slot, or reports the cap is full.
// Every successful reservation MUST be matched by exactly one
// commitRunner (success) or releaseSlot (failure).
func (a *App) reserveSlot(maxC int) bool {
	a.runnersMu.Lock()
	defer a.runnersMu.Unlock()
	if len(a.runners)+a.pending >= maxC {
		return false
	}
	a.pending++
	return true
}

func (a *App) releaseSlot() {
	a.runnersMu.Lock()
	if a.pending > 0 {
		a.pending--
	}
	a.runnersMu.Unlock()
}

func (a *App) commitRunner(id int64, r *streamRunner) {
	a.runnersMu.Lock()
	if a.pending > 0 {
		a.pending--
	}
	a.runners[id] = r
	a.runnersMu.Unlock()
}

func (a *App) viewerIdleSeconds(ctx *sdk.AppCtx) int {
	if v := ctx.Config().Get("viewer_idle_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 5 {
			return n
		}
	}
	return 30
}

// maxViewersPerIP is the heartbeat throttle's identity budget for one
// (source IP, stream) pair — see the throttle block in viewers.go.
// Operators fronted by a large shared egress (a university, a big
// corporate NAT) raise it; the beat ceiling scales with it.
func (a *App) maxViewersPerIP(ctx *sdk.AppCtx) int {
	if ctx == nil {
		return defaultMaxViewersPerIP
	}
	if v := strings.TrimSpace(ctx.Config().Get("max_viewers_per_ip")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 4096 {
			return n
		}
	}
	return defaultMaxViewersPerIP
}

// signedURLTTL is how long the signed URLs materializeURLs mints stay
// valid, in seconds. Two very different lifetimes hide behind one
// question here:
//
//   - A terminal stream's replay link should be short. An hour is
//     plenty to open a player, and the whole point of
//     require_signed_urls is that the link stops working.
//   - A LIVE stream's playback URL has to outlast the broadcast.
//     rewriteManifestQuery propagates the manifest's own exp+sig onto
//     every segment URI, so the entire viewing session is gated on
//     that one timestamp. v0.2 used the 1h replay TTL for live streams
//     too, which meant a 90-minute webinar went dark for every viewer
//     at once at T+60min, with no renewal path — the consumer app
//     would have had to re-poll streams_signed_url and swap the player
//     source mid-stream, which nothing told it to do.
//
// Callers that want an exact lifetime still use streams_signed_url.
// The expiry is now reported alongside the URL (see Stream's
// playback_url_expires_at) so a consumer can refresh deliberately
// rather than discovering it in a 404.
func (a *App) signedURLTTL(ctx *sdk.AppCtx, live bool) time.Duration {
	key, def, min, max := "replay_url_ttl_seconds", replayURLTTL, time.Minute, 7*24*time.Hour
	if live {
		key, def = "live_url_ttl_seconds", liveURLTTL
	}
	if ctx != nil {
		if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				d := time.Duration(n) * time.Second
				if d >= min && d <= max {
					return d
				}
			}
		}
	}
	return def
}

func (a *App) ffmpegPath(ctx *sdk.AppCtx) string {
	if v := strings.TrimSpace(ctx.Config().Get("ffmpeg_path")); v != "" {
		return v
	}
	return "ffmpeg"
}

// identityCacheTTL bounds how stale publicURL's cached answer can be.
// The platform's public URL only changes when an operator edits
// Settings → Server.
const identityCacheTTL = 60 * time.Second

// publicURL returns the base URL viewers use to reach this sidecar.
// Resolved from the platform's PublicURL (settable via Settings →
// Server) with a localhost fallback. The sidecar's actual listen port
// is not on the public URL — apteva-server reverse-proxies under
// /api/apps/streaming/.
//
// Cached: WhoAmI is an uncached HTTP round-trip to apteva-server and
// materializeURLs calls this twice per stream, inside list loops. A
// failed lookup is cached too — a platform that's briefly down
// shouldn't turn every streams_list into N stalled round-trips; the
// URLs just fall back to the relative prefix for a minute.
func (a *App) publicURL(ctx *sdk.AppCtx) string {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return ""
	}
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	if !a.identityExpiry.IsZero() && time.Now().Before(a.identityExpiry) {
		return a.identityURL
	}
	url := ""
	if id, err := ctx.PlatformAPI().WhoAmI(); err == nil && id != nil {
		url = strings.TrimRight(id.PublicURL, "/")
	}
	a.identityURL = url
	a.identityExpiry = time.Now().Add(identityCacheTTL)
	return url
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

// nowStamp is the ONE timestamp format this app writes: RFC3339 UTC.
//
// v0.1 mixed it with SQLite's CURRENT_TIMESTAMP ("2026-08-18
// 09:00:00") in the same columns — started_at from Go, ended_at from
// the watchdog and the OnMount reconciler from SQLite — so parsing or
// lexically sorting those columns gave different answers depending on
// which code path had run. Migration 002 normalizes the old rows.
func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// parseTimestamp accepts both formats v0.1 could have written, so
// consumers of old rows (the retention sweeper) don't trip over a
// pre-migration value.
func parseTimestamp(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
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
		`INSERT INTO stream_events (project_id, stream_id, kind, body, source_detail, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		s.ProjectID, s.ID, kind, nullStr(body), detailJSON, nowStamp()); err != nil {
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
