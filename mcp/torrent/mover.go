// mover.go — completion-mover.
//
// On every transition into "completed" or "seeding", walk the
// torrent's files in working_dir and upload each one to the storage
// app. Stamp the resulting file_ids on the torrents row. Best-effort
// fire `media.probe_file` for video/audio files. Emit
// `torrent.completed` on the platform bus.
//
// The mover is idempotent: a row with `storage_file_ids_json != '[]'`
// is skipped on subsequent transitions. This is what makes
// resume-on-restart safe — a torrent that completed while we were
// down gets re-detected on the next poll, and the mover walks it
// once.
//
// Memory pressure: storage's files_upload takes bytes_base64 inline.
// For multi-GB files this is bad. v0.1 caps single-file uploads at
// uploadInlineMax; anything larger is logged and skipped (with a
// note in last_error so the panel can show why). v0.2 should switch
// to a chunked / multipart upload tool on storage's side.
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	uploadInlineMax = 256 * 1024 * 1024 // 256 MiB; storage v0.1 inlines bytes
)

// onTransition is wired up in OnMount as the engine's transition
// callback. Runs on the engine's polling goroutine — keep work bounded
// or hand off to a new goroutine. Here we hand off because uploads
// can be slow.
func (a *App) onTransition(infohash, prev, next string, snap TorrentSnapshot) {
	a.ctx.Logger().Info("torrent transition",
		"name", snap.Name, "prev", prev, "next", next, "ih", infohash)

	// Persist state changes to the row so torrent_list / panel
	// reflect them without waiting for the next sync.
	a.persistSnapshot(infohash, snap)

	switch next {
	case "completed", "seeding":
		go a.handleCompletion(infohash, snap)
	case "error":
		a.ctx.Emit("torrent.error", map[string]any{
			"infohash": infohash, "error": snap.LastError, "name": snap.Name,
		})
	}
}

// persistSnapshot writes the current state into the torrents row. We
// don't try to capture every byte tick (the engine has that already);
// we just keep the DB consistent with the in-memory engine state on
// transitions.
func (a *App) persistSnapshot(infohash string, s TorrentSnapshot) {
	_, err := a.ctx.AppDB().Exec(
		`UPDATE torrents
		    SET name = COALESCE(NULLIF(?, ''), name),
		        total_bytes = ?,
		        downloaded_bytes = ?,
		        state = ?,
		        last_error = ?
		  WHERE project_id = ? AND infohash = ?`,
		s.Name, s.Length, s.BytesCompleted, s.State, s.LastError,
		projectScope(), infohash,
	)
	if err != nil {
		a.ctx.Logger().Warn("persist snapshot", "err", err.Error())
	}
}

// handleCompletion is the storage hand-off. Idempotent: if the row
// already has file_ids, we skip and just emit a redundant "completed"
// event so subscribers can rely on at-least-once delivery.
func (a *App) handleCompletion(infohash string, snap TorrentSnapshot) {
	row, err := getTorrentRow(a.ctx.AppDB(), projectScope(), infohash)
	if err != nil {
		a.ctx.Logger().Warn("completion: row lookup", "err", err.Error())
		return
	}
	var existing []int64
	_ = json.Unmarshal([]byte(row.StorageFileIDsJSON), &existing)
	if len(existing) > 0 {
		a.ctx.Emit("torrent.completed", map[string]any{
			"id": row.ID, "infohash": infohash, "name": snap.Name, "file_ids": existing,
		})
		return
	}

	files, err := a.engine.FileSnapshots(infohash)
	if err != nil {
		a.markError(infohash, "completion: "+err.Error())
		return
	}

	target := row.TargetFolder
	if target == "" {
		target = configString(a.ctx, "default_target_folder", "/downloads")
	}
	target = strings.TrimRight(target, "/")
	// Group files under a per-torrent folder when the torrent has
	// more than one file (preserves "Movie.2024.1080p/" structure).
	root := target
	if len(files) > 1 {
		root = target + "/" + sanitiseName(snap.Name)
	}

	uploaded := []int64{}
	uploadErrors := []string{}
	for _, f := range files {
		if f.Priority == "skip" {
			continue
		}
		// Skip files that didn't fully download (selective-skip case).
		if f.BytesCompleted < f.Length {
			continue
		}
		fileID, err := a.uploadOneFile(a.ctx, root, f)
		if err != nil {
			uploadErrors = append(uploadErrors, f.Path+": "+err.Error())
			a.ctx.Logger().Warn("completion upload", "path", f.Path, "err", err.Error())
			continue
		}
		uploaded = append(uploaded, fileID)
		a.maybeProbeMedia(fileID, f.Path)
	}

	// Bail-out path. If any file failed to upload, leave the working
	// copy on disk (the operator may want it), keep the row marked
	// 'error' with the combined reason, and emit torrent.error so
	// subscribers can react. Don't fake a torrent.completed when bytes
	// never made it to storage — earlier versions did exactly that
	// and the panel ended up showing a half-finished torrent as
	// "Fetching metadata".
	if len(uploadErrors) > 0 {
		msg := "upload to storage failed: " + strings.Join(uploadErrors, "; ")
		a.markError(infohash, msg)
		a.ctx.Emit("torrent.error", map[string]any{
			"id": row.ID, "infohash": infohash, "name": snap.Name,
			"error": msg, "phase": "completion-upload",
		})
		return
	}

	idsJSON, _ := json.Marshal(uploaded)
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = a.ctx.AppDB().Exec(
		`UPDATE torrents
		    SET storage_file_ids_json = ?, completed_at = ?, last_error = ''
		  WHERE project_id = ? AND infohash = ?`,
		string(idsJSON), now, projectScope(), infohash,
	)

	if !configFlag(a.ctx, "keep_working_copy", false) {
		a.cleanupWorkingCopy(snap.Name)
	}

	a.ctx.Emit("torrent.completed", map[string]any{
		"id":       row.ID,
		"infohash": infohash,
		"name":     snap.Name,
		"file_ids": uploaded,
	})
}

func (a *App) markError(infohash, msg string) {
	_, _ = a.ctx.AppDB().Exec(
		`UPDATE torrents SET state = 'error', last_error = ?
		  WHERE project_id = ? AND infohash = ?`,
		msg, projectScope(), infohash)
	// Mirror onto the engine's in-memory record so its next snapshot()
	// call returns state="error" too. Without this the engine could
	// re-overwrite our DB state on the next poll (it wins because
	// onTransition runs after our UPDATE).
	if a.engine != nil {
		a.engine.MarkError(infohash, msg)
	}
}

// uploadOneFile reads one local file and pushes it into storage via
// `files_upload`. The relative path inside the torrent is preserved
// under `root` — a torrent like Movie.X/subs/en.srt becomes
// {target}/Movie.X/subs/en.srt.
func (a *App) uploadOneFile(ctx *sdk.AppCtx, root string, f FileSnapshot) (int64, error) {
	if f.Length > uploadInlineMax {
		return 0, fmt.Errorf("file too large for inline upload (%d > %d MiB) — set keep_working_copy=true and upload manually until storage exposes a chunked upload tool",
			f.Length, uploadInlineMax/(1<<20))
	}
	working := configString(a.ctx, "working_dir", "/data/torrents")
	abs := filepath.Join(working, f.Path)
	bytes, err := os.ReadFile(abs)
	if err != nil {
		return 0, err
	}
	contentType := guessContentType(f.Path)
	relDir, name := filepath.Split(f.Path)
	folder := root
	if relDir != "" && relDir != "./" {
		folder = root + "/" + strings.Trim(relDir, "/")
	}

	raw, err := ctx.PlatformAPI().CallApp("storage", "files_upload", map[string]any{
		"filename":     name,
		"folder":       folder,
		"content_type": contentType,
		"bytes_base64": base64.StdEncoding.EncodeToString(bytes),
	})
	if err != nil {
		return 0, err
	}
	var out struct {
		FileID int64 `json:"file_id"`
		ID     int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("storage response: %w", err)
	}
	if out.FileID != 0 {
		return out.FileID, nil
	}
	if out.ID != 0 {
		return out.ID, nil
	}
	return 0, errors.New("storage returned no file id")
}

// maybeProbeMedia is fire-and-forget: if `media` isn't installed or
// errors, the metadata just doesn't appear and we move on. Restricted
// to file extensions where probing is meaningful so we don't bombard
// `media` with image/text files it'll just bounce.
func (a *App) maybeProbeMedia(fileID int64, path string) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".m4v",
		".mp3", ".flac", ".m4a", ".ogg", ".opus", ".wav":
		// proceed
	default:
		return
	}
	go func() {
		_, err := a.ctx.PlatformAPI().CallApp("media", "probe_file",
			map[string]any{"file_id": fileID})
		if err != nil {
			a.ctx.Logger().Info("media.probe skipped", "file_id", fileID, "err", err.Error())
		}
	}()
}

// cleanupWorkingCopy removes the local copy under working_dir. Best
// effort — if removal fails (file in use, permission), we log and
// move on; the next poll will retry on the next state change.
func (a *App) cleanupWorkingCopy(torrentName string) {
	working := configString(a.ctx, "working_dir", "/data/torrents")
	target := filepath.Join(working, torrentName)
	if target == working || target == "/" {
		return // refuse to remove the root
	}
	if err := os.RemoveAll(target); err != nil {
		a.ctx.Logger().Warn("cleanup working copy", "path", target, "err", err.Error())
	}
}

func guessContentType(path string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); t != "" {
		return t
	}
	return "application/octet-stream"
}

// sanitiseName strips path separators and trailing whitespace from a
// torrent name so it's safe to use as a folder segment.
func sanitiseName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	if s == "" {
		s = "untitled"
	}
	return s
}
