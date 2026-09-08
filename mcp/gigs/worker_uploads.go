package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type workerUpload struct {
	ID             string `json:"upload_id"`
	AssignmentID   int64  `json:"-"`
	ProjectID      string `json:"-"`
	Status         string `json:"status"`
	FileID         int64  `json:"storage_file_id,omitempty"`
	InstructionKey string `json:"instruction_key"`
	Filename       string `json:"filename"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	Transport      string `json:"transport"`
	PartSize       int64  `json:"part_size"`
	ClientKey      string `json:"client_key,omitempty"`
	Error          string `json:"error_detail,omitempty"`
	Stage          string `json:"stage,omitempty"`
}

var workerUploadLocks [64]sync.Mutex
var finalizationSlots = make(chan struct{}, 2)

func workerUploadLock(key string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return &workerUploadLocks[h.Sum32()%64]
}

const workerUploadSelect = `SELECT upload_id,assignment_id,project_id,status,COALESCE(storage_file_id,0),
 COALESCE(instruction_key,''),COALESCE(filename,''),COALESCE(content_type,''),COALESCE(size_bytes,0),
 transport,part_size,client_key,COALESCE(last_error,''),COALESCE(last_stage,'') FROM gig_upload_sessions`

func scanWorkerUpload(row interface{ Scan(...any) error }) (*workerUpload, error) {
	u := &workerUpload{}
	err := row.Scan(&u.ID, &u.AssignmentID, &u.ProjectID, &u.Status, &u.FileID, &u.InstructionKey, &u.Filename, &u.ContentType, &u.SizeBytes, &u.Transport, &u.PartSize, &u.ClientKey, &u.Error, &u.Stage)
	return u, err
}
func loadWorkerUpload(db *sql.DB, id string, aid int64, pid string) (*workerUpload, error) {
	return scanWorkerUpload(db.QueryRow(workerUploadSelect+` WHERE upload_id=? AND assignment_id=? AND project_id=?`, id, aid, pid))
}

func uploadWorkerState(w http.ResponseWriter, ctx *sdk.AppCtx, token string) (aid, gid int64, pid string, ok bool) {
	aid, gid, pid, ast, gst, _, revoked, expired, err := loadAssignmentState(ctx.AppDB(), token)
	if err != nil {
		httpErr(w, 404, "invalid or expired link")
		return 0, 0, "", false
	}
	if !assignmentAcceptsWork(ast, gst, revoked, expired) {
		httpErr(w, 410, "This assignment is closed. Contact your manager to restore access.")
		return 0, 0, "", false
	}
	return aid, gid, pid, true
}

func beginBinaryWorkerUpload(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, aid, gid int64, pid, key, name, mime string, size int64, clientKey string) {
	if len(clientKey) != 64 {
		httpErr(w, 400, "file fingerprint required")
		return
	}
	for _, c := range clientKey {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			httpErr(w, 400, "invalid file fingerprint")
			return
		}
	}
	mu := workerUploadLock("init:" + strconv.FormatInt(aid, 10))
	mu.Lock()
	defer mu.Unlock()
	old, err := scanWorkerUpload(ctx.AppDB().QueryRow(workerUploadSelect+` WHERE assignment_id=? AND instruction_key=? AND client_key=? AND status IN ('uploading','finalizing','completed') ORDER BY created_at DESC LIMIT 1`, aid, key, clientKey))
	if err == nil {
		if old.Filename != name || old.SizeBytes != size {
			httpErr(w, 409, "file fingerprint does not match upload")
			return
		}
		httpJSON(w, old)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		httpErr(w, 500, "Could not look up upload")
		return
	}
	var active int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM gig_upload_sessions WHERE assignment_id=? AND status IN ('uploading','finalizing')`, aid).Scan(&active); err != nil {
		httpErr(w, 500, "Could not check upload capacity")
		return
	}
	if active >= 12 {
		httpErr(w, 429, "Too many unfinished uploads. Resume or cancel an existing file first.")
		return
	}
	folder := fmt.Sprintf("%s/submissions/%d", storageRoot(ctx), gid)
	var init struct {
		UploadID string `json:"upload_id"`
		PartSize int64  `json:"part_size"`
	}
	err = storageHTTPJSON(r.Context(), pid, http.MethodPost, "/uploads", map[string]any{"filename": name, "size": size, "content_type": mime, "folder": folder, "source": "gigs-worker", "visibility": "private"}, &init)
	if err != nil {
		httpErr(w, 502, "Could not start upload: "+err.Error())
		return
	}
	if !validStorageUploadID(init.UploadID) || init.PartSize < 1 || init.PartSize > 16<<20 {
		httpErr(w, 502, "Storage returned an unsupported upload session")
		return
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO gig_upload_sessions(upload_id,assignment_id,project_id,status,instruction_key,filename,content_type,size_bytes,transport,part_size,client_key,updated_at) VALUES (?,?,?,'uploading',?,?,?,?,'binary',?,?,CURRENT_TIMESTAMP)`, init.UploadID, aid, pid, key, name, mime, size, init.PartSize, clientKey)
	if err != nil {
		_ = storageHTTPJSON(r.Context(), pid, http.MethodDelete, "/uploads/"+init.UploadID, nil, nil)
		httpErr(w, 500, "Could not register upload; please retry")
		return
	}
	u, _ := loadWorkerUpload(ctx.AppDB(), init.UploadID, aid, pid)
	httpJSON(w, u)
}

func validStorageUploadID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ", c) {
			return false
		}
	}
	return true
}

func recordUploadError(ctx *sdk.AppCtx, u *workerUpload, stage string, err error) string {
	detail := err.Error()
	if len(detail) > 500 {
		detail = detail[:500]
	}
	message := fmt.Sprintf("%s failed: %s (reference %s)", stage, detail, u.ID)
	if u.Stage == "finalize" {
		stage = "finalize"
	}
	_, _ = ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET last_error=?,last_stage=?,updated_at=CURRENT_TIMESTAMP WHERE upload_id=?`, message, stage, u.ID)
	ctx.Logger().Warn("worker upload failed", "upload_id", u.ID, "assignment_id", u.AssignmentID, "stage", stage, "error", detail)
	return message
}

func (a *App) handleWorkerBinaryPart(w http.ResponseWriter, r *http.Request, token string) {
	ctx := globalCtx
	aid, _, pid, ok := uploadWorkerState(w, ctx, token)
	if !ok {
		return
	}
	id := r.URL.Query().Get("upload_id")
	n, _ := strconv.Atoi(r.URL.Query().Get("part_number"))
	u, err := loadWorkerUpload(ctx.AppDB(), id, aid, pid)
	if err != nil {
		httpErr(w, 404, "upload session not found")
		return
	}
	if u.Transport != "binary" || u.Status != "uploading" {
		httpErr(w, 409, "upload is no longer receiving parts")
		return
	}
	if n < 1 || u.PartSize < 1 || int64(n) > ((u.SizeBytes+u.PartSize-1)/u.PartSize) {
		httpErr(w, 400, "invalid part number")
		return
	}
	expected := u.PartSize
	if remain := u.SizeBytes - int64(n-1)*u.PartSize; remain < expected {
		expected = remain
	}
	if r.ContentLength != expected {
		httpErr(w, 400, "part length does not match declared file size")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, expected)
	var result struct {
		Size int64 `json:"size"`
	}
	err = storageHTTP(r.Context(), pid, http.MethodPut, fmt.Sprintf("/uploads/%s/parts/%d", u.ID, n), r.Body, expected, &result)
	if err != nil {
		httpErr(w, 502, recordUploadError(ctx, u, fmt.Sprintf("Part %d", n), err))
		return
	}
	if result.Size != expected {
		httpErr(w, 502, recordUploadError(ctx, u, "Part", errors.New("Storage acknowledged the wrong byte count")))
		return
	}
	_, _ = ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET updated_at=CURRENT_TIMESTAMP,last_error=NULL,last_stage='upload' WHERE upload_id=? AND status='uploading'`, u.ID)
	httpJSON(w, map[string]any{"part_number": n, "size": result.Size})
}

func (a *App) handleWorkerUploadStatus(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST only")
		return
	}
	ctx := globalCtx
	aid, _, pid, ok := uploadWorkerState(w, ctx, token)
	if !ok {
		return
	}
	var body struct {
		UploadID string `json:"upload_id"`
	}
	if httpDecode(r, &body) != nil {
		httpErr(w, 400, "upload_id required")
		return
	}
	u, err := loadWorkerUpload(ctx.AppDB(), body.UploadID, aid, pid)
	if err != nil {
		httpErr(w, 404, "upload session not found")
		return
	}
	if u.Transport != "binary" {
		httpErr(w, 409, "This older upload cannot resume. Select the file again.")
		return
	}
	if u.Status != "uploading" {
		httpJSON(w, u)
		return
	}
	var status storageUploadStatus
	err = storageHTTPJSON(r.Context(), pid, http.MethodGet, "/uploads/"+u.ID, nil, &status)
	if err != nil {
		var he *storageHTTPError
		if errors.As(err, &he) && he.Status == 404 {
			if u.Stage == "finalize" {
				startBinaryFinalization(ctx, u)
				u.Status = "finalizing"
				httpJSON(w, u)
				return
			}
			_, _ = ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='expired',last_error='Partial upload expired. Select the original file to restart.',updated_at=CURRENT_TIMESTAMP WHERE upload_id=? AND status='uploading'`, u.ID)
			u.Status = "expired"
			u.Error = "Partial upload expired. Select the original file to restart."
			httpJSON(w, u)
			return
		}
		httpErr(w, 502, recordUploadError(ctx, u, "Status", err))
		return
	}
	httpJSON(w, map[string]any{"upload_id": u.ID, "status": u.Status, "parts": status.Parts, "bytes_uploaded": status.BytesUploaded, "part_size": u.PartSize, "stage": u.Stage, "error_detail": u.Error})
}

func startBinaryFinalization(ctx *sdk.AppCtx, u *workerUpload) bool {
	res, err := ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='finalizing',last_stage='finalize',last_error=NULL,updated_at=CURRENT_TIMESTAMP WHERE upload_id=? AND status='uploading'`, u.ID)
	if err != nil {
		return false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false
	}
	go func() {
		finalizationSlots <- struct{}{}
		defer func() { <-finalizationSlots }()
		c, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		var completed storageCompletion
		err := storageHTTPJSON(c, u.ProjectID, http.MethodPost, "/uploads/"+u.ID+"/complete", map[string]any{}, &completed)
		if err == nil && completed.File.ID <= 0 {
			err = errors.New("Storage completed without a file id")
		}
		if err == nil {
			err = commitWorkerUpload(ctx, u, completed)
		}
		if err != nil {
			recordUploadError(ctx, u, "finalize", err)
			// Preserve parts and the durable Storage receipt. Retry completion instead
			// of deleting a file which may already have been committed remotely.
			_, _ = ctx.AppDB().Exec(`UPDATE gig_upload_sessions SET status='uploading' WHERE upload_id=? AND status='finalizing'`, u.ID)
		}
	}()
	return true
}

func commitWorkerUpload(ctx *sdk.AppCtx, u *workerUpload, completed storageCompletion) error {
	var gid int64
	if err := ctx.AppDB().QueryRow(`SELECT gig_id FROM gig_assignments WHERE id=?`, u.AssignmentID).Scan(&gid); err != nil {
		return err
	}
	requirement, err := loadGigFileRequirement(ctx.AppDB(), gid, u.InstructionKey)
	if err != nil {
		return err
	}
	if requirement == nil {
		return errors.New("instruction no longer accepts files")
	}
	f := completed.File
	if f.SizeBytes != u.SizeBytes {
		return errors.New("Storage file size does not match the selected file")
	}
	if err := responseAcceptsFile(requirement.Spec.Files, f.Name, f.ContentType, f.SizeBytes); err != nil {
		return err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE gig_upload_sessions SET status='completed',storage_file_id=?,filename=?,content_type=?,size_bytes=?,was_existing=?,completed_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP,last_error=NULL,last_stage='saved' WHERE upload_id=? AND status='finalizing'`, f.ID, f.Name, f.ContentType, f.SizeBytes, completed.WasExisting, u.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("upload changed before completion")
	}
	var editable int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM gig_assignments a JOIN gigs g ON g.id=a.gig_id WHERE a.id=? AND a.status IN ('accepted','submitted') AND g.status IN ('accepted','submitted') AND a.token_revoked_at IS NULL AND (a.token_expires_at IS NULL OR datetime(a.token_expires_at)>datetime('now'))`, u.AssignmentID).Scan(&editable); err != nil {
		return err
	}
	if editable == 0 {
		return tx.Commit()
	}
	// Save the file association immediately, including when the browser closed
	// while Storage finalized. A later batch file cannot strand this attachment.
	var raw string
	err = tx.QueryRow(`SELECT payload_json FROM gig_assignment_drafts WHERE assignment_id=?`, u.AssignmentID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRow(`SELECT payload_json FROM gig_submissions WHERE assignment_id=? ORDER BY id DESC LIMIT 1`, u.AssignmentID).Scan(&raw)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	payload := map[string]any{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return err
		}
	}
	stripWorkerSignedURLs(payload)
	ref := map[string]any{"storage_file_id": f.ID, "filename": f.Name, "mime": f.ContentType}
	var kind, bodyJSON string
	if err := tx.QueryRow(`SELECT instruction_kind,rendered_body_json FROM gig_instructions WHERE gig_id=? AND sort_order=?`, gid, requirement.SortOrder).Scan(&kind, &bodyJSON); err != nil {
		return err
	}
	instructionBody := map[string]any{}
	_ = json.Unmarshal([]byte(bodyJSON), &instructionBody)
	if responseSpecFromBody(instructionBody).Files.Enabled {
		responses, _ := payload["instruction_responses"].([]any)
		var entry map[string]any
		for _, raw := range responses {
			m, _ := raw.(map[string]any)
			if strOf(m["key"]) == u.InstructionKey {
				entry = m
				break
			}
		}
		if entry == nil {
			entry = map[string]any{"key": u.InstructionKey, "step": requirement.SortOrder + 1, "instruction_kind": kind}
			responses = append(responses, entry)
		}
		files, _ := entry["files"].([]any)
		found := false
		for _, raw := range files {
			m, _ := raw.(map[string]any)
			if int64Cast(m["storage_file_id"]) == f.ID {
				found = true
			}
		}
		if !found {
			files = append(files, ref)
		}
		if requirement.Spec.Files.MaxItems > 0 && len(files) > requirement.Spec.Files.MaxItems {
			return errors.New("remove an existing file before finishing this upload")
		}
		entry["files"] = files
		payload["instruction_responses"] = responses
	} else {
		payload[u.InstructionKey] = ref
	}
	ids := draftAttachmentIDs(payload)
	if _, err := tx.Exec(`INSERT INTO gig_assignment_drafts(assignment_id,payload_json,attachment_file_ids_json,revision,updated_at) VALUES(?,?,?,1,CURRENT_TIMESTAMP) ON CONFLICT(assignment_id) DO UPDATE SET payload_json=excluded.payload_json,attachment_file_ids_json=excluded.attachment_file_ids_json,revision=gig_assignment_drafts.revision+1,updated_at=CURRENT_TIMESTAMP`, u.AssignmentID, mustJSON(payload), mustJSON(ids)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ctx.Logger().Info("worker upload saved", "upload_id", u.ID, "assignment_id", u.AssignmentID, "file_id", f.ID, "size_bytes", f.SizeBytes)
	return nil
}

// Reconcile lost sessions in bounded batches. Storage owns its expiry policy;
// never infer expiry from the age of a valid long-running upload.
func reconcileWorkerUploads(c context.Context, ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().QueryContext(c, workerUploadSelect+` WHERE status='uploading' AND updated_at<datetime('now','-1 hour') ORDER BY updated_at LIMIT 20`)
	if err != nil {
		return err
	}
	var items []*workerUpload
	for rows.Next() {
		u, e := scanWorkerUpload(rows)
		if e != nil {
			rows.Close()
			return e
		}
		items = append(items, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range items {
		if c.Err() != nil {
			return c.Err()
		}
		exists := true
		if u.Transport == "binary" {
			var status storageUploadStatus
			e := storageHTTPJSON(c, u.ProjectID, http.MethodGet, "/uploads/"+u.ID, nil, &status)
			var he *storageHTTPError
			exists = !(errors.As(e, &he) && he.Status == 404)
			if !exists && u.Stage == "finalize" {
				startBinaryFinalization(ctx, u)
				continue
			}
		} else {
			var status map[string]any
			e := ctx.WithProject(u.ProjectID).PlatformAPI().CallAppResult("storage", "storage_upload_status", map[string]any{"upload_id": u.ID}, &status)
			exists = e == nil || !strings.Contains(strings.ToLower(e.Error()), "not found")
		}
		if !exists {
			_, _ = ctx.AppDB().ExecContext(c, `UPDATE gig_upload_sessions SET status='expired',last_error='Partial upload expired. Select the original file to restart.',updated_at=CURRENT_TIMESTAMP WHERE upload_id=? AND status='uploading'`, u.ID)
		}
	}
	return nil
}
