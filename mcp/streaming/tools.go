package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	maxC := a.maxConcurrent(ctx)
	a.runnersMu.Lock()
	live := len(a.runners)
	a.runnersMu.Unlock()
	if live >= maxC {
		return nil, fmt.Errorf("at max_concurrent_streams=%d active publishers; stop one first", maxC)
	}

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
	record := boolArg(args, "record", true)
	retention := intArg(args, "retention_days", 30)
	if retention < 0 {
		retention = 0
	}

	// Insert the row first to mint an id; we'll patch storage_prefix
	// once we know it.
	res, err := ctx.AppDB().Exec(
		`INSERT INTO streams
			(project_id, name, owner_app, owner_tag,
			 ingest_protocol, ingest_port, stream_key, playback_token,
			 visibility, status, record, retention_days, storage_prefix)
		 VALUES (?, ?, ?, ?, 'rtmp', ?, ?, ?, ?, 'idle', ?, ?, '')`,
		pid, name,
		nullStr(strArg(args, "owner_app")),
		nullStr(strArg(args, "owner_tag")),
		port, streamKey, playbackToken, visibility,
		boolToInt(record), retention)
	if err != nil {
		a.ports.release(port)
		return nil, fmt.Errorf("insert stream: %w", err)
	}
	id, _ := res.LastInsertId()
	storagePrefix := fmt.Sprintf("streams/%d", id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET storage_prefix = ? WHERE id = ?`, storagePrefix, id); err != nil {
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

	a.runnersMu.Lock()
	a.runners[id] = runner
	a.runnersMu.Unlock()

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

	rows, err := ctx.AppDB().Query(
		`SELECT id FROM streams WHERE `+strings.Join(where, " AND ")+
			` ORDER BY created_at DESC LIMIT ?`,
		qargs...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	out := []*Stream{}
	for _, id := range ids {
		s, err := a.dbGet(ctx, pid, id)
		if err != nil || s == nil {
			continue
		}
		a.materializeURLs(ctx, s)
		out = append(out, s)
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

	if runner != nil {
		_ = runner.stop(5 * time.Second)
		a.ports.release(runner.port)
	}
	if a.viewers != nil {
		a.viewers.drop(id)
	}

	// Finalize: status=ended, ended_at, recording_path if present.
	now := time.Now().UTC().Format(time.RFC3339)
	recordingPath := ""
	if runner != nil && runner.recordingAvailable() {
		recordingPath = filepath.Join(s.StoragePrefix, "record.mp4")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams
		 SET status='ended', ended_at = ?, recording_path = ?
		 WHERE id = ? AND project_id = ?`,
		now, nullStr(recordingPath), id, pid); err != nil {
		return nil, err
	}

	s, _ = a.dbGet(ctx, pid, id)
	a.materializeURLs(ctx, s)
	emitStreamEvent(ctx, s, EventKindEnded, "", map[string]any{
		"peak_viewers":   s.PeakViewers,
		"recording":      recordingPath != "",
	})
	if recordingPath != "" {
		emitStreamEvent(ctx, s, EventKindRecordingFinalized, "", nil)
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

	// Stop the runner if any.
	a.runnersMu.Lock()
	runner := a.runners[id]
	delete(a.runners, id)
	a.runnersMu.Unlock()
	if runner != nil {
		_ = runner.stop(2 * time.Second)
		a.ports.release(runner.port)
	}
	if a.viewers != nil {
		a.viewers.drop(id)
	}

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

func (a *App) toolRotateKey(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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

	// Kill any active session for this stream — the new key invalidates
	// the URL the publisher is using.
	a.runnersMu.Lock()
	runner := a.runners[id]
	delete(a.runners, id)
	a.runnersMu.Unlock()
	if runner != nil {
		_ = runner.stop(2 * time.Second)
		a.ports.release(runner.port)
	}
	if a.viewers != nil {
		a.viewers.drop(id)
	}

	newKey := randomToken()
	port, err := a.ports.allocate()
	if err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams
		 SET stream_key = ?, ingest_port = ?, status='idle', error = NULL
		 WHERE id = ? AND project_id = ?`,
		newKey, port, id, pid); err != nil {
		a.ports.release(port)
		return nil, err
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
		a.ports.release(port)
		return nil, fmt.Errorf("respawn ffmpeg: %w", err)
	}
	a.runnersMu.Lock()
	a.runners[id] = newRunner
	a.runnersMu.Unlock()

	s, _ = a.dbGet(ctx, pid, id)
	a.materializeURLs(ctx, s)
	emitStreamEvent(ctx, s, EventKindKeyRotated, "", nil)
	return map[string]any{"stream": s}, nil
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
	out := map[string]any{"available": true}
	if s.RecordingPath != "" {
		out["mp4_url"] = a.publicPath(ctx) + "/streams/" + strconv.FormatInt(s.ID, 10) + "/record.mp4?t=" + s.PlaybackToken
	}
	// Segments still on disk = HLS replay.
	indexPath := filepath.Join(streamDataDir(ctx, s.StoragePrefix), "index.m3u8")
	if _, err := os.Stat(indexPath); err == nil {
		out["hls_url"] = a.publicPath(ctx) + "/streams/" + strconv.FormatInt(s.ID, 10) + "/index.m3u8?t=" + s.PlaybackToken
	}
	return out, nil
}

// ─── DB helpers ───────────────────────────────────────────────────

func (a *App) dbGet(ctx *sdk.AppCtx, pid string, id int64) (*Stream, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, name,
				COALESCE(owner_app,''), COALESCE(owner_tag,''),
				ingest_protocol, COALESCE(ingest_port, 0),
				stream_key, playback_token, visibility,
				status, record, retention_days, storage_prefix,
				COALESCE(recording_path,''),
				COALESCE(current_bitrate_kbps, 0),
				COALESCE(current_fps, 0),
				COALESCE(resolution,''), dropped_frames,
				current_viewers, peak_viewers, total_viewer_seconds,
				created_at, COALESCE(started_at,''), COALESCE(ended_at,''),
				COALESCE(error,'')
		 FROM streams WHERE id = ? AND project_id = ?`,
		id, pid)

	s := &Stream{}
	var record int
	if err := row.Scan(
		&s.ID, &s.ProjectID, &s.Name,
		&s.OwnerApp, &s.OwnerTag,
		&s.IngestProtocol, &s.IngestPort,
		&s.StreamKey, &s.PlaybackToken, &s.Visibility,
		&s.Status, &record, &s.RetentionDays, &s.StoragePrefix,
		&s.RecordingPath,
		&s.CurrentBitrateKbps, &s.CurrentFPS,
		&s.Resolution, &s.DroppedFrames,
		&s.CurrentViewers, &s.PeakViewers, &s.TotalViewerSeconds,
		&s.CreatedAt, &s.StartedAt, &s.EndedAt,
		&s.Error,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	s.Record = record != 0
	return s, nil
}

// materializeURLs fills ingest_url and playback_url from the stream's
// stored fields + the platform's public URL. Done at read time so a
// settings change to PUBLIC_URL takes effect without a row migration.
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

	httpsBase := a.publicPath(ctx)
	s.PlaybackURL = fmt.Sprintf("%s/streams/%d/index.m3u8?t=%s",
		httpsBase, s.ID, s.PlaybackToken)
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
