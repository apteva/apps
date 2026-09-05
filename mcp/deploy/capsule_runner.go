package main

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	runnerArtifactFilename = "artifact.zip"
	runnerLogFilename      = "build.log"
	runnerStateFilename    = "state.json"
	defaultRunnerRetention = 24 * time.Hour
)

type capsuleRunner struct {
	dataDir   string
	tokenHash [sha256.Size]byte
	client    *http.Client
	sem       chan struct{}

	mu   sync.Mutex
	jobs map[string]*runnerJob
}

type runnerJob struct {
	Response       runnerJobResponse `json:"response"`
	ArtifactPath   string            `json:"-"`
	LogPath        string            `json:"-"`
	IdempotencyKey string            `json:"idempotency_key"`
	cancel         context.CancelFunc
}

func newCapsuleRunner(dataDir, token string, concurrency int) (*capsuleRunner, error) {
	dataDir = strings.TrimSpace(dataDir)
	token = strings.TrimSpace(token)
	if dataDir == "" {
		return nil, errors.New("runner data directory is required")
	}
	if token == "" {
		return nil, errors.New("runner authentication token is required")
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "jobs"), 0o700); err != nil {
		return nil, err
	}
	runner := &capsuleRunner{
		dataDir: dataDir, tokenHash: sha256.Sum256([]byte(token)),
		client: &http.Client{Timeout: 30 * time.Minute},
		sem:    make(chan struct{}, concurrency), jobs: map[string]*runnerJob{},
	}
	if err := runner.loadJobs(); err != nil {
		return nil, err
	}
	runner.pruneJobs(time.Now())
	return runner, nil
}

func (r *capsuleRunner) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if req.URL.Path == "/health" && req.Method == http.MethodGet {
		r.handleHealth(w)
		return
	}
	if !r.authorized(req) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		runnerWriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if req.URL.Path == "/v1/jobs" && req.Method == http.MethodPost {
		r.handleSubmit(w, req)
		return
	}
	id, action, ok := parseRunnerJobPath(req.URL.Path)
	if !ok {
		runnerWriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch {
	case action == "" && req.Method == http.MethodGet:
		r.handleInspect(w, id)
	case action == "" && req.Method == http.MethodDelete:
		r.handleCancel(w, id)
	case action == "artifact" && req.Method == http.MethodGet:
		r.handleArtifact(w, req, id)
	case action == "log" && req.Method == http.MethodGet:
		r.handleLog(w, req, id)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		runnerWriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (r *capsuleRunner) authorized(req *http.Request) bool {
	const prefix = "Bearer "
	header := req.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	actual := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(header, prefix))))
	return subtle.ConstantTimeCompare(actual[:], r.tokenHash[:]) == 1
}

func (r *capsuleRunner) handleHealth(w http.ResponseWriter) {
	r.mu.Lock()
	active := 0
	for _, job := range r.jobs {
		if job.Response.Status == "queued" || job.Response.Status == "running" {
			active++
		}
	}
	r.mu.Unlock()
	_, xcodeErr := exec.LookPath("xcodebuild")
	runnerWriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "protocol": runnerProtocolVersion, "os": runtime.GOOS,
		"arch": runtime.GOARCH, "xcode": xcodeErr == nil,
		"active_jobs": active, "capacity": cap(r.sem),
	})
}

func (r *capsuleRunner) handleSubmit(w http.ResponseWriter, req *http.Request) {
	r.pruneJobs(time.Now())
	req.Body = http.MaxBytesReader(w, req.Body, 2<<20)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	var input runnerJobRequest
	if err := decoder.Decode(&input); err != nil {
		runnerWriteError(w, http.StatusBadRequest, "invalid job request: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		clearRunnerCredentials(&input.Credentials)
		runnerWriteError(w, http.StatusBadRequest, "job request must contain one JSON object")
		return
	}
	if err := validateRunnerJobRequest(input); err != nil {
		clearRunnerCredentials(&input.Credentials)
		runnerWriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	r.mu.Lock()
	for _, existing := range r.jobs {
		if existing.IdempotencyKey == input.IdempotencyKey {
			response := existing.Response
			r.mu.Unlock()
			clearRunnerCredentials(&input.Credentials)
			runnerWriteJSON(w, http.StatusOK, response)
			return
		}
	}
	id, err := randomRunnerJobID()
	if err != nil {
		r.mu.Unlock()
		clearRunnerCredentials(&input.Credentials)
		runnerWriteError(w, http.StatusInternalServerError, "generate job id")
		return
	}
	jobDir := r.jobDir(id)
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		r.mu.Unlock()
		clearRunnerCredentials(&input.Credentials)
		runnerWriteError(w, http.StatusInternalServerError, "create job directory")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ctx, cancel := context.WithCancel(context.Background())
	job := &runnerJob{
		Response: runnerJobResponse{
			Protocol: runnerProtocolVersion, ID: id, IdempotencyKey: input.IdempotencyKey,
			Status: "queued", CreatedAt: now,
		},
		ArtifactPath:   filepath.Join(jobDir, runnerArtifactFilename),
		LogPath:        filepath.Join(jobDir, runnerLogFilename),
		IdempotencyKey: input.IdempotencyKey, cancel: cancel,
	}
	r.jobs[id] = job
	r.persistJobLocked(job)
	response := job.Response
	r.mu.Unlock()

	go r.runJob(ctx, job, input)
	runnerWriteJSON(w, http.StatusAccepted, response)
}

func (r *capsuleRunner) handleInspect(w http.ResponseWriter, id string) {
	r.mu.Lock()
	job := r.jobs[id]
	if job == nil {
		r.mu.Unlock()
		runnerWriteError(w, http.StatusNotFound, "job not found")
		return
	}
	response := job.Response
	r.mu.Unlock()
	runnerWriteJSON(w, http.StatusOK, response)
}

func (r *capsuleRunner) handleCancel(w http.ResponseWriter, id string) {
	r.mu.Lock()
	job := r.jobs[id]
	if job == nil {
		r.mu.Unlock()
		runnerWriteError(w, http.StatusNotFound, "job not found")
		return
	}
	switch job.Response.Status {
	case "queued", "running":
		if job.cancel != nil {
			job.cancel()
		}
		job.Response.Status = "cancelled"
		job.Response.Error = "cancelled by operator"
		job.Response.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		job.Response.ArtifactReady = false
		_ = os.Remove(job.ArtifactPath)
		r.persistJobLocked(job)
	}
	response := job.Response
	r.mu.Unlock()
	runnerWriteJSON(w, http.StatusOK, response)
}

func (r *capsuleRunner) handleArtifact(w http.ResponseWriter, req *http.Request, id string) {
	r.mu.Lock()
	job := r.jobs[id]
	if job == nil {
		r.mu.Unlock()
		runnerWriteError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Response.Status != "succeeded" || !job.Response.ArtifactReady {
		r.mu.Unlock()
		runnerWriteError(w, http.StatusConflict, "artifact is not ready")
		return
	}
	path := job.ArtifactPath
	r.mu.Unlock()
	file, err := os.Open(path)
	if err != nil {
		runnerWriteError(w, http.StatusGone, "artifact unavailable")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		runnerWriteError(w, http.StatusGone, "artifact unavailable")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="artifact.zip"`)
	w.Header().Set("Content-Type", "application/zip")
	http.ServeContent(w, req, runnerArtifactFilename, info.ModTime(), file)
}

func (r *capsuleRunner) handleLog(w http.ResponseWriter, req *http.Request, id string) {
	r.mu.Lock()
	job := r.jobs[id]
	if job == nil {
		r.mu.Unlock()
		runnerWriteError(w, http.StatusNotFound, "job not found")
		return
	}
	path := job.LogPath
	r.mu.Unlock()
	file, err := os.Open(path)
	if err != nil {
		runnerWriteError(w, http.StatusNotFound, "log unavailable")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		runnerWriteError(w, http.StatusNotFound, "log unavailable")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeContent(w, req, runnerLogFilename, info.ModTime(), file)
}

func (r *capsuleRunner) runJob(ctx context.Context, job *runnerJob, input runnerJobRequest) {
	defer clearRunnerCredentials(&input.Credentials)
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return
	}
	if ctx.Err() != nil {
		return
	}
	r.updateJob(job.Response.ID, func(current *runnerJob) {
		current.Response.Status = "running"
		current.Response.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	})

	jobDir := r.jobDir(job.Response.ID)
	sourceArchive := filepath.Join(jobDir, sourceCapsuleFilename)
	sourceDir := filepath.Join(jobDir, "src")
	distDir := filepath.Join(jobDir, "dist")
	_ = os.RemoveAll(sourceDir)
	_ = os.RemoveAll(distDir)
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		r.finishJob(job.Response.ID, "failed", err)
		return
	}
	if err := os.MkdirAll(distDir, 0o700); err != nil {
		r.finishJob(job.Response.ID, "failed", err)
		return
	}
	defer os.RemoveAll(sourceDir)
	defer os.Remove(sourceArchive)

	logFile, err := os.OpenFile(job.LogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		r.finishJob(job.Response.ID, "failed", err)
		return
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "=== capsule runner job %s (%s) ===\n", job.Response.ID, runnerBuildLabel(input.Build))

	if err := r.downloadSource(ctx, input.Source, sourceArchive); err != nil {
		r.finishJob(job.Response.ID, runnerTerminalStatus(ctx, "failed"), err)
		return
	}
	fmt.Fprintf(logFile, "verified source capsule sha256=%s size=%d\n", input.Source.SHA256, input.Source.Size)
	if err := extractRunnerSource(sourceArchive, sourceDir); err != nil {
		r.finishJob(job.Response.ID, "failed", err)
		return
	}
	if ctx.Err() != nil {
		r.finishJob(job.Response.ID, "cancelled", ctx.Err())
		return
	}

	framework := strings.TrimSpace(input.Build.Framework)
	if framework == "" {
		framework = detectFramework(sourceDir)
	}
	builder, err := builderFor(framework)
	if err != nil {
		r.finishJob(job.Response.ID, "failed", err)
		return
	}
	_, err = builder.Build(sourceDir, distDir, BuildOverrides{
		BuildCmd: input.Build.BuildCmd, Env: input.Build.Env,
		TargetConfigJSON: input.Build.TargetConfigJSON,
		Credentials:      input.Credentials, Context: ctx,
	}, logFile)
	if err != nil {
		r.finishJob(job.Response.ID, runnerTerminalStatus(ctx, "failed"), err)
		return
	}
	if ctx.Err() != nil {
		_ = os.RemoveAll(distDir)
		r.finishJob(job.Response.ID, "cancelled", ctx.Err())
		return
	}
	if err := zipDirectoryTree(distDir, "", job.ArtifactPath); err != nil {
		r.finishJob(job.Response.ID, "failed", err)
		return
	}
	r.finishJob(job.Response.ID, "succeeded", nil)
}

func runnerTerminalStatus(ctx context.Context, fallback string) string {
	if ctx.Err() != nil {
		return "cancelled"
	}
	return fallback
}

func (r *capsuleRunner) downloadSource(ctx context.Context, source runnerSource, destination string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Apteva-Capsule-Runner/1")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("download source capsule: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("source capsule returned HTTP %d", resp.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, source.Size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != source.Size {
		return fmt.Errorf("source capsule size mismatch: got %d, expected %d", written, source.Size)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(actual, source.SHA256) {
		return errors.New("source capsule SHA-256 mismatch")
	}
	return nil
}

func validateRunnerJobRequest(input runnerJobRequest) error {
	if input.Protocol != runnerProtocolVersion {
		return fmt.Errorf("unsupported protocol %q", input.Protocol)
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 256 {
		return errors.New("idempotency_key is required and must not exceed 256 characters")
	}
	if input.Source.Format != sourceCapsuleFormat {
		return fmt.Errorf("unsupported source format %q", input.Source.Format)
	}
	if input.Source.Size < 1 || input.Source.Size > maxSourceCapsuleBytes {
		return fmt.Errorf("source size must be between 1 and %d bytes", maxSourceCapsuleBytes)
	}
	if len(input.Source.SHA256) != sha256.Size*2 {
		return errors.New("source sha256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(input.Source.SHA256); err != nil {
		return errors.New("source sha256 must contain 64 hexadecimal characters")
	}
	parsed, err := urlParseAbsolute(input.Source.URL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return errors.New("source URL must use https except for loopback testing")
	}
	if parsed.User != nil {
		return errors.New("source URL cannot contain credentials")
	}
	if strings.TrimSpace(input.Build.Deployment) == "" {
		return errors.New("build deployment is required")
	}
	if len(input.Build.Env) > 512 {
		return errors.New("build environment exceeds 512 variables")
	}
	return nil
}

func urlParseAbsolute(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("source URL must be absolute")
	}
	return parsed, nil
}

func extractRunnerSource(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	return extractBoundedZip(&reader.Reader, destination, maxSourceCapsuleBytes, false)
}

func zipDirectoryTree(root, prefix, destination string) error {
	temp := destination + ".tmp"
	_ = os.Remove(temp)
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	walkRoot := root
	if prefix != "" {
		walkRoot = filepath.Join(root, prefix)
	}
	err = filepath.Walk(walkRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == walkRoot {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if _, err := confinedDownloadArtifactPath(root, pathRelative(root, path)); err != nil {
				return err
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if info.IsDir() {
			name += "/"
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = name
		header.Method = zip.Deflate
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err == nil && filepath.IsAbs(target) {
				err = errors.New("artifact symlink must use a relative target")
			}
			if err != nil {
				return err
			}
			_, err = io.WriteString(entry, target)
			return err
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	closeWriterErr := writer.Close()
	closeFileErr := file.Close()
	if err != nil {
		_ = os.Remove(temp)
		return err
	}
	if closeWriterErr != nil {
		_ = os.Remove(temp)
		return closeWriterErr
	}
	if closeFileErr != nil {
		_ = os.Remove(temp)
		return closeFileErr
	}
	return os.Rename(temp, destination)
}

func (r *capsuleRunner) finishJob(id, status string, buildErr error) {
	r.updateJob(id, func(job *runnerJob) {
		if job.Response.Status == "cancelled" {
			return
		}
		job.Response.Status = status
		job.Response.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		job.Response.ArtifactReady = status == "succeeded"
		if buildErr != nil {
			job.Response.Error = buildErr.Error()
		}
		job.cancel = nil
	})
}

func (r *capsuleRunner) updateJob(id string, update func(*runnerJob)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if job := r.jobs[id]; job != nil {
		update(job)
		r.persistJobLocked(job)
	}
}

func (r *capsuleRunner) persistJobLocked(job *runnerJob) {
	body, err := json.Marshal(job)
	if err != nil {
		return
	}
	path := filepath.Join(r.jobDir(job.Response.ID), runnerStateFilename)
	temp := path + ".tmp"
	if os.WriteFile(temp, body, 0o600) == nil {
		_ = os.Rename(temp, path)
	}
}

func (r *capsuleRunner) loadJobs() error {
	entries, err := os.ReadDir(filepath.Join(r.dataDir, "jobs"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(r.dataDir, "jobs", entry.Name(), runnerStateFilename))
		if err != nil {
			continue
		}
		var job runnerJob
		if json.Unmarshal(body, &job) != nil || job.Response.ID != entry.Name() {
			continue
		}
		job.ArtifactPath = filepath.Join(r.jobDir(entry.Name()), runnerArtifactFilename)
		job.LogPath = filepath.Join(r.jobDir(entry.Name()), runnerLogFilename)
		if job.Response.Status == "queued" || job.Response.Status == "running" {
			job.Response.Status = "failed"
			job.Response.Error = "runner restarted while build was active"
			job.Response.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			job.Response.ArtifactReady = false
		}
		r.jobs[job.Response.ID] = &job
		r.persistJobLocked(&job)
	}
	return nil
}

func (r *capsuleRunner) pruneJobs(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, job := range r.jobs {
		if job.Response.Status == "queued" || job.Response.Status == "running" {
			continue
		}
		finished, _ := time.Parse(time.RFC3339Nano, job.Response.FinishedAt)
		if !finished.IsZero() && now.Sub(finished) > defaultRunnerRetention {
			delete(r.jobs, id)
			_ = os.RemoveAll(r.jobDir(id))
		}
	}
}

func (r *capsuleRunner) jobDir(id string) string {
	return filepath.Join(r.dataDir, "jobs", id)
}

func parseRunnerJobPath(raw string) (string, string, bool) {
	parts := strings.Split(strings.Trim(raw, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "v1" || parts[1] != "jobs" {
		return "", "", false
	}
	id := parts[2]
	if len(id) != 32 {
		return "", "", false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", "", false
	}
	if len(parts) == 3 {
		return id, "", true
	}
	if parts[3] != "artifact" && parts[3] != "log" {
		return "", "", false
	}
	return id, parts[3], true
}

func randomRunnerJobID() (string, error) {
	body := make([]byte, 16)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return hex.EncodeToString(body), nil
}

func runnerWriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func runnerWriteError(w http.ResponseWriter, status int, message string) {
	runnerWriteJSON(w, status, map[string]string{"error": message})
}

func runCapsuleRunner(args []string) error {
	flags := flag.NewFlagSet("capsule-runner", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:9075", "runner listen address")
	dataDir := flags.String("data-dir", "", "runner job data directory")
	tokenFile := flags.String("token-file", "", "file containing the shared runner token")
	tokenEnv := flags.String("token-env", defaultRunnerTokenEnv, "environment variable containing the shared runner token")
	concurrency := flags.Int("concurrency", 1, "maximum concurrent builds")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" {
		home, _ := os.UserHomeDir()
		*dataDir = filepath.Join(home, ".apteva", "deploy-runner")
	}
	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if *tokenFile != "" {
		body, err := os.ReadFile(*tokenFile)
		if err != nil {
			return fmt.Errorf("read runner token file: %w", err)
		}
		token = strings.TrimSpace(string(body))
	}
	if len(token) < 32 {
		return errors.New("runner token must contain at least 32 characters")
	}
	runner, err := newCapsuleRunner(*dataDir, token, *concurrency)
	if err != nil {
		return err
	}
	_ = os.Unsetenv(*tokenEnv)
	_ = os.Unsetenv(defaultRunnerTokenEnv)
	token = strings.Repeat("0", len(token))
	server := &http.Server{
		Addr: *listen, Handler: runner,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	fmt.Fprintf(os.Stderr, "Apteva capsule runner listening on %s data=%s\n", *listen, *dataDir)
	return server.ListenAndServe()
}
