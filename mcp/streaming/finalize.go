package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── The one end-of-stream transition ─────────────────────────────
//
// v0.1 had two copies of this logic — streams_stop's and the
// watchdog's — and they had already drifted: the watchdog's UPDATE
// wrote status/ended_at/error but NOT recording_path. That's the
// common path (host clicks Stop in OBS → publisher disconnects →
// ffmpeg exits 0 → watchdog marks `ended`), so every recording made
// that way was stranded on disk: streams_replay_url saw an empty
// recording_path and never emitted mp4_url, and a later streams_stop
// was a no-op because the status was already terminal.
//
// One function now, called from both paths.

// finalizeOpts parameterizes finalizeStream.
type finalizeOpts struct {
	// status is the terminal status to write: "ended" or "errored".
	status string
	// errMsg lands in streams.error; empty clears it.
	errMsg string
	// preEvents are emitted before the terminal event — the watchdog
	// uses it to record publisher_disconnect ahead of ended.
	preEvents []string
}

// finalizeStream flips a stream to its terminal status and does every
// piece of end-of-stream bookkeeping exactly once: ended_at (RFC3339
// UTC), error text, recording_path when a *complete* mp4 is on disk,
// the VOD replay playlist, playback-cache invalidation, and the
// lifecycle events (including recording_finalized).
//
// Returns the reloaded row with URLs materialized, or (nil, nil) when
// the row vanished under us.
func (a *App) finalizeStream(ctx *sdk.AppCtx, pid string, id int64, opts finalizeOpts) (*Stream, error) {
	s, err := a.dbGet(ctx, pid, id)
	if err != nil || s == nil {
		return nil, err
	}

	dir := streamDataDir(ctx, s.StoragePrefix)

	// The recording only counts as finalized when ffmpeg actually got
	// to write the moov atom — see mp4HasMoov.
	recordingPath := ""
	if s.Record && mp4HasMoov(filepath.Join(dir, recordingFile)) {
		recordingPath = filepath.Join(s.StoragePrefix, recordingFile)
	}

	// The live playlist is a rolling window; replay needs the whole
	// stream, so build the VOD manifest from what's on disk.
	if err := writeReplayPlaylist(dir, a.hlsSegmentSeconds(ctx)); err != nil {
		ctx.Logger().Warn("finalize: replay playlist", "id", id, "err", err)
	}

	if _, err := ctx.AppDB().Exec(
		`UPDATE streams
		 SET status = ?, ended_at = ?, error = ?,
		     recording_path = COALESCE(NULLIF(?, ''), recording_path)
		 WHERE id = ? AND project_id = ?`,
		opts.status, nowStamp(), nullStr(opts.errMsg), recordingPath, id, pid); err != nil {
		return nil, err
	}
	a.invalidatePlayback(pid, id)

	s, err = a.dbGet(ctx, pid, id)
	if err != nil || s == nil {
		return nil, err
	}
	a.materializeURLs(ctx, s)

	for _, kind := range opts.preEvents {
		emitStreamEvent(ctx, s, kind, "", nil)
	}
	terminal := EventKindEnded
	if opts.status == "errored" {
		terminal = EventKindErrored
	}
	emitStreamEvent(ctx, s, terminal, opts.errMsg, map[string]any{
		"peak_viewers": s.PeakViewers,
		"recording":    s.RecordingPath != "",
	})
	if recordingPath != "" {
		emitStreamEvent(ctx, s, EventKindRecordingFinalized, "", map[string]any{
			"path": recordingPath,
		})
	}
	return s, nil
}

// writeReplayPlaylist scans dir for seg-NNNNN.ts and writes a complete
// VOD playlist (replay.m3u8) beside them.
//
// The live playlist is bounded to a small rolling window
// (hls_window_segments) so a 2h webinar doesn't end up serving a
// 1800-entry, ~80KB manifest to every viewer every two seconds. That
// window is useless for replay — and ffmpeg's own delete_segments,
// which v0.1 passed but which never fired at hls_list_size 0, would
// have deleted the segments replay needs the moment the window became
// finite. So the runner keeps every segment on disk and we build the
// full manifest here, once, at finalize.
//
// EXTINF durations are the configured segment length rather than the
// exact per-segment duration: the live playlist that carried the real
// numbers has already rolled past most of them, and re-deriving them
// would mean demuxing every segment. With -c copy and keyframe-aligned
// cuts the two differ by well under one segment, which VOD players
// tolerate (seeking interpolates).
//
// Lexical sort == numeric order because the runner writes seg-%05d.ts.
func writeReplayPlaylist(dir string, segSeconds int) error {
	if segSeconds <= 0 {
		segSeconds = 4
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	names := []string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "seg-") || !strings.HasSuffix(n, ".ts") {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", segSeconds)
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, n := range names {
		fmt.Fprintf(&b, "#EXTINF:%d.000,\n%s\n", segSeconds, n)
	}
	b.WriteString("#EXT-X-ENDLIST\n")

	// Write-then-rename so a viewer never sees a half-written manifest.
	tmp := filepath.Join(dir, replayPlaylistFile+".tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, replayPlaylistFile))
}

// replayPlaylistExists reports whether the finalized VOD manifest is
// on disk for this stream.
func replayPlaylistExists(ctx *sdk.AppCtx, storagePrefix string) bool {
	st, err := os.Stat(filepath.Join(streamDataDir(ctx, storagePrefix), replayPlaylistFile))
	return err == nil && !st.IsDir() && st.Size() > 0
}
