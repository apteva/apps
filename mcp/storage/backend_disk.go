package main

// Disk backend — blobs as files under blobsDir(ctx). Preserves the
// pre-v0.6 layout exactly so an in-place upgrade doesn't move bytes:
//
//	<blobsDir>/<sha256[:2]>/<storage_key>
//
// Presigned ops are unsupported; the disk path is for installs that
// don't need direct-to-cloud transfer.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type diskBackend struct {
	ctx *sdk.AppCtx // for blobsDir lookup; allows env override per-test
}

func newDiskBackend(ctx *sdk.AppCtx) *diskBackend {
	return &diskBackend{ctx: ctx}
}

func (d *diskBackend) Kind() string { return "disk" }

func (d *diskBackend) absPath(key string) string {
	// Defence in depth: refuse paths that try to escape blobsDir.
	// objectKey produces "<2hex>/<uuid>" which is always safe; this
	// guards against future callers that compose keys differently.
	return filepath.Join(blobsDir(d.ctx), filepath.Clean("/"+key))
}

func (d *diskBackend) Put(_ context.Context, key, _ string, r io.Reader, size int64) error {
	abs := d.absPath(key)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	f, err := os.Create(abs)
	if err != nil {
		return err
	}
	// Cap at size — defend against a wonky reader that doesn't EOF.
	// size <= 0 means "trust the reader" (callers like saveBytes
	// already pass a bounded body).
	var rd io.Reader = r
	if size > 0 {
		rd = io.LimitReader(r, size)
	}
	if _, err := io.Copy(f, rd); err != nil {
		f.Close()
		_ = os.Remove(abs)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(abs)
		return err
	}
	return nil
}

func (d *diskBackend) Delete(_ context.Context, key string) error {
	abs := d.absPath(key)
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *diskBackend) Stat(_ context.Context, key string) (int64, error) {
	abs := d.absPath(key)
	st, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (d *diskBackend) LocalPath(key string) (string, bool) {
	return d.absPath(key), true
}

func (d *diskBackend) PresignGet(_ context.Context, _, _, _ string, _ time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}

func (d *diskBackend) PresignPut(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}
