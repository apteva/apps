package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// handleHeartbeat receives viewer heartbeats. Path shape:
//
//   POST /heartbeat/<stream_id>?t=<playback_token>[&v=<viewer_id>][&ext=<external_id>]
//
// Body is optional JSON:
//   { "position_seconds": 41.2, "user_agent": "..." }
//
// Returns: { ok: true, viewer_id: "<set-this-as-cookie>" }.
//
// The heartbeat table is the single source of truth for viewer counts;
// the viewer-counter worker decays stale rows every 10s.
func (a *App) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		// GET allowed so a `<img>` beacon can be used as a fallback if
		// the player can't issue POSTs.
		httpErr(w, http.StatusMethodNotAllowed, "POST or GET")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/heartbeat/")
	id, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "invalid stream id")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := globalCtx
	app := globalApp
	if ctx == nil || app == nil {
		httpErr(w, http.StatusServiceUnavailable, "sidecar not mounted")
		return
	}

	s, err := app.dbGet(ctx, pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s == nil {
		http.NotFound(w, r)
		return
	}

	// Token check — same gate as playback.
	if s.Visibility == "signed" {
		token := r.URL.Query().Get("t")
		if token == "" || token != s.PlaybackToken {
			http.NotFound(w, r)
			return
		}
	}

	// Resolve viewer_id: query param > cookie > new random.
	viewerID := r.URL.Query().Get("v")
	if viewerID == "" {
		if c, err := r.Cookie("apteva_viewer"); err == nil {
			viewerID = c.Value
		}
	}
	if viewerID == "" {
		viewerID = randomViewerID()
		http.SetCookie(w, &http.Cookie{
			Name:     "apteva_viewer",
			Value:    viewerID,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   86400, // 24h
		})
	}
	externalID := r.URL.Query().Get("ext")
	source := r.URL.Query().Get("source")
	if source != "replay" {
		source = "live"
	}

	// Optional JSON body — ignored for v0.1 except user_agent fallback.
	userAgent := r.UserAgent()
	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			UserAgent string `json:"user_agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.UserAgent != "" {
			userAgent = body.UserAgent
		}
	}

	// Upsert (stream_id, viewer_id) — refresh last_heartbeat each call.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO stream_viewers
			(project_id, stream_id, viewer_id, external_id, source,
			 joined_at, last_heartbeat, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(stream_id, viewer_id) DO UPDATE SET
			last_heartbeat = excluded.last_heartbeat,
			external_id = COALESCE(NULLIF(excluded.external_id,''), stream_viewers.external_id),
			user_agent = COALESCE(NULLIF(excluded.user_agent,''), stream_viewers.user_agent)`,
		pid, id, viewerID, nullStr(externalID), source, now, now, nullStr(userAgent)); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpJSON(w, map[string]any{
		"ok":        true,
		"viewer_id": viewerID,
	})
}

// runViewerCounter is the SDK Worker that:
//
//   - Marks viewers as "left" when last_heartbeat is older than the
//     idle window.
//   - Bumps total_viewer_seconds for active viewers (idle + 10s slot).
//   - Updates peak_viewers per stream.
//   - Emits stream.viewer_count_changed when the count moves (debounced
//     to avoid event spam: only emit if the value differs from the last
//     persisted peak_viewers OR a configurable poll cadence).
//
// Runs every 10s via the Worker schedule. Idempotent — re-running on
// the same data is safe.
func (a *App) runViewerCounter(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	idle := a.viewerIdleSeconds(app)
	cutoff := time.Now().UTC().Add(-time.Duration(idle) * time.Second).Format(time.RFC3339)

	// 1. Mark stale viewers as left.
	if _, err := app.AppDB().Exec(
		`UPDATE stream_viewers
		 SET left_at = CURRENT_TIMESTAMP
		 WHERE left_at IS NULL AND last_heartbeat < ?`, cutoff); err != nil {
		app.Logger().Warn("viewer-counter: mark stale", "err", err)
	}

	// 2. For each active stream, count viewers and update peak.
	rows, err := app.AppDB().Query(
		`SELECT id, project_id, peak_viewers FROM streams
		 WHERE status IN ('idle','live')`)
	if err != nil {
		return err
	}
	type streamSnap struct {
		ID         int64
		ProjectID  string
		Peak       int
	}
	var snaps []streamSnap
	for rows.Next() {
		var s streamSnap
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Peak); err == nil {
			snaps = append(snaps, s)
		}
	}
	rows.Close()

	for _, s := range snaps {
		var current int
		_ = app.AppDB().QueryRow(
			`SELECT COUNT(*) FROM stream_viewers
			 WHERE stream_id = ? AND left_at IS NULL`,
			s.ID).Scan(&current)

		// Bump total_viewer_seconds by current * (idle/3) — a fudge
		// matching the worker's cadence (10s) without trusting the
		// schedule. v0.2: track watch_seconds per-viewer with proper
		// time-since-last-tick math.
		if current > 0 {
			_, _ = app.AppDB().Exec(
				`UPDATE streams SET total_viewer_seconds = total_viewer_seconds + ?
				 WHERE id = ?`, current*10, s.ID)
		}

		if current > s.Peak {
			_, _ = app.AppDB().Exec(
				`UPDATE streams SET peak_viewers = ? WHERE id = ?`, current, s.ID)
		}

		// Emit on every tick — apteva-server's event bus is the
		// natural rate-limiter (it dedups in the buffer ring), and
		// dashboard subscribers need the regular tick to refresh.
		app.Emit("stream.viewer_count_changed", map[string]any{
			"id":    s.ID,
			"count": current,
		})
	}
	return nil
}

// runWatchdog detects ffmpeg children that exited unexpectedly and
// reconciles DB state. Runs every 5s.
func (a *App) runWatchdog(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil {
		return nil
	}
	a.runnersMu.Lock()
	dead := []int64{}
	deadDetail := map[int64]error{}
	for id, r := range a.runners {
		ex, ok := r.tryReadExit()
		if !ok {
			continue
		}
		dead = append(dead, id)
		deadDetail[id] = ex.err
	}
	for _, id := range dead {
		r := a.runners[id]
		delete(a.runners, id)
		a.ports.release(r.port)
	}
	a.runnersMu.Unlock()

	for _, id := range dead {
		err := deadDetail[id]
		// Look up the row so we can emit with project_id and pick a
		// clean status.
		var pid string
		var status string
		_ = app.AppDB().QueryRow(
			`SELECT project_id, status FROM streams WHERE id = ?`, id).Scan(&pid, &status)
		if pid == "" {
			continue
		}
		// If the operator already called streams_stop, status is
		// already 'ended' — leave it. Otherwise this was an unexpected
		// exit.
		if status == "ended" || status == "errored" {
			continue
		}
		newStatus := "errored"
		errMsg := ""
		if err == nil {
			// Graceful exit without an explicit stop — publisher
			// disconnected. Treat as ended.
			newStatus = "ended"
		} else {
			errMsg = err.Error()
		}
		if _, e := app.AppDB().Exec(
			`UPDATE streams SET status = ?, ended_at = CURRENT_TIMESTAMP, error = ?
			 WHERE id = ?`, newStatus, nullStr(errMsg), id); e != nil {
			app.Logger().Warn("watchdog: update", "id", id, "err", e)
			continue
		}
		s := &Stream{ID: id, ProjectID: pid}
		if newStatus == "errored" {
			emitStreamEvent(app, s, EventKindErrored, errMsg, nil)
		} else {
			emitStreamEvent(app, s, EventKindPublisherDisconnect, "", nil)
			emitStreamEvent(app, s, EventKindEnded, "", nil)
		}
	}

	// Also detect transition from idle → live: a runner whose first
	// progress line just landed bumps started_at on the row.
	a.runnersMu.Lock()
	live := map[int64]*streamRunner{}
	for id, r := range a.runners {
		live[id] = r
	}
	a.runnersMu.Unlock()
	for id, r := range live {
		m := r.metrics()
		if !m.HasPublisher {
			continue
		}
		// Bump started_at + status if not done already.
		var status string
		_ = app.AppDB().QueryRow(`SELECT status FROM streams WHERE id = ?`, id).Scan(&status)
		if status == "idle" {
			now := time.Now().UTC().Format(time.RFC3339)
			_, _ = app.AppDB().Exec(
				`UPDATE streams
				 SET status = 'live', started_at = ?, current_bitrate_kbps = ?,
				     current_fps = ?, resolution = ?, dropped_frames = ?
				 WHERE id = ?`,
				now, m.BitrateKbps, m.FPS, m.Resolution, m.DroppedFrames, id)
			var pid string
			_ = app.AppDB().QueryRow(`SELECT project_id FROM streams WHERE id = ?`, id).Scan(&pid)
			if pid != "" {
				emitStreamEvent(app, &Stream{ID: id, ProjectID: pid}, EventKindStarted, "", map[string]any{
					"resolution": m.Resolution,
					"bitrate":    m.BitrateKbps,
				})
			}
		} else if status == "live" {
			// Periodic refresh of the persisted snapshot.
			_, _ = app.AppDB().Exec(
				`UPDATE streams
				 SET current_bitrate_kbps = ?, current_fps = ?, resolution = ?,
				     dropped_frames = ?
				 WHERE id = ?`,
				m.BitrateKbps, m.FPS, m.Resolution, m.DroppedFrames, id)
		}
	}
	return nil
}

// randomViewerID is shorter than randomToken — viewers are anonymous
// and the cookie is set on every page load, so 16 bytes is plenty.
func randomViewerID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("rand.Read: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
