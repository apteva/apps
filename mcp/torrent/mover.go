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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	// chunkSize matches storage's defaultPartSize. Reads + PUTs one
	// chunk at a time so memory stays flat regardless of file size —
	// a 5 MB buffer is enough headroom for the storage HTTP client.
	chunkSize = 5 * 1024 * 1024
)

// completionInFlight dedupes overlapping handleCompletion runs for
// the same infohash. anacrolix's engine flips rapidly between
// `completed` and `seeding` while peers come and go (we saw
// completed→seeding→completed→seeding inside the same second on
// the C210/cffaba02 cycle in v0.1.13), and onTransition spawns a
// goroutine for each one. Without this guard, two goroutines both
// see `storage_file_ids_json='[]'`, both run the upload loop, and
// both write a final row — leaving an orphan file copy in storage.
//
// LoadOrStore returns loaded=true when the key already exists, so
// the second goroutine bails immediately. The defer Delete clears
// the entry on exit (success or any error path) so a future
// transition (e.g. after a long idle period) is free to re-enter.
var completionInFlight sync.Map // infohash → struct{}{}

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

// handleCompletion is the storage hand-off. Idempotent at two
// layers:
//
//  1. completionInFlight gates concurrent goroutines for the same
//     infohash — only one runs at a time; subsequent ones bail
//     immediately rather than queue. Avoids the v0.1.13 race that
//     produced orphaned uploads when the engine bounced between
//     completed/seeding faster than one upload could finish.
//
//  2. Even after acquiring that gate, we re-check
//     storage_file_ids_json: if a previous run already populated it,
//     just emit the redundant "completed" event so subscribers can
//     rely on at-least-once delivery.
func (a *App) handleCompletion(infohash string, snap TorrentSnapshot) {
	if _, loaded := completionInFlight.LoadOrStore(infohash, struct{}{}); loaded {
		a.ctx.Logger().Info("completion already in flight, skipping",
			"name", snap.Name, "ih", infohash)
		return
	}
	defer completionInFlight.Delete(infohash)

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

// uploadOneFile streams one local file into storage via the chunked
// /uploads protocol. Bypasses the base64-inline files_upload tool
// which capped at storage's max_upload_size_mb (default 100 MB) and
// pulled the whole file into RAM before write.
//
// The relative path inside the torrent is preserved under `root` —
// a torrent like Movie.X/subs/en.srt becomes {target}/Movie.X/subs/en.srt.
//
// Wire (cross-app HTTP, sidecar→sidecar via the platform gateway):
//
//   POST   {gateway}/api/apps/storage/uploads
//   PUT    {gateway}/api/apps/storage/uploads/{id}/parts/{N}     (×many)
//   POST   {gateway}/api/apps/storage/uploads/{id}/complete
//
// Auth: APTEVA_APP_TOKEN. Project: APTEVA_PROJECT_ID query param so
// storage's resolveProjectFromRequest is unambiguous even on
// global-scoped storage installs.
func (a *App) uploadOneFile(ctx *sdk.AppCtx, root string, f FileSnapshot) (int64, error) {
	working := resolveWorkingDir(a.ctx)
	abs := filepath.Join(working, f.Path)

	file, err := os.Open(abs)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}

	relDir, name := filepath.Split(f.Path)
	folder := root
	if relDir != "" && relDir != "./" {
		folder = root + "/" + strings.Trim(relDir, "/")
	}
	contentType := guessContentType(f.Path)

	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := os.Getenv("APTEVA_APP_TOKEN")
	if gateway == "" || token == "" {
		return 0, errors.New("APTEVA_GATEWAY_URL / APTEVA_APP_TOKEN not set in env")
	}
	pid := projectScope()
	base := gateway + "/api/apps/storage"
	q := "?project_id=" + url.QueryEscape(pid)

	// No per-request timeout — multi-GB uploads take minutes. The
	// engine's polling loop will keep tickling the row's progress in
	// the meantime, and onTransition runs in its own goroutine so the
	// poll loop doesn't block here.
	httpc := &http.Client{}

	// 1. init session.
	initBody, _ := json.Marshal(map[string]any{
		"filename":     name,
		"size":         stat.Size(),
		"content_type": contentType,
		"folder":       folder,
	})
	req, _ := http.NewRequest("POST", base+"/uploads"+q, bytes.NewReader(initBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("upload init: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return 0, fmt.Errorf("upload init: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var initOut struct {
		UploadID string `json:"upload_id"`
		PartSize int64  `json:"part_size"`
		File     *struct {
			ID int64 `json:"id"`
		} `json:"file"` // populated when init short-circuits on sha256 dedup
	}
	if err := json.NewDecoder(resp.Body).Decode(&initOut); err != nil {
		resp.Body.Close()
		return 0, fmt.Errorf("upload init decode: %w", err)
	}
	resp.Body.Close()
	if initOut.File != nil && initOut.File.ID != 0 {
		return initOut.File.ID, nil
	}

	partSize := initOut.PartSize
	if partSize <= 0 {
		partSize = chunkSize
	}

	// 2. parts — stream one chunk at a time, hash incrementally so
	// the complete call has the sha256 storage will verify against.
	hasher := sha256.New()
	buf := make([]byte, partSize)
	partNum := 1
	for {
		n, rerr := io.ReadFull(file, buf)
		if n > 0 {
			chunk := buf[:n]
			hasher.Write(chunk)
			partURL := fmt.Sprintf("%s/uploads/%s/parts/%d%s", base, initOut.UploadID, partNum, q)
			preq, _ := http.NewRequest("PUT", partURL, bytes.NewReader(chunk))
			preq.Header.Set("Authorization", "Bearer "+token)
			preq.Header.Set("Content-Type", "application/octet-stream")
			preq.ContentLength = int64(n)
			presp, perr := httpc.Do(preq)
			if perr != nil {
				abortUpload(httpc, base, q, token, initOut.UploadID)
				return 0, fmt.Errorf("upload part %d: %w", partNum, perr)
			}
			if presp.StatusCode/100 != 2 {
				body, _ := io.ReadAll(io.LimitReader(presp.Body, 2048))
				presp.Body.Close()
				abortUpload(httpc, base, q, token, initOut.UploadID)
				return 0, fmt.Errorf("upload part %d: HTTP %d: %s", partNum, presp.StatusCode, strings.TrimSpace(string(body)))
			}
			presp.Body.Close()
			partNum++
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			abortUpload(httpc, base, q, token, initOut.UploadID)
			return 0, fmt.Errorf("read %s: %w", abs, rerr)
		}
	}
	sha := hex.EncodeToString(hasher.Sum(nil))

	// 3. complete.
	compBody, _ := json.Marshal(map[string]any{"sha256": sha})
	creq, _ := http.NewRequest("POST", base+"/uploads/"+initOut.UploadID+"/complete"+q, bytes.NewReader(compBody))
	creq.Header.Set("Authorization", "Bearer "+token)
	creq.Header.Set("Content-Type", "application/json")
	cresp, err := httpc.Do(creq)
	if err != nil {
		return 0, fmt.Errorf("upload complete: %w", err)
	}
	defer cresp.Body.Close()
	if cresp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(cresp.Body, 2048))
		return 0, fmt.Errorf("upload complete: HTTP %d: %s", cresp.StatusCode, strings.TrimSpace(string(body)))
	}
	var compOut struct {
		File struct {
			ID int64 `json:"id"`
		} `json:"file"`
	}
	if err := json.NewDecoder(cresp.Body).Decode(&compOut); err != nil {
		return 0, fmt.Errorf("upload complete decode: %w", err)
	}
	if compOut.File.ID == 0 {
		return 0, errors.New("upload complete returned no file id")
	}
	return compOut.File.ID, nil
}

// abortUpload best-effort releases the partial session on storage's
// side after a part error. Don't return its error — we already have
// one to surface; abort is housekeeping.
func abortUpload(httpc *http.Client, base, q, token, uploadID string) {
	req, _ := http.NewRequest("DELETE", base+"/uploads/"+uploadID+q, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if resp, err := httpc.Do(req); err == nil {
		resp.Body.Close()
	}
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
