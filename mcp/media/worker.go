package main

// Indexer worker. On a tick:
//   1. Fetch the file list from the storage app.
//   2. Filter to media types (audio/video/image).
//   3. For each candidate (no row, or pending/failed, or sha changed),
//      stream bytes to a temp file, run ffprobe, upsert media row,
//      generate thumbnail or waveform, push the derivation back to
//      storage.
//
// Idempotent — re-running is a no-op except where state genuinely
// changed. Errors land on the row as probe_status=failed; the next
// tick retries.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	indexerBatchSize = 25
)

// runIndexer is the worker body. The framework calls it on the
// schedule declared in apteva.yaml.
// indexerConfig captures the per-tick configuration the indexer +
// event-driven indexOneFile both consume. Read once via readIndexerConfig
// rather than parsing the same env on every call.
type indexerConfig struct {
	maxSizeMB   any
	thumbSeek   any
	thumbWidth  any
	waveW, waveH any
	ffmpegPath  string
	ffprobePath string
}

func readIndexerConfig(app *sdk.AppCtx) indexerConfig {
	cfg := app.Config()
	c := indexerConfig{
		maxSizeMB:   parseConfigInt(cfg.Get("max_probe_size_mb"), 2048),
		thumbSeek:   parseConfigFloat(cfg.Get("thumbnail_seek_seconds"), 1.0),
		thumbWidth:  parseConfigInt(cfg.Get("thumbnail_width"), 320),
		waveW:       parseConfigInt(cfg.Get("waveform_width"), 800),
		waveH:       parseConfigInt(cfg.Get("waveform_height"), 100),
		ffmpegPath:  strings.TrimSpace(cfg.Get("ffmpeg_path")),
		ffprobePath: strings.TrimSpace(cfg.Get("ffprobe_path")),
	}
	if c.ffmpegPath == "" {
		c.ffmpegPath = "ffmpeg"
	}
	if c.ffprobePath == "" {
		c.ffprobePath = "ffprobe"
	}
	return c
}

func runIndexer(ctx context.Context, app *sdk.AppCtx) error {
	c := readIndexerConfig(app)
	maxSizeMB := c.maxSizeMB
	thumbSeek := c.thumbSeek
	thumbWidth := c.thumbWidth
	waveW := c.waveW
	waveH := c.waveH
	ffmpegPath := c.ffmpegPath
	ffprobePath := c.ffprobePath

	// SDK dispatches this worker once per project per tick. For a
	// project-scoped install ctx.CurrentProject() returns the
	// install's pinned project; for a global install it returns the
	// project the SDK is currently fanning out to. Treating "" as
	// "skip this tick" makes the worker safe when the SDK hasn't
	// resolved a project yet (e.g. fresh boot before ListProjects
	// returned).
	projectID := strings.TrimSpace(app.CurrentProject())
	if projectID == "" {
		app.Logger().Info("indexer: no project context for this dispatch; skipping")
		return nil
	}
	sc := newStorageClient()

	// Pull the file inventory once per tick. Storage's /files paginates;
	// we ask for a generous slab on the assumption most projects sit
	// well under a few thousand. The exact limit also gates the orphan
	// cleanup below — only safe when we know we got a complete view.
	const storageListLimit = 5000
	files, err := sc.SearchFiles(ctx, projectID, storageListLimit)
	if err != nil {
		app.Logger().Warn("storage search failed", "err", err)
		return nil
	}

	// Cascade-cleanup: any media row whose source file is no longer
	// in storage (soft-deleted, re-uploaded under a new id, etc.)
	// gets dropped along with its derivations + transcripts. Skipped
	// when the storage listing might be incomplete — the safety guard
	// is "did we hit the page limit". Storage soft-deletes by default
	// so re-creation later just re-indexes the file fresh.
	if len(files) < storageListLimit {
		// Only files we'd legitimately catalog count as "still here".
		// Excluded-folder files (renders, .screenshots, .media)
		// SHOULDN'T have a media row to begin with; if one snuck in
		// before isExcludedFromCatalog covered that folder, treat it
		// as orphan now so purgeOrphans cascades it out. Without this
		// filter, render outputs (storage genuinely keeps them) would
		// continue to drive catalog rows that confuse the panel.
		fileIDs := make([]string, 0, len(files))
		for _, f := range files {
			if isExcludedFromCatalog(f.Folder) {
				continue
			}
			fileIDs = append(fileIDs, strconv.FormatInt(f.ID, 10))
		}
		if n, err := purgeOrphans(app, sc, app.AppDB(), projectID, fileIDs); err != nil {
			app.Logger().Warn("purge orphan media failed", "err", err)
		} else if n > 0 {
			app.Logger().Info("purged orphan media", "count", n)
		}
	} else {
		app.Logger().Warn("storage listing hit page limit; orphan cleanup skipped this tick",
			"limit", storageListLimit)
	}

	media := filterMediaFiles(files)
	candidates := indexerCandidates(app.AppDB(), projectID, media, indexerBatchSize)
	if len(candidates) == 0 {
		return nil
	}
	app.Logger().Info("indexer tick",
		"total_in_storage", len(files),
		"media_in_storage", len(media),
		"candidates", len(candidates),
	)

	for _, f := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		processOne(ctx, app, sc, projectID, f, ffmpegPath, ffprobePath,
			maxSizeMB, thumbSeek, thumbWidth, waveW, waveH)
	}
	return nil
}

// indexOneFile runs the indexer pipeline for a single file —
// triggered by the storage.file.added event handler so newly-uploaded
// media gets probed + thumbnailed within a fraction of a second
// instead of waiting up to 30s for the indexer's tick.
//
// Skips with no error when:
//   - the file lives in an excluded folder (see isExcludedFromCatalog
//     — covers /.<hidden>/ and /renders/)
//   - the file isn't a media type (text, archives, …)
//   - the media row already exists with a matching sha (idempotent
//     re-event from a flapping connection / event replay)
//
// Wraps processOne with the same config setup runIndexer uses so
// the two entry points produce identical results.
func indexOneFile(ctx context.Context, app *sdk.AppCtx, projectID string, f StorageFile) {
	if isExcludedFromCatalog(f.Folder) {
		return
	}
	if !isMediaContentType(f.ContentType) && !isMediaByExt(f.Name) {
		return
	}
	// Skip if already indexed at this sha — events can replay from
	// the platform's ring buffer, and the sweep may have raced us.
	fid := strconv.FormatInt(f.ID, 10)
	if existing, err := getMedia(app.AppDB(), projectID, fid); err == nil &&
		existing.SourceSHA256 == f.SHA256 && existing.ProbeStatus == "ok" {
		return
	}

	c := readIndexerConfig(app)
	sc := newStorageClient()
	processOne(ctx, app, sc, projectID, f, c.ffmpegPath, c.ffprobePath,
		c.maxSizeMB, c.thumbSeek, c.thumbWidth, c.waveW, c.waveH)
}

// inFlightIndex is a process-wide set of (projectID, fileID) keys
// currently being indexed. The DB-row dedupe check inside processOne
// only catches the race when the first run has already written
// probe_status='ok'; between "first run started" and "first run wrote
// ok" the second run still flies past dedupe and ends up colliding
// on the workdir (the first's EXIT-trap rm -rf "$WORK" deletes
// probe.json out from under the second mid-execution).
//
// In-memory mutex closes that window: any second attempt for the
// same (project, file) skips immediately. Process-local is enough
// because the indexer is a singleton per sidecar; if multi-process
// indexing ever becomes a thing we'd need a SELECT FOR UPDATE on a
// dedicated locks table.
var inFlightIndex sync.Map // key: "projectID|fileID" → struct{}

// claimInFlight tries to register (projectID, fileID) as in-flight.
// Returns false when another goroutine is already processing — the
// caller skips silently. The releaseInFlight call MUST happen in a
// defer on success.
func claimInFlight(projectID, fileID string) bool {
	key := projectID + "|" + fileID
	_, loaded := inFlightIndex.LoadOrStore(key, struct{}{})
	return !loaded
}

func releaseInFlight(projectID, fileID string) {
	inFlightIndex.Delete(projectID + "|" + fileID)
}

func processOne(
	ctx context.Context, app *sdk.AppCtx, sc *storageClient, projectID string,
	f StorageFile, ffmpegPath, ffprobePath string,
	maxSizeMB, thumbSeek, thumbWidth, waveW, waveH any,
) {
	fid := strconv.FormatInt(f.ID, 10)
	// Race guard. Concurrent attempts for the same file (periodic
	// indexer tick + storage `file.added` event replay both firing,
	// or two ticks during a slow remote indexing run) would otherwise
	// step on each other's scratch workdirs + duplicate-upload to
	// storage. The DB-row dedupe further down only catches the case
	// after the first run has written probe_status='ok'; this
	// in-memory claim covers the gap.
	if !claimInFlight(projectID, fid) {
		// Concurrent attempt for the same file — skip silently.
		// Single Info log so operators can see the dedupe firing
		// without spamming Warn for normal-shape races.
		app.Logger().Info("indexer: file already in flight; skipping concurrent attempt",
			"file_id", fid, "name", f.Name)
		return
	}
	defer releaseInFlight(projectID, fid)

	maxBytes := int64(toInt(maxSizeMB)) * 1024 * 1024
	logger := app.Logger()
	logCtx := []any{"file_id", fid, "name", f.Name, "content_type", f.ContentType, "size", f.SizeBytes}

	// force_probe (set by media_reindex(force=true)) bypasses both
	// the dedupe-check below AND the size cap further down. The flag
	// is cleared at every terminal outcome below — success via
	// probe_status='ok', failure via markFailed — so a second cycle
	// re-applies both gates.
	var forceProbe int
	_ = app.AppDB().QueryRow(
		`SELECT force_probe FROM media WHERE project_id=? AND file_id=?`,
		projectID, fid,
	).Scan(&forceProbe)

	// Dedupe at the entry point. The same file can land here twice
	// in quick succession when the storage `file.added` SSE event
	// races the 30s periodic indexer tick — both grab the row as a
	// candidate, both spawn the (potentially remote) pipeline, the
	// second one collides with the first's still-cleaning workdir
	// or storage rejects the duplicate derivation upload.
	// indexOneFile already has this check; the periodic path didn't
	// (it relied on indexerCandidates filtering, which has a tiny
	// race window). Belt + suspenders here: if the row already
	// exists at this exact sha with probe_status=ok and the operator
	// isn't forcing a reprobe, the work's already been done — return
	// without touching the remote or local pipeline.
	if forceProbe == 0 {
		if existing, err := getMedia(app.AppDB(), projectID, fid); err == nil &&
			existing.SourceSHA256 == f.SHA256 && existing.ProbeStatus == "ok" {
			return
		}
	}

	// Remote indexing — when render_host_id is set, run probe +
	// derivations on the Hetzner host via HTTP-range-seek against a
	// signed source URL. Saves the giant local download for large
	// videos: the Apteva machine never touches the source bytes.
	// Auto-falls-back to local on any error so a flaky Hetzner
	// network degrades to "slow but works" instead of "stuck".
	//
	// The size cap is BYPASSED in this branch because the whole
	// point is that big files no longer require local disk space —
	// they stay on storage and only the ffmpeg/ffprobe-needed bytes
	// stream to the remote.
	if hostID := remoteIndexerHostID(app); hostID > 0 {
		ok := tryRemoteIndex(ctx, app, sc, projectID, fid, f, hostID,
			thumbSeek, thumbWidth, waveW, waveH)
		if ok {
			return
		}
		logger.Warn("remote indexing failed, falling back to local", "file_id", fid)
	}

	if forceProbe == 0 && maxBytes > 0 && f.SizeBytes > maxBytes {
		_ = markFailed(app.AppDB(), projectID, fid, f.SHA256, "skipped_size",
			fmt.Sprintf("file size %d > max_probe_size_mb (%d MB)", f.SizeBytes, toInt(maxSizeMB)))
		logger.Info("skipped — over size cap", logCtx...)
		return
	}

	// Stream bytes to a temp file. ffprobe + ffmpeg need to seek for
	// most formats — piping stdin is fragile.
	tmpDir, err := os.MkdirTemp("", "media-probe-")
	if err != nil {
		_ = markFailed(app.AppDB(), projectID, fid, f.SHA256, "failed", "mktemp: "+err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)
	srcPath := filepath.Join(tmpDir, sanitizeName(f.Name))
	srcFile, err := os.Create(srcPath)
	if err != nil {
		_ = markFailed(app.AppDB(), projectID, fid, f.SHA256, "failed", "tempfile: "+err.Error())
		return
	}
	if err := sc.DownloadContent(ctx, projectID, f.ID, srcFile); err != nil {
		srcFile.Close()
		_ = markFailed(app.AppDB(), projectID, fid, f.SHA256, "failed", "download: "+err.Error())
		logger.Warn("download failed", "err", err)
		return
	}
	srcFile.Close()

	probe, err := runProbe(ctx, ffprobePath, srcPath)
	if err != nil {
		_ = markFailed(app.AppDB(), projectID, fid, f.SHA256, "failed", err.Error())
		logger.Warn("probe failed", "err", err)
		return
	}
	if !probe.HasVideo && !probe.HasAudio && !probe.IsImage {
		_ = markFailed(app.AppDB(), projectID, fid, f.SHA256, "unsupported", "no audio, video, or image stream")
		return
	}
	if err := upsertMedia(app.AppDB(), projectID, fid, probe, f.SHA256, f.Folder, f.Name); err != nil {
		logger.Warn("upsert failed", "err", err)
		return
	}
	logger.Info("indexed",
		"duration_ms", probe.DurationMs,
		"video", probe.HasVideo, "audio", probe.HasAudio, "image", probe.IsImage,
	)
	app.Emit("media.indexed", map[string]any{
		"file_id":     fid,
		"name":        f.Name,
		"has_video":   probe.HasVideo,
		"has_audio":   probe.HasAudio,
		"is_image":    probe.IsImage,
		"duration_ms": probe.DurationMs,
	})

	// Derivations: thumbnail for video/image, waveform for audio.
	// Track success per derivation so the media.derived event below
	// tells subscribers exactly what's available — partial failures
	// (e.g. waveform produced but thumbnail upload 5xx'd) still emit
	// the event with the truthful subset.
	var hasThumb, hasWave bool
	var keyframeCount int
	if probe.HasVideo || probe.IsImage {
		thumbPath := filepath.Join(tmpDir, "thumb.jpg")
		if err := makeThumbnail(ctx, ffmpegPath, srcPath, thumbPath, toFloat(thumbSeek), toInt(thumbWidth), probe.IsImage, probe.DurationMs); err != nil {
			logger.Warn("thumbnail failed", "err", err)
		} else if err := uploadAndRecord(ctx, app, sc, projectID, fid, "thumbnail", thumbPath, "image/jpeg", toInt(thumbWidth), 0, 0); err != nil {
			logger.Warn("thumbnail upload failed", "err", err)
		} else {
			hasThumb = true
		}
	}
	if probe.HasAudio && !probe.HasVideo {
		wavePath := filepath.Join(tmpDir, "waveform.png")
		if err := makeWaveform(ctx, ffmpegPath, srcPath, wavePath, toInt(waveW), toInt(waveH)); err != nil {
			logger.Warn("waveform failed", "err", err)
		} else if err := uploadAndRecord(ctx, app, sc, projectID, fid, "waveform", wavePath, "image/png", toInt(waveW), toInt(waveH), 0); err != nil {
			logger.Warn("waveform upload failed", "err", err)
		} else {
			hasWave = true
		}
	}
	// Keyframes for video sources only (images are single-frame; audio-
	// only has no frames). Soft-fail per keyframe — a single extract or
	// upload error doesn't abort the rest. Counts what actually
	// persisted so media.derived's has_keyframes is truthful.
	if probe.HasVideo && !probe.IsImage && keyframesEnabled(app) {
		positions := keyframePositions(probe.DurationMs, app)
		for _, posMs := range positions {
			framePath := filepath.Join(tmpDir, fmt.Sprintf("kf-%d.jpg", posMs))
			if err := extractKeyframe(ctx, ffmpegPath, srcPath, framePath, float64(posMs)/1000.0, toInt(thumbWidth)); err != nil {
				logger.Warn("keyframe failed", "position_ms", posMs, "err", err)
				continue
			}
			if err := uploadAndRecord(ctx, app, sc, projectID, fid, "keyframe", framePath, "image/jpeg", toInt(thumbWidth), 0, posMs); err != nil {
				logger.Warn("keyframe upload failed", "position_ms", posMs, "err", err)
				continue
			}
			keyframeCount++
		}
	}

	// media.derived — the canonical "indexer is done with this file"
	// event. Fires REGARDLESS of whether Deepgram/LLM integrations are
	// bound, so subscribers that want "file is ready for consumption"
	// don't get silently starved on installs that only do indexing.
	// Distinguishable from media.indexed (which on the local path fires
	// BEFORE derivations) by the has_thumbnail / has_waveform fields.
	app.Emit("media.derived", map[string]any{
		"file_id":         fid,
		"name":            f.Name,
		"has_video":       probe.HasVideo,
		"has_audio":       probe.HasAudio,
		"is_image":        probe.IsImage,
		"duration_ms":     probe.DurationMs,
		"has_thumbnail":   hasThumb,
		"has_waveform":    hasWave,
		"keyframe_count":  keyframeCount,
	})

	// Wake the right downstream worker the moment probe finishes:
	//
	//   has audio  → notifyTranscriber. The transcriber claims a
	//                pending row, calls Deepgram, then itself
	//                notifies the describer once the transcript
	//                lands so the LLM gets {thumbnail + transcript}.
	//   no audio   → notifyDescriber directly. Image / silent video
	//                only ever needs a vision-only describe.
	//
	// Without this, both workers waited up to 60s for their next
	// periodic tick to notice the new row. Periodic sweep stays as
	// the safety net for both paths.
	if probe.HasAudio {
		notifyTranscriber(projectID, fid)
	} else {
		notifyDescriber(projectID, fid)
	}
	// media.completed coordinator. Runs after every stage; whichever
	// is "last applicable" given the install config wins the
	// idempotent UPDATE inside the helper and emits the event. For
	// installs with no transcripts/descriptions integrations bound,
	// this tail-call is the one that emits.
	maybeEmitMediaCompleted(app, projectID, fid)
}

// remoteIndexerHostID returns the host_id to dispatch indexing to,
// or 0 to keep everything local. Reads the same render_host_id
// config the render pool uses — operators don't need to configure
// indexing separately, they just say "use this host for ffmpeg work"
// and both renders and derivations honour it.
func remoteIndexerHostID(app *sdk.AppCtx) int64 {
	cfg := app.Config()
	return int64(parseConfigIntFallback(cfg.Get("render_host_id"), 0))
}

// tryRemoteIndex runs the whole indexer pipeline on the remote host
// and writes the resulting media + derivation rows. Returns false on
// any error so the caller can fall back to the local path. We don't
// touch the DB on failure — letting the local path do a clean
// attempt with its own error reporting.
func tryRemoteIndex(
	ctx context.Context, app *sdk.AppCtx, sc *storageClient,
	projectID, fid string, f StorageFile, hostID int64,
	thumbSeek, thumbWidth, waveW, waveH any,
) bool {
	logger := app.Logger()
	// Time-limited signed URL the remote uses for both probe + derivations.
	// Generous TTL covers slow installs of ffmpeg on first remote use.
	signedURL, err := sc.GetSignedURL(ctx, projectID, f.ID, 3600)
	if err != nil {
		logger.Warn("remote index: sign source url", "file_id", fid, "err", err)
		return false
	}
	probe, thumbID, waveID, keyframes, err := runRemoteIndexing(ctx, app, projectID, remoteIndexParams{
		HostID:               hostID,
		SignedURL:            signedURL,
		ThumbSeek:            toFloat(thumbSeek),
		ThumbWidth:           toInt(thumbWidth),
		WaveW:                toInt(waveW),
		WaveH:                toInt(waveH),
		FileID:               fid,
		KeyframeIntervalSecs: parseConfigIntFallback(app.Config().Get("keyframe_interval_seconds"), defaultKeyframeIntervalSeconds),
		KeyframeMaxCount:     parseConfigIntFallback(app.Config().Get("keyframe_max_count"), defaultKeyframeMaxCount),
		KeyframesEnabled:     keyframesEnabled(app),
	})
	if err != nil {
		logger.Warn("remote index: pipeline", "file_id", fid, "err", err)
		return false
	}
	if !probe.HasVideo && !probe.HasAudio && !probe.IsImage {
		_ = markFailed(app.AppDB(), projectID, fid, f.SHA256, "unsupported",
			"no audio, video, or image stream")
		return true
	}
	if err := upsertMedia(app.AppDB(), projectID, fid, probe, f.SHA256, f.Folder, f.Name); err != nil {
		logger.Warn("remote index: upsert media", "file_id", fid, "err", err)
		return false
	}
	logger.Info("indexed (remote)",
		"file_id", fid, "host_id", hostID,
		"duration_ms", probe.DurationMs,
		"video", probe.HasVideo, "audio", probe.HasAudio, "image", probe.IsImage,
		"thumb_id", thumbID, "wave_id", waveID,
	)
	app.Emit("media.indexed", map[string]any{
		"file_id":     fid,
		"name":        f.Name,
		"has_video":   probe.HasVideo,
		"has_audio":   probe.HasAudio,
		"is_image":    probe.IsImage,
		"duration_ms": probe.DurationMs,
		"executor":    "remote-instance",
	})
	if thumbID > 0 {
		if err := upsertDerivation(app.AppDB(), projectID, fid, "thumbnail", thumbID, toInt(thumbWidth), 0, 0); err != nil {
			logger.Warn("remote index: upsert thumbnail derivation", "err", err)
		}
	}
	if waveID > 0 {
		if err := upsertDerivation(app.AppDB(), projectID, fid, "waveform", waveID, toInt(waveW), toInt(waveH), 0); err != nil {
			logger.Warn("remote index: upsert waveform derivation", "err", err)
		}
	}
	// Keyframes (remote): upsert any keyframe rows the remote shell
	// produced. The shell returns them as a JSON array
	// [{position_ms, storage_file_id}, …] in the APTEVA_INDEX marker.
	for _, k := range keyframes {
		if err := upsertDerivation(app.AppDB(), projectID, fid, "keyframe", k.StorageFileID, toInt(thumbWidth), 0, k.PositionMs); err != nil {
			logger.Warn("remote index: upsert keyframe derivation", "err", err, "position_ms", k.PositionMs)
		}
	}

	// media.derived — same contract as the local path: fires after
	// the indexer has finished derive work for this file, regardless
	// of whether the transcriber/describer integrations are bound.
	// The executor field distinguishes this from the local emit; the
	// has_thumbnail / has_waveform truthfully reflect what the remote
	// shell uploaded back (thumbID > 0 means the upload completed
	// successfully).
	app.Emit("media.derived", map[string]any{
		"file_id":       fid,
		"name":          f.Name,
		"has_video":     probe.HasVideo,
		"has_audio":     probe.HasAudio,
		"is_image":      probe.IsImage,
		"duration_ms":   probe.DurationMs,
		"has_thumbnail": thumbID > 0,
		"has_waveform":  waveID > 0,
		"executor":      "remote-instance",
	})

	if probe.HasAudio {
		notifyTranscriber(projectID, fid)
	} else {
		notifyDescriber(projectID, fid)
	}
	// Same tail-call as the local path — see comment there.
	maybeEmitMediaCompleted(app, projectID, fid)
	return true
}

// uploadAndRecord pushes the derivation file to storage and records
// the resulting storage_file_id in the derivations table. The file
// lands under /.media/<kind>/<src_id>.<ext> so the dashboard's Files
// panel can hide it under a single hidden folder.
//
// positionMs is 0 for thumbnail/waveform; the source timestamp for
// keyframes (so multiple keyframe rows per file coexist via the
// (file, kind, position_ms) UNIQUE constraint).
func uploadAndRecord(
	ctx context.Context, app *sdk.AppCtx, sc *storageClient,
	projectID, fileID, kind, path, contentType string, w, h int, positionMs int64,
) error {
	bytesData, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	folder := "/.media/" + kind + "/"
	ext := filepath.Ext(path)
	name := fileID + ext
	// Keyframes need a unique filename per position; without this the
	// folder would have N files all named "<src>.jpg" with the last
	// upload winning and the prior keyframes orphaning their
	// derivation rows. Position-tag the filename so each keyframe is
	// its own storage row.
	if kind == "keyframe" && positionMs > 0 {
		name = fmt.Sprintf("%s-%d%s", fileID, positionMs, ext)
	}
	storageID, err := sc.UploadDerivation(ctx, projectID, name, folder, contentType, bytesData)
	if err != nil {
		return err
	}
	return upsertDerivation(app.AppDB(), projectID, fileID, kind, storageID, w, h, positionMs)
}

// filterMediaFiles keeps the subset whose content_type starts with a
// media prefix. Storage may not always have a content_type set — for
// those, fall back to the file extension.
//
// Hard skip: anything under a folder isExcludedFromCatalog returns
// true for — see that helper for the rules.
func filterMediaFiles(files []StorageFile) []StorageFile {
	out := make([]StorageFile, 0, len(files))
	for _, f := range files {
		if isExcludedFromCatalog(f.Folder) {
			continue
		}
		if isMediaContentType(f.ContentType) || isMediaByExt(f.Name) {
			out = append(out, f)
		}
	}
	return out
}

// isExcludedFromCatalog reports whether a storage folder should NEVER
// produce media catalog rows. Three categories:
//
//   1. Hidden folders (anything starting with "/.") — storage's
//      convention for "internal app bookkeeping, don't show users".
//      Covers /.media/ (this app's own derivations — would loop on
//      itself), /.screenshots/ (the screenshots app's captures), and
//      any future hidden-folder convention an app adopts.
//
//   2. Media's own render output folder (/renders/ by default).
//      Renders are OUTPUTS of media's pipeline, not new source
//      uploads. Indexing them would cluter the catalog with frames
//      that are already addressable via the renders table.
//
// Older versions only skipped /.media/. That let render outputs +
// screenshots + every other app's hidden folder pollute the catalog
// — operators reported "I see files I think I deleted" because the
// rows for /renders/frame-Nms.png stuck around (those storage files
// genuinely exist, just shouldn't be cataloged).
func isExcludedFromCatalog(folder string) bool {
	if strings.HasPrefix(folder, "/.") {
		return true
	}
	if folder == "/renders/" || strings.HasPrefix(folder, "/renders/") {
		return true
	}
	return false
}

// isOwnDerivation is kept as a backward-compat alias for callers
// that specifically want "is this MY derivation". Use
// isExcludedFromCatalog for the catalog-skip decision.
func isOwnDerivation(folder string) bool {
	return strings.HasPrefix(folder, "/.media/")
}

func isMediaContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "image/")
}

func isMediaByExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3", ".wav", ".flac", ".ogg", ".m4a", ".aac", ".opus":
		return true
	case ".mp4", ".mov", ".webm", ".mkv", ".avi", ".m4v":
		return true
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tiff", ".heic", ".heif":
		return true
	}
	return false
}

// sanitizeName strips path separators so a maliciously-named file
// can't escape our tempdir.
func sanitizeName(s string) string {
	if s == "" {
		return "file.bin"
	}
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	if s == "." || s == ".." {
		s = "file.bin"
	}
	return s
}

// timeout wraps a slow operation with a deadline so the worker tick
// can't hang forever on one file.
func timeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

// readAllCapped slurps r up to limit bytes; returns ErrUnexpectedEOF
// if the body exceeds. Currently unused — kept for v0.2 streaming.
func readAllCapped(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, io.ErrUnexpectedEOF
	}
	return buf.Bytes(), nil
}

// --- config helpers ----------------------------------------------------------

func parseConfigInt(s string, fallback int) any {
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}
func parseConfigFloat(s string, fallback float64) any {
	if s == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fallback
	}
	return v
}
func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}
func toFloat(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case float64:
		return x
	}
	return 0
}
