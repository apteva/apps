package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	cancels map[string]runningDownload
	wg      sync.WaitGroup
	slots   chan struct{}
	proxy   *safeProxy
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
	a.cancels = make(map[string]runningDownload)
	a.slots = make(chan struct{}, configInt(ctx, "max_concurrent_downloads", 2))
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
	proxy, err := startSafeProxy(nil)
	if err != nil {
		return err
	}
	a.proxy = proxy
	interrupted, err := interruptActiveDownloads(context.Background(), ctx.AppDB(), "interrupted by app restart; start a new download")
	if err != nil {
		_ = proxy.Close(context.Background())
		return err
	}
	for _, job := range interrupted {
		a.emitDownloadJob(ctx.WithProject(job.ProjectID), "download.failed", job)
	}
	a.cleanupJobDirs(ctx)
	ctx.Logger().Info("media-downloader mounted", "ytdlp_path", a.ytdlpPath, "ffmpeg_path", a.ffmpegPath, "max_concurrent_downloads", cap(a.slots))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	a.mu.Lock()
	running := make([]runningDownload, 0, len(a.cancels))
	for _, download := range a.cancels {
		running = append(running, download)
	}
	a.mu.Unlock()
	for _, download := range running {
		download.cancel()
	}
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		if a.ctx != nil {
			a.ctx.Logger().Warn("timed out waiting for downloads to stop")
		}
	}
	if a.proxy != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.proxy.Close(closeCtx)
	}
	return nil
}

func (a *App) cleanupJobDirs(ctx *sdk.AppCtx) {
	root := filepath.Join(a.dataDir, "jobs")
	entries, err := os.ReadDir(root)
	if err != nil {
		ctx.Logger().Warn("clean stale media download directories", "error", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			ctx.Logger().Warn("remove stale media download directory", "job_id", entry.Name(), "error", err)
		}
	}
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/jobs", Handler: a.httpJobs},
		{Method: http.MethodPost, Pattern: "/jobs", Handler: a.httpCreateJob},
		{Method: http.MethodPost, Pattern: "/probe", Handler: a.httpProbe},
		{Method: http.MethodGet, Pattern: "/jobs/", Handler: a.httpJob},
		{Method: http.MethodPost, Pattern: "/jobs/", Handler: a.httpJobAction},
		{Method: http.MethodGet, Pattern: "/profiles", Handler: a.httpProfiles},
		{Method: http.MethodPost, Pattern: "/profiles", Handler: a.httpCreateProfile},
		{Method: http.MethodPost, Pattern: "/profiles/", Handler: a.httpProfileAction},
		{Method: http.MethodDelete, Pattern: "/profiles/", Handler: a.httpDeleteProfile},
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
			Description: "Start a single-item media download job with yt-dlp and upload the completed file to storage. " +
				"Args: url, mode? (video|audio, default video), quality? (best|1080p|720p|480p|360p|worst or yt-dlp selector), format_id?, audio_format?, source_profile_id?, folder?, visibility?, tags?. Returns {job}.",
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
		{Name: "source_profile_create", Description: "Create an encrypted cookie source profile. Args: name, cookies_netscape, provider? (youtube|patreon, default youtube), auth_type? (cookies_netscape), test_url?, _project_id?.", InputSchema: schemaObj(map[string]any{"name": map[string]any{"type": "string"}, "cookies_netscape": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string", "enum": []string{"youtube", "patreon"}}, "auth_type": map[string]any{"type": "string"}, "test_url": map[string]any{"type": "string"}, "_project_id": map[string]any{"type": "string"}}, []string{"name", "cookies_netscape"}), Handler: a.toolProfileCreate},
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
	meta, err := probeMedia(context.Background(), a.runner, a.ytdlpPath, rawURL, tmp, parseExtraArgs(configString(ctx, "ytdlp_extra_args", "")), a.proxyURL())
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
	if !boolArg(args, "no_playlist", true) {
		return nil, errors.New("playlist downloads are not supported by a single-file job; submit one item URL per job")
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
		YoutubePlayer:     configString(ctx, "youtube_player_client", "android"),
		YTDLPExtraArgs:    parseExtraArgs(configString(ctx, "ytdlp_extra_args", "")),
		ProxyURL:          a.proxyURL(),
		MaxDownloadBytes:  int64(configInt(ctx, "max_download_mb", 512)) * 1024 * 1024,
		Tags:              stringSliceArg(args, "tags"),
	}
	j := downloadJob{ID: id, ProjectID: projectID, URL: rawURL, Status: statusQueued, Stage: stageQueued, Mode: mode, Quality: quality, FormatID: req.FormatID, SourceProfileID: req.SourceProfileID, StorageFolder: folder, StorageVisibility: visibility}
	if err := insertDownload(context.Background(), ctx.AppDB(), j); err != nil {
		return nil, err
	}
	a.emitDownloadJob(ctx, "download.created", j)
	runCtx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancels[id] = runningDownload{cancel: cancel, projectID: projectID}
	a.mu.Unlock()
	a.wg.Add(1)
	go a.runDownload(runCtx, ctx.WithProject(projectID), id, req)
	return map[string]any{"job": j}, nil
}

func (a *App) runDownload(runCtx context.Context, ctx *sdk.AppCtx, id string, req downloadRequest) {
	db := ctx.AppDB()
	defer func() {
		pruneDownloadLogs(context.Background(), db, id, 200)
		a.mu.Lock()
		delete(a.cancels, id)
		a.mu.Unlock()
		a.wg.Done()
	}()
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-runCtx.Done():
		a.finishError(ctx, req.ProjectID, id, runCtx.Err())
		return
	}
	_ = setDownloadRunning(context.Background(), db, id)
	appendLog(context.Background(), db, id, "info", "download started")
	a.emitDownload(ctx, "download.started", req.ProjectID, map[string]any{"id": id, "status": statusRunning, "stage": stageDownloading, "progress": 0})

	jobDir := filepath.Join(a.dataDir, "jobs", id)
	if err := os.MkdirAll(jobDir, 0700); err != nil {
		a.finishError(ctx, req.ProjectID, id, err)
		return
	}
	defer os.RemoveAll(jobDir)
	var cookieFile string
	if req.SourceProfileID != "" {
		payload, err := a.profilePayload(runCtx, ctx, req.ProjectID, req.SourceProfileID)
		if err != nil {
			a.finishError(ctx, req.ProjectID, id, err)
			return
		}
		cookieFile, err = writeCookieFile(jobDir, payload)
		if err != nil {
			a.finishError(ctx, req.ProjectID, id, err)
			return
		}
	}

	printed := make([]string, 0, 4)
	var lastErr string
	lastProgress := -1.0
	currentStage := stageDownloading
	var stateMu sync.Mutex
	err := a.runner.Run(runCtx, a.ytdlpPath, buildDownloadArgs(req, jobDir, cookieFile), func(line string) {
		line = trimLogLine(line)
		stateMu.Lock()
		defer stateMu.Unlock()
		if strings.HasPrefix(line, "__APTEVA_META__") {
			meta := strings.TrimPrefix(line, "__APTEVA_META__")
			title, extractor, _ := strings.Cut(meta, "|")
			_ = setDownloadProbe(context.Background(), db, id, title, extractor)
			a.emitDownload(ctx, "download.stage", req.ProjectID, map[string]any{"id": id, "status": statusRunning, "stage": currentStage, "progress": max(lastProgress, 0), "title": title, "extractor": extractor})
			return
		}
		if strings.HasPrefix(line, "__APTEVA_FILE__") {
			printed = append(printed, line)
			return
		}
		if p, ok := parseProgressLine(line); ok {
			progress := min(p*0.84, 84)
			if lastProgress < 0 || progress-lastProgress >= 1 || progress >= 84 {
				lastProgress = progress
				_ = updateDownloadStage(context.Background(), db, id, stageDownloading, progress)
				a.emitDownload(ctx, "download.progress", req.ProjectID, map[string]any{"id": id, "status": statusRunning, "stage": stageDownloading, "progress": progress})
			}
			return
		}
		if isPostprocessingLine(line) && currentStage != stagePostprocessing {
			currentStage = stagePostprocessing
			lastProgress = max(lastProgress, 85)
			_ = updateDownloadStage(context.Background(), db, id, currentStage, lastProgress)
			a.emitDownload(ctx, "download.stage", req.ProjectID, map[string]any{"id": id, "status": statusRunning, "stage": currentStage, "progress": lastProgress})
		}
		appendLog(context.Background(), db, id, "stdout", line)
	}, func(line string) {
		line = trimLogLine(line)
		stateMu.Lock()
		lastErr = line
		stateMu.Unlock()
		appendLog(context.Background(), db, id, "stderr", line)
	})
	if err != nil {
		stateMu.Lock()
		message := lastErr
		stateMu.Unlock()
		if errors.Is(err, context.Canceled) {
			a.finishError(ctx, req.ProjectID, id, err)
		} else {
			if message == "" {
				message = err.Error()
			}
			a.finishError(ctx, req.ProjectID, id, errors.New(message))
		}
		return
	}
	a.setDownloadStage(ctx, req.ProjectID, id, stagePostprocessing, 86)
	output, err := findOutputFile(jobDir, printed)
	if err != nil {
		a.finishError(ctx, req.ProjectID, id, err)
		return
	}
	if err := a.uploadOutput(runCtx, ctx, id, req, output); err != nil {
		a.finishError(ctx, req.ProjectID, id, err)
		return
	}
	appendLog(context.Background(), db, id, "info", "download completed")
	if job, err := getDownload(context.Background(), db, req.ProjectID, id); err == nil {
		a.emitDownloadJob(ctx, "download.completed", job)
	} else {
		a.emitDownload(ctx, "download.completed", req.ProjectID, map[string]any{"id": id, "status": statusCompleted})
	}
}

func isPostprocessingLine(line string) bool {
	for _, prefix := range []string{"[ExtractAudio]", "[Merger]", "[VideoConvertor]", "[AudioConvertor]", "[Fixup", "[Embed"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func (a *App) setDownloadStage(ctx *sdk.AppCtx, projectID, id, stage string, progress float64) {
	_ = updateDownloadStage(context.Background(), ctx.AppDB(), id, stage, progress)
	a.emitDownload(ctx, "download.stage", projectID, map[string]any{"id": id, "status": statusRunning, "stage": stage, "progress": progress})
}

func (a *App) uploadOutput(runCtx context.Context, ctx *sdk.AppCtx, id string, req downloadRequest, path string) error {
	if err := runCtx.Err(); err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if req.MaxDownloadBytes > 0 && st.Size() > req.MaxDownloadBytes {
		return fmt.Errorf("output is %d bytes, over max_download_mb", st.Size())
	}
	name := filepath.Base(path)
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	tags := append([]string{"media-downloader", req.Mode}, req.Tags...)
	a.setDownloadStage(ctx, req.ProjectID, id, stagePreparing, 88)
	lastHashProgress := -1.0
	lastUploadProgress := -1.0
	got, err := uploadFileMultipart(runCtx, req.ProjectID, path, name, ctype, req.StorageFolder, req.StorageVisibility, tags,
		func(progress float64) {
			if progress-lastHashProgress >= 0.1 || progress >= 1 {
				lastHashProgress = progress
				a.setDownloadStage(ctx, req.ProjectID, id, stagePreparing, 88+progress*4)
			}
		},
		func(progress float64) {
			if progress-lastUploadProgress >= 0.01 || progress >= 1 {
				lastUploadProgress = progress
				a.setDownloadStage(ctx, req.ProjectID, id, stageUploading, 92+progress*7)
			}
		})
	if err != nil {
		return err
	}
	return completeDownload(context.Background(), ctx.AppDB(), id, name, st.Size(), got.ID, got.URL)
}

type storageUploadResult struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func uploadFileMultipart(ctx context.Context, projectID, path, name, contentType, folder, visibility string, tags []string, hashProgress, uploadProgress func(float64)) (storageUploadResult, error) {
	client := newStorageHTTPClient()
	if client.base == "" {
		return storageUploadResult{}, errors.New("APTEVA_GATEWAY_URL not set; cannot stream large file to storage")
	}
	if client.token == "" {
		return storageUploadResult{}, errors.New("APTEVA_APP_TOKEN not set; cannot authenticate storage upload")
	}
	f, err := os.Open(path)
	if err != nil {
		return storageUploadResult{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return storageUploadResult{}, err
	}
	sha, err := hashFile(ctx, f, st.Size(), hashProgress)
	if err != nil {
		return storageUploadResult{}, fmt.Errorf("hash output: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return storageUploadResult{}, err
	}
	return client.uploadMultipart(ctx, projectID, f, name, contentType, folder, visibility, tags, st.Size(), sha, uploadProgress)
}

func (a *App) finishError(ctx *sdk.AppCtx, projectID, id string, err error) {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	status := statusFailed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = statusCanceled
		msg = "download canceled"
	}
	appendLog(context.Background(), a.ctx.AppDB(), id, "error", msg)
	_ = failDownload(context.Background(), a.ctx.AppDB(), id, status, msg)
	a.emitDownload(ctx, "download."+status, projectID, map[string]any{"id": id, "status": status, "stage": terminalStage(status), "error": msg})
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
	projectID := projectScope(ctx, args)
	job, err := getDownload(context.Background(), ctx.AppDB(), projectID, id)
	if err != nil {
		return nil, err
	}
	if job.Status != statusQueued && job.Status != statusRunning {
		return map[string]any{"canceled": false, "reason": "job is not running"}, nil
	}
	a.mu.Lock()
	running := a.cancels[id]
	a.mu.Unlock()
	if running.cancel == nil || running.projectID != projectID {
		return map[string]any{"canceled": false, "reason": "job is not running"}, nil
	}
	running.cancel()
	appendLog(context.Background(), ctx.AppDB(), id, "info", "cancel requested")
	a.emitDownload(ctx, "download.cancel_requested", projectID, map[string]any{"id": id, "status": statusRunning})
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
	a.emitProfile(ctx, "profile.created", public)
	if testURL := strArg(args, "test_url"); testURL != "" {
		if err := a.guard(ctx, args, testURL); err != nil {
			_ = markProfileValidated(context.Background(), ctx.AppDB(), id, p.ProjectID, err.Error())
			updated, _ := getProfile(context.Background(), ctx.AppDB(), p.ProjectID, id)
			a.emitProfile(ctx, "profile.validated", updated.sourceProfile)
			return map[string]any{"profile": updated.sourceProfile, "validated": false, "validation_error": err.Error()}, nil
		}
		if err := a.validateProfileAgainstURL(ctx, p.ProjectID, id, testURL); err != nil {
			updated, _ := getProfile(context.Background(), ctx.AppDB(), p.ProjectID, id)
			return map[string]any{"profile": updated.sourceProfile, "validated": false, "validation_error": err.Error()}, nil
		}
		updated, _ := getProfile(context.Background(), ctx.AppDB(), p.ProjectID, id)
		return map[string]any{"profile": updated.sourceProfile, "validated": true}, nil
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
		profile, _ := getProfile(context.Background(), ctx.AppDB(), projectScope(ctx, args), profileID)
		return map[string]any{"valid": false, "profile": profile.sourceProfile, "error": err.Error()}, nil
	}
	profile, _ := getProfile(context.Background(), ctx.AppDB(), projectScope(ctx, args), profileID)
	return map[string]any{"valid": true, "profile": profile.sourceProfile}, nil
}

func (a *App) validateProfileAgainstURL(ctx *sdk.AppCtx, projectID, profileID, rawURL string) error {
	tmp, cleanup, err := a.cookieFileForProfile(context.Background(), ctx, projectID, profileID)
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = probeMedia(context.Background(), a.runner, a.ytdlpPath, rawURL, tmp, parseExtraArgs(configString(ctx, "ytdlp_extra_args", "")), a.proxyURL())
	lastErr := ""
	if err != nil {
		lastErr = err.Error()
	}
	_ = markProfileValidated(context.Background(), ctx.AppDB(), profileID, projectID, lastErr)
	profile, getErr := getProfile(context.Background(), ctx.AppDB(), projectID, profileID)
	if getErr == nil {
		a.emitProfile(ctx, "profile.validated", profile.sourceProfile)
	} else {
		payload := map[string]any{"id": profileID, "status": "valid", "last_validated_at": nowRFC3339()}
		if lastErr != "" {
			payload["status"] = "invalid"
			payload["last_error"] = lastErr
		}
		a.emitProfileData(ctx, "profile.validated", projectID, payload)
	}
	return err
}

func (a *App) toolProfileDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID := projectScope(ctx, args)
	profileID := strArg(args, "profile_id")
	if err := deleteProfile(context.Background(), ctx.AppDB(), profileID, projectID); err != nil {
		return nil, err
	}
	a.emitProfileData(ctx, "profile.deleted", projectID, map[string]any{"id": profileID})
	return map[string]any{"deleted": true}, nil
}

func (a *App) emitDownloadJob(ctx *sdk.AppCtx, topic string, job downloadJob) {
	a.emitDownload(ctx, topic, job.ProjectID, map[string]any{
		"id":                 job.ID,
		"status":             job.Status,
		"stage":              job.Stage,
		"progress":           job.Progress,
		"url":                job.URL,
		"title":              job.Title,
		"mode":               job.Mode,
		"quality":            job.Quality,
		"format_id":          job.FormatID,
		"extractor":          job.Extractor,
		"source_profile_id":  job.SourceProfileID,
		"storage_folder":     job.StorageFolder,
		"storage_visibility": job.StorageVisibility,
		"storage_file_id":    job.StorageFileID,
		"storage_url":        job.StorageURL,
		"output_name":        job.OutputName,
		"output_bytes":       job.OutputBytes,
		"updated_at":         job.UpdatedAt,
		"error":              job.Error,
	})
}

func (a *App) emitDownload(ctx *sdk.AppCtx, topic, projectID string, data map[string]any) {
	if ctx == nil {
		ctx = a.ctx
	}
	if ctx == nil {
		return
	}
	if projectID != "" {
		ctx.EmitWithProject(topic, projectID, data)
		return
	}
	ctx.Emit(topic, data)
}

func (a *App) emitProfile(ctx *sdk.AppCtx, topic string, p sourceProfile) {
	a.emitProfileData(ctx, topic, p.ProjectID, map[string]any{
		"id":                p.ID,
		"name":              p.Name,
		"provider":          p.Provider,
		"auth_type":         p.AuthType,
		"status":            p.Status,
		"last_validated_at": p.LastValidatedAt,
		"last_error":        p.LastError,
	})
}

func (a *App) emitProfileData(ctx *sdk.AppCtx, topic, projectID string, data map[string]any) {
	if ctx == nil {
		ctx = a.ctx
	}
	if ctx == nil {
		return
	}
	if projectID != "" {
		ctx.EmitWithProject(topic, projectID, data)
		return
	}
	ctx.Emit(topic, data)
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

func (a *App) proxyURL() string {
	if a.proxy == nil {
		return ""
	}
	return a.proxy.url
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

func (a *App) httpCreateJob(w http.ResponseWriter, r *http.Request) {
	args, err := readJSONArgs(w, r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	withHTTPProject(r, args)
	out, err := a.toolDownload(a.ctx.WithProject(strArg(args, "_project_id")), args)
	writeJSON(w, out, err)
}

func (a *App) httpProbe(w http.ResponseWriter, r *http.Request) {
	args, err := readJSONArgs(w, r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	withHTTPProject(r, args)
	out, err := a.toolProbe(a.ctx.WithProject(strArg(args, "_project_id")), args)
	writeJSON(w, out, err)
}

func (a *App) httpJobAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/jobs/")
	if !strings.HasSuffix(path, "/cancel") {
		writeJSON(w, nil, errNotFound)
		return
	}
	id := strings.TrimSuffix(path, "/cancel")
	id = strings.TrimSuffix(id, "/")
	args := map[string]any{"job_id": id}
	withHTTPProject(r, args)
	out, err := a.toolCancel(a.ctx.WithProject(strArg(args, "_project_id")), args)
	writeJSON(w, out, err)
}

func (a *App) httpProfiles(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" && a.ctx != nil {
		projectID = a.ctx.CurrentProject()
	}
	profiles, err := listProfiles(r.Context(), a.ctx.AppDB(), projectID)
	writeJSON(w, map[string]any{"profiles": profiles}, err)
}

func (a *App) httpCreateProfile(w http.ResponseWriter, r *http.Request) {
	args, err := readJSONArgs(w, r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	withHTTPProject(r, args)
	out, err := a.toolProfileCreate(a.ctx.WithProject(strArg(args, "_project_id")), args)
	writeJSON(w, out, err)
}

func (a *App) httpProfileAction(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/profiles/"), "/")
	if !strings.HasSuffix(path, "/validate") {
		writeJSON(w, nil, errNotFound)
		return
	}
	id := strings.TrimSuffix(path, "/validate")
	id = strings.TrimSuffix(id, "/")
	args, err := readJSONArgs(w, r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	args["profile_id"] = id
	withHTTPProject(r, args)
	out, err := a.toolProfileValidate(a.ctx.WithProject(strArg(args, "_project_id")), args)
	writeJSON(w, out, err)
}

func (a *App) httpDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/profiles/")
	id = strings.TrimSuffix(id, "/")
	args := map[string]any{"profile_id": id}
	withHTTPProject(r, args)
	out, err := a.toolProfileDelete(a.ctx.WithProject(strArg(args, "_project_id")), args)
	writeJSON(w, out, err)
}

func readJSONArgs(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024*1024)
	var args map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return nil, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("request body must contain one JSON object")
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func withHTTPProject(r *http.Request, args map[string]any) {
	if args == nil {
		return
	}
	if _, ok := args["_project_id"]; ok {
		return
	}
	if p := strings.TrimSpace(r.URL.Query().Get("project_id")); p != "" {
		args["_project_id"] = p
	}
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
