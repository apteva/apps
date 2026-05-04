package main

// Destination — pluggable backup target.
//
// Each Destination knows how to:
//   - Put bytes (the snapshot tar.gz) and return a remote_key + size
//   - Get bytes back by remote_key (for restore)
//   - List existing keys and Delete one (for retention pruning)
//
// Two impls in v0.1:
//   - local: writes under a host directory; the simplest possible backup
//   - s3:    any S3-compatible bucket (AWS, R2, B2, Wasabi, MinIO).
//            Credentials come from the platform's connections store via
//            the SDK's GetConnection — bytes never touch backup.db.
//
// A storage_app destination is reserved for v0.2 (calls files_upload
// on the storage app); the manifest already declares storage as an
// optional dep so installing it later doesn't require a backup
// reinstall.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Destination_writer interface { // disambiguates from the DB row type
	Put(ctx context.Context, key string, body io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	List(ctx context.Context) ([]storedObject, error)
	Delete(ctx context.Context, key string) error
}

type storedObject struct {
	Key      string
	Size     int64
	Modified time.Time
}

const (
	kindLocal      = "local"
	kindS3         = "s3"
	kindStorageApp = "storage_app"
)

// validateDestination is run before insert. We don't try to *connect*
// (S3 endpoints can be flaky and we'd rather surface the error in the
// first run row), but we do enforce the per-kind schema so later code
// can trust the JSON layout.
func validateDestination(d *Destination) error {
	if d.Name == "" {
		return errors.New("name required")
	}
	if d.Kind == "" {
		return errors.New("kind required")
	}
	switch d.Kind {
	case kindLocal:
		var c localConfig
		if err := json.Unmarshal(d.Config, &c); err != nil {
			return fmt.Errorf("local config: %w", err)
		}
		if c.Path == "" {
			return errors.New("local destination requires {\"path\": \"/some/dir\"}")
		}
		if !filepath.IsAbs(c.Path) {
			return errors.New("local path must be absolute")
		}
	case kindS3:
		var c s3Config
		if err := json.Unmarshal(d.Config, &c); err != nil {
			return fmt.Errorf("s3 config: %w", err)
		}
		if c.Bucket == "" {
			return errors.New("s3 destination requires {\"bucket\": ...}")
		}
		// Endpoint may be empty for AWS; required for R2/B2/MinIO.
		// We only enforce the bucket here; the runner surfaces auth /
		// endpoint mistakes during the first Put.
		if d.ConnectionID == 0 {
			return errors.New("s3 destination requires connection_id pointing at credentials in the platform connections store")
		}
	case kindStorageApp:
		return errors.New("storage_app destination is reserved for v0.2")
	default:
		return fmt.Errorf("unknown destination kind %q", d.Kind)
	}
	return nil
}

// openDestination instantiates a writer for the row. The runner
// re-opens for each backup run rather than caching — destinations are
// rare-use and the cost of a fresh client is negligible.
func openDestination(d *Destination, getConn func(int64) (*credentials.Credentials, *s3Endpoint, error)) (Destination_writer, error) {
	switch d.Kind {
	case kindLocal:
		var c localConfig
		if err := json.Unmarshal(d.Config, &c); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(c.Path, 0o755); err != nil {
			return nil, fmt.Errorf("local dest mkdir %s: %w", c.Path, err)
		}
		return &localDest{cfg: c}, nil
	case kindS3:
		var c s3Config
		if err := json.Unmarshal(d.Config, &c); err != nil {
			return nil, err
		}
		creds, endpoint, err := getConn(d.ConnectionID)
		if err != nil {
			return nil, fmt.Errorf("s3 dest credentials: %w", err)
		}
		if endpoint != nil {
			if c.Endpoint == "" {
				c.Endpoint = endpoint.URL
			}
			if c.Region == "" {
				c.Region = endpoint.Region
			}
		}
		// Default to AWS S3 when the connection didn't pin an endpoint.
		host := c.Endpoint
		secure := true
		if host == "" {
			host = "s3.amazonaws.com"
		}
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		client, err := minio.New(host, &minio.Options{
			Creds:  creds,
			Secure: secure,
			Region: c.Region,
		})
		if err != nil {
			return nil, fmt.Errorf("s3 client: %w", err)
		}
		return &s3Dest{cfg: c, client: client}, nil
	default:
		return nil, fmt.Errorf("unsupported destination kind %q", d.Kind)
	}
}

// ─── local ──────────────────────────────────────────────────────────

type localConfig struct {
	Path string `json:"path"`
}

type localDest struct {
	cfg localConfig
}

func (d *localDest) Put(_ context.Context, key string, body io.Reader, _ int64) error {
	dst := filepath.Join(d.cfg.Path, key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func (d *localDest) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(d.cfg.Path, key))
}

func (d *localDest) List(_ context.Context) ([]storedObject, error) {
	out := []storedObject{}
	root := d.cfg.Path
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, storedObject{Key: rel, Size: info.Size(), Modified: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

func (d *localDest) Delete(_ context.Context, key string) error {
	err := os.Remove(filepath.Join(d.cfg.Path, key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ─── s3 ─────────────────────────────────────────────────────────────

type s3Config struct {
	Bucket    string `json:"bucket"`
	Region    string `json:"region,omitempty"`     // "us-east-1" by default
	Endpoint  string `json:"endpoint,omitempty"`   // "s3.amazonaws.com" by default; required for R2/B2/MinIO
	KeyPrefix string `json:"key_prefix,omitempty"` // "prod/" → all keys land under prod/
}

type s3Dest struct {
	cfg    s3Config
	client *minio.Client
}

// s3Endpoint is the parsed shape of a connection's S3-compatible
// metadata. The runner builds this from the connection's config_json.
type s3Endpoint struct {
	URL    string
	Region string
}

func (d *s3Dest) prefixedKey(key string) string {
	if d.cfg.KeyPrefix == "" {
		return key
	}
	return strings.TrimSuffix(d.cfg.KeyPrefix, "/") + "/" + key
}

func (d *s3Dest) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	_, err := d.client.PutObject(ctx, d.cfg.Bucket, d.prefixedKey(key), body, size,
		minio.PutObjectOptions{ContentType: "application/gzip"})
	return err
}

func (d *s3Dest) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := d.client.GetObject(ctx, d.cfg.Bucket, d.prefixedKey(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// Force a stat so callers get an error here rather than mid-stream
	// when the key is missing.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, err
	}
	return obj, nil
}

func (d *s3Dest) List(ctx context.Context) ([]storedObject, error) {
	out := []storedObject{}
	for info := range d.client.ListObjects(ctx, d.cfg.Bucket, minio.ListObjectsOptions{
		Prefix: d.cfg.KeyPrefix, Recursive: true,
	}) {
		if info.Err != nil {
			return nil, info.Err
		}
		key := info.Key
		key = strings.TrimPrefix(key, strings.TrimSuffix(d.cfg.KeyPrefix, "/")+"/")
		out = append(out, storedObject{Key: key, Size: info.Size, Modified: info.LastModified})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

func (d *s3Dest) Delete(ctx context.Context, key string) error {
	return d.client.RemoveObject(ctx, d.cfg.Bucket, d.prefixedKey(key), minio.RemoveObjectOptions{})
}

// ─── connection → credentials adapter ───────────────────────────────
//
// The runner passes a closure into openDestination so we don't have a
// hard dependency on the platform SDK shape in the destination layer
// (eases unit testing). The adapter below is the "real" implementation
// the runner uses; tests substitute a static credential.

func staticCredentialsAdapter(accessKey, secretKey, sessionToken string) func(int64) (*credentials.Credentials, *s3Endpoint, error) {
	return func(_ int64) (*credentials.Credentials, *s3Endpoint, error) {
		return credentials.NewStaticV4(accessKey, secretKey, sessionToken), nil, nil
	}
}
