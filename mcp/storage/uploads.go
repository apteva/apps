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
// Agent MCP wrappers expose the same session model via
// storage_upload_init / storage_upload_part / storage_upload_status /
// storage_upload_complete. MCP parts are intentionally capped smaller
// than HTTP parts because base64-in-JSON is transport-expensive.
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
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	// defaultUploadIdleTTL — how long an idle upload session sits
	// before the sweeper reclaims it. Was 24h; bumped down to 6h
	// because cancel-spam during dev fills disks faster than the
	// old TTL drained. Operators can override via the
	// upload_idle_ttl_hours config; 0 = never expire (anti-pattern,
	// flagged in the config description).
	defaultUploadIdleTTL = 6 * time.Hour
	// defaultSweepInterval — how often the sweeper looks for stale
	// sessions. Was 1h; 15min keeps reclaim within an hour even
	// when idle TTL drops to 1h. Operators override via
	// upload_sweep_interval_minutes.
	defaultSweepInterval = 15 * time.Minute
	uploadIDChars        = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	maxPartNumber        = 10000             // S3-compatible upper bound
	maxPartSize          = 100 * 1024 * 1024 // sanity cap per part
	defaultPartSize      = 5 * 1024 * 1024
	defaultParallel      = 4
)

// configuredUploadIdleTTL reads upload_idle_ttl_hours from install
// config, falling back to defaultUploadIdleTTL. 0 in config = no
// auto-cleanup ever — surfaced in the config description as an
// anti-pattern but technically supported.
func configuredUploadIdleTTL(ctx *sdk.AppCtx) time.Duration {
	if ctx == nil {
		return defaultUploadIdleTTL
	}
	raw := strings.TrimSpace(ctx.Config().Get("upload_idle_ttl_hours"))
	if raw == "" {
		return defaultUploadIdleTTL
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return defaultUploadIdleTTL
	}
	if n == 0 {
		// Effectively disable. 100y is "long enough to not fire."
		return 100 * 365 * 24 * time.Hour
	}
	return time.Duration(min(n, 168)) * time.Hour
}

// configuredSweepInterval reads upload_sweep_interval_minutes,
// clamped to [1m, 24h] for sanity. Anything outside that range
// likely indicates a typo.
func configuredSweepInterval(ctx *sdk.AppCtx) time.Duration {
	if ctx == nil {
		return defaultSweepInterval
	}
	raw := strings.TrimSpace(ctx.Config().Get("upload_sweep_interval_minutes"))
	if raw == "" {
		return defaultSweepInterval
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultSweepInterval
	}
	d := time.Duration(min(max(n, 1), 1440)) * time.Minute
	if d < time.Minute {
		return time.Minute
	}
	if d > 24*time.Hour {
		return 24 * time.Hour
	}
	return d
}

type uploadMeta struct {
	UserID         int64    `json:"user_id"`
	ProjectID      string   `json:"project_id"`
	Filename       string   `json:"filename"`
	ContentType    string   `json:"content_type,omitempty"`
	Folder         string   `json:"folder,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Visibility     string   `json:"visibility,omitempty"`
	Source         string   `json:"source,omitempty"`
	DeclaredSize   int64    `json:"declared_size"`
	DeclaredSHA256 string   `json:"declared_sha256,omitempty"`
	CreatedAt      string   `json:"created_at"`
}

// completeMu serializes the complete() critical section per session
// (concat + hash + insert + cleanup must run once even if the
// client retries complete twice). PUT /parts/N has no shared lock —
// each part writes to its own file.
type uploadLock struct {
	sync.RWMutex
	budget  sync.Mutex
	sizes   map[int]int64
	writing map[int]bool
	total   int64
	refs    int
	retired bool
}

var completeMu sync.Mutex
var completeLocks = map[string]*uploadLock{}

func sessionLock(id string) *uploadLock {
	completeMu.Lock()
	defer completeMu.Unlock()
	if m := completeLocks[id]; m != nil {
		m.refs++
		return m
	}
	// Idle cache entries may be evicted; active locks are never replaced.
	if len(completeLocks) >= 128 {
		for k, v := range completeLocks {
			if v.refs == 0 {
				delete(completeLocks, k)
			}
		}
	}
	m := &uploadLock{refs: 1}
	completeLocks[id] = m
	return m
}
func releaseSessionLock(id string) {
	completeMu.Lock()
	defer completeMu.Unlock()
	if m := completeLocks[id]; m != nil {
		m.refs--
		if m.refs == 0 && m.retired {
			delete(completeLocks, id)
		}
	}
}
func retireSessionLock(id string) {
	completeMu.Lock()
	defer completeMu.Unlock()
	if m := completeLocks[id]; m != nil {
		m.retired = true
	}
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
		Filename    string   `json:"filename"`
		Size        int64    `json:"size"`
		ContentType string   `json:"content_type"`
		Folder      string   `json:"folder"`
		Tags        []string `json:"tags"`
		Visibility  string   `json:"visibility"`
		Source      string   `json:"source"`
		SHA256      string   `json:"sha256"`
	}
	if err := decodeJSON(r, &body, 32*1024, false); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	body.Filename, err = validateFilename(body.Filename)
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
	if body.SHA256 != "" && !looksLikeSHA256Hex(body.SHA256) {
		httpErr(w, 400, "invalid sha256")
		return
	}
	if body.Filename == "" {
		httpErr(w, http.StatusBadRequest, "filename required")
		return
	}
	if body.Size <= 0 {
		httpErr(w, http.StatusBadRequest, "size must be > 0")
		return
	}
	if maxBytes := maxUploadBytes(ctx); body.Size > maxBytes {
		httpErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("upload exceeds max_upload_size_mb (%d bytes > %d)", body.Size, maxBytes))
		return
	}

	// Pre-dedup short-circuit. A file record is identified by its
	// requested destination as well as its bytes: identical content under
	// a different name/folder must still materialise a distinct row.
	if body.SHA256 != "" {
		if existing, err := dbFindExact(ctx.AppDB(), pid, strings.ToLower(body.SHA256), body.Folder, body.Filename); err == nil && existing != nil {
			if _, _, err := compatibleExisting(existing, uploadInput{ContentType: safeResponseContentType(body.ContentType), Visibility: effectiveVisibility(ctx, body.Visibility), Tags: cleanTags(body.Tags)}); err != nil {
				httpErr(w, 409, err.Error())
				return
			}
			httpJSON(w, map[string]any{
				"file":         existing,
				"was_existing": true,
			})
			return
		}
	}

	id := newUploadID()
	if err = reserveUpload(ctx, id, pid, body.Size, 0); err != nil {
		httpErr(w, 429, err.Error())
		return
	}
	reserved := true
	defer func() {
		if reserved {
			releaseUploadReservation(ctx, id)
		}
	}()
	dir := uploadSessionDir(ctx, id)
	if err := os.MkdirAll(filepath.Join(dir, "parts"), 0755); err != nil {
		httpErr(w, http.StatusInternalServerError, "mkdir: "+err.Error())
		return
	}

	meta := uploadMeta{
		UserID:      uid,
		ProjectID:   pid,
		Filename:    body.Filename,
		ContentType: body.ContentType,
		Folder:      body.Folder,
		Tags:        body.Tags,
		// effectiveVisibility falls back to the install's configured
		// default when the client doesn't pass an explicit value.
		// visibilityOrDefault alone returns "" on miss, which lands
		// in the DB as empty string and renders as "undefined" in the
		// dashboard. Match the single-shot upload path.
		Visibility:     effectiveVisibility(ctx, body.Visibility),
		Source:         strings.TrimSpace(body.Source),
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

	reserved = false
	httpJSON(w, map[string]any{
		"upload_id":    id,
		"part_size":    defaultPartSize,
		"max_parallel": defaultParallel,
		"max_parts":    maxPartNumber,
		"expires_at":   time.Now().Add(configuredUploadIdleTTL(ctx)).UTC().Format(time.RFC3339),
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
	dir := uploadSessionDir(ctx, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		httpErr(w, http.StatusNotFound, "session not found")
		return
	}
	if err := authorizeHTTPSession(r, meta); err != nil {
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
	written, err := writeUploadPart(r.Context(), ctx, id, n, r.Body, r.ContentLength, func(meta *uploadMeta) error { return authorizeHTTPSession(r, meta) })
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "allowance") {
			code = http.StatusRequestEntityTooLarge
		}
		if errors.Is(err, errAbortNotOwner) {
			code = http.StatusForbidden
		}
		httpErr(w, code, err.Error())
		return
	}
	httpJSON(w, map[string]any{"part_number": n, "size": written})
}

// ─── complete ────────────────────────────────────────────────────────

func (a *App) handleUploadComplete(w http.ResponseWriter, r *http.Request, id string) {
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
	meta, err := loadSessionOrCompletion(globalCtx, id, pid)
	if err != nil {
		httpErr(w, 404, "session not found")
		return
	}
	if err = authorizeHTTPSession(r, meta); err != nil {
		httpErr(w, 403, err.Error())
		return
	}
	c := context.WithValue(r.Context(), actorContextKey{}, requestActor(r))
	out, err := completeUploadSessionForTool(globalCtx, c, id, body.SHA256)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, out)
}

// ─── abort ───────────────────────────────────────────────────────────

func (a *App) handleUploadAbort(w http.ResponseWriter, r *http.Request, id string) {
	ctx := globalCtx
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	meta, err := loadSessionOrCompletion(ctx, id, pid)
	if err != nil {
		httpErr(w, 404, "session not found")
		return
	}
	if err = authorizeHTTPSession(r, meta); err != nil {
		httpErr(w, 403, err.Error())
		return
	}
	bytesFreed, err := abortUploadSession(ctx, id, uid, "client")
	if err != nil {
		switch err {
		case errAbortNotFound:
			httpErr(w, http.StatusNotFound, "session not found")
		case errAbortNotOwner:
			httpErr(w, http.StatusForbidden, "not your upload")
		default:
			httpErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httpJSON(w, map[string]any{"aborted": id, "bytes_freed": bytesFreed})
}

// toolAbortUploadCtx — MCP wrapper around abortUploadSession. MCP does
// not use the HTTP endpoint's user ownership model, but it must still
// enforce project isolation and files.write scope for the session's
// destination folder.
func (a *App) toolAbortUploadCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := canonicalUploadID(args)
	if err != nil {
		return nil, err
	}
	if _, _, err := a.requireUploadSessionWriteAccess(ctx, app, args); err != nil {
		if errors.Is(err, errUploadSessionNotFound) {
			return map[string]any{"found": false, "id": id}, nil
		}
		return nil, err
	}
	reason, _ := args["reason"].(string)
	if reason == "" {
		reason = "tool"
	}
	bytes, err := abortUploadSession(app, id, 0, reason)
	if err != nil {
		if errors.Is(err, errAbortNotFound) {
			return map[string]any{"found": false, "id": id}, nil
		}
		return nil, err
	}
	return map[string]any{"found": true, "id": id, "bytes_freed": bytes}, nil
}

// abortUploadSession is the shared backend for the HTTP DELETE
// /uploads/<id> route, the storage_abort_upload MCP tool, and the
// stale-upload sweeper. Returns bytes reclaimed so the caller can
// surface "X MB freed" without a separate stat call.
//
// reason is logged + included in the upload.aborted event ("client",
// "tool", "sweep") for ops visibility.
func abortUploadSession(ctx *sdk.AppCtx, id string, requestingUser int64, reason string) (int64, error) {
	mu := sessionLock(id)
	mu.Lock()
	defer func() { mu.Unlock(); releaseSessionLock(id) }()
	dir := uploadSessionDir(ctx, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		return 0, errAbortNotFound
	}
	if reason == "client" && meta.UserID != requestingUser {
		return 0, errAbortNotOwner
	}
	if requestingUser != 0 && meta.UserID != 0 && meta.UserID != requestingUser {
		return 0, errAbortNotOwner
	}
	if reason == "sweep" {
		if st, e := os.Stat(dir); e != nil || !st.ModTime().Before(time.Now().Add(-configuredUploadIdleTTL(ctx))) {
			return 0, nil
		}
	}
	bytes := dirSize(dir)
	if err = os.RemoveAll(dir); err != nil {
		return 0, err
	}
	retireSessionLock(id)
	releaseUploadReservation(ctx, id)
	emitUploadAborted(ctx, id, meta, bytes, reason)
	return bytes, nil
}

// emitUploadAborted publishes upload.aborted so the dashboard's
// status banner + ops dashboards see "session reclaimed" without
// polling the filesystem.
func emitUploadAborted(ctx *sdk.AppCtx, id string, meta *uploadMeta, bytes int64, reason string) {
	if ctx == nil {
		return
	}
	ctx.EmitWithProject("upload.aborted", meta.ProjectID, map[string]any{
		"upload_id":   id,
		"filename":    meta.Filename,
		"folder":      meta.Folder,
		"bytes_freed": bytes,
		"reason":      reason,
	})
}

// dirSize sums the byte count of every regular file under root.
// Best-effort — failures (deleted mid-walk, permission gaps) silently
// roll forward to whatever was countable.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

var (
	errAbortNotFound = errors.New("session not found")
	errAbortNotOwner = errors.New("not your upload")
)

// ─── partsReaderAt — io.Reader + io.ReaderAt over the part files ─────
//
// minio-go's PutObject probes for io.ReaderAt to enable parallel
// multipart upload. We can't expose a regular *os.File (parts are
// separate files) and io.MultiReader doesn't implement ReadAt, so
// we map a virtual byte offset across the part files ourselves.
//
// Each ReadAt opens only the part files it spans and closes them
// immediately. Descriptor usage is therefore bounded by active S3
// workers instead of total part count; pread keeps offsets independent.

type partRange struct {
	path  string
	start int64 // virtual offset of part start
	size  int64
}

type partsReaderAt struct {
	ranges []partRange
	total  int64

	mu  sync.Mutex
	pos int64 // for sequential Read()
}

func newPartsReaderAt(ctx *sdk.AppCtx, sessionID string, parts []partInfo) (*partsReaderAt, error) {
	pr := &partsReaderAt{
		ranges: make([]partRange, 0, len(parts)),
	}
	var off int64
	for _, p := range parts {
		pr.ranges = append(pr.ranges, partRange{
			path:  partPath(ctx, sessionID, p.N),
			start: off,
			size:  p.Size,
		})
		off += p.Size
	}
	pr.total = off
	return pr, nil
}

// Close is retained for io.Closer compatibility. ReadAt opens only the part
// it is actively reading and closes it immediately, so a 10,000-part upload
// can no longer consume 10,000 process file descriptors.
func (pr *partsReaderAt) Close() error { return nil }

// ReadAt fills p starting at virtual offset off. May span multiple
// part files. Safe for concurrent calls because each call owns its
// file descriptors and uses position-independent ReadAt operations.
func (pr *partsReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= pr.total {
		return 0, io.EOF
	}
	total := 0
	idx := pr.findRange(off)
	for total < len(p) && idx < len(pr.ranges) {
		rng := pr.ranges[idx]
		f, err := os.Open(rng.path)
		if err != nil {
			return total, err
		}
		// Where to start within this part, and how many bytes are left
		// in it from there.
		partOff := (off + int64(total)) - rng.start
		remainingInPart := rng.size - partOff
		want := int64(len(p) - total)
		if want > remainingInPart {
			want = remainingInPart
		}
		n, err := f.ReadAt(p[total:total+int(want)], partOff)
		closeErr := f.Close()
		total += n
		if err != nil && err != io.EOF {
			return total, err
		}
		if closeErr != nil {
			return total, closeErr
		}
		if int64(n) < want {
			// Short read — bail; caller decides what to do.
			break
		}
		idx++
	}
	if total < len(p) && off+int64(total) >= pr.total {
		return total, io.EOF
	}
	return total, nil
}

// Read implements io.Reader for callers that don't use ReadAt
// (notably the disk backend's io.Copy). Single-threaded; uses the
// internal cursor pos.
func (pr *partsReaderAt) Read(p []byte) (int, error) {
	pr.mu.Lock()
	off := pr.pos
	pr.mu.Unlock()
	n, err := pr.ReadAt(p, off)
	pr.mu.Lock()
	pr.pos += int64(n)
	pr.mu.Unlock()
	return n, err
}

// findRange returns the index of the part containing the given
// virtual offset. Linear scan — number of parts is small (typically
// <100 even for GB uploads at 5MB part size) so binary search isn't
// worth the complexity.
func (pr *partsReaderAt) findRange(off int64) int {
	return sort.Search(len(pr.ranges), func(i int) bool { return pr.ranges[i].start+pr.ranges[i].size > off })
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

func newUploadID() string { return newDirectUploadID() }

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
	ttl := configuredUploadIdleTTL(ctx)
	cutoff := time.Now().Add(-ttl)
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
			// Route through abortUploadSession so we get the same
			// lock + event emission as a client-initiated abort.
			// requestingUser=0 skips ownership check; reason="sweep"
			// distinguishes in upload.aborted listeners.
			bytes, err := abortUploadSession(ctx, e.Name(), 0, "sweep")
			if err != nil {
				ctx.Logger().Warn("upload sweeper failed to remove session",
					"id", e.Name(), "err", err)
				continue
			}
			ctx.Logger().Info("upload sweeper removed stale session",
				"id", e.Name(), "age", time.Since(st.ModTime()).String(),
				"bytes_freed", bytes)
		}
	}
}
