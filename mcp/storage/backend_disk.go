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
	"fmt"
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

// CommitTemp atomically promotes an already-written temporary file into the
// blob tree. Resumable disk uploads hash while assembling, so using rename
// here avoids rereading the complete upload merely to copy it into place.
func (d *diskBackend) CommitTemp(tmpPath, key string) error {
	abs := d.absPath(key)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, abs); err == nil {
		return nil
	}
	// STORAGE_UPLOADS_DIR and STORAGE_BLOBS_DIR may live on different
	// filesystems. Preserve compatibility in that configuration.
	return copyAndRemove(tmpPath, abs)
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

func (d *diskBackend) HeadObject(_ context.Context, key string) (ObjectMetadata, error) {
	st, err := os.Stat(d.absPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return ObjectMetadata{}, ErrNotFound
	}
	if err != nil {
		return ObjectMetadata{}, err
	}
	return ObjectMetadata{Size: st.Size(), LastModified: st.ModTime()}, nil
}

func (d *diskBackend) OpenObject(_ context.Context, key string, options ObjectReadOptions) (*ObjectReadResult, error) {
	f, err := os.Open(d.absPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	start, end, ranged, err := resolveByteRange(options.Range, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	result := &ObjectReadResult{
		Body: f, StatusCode: 200, ContentLength: st.Size(), LastModified: st.ModTime(),
	}
	if ranged {
		length := end - start + 1
		result.Body = &sectionReadCloser{SectionReader: io.NewSectionReader(f, start, length), Closer: f}
		result.StatusCode = 206
		result.ContentLength = length
		result.ContentRange = fmt.Sprintf("bytes %d-%d/%d", start, end, st.Size())
	}
	return result, nil
}

type sectionReadCloser struct {
	*io.SectionReader
	io.Closer
}

func (d *diskBackend) LocalPath(key string) (string, bool) {
	return d.absPath(key), true
}

func (d *diskBackend) PresignGet(_ context.Context, _ string, _ GetObjectOptions, _ time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}

func (d *diskBackend) PresignPut(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}
