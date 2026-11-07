package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxUploadAttempts = 5

type uploadState struct {
	Status        string `json:"status"`
	StorageFileID int64  `json:"storage_file_id,omitempty"`
	Attempts      int    `json:"attempts,omitempty"`
	Error         string `json:"error,omitempty"`
}

// configPresets reads project presets from export_presets_json.
func (a *App) configPresets() ([]Preset, error) {
	raw := ""
	if a.ctx != nil {
		raw = strings.TrimSpace(a.ctx.Config().Get("export_presets_json"))
	}
	if raw == "" {
		return nil, nil
	}
	var presets []Preset
	if err := json.Unmarshal([]byte(raw), &presets); err != nil {
		return nil, problem("invalid_configuration", "export_presets_json must be an array of preset objects")
	}
	for _, p := range presets {
		if err := validatePreset(p); err != nil {
			return nil, problem("invalid_configuration", "export_presets_json: "+err.Error())
		}
	}
	return presets, nil
}

// prepareRecordCall resolves presets here so a remote runner never needs the
// project's configuration.
func (a *App) prepareRecordCall(tool string, args map[string]any) (map[string]any, any, error) {
	if args == nil {
		args = map[string]any{}
	}
	switch {
	case tool == "mobile_record" && str(args, "action") == "presets":
		extra, err := a.configPresets()
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"presets": append(extra, builtinPresets()...)}, nil
	case tool == "mobile_record" && str(args, "action") == "upload":
		rid := str(args, "recording_id")
		if rid == "" {
			return nil, nil, problem("invalid_argument", "recording_id is required")
		}
		if _, err := a.ctx.AppDB().Exec(`DELETE FROM mobile_recording_uploads WHERE recording_id=? AND project_id=? AND status<>'uploading'`, rid, a.project); err != nil {
			return nil, nil, err
		}
		go a.uploadPending(context.Background(), rid)
		return nil, map[string]any{"recording_id": rid, "upload": uploadState{Status: "queued"}}, nil
	case tool == "mobile_record_export":
		out := merge(args, nil)
		delete(out, "preset_definition")
		if custom, ok := args["custom"]; ok {
			var p Preset
			if err := decodeMap(custom, &p); err != nil {
				return nil, nil, problem("invalid_argument", "custom must be a preset object")
			}
			if err := validatePreset(p); err != nil {
				return nil, nil, err
			}
			delete(out, "custom")
			out["preset_definition"] = p
			return out, nil, nil
		}
		extra, err := a.configPresets()
		if err != nil {
			return nil, nil, err
		}
		p, ok := findPreset(str(args, "preset"), extra)
		if !ok {
			return nil, nil, problem("invalid_argument", "unknown preset; list presets with mobile_record(action=presets)")
		}
		out["preset_definition"] = p
		return out, nil, nil
	}
	return args, nil, nil
}

func (a *App) recordingURL(rid string) string {
	return "/api/apps/mobile/api/recordings/" + url.PathEscape(rid) + "/file?project_id=" + url.QueryEscape(a.project)
}

// decorateRecordings adds the download URL and storage upload state.
func (a *App) decorateRecordings(result any) any {
	var out map[string]any
	if decodeMap(result, &out) != nil {
		return result
	}
	decorate := func(v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		rid := str(m, "id")
		if rid == "" {
			return v
		}
		if str(m, "status") == "ready" {
			m["file_url"] = a.recordingURL(rid)
		}
		if st, ok := a.uploadState(rid); ok {
			m["upload"] = st
		}
		return m
	}
	if r, ok := out["recording"]; ok {
		out["recording"] = decorate(r)
	}
	if list, ok := out["recordings"].([]any); ok {
		for i := range list {
			list[i] = decorate(list[i])
		}
	}
	return out
}

func (a *App) uploadState(rid string) (uploadState, bool) {
	var st uploadState
	err := a.ctx.AppDB().QueryRow(`SELECT status,storage_file_id,attempts,error FROM mobile_recording_uploads WHERE recording_id=? AND project_id=?`, rid, a.project).Scan(&st.Status, &st.StorageFileID, &st.Attempts, &st.Error)
	return st, err == nil
}

// openRecording returns the full recording bytes from the local engine or
// the remote runner.
func (a *App) openRecording(ctx context.Context, rid string) (io.ReadCloser, error) {
	if a.local != nil {
		p, _, err := a.local.RecordingFile(a.project, rid)
		if err != nil {
			return nil, err
		}
		return os.Open(p)
	}
	r, ok := a.executor.(*remote)
	if !ok {
		return nil, errors.New("recording reader is unavailable")
	}
	res, err := r.stream(ctx, a.project, rid, "")
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

func (a *App) handleRecording(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "GET required", 405)
		return
	}
	if err := a.requestProject(r); err != nil {
		writeFailure(w, err)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/recordings/")
	rid, tail, _ := strings.Cut(rest, "/")
	if rid == "" || tail != "file" {
		writeFailure(w, problem("not_found", "unknown recording path"))
		return
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	if a.local != nil {
		p, rec, err := a.local.RecordingFile(a.project, rid)
		if err != nil {
			writeFailure(w, err)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			writeFailure(w, err)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			writeFailure(w, err)
			return
		}
		serveVideoHeaders(w, rec.ContentType, disposition, rec.FileName)
		http.ServeContent(w, r, rec.FileName, info.ModTime(), f)
		return
	}
	rem, ok := a.executor.(*remote)
	if !ok {
		writeFailure(w, errors.New("recording reader is unavailable"))
		return
	}
	res, err := rem.stream(r.Context(), a.project, rid, r.Header.Get("Range"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	defer res.Body.Close()
	for _, h := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	serveVideoHeaders(w, res.Header.Get("Content-Type"), disposition, res.Header.Get("X-Recording-Name"))
	w.WriteHeader(res.StatusCode)
	if r.Method != "HEAD" {
		_, _ = io.Copy(w, res.Body)
	}
}

func serveVideoHeaders(w http.ResponseWriter, contentType, disposition, name string) {
	if contentType != "video/mp4" && contentType != "video/quicktime" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if name != "" {
		w.Header().Set("Content-Disposition", disposition+"; filename=\""+unsafeName.ReplaceAllString(name, "_")+"\"")
	}
}

func (a *App) recordingUploadWorker(ctx context.Context, _ *sdk.AppCtx) error {
	if a.executor == nil || a.ctx == nil || a.ctx.PlatformAPI() == nil {
		return nil
	}
	a.uploadPending(ctx, "")
	return nil
}

// uploadPending copies finished recordings into Storage. Only one pass runs
// at a time; only is restricted to one recording when set.
func (a *App) uploadPending(ctx context.Context, only string) {
	if !a.uploadMu.TryLock() {
		return
	}
	defer a.uploadMu.Unlock()
	args := map[string]any{"action": "list", "status": "ready", "limit": 50}
	v, err := a.executor.Execute(ctx, a.project, "mobile_record", args)
	if err != nil {
		return
	}
	var out struct {
		Recordings []Recording `json:"recordings"`
	}
	if decodeMap(v, &out) != nil {
		return
	}
	done := 0
	for _, rec := range out.Recordings {
		if only != "" && rec.ID != only || done >= 3 {
			continue
		}
		st, exists := a.uploadState(rec.ID)
		if exists {
			wait := time.Duration(1<<st.Attempts) * time.Minute
			var updated int64
			_ = a.ctx.AppDB().QueryRow(`SELECT updated_at FROM mobile_recording_uploads WHERE recording_id=?`, rec.ID).Scan(&updated)
			if st.Status == "uploaded" || st.Status == "uploading" || st.Attempts >= maxUploadAttempts || time.Since(time.Unix(updated, 0)) < wait {
				continue
			}
		}
		done++
		a.setUpload(rec.ID, uploadState{Status: "uploading", Attempts: st.Attempts})
		fileID, err := a.uploadRecording(ctx, rec)
		if err != nil {
			a.setUpload(rec.ID, uploadState{Status: "failed", Attempts: st.Attempts + 1, Error: err.Error()})
			continue
		}
		a.setUpload(rec.ID, uploadState{Status: "uploaded", StorageFileID: fileID, Attempts: st.Attempts + 1})
		a.ctx.EmitWithProject("mobile.recording.uploaded", a.project, map[string]any{"recording_id": rec.ID, "storage_file_id": fileID, "kind": rec.Kind, "preset": rec.Preset})
	}
}

func (a *App) setUpload(rid string, st uploadState) {
	_, _ = a.ctx.AppDB().Exec(`INSERT INTO mobile_recording_uploads(recording_id,project_id,status,storage_file_id,attempts,error,updated_at) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(recording_id) DO UPDATE SET status=excluded.status,storage_file_id=excluded.storage_file_id,attempts=excluded.attempts,error=excluded.error,updated_at=excluded.updated_at`,
		rid, a.project, st.Status, st.StorageFileID, st.Attempts, st.Error, time.Now().Unix())
}

// uploadRecording streams one file to Storage in 1 MiB parts, the same
// bounded-memory protocol other apps use for large renders.
func (a *App) uploadRecording(ctx context.Context, rec Recording) (int64, error) {
	if rec.SizeBytes <= 0 {
		return 0, errors.New("recording size is unknown")
	}
	body, err := a.openRecording(ctx, rec.ID)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	api := a.ctx.PlatformAPI()
	call := func(tool string, args map[string]any, out any) error {
		args["_project_id"] = a.project
		return sdk.CallAppResultContext(ctx, api, "storage", tool, args, out)
	}
	folder := "/.mobile/recordings/"
	tags := []string{"mobile", "recording"}
	if rec.Kind == "export" {
		folder = "/.mobile/exports/"
		tags = []string{"mobile", "export", rec.Preset}
	}
	var init struct {
		UploadID string `json:"upload_id"`
		PartSize int    `json:"part_size"`
		File     struct {
			ID int64 `json:"id"`
		} `json:"file"`
	}
	if err := call("storage_upload_init", map[string]any{"name": rec.FileName, "size_bytes": rec.SizeBytes, "folder": folder, "content_type": rec.ContentType, "tags": tags, "source": "mobile"}, &init); err != nil {
		return 0, fmt.Errorf("storage is unavailable (install Storage and bind it to Mobile): %w", err)
	}
	if init.File.ID > 0 {
		return init.File.ID, nil
	}
	if init.UploadID == "" {
		return 0, errors.New("storage returned no upload session")
	}
	completed := false
	defer func() {
		if !completed {
			var out any
			_ = call("storage_abort_upload", map[string]any{"id": init.UploadID, "reason": "Mobile recording upload interrupted"}, &out)
		}
	}()
	partSize := init.PartSize
	if partSize <= 0 || partSize > 1<<20 {
		partSize = 1 << 20
	}
	hash := sha256.New()
	buf := make([]byte, partSize)
	var sent int64
	for part := 1; ; part++ {
		n, err := io.ReadFull(body, buf)
		if err == io.EOF {
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return 0, err
		}
		hash.Write(buf[:n])
		sent += int64(n)
		var out any
		if err := call("storage_upload_part", map[string]any{"upload_id": init.UploadID, "part_number": part, "content_base64": base64.StdEncoding.EncodeToString(buf[:n])}, &out); err != nil {
			return 0, err
		}
	}
	if sent != rec.SizeBytes {
		return 0, fmt.Errorf("read %d of %d bytes", sent, rec.SizeBytes)
	}
	var result struct {
		File struct {
			ID int64 `json:"id"`
		} `json:"file"`
	}
	if err := call("storage_upload_complete", map[string]any{"upload_id": init.UploadID, "sha256": hex.EncodeToString(hash.Sum(nil))}, &result); err != nil {
		return 0, err
	}
	if result.File.ID <= 0 {
		return 0, errors.New("storage did not return a file")
	}
	completed = true
	return result.File.ID, nil
}
