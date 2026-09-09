package main

import (
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

// Local fallback for rendered bytes when storage isn't bound. Lifted
// verbatim from media-studio v0.5.3 — same pattern, different path
// root.
//
// Renders saved under <db_dir>/cache/<render_id>.<ext>. Served via
// GET /cache/<render_id>. history rows surface local_cache_url when
// storage_id == 0.

func cacheDir() (string, error) {
	base := "/data"
	if env := os.Getenv("DB_PATH"); env != "" {
		base = filepath.Dir(env)
	}
	dir := filepath.Join(base, "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// writeLocalCacheFromPath moves the executor's output file into the
// cache dir under <renderID>.<ext>. We use rename when possible
// (fast, atomic on same FS) and fall back to copy.
func writeLocalCacheFromPath(renderID int64, srcPath, ext string) error {
	if renderID == 0 || srcPath == "" {
		return errors.New("render id and output path required")
	}
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	if ext == "" {
		ext = "bin"
	}
	dst := filepath.Join(dir, strconv.FormatInt(renderID, 10)+"."+ext)
	if err := os.Rename(srcPath, dst); err == nil {
		return nil
	}
	// rename failed (cross-FS?); fall back to copy.
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".composer-cache-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func localCacheURL(renderID int64, projectID ...string) string {
	if renderID == 0 {
		return ""
	}
	if _, ok := localCachePath(renderID); ok {
		pid := ""
		if len(projectID) > 0 {
			pid = projectID[0]
		}
		return "/api/apps/composer/cache/" + strconv.FormatInt(renderID, 10) + "?project_id=" + url.QueryEscape(pid)
	}
	return ""
}

func localCachePath(renderID int64) (string, bool) {
	dir, err := cacheDir()
	if err != nil {
		return "", false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, strconv.FormatInt(renderID, 10)+".*"))
	if len(matches) == 0 {
		return "", false
	}
	return matches[0], true
}

func (a *App) handleCacheGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
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
	ctx := requestAppCtx(r)
	if ctx == nil || !renderBelongsToProject(ctx, id, projectScope(ctx)) {
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
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
