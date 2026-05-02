package main

// Resumable chunked upload protocol. Lets browsers stream multi-GB
// videos to storage without buffering the whole body in memory and
// while surviving network drops.
//
// Protocol (S3-shaped, sequential offset):
//   POST   /uploads                 init   → {upload_id, offset:0, expires_at}
//                                     or short-circuit on sha256 hit
//                                     → {file, was_existing:true}
//   GET    /uploads/{id}            status → {offset, declared_size, status}
//   PATCH  /uploads/{id}            chunk  (Upload-Offset header, octet body)
//                                          → {offset}
//   POST   /uploads/{id}/complete   finalize, dedup, insert files row
//                                          → {file, was_existing}
//   DELETE /uploads/{id}            abort, rm session dir
//
// On disk:
//   <data>/uploads/<ulid>/
//     meta.json   {user_id, project_id, filename, content_type, folder,
//                  tags, visibility, declared_size, declared_sha256,
//                  created_at}
//     data        bytes appended via PATCH (stat().Size() = bytes_received)
//     hash.bin    sha256.Hash.MarshalBinary state, refreshed each PATCH
//                 so a sidecar restart can resume without re-hashing.
//
// No new SQL — the filesystem is the session table. The 24h sweeper
// (sweepStaleUploads) deletes session dirs whose mtime is too old.

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// uploadIdleTTL is how long an in-progress upload session stays on
// disk after the last PATCH before the sweeper reclaims it.
const uploadIdleTTL = 24 * time.Hour

// uploadIDPattern restricts session ids to crockford-base32 ulid
// charset so we can plug them into filepath.Join without worrying
// about path traversal.
const uploadIDChars = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// uploadMeta is the per-session JSON sidecar that holds everything
// needed to authorise + finalize an upload. Read on every PATCH and
// at complete; never updated after init (size is the bytes file).
type uploadMeta struct {
	UserID         int64    `json:"user_id"`
	ProjectID      string   `json:"project_id"`
	Filename       string   `json:"filename"`
	ContentType    string   `json:"content_type,omitempty"`
	Folder         string   `json:"folder,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Visibility     string   `json:"visibility,omitempty"`
	DeclaredSize   int64    `json:"declared_size"`
	DeclaredSHA256 string   `json:"declared_sha256,omitempty"`
	CreatedAt      string   `json:"created_at"`
}

// uploadMu serializes per-session writes. Without it, two concurrent
// PATCH calls would race on stat→write→hash and corrupt the file.
// Map keyed by upload_id; entries removed on complete/abort.
var (
	uploadMu sync.Mutex
	uploadLocks = map[string]*sync.Mutex{}
)

func uploadLock(id string) *sync.Mutex {
	uploadMu.Lock()
	defer uploadMu.Unlock()
	if m, ok := uploadLocks[id]; ok {
		return m
	}
	m := &sync.Mutex{}
	uploadLocks[id] = m
	return m
}

func releaseUploadLock(id string) {
	uploadMu.Lock()
	defer uploadMu.Unlock()
	delete(uploadLocks, id)
}

// uploadsDir returns the on-disk root for upload sessions. Lives next
// to the existing blobs dir so a single volume mount covers both.
func uploadsDir(ctx *sdk.AppCtx) string {
	if v := os.Getenv("STORAGE_UPLOADS_DIR"); v != "" {
		return v
	}
	base := filepath.Dir(blobsDir(ctx)) // sibling of storage-blobs
	return filepath.Join(base, "storage-uploads")
}

func uploadSessionDir(ctx *sdk.AppCtx, id string) string {
	return filepath.Join(uploadsDir(ctx), id)
}

// validUploadID ensures the path component the client gave us is one
// component, in the ulid charset, and reasonable length. Anything
// else → 400 before we touch the filesystem.
func validUploadID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune(uploadIDChars, c) {
			return false
		}
	}
	return true
}

// ─── HTTP entry points ───────────────────────────────────────────────

func (a *App) handleUploadsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	a.handleUploadInit(w, r)
}

func (a *App) handleUploadsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/uploads/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if !validUploadID(id) {
		httpErr(w, http.StatusBadRequest, "invalid upload id")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch tail {
	case "":
		switch r.Method {
		case http.MethodGet:
			a.handleUploadStatus(w, r, id)
		case http.MethodPatch:
			a.handleUploadChunk(w, r, id)
		case http.MethodDelete:
			a.handleUploadAbort(w, r, id)
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "complete":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		a.handleUploadComplete(w, r, id)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

// ─── init ────────────────────────────────────────────────────────────

func (a *App) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)

	var body struct {
		Filename       string   `json:"filename"`
		Size           int64    `json:"size"`
		ContentType    string   `json:"content_type"`
		Folder         string   `json:"folder"`
		Tags           []string `json:"tags"`
		Visibility     string   `json:"visibility"`
		SHA256         string   `json:"sha256"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024)).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	body.Filename = normaliseFilename(body.Filename)
	body.Folder = normaliseFolder(body.Folder)
	if body.Filename == "" {
		httpErr(w, http.StatusBadRequest, "filename required")
		return
	}
	if body.Size <= 0 {
		httpErr(w, http.StatusBadRequest, "size must be > 0")
		return
	}
	// Pre-dedup short-circuit: client computed the hash and we already
	// have the bytes for some other row in this project. Skip the
	// upload entirely and return the existing file.
	if body.SHA256 != "" {
		if existing, err := dbFindBySHA(ctx.AppDB(), pid, strings.ToLower(body.SHA256)); err == nil && existing != nil {
			httpJSON(w, map[string]any{
				"file":         existing,
				"was_existing": true,
			})
			return
		}
	}

	id := newUploadID()
	dir := uploadSessionDir(ctx, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		httpErr(w, http.StatusInternalServerError, "mkdir: "+err.Error())
		return
	}

	meta := uploadMeta{
		UserID:         uid,
		ProjectID:      pid,
		Filename:       body.Filename,
		ContentType:    body.ContentType,
		Folder:         body.Folder,
		Tags:           body.Tags,
		Visibility:     visibilityOrDefault(body.Visibility),
		DeclaredSize:   body.Size,
		DeclaredSHA256: strings.ToLower(body.SHA256),
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	mj, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), mj, 0644); err != nil {
		_ = os.RemoveAll(dir)
		httpErr(w, http.StatusInternalServerError, "write meta: "+err.Error())
		return
	}
	// Seed empty data file + initial hash state so PATCHes can be
	// strictly append-only and the sweeper doesn't have to special-
	// case the pre-first-chunk window.
	if err := os.WriteFile(filepath.Join(dir, "data"), nil, 0644); err != nil {
		_ = os.RemoveAll(dir)
		httpErr(w, http.StatusInternalServerError, "write data: "+err.Error())
		return
	}
	h := sha256.New()
	if err := saveHashState(filepath.Join(dir, "hash.bin"), h); err != nil {
		_ = os.RemoveAll(dir)
		httpErr(w, http.StatusInternalServerError, "save hash: "+err.Error())
		return
	}

	httpJSON(w, map[string]any{
		"upload_id":              id,
		"offset":                 0,
		"chunk_size_recommended": 5 * 1024 * 1024,
		"expires_at":             time.Now().Add(uploadIdleTTL).UTC().Format(time.RFC3339),
	})
}

// ─── status ──────────────────────────────────────────────────────────

func (a *App) handleUploadStatus(w http.ResponseWriter, r *http.Request, id string) {
	ctx := globalCtx
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)
	dir := uploadSessionDir(ctx, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		httpErr(w, http.StatusNotFound, "session not found")
		return
	}
	if meta.UserID != uid {
		httpErr(w, http.StatusForbidden, "not your upload")
		return
	}
	st, err := os.Stat(filepath.Join(dir, "data"))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "stat data: "+err.Error())
		return
	}
	httpJSON(w, map[string]any{
		"upload_id":     id,
		"offset":        st.Size(),
		"declared_size": meta.DeclaredSize,
		"status":        "in_progress",
	})
}

// ─── chunk (PATCH) ──────────────────────────────────────────────────

func (a *App) handleUploadChunk(w http.ResponseWriter, r *http.Request, id string) {
	ctx := globalCtx
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)

	mu := uploadLock(id)
	mu.Lock()
	defer mu.Unlock()

	dir := uploadSessionDir(ctx, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		httpErr(w, http.StatusNotFound, "session not found")
		return
	}
	if meta.UserID != uid {
		httpErr(w, http.StatusForbidden, "not your upload")
		return
	}
	dataPath := filepath.Join(dir, "data")
	st, err := os.Stat(dataPath)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "stat data: "+err.Error())
		return
	}
	current := st.Size()

	// Offset enforcement. Mismatch → 409 with the actual offset so the
	// client can reconcile (after a network drop the client may not
	// know how many bytes the server actually accepted).
	got, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "Upload-Offset header required")
		return
	}
	if got != current {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "offset mismatch",
			"offset": current,
		})
		return
	}

	// Restore the in-flight hash, append the chunk, persist updated
	// hash. Write directly with io.Copy — no buffering. Cap by
	// declared_size so a runaway client can't fill the disk past
	// what they declared.
	h := sha256.New()
	if err := loadHashState(filepath.Join(dir, "hash.bin"), h); err != nil {
		httpErr(w, http.StatusInternalServerError, "load hash: "+err.Error())
		return
	}
	remaining := meta.DeclaredSize - current
	if remaining <= 0 {
		httpErr(w, http.StatusConflict, "upload already at declared_size — call /complete")
		return
	}
	f, err := os.OpenFile(dataPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "open data: "+err.Error())
		return
	}
	defer f.Close()
	mw := io.MultiWriter(f, h)
	written, copyErr := io.Copy(mw, io.LimitReader(r.Body, remaining))
	if copyErr != nil && copyErr != io.EOF {
		// Partial write is fine — disk reflects truth, client can resume.
		// Still log + report so the client knows to retry.
		ctx.Logger().Warn("upload chunk partial", "id", id, "err", copyErr, "written", written)
		httpErr(w, http.StatusInternalServerError, "copy: "+copyErr.Error())
		return
	}
	if err := saveHashState(filepath.Join(dir, "hash.bin"), h); err != nil {
		httpErr(w, http.StatusInternalServerError, "save hash: "+err.Error())
		return
	}
	// Touch dir mtime so the sweeper sees activity.
	_ = os.Chtimes(dir, time.Now(), time.Now())

	httpJSON(w, map[string]any{
		"offset": current + written,
	})
}

// ─── complete ────────────────────────────────────────────────────────

func (a *App) handleUploadComplete(w http.ResponseWriter, r *http.Request, id string) {
	ctx := globalCtx
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)

	mu := uploadLock(id)
	mu.Lock()
	defer mu.Unlock()

	dir := uploadSessionDir(ctx, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		httpErr(w, http.StatusNotFound, "session not found")
		return
	}
	if meta.UserID != uid {
		httpErr(w, http.StatusForbidden, "not your upload")
		return
	}
	dataPath := filepath.Join(dir, "data")
	st, err := os.Stat(dataPath)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "stat data: "+err.Error())
		return
	}
	if st.Size() != meta.DeclaredSize {
		httpErr(w, http.StatusBadRequest, fmt.Sprintf(
			"upload incomplete: have %d bytes, declared %d", st.Size(), meta.DeclaredSize))
		return
	}

	// Finalize hash. The complete request body may carry a client-
	// computed sha256 — if it disagrees with what we have, refuse
	// rather than persist drifted bytes.
	h := sha256.New()
	if err := loadHashState(filepath.Join(dir, "hash.bin"), h); err != nil {
		httpErr(w, http.StatusInternalServerError, "load hash: "+err.Error())
		return
	}
	finalSHA := hex.EncodeToString(h.Sum(nil))

	var body struct {
		SHA256 string `json:"sha256"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)
	if body.SHA256 != "" && !strings.EqualFold(body.SHA256, finalSHA) {
		httpErr(w, http.StatusBadRequest, "sha256 mismatch: client="+body.SHA256+" server="+finalSHA)
		return
	}
	if meta.DeclaredSHA256 != "" && !strings.EqualFold(meta.DeclaredSHA256, finalSHA) {
		httpErr(w, http.StatusBadRequest, "declared sha256 mismatch — bytes corrupted")
		return
	}

	// Dedup: same content already in this project? Drop the temp dir
	// and return the existing row instead of inserting a new one.
	if existing, err := dbFindBySHA(ctx.AppDB(), meta.ProjectID, finalSHA); err == nil && existing != nil {
		_ = os.RemoveAll(dir)
		releaseUploadLock(id)
		httpJSON(w, map[string]any{
			"file":         existing,
			"was_existing": true,
		})
		return
	}

	// Move the temp file into the canonical content path and insert
	// the row. Mirrors saveBytes' layout (storage-blobs/<sha-prefix>/<key>)
	// so files_get/serve work without changes.
	key := newUploadID() + extOf(meta.Filename, meta.ContentType) // reuse the ulid generator for storage_key uniqueness
	finalDir := filepath.Join(blobsDir(ctx), finalSHA[:2])
	if err := os.MkdirAll(finalDir, 0755); err != nil {
		httpErr(w, http.StatusInternalServerError, "mkdir blobs: "+err.Error())
		return
	}
	finalPath := filepath.Join(finalDir, key)
	if err := os.Rename(dataPath, finalPath); err != nil {
		// Cross-device rename can fail (Docker bind mounts) — fall
		// back to copy + remove.
		if err := copyAndRemove(dataPath, finalPath); err != nil {
			httpErr(w, http.StatusInternalServerError, "rename: "+err.Error())
			return
		}
	}
	tagsJSON, _ := json.Marshal(meta.Tags)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO files
			(project_id, name, folder, storage_key, content_type, size_bytes,
			 sha256, uploaded_by, source, tags, visibility)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.ProjectID, meta.Filename, meta.Folder, key, meta.ContentType, meta.DeclaredSize,
		finalSHA, callerLabel(), "human", string(tagsJSON), meta.Visibility,
	)
	if err != nil {
		// Best-effort rollback: delete the just-moved blob so we don't
		// leak orphaned bytes.
		_ = os.Remove(finalPath)
		httpErr(w, http.StatusInternalServerError, "insert: "+err.Error())
		return
	}
	insID, _ := res.LastInsertId()
	row, err := dbGetByID(ctx.AppDB(), meta.ProjectID, insID)
	if err != nil || row == nil {
		httpErr(w, http.StatusInternalServerError, "lookup: "+fmt.Sprint(err))
		return
	}
	emitFileEvent(ctx, "file.added", row, false)

	// Tear down the session dir + lock entry.
	_ = os.RemoveAll(dir)
	releaseUploadLock(id)

	httpJSON(w, map[string]any{
		"file":         row,
		"was_existing": false,
	})
}

// ─── abort ───────────────────────────────────────────────────────────

func (a *App) handleUploadAbort(w http.ResponseWriter, r *http.Request, id string) {
	ctx := globalCtx
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)
	dir := uploadSessionDir(ctx, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		httpErr(w, http.StatusNotFound, "session not found")
		return
	}
	if meta.UserID != uid {
		httpErr(w, http.StatusForbidden, "not your upload")
		return
	}
	mu := uploadLock(id)
	mu.Lock()
	_ = os.RemoveAll(dir)
	mu.Unlock()
	releaseUploadLock(id)
	httpJSON(w, map[string]any{"aborted": id})
}

// ─── helpers ─────────────────────────────────────────────────────────

func loadUploadMeta(dir string) (*uploadMeta, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var m uploadMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// saveHashState writes the running sha256.Hash's internal state so a
// crash mid-upload (or a sidecar restart) can resume without
// re-hashing every accepted byte. sha256.Hash satisfies
// encoding.BinaryMarshaler.
func saveHashState(path string, h hash.Hash) error {
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return fmt.Errorf("hash does not support BinaryMarshaler")
	}
	b, err := m.MarshalBinary()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func loadHashState(path string, h hash.Hash) error {
	u, ok := h.(encoding.BinaryUnmarshaler)
	if !ok {
		return fmt.Errorf("hash does not support BinaryUnmarshaler")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return u.UnmarshalBinary(b)
}

// newUploadID generates a 26-char crockford-base32 ulid-shaped id.
// Time-ordered (so directory listings sort chronologically) and
// big enough that brute-forcing one is infeasible.
func newUploadID() string {
	const alphabet = uploadIDChars
	now := uint64(time.Now().UnixMilli())
	var buf [26]byte
	// 10 chars of timestamp (48 bits, ~10889 years range) + 16 of
	// randomness. We don't need cryptographic ulid layout here —
	// just enough entropy to make ids unguessable in the user-id
	// scope. crypto/rand keeps us honest.
	for i := 9; i >= 0; i-- {
		buf[i] = alphabet[now&0x1f]
		now >>= 5
	}
	rand := make([]byte, 10) // 80 random bits → 16 base32 chars
	_, _ = readRandom(rand)
	idx := 10
	for _, b := range rand {
		buf[idx] = alphabet[b&0x1f]
		idx++
		if idx >= 26 {
			break
		}
	}
	for idx < 26 {
		buf[idx] = alphabet[0]
		idx++
	}
	return string(buf[:])
}

// readRandom is split out so tests can stub it deterministically if
// they ever need to.
var readRandom = func(b []byte) (int, error) {
	return cryptorand.Read(b)
}

func copyAndRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// sweepStaleUploads is meant to be called from a goroutine on
// startup + every hour. Walks the uploads dir and removes session
// directories whose mtime is older than uploadIdleTTL.
func sweepStaleUploads(ctx *sdk.AppCtx) {
	root := uploadsDir(ctx)
	entries, err := os.ReadDir(root)
	if err != nil {
		return // dir doesn't exist yet — nothing to sweep
	}
	cutoff := time.Now().Add(-uploadIdleTTL)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !validUploadID(e.Name()) {
			continue // ignore stray files / unrelated dirs
		}
		path := filepath.Join(root, e.Name())
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		if st.ModTime().Before(cutoff) {
			ctx.Logger().Info("upload sweeper removing stale session",
				"id", e.Name(), "age", time.Since(st.ModTime()).String())
			_ = os.RemoveAll(path)
		}
	}
}
