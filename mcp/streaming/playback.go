package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// handlePlayback serves HLS manifests, segments, and recording mp4s
// directly from the stream's data dir. Token-gated via ?t=<playback_token>,
// optionally signature-gated via ?exp=&sig= (see signing.go).
//
// URL shapes:
//
//	/streams/<id>/index.m3u8         — live HLS manifest (rolling window)
//	/streams/<id>/replay.m3u8        — VOD manifest, written at finalize
//	/streams/<id>/seg-NNNNN.ts       — HLS segments
//	/streams/<id>/record.mp4         — full recording (status=ended only)
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
		// The row can't be loaded without a project, and a global-scope
		// install can't infer one from its env — so every URL this app
		// generates carries project_id (see urlProjectID). A request
		// without it is a hand-built URL, not one of ours.
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := globalCtx
	app := globalApp
	if ctx == nil || app == nil {
		httpErr(w, http.StatusServiceUnavailable, "sidecar not mounted")
		return
	}

	// Cached gating record — a 26-column row read per segment request
	// would serialize every viewer behind the DB's single connection.
	rec, err := app.playbackFor(ctx, pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		http.NotFound(w, r)
		return
	}

	// Visibility + signature gate. 404, not 403, so we don't leak
	// existence.
	if !playbackAuthorized(rec, r.URL.Query(), time.Now()) {
		http.NotFound(w, r)
		return
	}

	// The recording is only servable once the stream is over. Mid-stream
	// the file has no moov atom, so what a token-holder would get is an
	// unplayable partial — and with the old `public, max-age=3600` any
	// intermediate cache then stored that corrupt copy for an hour and
	// kept serving it long after a clean finalize.
	if filename == recordingFile && rec.Status != "ended" {
		http.NotFound(w, r)
		return
	}

	// Resolve the on-disk path. streamDataDir already uses filepath.Join
	// which collapses any embedded "..", but validPlaybackFilename
	// caught those above anyway.
	dir := streamDataDir(ctx, rec.StoragePrefix)
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
	//
	// Token- or signature-gated bytes are cached `private`: a shared
	// cache (CDN, corporate proxy) must not be able to hand one
	// viewer's authorized copy to the next requester, and a
	// signature's expiry means nothing if a proxy keeps serving the
	// object after it.
	scope := "public"
	if rec.Visibility == "signed" || rec.RequireSignedURLs {
		scope = "private"
	}
	switch ext := filepath.Ext(filename); ext {
	case ".m3u8":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	case ".ts":
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", scope+", max-age=31536000, immutable")
	case ".mp4":
		// Only reachable for status=ended (gated above), so the file is
		// final and cacheable for its lifetime.
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Cache-Control", scope+", max-age=3600")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// CORS: allow embedding in third-party pages (the public-facing
	// "live page" the consumer app serves may be a different origin).
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Manifests are rewritten so their relative segment URIs keep the
	// request's credentials — see rewriteManifestQuery.
	if filepath.Ext(filename) == ".m3u8" {
		body, err := os.ReadFile(abs)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body = rewriteManifestQuery(body, r.URL.RawQuery)
		http.ServeContent(w, r, filename, st.ModTime(), bytes.NewReader(body))
		return
	}

	http.ServeFile(w, r, abs)
}

// rewriteManifestQuery appends the manifest request's own query string
// to every relative segment URI in an HLS playlist.
//
// A browser (and hls.js, and native Safari) resolves the bare
// "seg-00042.ts" lines ffmpeg writes against the manifest URL WITHOUT
// its query string — so a player handed index.m3u8?t=… fetched every
// segment with no credentials at all and got a 404 for each one.
// Token-gated HLS therefore never worked in a real player; the only
// thing that exercised it was the built-in load generator, which
// re-appends the query itself (loadtest.go), which is exactly why the
// load numbers looked fine.
//
// With signed URLs the segments inherit the manifest's exp+sig, so a
// signature's expiry applies to the whole session, not just the
// manifest fetch.
func rewriteManifestQuery(body []byte, rawQuery string) []byte {
	if rawQuery == "" {
		return body
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		uri := strings.TrimSpace(line)
		switch {
		case uri == "", strings.HasPrefix(uri, "#"): // tags + blanks
			continue
		case strings.Contains(uri, "://"): // absolute, not ours to sign
			continue
		case strings.Contains(uri, "?"): // already carries a query
			continue
		}
		lines[i] = uri + "?" + rawQuery
	}
	return []byte(strings.Join(lines, "\n"))
}

// validPlaybackFilename allows only the small flat shapes the runner
// (or finalize) produces: index.m3u8, replay.m3u8, seg-NNNNN.ts,
// record.mp4. Rejects paths with separators or "..".
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
	case name == indexPlaylistFile:
		return true
	case name == replayPlaylistFile:
		return true
	case name == recordingFile:
		return true
	case strings.HasPrefix(name, "seg-") && strings.HasSuffix(name, ".ts"):
		return true
	}
	return false
}
