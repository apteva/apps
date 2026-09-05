package main

// Direct presigned-upload protocol (v0.6+, S3-compatible backends only).
//
// Lets clients PUT bytes straight to the storage backend without
// proxying through this container. Saves latency + container CPU
// for large objects. Three calls:
//
//  1. POST /files/init        → mint presigned PUT URL + upload_id
//  2. PUT  <upload_url>       (client → S3 directly)
//  3. POST /files/{id}/finalize  → verify, insert files row
//
// On disk-backed installs the init endpoint returns 501 — clients
// fall back to POST /files (the bytes-through-storage path that
// works on any backend).
//
// Finalization hashes a bounded snapshot and writes a new object key. Only
// the temporary key is client-writable, and its cleanup remains scheduled
// until the signed PUT expires. Publication and completion are atomic.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

// presignTTL is how long the upload URL stays valid. Should be long
// enough for slow uploaders (mobile / metered) but short enough to
// limit abuse if the URL leaks. 1h is the comfortable middle.
const presignTTL = 1 * time.Hour

// ─── routing ──────────────────────────────────────────────────────

// dispatchDirectUpload is invoked from handleFilesCollection /
// handleFilesItem when the path matches the init / finalize routes.
// Returns true when it handled the request (so the caller stops).
func (a *App) dispatchDirectUpload(w http.ResponseWriter, r *http.Request, tail string) bool {
	switch {
	case r.Method == http.MethodPost && tail == "init":
		a.handleDirectInit(w, r)
		return true
	case r.Method == http.MethodPost && strings.HasSuffix(tail, "/finalize"):
		uploadID := strings.TrimSuffix(tail, "/finalize")
		if !validUploadID(uploadID) {
			httpErr(w, http.StatusBadRequest, "invalid upload_id")
			return true
		}
		a.handleDirectFinalize(w, r, uploadID)
		return true
	}
	return false
}

// ─── init ─────────────────────────────────────────────────────────

func (a *App) handleDirectInit(w http.ResponseWriter, r *http.Request) {
	ctx := globalCtx
	be := backend()
	if be.Kind() != "s3" {
		// The disk backend can't mint presigned URLs. Tell the client
		// to use the proxy path; this is a 501 not a 400 because the
		// endpoint *exists* — it's just unavailable on this install.
		httpErr(w, http.StatusNotImplemented,
			"backend=disk: presigned uploads not supported; POST bytes to /files instead")
		return
	}

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var body struct {
		Name        string   `json:"name"`
		Folder      string   `json:"folder"`
		ContentType string   `json:"content_type"`
		SizeBytes   int64    `json:"size_bytes"`
		SHA256      string   `json:"sha256"`
		Visibility  string   `json:"visibility"`
		Tags        []string `json:"tags"`
		Source      string   `json:"source"`
	}
	if err := decodeJSON(r, &body, 32*1024, false); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	body.Name, err = validateFilename(body.Name)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	body.Folder, err = validateFolder(body.Folder)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err = validateVisibility(body.Visibility); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	body.SHA256 = strings.ToLower(strings.TrimSpace(body.SHA256))

	if body.Name == "" {
		httpErr(w, http.StatusBadRequest, "name required")
		return
	}
	if body.SizeBytes <= 0 {
		httpErr(w, http.StatusBadRequest, "size_bytes must be > 0")
		return
	}
	if maxBytes := maxUploadBytes(ctx); body.SizeBytes > maxBytes {
		httpErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("upload exceeds max_upload_size_mb (%d bytes > %d)", body.SizeBytes, maxBytes))
		return
	}
	if body.SHA256 == "" {
		// Client trust is the whole point of the direct path; if we
		// can't dedup or verify on finalize, the protocol is moot.
		httpErr(w, http.StatusBadRequest, "sha256 required (client-computed); use POST /files if you can't compute it")
		return
	}
	if !looksLikeSHA256Hex(body.SHA256) {
		httpErr(w, http.StatusBadRequest, "sha256 must be 64-char lowercase hex")
		return
	}

	visibility := visibilityOrDefault(body.Visibility)
	if visibility == "" {
		visibility = configuredDefaultVisibility(ctx)
	}

	// Pre-dedup only an exact destination match. The same bytes under a
	// new filename/folder are a distinct user-visible file and must be
	// uploaded/materialised there.
	if existing, err := dbFindExact(ctx.AppDB(), pid, body.SHA256, body.Folder, body.Name); err == nil && existing != nil {
		if _, _, err := compatibleExisting(existing, uploadInput{ContentType: safeResponseContentType(body.ContentType), Visibility: visibility, Tags: cleanTags(body.Tags)}); err != nil {
			httpErr(w, 409, err.Error())
			return
		}
		httpJSON(w, map[string]any{
			"file":         existing,
			"was_existing": true,
			"mode":         "deduplicated",
		})
		return
	}

	uploadID := newDirectUploadID()
	expiresAt := time.Now().Add(presignTTL).Unix()
	if err = reserveUpload(ctx, uploadID, pid, body.SizeBytes, expiresAt); err != nil {
		httpErr(w, 429, err.Error())
		return
	}
	reserved := true
	defer func() {
		if reserved {
			releaseUploadReservation(ctx, uploadID)
		}
	}()
	storageKey := uuid.NewString() + extOf(body.Name, body.ContentType)
	objKey := objectKey(body.SHA256, storageKey)

	effectiveContentType := ifEmpty(body.ContentType, "application/octet-stream")
	uploadHeaders := map[string]string{"Content-Type": effectiveContentType}
	var uploadURL string
	if constrained, ok := be.(constrainedPutPresigner); ok {
		uploadURL, uploadHeaders, err = constrained.PresignPutConstrained(
			r.Context(), objKey, effectiveContentType, body.SizeBytes, presignTTL)
	} else {
		uploadURL, err = be.PresignPut(r.Context(), objKey, body.ContentType, presignTTL)
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "presign: "+err.Error())
		return
	}

	tagsJSON, _ := json.Marshal(body.Tags)
	if _, err := ctx.AppDB().Exec(`
		INSERT INTO pending_uploads
			(upload_id, project_id, storage_key, name, folder, content_type,
			 size_bytes, declared_sha256, visibility, tags, source, requested_by, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uploadID, pid, storageKey, body.Name, body.Folder, body.ContentType,
		body.SizeBytes, body.SHA256, visibility, string(tagsJSON), body.Source, requestActor(r), expiresAt,
	); err != nil {
		httpErr(w, http.StatusInternalServerError, "persist session: "+err.Error())
		return
	}

	if err = queueBlobCleanup(ctx, objKey, time.Unix(expiresAt+60, 0)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	reserved = false
	httpJSON(w, map[string]any{
		"upload_id":  uploadID,
		"upload_url": uploadURL,
		"method":     "PUT",
		"headers":    uploadHeaders,
		"expires_at": expiresAt,
		"mode":       "presigned",
	})
}

// ─── finalize ─────────────────────────────────────────────────────

func (a *App) handleDirectFinalize(w http.ResponseWriter, r *http.Request, uploadID string) {
	app := globalCtx
	if backend().Kind() != "s3" {
		httpErr(w, 501, "backend=disk: presigned uploads not supported")
		return
	}
	var body struct {
		SHA256 string `json:"sha256"`
	}
	if err := decodeJSON(r, &body, 1024, true); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	mu := sessionLock(uploadID)
	mu.Lock()
	defer func() { mu.Unlock(); releaseSessionLock(uploadID) }()
	if f, existed, e := completedUpload(app, uploadID, pid); e != nil {
		httpErr(w, 500, e.Error())
		return
	} else if f != nil {
		meta, e := loadSessionOrCompletion(app, uploadID, pid)
		if e != nil || authorizeHTTPSession(r, meta) != nil {
			httpErr(w, 403, "not your upload")
			return
		}
		if body.SHA256 != "" && !strings.EqualFold(body.SHA256, f.SHA256) {
			httpErr(w, 400, "sha256 mismatch")
			return
		}
		httpJSON(w, map[string]any{"file": f, "was_existing": existed})
		return
	}
	var sk, name, folder, ct, vis, tags, source, sha, owner string
	var size, expires int64
	err = app.AppDB().QueryRow(`SELECT storage_key,name,folder,COALESCE(content_type,''),size_bytes,declared_sha256,COALESCE(visibility,''),COALESCE(tags,'[]'),COALESCE(source,''),expires_at,COALESCE(requested_by,'') FROM pending_uploads WHERE upload_id=? AND project_id=?`, uploadID, pid).Scan(&sk, &name, &folder, &ct, &size, &sha, &vis, &tags, &source, &expires, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		httpErr(w, 404, "upload session not found")
		return
	}
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if strings.HasPrefix(owner, "human:") && owner != requestActor(r) {
		httpErr(w, 403, "not your upload")
		return
	}
	if time.Now().Unix() > expires {
		httpErr(w, 410, "upload session expired")
		return
	}
	if body.SHA256 != "" && !strings.EqualFold(body.SHA256, sha) {
		httpErr(w, 400, "sha256 mismatch with declared at init")
		return
	}
	release, err := acquireTransfer(r.Context())
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	defer release()
	key := objectKey(sha, sk)
	read, err := backend().OpenObject(r.Context(), key, ObjectReadOptions{})
	if err != nil {
		httpErr(w, 400, "no object at presigned URL: "+err.Error())
		return
	}
	defer read.Body.Close()
	var tagList []string
	if err = json.Unmarshal([]byte(tags), &tagList); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)
	in := uploadInput{Name: name, Folder: folder, ContentType: ct, Visibility: vis, Tags: tagList, Source: ifEmpty(source, "presigned"), ExpectedSHA: sha, ExpectedSize: size, UploadID: uploadID, UserID: uid}
	// Spool and hash this exact GET snapshot, then publish under a NEW key.
	// Reusing a still-valid PUT can only alter the temporary object.
	c := context.WithValue(r.Context(), actorContextKey{}, requestActor(r))
	row, existed, err := saveStream(c, app, pid, in, read.Body)
	if err != nil {
		cleanupBlobNow(app, key)
		_ = queueBlobCleanup(app, key, time.Unix(expires+60, 0))
		httpErr(w, 400, err.Error())
		return
	}
	// Keep cleanup scheduled until the PUT expires, even if the client recreates it.
	if err = queueBlobCleanup(app, key, time.Unix(expires+60, 0)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	retireSessionLock(uploadID)
	emitFileEvent(app, "file.added", row, existed)
	httpJSON(w, map[string]any{"file": row, "was_existing": existed})
}

// ─── helpers ───────────────────────────────────────────────────────

// newDirectUploadID returns a random ULID-shaped identifier. Reuses
// the existing chunked-upload charset so validUploadID accepts both.
func newDirectUploadID() string {
	const n = 26
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic — fall back to uuid so
		// at least we don't hand out colliding IDs.
		return strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	out := make([]byte, n)
	for i := range b {
		out[i] = uploadIDChars[int(b[i])%len(uploadIDChars)]
	}
	return string(out)
}

func looksLikeSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func ifEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// ─── stale session sweeper ────────────────────────────────────────

// sweepStalePendingUploads runs alongside sweepStaleUploads. It removes both
// the pending row and any object uploaded through the expired presigned URL;
// otherwise abandoned direct uploads become permanent, unmetered bucket
// usage. Failed deletes retain their row so the next sweep retries.
func sweepStalePendingUploads(ctx *sdk.AppCtx) {
	if ctx == nil || ctx.AppDB() == nil {
		return
	}
	now := time.Now().Unix()
	rows, err := ctx.AppDB().Query(
		`SELECT upload_id, storage_key, declared_sha256
		 FROM pending_uploads WHERE expires_at < ?`, now)
	if err != nil {
		ctx.Logger().Warn("pending uploads sweep failed", "err", err)
		return
	}
	type staleUpload struct{ id, storageKey, sha256 string }
	var stale []staleUpload
	for rows.Next() {
		var item staleUpload
		if err := rows.Scan(&item.id, &item.storageKey, &item.sha256); err != nil {
			rows.Close()
			ctx.Logger().Warn("pending uploads sweep scan failed", "err", err)
			return
		}
		stale = append(stale, item)
	}
	if err := rows.Close(); err != nil {
		ctx.Logger().Warn("pending uploads sweep close failed", "err", err)
		return
	}
	removed := 0
	for _, item := range stale {
		key := objectKey(item.sha256, item.storageKey)
		if !cleanupBlob(ctx, key) {
			err := errors.New("blob cleanup pending")
			ctx.Logger().Warn("pending upload object cleanup failed", "upload_id", item.id, "key", key, "err", err)
			continue
		}
		releaseUploadReservation(ctx, item.id)
		res, err := ctx.AppDB().Exec(
			`DELETE FROM pending_uploads WHERE upload_id = ? AND expires_at < ?`, item.id, now)
		if err != nil {
			ctx.Logger().Warn("pending upload row cleanup failed", "upload_id", item.id, "err", err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			removed++
		}
	}
	if removed > 0 {
		ctx.Logger().Info("pending uploads swept", "rows", removed)
	}
}
