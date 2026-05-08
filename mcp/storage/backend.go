package main

// Pluggable storage backend.
//
// Until v0.6, storage wrote bytes directly to the local filesystem.
// v0.6 introduces a Backend interface so an S3-compatible backend
// (AWS, R2, B2, Wasabi, MinIO, …) can host blobs instead. The
// install picks one via the `backend` config field; "disk" stays
// the default and behaves bit-for-bit as before.
//
// The interface is intentionally tiny: Put/Delete/Stat for the proxy
// path, and PresignPut/PresignGet for direct client⇄storage transfer.
// Disk implements the proxy ops + returns ErrPresignNotSupported on
// the presigned ones. S3 implements all four.
//
// Key layout: every blob is addressed by a `<sha256[:2]>/<storage_key>`
// path-style key. Disk uses it as a filesystem path under blobsDir;
// S3 uses it as the bucket-relative object key. The two-byte hash
// prefix exists for the disk's benefit (avoids 1M files in one
// directory) and is harmless on S3.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ErrPresignNotSupported is returned by backends that can't mint
// direct client URLs (i.e. disk). Handlers detect this and either
// fall back to the proxy path or return 501 to opt-in clients.
var ErrPresignNotSupported = errors.New("backend does not support presigned URLs")

// ErrNotFound is returned by Stat when an object is absent. Other
// methods report not-found via os.IsNotExist-style nil errors where
// it's harmless (Delete is idempotent).
var ErrNotFound = errors.New("object not found")

// Backend is the abstract blob store. Implementations live in
// backend_disk.go and backend_s3.go.
type Backend interface {
	// Kind identifies the backend in logs + metrics. "disk" | "s3".
	Kind() string

	// Put writes a blob. size is the authoritative content-length;
	// implementations MUST stop reading once size bytes are consumed
	// to defend against runaway readers.
	Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error

	// Delete removes a blob. Idempotent — missing keys do not error.
	Delete(ctx context.Context, key string) error

	// Stat returns the blob's size when present. Returns ErrNotFound
	// when absent. Used by the presigned-finalize endpoint to verify
	// a client-direct upload actually arrived.
	Stat(ctx context.Context, key string) (int64, error)

	// LocalPath returns a filesystem path to serve via http.ServeFile
	// when the backend stores bytes locally. Disk returns (path, true);
	// remote backends return ("", false) so the caller can switch to a
	// presigned redirect.
	LocalPath(key string) (string, bool)

	// PresignGet mints a direct download URL with the given TTL.
	// filename and contentType are advisory — backends use them to set
	// Content-Disposition / Content-Type on the presigned response so
	// the user-agent gets the right behaviour. Disk returns
	// ErrPresignNotSupported.
	PresignGet(ctx context.Context, key, filename, contentType string, ttl time.Duration) (string, error)

	// PresignPut mints a direct upload URL with the given TTL. Used by
	// the /files/init endpoint to hand a client an S3 PUT URL it can
	// upload to without proxying through us. Disk returns
	// ErrPresignNotSupported.
	PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error)
}

// objectKey is the canonical key for a blob: <sha256[:2]>/<storage_key>.
// Both backends agree on this — disk treats it as a relative path
// under blobsDir, S3 uses it as the object key in the bucket.
//
// The first two hex chars from sha256 fan out the keyspace so the
// disk backend doesn't pile millions of files into one directory.
// On S3 it's just two extra characters; bucket key listings stay
// efficient regardless of fan-out.
func objectKey(sha256, storageKey string) string {
	prefix := "00"
	if len(sha256) >= 2 {
		prefix = sha256[:2]
	}
	return prefix + "/" + storageKey
}

// ─── Backend selection ─────────────────────────────────────────────

// globalBackend is the resolved Backend for this install. Set in
// OnMount; nil before then. Tests can override directly to inject a
// stub. Use backend() rather than referencing this var directly so
// the lazy-init disk fallback kicks in for tests that bypass
// OnMount.
var globalBackend Backend

// backend returns the active backend. Falls back to a disk backend
// rooted at globalCtx when the global hasn't been initialised yet —
// keeps unit tests that skip OnMount working without a setup hook.
func backend() Backend {
	if globalBackend != nil {
		return globalBackend
	}
	if globalCtx != nil {
		globalBackend = newDiskBackend(globalCtx)
		return globalBackend
	}
	// Last-resort no-op: should never happen in production, but a
	// nil deref here would mask real test ordering bugs.
	panic("backend(): globalCtx not set — call tk.NewAppCtx + assign globalCtx before invoking storage handlers")
}

// initBackend resolves the active backend from the install state.
// v0.9 model:
//
//	If requires.integrations[role=backend] is bound + a bucket is
//	configured → s3, with credentials read live from the bound
//	connection.
//	Otherwise → disk.
//
// No more `backend` config toggle: the binding's presence is the
// signal. An operator who wants to fall back to disk can clear the
// binding from Settings.
//
// Returns an error rather than silently falling back — a binding
// present but bucket missing (or creds unreadable) should fail loud
// at boot, not route writes to disk.
func initBackend(ctx *sdk.AppCtx) (Backend, error) {
	bound := ctx.IntegrationFor(s3IntegrationRole)
	if bound == nil {
		return newDiskBackend(ctx), nil
	}
	bucket := strings.TrimSpace(ctx.Config().Get("s3_bucket"))
	if bucket == "" {
		return nil, fmt.Errorf("s3 backend: integration is bound but s3_bucket is empty — set the bucket name in Storage settings")
	}
	return newS3Backend(ctx, bound, bucket)
}

// s3IntegrationRole is the role name in requires.integrations.
const s3IntegrationRole = "backend"
