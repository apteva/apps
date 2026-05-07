package main

// Destination — pluggable backup target.
//
// Each Destination knows how to:
//   - Put bytes (the snapshot tar.gz) and return a remote_key + size
//   - Get bytes back by remote_key (for restore)
//   - List existing keys and Delete one (for retention pruning)
//
// Two impls in v0.2:
//   - local: writes under a host directory; the simplest possible backup.
//   - s3:    any S3-compatible bucket. Credentials never touch this app —
//            we use ctx.PlatformAPI().ExecuteIntegrationTool against the
//            install's bound `cloud_storage` integration (aws-s3,
//            cloudflare-r2, …). The runner handles SigV4 signing.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
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
		// Credentials, endpoint, region all live on the bound
		// cloud_storage integration in v0.2 — no per-destination
		// connection_id and no access_key fields. The bind happens at
		// install time via the install dialog's RolePicker.
	case kindStorageApp:
		return errors.New("storage_app destination is reserved for v0.3")
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
			return nil, errors.New("s3 destination requires a cloud_storage connection — bind one in the app's settings (R2, S3, …)")
		}
		return &cloudDest{cfg: c, ctx: ctx, bound: bound}, nil
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

// ─── cloud (s3-compatible via integration) ─────────────────────────

type s3Config struct {
	Bucket    string `json:"bucket"`
	KeyPrefix string `json:"key_prefix,omitempty"` // "prod/" → all keys land under prod/
}

type cloudDest struct {
	cfg   s3Config
	ctx   *sdk.AppCtx
	bound *sdk.BoundIntegration
}

func (d *cloudDest) prefixedKey(key string) string {
	if d.cfg.KeyPrefix == "" {
		return key
	}
	return strings.TrimSuffix(d.cfg.KeyPrefix, "/") + "/" + key
}

// callTool routes a logical capability through the bound integration's
// configured tool name, passing the input through ExecuteIntegrationTool.
// Returns the parsed result envelope or an actionable error.
func (d *cloudDest) callTool(capability string, input map[string]any) (*sdk.ExecuteResult, error) {
	tool := d.bound.ToolFor(capability)
	res, err := d.ctx.PlatformAPI().ExecuteIntegrationTool(d.bound.ConnectionID, tool, input)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		status := 0
		if res != nil {
			status = res.Status
		}
		return nil, fmt.Errorf("%s: %d %s", tool, status, strings.TrimSpace(body))
	}
	return res, nil
}

func (d *cloudDest) Put(_ context.Context, key string, body io.Reader, _ int64) error {
	// Read the full body into memory. Snapshots are typically small
	// (<<100MB on real instances; the tar.gz is the platform DB +
	// per-app DBs, which are SQLite and compress well). For very large
	// instances we'd want a streaming put — punt to v0.3.
	bytes, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	_, err = d.callTool("object.put", map[string]any{
		"bucket": d.cfg.Bucket,
		"key":    d.prefixedKey(key),
		"body":   bytes,
	})
	return err
}

func (d *cloudDest) Get(_ context.Context, key string) (io.ReadCloser, error) {
	res, err := d.callTool("object.get", map[string]any{
		"bucket": d.cfg.Bucket,
		"key":    d.prefixedKey(key),
	})
	if err != nil {
		return nil, err
	}
	// The runner returns the upstream body in res.Data; for binary
	// responses it's the raw object bytes. JSON-decoding is *not* run
	// for non-JSON content types, so res.Data is what S3 sent us.
	return io.NopCloser(strings.NewReader(string(res.Data))), nil
}

// listBucketResult mirrors the S3 ListObjectsV2 XML response. Only the
// fields backup needs.
type listBucketResult struct {
	XMLName               xml.Name           `xml:"ListBucketResult"`
	IsTruncated           bool               `xml:"IsTruncated"`
	NextContinuationToken string             `xml:"NextContinuationToken"`
	Contents              []listBucketObject `xml:"Contents"`
}

type listBucketObject struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	LastModified time.Time `xml:"LastModified"`
}

func (d *cloudDest) List(_ context.Context) ([]storedObject, error) {
	out := []storedObject{}
	cursor := ""
	for {
		input := map[string]any{
			"bucket":    d.cfg.Bucket,
			"list-type": 2,
		}
		if d.cfg.KeyPrefix != "" {
			input["prefix"] = d.cfg.KeyPrefix
		}
		if cursor != "" {
			input["continuation-token"] = cursor
		}
		res, err := d.callTool("object.list", input)
		if err != nil {
			return nil, err
		}
		var lbr listBucketResult
		if err := xml.Unmarshal(res.Data, &lbr); err != nil {
			return nil, fmt.Errorf("parse list_objects xml: %w", err)
		}
		for _, c := range lbr.Contents {
			key := c.Key
			if d.cfg.KeyPrefix != "" {
				key = strings.TrimPrefix(key, strings.TrimSuffix(d.cfg.KeyPrefix, "/")+"/")
			}
			out = append(out, storedObject{Key: key, Size: c.Size, Modified: c.LastModified})
		}
		if !lbr.IsTruncated || lbr.NextContinuationToken == "" {
			break
		}
		cursor = lbr.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

func (d *cloudDest) Delete(_ context.Context, key string) error {
	_, err := d.callTool("object.delete", map[string]any{
		"bucket": d.cfg.Bucket,
		"key":    d.prefixedKey(key),
	})
	return err
}
