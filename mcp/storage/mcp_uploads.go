package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const mcpUploadPartSize int64 = 1 * 1024 * 1024

var errUploadSessionNotFound = errors.New("upload session not found")

func (a *App) toolUploadInitCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name, err := validateFilename(strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if err = validateVisibility(strArg(args, "visibility")); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, errors.New("name required")
	}
	size := int64Arg(args, "size_bytes")
	if size <= 0 {
		return nil, errors.New("size_bytes must be > 0")
	}
	if size > maxUploadBytes(app) {
		return nil, fmt.Errorf("upload exceeds max_upload_size_mb (%d bytes > %d)", size, maxUploadBytes(app))
	}
	folder, err := authorizeFolder(ctx, "files.write", strArg(args, "folder"))
	if err != nil {
		return nil, err
	}
	if caller := sdk.CallerFrom(ctx); caller != nil {
		res := fileResource(folder)
		if !caller.Allows("files.write", res) {
			return nil, sdk.Forbidden("files.write", res)
		}
	}
	sha := strings.ToLower(strings.TrimSpace(strArg(args, "sha256")))
	if sha != "" {
		if !looksLikeSHA256Hex(sha) {
			return nil, errors.New("invalid sha256")
		}
		if existing, err := dbFindExact(app.AppDB(), pid, sha, folder, name); err == nil && existing != nil {
			if _, _, err := compatibleExisting(existing, uploadInput{ContentType: safeResponseContentType(strArg(args, "content_type")), Visibility: effectiveVisibility(app, strArg(args, "visibility")), Tags: cleanTags(strArrayArg(args, "tags"))}); err != nil {
				return nil, err
			}
			return map[string]any{"file": existing, "was_existing": true}, nil
		}
	}

	id := newUploadID()
	if err = reserveUpload(app, id, pid, size, 0); err != nil {
		return nil, err
	}
	reserved := true
	defer func() {
		if reserved {
			releaseUploadReservation(app, id)
		}
	}()
	dir := uploadSessionDir(app, id)
	if err := os.MkdirAll(filepath.Join(dir, "parts"), 0755); err != nil {
		return nil, fmt.Errorf("mkdir upload session: %w", err)
	}
	source := strings.TrimSpace(strArg(args, "source"))
	if source == "" {
		source = "agent"
	}
	meta := uploadMeta{
		UserID:         0,
		ProjectID:      pid,
		Filename:       name,
		ContentType:    strArg(args, "content_type"),
		Folder:         folder,
		Tags:           strArrayArg(args, "tags"),
		Visibility:     effectiveVisibility(app, strArg(args, "visibility")),
		Source:         source,
		DeclaredSize:   size,
		DeclaredSHA256: sha,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	mj, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), mj, 0644); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write upload metadata: %w", err)
	}
	reserved = false
	return map[string]any{
		"upload_id":    id,
		"part_size":    mcpUploadPartSize,
		"max_parts":    maxPartNumber,
		"expires_at":   time.Now().Add(configuredUploadIdleTTL(app)).UTC().Format(time.RFC3339),
		"was_existing": false,
	}, nil
}

func (a *App) toolUploadStatusCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	id, meta, err := a.requireUploadSessionWriteAccess(ctx, app, args)
	if err != nil {
		return nil, err
	}
	parts, bytesUploaded, err := uploadSessionPartsStatus(app, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"upload_id":      id,
		"parts":          parts,
		"bytes_uploaded": bytesUploaded,
		"declared_size":  meta.DeclaredSize,
		"status":         "in_progress",
	}, nil
}

func (a *App) toolUploadPartCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	id, _, err := a.requireUploadSessionWriteAccess(ctx, app, args)
	if err != nil {
		return nil, err
	}
	n, err := uploadPartNumberArg(args)
	if err != nil {
		return nil, err
	}
	raw := strArg(args, "content_base64")
	if raw == "" {
		return nil, errors.New("content_base64 required")
	}
	if int64(len(raw)) > mcpUploadPartSize*4/3+4096 {
		return nil, errors.New("encoded part exceeds MCP chunk limit")
	}
	body, err := decodeUploadPayload(raw)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("empty part")
	}
	if int64(len(body)) > mcpUploadPartSize {
		return nil, fmt.Errorf("decoded part exceeds MCP chunk limit (%d > %d); split into smaller chunks", len(body), mcpUploadPartSize)
	}
	if _, err := writeUploadPart(ctx, app, id, n, bytes.NewReader(body), int64(len(body)), nil); err != nil {
		return nil, err
	}
	mu := sessionLock(id)
	mu.budget.Lock()
	bytesUploaded := mu.total
	uploadedParts := len(mu.sizes)
	mu.budget.Unlock()
	releaseSessionLock(id)
	return map[string]any{
		"upload_id":       id,
		"part_number":     n,
		"size":            len(body),
		"bytes_uploaded":  bytesUploaded,
		"uploaded_parts":  uploadedParts,
		"recommended_max": mcpUploadPartSize,
	}, nil
}

func (a *App) toolUploadCompleteCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	id, _, err := a.requireUploadSessionWriteAccess(ctx, app, args)
	if err != nil {
		return nil, err
	}
	return completeUploadSessionForTool(app, ctx, id, strArg(args, "sha256"))
}

func (a *App) requireUploadSessionWriteAccess(ctx context.Context, app *sdk.AppCtx, args map[string]any) (string, *uploadMeta, error) {
	id, err := canonicalUploadID(args)
	if err != nil {
		return "", nil, err
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return "", nil, err
	}
	meta, err := loadSessionOrCompletion(app, id, pid)
	if err != nil {
		return "", nil, err
	}
	if pid != meta.ProjectID {
		return "", nil, errors.New("upload session belongs to a different project")
	}
	if caller := sdk.CallerFrom(ctx); caller != nil {
		res := fileResource(meta.Folder)
		if !caller.Allows("files.write", res) {
			return "", nil, sdk.Forbidden("files.write", res)
		}
	}
	return id, meta, nil
}

func uploadSessionPartsStatus(ctx *sdk.AppCtx, id string) ([]partInfo, int64, error) {
	parts, err := listParts(ctx, id)
	if err != nil {
		return nil, 0, fmt.Errorf("list parts: %w", err)
	}
	var bytesUploaded int64
	for _, p := range parts {
		bytesUploaded += p.Size
	}
	return parts, bytesUploaded, nil
}

func writeUploadPartBytes(app *sdk.AppCtx, id string, n int, body []byte) error {
	_, err := writeUploadPart(context.Background(), app, id, n, bytes.NewReader(body), int64(len(body)), nil)
	return err
}

func completeUploadSessionForTool(app *sdk.AppCtx, c context.Context, id, suppliedSHA string) (any, error) {
	mu := sessionLock(id)
	mu.Lock()
	defer func() { mu.Unlock(); releaseSessionLock(id) }()
	var pid string
	err := app.AppDB().QueryRow(`SELECT project_id FROM completed_uploads WHERE upload_id=?`, id).Scan(&pid)
	if err == nil {
		f, existed, e := completedUpload(app, id, pid)
		if e != nil {
			return nil, e
		}
		if suppliedSHA != "" && !strings.EqualFold(suppliedSHA, f.SHA256) {
			return nil, errors.New("sha256 mismatch")
		}
		return map[string]any{"file": f, "was_existing": existed, "sha256": f.SHA256, "size_bytes": f.SizeBytes}, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	dir := uploadSessionDir(app, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		return nil, errUploadSessionNotFound
	}
	parts, err := listParts(app, id)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("no parts uploaded")
	}
	var total int64
	for i, p := range parts {
		if p.N != i+1 {
			return nil, fmt.Errorf("missing part %d", i+1)
		}
		total += p.Size
	}
	if total != meta.DeclaredSize {
		return nil, fmt.Errorf("size mismatch: parts total %d, declared %d", total, meta.DeclaredSize)
	}
	if suppliedSHA != "" && meta.DeclaredSHA256 != "" && !strings.EqualFold(suppliedSHA, meta.DeclaredSHA256) {
		return nil, errors.New("declared sha256 mismatch")
	}
	pr, err := newPartsReaderAt(app, id, parts)
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	release, err := acquireTransfer(c)
	if err != nil {
		return nil, err
	}
	defer release()
	in := uploadInput{Name: meta.Filename, Folder: meta.Folder, ContentType: meta.ContentType, Tags: meta.Tags, Visibility: meta.Visibility, Source: meta.Source, ExpectedSize: meta.DeclaredSize, ExpectedSHA: ifEmpty(suppliedSHA, meta.DeclaredSHA256), UploadID: id, UserID: meta.UserID}
	f, existed, err := saveStream(c, app, meta.ProjectID, in, pr)
	if err != nil {
		return nil, err
	}
	if err = os.RemoveAll(dir); err != nil {
		app.Logger().Warn("completed scratch cleanup pending", "upload_id", id)
	}
	retireSessionLock(id)
	releaseUploadReservation(app, id)
	emitFileEvent(app, "file.added", f, existed)
	return map[string]any{"file": f, "was_existing": existed, "sha256": f.SHA256, "size_bytes": f.SizeBytes}, nil
}

func uploadPartNumberArg(args map[string]any) (int, error) {
	n := intArg(args, "part_number", 0)
	if n < 1 || n > maxPartNumber {
		return 0, errors.New("part_number out of range")
	}
	return n, nil
}
