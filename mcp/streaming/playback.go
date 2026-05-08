package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// handlePlayback serves HLS manifests, segments, and recording mp4s
// directly from the stream's data dir. Token-gated via ?t=<playback_token>.
//
// URL shapes:
//
//   /streams/<id>/index.m3u8         — HLS manifest (live or replay)
//   /streams/<id>/seg-NNNNN.ts       — HLS segments
//   /streams/<id>/record.mp4         — full recording (post-stream)
//
// Public-visibility streams skip the token check; signed-visibility
// requires ?t=<playback_token> matching the row.
func (a *App) handlePlayback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/streams/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "stream id required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "invalid stream id")
		return
	}
	if len(parts) < 2 || parts[1] == "" {
		httpErr(w, http.StatusBadRequest, "filename required")
		return
	}
	filename := parts[1]
	// Reject path-traversal attempts up front. Filenames are flat —
	// segments live in the same dir as the manifest. Anything with /
	// or \ or starting with . is suspicious.
	if !validPlaybackFilename(filename) {
		httpErr(w, http.StatusBadRequest, "invalid filename")
		return
	}

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		// Token-only access works even without project_id when in
		// scope=global mode AND the URL carries the token. We need
		// project_id to load the row, so require it explicitly.
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := globalCtx
	app := globalApp
	if ctx == nil || app == nil {
		httpErr(w, http.StatusServiceUnavailable, "sidecar not mounted")
		return
	}

	s, err := app.dbGet(ctx, pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s == nil {
		http.NotFound(w, r)
		return
	}

	// Visibility gate.
	if s.Visibility == "signed" {
		token := r.URL.Query().Get("t")
		if token == "" || token != s.PlaybackToken {
			// 404, not 403, so we don't leak existence.
			http.NotFound(w, r)
			return
		}
	}

	// Resolve the on-disk path. streamDataDir already uses filepath.Join
	// which collapses any embedded "..", but validPlaybackFilename
	// caught those above anyway.
	dir := streamDataDir(ctx, s.StoragePrefix)
	full := filepath.Join(dir, filename)

	// Final containment check — defense in depth.
	abs, err := filepath.Abs(full)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dirAbs, _ := filepath.Abs(dir)
	if !strings.HasPrefix(abs, dirAbs+string(filepath.Separator)) {
		httpErr(w, http.StatusBadRequest, "path traversal blocked")
		return
	}

	st, err := os.Stat(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if st.IsDir() {
		http.NotFound(w, r)
		return
	}

	// Set content-type and cache headers per file kind. HLS manifests
	// must NOT be cached (they update every segment); segments are
	// immutable once written so cache aggressively.
	switch ext := filepath.Ext(filename); ext {
	case ".m3u8":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	case ".ts":
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case ".mp4":
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Cache-Control", "public, max-age=3600")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// CORS: allow embedding in third-party pages (the public-facing
	// "live page" the consumer app serves may be a different origin).
	w.Header().Set("Access-Control-Allow-Origin", "*")

	http.ServeFile(w, r, abs)
}

// validPlaybackFilename allows only the small flat shapes the runner
// produces: index.m3u8, seg-NNNNN.ts, record.mp4. Rejects paths with
// separators or "..".
func validPlaybackFilename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	switch {
	case name == "index.m3u8":
		return true
	case name == "record.mp4":
		return true
	case strings.HasPrefix(name, "seg-") && strings.HasSuffix(name, ".ts"):
		return true
	}
	return false
}
