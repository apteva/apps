package main

// Destination — pluggable backup target.
//
// Each Destination knows how to:
//   - Put bytes (the snapshot tar.gz) and return a remote_key + size
//   - Get bytes back by remote_key (for restore)
//   - List existing keys and Delete one (for retention pruning)
//
// Two implementations:
//   - local: writes under a host directory; the simplest possible backup.
//   - s3: AWS S3 and Cloudflare R2. Credentials are read on demand through
//     the SDK's restricted connection credential API and never stored here.

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

	sdk "github.com/apteva/app-sdk"
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
		// Empty path is allowed: the runner fills in a default rooted
		// at the install's writable data dir (`<DataDir>/backups`).
		if c.Path != "" && !filepath.IsAbs(c.Path) {
			return errors.New("local path must be absolute (or leave blank for the default under the install's data dir)")
		}
	case kindS3:
		var c s3Config
		if err := json.Unmarshal(d.Config, &c); err != nil {
			return fmt.Errorf("s3 config: %w", err)
		}
		if c.Bucket == "" {
			return errors.New("s3 destination requires {\"bucket\": ...}")
		}
		// Credentials, endpoint, and region live on the bound
		// cloud_storage integration. The row records the binding ID so a
		// later global rebind cannot silently redirect existing backups.
	case kindStorageApp:
		return errors.New("storage_app destinations are not supported")
	default:
		return fmt.Errorf("unknown destination kind %q", d.Kind)
	}
	return nil
}

// openDestination instantiates a writer for the row. Cloud (kindS3)
// destinations resolve credentials via the bound cloud_storage role on
// the install — none of the operator's S3 keys ever land in backup's
// own DB.
func openDestination(d *Destination, ctx *sdk.AppCtx, defaultLocalDir string) (Destination_writer, error) {
	switch d.Kind {
	case kindLocal:
		var c localConfig
		if err := json.Unmarshal(d.Config, &c); err != nil {
			return nil, err
		}
		if c.Path == "" {
			if defaultLocalDir == "" {
				return nil, errors.New("local destination has no path and no default available")
			}
			c.Path = defaultLocalDir
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
		bound := ctx.IntegrationFor("cloud_storage")
		if bound == nil {
			return nil, errors.New("s3 destination has no cloud storage connection bound")
		}
		if d.ConnectionID != 0 && d.ConnectionID != bound.ConnectionID {
			return nil, fmt.Errorf("s3 destination %q uses connection %d, but cloud_storage is currently bound to connection %d", d.Name, d.ConnectionID, bound.ConnectionID)
		}
		return newCloudDest(ctx, bound, c)
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

func (d *localDest) pathForKey(key string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(key))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid backup object key %q", key)
	}
	return filepath.Join(d.cfg.Path, clean), nil
}

func (d *localDest) Put(_ context.Context, key string, body io.Reader, _ int64) error {
	dst, err := d.pathForKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	committed = true
	return nil
}

func (d *localDest) Get(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := d.pathForKey(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (d *localDest) List(_ context.Context) ([]storedObject, error) {
	out := []storedObject{}
	root := d.cfg.Path
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
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
	path, err := d.pathForKey(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ─── cloud (s3-compatible via integration) ─────────────────────────

type s3Config struct {
	Bucket    string `json:"bucket"`
	KeyPrefix string `json:"key_prefix,omitempty"` // "prod/" → all keys land under prod/
}

type cloudDest struct {
	cfg    s3Config
	client *minio.Client
	region string
}

func (d *cloudDest) prefixedKey(key string) string {
	if d.cfg.KeyPrefix == "" {
		return key
	}
	return strings.TrimSuffix(d.cfg.KeyPrefix, "/") + "/" + key
}

func newCloudDest(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, cfg s3Config) (*cloudDest, error) {
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("read cloud storage credentials: %w", err)
	}
	access := strings.TrimSpace(creds.Fields["access_key_id"])
	secret := strings.TrimSpace(creds.Fields["secret_access_key"])
	if access == "" || secret == "" {
		return nil, errors.New("cloud storage connection is missing access_key_id or secret_access_key")
	}
	region := strings.TrimSpace(creds.Fields["region"])
	endpoint := ""
	lookup := minio.BucketLookupAuto
	switch creds.Slug {
	case "aws-s3":
		if region == "" {
			region = "us-east-1"
		}
		endpoint = "s3." + region + ".amazonaws.com"
	case "cloudflare-r2":
		accountID := strings.TrimSpace(creds.Fields["account_id"])
		if accountID == "" {
			return nil, errors.New("Cloudflare R2 connection is missing account_id")
		}
		if region == "" {
			region = "auto"
		}
		endpoint = accountID + ".r2.cloudflarestorage.com"
		lookup = minio.BucketLookupDNS
	default:
		return nil, fmt.Errorf("unsupported cloud storage connection %q", creds.Slug)
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, strings.TrimSpace(creds.Fields["session_token"])),
		Secure: true, Region: region, BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return &cloudDest{cfg: cfg, client: client, region: region}, nil
}

func (d *cloudDest) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if size <= 0 {
		size = -1
	}
	_, err := d.client.PutObject(ctx, d.cfg.Bucket, d.prefixedKey(key), body, size, minio.PutObjectOptions{
		ContentType: "application/octet-stream", PartSize: 16 << 20, NumThreads: 4,
	})
	if err != nil {
		return fmt.Errorf("S3 put %s: %w", key, err)
	}
	return nil
}

func (d *cloudDest) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := d.client.GetObject(ctx, d.cfg.Bucket, d.prefixedKey(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("S3 get %s: %w", key, err)
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, fmt.Errorf("S3 stat %s: %w", key, err)
	}
	return object, nil
}

func (d *cloudDest) List(ctx context.Context) ([]storedObject, error) {
	out := []storedObject{}
	prefix := strings.TrimSuffix(d.cfg.KeyPrefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	for object := range d.client.ListObjects(ctx, d.cfg.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return nil, fmt.Errorf("S3 list: %w", object.Err)
		}
		key := strings.TrimPrefix(object.Key, prefix)
		out = append(out, storedObject{Key: key, Size: object.Size, Modified: object.LastModified})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

func (d *cloudDest) Delete(ctx context.Context, key string) error {
	if err := d.client.RemoveObject(ctx, d.cfg.Bucket, d.prefixedKey(key), minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("S3 delete %s: %w", key, err)
	}
	return nil
}
