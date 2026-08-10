// mover.go — completion-mover.
//
// On every transition into "completed" or "seeding", walk the
// torrent's files in working_dir and upload each one to the storage
// app with its chunked upload protocol. Stamp the resulting file IDs
// on the torrents row and emit `torrent.completed` on the platform
// bus. The media app discovers new files from storage events.
//
// The mover is restart-safe and idempotent. It records every uploaded
// path in upload_progress_json, resumes after partial failure, and
// marks completed_at only after every selected file is in storage.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
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

	// storageBoundProxyPath is the platform's streaming proxy for the
	// storage app declared in requires.apps. The proxy authenticates this
	// Torrent install, validates the binding and project, then replaces the
	// caller credential with the target Storage install's credential.
	storageBoundProxyPath = "/api/apps/callback/apps/storage/proxy"
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

	rows, err := torrentRowsForInfohash(a.ctx.AppDB(), infohash)
	if err != nil {
		a.ctx.Logger().Warn("transition rows", "ih", infohash, "err", err.Error())
		return
	}
	for _, row := range rows {
		a.persistSnapshot(row.ProjectID, infohash, snap)
		scoped := a.ctx.WithProject(row.ProjectID)
		switch next {
		case "completed", "seeding":
			go a.handleCompletion(row.ProjectID, infohash, snap)
		case "error":
			scoped.Emit("torrent.error", map[string]any{
				"id": row.ID, "infohash": infohash, "error": snap.LastError, "name": snap.Name,
			})
		}
	}
}

// persistSnapshot writes the current state into the torrents row. We
// don't try to capture every byte tick (the engine has that already);
// we just keep the DB consistent with the in-memory engine state on
// transitions.
func (a *App) persistSnapshot(projectID, infohash string, s TorrentSnapshot) {
	_, err := a.ctx.AppDB().Exec(
		`UPDATE torrents
		    SET name = COALESCE(NULLIF(?, ''), name),
		        total_bytes = ?,
		        downloaded_bytes = ?,
		        state = CASE
		          WHEN state = 'error' AND last_error <> '' AND ? = '' THEN state
		          WHEN state = 'paused' AND ? <> 'error' THEN state
		          ELSE ?
		        END,
		        last_error = CASE
		          WHEN state = 'error' AND last_error <> '' AND ? = '' THEN last_error
		          ELSE ?
		        END
		  WHERE project_id = ? AND infohash = ?`,
		s.Name, s.Length, s.BytesCompleted,
		s.LastError, s.State, s.State, s.LastError, s.LastError,
		projectID, infohash,
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
func (a *App) handleCompletion(projectID, infohash string, snap TorrentSnapshot) {
	flightKey := projectID + ":" + infohash
	if _, loaded := completionInFlight.LoadOrStore(flightKey, struct{}{}); loaded {
		a.ctx.Logger().Info("completion already in flight, skipping",
			"name", snap.Name, "ih", infohash)
		return
	}
	defer completionInFlight.Delete(flightKey)

	row, err := getTorrentRow(a.ctx.AppDB(), projectID, infohash)
	if err != nil {
		a.ctx.Logger().Warn("completion: row lookup", "err", err.Error())
		return
	}
	var existing []int64
	_ = json.Unmarshal([]byte(row.StorageFileIDsJSON), &existing)
	if row.CompletedAt != "" {
		if snap.State == "completed" && !configFlag(a.ctx, "keep_working_copy", false) {
			a.maybeCleanupWorkingCopy(infohash, snap.Name)
		}
		a.ctx.WithProject(projectID).Emit("torrent.completed", map[string]any{
			"id": row.ID, "infohash": infohash, "name": snap.Name, "file_ids": existing,
		})
		return
	}

	files, err := a.engine.FileSnapshots(infohash)
	if err != nil {
		a.markCompletionError(projectID, infohash, "completion: "+err.Error())
		return
	}
	if len(files) == 0 {
		a.markCompletionError(projectID, infohash, "completion: torrent metadata contains no files")
		return
	}

	target := row.TargetFolder
	if target == "" {
		target = configString(a.ctx, "default_target_folder", "/downloads")
	}
	target = strings.TrimRight(target, "/")
	if target == "" {
		target = "/"
	}
	// Choose the per-torrent root carefully. anacrolix's File.Path()
	// returns the BEP-3 path verbatim, which for any multi-file
	// torrent that declares a top-level "name" already starts with
	// that name (e.g. f.Path = "Show.S01/E01.mkv"). If we naively
	// prefix sanitiseName(snap.Name) on top, the storage path doubles
	// up to /downloads/Show.S01/Show.S01/E01.mkv — exactly what hit
	// the live cffaba02 install in v0.1.15.
	//
	// So: use target as the root when the torrent's files already
	// share a wrapper directory. Add the snap.Name wrapper only for
	// the rare "flat multi-file" case (>1 file, none of them in a
	// subdir) so those don't all collide in /downloads/ root.
	root := target
	if len(files) > 1 && !filesShareWrapperDir(files) {
		root = path.Join(target, sanitiseName(snap.Name))
	}

	progress := map[string]int64{}
	_ = json.Unmarshal([]byte(row.UploadProgressJSON), &progress)
	projectPriorities := decodePriorities(row.FilePrioritiesJSON)
	uploaded := make([]int64, 0, len(files))
	uploadErrors := []string{}
	hasIncompleteSelectedFile := false
	parentCtx := a.runCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	uploadCtx, cancel := context.WithTimeout(parentCtx, 2*time.Hour)
	defer cancel()
	scoped := a.ctx.WithProject(projectID)
	for _, f := range files {
		if priority, ok := projectPriorities[f.Index]; (ok && priority == "skip") || (!ok && f.Priority == "skip") {
			continue
		}
		// Skip files that didn't fully download (selective-skip case).
		if f.BytesCompleted < f.Length {
			hasIncompleteSelectedFile = true
			continue
		}
		if fileID := progress[f.Path]; fileID > 0 {
			uploaded = append(uploaded, fileID)
			continue
		}
		fileID, err := a.uploadOneFile(uploadCtx, scoped, root, f)
		if err != nil {
			uploadErrors = append(uploadErrors, f.Path+": "+err.Error())
			a.ctx.Logger().Warn("completion upload", "path", f.Path, "err", err.Error())
			continue
		}
		uploaded = append(uploaded, fileID)
		progress[f.Path] = fileID
		progressJSON, _ := json.Marshal(progress)
		_, _ = a.ctx.AppDB().Exec(
			`UPDATE torrents SET upload_progress_json = ? WHERE project_id = ? AND infohash = ?`,
			string(progressJSON), projectID, infohash)
		// Media indexing happens automatically via the media app's
		// storage.file.added subscription — no MCP round-trip needed
		// from here. (Earlier versions called a non-existent
		// "media.probe_file" tool that errored silently.)
	}
	if hasIncompleteSelectedFile {
		return
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
		a.markCompletionError(projectID, infohash, msg)
		scoped.Emit("torrent.error", map[string]any{
			"id": row.ID, "infohash": infohash, "name": snap.Name,
			"error": msg, "phase": "completion-upload",
		})
		return
	}

	idsJSON, _ := json.Marshal(uploaded)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := a.ctx.AppDB().Exec(
		`UPDATE torrents
			    SET storage_file_ids_json = ?, upload_progress_json = '{}',
			        completed_at = ?, state = ?, last_error = ''
			  WHERE project_id = ? AND infohash = ?`,
		string(idsJSON), now, snap.State, projectID, infohash,
	); err != nil {
		a.markCompletionError(projectID, infohash, "persist completed upload: "+err.Error())
		return
	}

	if snap.State == "completed" && !configFlag(a.ctx, "keep_working_copy", false) {
		a.maybeCleanupWorkingCopy(infohash, snap.Name)
	}

	scoped.Emit("torrent.completed", map[string]any{
		"id":       row.ID,
		"infohash": infohash,
		"name":     snap.Name,
		"file_ids": uploaded,
	})
}

func (a *App) markCompletionError(projectID, infohash, msg string) {
	_, _ = a.ctx.AppDB().Exec(
		`UPDATE torrents SET state = 'error', last_error = ?
		  WHERE project_id = ? AND infohash = ?`,
		msg, projectID, infohash)
}

// uploadOneFile streams one local file into storage via the chunked
// /uploads protocol. Bypasses the base64-inline files_upload tool
// which capped at storage's max_upload_size_mb (default 100 MB) and
// pulled the whole file into RAM before write.
//
// The relative path inside the torrent is preserved under `root` —
// a torrent like Movie.X/subs/en.srt becomes {target}/Movie.X/subs/en.srt.
//
// Wire (cross-app HTTP via the platform's bound-app streaming proxy):
//
//	POST   {gateway}/api/apps/callback/apps/storage/proxy/uploads
//	PUT    {gateway}/api/apps/callback/apps/storage/proxy/uploads/{id}/parts/{N}     (×many)
//	POST   {gateway}/api/apps/callback/apps/storage/proxy/uploads/{id}/complete
//
// Auth: APTEVA_OUTBOUND_TOKEN (falling back to APTEVA_APP_TOKEN for
// older runtimes). Project: APTEVA_PROJECT_ID query param so the
// platform selects the correctly bound project-scoped Storage install.
func (a *App) uploadOneFile(requestCtx context.Context, ctx *sdk.AppCtx, root string, f FileSnapshot) (int64, error) {
	working := resolveWorkingDir(a.ctx)
	abs, err := safeChildPath(working, f.Path)
	if err != nil {
		return 0, err
	}

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
		folder = path.Join(root, strings.Trim(relDir, "/"))
	}
	contentType := guessContentType(f.Path)

	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := strings.TrimSpace(os.Getenv("APTEVA_OUTBOUND_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	}
	if gateway == "" || token == "" {
		return 0, errors.New("APTEVA_GATEWAY_URL / outbound app token not set in env")
	}
	pid := projectScope(ctx)
	base := gateway + storageBoundProxyPath
	q := "?project_id=" + url.QueryEscape(pid)

	httpc := &http.Client{Timeout: 15 * time.Minute}

	// 1. init session.
	initBody, _ := json.Marshal(map[string]any{
		"filename":     name,
		"size":         stat.Size(),
		"content_type": contentType,
		"folder":       folder,
	})
	req, err := http.NewRequestWithContext(requestCtx, "POST", base+"/uploads"+q, bytes.NewReader(initBody))
	if err != nil {
		return 0, err
	}
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
	if initOut.UploadID == "" {
		return 0, errors.New("upload init returned no upload id")
	}

	partSize := initOut.PartSize
	if partSize <= 0 {
		partSize = chunkSize
	}
	if partSize > 64*1024*1024 {
		abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
		return 0, fmt.Errorf("upload part size %d exceeds 64 MiB safety limit", partSize)
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
			preq, err := http.NewRequestWithContext(requestCtx, "PUT", partURL, bytes.NewReader(chunk))
			if err != nil {
				abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
				return 0, err
			}
			preq.Header.Set("Authorization", "Bearer "+token)
			preq.Header.Set("Content-Type", "application/octet-stream")
			preq.ContentLength = int64(n)
			presp, perr := httpc.Do(preq)
			if perr != nil {
				abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
				return 0, fmt.Errorf("upload part %d: %w", partNum, perr)
			}
			if presp.StatusCode/100 != 2 {
				body, _ := io.ReadAll(io.LimitReader(presp.Body, 2048))
				presp.Body.Close()
				abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
				return 0, fmt.Errorf("upload part %d: HTTP %d: %s", partNum, presp.StatusCode, strings.TrimSpace(string(body)))
			}
			presp.Body.Close()
			partNum++
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
			return 0, fmt.Errorf("read %s: %w", abs, rerr)
		}
	}
	sha := hex.EncodeToString(hasher.Sum(nil))

	// 3. complete.
	compBody, _ := json.Marshal(map[string]any{"sha256": sha})
	creq, err := http.NewRequestWithContext(requestCtx, "POST", base+"/uploads/"+initOut.UploadID+"/complete"+q, bytes.NewReader(compBody))
	if err != nil {
		abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
		return 0, err
	}
	creq.Header.Set("Authorization", "Bearer "+token)
	creq.Header.Set("Content-Type", "application/json")
	cresp, err := httpc.Do(creq)
	if err != nil {
		abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
		return 0, fmt.Errorf("upload complete: %w", err)
	}
	defer cresp.Body.Close()
	if cresp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(cresp.Body, 2048))
		abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
		return 0, fmt.Errorf("upload complete: HTTP %d: %s", cresp.StatusCode, strings.TrimSpace(string(body)))
	}
	var compOut struct {
		File struct {
			ID int64 `json:"id"`
		} `json:"file"`
	}
	if err := json.NewDecoder(cresp.Body).Decode(&compOut); err != nil {
		abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
		return 0, fmt.Errorf("upload complete decode: %w", err)
	}
	if compOut.File.ID == 0 {
		abortUpload(requestCtx, httpc, base, q, token, initOut.UploadID)
		return 0, errors.New("upload complete returned no file id")
	}
	return compOut.File.ID, nil
}

// abortUpload best-effort releases the partial session on storage's
// side after a part error. Don't return its error — we already have
// one to surface; abort is housekeeping.
func abortUpload(_ context.Context, httpc *http.Client, base, q, token, uploadID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", base+"/uploads/"+uploadID+q, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if resp, err := httpc.Do(req); err == nil {
		resp.Body.Close()
	}
}

// filesShareWrapperDir — true when every file in the torrent has at
// least one path separator, i.e. they all live inside some parent
// directory. anacrolix puts the BEP-3 top-level name into Path()
// for multi-file torrents, so this is the cheap check for "the
// torrent already brings its own wrapper".
func filesShareWrapperDir(files []FileSnapshot) bool {
	for _, f := range files {
		if !strings.Contains(f.Path, "/") {
			return false
		}
	}
	return true
}

// cleanupWorkingCopy removes the local copy under working_dir. Best
// effort — if removal fails (file in use, permission), we log and
// move on; the next poll will retry on the next state change.
func (a *App) cleanupWorkingCopy(torrentName string) error {
	working := resolveWorkingDir(a.ctx)
	target, err := safeChildPath(working, torrentName)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		a.ctx.Logger().Warn("cleanup working copy", "path", target, "err", err.Error())
		return err
	}
	return nil
}

func (a *App) maybeCleanupWorkingCopy(infohash, torrentName string) {
	var pending int
	if err := a.ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM torrents WHERE infohash = ? AND completed_at IS NULL`, infohash).Scan(&pending); err != nil || pending > 0 {
		return
	}
	if err := a.cleanupWorkingCopy(torrentName); err != nil {
		a.ctx.Logger().Warn("cleanup working copy", "ih", infohash, "err", err.Error())
	}
}

func torrentRowsForInfohash(db *sql.DB, infohash string) ([]TorrentRow, error) {
	rows, err := db.Query(
		`SELECT id, project_id, infohash, name, magnet, target_folder,
		        total_bytes, downloaded_bytes, state, storage_file_ids_json,
		        upload_progress_json, file_priorities_json,
		        last_error, added_at, completed_at
		   FROM torrents WHERE infohash = ? ORDER BY added_at DESC`, infohash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TorrentRow{}
	for rows.Next() {
		var row TorrentRow
		var completed sql.NullString
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Infohash, &row.Name, &row.Magnet,
			&row.TargetFolder, &row.TotalBytes, &row.DownloadedBytes, &row.State,
			&row.StorageFileIDsJSON, &row.UploadProgressJSON, &row.FilePrioritiesJSON,
			&row.LastError, &row.AddedAt, &completed); err != nil {
			return nil, err
		}
		row.CompletedAt = completed.String
		out = append(out, row)
	}
	return out, rows.Err()
}

func (a *App) retryPendingCompletions() {
	rows, err := listAllTorrentRows(a.ctx.AppDB())
	if err != nil {
		a.ctx.Logger().Warn("completion retry list", "err", err.Error())
		return
	}
	for _, row := range rows {
		if row.CompletedAt != "" {
			continue
		}
		snap := a.engine.Snapshot(row.Infohash)
		if snap == nil || !snap.HasInfo || snap.BytesMissing != 0 {
			continue
		}
		go a.handleCompletion(row.ProjectID, row.Infohash, *snap)
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
