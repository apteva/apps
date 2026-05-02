package main

// Resumable parallel chunked upload protocol. S3-shaped: each part
// is an independent PUT under a part_number, parts can land in any
// order, complete() concatenates them in sorted order and hashes
// in a single pass.
//
// Protocol:
//   POST   /uploads                init   → {upload_id, part_size, max_parallel}
//                                  or short-circuit on sha256 hit
//                                  → {file, was_existing:true}
//   GET    /uploads/{id}           status → {parts:[{n,size}], status}
//   PUT    /uploads/{id}/parts/{N} upload one part (binary body).
//                                  Independent — N-way parallel.
//                                  → {part_number, size}
//   POST   /uploads/{id}/complete  finalize: validate contiguous,
//                                  stream-concat+hash, dedup,
//                                  insert files row
//                                  → {file, was_existing}
//   DELETE /uploads/{id}           abort, rm session dir
//
// On disk:
//   <data>/uploads/<ulid>/
//     meta.json   {user_id, project_id, filename, content_type, folder,
//                  tags, visibility, declared_size, declared_sha256,
//                  created_at}
//     parts/
//       000001    bytes for part 1
//       000002    bytes for part 2
//       ...
//
// No new SQL — the filesystem is the session. Per-part files mean
// no shared mutex on PUT, so the network is the only contention.

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	uploadIdleTTL  = 24 * time.Hour
	uploadIDChars  = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	maxPartNumber  = 10000              // S3-compatible upper bound
	maxPartSize    = 100 * 1024 * 1024  // sanity cap per part
	defaultPartSize = 5 * 1024 * 1024
	defaultParallel = 4
)

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

// completeMu serializes the complete() critical section per session
// (concat + hash + insert + cleanup must run once even if the
// client retries complete twice). PUT /parts/N has no shared lock —
// each part writes to its own file.
var (
	completeMu    sync.Mutex
	completeLocks = map[string]*sync.Mutex{}
)

func sessionLock(id string) *sync.Mutex {
	completeMu.Lock()
	defer completeMu.Unlock()
	if m, ok := completeLocks[id]; ok {
		return m
	}
	m := &sync.Mutex{}
	completeLocks[id] = m
	return m
}

func releaseSessionLock(id string) {
	completeMu.Lock()
	defer completeMu.Unlock()
	delete(completeLocks, id)
}

func uploadsDir(ctx *sdk.AppCtx) string {
	if v := os.Getenv("STORAGE_UPLOADS_DIR"); v != "" {
		return v
	}
	base := filepath.Dir(blobsDir(ctx))
	return filepath.Join(base, "storage-uploads")
}

func uploadSessionDir(ctx *sdk.AppCtx, id string) string {
	return filepath.Join(uploadsDir(ctx), id)
}

func partsDir(ctx *sdk.AppCtx, id string) string {
	return filepath.Join(uploadSessionDir(ctx, id), "parts")
}

func partPath(ctx *sdk.AppCtx, id string, n int) string {
	return filepath.Join(partsDir(ctx, id), fmt.Sprintf("%06d", n))
}

// validUploadID — single component, ulid charset, reasonable length.
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

// ─── HTTP routing ────────────────────────────────────────────────────

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
	parts := strings.SplitN(rest, "/", 3)
	id := parts[0]
	if !validUploadID(id) {
		httpErr(w, http.StatusBadRequest, "invalid upload id")
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			a.handleUploadStatus(w, r, id)
		case http.MethodDelete:
			a.handleUploadAbort(w, r, id)
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	switch parts[1] {
	case "complete":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		a.handleUploadComplete(w, r, id)
	case "parts":
		if len(parts) != 3 || parts[2] == "" {
			httpErr(w, http.StatusBadRequest, "part number required")
			return
		}
		if r.Method != http.MethodPut {
			httpErr(w, http.StatusMethodNotAllowed, "PUT only")
			return
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 1 || n > maxPartNumber {
			httpErr(w, http.StatusBadRequest, "invalid part number")
			return
		}
		a.handleUploadPart(w, r, id, n)
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

	// Pre-dedup short-circuit.
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
	if err := os.MkdirAll(filepath.Join(dir, "parts"), 0755); err != nil {
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

	httpJSON(w, map[string]any{
		"upload_id":    id,
		"part_size":    defaultPartSize,
		"max_parallel": defaultParallel,
		"max_parts":    maxPartNumber,
		"expires_at":   time.Now().Add(uploadIdleTTL).UTC().Format(time.RFC3339),
	})
}

// ─── status ──────────────────────────────────────────────────────────

type partInfo struct {
	N    int   `json:"n"`
	Size int64 `json:"size"`
}

func listParts(ctx *sdk.AppCtx, id string) ([]partInfo, error) {
	entries, err := os.ReadDir(partsDir(ctx, id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]partInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(e.Name())
		if err != nil || n < 1 || n > maxPartNumber {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, partInfo{N: n, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out, nil
}

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
	parts, err := listParts(ctx, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "list parts: "+err.Error())
		return
	}
	var bytesUploaded int64
	for _, p := range parts {
		bytesUploaded += p.Size
	}
	httpJSON(w, map[string]any{
		"upload_id":      id,
		"parts":          parts,
		"bytes_uploaded": bytesUploaded,
		"declared_size":  meta.DeclaredSize,
		"status":         "in_progress",
	})
}

// ─── PUT one part ────────────────────────────────────────────────────

func (a *App) handleUploadPart(w http.ResponseWriter, r *http.Request, id string, n int) {
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

	// Stream straight to a temp sibling; rename atomically. The
	// rename is what makes a re-upload of the same part_number safe
	// — the previous bytes are replaced in one syscall, no torn
	// half-state observable by complete().
	pp := partPath(ctx, id, n)
	tmp := pp + ".tmp." + randHex(8)
	f, err := os.Create(tmp)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "create part: "+err.Error())
		return
	}
	written, copyErr := io.Copy(f, io.LimitReader(r.Body, maxPartSize+1))
	cerr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		httpErr(w, http.StatusInternalServerError, "copy: "+copyErr.Error())
		return
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		httpErr(w, http.StatusInternalServerError, "close: "+cerr.Error())
		return
	}
	if written > maxPartSize {
		_ = os.Remove(tmp)
		httpErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("part exceeds %d bytes", maxPartSize))
		return
	}
	if written == 0 {
		_ = os.Remove(tmp)
		httpErr(w, http.StatusBadRequest, "empty part")
		return
	}
	if err := os.Rename(tmp, pp); err != nil {
		_ = os.Remove(tmp)
		httpErr(w, http.StatusInternalServerError, "rename: "+err.Error())
		return
	}
	// Touch dir mtime so the sweeper sees activity.
	_ = os.Chtimes(dir, time.Now(), time.Now())

	httpJSON(w, map[string]any{
		"part_number": n,
		"size":        written,
	})
}

// ─── complete ────────────────────────────────────────────────────────

func (a *App) handleUploadComplete(w http.ResponseWriter, r *http.Request, id string) {
	ctx := globalCtx
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)

	mu := sessionLock(id)
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
	parts, err := listParts(ctx, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "list parts: "+err.Error())
		return
	}
	if len(parts) == 0 {
		httpErr(w, http.StatusBadRequest, "no parts uploaded")
		return
	}
	// Validate contiguous 1..N — any gap means a part was lost.
	var totalSize int64
	for i, p := range parts {
		if p.N != i+1 {
			httpErr(w, http.StatusBadRequest,
				fmt.Sprintf("missing part %d (have %d parts, last is %d)", i+1, len(parts), p.N))
			return
		}
		totalSize += p.Size
	}
	if totalSize != meta.DeclaredSize {
		httpErr(w, http.StatusBadRequest,
			fmt.Sprintf("size mismatch: parts total %d, declared %d", totalSize, meta.DeclaredSize))
		return
	}

	var body struct {
		SHA256 string `json:"sha256"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)

	// Stream-concat parts into the canonical content path, hashing
	// as we go. We don't know the SHA256 yet, so write to a temp
	// file and rename after we know the hash + dedup answer.
	tmpKey := newUploadID() + extOf(meta.Filename, meta.ContentType)
	tmpDir := blobsDir(ctx)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		httpErr(w, http.StatusInternalServerError, "mkdir blobs: "+err.Error())
		return
	}
	tmpPath := filepath.Join(tmpDir, tmpKey+".tmp")
	out, err := os.Create(tmpPath)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "create blob: "+err.Error())
		return
	}
	h := sha256.New()
	mw := io.MultiWriter(out, h)
	for _, p := range parts {
		f, err := os.Open(partPath(ctx, id, p.N))
		if err != nil {
			out.Close()
			_ = os.Remove(tmpPath)
			httpErr(w, http.StatusInternalServerError, "open part "+strconv.Itoa(p.N)+": "+err.Error())
			return
		}
		if _, err := io.Copy(mw, f); err != nil {
			f.Close()
			out.Close()
			_ = os.Remove(tmpPath)
			httpErr(w, http.StatusInternalServerError, "concat: "+err.Error())
			return
		}
		f.Close()
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		httpErr(w, http.StatusInternalServerError, "close blob: "+err.Error())
		return
	}
	finalSHA := hex.EncodeToString(h.Sum(nil))

	// Verify any client-supplied or pre-declared hash before we
	// take any action that could observe drift.
	if body.SHA256 != "" && !strings.EqualFold(body.SHA256, finalSHA) {
		_ = os.Remove(tmpPath)
		httpErr(w, http.StatusBadRequest, "sha256 mismatch: client="+body.SHA256+" server="+finalSHA)
		return
	}
	if meta.DeclaredSHA256 != "" && !strings.EqualFold(meta.DeclaredSHA256, finalSHA) {
		_ = os.Remove(tmpPath)
		httpErr(w, http.StatusBadRequest, "declared sha256 mismatch — bytes corrupted")
		return
	}

	// Dedup against existing files. If we already have these bytes,
	// drop the freshly-concatenated blob and return the old row.
	if existing, err := dbFindBySHA(ctx.AppDB(), meta.ProjectID, finalSHA); err == nil && existing != nil {
		_ = os.Remove(tmpPath)
		_ = os.RemoveAll(dir)
		releaseSessionLock(id)
		httpJSON(w, map[string]any{"file": existing, "was_existing": true})
		return
	}

	// Move the temp blob into the canonical sha-prefixed location.
	finalDir := filepath.Join(blobsDir(ctx), finalSHA[:2])
	if err := os.MkdirAll(finalDir, 0755); err != nil {
		_ = os.Remove(tmpPath)
		httpErr(w, http.StatusInternalServerError, "mkdir final: "+err.Error())
		return
	}
	finalPath := filepath.Join(finalDir, tmpKey)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		if cerr := copyAndRemove(tmpPath, finalPath); cerr != nil {
			_ = os.Remove(tmpPath)
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
		meta.ProjectID, meta.Filename, meta.Folder, tmpKey, meta.ContentType, meta.DeclaredSize,
		finalSHA, callerLabel(), "human", string(tagsJSON), meta.Visibility,
	)
	if err != nil {
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

	_ = os.RemoveAll(dir)
	releaseSessionLock(id)

	httpJSON(w, map[string]any{"file": row, "was_existing": false})
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
	mu := sessionLock(id)
	mu.Lock()
	_ = os.RemoveAll(dir)
	mu.Unlock()
	releaseSessionLock(id)
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

func newUploadID() string {
	const alphabet = uploadIDChars
	now := uint64(time.Now().UnixMilli())
	var buf [26]byte
	for i := 9; i >= 0; i-- {
		buf[i] = alphabet[now&0x1f]
		now >>= 5
	}
	rand := make([]byte, 10)
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

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = readRandom(b)
	return hex.EncodeToString(b)
}

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

func sweepStaleUploads(ctx *sdk.AppCtx) {
	root := uploadsDir(ctx)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-uploadIdleTTL)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !validUploadID(e.Name()) {
			continue
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
