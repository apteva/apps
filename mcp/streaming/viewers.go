package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// viewerTracker is the in-memory anonymous-viewer counter. One bucket
// per active stream; each bucket maps an opaque cookie to its last
// heartbeat time. The worker sweeps stale entries and projects the
// size into the streams.current_viewers column.
//
// Identity is deliberately absent here. Consumer apps (webinars,
// classroom, …) own per-identity attendance in their own tables.
type viewerTracker struct {
	mu      sync.Mutex
	streams map[int64]map[string]time.Time
}

func newViewerTracker() *viewerTracker {
	return &viewerTracker{streams: map[int64]map[string]time.Time{}}
}

// bump records a heartbeat for (stream, cookie).
func (v *viewerTracker) bump(streamID int64, cookie string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	bucket, ok := v.streams[streamID]
	if !ok {
		bucket = map[string]time.Time{}
		v.streams[streamID] = bucket
	}
	bucket[cookie] = time.Now()
}

// sweep drops cookies stale past `idle`, returns the active count
// after the sweep. Safe to call when the stream has no bucket
// (returns 0).
func (v *viewerTracker) sweep(streamID int64, idle time.Duration) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	bucket, ok := v.streams[streamID]
	if !ok {
		return 0
	}
	cutoff := time.Now().Add(-idle)
	for cookie, ts := range bucket {
		if ts.Before(cutoff) {
			delete(bucket, cookie)
		}
	}
	return len(bucket)
}

// count returns the bucket size without sweeping.
func (v *viewerTracker) count(streamID int64) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.streams[streamID])
}

// drop removes the bucket entirely — used when a stream is deleted
// or torn down.
func (v *viewerTracker) drop(streamID int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.streams, streamID)
}

// trackedStreams returns a snapshot of the stream IDs that currently
// have at least one bucket entry. The worker iterates over these to
// run sweeps.
func (v *viewerTracker) trackedStreams() []int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]int64, 0, len(v.streams))
	for id := range v.streams {
		out = append(out, id)
	}
	return out
}

// ─── Heartbeat handler ────────────────────────────────────────────
//
// POST /heartbeat/<stream_id>?t=<playback_token>
// Cookie-based viewer ID — the server sets one on first contact.
// Returns: { ok: true, viewer_id: "<set-as-cookie>" }.
//
// Anonymous by design. If the consumer app needs identity attribution,
// it runs its own heartbeat endpoint; this one only feeds the
// aggregate counter.

func (a *App) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		// GET allowed so a `<img>` beacon can be used as a fallback.
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

	if s.Visibility == "signed" {
		token := r.URL.Query().Get("t")
		if token == "" || token != s.PlaybackToken {
			http.NotFound(w, r)
			return
		}
	}

	cookie := ""
	if c, err := r.Cookie("apteva_viewer"); err == nil {
		cookie = c.Value
	}
	if cookie == "" {
		cookie = randomViewerID()
		http.SetCookie(w, &http.Cookie{
			Name:     "apteva_viewer",
			Value:    cookie,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   86400, // 24h — same idle window-of-windows
		})
	}

	app.viewers.bump(id, cookie)

	httpJSON(w, map[string]any{
		"ok":        true,
		"viewer_id": cookie,
	})
}

// ─── Worker: viewer-counter ───────────────────────────────────────
//
// Every 10s, sweep each tracked stream's bucket, project the active
// count into `streams.current_viewers`, bump `peak_viewers` and
// `total_viewer_seconds`, and emit `stream.viewer_count_changed` so
// the dashboard panel can render without polling.

func (a *App) runViewerCounter(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil || a.viewers == nil {
		return nil
	}
	idle := time.Duration(a.viewerIdleSeconds(app)) * time.Second

	for _, streamID := range a.viewers.trackedStreams() {
		current := a.viewers.sweep(streamID, idle)

		// Look up project_id + previous peak. A row may have been
		// deleted out from under us — drop the bucket and continue.
		var pid string
		var peak int
		err := app.AppDB().QueryRow(
			`SELECT project_id, peak_viewers FROM streams WHERE id = ?`,
			streamID).Scan(&pid, &peak)
		if err != nil {
			a.viewers.drop(streamID)
			continue
		}

		newPeak := peak
		if current > peak {
			newPeak = current
		}
		// Tick adds (current * 10s) of watch time. Approximate; v0.2
		// could compute per-cookie session lengths from the map.
		_, _ = app.AppDB().Exec(
			`UPDATE streams
			 SET current_viewers = ?, peak_viewers = ?,
			     total_viewer_seconds = total_viewer_seconds + ?
			 WHERE id = ?`,
			current, newPeak, current*10, streamID)

		app.Emit("stream.viewer_count_changed", map[string]any{
			"id":    streamID,
			"count": current,
		})
		_ = pid // reserved for v0.2 per-project rate limiting
	}
	return nil
}

// ─── Watchdog (unchanged) ─────────────────────────────────────────
//
// Detects ffmpeg children that exited unexpectedly + the idle→live
// transition on the first scraped bitrate line. Every 5s.

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
		var pid string
		var status string
		_ = app.AppDB().QueryRow(
			`SELECT project_id, status FROM streams WHERE id = ?`, id).Scan(&pid, &status)
		if pid == "" {
			a.viewers.drop(id)
			continue
		}
		if status == "ended" || status == "errored" {
			a.viewers.drop(id)
			continue
		}
		newStatus := "errored"
		errMsg := ""
		if err == nil {
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
		a.viewers.drop(id)
	}

	// idle→live transition on first scraped bitrate.
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

// randomViewerID — short opaque cookie for anonymous viewers.
func randomViewerID() string {
	return randomToken()[:16]
}
