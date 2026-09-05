package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cache entries are immutable, scoped to the Storage binding + project, and
// addressed by the authoritative source digest. Active jobs hold hardlinks, so
// bounded cache eviction cannot remove their inputs. No digest => no reuse.
var sourceCacheLocks keyedGate
var sourceCachePruneMu sync.Mutex

type cachedSourceStamp struct {
	Size       int64
	Mtime      int64
	VerifiedAt time.Time
}
type renderSourcesKey struct{}
type localSourceCacheRootKey struct{}

func materializeLocalSource(ctx context.Context, app *sdk.AppCtx, sc *storageClient, project string, f *StorageFile, dst string) (bool, error) {
	digest, err := hex.DecodeString(f.SHA256)
	if err != nil || len(digest) != 32 {
		return false, downloadVerifiedSource(ctx, sc, project, f, dst)
	}
	key := sha256.Sum256([]byte(sc.base + "\x00" + project + "\x00" + fmt.Sprint(f.ID) + "\x00" + strings.ToLower(f.SHA256)))
	release, err := sourceCacheLocks.acquire(ctx, key[0])
	if err != nil {
		return false, err
	}
	defer release()
	root, _ := ctx.Value(localSourceCacheRootKey{}).(string)
	if root == "" {
		root = filepath.Join(resolveScratchRoot(app, app.Config().Get("render_scratch_dir")), "sources")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return false, downloadVerifiedSource(ctx, sc, project, f, dst)
	}
	path := filepath.Join(root, fmt.Sprintf("%x", key))
	stampPath := path + ".json"
	hit := false
	var stamp cachedSourceStamp
	if raw, err := os.ReadFile(stampPath); err == nil && json.Unmarshal(raw, &stamp) == nil {
		if info, err := os.Stat(path); err == nil && info.Size() == stamp.Size && (f.SizeBytes == 0 || info.Size() == f.SizeBytes) && info.ModTime().UnixNano() == stamp.Mtime {
			hit = time.Since(stamp.VerifiedAt) < 24*time.Hour
			if !hit {
				actual, err := sha256File(path)
				hit = err == nil && strings.EqualFold(actual, f.SHA256)
			}
		}
	}
	if !hit {
		tmp, err := os.CreateTemp(root, ".download-")
		if err != nil {
			return false, err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		defer os.Remove(tmpPath)
		if err := downloadVerifiedSource(ctx, sc, project, f, tmpPath); err != nil {
			return false, err
		}
		if err := os.Rename(tmpPath, path); err != nil {
			return false, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !hit || time.Since(stamp.VerifiedAt) >= 24*time.Hour {
		raw, _ := json.Marshal(cachedSourceStamp{info.Size(), info.ModTime().UnixNano(), time.Now()})
		if err := os.WriteFile(stampPath, raw, 0600); err != nil {
			return false, err
		}
	}
	if err := os.Link(path, dst); err != nil {
		in, err := os.Open(path)
		if err != nil {
			return false, err
		}
		defer in.Close()
		out, err := os.Create(dst)
		if err != nil {
			return false, err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return false, copyErr
		}
		if closeErr != nil {
			return false, closeErr
		}
	}
	// Access time lives on the stamp; immutable data mtime remains an integrity guard.
	_ = os.Chtimes(stampPath, time.Now(), time.Now())
	pruneLocalSourceCache(root, parseConfigInt64Fallback(app.Config().Get("render_source_cache_max_bytes"), remoteSourceCacheDefaultMaxBytes))
	return hit, nil
}
func downloadVerifiedSource(ctx context.Context, sc *storageClient, project string, f *StorageFile, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	hash := sha256.New()
	err = sc.DownloadContent(ctx, project, f.ID, io.MultiWriter(out, hash))
	info, statErr := out.Stat()
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if statErr != nil {
		return statErr
	}
	if closeErr != nil {
		return closeErr
	}
	if f.SizeBytes > 0 && info.Size() != f.SizeBytes {
		return fmt.Errorf("source size changed: expected %d, received %d", f.SizeBytes, info.Size())
	}
	if expected, err := hex.DecodeString(f.SHA256); err == nil && len(expected) == 32 && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), f.SHA256) {
		return fmt.Errorf("source checksum mismatch for file %d", f.ID)
	}
	return nil
}
func pruneLocalSourceCache(root string, maxBytes int64) {
	if !sourceCachePruneMu.TryLock() {
		return
	}
	defer sourceCachePruneMu.Unlock()
	entries, _ := os.ReadDir(root)
	type entry struct {
		path string
		size int64
		used time.Time
	}
	var all []entry
	var total int64
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) != 64 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		used := info.ModTime()
		if stamp, err := os.Stat(filepath.Join(root, e.Name()+".json")); err == nil {
			used = stamp.ModTime()
		}
		all = append(all, entry{filepath.Join(root, e.Name()), info.Size(), used})
		total += info.Size()
	}
	sort.Slice(all, func(i, j int) bool { return all[i].used.Before(all[j].used) })
	for _, e := range all {
		if total <= maxBytes {
			break
		}
		if time.Since(e.used) < time.Minute {
			continue
		}
		if os.Remove(e.path) == nil {
			total -= e.size
			_ = os.Remove(e.path + ".json")
		}
	}
}
