package main

// Immutable, scoped source archives. Transport is independent of project type:
// small archives remain inline; large ones use bounded authenticated MCP reads.
import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const snapshotChunkBytes = 1 << 20
const snapshotTTL = 24 * time.Hour

var snapshotIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var snapshotMu sync.Mutex

type repoSourceExport struct {
	Slug           string `json:"slug"`
	SnapshotID     string `json:"snapshot_id"`
	SourceRevision string `json:"source_revision"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Format         string `json:"format"`
	Inline         bool   `json:"inline"`
	ZipB64         string `json:"zip_b64,omitempty"`
	DownloadURL    string `json:"download_url"`
	ExpiresAt      string `json:"expires_at"`
}

func (a *App) snapshotDir(repo *Repo) string {
	return filepath.Join(a.dataDir, "source-snapshots", strconv.FormatInt(repo.ID, 10))
}
func snapshotMaxBytes() int64 { return envLimit("CODE_SNAPSHOT_MAX_BYTES", 1<<30) }
func (a *App) snapshotDescriptor(repo *Repo, id string, info os.FileInfo) *repoSourceExport {
	q := url.Values{"project_id": {repo.ProjectID}, "snapshot_id": {id}}
	if install := os.Getenv("APTEVA_INSTALL_ID"); install != "" {
		q.Set("install_id", install)
	}
	return &repoSourceExport{Slug: repo.Slug, SnapshotID: id, SourceRevision: "sha256:" + id, SHA256: id, Size: info.Size(), Format: "zip-v1", ExpiresAt: info.ModTime().Add(snapshotTTL).UTC().Format(time.RFC3339), DownloadURL: "/api/apps/code/api/repos/" + url.PathEscape(repo.Slug) + "/export?" + q.Encode()}
}
func (a *App) openSourceSnapshot(repo *Repo, id string) (*os.File, *repoSourceExport, error) {
	if a.dataDir == "" || !snapshotIDPattern.MatchString(id) {
		return nil, nil, errors.New("invalid source snapshot id")
	}
	path := filepath.Join(a.snapshotDir(repo), id+".zip")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("source snapshot unavailable; it may have expired: %w", err)
	}
	if !info.Mode().IsRegular() || time.Now().After(info.ModTime().Add(snapshotTTL)) {
		return nil, nil, errors.New("source snapshot expired or invalid; create a new snapshot explicitly")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, a.snapshotDescriptor(repo, id, info), nil
}

type snapshotContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r snapshotContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type snapshotLimitWriter struct {
	w      io.Writer
	n, max int64
}

func (w *snapshotLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.max-w.n {
		return 0, errors.New("source snapshot exceeds byte budget")
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}
func (a *App) createSourceSnapshot(callCtx context.Context, repo *Repo) (*repoSourceExport, error) {
	return a.createSourceSnapshotSubdir(callCtx, repo, "")
}
func (a *App) createSourceSnapshotSubdir(callCtx context.Context, repo *Repo, subdir string) (*repoSourceExport, error) {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	if a.dataDir == "" {
		return nil, errors.New("source snapshot storage unavailable")
	}
	root := filepath.Join(a.dataDir, "source-snapshots")
	if err := os.MkdirAll(a.snapshotDir(repo), 0700); err != nil {
		return nil, err
	}
	// Unexpired snapshots are never evicted to admit new ones. Readers can rely
	// on the expiry returned in their receipt, including across sidecar restarts.
	var used int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if time.Now().After(info.ModTime().Add(snapshotTTL)) {
			return os.Remove(path)
		}
		used += info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	capacity := envLimit("CODE_SNAPSHOT_CACHE_BYTES", 8<<30)
	temp, err := os.CreateTemp(a.snapshotDir(repo), ".snapshot-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	sum := sha256.New()
	writer := &snapshotLimitWriter{w: io.MultiWriter(temp, sum), max: snapshotMaxBytes()}
	_, err = withRepoWrite(a.storeFor(repo), repo.Slug, func(raw FileStore) (bool, error) {
		return true, writeSnapshotZIP(callCtx, raw, repo.Slug, subdir, writer)
	})
	if err != nil {
		return nil, err
	}
	if err := temp.Sync(); err != nil {
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(sum.Sum(nil))
	dest := filepath.Join(a.snapshotDir(repo), id+".zip")
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if writer.n > capacity-used {
			return nil, errors.New("source snapshot cache full; wait for expiry or increase CODE_SNAPSHOT_CACHE_BYTES")
		}
		if err := os.Rename(temp.Name(), dest); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	now := time.Now()
	if err := os.Chtimes(dest, now, now); err != nil {
		return nil, err
	}
	info, err := os.Stat(dest)
	if err != nil {
		return nil, err
	}
	return a.snapshotDescriptor(repo, id, info), nil
}

func snapshotSubdir(raw string) (string, error) {
	if raw == "." {
		return "", nil
	}
	if strings.ContainsAny(raw, "\\\x00") || filepath.IsAbs(raw) {
		return "", errors.New("subdir must be a relative directory")
	}
	if raw != "" {
		for _, part := range strings.Split(raw, "/") {
			if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
				return "", errors.New("invalid source subdir")
			}
		}
	}
	return raw, nil
}
func writeSnapshotZIP(ctx context.Context, store FileStore, slug, subdir string, w io.Writer) error {
	var err error
	subdir, err = snapshotSubdir(subdir)
	if err != nil {
		return err
	}
	if subdir != "" {
		// Check every component without following symlinks, even when their target
		// happens to remain inside this repository.
		local, ok := store.(FileStoreLocalPath)
		if !ok {
			return errors.New("subdir export requires local repository storage")
		}
		selected := local.RepoPath(slug)
		for _, part := range strings.Split(subdir, "/") {
			selected = filepath.Join(selected, part)
			info, err := os.Lstat(selected)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return errors.New("subdir must be a real directory, not a symlink")
			}
		}
	}
	files, err := listSourceFiles(store, slug, subdir, true, false)
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files) > int(envLimit("CODE_SNAPSHOT_MAX_FILES", 100000)) {
		return errors.New("too many snapshot source entries")
	}
	z := zip.NewWriter(w)
	var total int64
	revisions := map[string]string{}
	local, isLocal := store.(FileStoreLocalPath)
	for _, meta := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if meta.IsDir {
			continue
		}
		if meta.Size < 0 || meta.Size > snapshotMaxBytes()-total {
			return errors.New("snapshot expanded source exceeds byte budget")
		}
		total += meta.Size
		mode := os.FileMode(meta.Mode)
		if mode == 0 {
			mode = 0644
		}
		var input io.ReadCloser
		if isLocal {
			full, err := safeJoinSource(local.RepoPath(slug), meta.Path)
			if err != nil {
				return err
			}
			info, err := os.Lstat(full)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("snapshot requires regular source file: %s", meta.Path)
			}
			if info.Size() != meta.Size {
				return errRevisionConflict
			}
			revisions[full] = fileRevision(info)
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			input = f
			mode = info.Mode().Perm()
		} else {
			body, err := store.Read(slug, meta.Path)
			if err != nil {
				return err
			}
			input = io.NopCloser(bytes.NewReader(body))
		}
		name := meta.Path
		if subdir != "" {
			if !strings.HasPrefix(name, subdir+"/") {
				input.Close()
				return errors.New("source entry outside selected directory")
			}
			name = strings.TrimPrefix(name, subdir+"/")
		}
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(mode)
		out, err := z.CreateHeader(h)
		if err != nil {
			input.Close()
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(snapshotContextReader{ctx, input}, meta.Size+1))
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != meta.Size {
			return errRevisionConflict
		}
	}
	after, err := listSourceFiles(store, slug, subdir, true, false)
	if err != nil {
		return err
	}
	sort.Slice(after, func(i, j int) bool { return after[i].Path < after[j].Path })
	if len(after) != len(files) {
		return errRevisionConflict
	}
	for i := range files {
		if after[i].Path != files[i].Path || after[i].IsDir != files[i].IsDir {
			return errRevisionConflict
		}
	}
	// Also reject external writers which bypass Code's repository lock.
	for path, before := range revisions {
		info, err := os.Lstat(path)
		if err != nil || fileRevision(info) != before {
			return errRevisionConflict
		}
	}
	return z.Close()
}

func snapshotInteger(args map[string]any, key string, fallback int64) (int64, error) {
	v, exists := args[key]
	if !exists {
		return fallback, nil
	}
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case json.Number:
		return n.Int64()
	case float64:
		if n >= 0 && n <= 1<<53 && math.Trunc(n) == n {
			return int64(n), nil
		}
	}
	return 0, fmt.Errorf("%s must be an integer", key)
}

func (a *App) toolSnapshotRead(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	offset, err := snapshotInteger(args, "offset", 0)
	if err != nil {
		return nil, err
	}
	limit, err := snapshotInteger(args, "limit", snapshotChunkBytes)
	if err != nil {
		return nil, err
	}
	if offset < 0 || limit < 1 || limit > snapshotChunkBytes {
		return nil, errors.New("invalid snapshot offset or limit (maximum 1 MiB)")
	}
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	f, desc, err := a.openSourceSnapshot(repo, strArg(args, "snapshot_id"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if int64(offset) > desc.Size {
		return nil, errors.New("snapshot offset exceeds size")
	}
	data := make([]byte, min(int64(limit), desc.Size-int64(offset)))
	n, err := f.ReadAt(data, int64(offset))
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n != len(data) {
		return nil, errors.New("snapshot truncated")
	}
	return map[string]any{"snapshot_id": desc.SnapshotID, "sha256": desc.SHA256, "size": desc.Size, "offset": offset, "next_offset": offset + int64(n), "eof": offset+int64(n) == desc.Size, "data_b64": base64.StdEncoding.EncodeToString(data)}, nil
}
func (a *App) serveSourceSnapshot(w http.ResponseWriter, r *http.Request, repo *Repo) {
	snapshotMu.Lock()
	f, desc, err := a.openSourceSnapshot(repo, r.URL.Query().Get("snapshot_id"))
	snapshotMu.Unlock()
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", repo.Slug+".zip"))
	w.Header().Set("ETag", `"`+desc.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, repo.Slug+".zip", time.Time{}, f)
}
