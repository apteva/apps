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
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type s3Backend struct {
	client *minio.Client
	bucket string
	region string
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
			return minio.BucketLookupAuto
		}(),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 backend: minio.New: %w", err)
	}
	return &s3Backend{client: client, bucket: bucket, region: resolved.region}, nil
}

// s3ResolvedConnection is the post-slug-resolution form of a bound
// connection — same shape regardless of which provider the operator
// picked.
type s3ResolvedConnection struct {
	endpoint       string
	region         string
	accessKey      string
	secretKey      string
	useSSL         bool
	forcePathStyle bool
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
		// nbg1 (Nuremberg, DE), hel1 (Helsinki, FI). The catalog
		// presents these as a select + sets a default of nbg1, but
		// we still validate here in case an operator hand-edits the
		// connection JSON. SigV4 region naming: Hetzner doesn't care
		// what we sign with, but minio-go demands a non-empty value
		// — pass through whatever's in the credential.
		if out.region == "" {
			return nil, fmt.Errorf("s3 backend: hetzner-object-storage connection %d has no region (fsn1/nbg1/hel1)", creds.ConnectionID)
		}
		out.endpoint = out.region + ".your-objectstorage.com"
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
	//   PartSize  — 32 MB. Larger parts mean fewer round trips, more
	//               throughput per concurrent upload, and lower
	//               total latency on R2 / S3 over residential links.
	//               minio-go's default of ~16 MB is conservative for
	//               small objects; storage handles uploads up to GBs
	//               so 32 MB pays for itself the moment we cross the
	//               5-MB single-PUT threshold. R2's max part size is
	//               5 GB, AWS S3 is 5 GB — well within the cap.
	//
	//   NumThreads — 8 concurrent part uploads. Default 4 leaves
	//               throughput on the table on anything faster than
	//               ~50 Mbps. 8 saturates a typical residential up
	//               without overshooting the connection budget.
	//
	// These are hints — minio-go falls back to a single PUT below
	// the multipart threshold (5 MB) and won't spawn unnecessary
	// goroutines for tiny payloads.
	opts.PartSize = 32 * 1024 * 1024
	opts.NumThreads = 8
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
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		// minio-go returns a typed error — map "NoSuchKey" / 404 to
		// our generic ErrNotFound so callers can decide.
		errResp := minio.ToErrorResponse(err)
		if errResp.StatusCode == 404 || errResp.Code == "NoSuchKey" {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("s3 stat %s: %w", key, err)
	}
	return info.Size, nil
}

// LocalPath always returns ("", false) for s3 — callers MUST switch
// to the presigned-redirect path.
func (s *s3Backend) LocalPath(_ string) (string, bool) { return "", false }

func (s *s3Backend) PresignGet(ctx context.Context, key, filename, contentType string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}
	if ttl > 7*24*time.Hour {
		// SigV4 cap.
		ttl = 7 * 24 * time.Hour
	}
	reqParams := url.Values{}
	if filename != "" {
		// %22 quoting handled by Set; UA gets a sensible Save-As name.
		reqParams.Set("response-content-disposition",
			`attachment; filename="`+sanitiseFilename(filename)+`"`)
	}
	if contentType != "" {
		reqParams.Set("response-content-type", contentType)
	}
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
