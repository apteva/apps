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
// Trade-off: client-supplied sha256 is trusted at finalize time. We
// can't reliably re-derive it server-side because S3's ETag is MD5
// for single-PUT and a hash-of-hashes for multipart — neither is
// SHA256, and re-fetching the bytes to hash defeats the purpose.
// We DO verify size_bytes via Stat so a corrupted-but-shorter
// upload won't be accepted.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024)).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	body.Name = normaliseFilename(body.Name)
	body.Folder = normaliseFolder(body.Folder)
	body.SHA256 = strings.ToLower(strings.TrimSpace(body.SHA256))

	if body.Name == "" {
		httpErr(w, http.StatusBadRequest, "name required")
		return
	}
	if body.SizeBytes <= 0 {
		httpErr(w, http.StatusBadRequest, "size_bytes must be > 0")
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

	// Pre-dedup: if we already have these bytes in this project,
	// short-circuit. Saves the client an upload + the bucket a write.
	if existing, err := dbFindBySHA(ctx.AppDB(), pid, body.SHA256); err == nil && existing != nil {
		httpJSON(w, map[string]any{
			"file":         existing,
			"was_existing": true,
			"mode":         "deduplicated",
		})
		return
	}

	storageKey := uuid.NewString() + extOf(body.Name, body.ContentType)
	objKey := objectKey(body.SHA256, storageKey)

	uploadURL, err := be.PresignPut(r.Context(), objKey, body.ContentType, presignTTL)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "presign: "+err.Error())
		return
	}

	uploadID := newDirectUploadID()
	expiresAt := time.Now().Add(presignTTL).Unix()
	tagsJSON, _ := json.Marshal(body.Tags)
	if _, err := ctx.AppDB().Exec(`
		INSERT INTO pending_uploads
			(upload_id, project_id, storage_key, name, folder, content_type,
			 size_bytes, declared_sha256, visibility, tags, source, requested_by, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uploadID, pid, storageKey, body.Name, body.Folder, body.ContentType,
		body.SizeBytes, body.SHA256, visibility, string(tagsJSON), body.Source, callerLabel(), expiresAt,
	); err != nil {
		httpErr(w, http.StatusInternalServerError, "persist session: "+err.Error())
		return
	}

	httpJSON(w, map[string]any{
		"upload_id":  uploadID,
		"upload_url": uploadURL,
		"method":     "PUT",
		"headers": map[string]any{
			// Soft hint — clients should set Content-Type so the
			// resulting object is browser-friendly. Backends that
			// require this in the signature (B2 in some modes) will
			// reject mismatches.
			"Content-Type": ifEmpty(body.ContentType, "application/octet-stream"),
		},
		"expires_at": expiresAt,
		"mode":       "presigned",
	})
}

// ─── finalize ─────────────────────────────────────────────────────

func (a *App) handleDirectFinalize(w http.ResponseWriter, r *http.Request, uploadID string) {
	ctx := globalCtx
	be := backend()
	if be.Kind() != "s3" {
		httpErr(w, http.StatusNotImplemented, "backend=disk: presigned uploads not supported")
		return
	}

	var body struct {
		SHA256 string `json:"sha256"` // optional — must match init's declared if set
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var (
		sk, name, folder, ct, vis, tags, source, declaredSHA string
		size                                                 int64
		expiresAt                                            int64
	)
	err = ctx.AppDB().QueryRow(`
		SELECT storage_key, name, folder, COALESCE(content_type,''),
		       size_bytes, declared_sha256, COALESCE(visibility,''),
		       COALESCE(tags,'[]'), COALESCE(source,''), expires_at
		  FROM pending_uploads
		 WHERE upload_id = ? AND project_id = ?`,
		uploadID, pid,
	).Scan(&sk, &name, &folder, &ct, &size, &declaredSHA, &vis, &tags, &source, &expiresAt)
	if err == sql.ErrNoRows {
		httpErr(w, http.StatusNotFound, "upload session not found (already finalized or expired?)")
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "lookup: "+err.Error())
		return
	}
	if time.Now().Unix() > expiresAt {
		httpErr(w, http.StatusGone, "upload session expired")
		return
	}

	// Optional client-side hash check; reject mismatch loud.
	if body.SHA256 != "" && !strings.EqualFold(body.SHA256, declaredSHA) {
		httpErr(w, http.StatusBadRequest, "sha256 mismatch with declared at init")
		return
	}

	objKey := objectKey(declaredSHA, sk)

	// Verify the object actually arrived + is the declared size.
	gotSize, err := be.Stat(r.Context(), objKey)
	if errors.Is(err, ErrNotFound) {
		httpErr(w, http.StatusBadRequest, "no object at presigned URL — did the PUT succeed?")
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "stat object: "+err.Error())
		return
	}
	if gotSize != size {
		// Best-effort cleanup so a half-broken upload doesn't linger
		// in the bucket.
		_ = be.Delete(r.Context(), objKey)
		httpErr(w, http.StatusBadRequest,
			fmt.Sprintf("size mismatch: declared %d, actual %d", size, gotSize))
		return
	}

	// Defensive dedup re-check — between init and finalize, another
	// upload of the same bytes might have raced ahead and already
	// inserted a row. Use the existing one and tombstone the bucket
	// object we just verified.
	if existing, err := dbFindBySHA(ctx.AppDB(), pid, declaredSHA); err == nil && existing != nil {
		_ = be.Delete(r.Context(), objKey)
		_, _ = ctx.AppDB().Exec(`DELETE FROM pending_uploads WHERE upload_id = ?`, uploadID)
		httpJSON(w, map[string]any{
			"file":         existing,
			"was_existing": true,
		})
		return
	}

	// Insert the final files row + delete the session in one go.
	res, err := ctx.AppDB().Exec(`
		INSERT INTO files
			(project_id, name, folder, storage_key, content_type, size_bytes,
			 sha256, uploaded_by, source, tags, visibility)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, name, folder, sk, ct, size, declaredSHA, callerLabel(),
		ifEmpty(source, "presigned"), tags, vis,
	)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "insert: "+err.Error())
		return
	}
	insID, _ := res.LastInsertId()
	if _, err := ctx.AppDB().Exec(`DELETE FROM pending_uploads WHERE upload_id = ?`, uploadID); err != nil {
		ctx.Logger().Warn("pending session cleanup failed", "upload_id", uploadID, "err", err)
	}
	row, err := dbGetByID(ctx.AppDB(), pid, insID)
	if err != nil || row == nil {
		httpErr(w, http.StatusInternalServerError, "lookup new row")
		return
	}
	emitFileEvent(ctx, "file.added", row, false)
	httpJSON(w, map[string]any{
		"file":         row,
		"was_existing": false,
	})
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

// sweepStalePendingUploads runs hourly alongside sweepStaleUploads.
// Removes pending_uploads rows whose presigned URL is past expiry —
// the bucket-side object (if any) is left as a tombstone for a
// future bucket-lifecycle rule to reap. Doing the cleanup ourselves
// would mean N Stat+Delete round-trips per sweep; bucket lifecycle
// is the right tool for that job.
func sweepStalePendingUploads(ctx *sdk.AppCtx) {
	if ctx == nil || ctx.AppDB() == nil {
		return
	}
	now := time.Now().Unix()
	res, err := ctx.AppDB().Exec(`DELETE FROM pending_uploads WHERE expires_at < ?`, now)
	if err != nil {
		ctx.Logger().Warn("pending uploads sweep failed", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		ctx.Logger().Info("pending uploads swept", "rows", n)
	}
}
