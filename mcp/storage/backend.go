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
	"mime"
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

// ErrRangeNotSatisfiable is returned when a syntactically valid byte range
// does not overlap the object. HTTP handlers map it to 416.
var ErrRangeNotSatisfiable = errors.New("object range not satisfiable")

type ContentDisposition string

const (
	DispositionInline     ContentDisposition = "inline"
	DispositionAttachment ContentDisposition = "attachment"
)

// GetObjectOptions describes how a fetched object should be presented. The
// disposition is normalized against ContentType before use so executable or
// unknown content cannot be rendered inline on the Storage origin.
type GetObjectOptions struct {
	Filename    string
	ContentType string
	Disposition ContentDisposition
}

// ObjectMetadata is the backend-authoritative metadata used by proxied HEAD
// responses. ETag is stored without imposing a quoting convention; the HTTP
// layer normalizes it before writing the response header.
type ObjectMetadata struct {
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

// ObjectReadOptions controls a streaming backend GET. Range is either empty
// or a validated single RFC byte range such as "bytes=0-1023".
type ObjectReadOptions struct {
	Range string
}

// ObjectReadResult owns Body; callers must close it. ContentLength describes
// this response body (the selected range for 206), not necessarily the full
// object size.
type ObjectReadResult struct {
	Body          io.ReadCloser
	StatusCode    int
	ContentLength int64
	ContentRange  string
	ContentType   string
	ETag          string
	LastModified  time.Time
}

// Backend is the abstract blob store. Implementations live in
// backend_disk.go and backend_s3.go. HeadObject and OpenObject support the
// explicit proxy delivery path without creating backend URLs.
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

	// HeadObject returns authoritative metadata without opening an object body.
	HeadObject(ctx context.Context, key string) (ObjectMetadata, error)

	// OpenObject starts a credentialed streaming GET against the backend. It
	// must never mint or follow a client-visible presigned URL.
	OpenObject(ctx context.Context, key string, options ObjectReadOptions) (*ObjectReadResult, error)

	// LocalPath returns a filesystem path to serve via http.ServeFile
	// when the backend stores bytes locally. Disk returns (path, true);
	// remote backends return ("", false) so the caller can switch to a
	// presigned redirect.
	LocalPath(key string) (string, bool)

	// PresignGet mints a direct delivery URL with the given TTL.
	// options are advisory — backends use them to set
	// Content-Disposition / Content-Type on the presigned response so
	// the user-agent gets the right behaviour. Disk returns
	// ErrPresignNotSupported.
	PresignGet(ctx context.Context, key string, options GetObjectOptions, ttl time.Duration) (string, error)

	// PresignPut mints a direct upload URL with the given TTL. Used by
	// the /files/init endpoint to hand a client an S3 PUT URL it can
	// upload to without proxying through us. Disk returns
	// ErrPresignNotSupported.
	PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error)
}

type byteRange struct {
	start  int64
	end    int64
	suffix bool
	open   bool
}

// parseSingleByteRange deliberately supports one range only. Multipart byte
// ranges add substantial response complexity and are unnecessary for media
// ingestion clients, which issue one probe range at a time.
func parseSingleByteRange(raw string) (byteRange, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return byteRange{}, nil
	}
	if !strings.HasPrefix(strings.ToLower(raw), "bytes=") {
		return byteRange{}, fmt.Errorf("invalid range unit")
	}
	spec := strings.TrimSpace(raw[len("bytes="):])
	if spec == "" || strings.Contains(spec, ",") {
		return byteRange{}, fmt.Errorf("exactly one byte range is required")
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return byteRange{}, fmt.Errorf("invalid byte range")
	}
	if parts[0] == "" {
		n, err := parseNonNegativeInt64(parts[1])
		if err != nil || n <= 0 {
			return byteRange{}, fmt.Errorf("invalid suffix byte range")
		}
		return byteRange{end: n, suffix: true}, nil
	}
	start, err := parseNonNegativeInt64(parts[0])
	if err != nil {
		return byteRange{}, fmt.Errorf("invalid byte range start")
	}
	if parts[1] == "" {
		return byteRange{start: start, open: true}, nil
	}
	end, err := parseNonNegativeInt64(parts[1])
	if err != nil || end < start {
		return byteRange{}, fmt.Errorf("invalid byte range end")
	}
	return byteRange{start: start, end: end}, nil
}

func parseNonNegativeInt64(raw string) (int64, error) {
	if raw == "" {
		return 0, errors.New("empty integer")
	}
	var n int64
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, errors.New("non-decimal integer")
		}
		if n > (1<<63-1-int64(ch-'0'))/10 {
			return 0, errors.New("integer overflow")
		}
		n = n*10 + int64(ch-'0')
	}
	return n, nil
}

func resolveByteRange(raw string, size int64) (start, end int64, ranged bool, err error) {
	parsed, err := parseSingleByteRange(raw)
	if err != nil {
		return 0, 0, false, err
	}
	if strings.TrimSpace(raw) == "" {
		return 0, 0, false, nil
	}
	if size <= 0 {
		return 0, 0, true, ErrRangeNotSatisfiable
	}
	if parsed.suffix {
		length := parsed.end
		if length > size {
			length = size
		}
		return size - length, size - 1, true, nil
	}
	if parsed.start >= size {
		return 0, 0, true, ErrRangeNotSatisfiable
	}
	end = parsed.end
	if parsed.open || end >= size {
		end = size - 1
	}
	return parsed.start, end, true, nil
}

func parseContentDisposition(raw string) (ContentDisposition, error) {
	switch ContentDisposition(strings.ToLower(strings.TrimSpace(raw))) {
	case "", DispositionInline:
		return DispositionInline, nil
	case DispositionAttachment:
		return DispositionAttachment, nil
	default:
		return "", fmt.Errorf("disposition must be one of: inline, attachment")
	}
}

// effectiveContentDisposition refuses inline rendering for active or unknown
// formats. This matters most on disk-backed installs, where bytes are served
// from the same origin as the application.
func effectiveContentDisposition(requested ContentDisposition, contentType string) ContentDisposition {
	if requested == DispositionAttachment {
		return DispositionAttachment
	}
	if isSafeInlineContentType(contentType) {
		return DispositionInline
	}
	return DispositionAttachment
}

func isSafeInlineContentType(raw string) bool {
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "audio/") || strings.HasPrefix(mediaType, "video/") {
		return true
	}
	switch mediaType {
	case "application/pdf", "text/plain",
		"image/png", "image/jpeg", "image/gif", "image/webp", "image/avif",
		"image/apng", "image/bmp", "image/tiff", "image/x-icon", "image/vnd.microsoft.icon":
		return true
	default:
		return false
	}
}

func safeResponseContentType(raw string) string {
	mediaType, params, err := mime.ParseMediaType(raw)
	if err != nil || mediaType == "" {
		return "application/octet-stream"
	}
	if formatted := mime.FormatMediaType(strings.ToLower(mediaType), params); formatted != "" {
		return formatted
	}
	return strings.ToLower(mediaType)
}

// contentDispositionHeader emits both a conservative ASCII fallback and an
// RFC 5987 UTF-8 filename. Header-breaking bytes never survive either form.
func contentDispositionHeader(disposition ContentDisposition, filename string) string {
	if disposition != DispositionInline && disposition != DispositionAttachment {
		disposition = DispositionAttachment
	}
	fallback := asciiFilenameFallback(filename)
	encoded := encodeRFC5987Filename(filename)
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disposition, fallback, encoded)
}

func asciiFilenameFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '/' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "download"
	}
	return b.String()
}

func encodeRFC5987Filename(name string) string {
	if name == "" {
		name = "download"
	}
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.ContainsRune("!#$&+-.^_`|~", rune(c)) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// presignedResponseCacheControl keeps cached S3 responses inside the URL's
// validity window. The skew allowance covers time spent following the 302.
func presignedResponseCacheControl(ttl time.Duration) string {
	seconds := int64(ttl / time.Second)
	const skew = int64(30)
	if seconds <= skew {
		return "private, no-store"
	}
	return fmt.Sprintf("private, max-age=%d", seconds-skew)
}

// constrainedPutPresigner is an optional S3 capability used by direct
// uploads. Signing Content-Length prevents a presigned URL declared for a
// small upload from being used to store an arbitrarily large object.
type constrainedPutPresigner interface {
	PresignPutConstrained(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, map[string]string, error)
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
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("backend identity unavailable")
	}
	identity, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || identity == nil {
		return nil, fmt.Errorf("backend identity unavailable: %v", err)
	}
	bound := ctx.IntegrationFor(s3IntegrationRole)
	if bound == nil {
		if identity.Bindings[s3IntegrationRole] != nil {
			return nil, errors.New("backend binding is unavailable or invalid")
		}
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
