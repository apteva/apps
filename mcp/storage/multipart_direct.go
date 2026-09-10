package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

type directMultipart struct {
	ID         string `json:"id"`
	StorageKey string `json:"storage_key"`
	PartSize   int64  `json:"part_size"`
}

func saveUploadMeta(dir string, meta *uploadMeta) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "meta.json"))
}
func beginDirectMultipart(c context.Context, app *sdk.AppCtx, id string, meta *uploadMeta) error {
	be, ok := backend().(multipartBackend)
	if !ok {
		return errors.New("direct multipart backend unavailable")
	}
	partSize := int64(configUintClamped(app.Config().Get("s3_part_size_mb"), 16, 5, 128)) * 1024 * 1024
	// Keep S3's 10,000-part ceiling even for large configured file limits.
	partSize = max(partSize, (meta.DeclaredSize+maxPartNumber-1)/maxPartNumber)
	remote := &directMultipart{StorageKey: uuid.NewString() + extOf(meta.Filename, meta.ContentType), PartSize: partSize}
	key := objectKey("", remote.StorageKey)
	// The final object uses this unique key. UploadPart URLs cannot mutate it
	// after CompleteMultipartUpload consumes the provider's upload ID.
	if err := queueBlobCleanup(app, key, time.Now().Add(configuredUploadIdleTTL(app)+24*time.Hour)); err != nil {
		return err
	}
	var err error
	remote.ID, err = be.BeginMultipart(c, key, safeResponseContentType(meta.ContentType))
	if err != nil {
		return err
	}
	meta.Direct = remote
	if err = saveUploadMeta(uploadSessionDir(app, id), meta); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = be.AbortMultipart(cleanup, key, remote.ID)
		return err
	}
	return nil
}
func directPartSize(meta *uploadMeta, n int) (int64, error) {
	d := meta.Direct
	if d == nil || d.PartSize <= 0 || n < 1 || int64(n) > (meta.DeclaredSize+d.PartSize-1)/d.PartSize {
		return 0, errors.New("invalid direct upload part")
	}
	return min(d.PartSize, meta.DeclaredSize-int64(n-1)*d.PartSize), nil
}
func (a *App) handleUploadPartURL(w http.ResponseWriter, r *http.Request, id string, n int) {
	app := globalCtx
	mu := sessionLock(id)
	mu.RLock()
	defer func() { mu.RUnlock(); releaseSessionLock(id) }()
	dir := uploadSessionDir(app, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		httpErr(w, 404, "session not found")
		return
	}
	if err = authorizeHTTPSession(r, meta); err != nil {
		httpErr(w, 403, "not your upload")
		return
	}
	size, err := directPartSize(meta, n)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	be, ok := backend().(multipartBackend)
	if !ok {
		httpErr(w, 409, "multipart backend unavailable")
		return
	}
	u, err := be.SignMultipartPart(r.Context(), objectKey("", meta.Direct.StorageKey), meta.Direct.ID, n, size)
	if err != nil {
		httpErr(w, 502, "cannot sign upload part")
		app.Logger().Warn("multipart signing failed", "upload_id", id, "part", n, "err", err)
		return
	}
	_ = os.Chtimes(dir, time.Now(), time.Now())
	w.Header().Set("Cache-Control", "no-store")
	httpJSON(w, map[string]any{"url": u, "headers": map[string]string{}, "size": size})
}
func directParts(c context.Context, meta *uploadMeta) ([]remotePart, error) {
	be, ok := backend().(multipartBackend)
	if !ok {
		return nil, errors.New("multipart backend unavailable")
	}
	return be.MultipartParts(c, objectKey("", meta.Direct.StorageKey), meta.Direct.ID)
}

// Called with the exclusive session lock. Only S3 metadata crosses Apteva:
// authoritative part sizes/ETags, complete, and HEAD. No download, re-upload,
// staging file, or invented whole-file SHA256.
func completeDirectMultipart(c context.Context, app *sdk.AppCtx, id string, meta *uploadMeta) (any, error) {
	be, ok := backend().(multipartBackend)
	if !ok {
		return nil, errors.New("multipart backend unavailable")
	}
	key := objectKey("", meta.Direct.StorageKey)
	// HEAD also recovers a previous successful S3 completion when its response
	// or the following database commit was interrupted.
	stat, statErr := backend().Stat(c, key)
	if statErr != nil {
		parts, err := directParts(c, meta)
		if err != nil {
			return nil, err
		}
		expected := (meta.DeclaredSize + meta.Direct.PartSize - 1) / meta.Direct.PartSize
		if int64(len(parts)) != expected {
			return nil, fmt.Errorf("incomplete upload: %d of %d parts", len(parts), expected)
		}
		for i, p := range parts {
			size, e := directPartSize(meta, i+1)
			if e != nil || p.Number != i+1 || p.Size != size || p.ETag == "" {
				return nil, fmt.Errorf("invalid size or identity for part %d", i+1)
			}
		}
		if err = be.FinishMultipart(c, key, meta.Direct.ID, parts); err != nil {
			return nil, err
		}
		stat, statErr = backend().Stat(c, key)
	}
	if statErr != nil {
		return nil, statErr
	}
	if stat != meta.DeclaredSize {
		return nil, errors.New("completed object size mismatch")
	}
	in := uploadInput{Name: meta.Filename, Folder: meta.Folder, ContentType: safeResponseContentType(meta.ContentType), Tags: cleanTags(meta.Tags), Visibility: meta.Visibility, Source: ifEmpty(meta.Source, "human"), UserID: meta.UserID, UploadID: id}
	f, existed, err := publishFile(c, app, meta.ProjectID, in, "", meta.Direct.StorageKey, meta.DeclaredSize, id)
	if err != nil {
		return nil, err
	}
	_ = os.RemoveAll(uploadSessionDir(app, id))
	releaseUploadReservation(app, id)
	retireSessionLock(id)
	app.Logger().Info("multipart upload completed", "upload_id", id, "project_id", meta.ProjectID, "file_id", f.ID, "bytes", f.SizeBytes, "mode", "s3_multipart")
	emitFileEvent(app, "file.added", f, existed)
	return map[string]any{"file": f, "was_existing": existed, "size_bytes": f.SizeBytes}, nil
}
