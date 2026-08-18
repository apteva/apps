package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── streams_create ───────────────────────────────────────────────

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}

	// Cap concurrency BEFORE allocating a port — refuse cleanly when
	// the operator's max_concurrent_streams is the binding constraint.
	// The reservation and the check are one critical section (see
	// reserveSlot): v0.1 split them and two concurrent creates could
	// both pass the cap.
	maxC := a.maxConcurrent(ctx)
	if !a.reserveSlot(maxC) {
		return nil, fmt.Errorf("at max_concurrent_streams=%d active publishers; stop one first", maxC)
	}
	committed := false
	defer func() {
		if !committed {
			a.releaseSlot()
		}
	}()

	port, err := a.ports.allocate()
	if err != nil {
		return nil, err
	}

	visibility := strArg(args, "visibility")
	if visibility == "" {
		visibility = "signed"
	}
	if visibility != "signed" && visibility != "public" {
		a.ports.release(port)
		return nil, fmt.Errorf("visibility must be signed|public, got %q", visibility)
	}

	streamKey := randomToken()
	playbackToken := randomToken()
	signingSecret := randomToken()
	record := boolArg(args, "record", true)
	retention := intArg(args, "retention_days", 30)
	if retention < 0 {
		retention = 0
	}

	// Insert the row first to mint an id; we'll patch storage_prefix
	// once we know it. created_at is written explicitly so it matches
	// the RFC3339 the rest of the app writes (the column DEFAULT would
	// give SQLite's "YYYY-MM-DD HH:MM:SS").
	res, err := ctx.AppDB().Exec(
		`INSERT INTO streams
			(project_id, name, owner_app, owner_tag,
			 ingest_protocol, ingest_port, stream_key, playback_token,
			 url_signing_secret, visibility, status, record,
			 retention_days, storage_prefix, created_at)
		 VALUES (?, ?, ?, ?, 'rtmp', ?, ?, ?, ?, ?, 'idle', ?, ?, '', ?)`,
		pid, name,
		nullStr(strArg(args, "owner_app")),
		nullStr(strArg(args, "owner_tag")),
		port, streamKey, playbackToken, signingSecret, visibility,
		boolToInt(record), retention, nowStamp())
	if err != nil {
		a.ports.release(port)
		return nil, fmt.Errorf("insert stream: %w", err)
	}
	id, _ := res.LastInsertId()
	storagePrefix := fmt.Sprintf("streams/%d", id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET storage_prefix = ? WHERE id = ?`, storagePrefix, id); err != nil {
		// The row exists but has no storage prefix — it would be an
		// orphan nothing can serve or clean up. Same rollback the
		// spawn-failure path does.
		_, _ = ctx.AppDB().Exec(`DELETE FROM streams WHERE id = ?`, id)
		a.ports.release(port)
		return nil, fmt.Errorf("update prefix: %w", err)
	}

	// Spawn the runner. If it fails, roll back the row + port.
	runner, err := a.runnerFactory(runnerOpts{
		streamID:  id,
		port:      port,
		ffmpegBin: a.ffmpegPath(ctx),
		dataDir:   streamDataDir(ctx, storagePrefix),
		streamKey: streamKey,
		hlsTime:   a.hlsSegmentSeconds(ctx),
		hlsWindow: a.hlsWindowSegments(ctx),
		record:    record,
	})
	if err != nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM streams WHERE id = ?`, id)
		a.ports.release(port)
		return nil, fmt.Errorf("spawn ffmpeg: %w", err)
	}

	a.commitRunner(id, runner)
	committed = true

	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		// Don't tear down on a read error — the runner is fine.
		return nil, err
	}
	a.materializeURLs(ctx, s)
	emitStreamEvent(ctx, s, EventKindCreated, "", nil)

	return map[string]any{"stream": s}, nil
}

// ─── streams_get ──────────────────────────────────────────────────

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return map[string]any{"stream": nil, "found": false}, nil
	}
	a.materializeURLs(ctx, s)
	return map[string]any{"stream": s, "found": true}, nil
}

// ─── streams_list ─────────────────────────────────────────────────

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"project_id = ?"}
	qargs := []any{pid}
	if v := strArg(args, "status"); v != "" {
		where = append(where, "status = ?")
		qargs = append(qargs, v)
	}
	if v := strArg(args, "owner_app"); v != "" {
		where = append(where, "owner_app = ?")
		qargs = append(qargs, v)
	}
	if v := strArg(args, "owner_tag"); v != "" {
		where = append(where, "owner_tag = ?")
		qargs = append(qargs, v)
	}
	qargs = append(qargs, limit)

	// One query for everything. v0.1 selected ids and then ran a
	// full-row dbGet per id — 2N+1 round-trips down the DB's single
	// connection, competing with every viewer's manifest fetch.
	rows, err := ctx.AppDB().Query(
		`SELECT `+streamColumns+` FROM streams WHERE `+strings.Join(where, " AND ")+
			` ORDER BY created_at DESC LIMIT ?`,
		qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Stream{}
	for rows.Next() {
		s, err := scanStream(rows)
		if err != nil {
			return nil, err
		}
		a.materializeURLs(ctx, s)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"streams": out, "count": len(out)}, nil
}

// ─── streams_stop ─────────────────────────────────────────────────

func (a *App) toolStop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("stream not found")
	}
	if s.Status == "ended" || s.Status == "errored" {
		return map[string]any{"stream": s, "noop": true}, nil
	}

	a.runnersMu.Lock()
	runner := a.runners[id]
	delete(a.runners, id)
	a.runnersMu.Unlock()

	// Generous grace when recording: +faststart rewrites the whole mp4
	// on close and a SIGKILL mid-rewrite truncates it.
	a.stopRunner(ctx, runner)
	if a.viewers != nil {
		a.viewers.drop(id)
	}

	// One shared finalization for both the explicit-stop and the
	// publisher-disconnect paths — see finalizeStream.
	s, err = a.finalizeStream(ctx, pid, id, finalizeOpts{status: "ended"})
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("stream not found")
	}
	return map[string]any{"stream": s}, nil
}

// ─── streams_delete ───────────────────────────────────────────────

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		// Idempotent — already gone.
		return map[string]any{"deleted": true}, nil
	}

	// Stop the runner if any. Short grace on purpose: everything the
	// long grace protects (the mp4's moov rewrite) is about to be
	// deleted anyway.
	a.runnersMu.Lock()
	runner := a.runners[id]
	delete(a.runners, id)
	a.runnersMu.Unlock()
	if runner != nil {
		_ = runner.stop(5 * time.Second)
		a.ports.release(runner.port)
	}
	if a.viewers != nil {
		a.viewers.drop(id)
	}
	a.invalidatePlayback(pid, id)

	// Delete the disk dir. Best-effort — log on failure but proceed.
	dir := streamDataDir(ctx, s.StoragePrefix)
	if err := os.RemoveAll(dir); err != nil {
		ctx.Logger().Warn("streams_delete: rmdir", "id", id, "dir", dir, "err", err)
	}

	// Delete the row (cascade drops viewers + events).
	if _, err := ctx.AppDB().Exec(`DELETE FROM streams WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return nil, err
	}
	ctx.Emit("stream.deleted", map[string]any{"id": id})
	return map[string]any{"deleted": true}, nil
}

// ─── streams_rotate_key ───────────────────────────────────────────
//
// Two modes:
//
//   - idle|live stream: kill the session, mint a new stream_key, take
//     a fresh port, respawn the listener.
//   - any status, rotate_playback_token=true: also mint a new
//     playback_token + url_signing_secret, which invalidates every
//     playback/replay URL outstanding for the stream. This is the
//     revocation path a consumer app calls when a replay link leaks,
//     and it's the ONLY thing rotate does for a terminal stream.
//
// v0.1 rotated any status unconditionally, which resurrected an ended
// stream to `idle` and spawned ffmpeg for it — bypassing the
// max_concurrent_streams cap streams_create enforces.
func (a *App) toolRotateKey(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	rotatePlayback := boolArg(args, "rotate_playback_token", false)

	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("stream not found")
	}

	terminal := s.Status == "ended" || s.Status == "errored"
	if terminal && !rotatePlayback {
		return nil, fmt.Errorf("stream is %s — rotating its ingest key would resurrect it; "+
			"pass rotate_playback_token=true to revoke its playback URLs instead", s.Status)
	}
	if !terminal && s.Status != "idle" && s.Status != "live" {
		return nil, fmt.Errorf("cannot rotate a stream in status %q", s.Status)
	}

	// Kill any active session for this stream — the new key invalidates
	// the URL the publisher is using. Full grace: the old session's
	// recording still has to be flushed.
	a.runnersMu.Lock()
	runner := a.runners[id]
	delete(a.runners, id)
	a.runnersMu.Unlock()
	a.stopRunner(ctx, runner)
	if a.viewers != nil {
		a.viewers.drop(id)
	}

	// Terminal streams stop here: revoke the playback credentials, no
	// respawn, no status change.
	if terminal {
		if err := a.rotatePlaybackCreds(ctx, pid, id); err != nil {
			return nil, err
		}
		s, _ = a.dbGet(ctx, pid, id)
		a.materializeURLs(ctx, s)
		emitStreamEvent(ctx, s, EventKindKeyRotated, "playback credentials revoked", nil)
		return map[string]any{"stream": s, "respawned": false}, nil
	}

	// The old session is definitively dead from here on (we killed it,
	// and its port went back to the pool), so EVERY failure path below
	// has to leave the row saying so. v0.1 left it at status='idle'
	// advertising an ingest_port it no longer owned — the port could be
	// handed to another stream while this row kept publishing it, and
	// no runner existed to ever clean it up.
	//
	// Note this is deliberately not a restore of the pre-rotation
	// values: the old key is the one being rotated away, usually
	// because it leaked, and the old session can't be resumed. The
	// honest end state is a terminal row that claims no port.
	abandon := func(cause error) {
		if _, err := ctx.AppDB().Exec(
			`UPDATE streams SET status='errored', ingest_port = NULL, ended_at = ?, error = ?
			 WHERE id = ? AND project_id = ?`,
			nowStamp(), "key rotation failed: "+cause.Error(), id, pid); err != nil {
			ctx.Logger().Warn("rotate: abandon", "id", id, "err", err)
		}
		a.invalidatePlayback(pid, id)
	}

	// A rotation re-enters the "one running ffmpeg" pool, so it has to
	// respect the same cap create does. Our own runner is already out
	// of the map at this point, so the slot we vacated is available.
	maxC := a.maxConcurrent(ctx)
	if !a.reserveSlot(maxC) {
		err := fmt.Errorf("at max_concurrent_streams=%d active publishers; stop one first", maxC)
		abandon(err)
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			a.releaseSlot()
		}
	}()

	newKey := randomToken()
	port, err := a.ports.allocate()
	if err != nil {
		abandon(err)
		return nil, err
	}
	rollback := func(cause error) {
		a.ports.release(port)
		abandon(cause)
	}

	if _, err := ctx.AppDB().Exec(
		`UPDATE streams
		 SET stream_key = ?, ingest_port = ?, status='idle', error = NULL
		 WHERE id = ? AND project_id = ?`,
		newKey, port, id, pid); err != nil {
		rollback(err)
		return nil, err
	}
	a.invalidatePlayback(pid, id)
	if rotatePlayback {
		if err := a.rotatePlaybackCreds(ctx, pid, id); err != nil {
			rollback(err)
			return nil, err
		}
	}

	// Re-spawn ffmpeg with the new key.
	newRunner, err := a.runnerFactory(runnerOpts{
		streamID:  id,
		port:      port,
		ffmpegBin: a.ffmpegPath(ctx),
		dataDir:   streamDataDir(ctx, s.StoragePrefix),
		streamKey: newKey,
		hlsTime:   a.hlsSegmentSeconds(ctx),
		hlsWindow: a.hlsWindowSegments(ctx),
		record:    s.Record,
	})
	if err != nil {
		rollback(err)
		return nil, fmt.Errorf("respawn ffmpeg: %w", err)
	}
	a.commitRunner(id, newRunner)
	committed = true

	s, _ = a.dbGet(ctx, pid, id)
	a.materializeURLs(ctx, s)
	emitStreamEvent(ctx, s, EventKindKeyRotated, "", nil)
	return map[string]any{"stream": s, "respawned": true}, nil
}

// rotatePlaybackCreds mints a fresh playback_token + url_signing_secret,
// invalidating every URL previously handed out for the stream.
func (a *App) rotatePlaybackCreds(ctx *sdk.AppCtx, pid string, id int64) error {
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET playback_token = ?, url_signing_secret = ?
		 WHERE id = ? AND project_id = ?`,
		randomToken(), randomToken(), id, pid); err != nil {
		return err
	}
	a.invalidatePlayback(pid, id)
	return nil
}

// ─── streams_signed_url ───────────────────────────────────────────

func (a *App) toolSignedURL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	ttl := intArg(args, "expires_in_seconds", 0)
	if ttl <= 0 {
		return nil, errors.New("expires_in_seconds must be > 0")
	}
	kind := strings.TrimSpace(strArg(args, "kind"))
	if kind == "" {
		kind = "hls"
	}
	if kind != "hls" && kind != "mp4" && kind != "heartbeat" {
		return nil, fmt.Errorf(`kind must be hls|mp4|heartbeat, got %q`, kind)
	}

	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("stream not found")
	}
	if err := a.ensureSigningSecret(ctx, s); err != nil {
		return nil, err
	}

	// The heartbeat endpoint runs the same playbackAuthorized gate as
	// the media routes, so a consumer app that flips require_signed_urls
	// needs a signed heartbeat URL too — without one its viewer counts
	// silently drop to zero while playback keeps working.
	if kind == "heartbeat" {
		exp := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
		return map[string]any{
			"url":        a.heartbeatURL(ctx, s, exp),
			"expires_at": exp,
			"kind":       kind,
		}, nil
	}

	file := indexPlaylistFile
	switch kind {
	case "mp4":
		if s.Status != "ended" {
			return nil, fmt.Errorf("recording is only servable once the stream has ended (status=%s)", s.Status)
		}
		if s.RecordingPath == "" {
			return nil, errors.New("stream has no finalized recording")
		}
		file = recordingFile
	case "hls":
		// Finished streams get the complete VOD manifest; live ones the
		// rolling-window playlist.
		if s.Status == "ended" && replayPlaylistExists(ctx, s.StoragePrefix) {
			file = replayPlaylistFile
		}
	}

	exp := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
	return map[string]any{
		"url":        a.mediaURL(ctx, s, file, exp),
		"expires_at": exp,
		"kind":       kind,
	}, nil
}

// ─── streams_set_url_policy ───────────────────────────────────────

func (a *App) toolSetURLPolicy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, ok := args["require_signed_urls"]; !ok {
		return nil, errors.New("require_signed_urls required")
	}
	require := boolArg(args, "require_signed_urls", false)

	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("stream not found")
	}
	if require {
		// Requiring signatures against an empty secret would lock the
		// stream out entirely.
		if err := a.ensureSigningSecret(ctx, s); err != nil {
			return nil, err
		}
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET require_signed_urls = ? WHERE id = ? AND project_id = ?`,
		boolToInt(require), id, pid); err != nil {
		return nil, err
	}
	a.invalidatePlayback(pid, id)

	s, _ = a.dbGet(ctx, pid, id)
	a.materializeURLs(ctx, s)
	emitStreamEvent(ctx, s, EventKindURLPolicyChanged, "", map[string]any{
		"require_signed_urls": require,
	})
	return map[string]any{"stream": s, "require_signed_urls": require}, nil
}

// ─── streams_get_metrics ──────────────────────────────────────────

func (a *App) toolGetMetrics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("stream not found")
	}

	// Pull live data from the runner if alive, fall back to last
	// persisted values.
	a.runnersMu.Lock()
	runner := a.runners[id]
	a.runnersMu.Unlock()

	// current_viewers is read live from the in-memory tracker so the
	// metric reflects this instant, not the worker's last sweep.
	currentViewers := 0
	if a.viewers != nil {
		currentViewers = a.viewers.count(id)
	} else {
		currentViewers = s.CurrentViewers
	}

	out := map[string]any{
		"id":                   s.ID,
		"status":               s.Status,
		"current_bitrate_kbps": s.CurrentBitrateKbps,
		"current_fps":          s.CurrentFPS,
		"resolution":           s.Resolution,
		"dropped_frames":       s.DroppedFrames,
		"current_viewers":      currentViewers,
		"peak_viewers":         s.PeakViewers,
		"total_viewer_seconds": s.TotalViewerSeconds,
		"uptime_seconds":       0,
	}
	if runner != nil {
		m := runner.metrics()
		out["current_bitrate_kbps"] = m.BitrateKbps
		out["current_fps"] = m.FPS
		out["resolution"] = m.Resolution
		out["dropped_frames"] = m.DroppedFrames
		out["uptime_seconds"] = m.UptimeSeconds
	}
	return out, nil
}

// ─── streams_replay_url ───────────────────────────────────────────

func (a *App) toolReplayURL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("stream not found")
	}
	if s.Status != "ended" {
		return map[string]any{"available": false, "reason": "stream is " + s.Status}, nil
	}

	// A stream under a signed-URL policy has no usable plain URL, so
	// hand back a signed one with a default lifetime. Callers that
	// want to choose the lifetime use streams_signed_url.
	var exp int64
	if s.RequireSignedURLs {
		if err := a.ensureSigningSecret(ctx, s); err != nil {
			return nil, err
		}
		exp = time.Now().Add(replayURLTTL).Unix()
	}

	out := map[string]any{"available": true}
	if s.RecordingPath != "" {
		out["mp4_url"] = a.mediaURL(ctx, s, recordingFile, exp)
	}
	// Segments still on disk = HLS replay. Prefer the complete VOD
	// manifest finalize wrote; fall back to the live one (which only
	// covers the rolling window) for streams finalized by v0.1.
	dir := streamDataDir(ctx, s.StoragePrefix)
	switch {
	case replayPlaylistExists(ctx, s.StoragePrefix):
		out["hls_url"] = a.mediaURL(ctx, s, replayPlaylistFile, exp)
	default:
		if _, err := os.Stat(filepath.Join(dir, indexPlaylistFile)); err == nil {
			out["hls_url"] = a.mediaURL(ctx, s, indexPlaylistFile, exp)
		}
	}
	if exp > 0 {
		out["expires_at"] = exp
	}
	return out, nil
}

// replayURLTTL is the lifetime of the signed URLs streams_replay_url
// mints for streams that require signatures.
const replayURLTTL = time.Hour

// ─── DB helpers ───────────────────────────────────────────────────

// streamColumns is the full projection behind both dbGet and
// toolList, kept in one place so the two can't scan different shapes.
const streamColumns = `id, project_id, name,
		COALESCE(owner_app,''), COALESCE(owner_tag,''),
		ingest_protocol, COALESCE(ingest_port, 0),
		stream_key, playback_token, COALESCE(url_signing_secret,''),
		require_signed_urls, visibility,
		status, record, retention_days, storage_prefix,
		COALESCE(recording_path,''),
		COALESCE(current_bitrate_kbps, 0),
		COALESCE(current_fps, 0),
		COALESCE(resolution,''), dropped_frames,
		current_viewers, peak_viewers, total_viewer_seconds,
		created_at, COALESCE(started_at,''), COALESCE(ended_at,''),
		COALESCE(pruned_at,''), COALESCE(error,'')`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanStream(row rowScanner) (*Stream, error) {
	s := &Stream{}
	var record, requireSigned int
	if err := row.Scan(
		&s.ID, &s.ProjectID, &s.Name,
		&s.OwnerApp, &s.OwnerTag,
		&s.IngestProtocol, &s.IngestPort,
		&s.StreamKey, &s.PlaybackToken, &s.URLSigningSecret,
		&requireSigned, &s.Visibility,
		&s.Status, &record, &s.RetentionDays, &s.StoragePrefix,
		&s.RecordingPath,
		&s.CurrentBitrateKbps, &s.CurrentFPS,
		&s.Resolution, &s.DroppedFrames,
		&s.CurrentViewers, &s.PeakViewers, &s.TotalViewerSeconds,
		&s.CreatedAt, &s.StartedAt, &s.EndedAt,
		&s.PrunedAt, &s.Error,
	); err != nil {
		return nil, err
	}
	s.Record = record != 0
	s.RequireSignedURLs = requireSigned != 0
	return s, nil
}

func (a *App) dbGet(ctx *sdk.AppCtx, pid string, id int64) (*Stream, error) {
	s, err := scanStream(ctx.AppDB().QueryRow(
		`SELECT `+streamColumns+` FROM streams WHERE id = ? AND project_id = ?`,
		id, pid))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return s, nil
}

// materializeURLs fills ingest_url, playback_url and heartbeat_url
// from the stream's stored fields + the platform's public URL. Done at
// read time so a settings change to PUBLIC_URL takes effect without a
// row migration.
//
// The playback/heartbeat URLs carry project_id on a global-scoped
// install — see urlProjectID. They're unsigned unless the stream's
// policy demands a signature, in which case they get the default
// lifetime; callers that want to choose the lifetime use
// streams_signed_url.
func (a *App) materializeURLs(ctx *sdk.AppCtx, s *Stream) {
	if s == nil {
		return
	}
	host := a.publicURL(ctx)
	rtmpHost := host
	if rtmpHost == "" {
		rtmpHost = "rtmp://localhost"
	} else {
		// Translate https://host[:port]/ to rtmp://host. Port stays
		// distinct because RTMP is on its own port, not the HTTPS one.
		rtmpHost = "rtmp://" + stripScheme(host)
	}
	s.IngestURL = fmt.Sprintf("%s:%d/live/%s", rtmpHost, s.IngestPort, s.StreamKey)

	playbackFile := indexPlaylistFile
	if s.Status == "ended" && replayPlaylistExists(ctx, s.StoragePrefix) {
		playbackFile = replayPlaylistFile
	}
	// A stream under a signed-URL policy has no working plain URL, so
	// don't hand one back.
	var exp int64
	if s.RequireSignedURLs && s.URLSigningSecret != "" {
		exp = time.Now().Add(replayURLTTL).Unix()
	}
	s.PlaybackURL = a.mediaURL(ctx, s, playbackFile, exp)
	s.HeartbeatURL = a.heartbeatURL(ctx, s, exp)
}

// publicPath returns the URL prefix viewers use to reach this sidecar's
// HTTP routes through apteva-server's reverse proxy. Falls back to a
// relative prefix if PUBLIC_URL is unset (dev/local).
func (a *App) publicPath(ctx *sdk.AppCtx) string {
	host := a.publicURL(ctx)
	if host == "" {
		return "/api/apps/streaming"
	}
	return host + "/api/apps/streaming"
}

func stripScheme(u string) string {
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(u, p) {
			return strings.TrimPrefix(u, p)
		}
	}
	return u
}
