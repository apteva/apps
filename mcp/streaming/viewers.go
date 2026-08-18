package main

import (
	"context"
	"net"
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

// ─── Anti-spoof throttle ──────────────────────────────────────────
//
// Viewer identity is a client-supplied opaque string (cookie or ?v=),
// which means a client can loop heartbeats with a fresh id each time
// and drive current_viewers/peak_viewers as high as it likes. The
// counters feed consumer-app reporting, so that matters.
//
// The throttle is deliberately simple and per source IP, in a fixed
// 60s window:
//
//   - at most throttleMaxBeats requests — beyond that: 429.
//   - at most throttleMaxIdentities DISTINCT viewer ids counted. Past
//     that the request still succeeds, but the identity collapses to
//     one synthetic per-IP id, so extra ids stop adding headcount.
//
// It is not a defense against a distributed spoofer — nothing here
// can be, since viewers are anonymous by design — it just makes the
// trivial single-client attack ineffective.
const (
	throttleWindow        = time.Minute
	throttleMaxBeats      = 120
	throttleMaxIdentities = 8
)

type viewerThrottle struct {
	mu  sync.Mutex
	ips map[string]*ipBucket
}

type ipBucket struct {
	windowStart time.Time
	beats       int
	ids         map[string]bool
}

func newViewerThrottle() *viewerThrottle {
	return &viewerThrottle{ips: map[string]*ipBucket{}}
}

// admit records a heartbeat from ip claiming identity id. It returns
// the identity to actually count and whether the request is allowed.
func (t *viewerThrottle) admit(ip, id string) (string, bool) {
	if t == nil || ip == "" {
		return id, true
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	b, ok := t.ips[ip]
	if !ok || now.Sub(b.windowStart) > throttleWindow {
		b = &ipBucket{windowStart: now, ids: map[string]bool{}}
		t.ips[ip] = b
	}
	b.beats++
	if b.beats > throttleMaxBeats {
		return id, false
	}
	if !b.ids[id] {
		if len(b.ids) >= throttleMaxIdentities {
			// Over the identity budget — count this beat against one
			// synthetic per-IP viewer instead of a new head.
			return "ip:" + ip, true
		}
		b.ids[id] = true
	}
	// Opportunistic GC so a long-lived sidecar doesn't accumulate a
	// bucket per IP it has ever seen.
	if len(t.ips) > 4096 {
		for k, v := range t.ips {
			if now.Sub(v.windowStart) > throttleWindow {
				delete(t.ips, k)
			}
		}
	}
	return id, true
}

// clientIP extracts the viewer's address, or "" when this request
// carries no usable one.
//
// Everything reaching this sidecar in production has been
// reverse-proxied by apteva-server, so RemoteAddr is loopback for
// every viewer on the planet — trust X-Forwarded-For (which Go's
// httputil.ReverseProxy appends), but ONLY when the immediate peer is
// loopback, i.e. our own proxy. A non-proxy peer can't talk its way
// into a different bucket.
//
// A loopback peer with no forwarded address means we genuinely can't
// tell viewers apart, and throttling them as one client would rate-
// limit a whole audience into 429s. Returning "" disables the
// throttle for that request — fail open, since the throttle is an
// anti-inflation measure, not an access control.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		fwd := r.Header.Get("X-Forwarded-For")
		if fwd == "" {
			return ""
		}
		if first := strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0]); first != "" {
			return first
		}
		return ""
	}
	return host
}

// validViewerID accepts the documented `?v=<viewer_id>` shape: a short
// opaque token. Anything else is ignored rather than trusted.
func validViewerID(v string) bool {
	if len(v) == 0 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// ─── Heartbeat handler ────────────────────────────────────────────
//
// POST /heartbeat/<stream_id>?t=<playback_token>[&v=<viewer_id>]
// Viewer identity comes from ?v= if supplied, else the apteva_viewer
// cookie, else a fresh random id. Returns:
//   { ok: true, viewer_id: "<persist and send back as ?v= next time>" }
//
// The ?v= fallback is what makes cross-origin embedding countable:
// playback deliberately sets Access-Control-Allow-Origin: * so a
// consumer app can host the player on its own domain, and a
// SameSite=Lax cookie is NOT sent on those requests — so v0.1 minted
// a brand-new random id on every single beat and one real viewer
// counted as dozens. README documented the `v` parameter; the handler
// never read it.
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

	rec, err := app.playbackFor(ctx, pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		http.NotFound(w, r)
		return
	}

	if !playbackAuthorized(rec, r.URL.Query(), time.Now()) {
		http.NotFound(w, r)
		return
	}

	// A finished stream has no live audience. v0.1 kept accepting
	// beats from replay viewers and the counter worker kept adding
	// current_viewers × 10s to total_viewer_seconds forever, inflating
	// the live session's watch stats after the fact.
	if rec.Status == "ended" || rec.Status == "errored" {
		httpErr(w, http.StatusGone, "stream is "+rec.Status)
		return
	}

	// Identity: explicit ?v= wins (cross-origin players have no
	// cookie), then the cookie, then a fresh id.
	viewerID := strings.TrimSpace(r.URL.Query().Get("v"))
	if !validViewerID(viewerID) {
		viewerID = ""
	}
	if viewerID == "" {
		if c, err := r.Cookie("apteva_viewer"); err == nil && validViewerID(c.Value) {
			viewerID = c.Value
		}
	}
	if viewerID == "" {
		viewerID = randomViewerID()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "apteva_viewer",
		Value:    viewerID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400, // 24h — same idle window-of-windows
	})

	counted, ok := app.throttle.admit(clientIP(r), viewerID)
	if !ok {
		httpErr(w, http.StatusTooManyRequests, "heartbeat rate limit")
		return
	}

	app.viewers.bump(id, counted)

	httpJSON(w, map[string]any{
		"ok":        true,
		"viewer_id": viewerID,
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
		var pid, status string
		var peak int
		err := app.AppDB().QueryRow(
			`SELECT project_id, peak_viewers, status FROM streams WHERE id = ?`,
			streamID).Scan(&pid, &peak, &status)
		if err != nil {
			a.viewers.drop(streamID)
			continue
		}
		// Terminal streams take no more watch-seconds — zero the live
		// counter once and stop tracking. (Belt and braces: the
		// heartbeat handler already refuses beats for these.)
		if status == "ended" || status == "errored" {
			_, _ = app.AppDB().Exec(
				`UPDATE streams SET current_viewers = 0 WHERE id = ?`, streamID)
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

// ─── Watchdog ─────────────────────────────────────────────────────
//
// Detects ffmpeg children that exited (the COMMON end path — the host
// stops pushing from OBS) + the idle→live transition on the first
// scraped bitrate line. Every 5s. Termination goes through
// finalizeStream, the same function streams_stop uses.

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
		opts := finalizeOpts{status: "errored", errMsg: ""}
		if err == nil {
			// Graceful publisher disconnect — the same finalization
			// streams_stop performs, recording_path included.
			opts = finalizeOpts{
				status:    "ended",
				preEvents: []string{EventKindPublisherDisconnect},
			}
		} else {
			opts.errMsg = err.Error()
		}
		if _, e := a.finalizeStream(app, pid, id, opts); e != nil {
			app.Logger().Warn("watchdog: finalize", "id", id, "err", e)
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
			_, _ = app.AppDB().Exec(
				`UPDATE streams
				 SET status = 'live', started_at = ?, current_bitrate_kbps = ?,
				     current_fps = ?, resolution = ?, dropped_frames = ?
				 WHERE id = ?`,
				nowStamp(), m.BitrateKbps, m.FPS, m.Resolution, m.DroppedFrames, id)
			var pid string
			_ = app.AppDB().QueryRow(`SELECT project_id FROM streams WHERE id = ?`, id).Scan(&pid)
			// status changed — the media handlers' cached copy is stale.
			a.invalidatePlayback(pid, id)
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
