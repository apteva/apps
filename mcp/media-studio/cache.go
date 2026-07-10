package main

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Local byte cache for generated media when storage isn't bound.
//
// Without storage, the panel would otherwise fall back to thumbnail_b64
// (~256px JPEG, lossy) — visibly soft. We instead write the full
// provider bytes to <db_dir>/cache/<gen_id>.<ext> and expose them
// via GET /cache/<gen_id>. The panel prefers this URL when no
// storage_url is available.
//
// Best-effort: write failures are logged + swallowed (the row still
// has the thumbnail fallback). No GC yet — a future migration can
// expire entries by generation id age.

func cacheDir() (string, error) {
	if globalCtx == nil {
		return "", errors.New("app not mounted")
	}
	base := globalCtx.DataDir()
	if base == "" {
		base = filepath.Join(os.TempDir(), "apteva-media-studio-"+strconv.Itoa(os.Getpid()))
	}
	dir := filepath.Join(base, "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// writeLocalCache decodes the base64 bytes and writes them to
// <cache>/<genID>.<ext>. Returns the ext actually used (for logging).
func writeLocalCache(genID int64, b64, ext string) error {
	if genID == 0 || b64 == "" {
		return nil
	}
	bytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	if ext == "" {
		ext = "bin"
	}
	path := filepath.Join(dir, strconv.FormatInt(genID, 10)+"."+ext)
	return os.WriteFile(path, bytes, 0o644)
}

// localCacheURL returns the panel-visible URL for a given gen id,
// or "" if no cache file exists.
func localCacheURL(genID int64, projectID string) string {
	if genID == 0 {
		return ""
	}
	if path, ok := localCachePath(genID); ok {
		_ = path
		return "/api/apps/media-studio/cache/" + strconv.FormatInt(genID, 10) +
			"?project_id=" + url.QueryEscape(projectID)
	}
	return ""
}

// localCachePath returns the absolute filesystem path of the cache
// file for a given gen id, or "" + false if it doesn't exist.
func localCachePath(genID int64) (string, bool) {
	dir, err := cacheDir()
	if err != nil {
		return "", false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, strconv.FormatInt(genID, 10)+".*"))
	if len(matches) == 0 {
		return "", false
	}
	return matches[0], true
}

func deleteLocalCache(genID int64) error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, strconv.FormatInt(genID, 10)+".*"))
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func writePendingJobCache(jobID int64, b64, ext string) error {
	if jobID <= 0 || b64 == "" {
		return errors.New("pending job cache requires job id and bytes")
	}
	bytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	if ext == "" {
		ext = "bin"
	}
	return os.WriteFile(filepath.Join(dir, "job-"+strconv.FormatInt(jobID, 10)+"."+ext), bytes, 0o600)
}

func readPendingJobCache(jobID int64) (string, string, string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", "", "", err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "job-"+strconv.FormatInt(jobID, 10)+".*"))
	if len(matches) == 0 {
		return "", "", "", os.ErrNotExist
	}
	bytes, err := os.ReadFile(matches[0])
	if err != nil {
		return "", "", "", err
	}
	ext := strings.TrimPrefix(filepath.Ext(matches[0]), ".")
	mt := mime.TypeByExtension("." + ext)
	if mt == "" {
		mt = "application/octet-stream"
	}
	return base64.StdEncoding.EncodeToString(bytes), mt, ext, nil
}

func deletePendingJobCache(jobID int64) error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "job-"+strconv.FormatInt(jobID, 10)+".*"))
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// HTTP /cache/<id> — serves the cached bytes for one generation.
// Pattern is registered as "/cache/" (trailing slash) so net/http's
// mux routes everything under it here.
func (a *App) handleCacheGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/cache/")
	idStr = strings.SplitN(idStr, "/", 2)[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ctx, pid, err := projectContextFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var owned int
	if err := ctx.AppDB().QueryRow(
		`SELECT 1 FROM generations WHERE id=? AND project_id=?`, id, pid,
	).Scan(&owned); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	path, ok := localCachePath(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	mt := mime.TypeByExtension(filepath.Ext(path))
	if mt == "" {
		mt = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.Copy(w, f)
}
