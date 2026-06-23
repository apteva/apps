package main

// handlers.go — HTTP surface. Two audiences:
//
//   /api/shows[/...], /api/episodes[/...]  — panel + agent REST mirror,
//       token-gated, JSON in/out. Shares store + integration logic
//       with the MCP tools.
//   /feed/{slug}.xml, /e/{guid}/file.mp3, /art/..., /transcript/...
//       — public (NoAuth): podcast clients, browsers and directories
//       carry no token.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─── /api/shows ────────────────────────────────────────────────────

func (a *App) handleShowsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		shows, err := dbListShows(globalCtx.AppDB(), httpProject(r), 100, 0)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"shows": shows, "count": len(shows)})
	case http.MethodPost:
		args, err := decodeBody(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		show, err := dbInsertShow(globalCtx.AppDB(), args, httpProject(r))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		warning := wireHostname(globalCtx, show)
		w.WriteHeader(http.StatusCreated)
		httpJSON(w, map[string]any{"show": show, "feed_url": feedURL(show), "warning": warning})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleShowItem(w http.ResponseWriter, r *http.Request) {
	id, action, ok := pathIDAction(w, r, "/api/shows/")
	if !ok {
		return
	}
	// /api/shows/{id}/validate — feed health check.
	if action == "validate" {
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		res, err := a.toolFeedValidate(globalCtx, map[string]any{"show_id": float64(id)})
		if err != nil {
			httpNotFoundOr500(w, err)
			return
		}
		httpJSON(w, res)
		return
	}
	if action != "" {
		httpErr(w, http.StatusNotFound, "unknown show action")
		return
	}
	switch r.Method {
	case http.MethodGet:
		show, err := dbGetShow(globalCtx.AppDB(), id)
		if err != nil {
			httpNotFoundOr500(w, err)
			return
		}
		httpJSON(w, map[string]any{"show": show, "feed_url": feedURL(show)})
	case http.MethodPatch, http.MethodPut:
		args, err := decodeBody(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		args["id"] = float64(id)
		res, err := a.toolShowUpdate(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, res)
	case http.MethodDelete:
		if _, err := a.toolShowDelete(globalCtx, map[string]any{"id": float64(id)}); err != nil {
			httpNotFoundOr500(w, err)
			return
		}
		httpJSON(w, map[string]any{"removed": true})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH or DELETE")
	}
}

// ─── /api/episodes ─────────────────────────────────────────────────

func (a *App) handleEpisodesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var showID int64
		if v := r.URL.Query().Get("show_id"); v != "" {
			showID, _ = strconv.ParseInt(v, 10, 64)
		}
		eps, err := dbListEpisodes(globalCtx.AppDB(), showID,
			r.URL.Query().Get("status"), 100, 0)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"episodes": eps, "count": len(eps)})
	case http.MethodPost:
		args, err := decodeBody(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		ep, err := dbInsertEpisode(globalCtx.AppDB(), args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		httpJSON(w, map[string]any{"episode": ep})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleEpisodeItem(w http.ResponseWriter, r *http.Request) {
	id, action, ok := pathIDAction(w, r, "/api/episodes/")
	if !ok {
		return
	}
	// /api/episodes/{id}/{action} — lifecycle actions, all POST. Each
	// delegates to the matching MCP tool so the REST + agent surfaces
	// stay behaviourally identical.
	if action != "" {
		a.handleEpisodeAction(w, r, id, action)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ep, err := dbGetEpisode(globalCtx.AppDB(), id)
		if err != nil {
			httpNotFoundOr500(w, err)
			return
		}
		httpJSON(w, map[string]any{"episode": ep})
	case http.MethodPatch, http.MethodPut:
		args, err := decodeBody(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		args["id"] = float64(id)
		res, err := a.toolEpisodeUpdate(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, res)
	case http.MethodDelete:
		if _, err := a.toolEpisodeDelete(globalCtx, map[string]any{"id": float64(id)}); err != nil {
			httpNotFoundOr500(w, err)
			return
		}
		httpJSON(w, map[string]any{"removed": true})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH or DELETE")
	}
}

// handleEpisodeAction runs a lifecycle action against one episode. All
// actions are POST and delegate to the matching MCP tool, so the panel
// and an agent get identical behaviour + validation.
func (a *App) handleEpisodeAction(w http.ResponseWriter, r *http.Request, id int64, action string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	args := map[string]any{"id": float64(id)}
	var (
		res any
		err error
	)
	switch action {
	case "audio", "schedule":
		body, derr := decodeBody(r)
		if derr != nil {
			httpErr(w, http.StatusBadRequest, derr.Error())
			return
		}
		for k, v := range body {
			args[k] = v
		}
		if action == "audio" {
			res, err = a.toolEpisodeSetAudio(globalCtx, args)
		} else {
			res, err = a.toolEpisodeSchedule(globalCtx, args)
		}
	case "publish":
		res, err = a.toolEpisodePublish(globalCtx, args)
	case "unpublish":
		res, err = a.toolEpisodeUnpublish(globalCtx, args)
	default:
		httpErr(w, http.StatusNotFound, "unknown episode action")
		return
	}
	if err != nil {
		// errNotFound is a 404; everything else from these tools is a
		// validation failure (no audio attached, publish_at in the
		// past, …) — a 400, not a 500.
		if errors.Is(err, errNotFound) {
			httpErr(w, http.StatusNotFound, "not found")
		} else {
			httpErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	httpJSON(w, res)
}

// ─── /feed/{slug}.xml — public RSS ─────────────────────────────────

func (a *App) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/feed/"), ".xml")
	if slug == "" {
		httpErr(w, http.StatusNotFound, "feed not found")
		return
	}
	show, err := dbGetShowForFeedRequest(globalCtx.AppDB(), slug, r)
	if err != nil {
		httpNotFoundOr500(w, err)
		return
	}

	ttl := time.Duration(configInt("feed_cache_seconds", 300)) * time.Second
	body, ok := cachedFeed(show.ID, ttl)
	if !ok {
		eps, err := dbListPublishedEpisodes(globalCtx.AppDB(), show.ID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		body, err = renderFeed(show, eps)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		storeFeed(show.ID, body)
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(configInt("feed_cache_seconds", 300)))
	_, _ = w.Write(body)
}

// ─── /e/{guid}/{file} — download tracking redirect ─────────────────

func (a *App) handleDownloadRedirect(w http.ResponseWriter, r *http.Request) {
	guid := episodeGUIDFromDownloadPath(r.URL.Path)
	if guid == "" {
		httpErr(w, http.StatusNotFound, "episode not found")
		return
	}
	ep, err := dbGetEpisodeByGUID(globalCtx.AppDB(), guid)
	if err != nil {
		httpNotFoundOr500(w, err)
		return
	}
	if ep.AudioURL == "" {
		httpErr(w, http.StatusNotFound, "episode has no audio")
		return
	}

	// IAB-style dedupe: a repeat hit from the same client inside the
	// configured window doesn't re-count. Best-effort, in-memory —
	// good enough for a single sidecar; a shared store is a v0.2 item.
	window := time.Duration(configInt("download_dedupe_minutes", 1440)) * time.Minute
	dedupeKey := clientIP(r) + "|" + r.UserAgent() + "|" + guid
	if !downloadDedupe.seenRecently(dedupeKey, window) {
		if err := dbBumpDownload(globalCtx.AppDB(), ep.ID); err != nil {
			globalCtx.Logger().Warn("bump download", "episode", ep.ID, "err", err.Error())
		}
		if show, err := dbGetShow(globalCtx.AppDB(), ep.ShowID); err == nil {
			trackDownload(globalCtx, show, ep, r)
		}
	}
	http.Redirect(w, r, ep.AudioURL, http.StatusTemporaryRedirect)
}

// ─── /art/{show|episode}/{id} — cover art passthrough ──────────────
//
// The feed's <itunes:image> needs an absolute, stable URL. Episodes
// and shows store a storage file id, not a URL, so this route resolves
// the id to a storage URL at request time and redirects to it.

func (a *App) handleArt(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/art/"), "/")
	if len(parts) != 2 {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	var fileID string
	var projectID string
	switch parts[0] {
	case "show":
		s, err := dbGetShow(globalCtx.AppDB(), id)
		if err != nil {
			httpNotFoundOr500(w, err)
			return
		}
		fileID = s.ImageFileID
		projectID = s.ProjectID
	case "episode":
		e, err := dbGetEpisode(globalCtx.AppDB(), id)
		if err != nil {
			httpNotFoundOr500(w, err)
			return
		}
		fileID = e.ImageFileID
		if s, err := dbGetShow(globalCtx.AppDB(), e.ShowID); err == nil {
			projectID = s.ProjectID
		}
	default:
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if fileID == "" {
		httpErr(w, http.StatusNotFound, "no artwork")
		return
	}
	fileURL, err := resolveStorageFileURL(fileID, projectID)
	if err != nil {
		httpErr(w, http.StatusBadGateway, "could not resolve artwork from storage")
		return
	}
	http.Redirect(w, r, fileURL, http.StatusTemporaryRedirect)
}

// ─── /transcript/episode/{id} — transcript passthrough ──────────────

func (a *App) handleTranscript(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/transcript/"), "/")
	if len(parts) != 2 || parts[0] != "episode" {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	ep, err := dbGetEpisode(globalCtx.AppDB(), id)
	if err != nil {
		httpNotFoundOr500(w, err)
		return
	}
	if ep.TranscriptFileID == "" {
		httpErr(w, http.StatusNotFound, "no transcript")
		return
	}
	show, err := dbGetShow(globalCtx.AppDB(), ep.ShowID)
	if err != nil {
		httpNotFoundOr500(w, err)
		return
	}
	fileURL, err := resolveStorageFileURL(ep.TranscriptFileID, show.ProjectID)
	if err != nil {
		httpErr(w, http.StatusBadGateway, "could not resolve transcript from storage")
		return
	}
	http.Redirect(w, r, fileURL, http.StatusTemporaryRedirect)
}

// ─── download dedupe ───────────────────────────────────────────────

type dedupeCache struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	lastSweep time.Time
}

var downloadDedupe = &dedupeCache{seen: map[string]time.Time{}}

// seenRecently reports whether key was seen inside window, and records
// this sighting. Expired entries are swept lazily once per window.
func (d *dedupeCache) seenRecently(key string, window time.Duration) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if now.Sub(d.lastSweep) > window {
		for k, t := range d.seen {
			if now.Sub(t) > window {
				delete(d.seen, k)
			}
		}
		d.lastSweep = now
	}
	last, ok := d.seen[key]
	d.seen[key] = now
	return ok && now.Sub(last) <= window
}

// ─── HTTP helpers ──────────────────────────────────────────────────

func decodeBody(r *http.Request) (map[string]any, error) {
	if r.Body == nil {
		return map[string]any{}, nil
	}
	defer r.Body.Close()
	args := map[string]any{}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&args); err != nil {
		return nil, errors.New("request body must be a JSON object: " + err.Error())
	}
	return args, nil
}

// pathIDAction splits the path tail after prefix into a trailing
// integer id and an optional action segment ("" when the path is just
// the id). Writes a 404 + returns ok=false on a missing/malformed id.
func pathIDAction(w http.ResponseWriter, r *http.Request, prefix string) (int64, string, bool) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id == 0 {
		httpErr(w, http.StatusNotFound, "not found")
		return 0, "", false
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	return id, action, true
}

func httpNotFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpErr(w, http.StatusInternalServerError, err.Error())
}

// httpProject is the owning project for panel/public HTTP requests —
// the platform-injected APTEVA_PROJECT_ID, or "" for install scope.
func httpProject(r *http.Request) string {
	if r != nil {
		if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
			return v
		}
	}
	return projectFromArgs(nil)
}

func dbGetShowForFeedRequest(db *sql.DB, slug string, r *http.Request) (*Show, error) {
	host := requestHostname(r)
	if host != "" && host != platformPublicHost() {
		show, err := dbGetShowBySlugAndHostname(db, slug, host)
		if err == nil {
			return show, nil
		}
		if !errors.Is(err, errNotFound) {
			return nil, err
		}
	}
	return dbGetShowBySlug(db, slug, httpProject(r))
}

func requestHostname(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	if i := strings.IndexByte(host, ','); i > 0 {
		host = strings.TrimSpace(host[:i])
	}
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func episodeGUIDFromDownloadPath(rawPath string) string {
	rest := strings.Trim(strings.TrimPrefix(rawPath, "/e/"), "/")
	if rest == "" {
		return ""
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	guid, err := neturl.PathUnescape(rest)
	if err != nil {
		return rest
	}
	return guid
}

func resolveStorageFileURL(fileID, projectID string) (string, error) {
	numericID, err := strconv.ParseInt(fileID, 10, 64)
	if err != nil {
		return "", errors.New("storage file id is not numeric")
	}
	args := map[string]any{"id": numericID}
	if projectID != "" {
		args["_project_id"] = projectID
	}
	var raw json.RawMessage
	if err := globalCtx.PlatformAPI().CallAppResult("storage", "files_get", args, &raw); err != nil {
		return "", err
	}
	file, err := decodeStorageFileProbe(raw)
	if err != nil {
		return "", err
	}
	if file == nil || file.URL == "" {
		return "", errors.New("storage file not found")
	}
	return file.URL, nil
}

// clientIP extracts the originating IP, honouring the proxy header
// apteva-server sets when it reverse-proxies inbound traffic.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		return host[:i]
	}
	return host
}

func configInt(key string, def int) int {
	if globalCtx == nil {
		return def
	}
	v := strings.TrimSpace(globalCtx.Config().Get(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
