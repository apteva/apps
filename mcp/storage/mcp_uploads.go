package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	name := normaliseFilename(strArg(args, "name"))
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
	folder := normaliseFolder(strArg(args, "folder"))
	if caller := sdk.CallerFrom(ctx); caller != nil {
		res := fileResource(folder)
		if !caller.Allows("files.write", res) {
			return nil, sdk.Forbidden("files.write", res)
		}
	}
	sha := strings.ToLower(strings.TrimSpace(strArg(args, "sha256")))
	if sha != "" {
		if existing, err := dbFindExact(app.AppDB(), pid, sha, folder, name); err == nil && existing != nil {
			return map[string]any{"file": existing, "was_existing": true}, nil
		}
	}

	id := newUploadID()
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
	if err := writeUploadPartBytes(app, id, n, body); err != nil {
		return nil, err
	}
	parts, bytesUploaded, err := uploadSessionPartsStatus(app, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"upload_id":       id,
		"part_number":     n,
		"size":            len(body),
		"bytes_uploaded":  bytesUploaded,
		"uploaded_parts":  len(parts),
		"recommended_max": mcpUploadPartSize,
	}, nil
}

func (a *App) toolUploadCompleteCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	id, _, err := a.requireUploadSessionWriteAccess(ctx, app, args)
	if err != nil {
		return nil, err
	}
	return completeUploadSessionForTool(app, context.Background(), id, strArg(args, "sha256"))
}

func (a *App) requireUploadSessionWriteAccess(ctx context.Context, app *sdk.AppCtx, args map[string]any) (string, *uploadMeta, error) {
	id := strings.TrimSpace(strArg(args, "upload_id"))
	if id == "" {
		id = strings.TrimSpace(strArg(args, "id"))
	}
	if !validUploadID(id) {
		return "", nil, errors.New("valid upload_id required")
	}
	meta, err := loadUploadMeta(uploadSessionDir(app, id))
	if err != nil {
		return "", nil, errUploadSessionNotFound
	}
	pid, err := resolveProjectFromArgs(args)
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

func writeUploadPartBytes(ctx *sdk.AppCtx, id string, n int, body []byte) error {
	if int64(len(body)) > maxPartSize {
		return fmt.Errorf("part exceeds %d bytes", maxPartSize)
	}
	dir := uploadSessionDir(ctx, id)
	if _, err := loadUploadMeta(dir); err != nil {
		return errors.New("upload session not found")
	}
	pp := partPath(ctx, id, n)
	tmp := pp + ".tmp." + randHex(8)
	if err := os.WriteFile(tmp, body, 0644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write part: %w", err)
	}
	if err := os.Rename(tmp, pp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename part: %w", err)
	}
	_ = os.Chtimes(dir, time.Now(), time.Now())
	return nil
}

func completeUploadSessionForTool(ctx *sdk.AppCtx, reqCtx context.Context, id, suppliedSHA string) (any, error) {
	mu := sessionLock(id)
	mu.Lock()
	defer func() {
		mu.Unlock()
		releaseSessionLock(id)
	}()

	dir := uploadSessionDir(ctx, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		return nil, errors.New("upload session not found")
	}
	parts, err := listParts(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list parts: %w", err)
	}
	if len(parts) == 0 {
		return nil, errors.New("no parts uploaded")
	}
	var totalSize int64
	for i, p := range parts {
		if p.N != i+1 {
			return nil, fmt.Errorf("missing part %d (have %d parts, last is %d)", i+1, len(parts), p.N)
		}
		totalSize += p.Size
	}
	if totalSize != meta.DeclaredSize {
		return nil, fmt.Errorf("size mismatch: parts total %d, declared %d", totalSize, meta.DeclaredSize)
	}

	h := sha256.New()
	for _, p := range parts {
		f, err := os.Open(partPath(ctx, id, p.N))
		if err != nil {
			return nil, fmt.Errorf("open part %d: %w", p.N, err)
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return nil, fmt.Errorf("hash part %d: %w", p.N, err)
		}
		f.Close()
	}
	finalSHA := hex.EncodeToString(h.Sum(nil))
	if suppliedSHA != "" && !strings.EqualFold(suppliedSHA, finalSHA) {
		return nil, fmt.Errorf("sha256 mismatch: client=%s server=%s", suppliedSHA, finalSHA)
	}
	if meta.DeclaredSHA256 != "" && !strings.EqualFold(meta.DeclaredSHA256, finalSHA) {
		return nil, errors.New("declared sha256 mismatch - bytes corrupted")
	}
	if existing, err := dbFindExact(ctx.AppDB(), meta.ProjectID, finalSHA, meta.Folder, meta.Filename); err == nil && existing != nil {
		_ = os.RemoveAll(dir)
		return map[string]any{"file": existing, "was_existing": true}, nil
	}

	pr, err := newPartsReaderAt(ctx, id, parts)
	if err != nil {
		return nil, fmt.Errorf("open parts: %w", err)
	}
	defer pr.Close()
	tmpKey := newUploadID() + extOf(meta.Filename, meta.ContentType)
	finalKey := objectKey(finalSHA, tmpKey)
	if err := backend().Put(reqCtx, finalKey, meta.ContentType, pr, totalSize); err != nil {
		return nil, fmt.Errorf("backend put: %w", err)
	}

	tagsJSON, _ := json.Marshal(meta.Tags)
	source := strings.TrimSpace(meta.Source)
	if source == "" {
		source = "agent"
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO files
			(project_id, name, folder, storage_key, content_type, size_bytes,
			 sha256, uploaded_by, source, tags, visibility)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.ProjectID, meta.Filename, meta.Folder, tmpKey, meta.ContentType, meta.DeclaredSize,
		finalSHA, callerLabel(), source, string(tagsJSON), meta.Visibility,
	)
	if err != nil {
		_ = backend().Delete(reqCtx, finalKey)
		return nil, fmt.Errorf("insert: %w", err)
	}
	insID, _ := res.LastInsertId()
	row, err := dbGetByID(ctx.AppDB(), meta.ProjectID, insID)
	if err != nil || row == nil {
		return nil, fmt.Errorf("lookup new file %d: %v", insID, err)
	}
	emitFileEvent(ctx, "file.added", row, false)
	_ = os.RemoveAll(dir)

	return map[string]any{
		"file":         row,
		"was_existing": false,
		"sha256":       finalSHA,
		"size_bytes":   totalSize,
	}, nil
}

func uploadPartNumberArg(args map[string]any) (int, error) {
	n := intArg(args, "part_number", 0)
	if n < 1 || n > maxPartNumber {
		return 0, errors.New("part_number out of range")
	}
	return n, nil
}
