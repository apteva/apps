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
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	indexerBatchSize = 25
)

// runIndexer is the worker body. The framework calls it on the
// schedule declared in apteva.yaml.
func runIndexer(ctx context.Context, app *sdk.AppCtx) error {
	cfg := app.Config()
	maxSizeMB := parseConfigInt(cfg.Get("max_probe_size_mb"), 2048)
	thumbSeek := parseConfigFloat(cfg.Get("thumbnail_seek_seconds"), 1.0)
	thumbWidth := parseConfigInt(cfg.Get("thumbnail_width"), 320)
	waveW := parseConfigInt(cfg.Get("waveform_width"), 800)
	waveH := parseConfigInt(cfg.Get("waveform_height"), 100)
	ffmpegPath := strings.TrimSpace(cfg.Get("ffmpeg_path"))
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	ffprobePath := strings.TrimSpace(cfg.Get("ffprobe_path"))
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}

	projectID := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
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
		fileIDs := make([]string, 0, len(files))
		for _, f := range files {
			fileIDs = append(fileIDs, strconv.FormatInt(f.ID, 10))
		}
		if n, err := purgeOrphans(app.AppDB(), projectID, fileIDs); err != nil {
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

func processOne(
	ctx context.Context, app *sdk.AppCtx, sc *storageClient, projectID string,
	f StorageFile, ffmpegPath, ffprobePath string,
	maxSizeMB, thumbSeek, thumbWidth, waveW, waveH any,
) {
	fid := strconv.FormatInt(f.ID, 10)
	maxBytes := int64(toInt(maxSizeMB)) * 1024 * 1024
	logger := app.Logger()
	logCtx := []any{"file_id", fid, "name", f.Name, "content_type", f.ContentType, "size", f.SizeBytes}

	if maxBytes > 0 && f.SizeBytes > maxBytes {
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
	if err := upsertMedia(app.AppDB(), projectID, fid, probe, f.SHA256); err != nil {
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
	if probe.HasVideo || probe.IsImage {
		thumbPath := filepath.Join(tmpDir, "thumb.jpg")
		if err := makeThumbnail(ctx, ffmpegPath, srcPath, thumbPath, toFloat(thumbSeek), toInt(thumbWidth), probe.IsImage, probe.DurationMs); err != nil {
			logger.Warn("thumbnail failed", "err", err)
		} else if err := uploadAndRecord(ctx, app, sc, projectID, fid, "thumbnail", thumbPath, "image/jpeg", toInt(thumbWidth), 0); err != nil {
			logger.Warn("thumbnail upload failed", "err", err)
		}
	}
	if probe.HasAudio && !probe.HasVideo {
		wavePath := filepath.Join(tmpDir, "waveform.png")
		if err := makeWaveform(ctx, ffmpegPath, srcPath, wavePath, toInt(waveW), toInt(waveH)); err != nil {
			logger.Warn("waveform failed", "err", err)
		} else if err := uploadAndRecord(ctx, app, sc, projectID, fid, "waveform", wavePath, "image/png", toInt(waveW), toInt(waveH)); err != nil {
			logger.Warn("waveform upload failed", "err", err)
		}
	}
}

// uploadAndRecord pushes the derivation file to storage and records
// the resulting storage_file_id in the derivations table. The file
// lands under /.media/<kind>/<src_id>.<ext> so the dashboard's Files
// panel can hide it under a single hidden folder.
func uploadAndRecord(
	ctx context.Context, app *sdk.AppCtx, sc *storageClient,
	projectID, fileID, kind, path, contentType string, w, h int,
) error {
	bytesData, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	folder := "/.media/" + kind + "/"
	ext := filepath.Ext(path)
	name := fileID + ext
	storageID, err := sc.UploadDerivation(ctx, projectID, name, folder, contentType, bytesData)
	if err != nil {
		return err
	}
	return upsertDerivation(app.AppDB(), projectID, fileID, kind, storageID, w, h)
}

// filterMediaFiles keeps the subset whose content_type starts with a
// media prefix. Storage may not always have a content_type set — for
// those, fall back to the file extension.
//
// Hard skip: anything under /.media/ — that's where this app writes
// its own thumbnails + waveforms. Indexing them would create a tight
// loop (probe a thumbnail, generate a thumbnail of the thumbnail,
// repeat every sweep) and pollute both the catalog and storage.
func filterMediaFiles(files []StorageFile) []StorageFile {
	out := make([]StorageFile, 0, len(files))
	for _, f := range files {
		if isOwnDerivation(f.Folder) {
			continue
		}
		if isMediaContentType(f.ContentType) || isMediaByExt(f.Name) {
			out = append(out, f)
		}
	}
	return out
}

// isOwnDerivation reports whether a storage folder path holds files
// the media indexer wrote (thumbnails, waveforms). They live under
// /.media/<kind>/ and must never be re-indexed. The leading dot
// matches the storage app's "hidden folder" convention.
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
