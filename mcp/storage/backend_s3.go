package main

// S3-compatible backend. Uses minio-go because it speaks vanilla S3
// SigV4 against AWS, Cloudflare R2, Backblaze B2, Wasabi, MinIO, and
// any other compatible service. The choice is opaque to callers —
// this file is the only place that touches S3 SDK types.
//
// v0.9 model: credentials come from a bound integration, NOT
// config_schema. The operator picks an aws-s3 / cloudflare-r2 /
// backblaze-b2 connection at install time; this file reads
// connection.Fields via PlatformAPI().GetConnectionCredentials and
// resolves slug-specific endpoint construction.
//
// Per-slug resolution rules (the only slug-aware code in storage):
//
//   cloudflare-r2:  endpoint = "<account_id>.r2.cloudflarestorage.com"
//                   region   = "auto"
//                   path-style = false
//   aws-s3:         endpoint = "s3.<region>.amazonaws.com"
//                   region   = catalog field (default "us-east-1")
//                   path-style = false
//   backblaze-b2:   endpoint = "s3.<region>.backblazeb2.com"
//                   region   = catalog field
//                   path-style = false
//
// The bucket name lives in install config (`s3_bucket`) — one R2
// account commonly hosts many buckets, so it's per-install state, not
// per-connection.
//
// Key semantics: s3 object key == objectKey(sha256, storage_key) ==
// "<2hex>/<storage_key>". Buckets stay flat-ish (256 prefixes) which
// keeps listings cheap.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type s3Backend struct {
	client        *minio.Client
	bucket        string
	region        string
	partSize      uint64
	uploadThreads uint
}

// newS3Backend reads the bound connection's credentials, resolves the
// slug-specific endpoint, and initialises a minio client. Returns an
// error rather than panicking — OnMount logs + surfaces it so a
// misconfigured install fails loud.
func newS3Backend(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, bucket string) (*s3Backend, error) {
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("s3 backend: read credentials for connection %d: %w", bound.ConnectionID, err)
	}
	resolved, err := resolveS3Connection(creds)
	if err != nil {
		return nil, err
	}

	client, err := minio.New(resolved.endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(resolved.accessKey, resolved.secretKey, ""),
		Secure: resolved.useSSL,
		Region: resolved.region,
		BucketLookup: func() minio.BucketLookupType {
			if resolved.forcePathStyle {
				return minio.BucketLookupPath
			}
			if resolved.forceVirtualHost {
				// minio-go's BucketLookupAuto falls back to path-style
				// for non-AWS hostnames. That breaks providers whose
				// canonical URL is <bucket>.<endpoint> (Hetzner Object
				// Storage in particular) — the SigV4 signature mismatch
				// surfaces as Access Denied. Force DNS/virtual-host
				// here so the URL + signature both line up.
				return minio.BucketLookupDNS
			}
			return minio.BucketLookupAuto
		}(),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 backend: minio.New: %w", err)
	}
	partSizeMB := configUintClamped(ctx.Config().Get("s3_part_size_mb"), 16, 5, 128)
	uploadThreads := configIntClamped(ctx.Config().Get("s3_upload_concurrency"), 4, 1, 8)
	return &s3Backend{
		client: client, bucket: bucket, region: resolved.region,
		partSize: partSizeMB * 1024 * 1024, uploadThreads: uint(uploadThreads),
	}, nil
}

// s3ResolvedConnection is the post-slug-resolution form of a bound
// connection — same shape regardless of which provider the operator
// picked.
type s3ResolvedConnection struct {
	endpoint         string
	region           string
	accessKey        string
	secretKey        string
	useSSL           bool
	forcePathStyle   bool
	forceVirtualHost bool // pin BucketLookupDNS for providers whose canonical URL is <bucket>.<endpoint>
}

// resolveS3Connection turns a ConnectionCredentials bundle (slug +
// catalog credential_fields) into the wire-level config minio-go
// expects. Slug-aware: this is the only place storage knows that R2
// uses <account_id>.r2.cloudflarestorage.com vs AWS uses
// s3.<region>.amazonaws.com etc.
func resolveS3Connection(creds *sdk.ConnectionCredentials) (*s3ResolvedConnection, error) {
	if creds == nil {
		return nil, errors.New("s3 backend: nil credentials")
	}
	access := strings.TrimSpace(creds.Fields["access_key_id"])
	secret := strings.TrimSpace(creds.Fields["secret_access_key"])
	if access == "" || secret == "" {
		return nil, fmt.Errorf("s3 backend: connection %d (%s) is missing access_key_id / secret_access_key", creds.ConnectionID, creds.Slug)
	}
	region := strings.TrimSpace(creds.Fields["region"])

	out := &s3ResolvedConnection{
		accessKey:      access,
		secretKey:      secret,
		useSSL:         true,
		forcePathStyle: false,
		region:         region,
	}

	switch creds.Slug {
	case "cloudflare-r2":
		acct := strings.TrimSpace(creds.Fields["account_id"])
		if acct == "" {
			return nil, fmt.Errorf("s3 backend: cloudflare-r2 connection %d has no account_id", creds.ConnectionID)
		}
		out.endpoint = acct + ".r2.cloudflarestorage.com"
		if out.region == "" {
			out.region = "auto"
		}
	case "aws-s3":
		if out.region == "" {
			out.region = "us-east-1"
		}
		out.endpoint = "s3." + out.region + ".amazonaws.com"
	case "backblaze-b2":
		if out.region == "" {
			return nil, fmt.Errorf("s3 backend: backblaze-b2 connection %d has no region (e.g. us-west-004)", creds.ConnectionID)
		}
		out.endpoint = "s3." + out.region + ".backblazeb2.com"
	case "hetzner-object-storage":
		// Hetzner uses one endpoint per data centre at <region>.your-
		// objectstorage.com. Three regions: fsn1 (Falkenstein, DE),
		// nbg1 (Nuremberg, DE), hel1 (Helsinki, FI). Canonical URL
		// per their docs is <bucket>.<region>.your-objectstorage.com
		// — so force virtual-host (DNS) lookup; minio-go's
		// BucketLookupAuto would otherwise fall through to path
		// style for any non-AWS endpoint, which Hetzner rejects
		// with Access Denied (signature mismatch on host header).
		if out.region == "" {
			return nil, fmt.Errorf("s3 backend: hetzner-object-storage connection %d has no region (fsn1/nbg1/hel1)", creds.ConnectionID)
		}
		out.endpoint = out.region + ".your-objectstorage.com"
		out.forceVirtualHost = true
	default:
		// Generic S3-compatible (MinIO, Wasabi, custom Ceph). Catalog
		// must surface an "endpoint" credential field for these.
		ep := strings.TrimSpace(creds.Fields["endpoint"])
		if ep == "" {
			return nil, fmt.Errorf("s3 backend: unknown slug %q and connection has no 'endpoint' field", creds.Slug)
		}
		// Strip an accidental scheme — minio-go expects "host" not "https://host".
		ep = strings.TrimPrefix(ep, "https://")
		ep = strings.TrimPrefix(ep, "http://")
		ep = strings.TrimRight(ep, "/")
		out.endpoint = ep
		if out.region == "" {
			out.region = "us-east-1"
		}
		// Generic deployments (MinIO especially) commonly need
		// path-style. Read it from creds if present, else default true.
		out.forcePathStyle = configBool(creds.Fields["force_path_style"], true)
		out.useSSL = configBool(creds.Fields["use_ssl"], true)
	}
	return out, nil
}

func (s *s3Backend) Kind() string { return "s3" }

func (s *s3Backend) Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	opts := minio.PutObjectOptions{}
	if contentType != "" {
		opts.ContentType = contentType
	}
	// Tuning notes:
	//
	// Defaults (16 MiB × 4 workers) bound per-upload buffer/connection
	// pressure while retaining parallel multipart throughput. Operators
	// can tune both within conservative clamps via install config.
	opts.PartSize = s.partSize
	opts.NumThreads = s.uploadThreads
	// minio-go needs a known size for non-multipart uploads; -1 falls
	// back to multipart with PartSize hints.
	if size <= 0 {
		size = -1
	}
	if _, err := s.client.PutObject(ctx, s.bucket, key, r, size, opts); err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

func (s *s3Backend) Delete(ctx context.Context, key string) error {
	// RemoveObject is idempotent on missing keys — no special-casing.
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

func (s *s3Backend) Stat(ctx context.Context, key string) (int64, error) {
	info, err := s.HeadObject(ctx, key)
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

func (s *s3Backend) HeadObject(ctx context.Context, key string) (ObjectMetadata, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		// minio-go returns a typed error — map "NoSuchKey" / 404 to
		// our generic ErrNotFound so callers can decide.
		errResp := minio.ToErrorResponse(err)
		if errResp.StatusCode == 404 || errResp.Code == "NoSuchKey" {
			return ObjectMetadata{}, ErrNotFound
		}
		return ObjectMetadata{}, fmt.Errorf("s3 stat %s: %w", key, err)
	}
	return ObjectMetadata{
		Size: info.Size, ContentType: info.ContentType, ETag: info.ETag, LastModified: info.LastModified,
	}, nil
}

func (s *s3Backend) OpenObject(ctx context.Context, key string, options ObjectReadOptions) (*ObjectReadResult, error) {
	getOptions := minio.GetObjectOptions{}
	if options.Range != "" {
		getOptions.Set("Range", options.Range)
	}
	core := minio.Core{Client: s.client}
	body, info, headers, err := core.GetObject(ctx, s.bucket, key, getOptions)
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		switch {
		case errResp.StatusCode == http.StatusNotFound || errResp.Code == "NoSuchKey":
			return nil, ErrNotFound
		case errResp.StatusCode == http.StatusRequestedRangeNotSatisfiable || errResp.Code == "InvalidRange":
			return nil, ErrRangeNotSatisfiable
		default:
			return nil, fmt.Errorf("s3 get %s: %w", key, err)
		}
	}
	status := http.StatusOK
	contentRange := headers.Get("Content-Range")
	if contentRange != "" {
		status = http.StatusPartialContent
	}
	contentLength := info.Size
	if raw := headers.Get("Content-Length"); raw != "" {
		if parsed, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
			contentLength = parsed
		}
	}
	contentType := headers.Get("Content-Type")
	if contentType == "" {
		contentType = info.ContentType
	}
	etag := headers.Get("ETag")
	if etag == "" {
		etag = info.ETag
	}
	return &ObjectReadResult{
		Body: body, StatusCode: status, ContentLength: contentLength,
		ContentRange: contentRange, ContentType: contentType, ETag: etag,
		LastModified: info.LastModified,
	}, nil
}

// LocalPath always returns ("", false) for s3 — callers MUST switch
// to the presigned-redirect path.
func (s *s3Backend) LocalPath(_ string) (string, bool) { return "", false }

func (s *s3Backend) PresignGet(ctx context.Context, key string, options GetObjectOptions, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}
	if ttl > 7*24*time.Hour {
		// SigV4 cap.
		ttl = 7 * 24 * time.Hour
	}
	contentType := safeResponseContentType(options.ContentType)
	disposition := effectiveContentDisposition(options.Disposition, contentType)
	reqParams := url.Values{}
	reqParams.Set("response-content-disposition", contentDispositionHeader(disposition, options.Filename))
	reqParams.Set("response-content-type", contentType)
	reqParams.Set("response-cache-control", presignedResponseCacheControl(ttl))
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, reqParams)
	if err != nil {
		return "", fmt.Errorf("s3 presign get %s: %w", key, err)
	}
	return u.String(), nil
}

func (s *s3Backend) PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	// minio-go's PresignedPutObject doesn't take a content-type; if
	// the client wants to set one, they include it in the PUT request
	// header at upload time. We accept the param to keep parity with
	// PresignGet but use it only as a soft hint for now.
	_ = contentType
	u, err := s.client.PresignedPutObject(ctx, s.bucket, key, ttl)
	if err != nil {
		return "", fmt.Errorf("s3 presign put %s: %w", key, err)
	}
	return u.String(), nil
}

func (s *s3Backend) PresignPutConstrained(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, map[string]string, error) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	headers := http.Header{}
	headers.Set("Content-Length", strconv.FormatInt(size, 10))
	if contentType != "" {
		headers.Set("Content-Type", contentType)
	}
	u, err := s.client.PresignHeader(ctx, http.MethodPut, s.bucket, key, ttl, url.Values{}, headers)
	if err != nil {
		return "", nil, fmt.Errorf("s3 constrained presign put %s: %w", key, err)
	}
	// Browsers set Content-Length automatically and forbid JavaScript from
	// assigning it. Keep it in the signature, but don't tell clients to set
	// the forbidden header explicitly.
	outHeaders := map[string]string{}
	if contentType != "" {
		outHeaders["Content-Type"] = contentType
	}
	return u.String(), outHeaders, nil
}

// ─── small helpers ─────────────────────────────────────────────────

func configBool(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

func configIntClamped(raw string, def, min, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n == 0 {
		n = def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func configUintClamped(raw string, def, min, max uint64) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || n == 0 {
		n = def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// sanitiseFilename strips characters that would break a quoted
// Content-Disposition filename. Conservative — we're not trying to
// preserve every Unicode character, just keep the header valid.
func sanitiseFilename(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '"' || c == '\\' || c < 0x20 || c == 0x7f {
			out = append(out, '_')
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
