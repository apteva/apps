package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type sourceReceipt struct {
	SnapshotID     string `json:"snapshot_id"`
	SourceRevision string `json:"source_revision"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Format         string `json:"format"`
	ExpiresAt      string `json:"expires_at"`
	ZipB64         string `json:"zip_b64,omitempty"`
	ProjectID      string `json:"project_id"`
	Repository     string `json:"repository"`
	Subdir         string `json:"subdir,omitempty"`
}
type sourceSelection struct {
	SnapshotID       string `json:"snapshot_id"`
	Subdir           string `json:"subdir"`
	MaxBytes         int64  `json:"max_bytes"`
	MaxExpandedBytes int64  `json:"max_expanded_bytes"`
}

func (f *codeFetcher) fetchSnapshot(d *Deployment, dest string) error {
	if f.platform == nil || d.SourceRef == "" {
		return errors.New("Code source requires a repository and platform binding")
	}
	ctx := f.config.Context
	if ctx == nil {
		ctx = context.Background()
	}
	var sel sourceSelection
	if err := json.Unmarshal([]byte(defaultStr(d.SourceExtraJSON, "{}")), &sel); err != nil {
		return err
	}
	if sel.MaxBytes == 0 {
		sel.MaxBytes = sourceTransferLimit()
	}
	if sel.MaxExpandedBytes == 0 {
		sel.MaxExpandedBytes = sourceExpandedLimit()
	}
	if sel.MaxBytes < 1 || sel.MaxExpandedBytes < 1 || sel.MaxBytes > sourceTransferLimit() || sel.MaxExpandedBytes > sourceExpandedLimit() {
		return errors.New("source budgets must be positive and within operator limits")
	}
	cache := f.config.CacheDir
	if cache == "" {
		var err error
		cache, err = os.MkdirTemp("", "deploy-source-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(cache)
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	receiptPath := filepath.Join(cache, "source-receipt.json")
	archivePath := filepath.Join(cache, "source-snapshot.zip")
	var receipt sourceReceipt
	raw, err := os.ReadFile(receiptPath)
	if err == nil {
		if err = json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		if receipt.ProjectID != d.ProjectID || receipt.Repository != d.SourceRef || receipt.Subdir != sel.Subdir || (sel.SnapshotID != "" && sel.SnapshotID != receipt.SnapshotID) {
			return errors.New("cached snapshot belongs to different source selection")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		args := map[string]any{"slug": d.SourceRef, "_project_id": d.ProjectID, "metadata_only": true}
		if sel.SnapshotID != "" {
			args["snapshot_id"] = sel.SnapshotID
		} else if sel.Subdir != "" {
			args["subdir"] = sel.Subdir
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = f.platform.CallAppResult("code", "repos_export", args, &receipt); err != nil {
			return err
		}
		if sel.SnapshotID != "" && receipt.SnapshotID != sel.SnapshotID {
			return errors.New("Code returned a different pinned snapshot")
		}
		// Small exports from older Code remain supported, but cannot claim pinned provenance.
		if receipt.SnapshotID == "" && receipt.ZipB64 != "" {
			data, e := base64.StdEncoding.DecodeString(receipt.ZipB64)
			if e != nil {
				return e
			}
			if int64(len(data)) > sel.MaxBytes {
				return errors.New("source exceeds compressed budget")
			}
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			if receipt.SHA256 != "" && receipt.SHA256 != digest {
				return errors.New("source checksum mismatch")
			}
			receipt.SHA256 = digest
			receipt.Size = int64(len(data))
			receipt.Format = "zip-v1"
			if e = os.WriteFile(archivePath, data, 0600); e != nil {
				return e
			}
		}
		receipt.ProjectID = d.ProjectID
		receipt.Repository = d.SourceRef
		receipt.Subdir = sel.Subdir
		receipt.ZipB64 = ""
		if err = validateSourceReceipt(receipt, sel.MaxBytes); err != nil {
			return err
		}
		if err = atomicJSONFile(receiptPath, receipt); err != nil {
			return err
		}
	} else {
		return err
	}
	if err = validateSourceReceipt(receipt, sel.MaxBytes); err != nil {
		return err
	}
	if !verifiedSourceFile(archivePath, receipt) {
		if receipt.SnapshotID == "" {
			return errors.New("legacy source unavailable; create a new build")
		}
		expiry, e := time.Parse(time.RFC3339, receipt.ExpiresAt)
		if e != nil || time.Now().After(expiry) {
			return errors.New("pinned source snapshot expired; create a new build explicitly")
		}
		partial := archivePath + ".partial"
		out, e := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		defer out.Close()
		info, e := out.Stat()
		if e != nil {
			return e
		}
		offset := info.Size()
		if offset > receipt.Size {
			return errors.New("partial snapshot exceeds receipt size")
		}
		if _, e = out.Seek(offset, io.SeekStart); e != nil {
			return e
		}
		for offset < receipt.Size {
			if e = ctx.Err(); e != nil {
				return e
			}
			var chunk struct {
				SnapshotID string `json:"snapshot_id"`
				SHA256     string `json:"sha256"`
				Size       int64  `json:"size"`
				Offset     int64  `json:"offset"`
				Next       int64  `json:"next_offset"`
				EOF        bool   `json:"eof"`
				Data       string `json:"data_b64"`
			}
			limit := min(int64(1<<20), receipt.Size-offset)
			e = f.platform.CallAppResult("code", "repos_snapshot_read", map[string]any{"_project_id": d.ProjectID, "slug": d.SourceRef, "snapshot_id": receipt.SnapshotID, "offset": offset, "limit": limit}, &chunk)
			if e != nil {
				return fmt.Errorf("snapshot read at %d: %w", offset, e)
			}
			if len(chunk.Data) > base64.StdEncoding.EncodedLen(int(limit)) {
				return errors.New("oversized snapshot chunk")
			}
			data, e := base64.StdEncoding.DecodeString(chunk.Data)
			if e != nil {
				return e
			}
			if chunk.SnapshotID != receipt.SnapshotID || chunk.SHA256 != receipt.SHA256 || chunk.Size != receipt.Size || chunk.Offset != offset || len(data) == 0 || int64(len(data)) > limit || chunk.Next != offset+int64(len(data)) || chunk.EOF != (chunk.Next == receipt.Size) {
				return errors.New("snapshot chunk identity or offset mismatch")
			}
			if _, e = out.Write(data); e != nil {
				return e
			}
			if chunk.Next%(8<<20) == 0 || chunk.EOF {
				if e = out.Sync(); e != nil {
					return e
				}
			}
			offset = chunk.Next
		}
		if e = out.Close(); e != nil {
			return e
		}
		if !verifiedSourceFile(partial, receipt) {
			os.Remove(partial)
			return errors.New("snapshot final size or checksum mismatch")
		}
		if e = os.Rename(partial, archivePath); e != nil {
			return e
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	z, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer z.Close()
	return extractBoundedZip(&z.Reader, dest, sel.MaxExpandedBytes, true)
}
func validateSourceReceipt(r sourceReceipt, maxBytes int64) error {
	digest, e := hex.DecodeString(r.SHA256)
	if e != nil || len(digest) != 32 || r.Size < 1 || r.Size > maxBytes || r.Format != "zip-v1" {
		return errors.New("invalid source snapshot receipt or source exceeds budget")
	}
	if r.SnapshotID != "" && (r.SnapshotID != r.SHA256 || r.SourceRevision != "sha256:"+r.SHA256) {
		return errors.New("source snapshot identity mismatch")
	}
	return nil
}
func verifiedSourceFile(path string, r sourceReceipt) bool {
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() != r.Size {
		return false
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, r.Size+1))
	return e == nil && n == r.Size && hex.EncodeToString(h.Sum(nil)) == r.SHA256
}
func atomicJSONFile(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".metadata-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, e = tmp.Write(b); e != nil {
		return e
	}
	if e = tmp.Sync(); e != nil {
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	return os.Rename(tmp.Name(), path)
}
