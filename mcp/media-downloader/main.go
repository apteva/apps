package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestFS embed.FS

type App struct {
	ctx        *sdk.AppCtx
	ytdlpPath  string
	ffmpegPath string
	dataDir    string
	runner     commandRunner

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (a *App) Manifest() sdk.Manifest {
	b, err := manifestFS.ReadFile("apteva.yaml")
	if err != nil {
		panic(err)
	}
	m, err := sdk.ParseManifest(b)
	if err != nil {
		panic("media-downloader: invalid manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("media-downloader requires app DB")
	}
	a.ctx = ctx
	a.runner = osCommandRunner{}
	a.cancels = make(map[string]context.CancelFunc)
	a.dataDir = ctx.DataDir()
	if a.dataDir == "" {
		return errors.New("media-downloader requires APTEVA_DATA_DIR or DB_PATH")
	}
	if err := os.MkdirAll(filepath.Join(a.dataDir, "jobs"), 0700); err != nil {
		return err
	}

	a.ytdlpPath = configString(ctx, "ytdlp_path", "")
	if a.ytdlpPath == "" {
		p, err := exec.LookPath("yt-dlp")
		if err != nil {
			return errors.New("yt-dlp not found on PATH; install yt-dlp or set ytdlp_path")
		}
		a.ytdlpPath = p
	}
	a.ffmpegPath = configString(ctx, "ffmpeg_path", "")
	if a.ffmpegPath == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			a.ffmpegPath = p
		}
	}
	ctx.Logger().Info("media-downloader mounted", "ytdlp_path", a.ytdlpPath, "ffmpeg_path", a.ffmpegPath)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, cancel := range a.cancels {
		cancel()
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/jobs", Handler: a.httpJobs},
		{Method: http.MethodGet, Pattern: "/jobs/", Handler: a.httpJob},
		{Method: http.MethodGet, Pattern: "/profiles", Handler: a.httpProfiles},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "download_probe_url",
			Description: "Inspect a public or authenticated media URL using yt-dlp. Args: url, source_profile_id?, _project_id?. Returns metadata without downloading.",
			InputSchema: schemaObj(map[string]any{
				"url":               map[string]any{"type": "string"},
				"source_profile_id": map[string]any{"type": "string"},
				"_project_id":       map[string]any{"type": "string"},
			}, []string{"url"}),
			Handler: a.toolProbe,
		},
		{
			Name: "download_media",
			Description: "Start a media download job with yt-dlp and upload the completed file to storage. " +
				"Args: url, mode? (video|audio, default video), quality? (best|1080p|720p|480p|360p|worst or yt-dlp selector), format_id?, audio_format?, source_profile_id?, folder?, visibility?, tags?, no_playlist? (default true). Returns {job}.",
			InputSchema: schemaObj(map[string]any{
				"url":               map[string]any{"type": "string"},
				"mode":              map[string]any{"type": "string"},
				"quality":           map[string]any{"type": "string"},
				"format_id":         map[string]any{"type": "string"},
				"audio_format":      map[string]any{"type": "string"},
				"source_profile_id": map[string]any{"type": "string"},
				"folder":            map[string]any{"type": "string"},
				"visibility":        map[string]any{"type": "string"},
				"tags":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"no_playlist":       map[string]any{"type": "boolean"},
				"_project_id":       map[string]any{"type": "string"},
			}, []string{"url"}),
			Handler: a.toolDownload,
		},
		{Name: "download_status", Description: "Get one download job with recent logs. Args: job_id, _project_id?.", InputSchema: schemaObj(map[string]any{"job_id": map[string]any{"type": "string"}, "_project_id": map[string]any{"type": "string"}}, []string{"job_id"}), Handler: a.toolStatus},
		{Name: "download_list", Description: "List recent download jobs. Args: limit?, _project_id?.", InputSchema: schemaObj(map[string]any{"limit": map[string]any{"type": "integer"}, "_project_id": map[string]any{"type": "string"}}, nil), Handler: a.toolList},
		{Name: "download_cancel", Description: "Cancel a running download job. Args: job_id, _project_id?.", InputSchema: schemaObj(map[string]any{"job_id": map[string]any{"type": "string"}, "_project_id": map[string]any{"type": "string"}}, []string{"job_id"}), Handler: a.toolCancel},
		{Name: "source_profile_create", Description: "Create an encrypted YouTube cookie source profile. Args: name, cookies_netscape, provider? (youtube), auth_type? (cookies_netscape), test_url?, _project_id?.", InputSchema: schemaObj(map[string]any{"name": map[string]any{"type": "string"}, "cookies_netscape": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"}, "auth_type": map[string]any{"type": "string"}, "test_url": map[string]any{"type": "string"}, "_project_id": map[string]any{"type": "string"}}, []string{"name", "cookies_netscape"}), Handler: a.toolProfileCreate},
		{Name: "source_profile_list", Description: "List source profiles without secret payloads. Args: _project_id?.", InputSchema: schemaObj(map[string]any{"_project_id": map[string]any{"type": "string"}}, nil), Handler: a.toolProfileList},
		{Name: "source_profile_validate", Description: "Validate a profile against yt-dlp. Args: profile_id, url, _project_id?.", InputSchema: schemaObj(map[string]any{"profile_id": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}, "_project_id": map[string]any{"type": "string"}}, []string{"profile_id", "url"}), Handler: a.toolProfileValidate},
		{Name: "source_profile_delete", Description: "Delete a source profile. Args: profile_id, _project_id?.", InputSchema: schemaObj(map[string]any{"profile_id": map[string]any{"type": "string"}, "_project_id": map[string]any{"type": "string"}}, []string{"profile_id"}), Handler: a.toolProfileDelete},
	}
}

func (a *App) toolProbe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rawURL := strArg(args, "url")
	if rawURL == "" {
		return nil, errors.New("url is required")
	}
	if err := a.guard(ctx, args, rawURL); err != nil {
		return nil, err
	}
	projectID := projectScope(ctx, args)
	tmp, cleanup, err := a.cookieFileForProfile(context.Background(), ctx, projectID, strArg(args, "source_profile_id"))
	if err != nil {
		return nil, err
	}
	defer cleanup()
	meta, err := probeMedia(context.Background(), a.runner, a.ytdlpPath, rawURL, tmp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"metadata": slimMetadata(meta)}, nil
}

func (a *App) toolDownload(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rawURL := strArg(args, "url")
	if rawURL == "" {
		return nil, errors.New("url is required")
	}
	if err := a.guard(ctx, args, rawURL); err != nil {
		return nil, err
	}
	projectID := projectScope(ctx, args)
	mode := strings.ToLower(strArg(args, "mode"))
	if mode == "" {
		mode = "video"
	}
	if mode != "video" && mode != "audio" {
		return nil, errors.New("mode must be video or audio")
	}
	quality := strArg(args, "quality")
	if quality == "" {
		quality = "best"
	}
	folder := strArg(args, "folder")
	if folder == "" {
		folder = configString(ctx, "default_storage_folder", "/.downloads/media")
	}
	visibility := strArg(args, "visibility")
	if visibility == "" {
		visibility = configString(ctx, "default_visibility", "private")
	}
	id, err := newID("dl")
	if err != nil {
		return nil, err
	}
	req := downloadRequest{
		ProjectID:         projectID,
		URL:               rawURL,
		Mode:              mode,
		Quality:           quality,
		FormatID:          strArg(args, "format_id"),
		SourceProfileID:   strArg(args, "source_profile_id"),
		StorageFolder:     folder,
		StorageVisibility: visibility,
		AudioFormat:       strArg(args, "audio_format"),
		FFmpegLocation:    a.ffmpegPath,
		NoPlaylist:        boolArg(args, "no_playlist", true),
		Tags:              stringSliceArg(args, "tags"),
	}
	j := downloadJob{ID: id, ProjectID: projectID, URL: rawURL, Status: statusQueued, Mode: mode, Quality: quality, FormatID: req.FormatID, SourceProfileID: req.SourceProfileID, StorageFolder: folder, StorageVisibility: visibility}
	if err := insertDownload(context.Background(), ctx.AppDB(), j); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancels[id] = cancel
	a.mu.Unlock()
	go a.runDownload(runCtx, ctx.WithProject(projectID), id, req)
	return map[string]any{"job": j}, nil
}

func (a *App) runDownload(runCtx context.Context, ctx *sdk.AppCtx, id string, req downloadRequest) {
	db := ctx.AppDB()
	defer func() {
		a.mu.Lock()
		delete(a.cancels, id)
		a.mu.Unlock()
	}()
	_ = setDownloadRunning(context.Background(), db, id)
	appendLog(context.Background(), db, id, "info", "download started")

	jobDir := filepath.Join(a.dataDir, "jobs", id)
	if err := os.MkdirAll(jobDir, 0700); err != nil {
		a.fail(id, err)
		return
	}
	var cookieFile string
	if req.SourceProfileID != "" {
		payload, err := a.profilePayload(context.Background(), ctx, req.ProjectID, req.SourceProfileID)
		if err != nil {
			a.fail(id, err)
			return
		}
		cookieFile, err = writeCookieFile(jobDir, payload)
		if err != nil {
			a.fail(id, err)
			return
		}
	}

	printed := make([]string, 0, 4)
	var lastErr string
	err := a.runner.Run(runCtx, a.ytdlpPath, buildDownloadArgs(req, jobDir, cookieFile), func(line string) {
		line = trimLogLine(line)
		if p, ok := parseProgressLine(line); ok {
			_ = updateDownloadProgress(context.Background(), db, id, p)
		}
		if strings.HasPrefix(line, "/") || strings.HasPrefix(line, jobDir) {
			printed = append(printed, line)
		}
		appendLog(context.Background(), db, id, "stdout", line)
	}, func(line string) {
		lastErr = trimLogLine(line)
		appendLog(context.Background(), db, id, "stderr", line)
	})
	if err != nil {
		status := statusFailed
		if errors.Is(err, context.Canceled) {
			status = statusCanceled
			lastErr = "download canceled"
		} else if lastErr == "" {
			lastErr = err.Error()
		}
		_ = failDownload(context.Background(), db, id, status, lastErr)
		return
	}
	output, err := findOutputFile(jobDir, printed)
	if err != nil {
		a.fail(id, err)
		return
	}
	if err := a.uploadOutput(ctx, id, req, output); err != nil {
		a.fail(id, err)
		return
	}
	appendLog(context.Background(), db, id, "info", "download completed")
}

func (a *App) uploadOutput(ctx *sdk.AppCtx, id string, req downloadRequest, path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	maxBytes := int64(configInt(ctx, "max_download_mb", 512)) * 1024 * 1024
	if maxBytes > 0 && st.Size() > maxBytes {
		return fmt.Errorf("output is %d bytes, over max_download_mb", st.Size())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	name := filepath.Base(path)
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	tags := append([]string{"media-downloader", req.Mode}, req.Tags...)
	args := map[string]any{
		"name":           name,
		"folder":         req.StorageFolder,
		"content_type":   ctype,
		"content_base64": base64.StdEncoding.EncodeToString(body),
		"tags":           tags,
		"visibility":     req.StorageVisibility,
		"source":         "media-downloader",
	}
	var got struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", storageArgs(req.ProjectID, args), &got); err != nil {
		return fmt.Errorf("storage.files_upload: %w", err)
	}
	return completeDownload(context.Background(), ctx.AppDB(), id, name, st.Size(), got.ID, got.URL)
}

func (a *App) fail(id string, err error) {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	appendLog(context.Background(), a.ctx.AppDB(), id, "error", msg)
	_ = failDownload(context.Background(), a.ctx.AppDB(), id, statusFailed, msg)
}

func (a *App) toolStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	j, err := getDownload(context.Background(), ctx.AppDB(), projectScope(ctx, args), strArg(args, "job_id"))
	if err != nil {
		return nil, err
	}
	logs, err := recentLogs(context.Background(), ctx.AppDB(), j.ID, 25)
	if err != nil {
		return nil, err
	}
	return map[string]any{"job": j, "logs": logs}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	jobs, err := listDownloads(context.Background(), ctx.AppDB(), projectScope(ctx, args), intArg(args, "limit", 25))
	if err != nil {
		return nil, err
	}
	return map[string]any{"jobs": jobs}, nil
}

func (a *App) toolCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strArg(args, "job_id")
	a.mu.Lock()
	cancel := a.cancels[id]
	a.mu.Unlock()
	if cancel == nil {
		return map[string]any{"canceled": false, "reason": "job is not running"}, nil
	}
	cancel()
	appendLog(context.Background(), ctx.AppDB(), id, "info", "cancel requested")
	return map[string]any{"canceled": true}, nil
}

func (a *App) toolProfileCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name is required")
	}
	provider := strings.ToLower(strArg(args, "provider"))
	if provider == "" {
		provider = "youtube"
	}
	authType := strings.ToLower(strArg(args, "auth_type"))
	if authType == "" {
		authType = "cookies_netscape"
	}
	payload := profilePayload{CookiesNetscape: strArg(args, "cookies_netscape")}
	if err := validateCookieProfile(provider, authType, payload); err != nil {
		return nil, err
	}
	encrypted, err := encryptPayload(ctx, payload)
	if err != nil {
		return nil, err
	}
	id, err := newID("src")
	if err != nil {
		return nil, err
	}
	p := storedProfile{sourceProfile: sourceProfile{ID: id, ProjectID: projectScope(ctx, args), Name: name, Provider: provider, AuthType: authType}, EncryptedPayload: encrypted}
	if err := insertProfile(context.Background(), ctx.AppDB(), p); err != nil {
		return nil, err
	}
	public := p.sourceProfile
	public.Status = "active"
	if testURL := strArg(args, "test_url"); testURL != "" {
		if err := a.guard(ctx, args, testURL); err != nil {
			_ = markProfileValidated(context.Background(), ctx.AppDB(), id, p.ProjectID, err.Error())
			return map[string]any{"profile": public, "validated": false, "validation_error": err.Error()}, nil
		}
		if err := a.validateProfileAgainstURL(ctx, p.ProjectID, id, testURL); err != nil {
			return map[string]any{"profile": public, "validated": false, "validation_error": err.Error()}, nil
		}
		return map[string]any{"profile": public, "validated": true}, nil
	}
	return map[string]any{"profile": public}, nil
}

func (a *App) toolProfileList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profiles, err := listProfiles(context.Background(), ctx.AppDB(), projectScope(ctx, args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"profiles": profiles}, nil
}

func (a *App) toolProfileValidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profileID, rawURL := strArg(args, "profile_id"), strArg(args, "url")
	if profileID == "" || rawURL == "" {
		return nil, errors.New("profile_id and url are required")
	}
	if err := a.guard(ctx, args, rawURL); err != nil {
		return nil, err
	}
	if err := a.validateProfileAgainstURL(ctx, projectScope(ctx, args), profileID, rawURL); err != nil {
		return map[string]any{"valid": false, "error": err.Error()}, nil
	}
	return map[string]any{"valid": true}, nil
}

func (a *App) validateProfileAgainstURL(ctx *sdk.AppCtx, projectID, profileID, rawURL string) error {
	tmp, cleanup, err := a.cookieFileForProfile(context.Background(), ctx, projectID, profileID)
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = probeMedia(context.Background(), a.runner, a.ytdlpPath, rawURL, tmp)
	lastErr := ""
	if err != nil {
		lastErr = err.Error()
	}
	_ = markProfileValidated(context.Background(), ctx.AppDB(), profileID, projectID, lastErr)
	return err
}

func (a *App) toolProfileDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := deleteProfile(context.Background(), ctx.AppDB(), strArg(args, "profile_id"), projectScope(ctx, args)); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}

func (a *App) profilePayload(ctx context.Context, app *sdk.AppCtx, projectID, id string) (profilePayload, error) {
	p, err := getProfile(ctx, app.AppDB(), projectID, id)
	if err != nil {
		return profilePayload{}, err
	}
	payload, err := decryptPayload(app, p.EncryptedPayload)
	if err != nil {
		return profilePayload{}, err
	}
	return payload, validateCookieProfile(p.Provider, p.AuthType, payload)
}

func (a *App) cookieFileForProfile(ctx context.Context, app *sdk.AppCtx, projectID, id string) (string, func(), error) {
	if id == "" {
		return "", func() {}, nil
	}
	payload, err := a.profilePayload(ctx, app, projectID, id)
	if err != nil {
		return "", func() {}, err
	}
	dir, err := os.MkdirTemp("", "media-downloader-profile-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	cookieFile, err := writeCookieFile(dir, payload)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return cookieFile, cleanup, nil
}

func (a *App) guard(ctx *sdk.AppCtx, args map[string]any, rawURL string) error {
	allowed := parseDomainList(configString(ctx, "allowed_domains", ""))
	blocked := parseDomainList(configString(ctx, "blocked_domains", "localhost,127.0.0.1,::1"))
	return guardURL(context.Background(), rawURL, allowed, blocked, nil)
}

func slimMetadata(meta map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"id", "title", "extractor", "extractor_key", "webpage_url", "duration", "live_status", "age_limit", "channel", "uploader", "upload_date"} {
		if v, ok := meta[k]; ok {
			out[k] = v
		}
	}
	if formats, ok := meta["formats"].([]any); ok {
		out["format_count"] = len(formats)
	}
	return out
}

func (a *App) httpJobs(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" && a.ctx != nil {
		projectID = a.ctx.CurrentProject()
	}
	limit := 25
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	jobs, err := listDownloads(r.Context(), a.ctx.AppDB(), projectID, limit)
	writeJSON(w, map[string]any{"jobs": jobs}, err)
}

func (a *App) httpJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/jobs/")
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" && a.ctx != nil {
		projectID = a.ctx.CurrentProject()
	}
	j, err := getDownload(r.Context(), a.ctx.AppDB(), projectID, id)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	logs, err := recentLogs(r.Context(), a.ctx.AppDB(), id, 25)
	writeJSON(w, map[string]any{"job": j, "logs": logs}, err)
}

func (a *App) httpProfiles(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" && a.ctx != nil {
		projectID = a.ctx.CurrentProject()
	}
	profiles, err := listProfiles(r.Context(), a.ctx.AppDB(), projectID)
	writeJSON(w, map[string]any{"profiles": profiles}, err)
}

func writeJSON(w http.ResponseWriter, body any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errNotFound) {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// Keep database/sql imported for module-local tests that open an app DB
// with the same driver set as the sidecar binary.
var _ *sql.DB

func main() { sdk.Run(&App{}) }
