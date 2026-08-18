package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── In-memory attendance accumulator ─────────────────────────────
//
// The live room heartbeats every 10s per viewer. Writing that straight
// through means 1000 viewers = 100 writes/sec on a pool the SDK caps at
// ONE connection, interleaved with chat inserts and the events poll's
// ~3000 reads/sec. The sibling `streaming` app already solved the
// aggregate version of this with an in-memory tracker + a periodic
// flush; this is the per-identity equivalent.
//
// Two correctness bugs from the write-through version are fixed here:
//
//   - The old upsert never cleared `left_at`. One idle blip (phone
//     locks, tab backgrounds) had the decay worker stamp left_at and
//     nothing ever unstamped it, so the viewer read as "left" for the
//     rest of the webinar while watch_seconds kept climbing. The flush
//     clears left_at on every beat.
//   - watch_seconds was bumped by a hardcoded +10 per heartbeat with no
//     reference to elapsed time, so a registrant could POST the
//     heartbeat endpoint in a loop and mint arbitrary watch time (and
//     an attended_live flag) for themselves. Credit is now the real
//     wall-clock gap since that viewer's previous beat, clamped.

type attendanceKey struct {
	RegistrantID int64
	Source       string // live | replay
}

type attendanceEntry struct {
	ProjectID string
	WebinarID int64
	JoinedAt  time.Time
	LastBeat  time.Time
	Pending   int  // watch-seconds credited since the last flush
	Dirty     bool // has state the flush hasn't written yet
}

// attendanceFlush is one row's worth of work handed to the flusher.
type attendanceFlush struct {
	Key       attendanceKey
	ProjectID string
	WebinarID int64
	JoinedAt  time.Time
	LastBeat  time.Time
	Seconds   int
}

type attendanceTracker struct {
	mu      sync.Mutex
	entries map[attendanceKey]*attendanceEntry
}

func newAttendanceTracker() *attendanceTracker {
	return &attendanceTracker{entries: map[attendanceKey]*attendanceEntry{}}
}

// record books one heartbeat and returns the watch-seconds credited.
//
// interval is the room's configured heartbeat cadence: it's the credit
// granted for the first beat of a session (the viewer just arrived and
// will watch at least one interval) and, doubled, the ceiling on any
// single beat's credit. A viewer hammering the endpoint gets ~0 credit
// per extra beat because the wall-clock gap is ~0; a viewer whose
// browser was suspended for an hour gets the ceiling, not the hour.
func (t *attendanceTracker) record(key attendanceKey, projectID string, webinarID int64, now time.Time, interval time.Duration) int {
	if t == nil {
		return 0
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	maxCredit := 2 * interval

	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[key]
	if !ok {
		e = &attendanceEntry{
			ProjectID: projectID,
			WebinarID: webinarID,
			JoinedAt:  now,
			LastBeat:  now,
		}
		t.entries[key] = e
		e.Pending = int(interval / time.Second)
		e.Dirty = true
		return e.Pending
	}

	elapsed := now.Sub(e.LastBeat)
	credit := 0
	switch {
	case elapsed <= 0:
		credit = 0
	case elapsed > maxCredit:
		credit = int(maxCredit / time.Second)
	default:
		credit = int(elapsed / time.Second)
	}
	e.ProjectID = projectID
	e.WebinarID = webinarID
	e.LastBeat = now
	e.Pending += credit
	e.Dirty = true
	return credit
}

// drain snapshots every dirty entry and resets its pending counter. The
// entries themselves stay resident so LastBeat survives across flushes —
// that's what makes the elapsed-time clamp meaningful.
func (t *attendanceTracker) drain() []attendanceFlush {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]attendanceFlush, 0, len(t.entries))
	for key, e := range t.entries {
		if !e.Dirty {
			continue
		}
		out = append(out, attendanceFlush{
			Key:       key,
			ProjectID: e.ProjectID,
			WebinarID: e.WebinarID,
			JoinedAt:  e.JoinedAt,
			LastBeat:  e.LastBeat,
			Seconds:   e.Pending,
		})
		e.Pending = 0
		e.Dirty = false
	}
	return out
}

// evictIdle drops clean entries whose last beat is older than idle, so a
// long-running sidecar's map tracks live viewers rather than everyone
// who ever joined.
func (t *attendanceTracker) evictIdle(now time.Time, idle time.Duration) {
	if t == nil || idle <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, e := range t.entries {
		if e.Dirty {
			continue
		}
		if now.Sub(e.LastBeat) > idle {
			delete(t.entries, key)
		}
	}
}

// size reports the number of tracked viewers. Test + diagnostics hook.
func (t *attendanceTracker) size() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// ─── Public helper for the live-room heartbeat handler ────────────

// RecordHeartbeat books one live-room heartbeat for this registrant in
// memory and returns the watch-seconds credited (0 when the beat is a
// duplicate or arrived faster than real time). Nothing touches SQLite
// on this path — the attendance-flush worker writes the accumulated
// batch every 15s.
//
// Source is derived from the webinar's own status so callers don't have
// to: a heartbeat against an ended webinar is replay watch time.
func (a *App) RecordHeartbeat(ctx *sdk.AppCtx, w *Webinar, reg *Registrant) int {
	if a == nil || w == nil || reg == nil {
		return 0
	}
	a.ensureState()
	source := "live"
	if w.Status == "ended" || w.Status == "cancelled" {
		source = "replay"
	}
	interval := time.Duration(a.heartbeatIntervalSeconds(ctx)) * time.Second
	return a.attendance.record(
		attendanceKey{RegistrantID: reg.ID, Source: source},
		w.ProjectID, w.ID, time.Now().UTC(), interval)
}

// ─── Worker: attendance-flush ─────────────────────────────────────
//
// Every 15s: write the accumulated batch in ONE transaction, promote
// attended_live / attended_replay for exactly the registrants in that
// batch, then evict idle viewers from the map.

func (a *App) runAttendanceFlush(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	a.ensureState()
	batch := a.attendance.drain()
	if len(batch) > 0 {
		if err := a.flushAttendance(app, batch); err != nil {
			app.Logger().Warn("attendance-flush", "rows", len(batch), "err", err)
		}
	}
	idle := time.Duration(a.viewerIdleSeconds(app)*4) * time.Second
	a.attendance.evictIdle(time.Now().UTC(), idle)
	return nil
}

// flushAttendance upserts the batch and promotes the attendance flags,
// all inside one transaction so the single writer takes one commit
// instead of one per viewer.
func (a *App) flushAttendance(app *sdk.AppCtx, batch []attendanceFlush) error {
	tx, err := app.AppDB().Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(
		`INSERT INTO webinar_attendance
			(project_id, webinar_id, registrant_id, source,
			 joined_at, last_heartbeat, watch_seconds)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(registrant_id, source) DO UPDATE SET
			last_heartbeat = excluded.last_heartbeat,
			watch_seconds  = watch_seconds + excluded.watch_seconds,
			left_at        = NULL`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	liveIDs := []int64{}
	replayIDs := []int64{}
	for _, f := range batch {
		if _, err := stmt.Exec(
			f.ProjectID, f.WebinarID, f.Key.RegistrantID, f.Key.Source,
			formatRFC3339(f.JoinedAt), formatRFC3339(f.LastBeat), f.Seconds); err != nil {
			return err
		}
		if f.Key.Source == "replay" {
			replayIDs = append(replayIDs, f.Key.RegistrantID)
		} else {
			liveIDs = append(liveIDs, f.Key.RegistrantID)
		}
	}

	// Promote the flags for exactly the registrants we just touched. The
	// decay worker's broader sweep is a backstop; this is the hot path
	// and it stays O(batch).
	if err := promoteAttendedFlag(tx, "attended_live", liveIDs); err != nil {
		return err
	}
	if err := promoteAttendedFlag(tx, "attended_replay", replayIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// sqlExecer is the slice of *sql.Tx / *sql.DB the batch writers need, so
// the same helper works inside and outside a transaction.
type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// promoteAttendedFlag flips one attendance flag for a bounded id list.
func promoteAttendedFlag(tx sqlExecer, column string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	const chunk = 200
	for start := 0; start < len(ids); start += chunk {
		end := minInt(start+chunk, len(ids))
		part := ids[start:end]
		args := make([]any, 0, len(part))
		for _, id := range part {
			args = append(args, id)
		}
		q := fmt.Sprintf(
			`UPDATE webinar_registrants SET %s = 1 WHERE %s = 0 AND id IN (%s)`,
			column, column, placeholders(len(part)))
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return nil
}

// placeholders renders "?, ?, ?" for an IN clause of n values.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// ─── Worker: retention-prune ──────────────────────────────────────
//
// Nothing used to prune the append-only tables (chat, offer clicks,
// reminders, attendance). Unbounded growth is what turns the decay
// worker's sweeps into progressively slower full scans, so retention is
// part of the same fix rather than a nice-to-have.
//
// Deletes run in bounded batches so the single writer never takes one
// enormous statement. `retention_days` = 0 disables pruning entirely.

func (a *App) runRetentionPrune(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	days := a.retentionDays(app)
	if days <= 0 {
		return nil
	}
	cutoff := formatRFC3339(time.Now().UTC().AddDate(0, 0, -days))

	type pruneSpec struct {
		name  string
		query string
	}
	specs := []pruneSpec{
		{"chat", `DELETE FROM webinar_chat WHERE id IN (
			SELECT id FROM webinar_chat WHERE created_at < ? LIMIT ?)`},
		{"offer_clicks", `DELETE FROM webinar_offer_clicks WHERE id IN (
			SELECT id FROM webinar_offer_clicks WHERE clicked_at < ? LIMIT ?)`},
		// Pending reminders are never pruned — a pending row past the
		// cutoff is a bug to investigate, not garbage to sweep.
		{"reminders", `DELETE FROM webinar_reminders WHERE id IN (
			SELECT id FROM webinar_reminders
			 WHERE status <> 'pending' AND COALESCE(sent_at, scheduled_for) < ? LIMIT ?)`},
		// Attendance only goes once its webinar is finished, so an
		// in-flight long-running webinar can't lose rows mid-flight.
		{"attendance", `DELETE FROM webinar_attendance WHERE id IN (
			SELECT att.id FROM webinar_attendance att
			  JOIN webinars w ON w.id = att.webinar_id
			 WHERE att.last_heartbeat < ? AND w.status IN ('ended','cancelled') LIMIT ?)`},
	}

	const batchSize = 2000
	const maxBatches = 10
	for _, spec := range specs {
		for i := 0; i < maxBatches; i++ {
			res, err := app.AppDB().Exec(spec.query, cutoff, batchSize)
			if err != nil {
				app.Logger().Warn("retention-prune", "table", spec.name, "err", err)
				break
			}
			n, _ := res.RowsAffected()
			if n < batchSize {
				break
			}
		}
	}
	return nil
}

// ─── Config accessors ─────────────────────────────────────────────

// configInt reads an integer config field with a default.
func configInt(ctx *sdk.AppCtx, key string, def int) int {
	if ctx == nil {
		return def
	}
	raw := strings.TrimSpace(ctx.Config().Get(key))
	if raw == "" {
		return def
	}
	return intArg(map[string]any{"v": raw}, "v", def)
}

func (a *App) heartbeatIntervalSeconds(ctx *sdk.AppCtx) int {
	n := configInt(ctx, "heartbeat_interval_s", 10)
	if n <= 0 {
		n = 10
	}
	return n
}

func (a *App) viewerIdleSeconds(ctx *sdk.AppCtx) int {
	n := configInt(ctx, "viewer_idle_seconds", 30)
	if n <= 0 {
		n = 30
	}
	return n
}

func (a *App) retentionDays(ctx *sdk.AppCtx) int {
	return configInt(ctx, "retention_days", 180)
}
