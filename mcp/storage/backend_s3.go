package main

// S3-compatible backend. Uses minio-go because it speaks vanilla S3
// SigV4 against AWS, Cloudflare R2, Backblaze B2, Wasabi, MinIO, and
// any other compatible service. The choice is opaque to callers —
// this file is the only place that touches S3 SDK types.
//
// Config (from config_schema, surfaced via ctx.Config):
//
//	backend                = s3
//	s3_endpoint            = host[:port]   (no scheme; minio-go derives that from secure flag)
//	s3_region              = us-east-1     (default; some providers require their own value)
//	s3_bucket              = my-bucket
//	s3_access_key
//	s3_secret_key
//	s3_use_ssl             = "true" / "false" (default true)
//	s3_force_path_style    = "true" / "false" (default false; set true for MinIO)
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

// newS3Backend resolves the install config and initialises a minio
// client. Returns an error rather than panicking — OnMount logs and
// surfaces it so a misconfigured install fails loud.
func newS3Backend(ctx *sdk.AppCtx) (*s3Backend, error) {
	cfg := ctx.Config()
	endpoint := strings.TrimSpace(cfg.Get("s3_endpoint"))
	if endpoint == "" {
		return nil, errors.New("s3 backend: s3_endpoint required (e.g. s3.amazonaws.com)")
	}
	bucket := strings.TrimSpace(cfg.Get("s3_bucket"))
	if bucket == "" {
		return nil, errors.New("s3 backend: s3_bucket required")
	}
	access := strings.TrimSpace(cfg.Get("s3_access_key"))
	secret := strings.TrimSpace(cfg.Get("s3_secret_key"))
	if access == "" || secret == "" {
		return nil, errors.New("s3 backend: s3_access_key + s3_secret_key required")
	}
	region := strings.TrimSpace(cfg.Get("s3_region"))
	if region == "" {
		region = "us-east-1"
	}

	useSSL := configBool(cfg.Get("s3_use_ssl"), true)

	// Strip an accidental scheme — minio-go expects "host" not "https://host".
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimRight(endpoint, "/")

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: useSSL,
		Region: region,
		// BucketLookup: explicit when force_path_style=true; some
		// providers (MinIO, custom Ceph, R2 sometimes) need path
		// style instead of vhost style.
		BucketLookup: func() minio.BucketLookupType {
			if configBool(cfg.Get("s3_force_path_style"), false) {
				return minio.BucketLookupPath
			}
			return minio.BucketLookupAuto
		}(),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 backend: minio.New: %w", err)
	}
	return &s3Backend{client: client, bucket: bucket, region: region}, nil
}

func (s *s3Backend) Kind() string { return "s3" }

func (s *s3Backend) Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	opts := minio.PutObjectOptions{}
	if contentType != "" {
		opts.ContentType = contentType
	}
	// minio-go needs a known size for non-multipart uploads; -1 falls
	// back to multipart with PartSize hints. saveBytes always knows
	// size, so we expect size>0 here.
	if size <= 0 {
		size = -1
		opts.PartSize = 16 * 1024 * 1024
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
