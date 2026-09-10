package main

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Caller holds the shared session lock, so abort/completion cannot race a PUT.
func relayUploadPart(c context.Context, app *sdk.AppCtx, id string, n int, meta *uploadMeta, mu *uploadLock, r io.Reader, length int64) (int64, error) {
	size, err := directPartSize(meta, n)
	if err != nil {
		return 0, err
	}
	if length != size {
		return 0, errors.New("part Content-Length must match expected size")
	}
	be, ok := backend().(multipartRelayBackend)
	if !ok {
		return 0, errors.New("multipart relay unavailable")
	}
	mu.budget.Lock()
	if mu.writing == nil {
		mu.writing = map[int]bool{}
	}
	if mu.writing[n] {
		mu.budget.Unlock()
		return 0, errors.New("part already being written; retry")
	}
	mu.writing[n] = true
	firstRelay := !mu.relayed
	mu.relayed = true
	mu.budget.Unlock()
	if firstRelay {
		app.Logger().Info("multipart relay started", "upload_id", id, "project_id", meta.ProjectID)
	}
	defer func() { mu.budget.Lock(); delete(mu.writing, n); mu.budget.Unlock() }()
	body := &io.LimitedReader{R: &contextReader{c, r}, N: size}
	part, err := be.PutMultipartPart(c, objectKey("", meta.Direct.StorageKey), meta.Direct.ID, n, body, size)
	if err != nil {
		app.Logger().Warn("multipart relay failed", "upload_id", id, "part", n, "err", err)
		return 0, errors.New("backend part transfer failed; retry")
	}
	if body.N != 0 || part.Size != size || part.Number != n || part.ETag == "" {
		return 0, errors.New("incomplete backend part")
	}
	_ = os.Chtimes(uploadSessionDir(app, id), time.Now(), time.Now())
	return size, nil
}
