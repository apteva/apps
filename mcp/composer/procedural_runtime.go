package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	procedureJobVersion = "composer/procedural/v1"
	procedureLogLimit   = 64 * 1024
	procedureInputLimit = int64(2 << 30)
)

type procedureRunner func(context.Context, *sdk.AppCtx, *ProcedureRevisionRecord, *ProceduralAsset, Output, procedureTimeline) (procedureRunResult, error)

type procedureTimeline struct {
	JobID            string  `json:"-"`
	CompositionStart float64 `json:"composition_start"`
	SourceStart      float64 `json:"source_start"`
	Duration         float64 `json:"duration"`
	FPS              int     `json:"fps"`
}

type procedureRunResult struct {
	LocalPath       string
	Kind            string
	DurationSeconds float64
	Logs            string
	RuntimeJobID    string
	Cleanup         func()
}

type procedureJobAsset struct {
	Path   string `json:"path"`
	Kind   string `json:"kind,omitempty"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type procedureJob struct {
	APIVersion string                       `json:"api_version"`
	JobID      string                       `json:"job_id"`
	Target     string                       `json:"target"`
	Entrypoint string                       `json:"entrypoint"`
	Timeline   procedureTimeline            `json:"timeline"`
	Canvas     map[string]any               `json:"canvas"`
	Parameters map[string]any               `json:"parameters"`
	Assets     map[string]procedureJobAsset `json:"assets"`
	Output     map[string]any               `json:"output"`
	Tools      map[string]string            `json:"tools"`
}

type procedureResultManifest struct {
	APIVersion string `json:"api_version"`
	Artifacts  []struct {
		Name     string  `json:"name"`
		Kind     string  `json:"kind"`
		Path     string  `json:"path"`
		MimeType string  `json:"mime_type,omitempty"`
		Duration float64 `json:"duration,omitempty"`
	} `json:"artifacts"`
}

type boundedProcedureLog struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *boundedProcedureLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := len(p)
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			w.truncated = true
		}
		_, _ = w.buf.Write(p)
	} else if len(p) > 0 {
		w.truncated = true
	}
	return written, nil
}

func (w *boundedProcedureLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	value := w.buf.String()
	if w.truncated {
		value += "\n[logs truncated]"
	}
	return redactSecrets(value)
}

func (a *App) materializeProceduralAssets(ctx context.Context, appCtx *sdk.AppCtx, edit *Edit, output Output, compositionID int64, projectID string) (bool, error) {
	if edit == nil {
		return false, nil
	}
	if issues := validateProcedureReferences(appCtx.AppDB(), projectID, edit); len(issues) > 0 {
		return false, errors.New(strings.Join(issues, "; "))
	}
	changed := false
	for trackIndex := range edit.Timeline.Tracks {
		track := &edit.Timeline.Tracks[trackIndex]
		for clipIndex := range track.Clips {
			clip := &track.Clips[clipIndex]
			binding := clip.Asset.Procedure
			if binding == nil {
				continue
			}
			_, revision, err := getProcedure(appCtx.AppDB(), projectID, binding.ProcedureID, binding.Revision)
			if err != nil {
				return changed, fmt.Errorf("clip %s procedure: %w", clip.UID, err)
			}
			if binding.Revision == 0 {
				binding.Revision = revision.Revision
				changed = true
			}
			binding.OutputKind = strings.ToLower(strings.TrimSpace(binding.OutputKind))
			if binding.OutputKind == "" {
				binding.OutputKind = revision.OutputKind
				changed = true
			}
			if binding.Target == "" {
				binding.Target = revision.Target
				changed = true
			}
			binding.Target = strings.ToLower(strings.TrimSpace(binding.Target))
			if binding.Target != revision.Target {
				return changed, fmt.Errorf("clip %s procedure target %q does not match revision target %q", clip.UID, binding.Target, revision.Target)
			}
			if binding.OutputKind != revision.OutputKind {
				return changed, fmt.Errorf("clip %s procedure output kind %q does not match revision kind %q", clip.UID, binding.OutputKind, revision.OutputKind)
			}
			cacheKey := procedureMaterializationCacheKey(revision, binding, output, *clip)
			if storageID, kind, ok := cachedProcedureMaterialization(appCtx.AppDB(), projectID, cacheKey); ok {
				applyProcedureArtifact(clip, binding, storageID, kind)
				changed = true
				continue
			}
			if !proceduralExecutionEnabled(appCtx) {
				return changed, errors.New("procedural clip execution is disabled for this Composer installation")
			}
			_, err = appCtx.AppDB().Exec(`INSERT INTO procedure_materializations(project_id,procedure_id,procedure_revision,cache_key,status)
				VALUES(?,?,?,?, 'running')
				ON CONFLICT(project_id,cache_key) DO UPDATE SET status='running',storage_id=0,logs='',error='',finished_at=NULL,updated_at=CURRENT_TIMESTAMP
				WHERE procedure_materializations.status != 'complete'`, projectID, revision.ProcedureID, revision.Revision, cacheKey)
			if err != nil {
				return changed, fmt.Errorf("record procedure materialization: %w", err)
			}
			runner := a.procedureRunner
			if runner == nil {
				runner = runProcedureLocal
			}
			timeline := procedureTimeline{
				JobID:            fmt.Sprintf("composition-%d:%s", compositionID, clip.UID),
				CompositionStart: clip.Start,
				SourceStart:      clip.SourceStart,
				Duration:         clipDuration(*clip),
				FPS:              output.FPS,
			}
			result, runErr := runner(ctx, appCtx.WithProject(projectID), revision, binding, output, timeline)
			if result.Cleanup != nil {
				defer result.Cleanup()
			}
			if runErr != nil {
				binding.Status, binding.Error = "failed", runErr.Error()
				_, _ = appCtx.AppDB().Exec(`UPDATE procedure_materializations SET status='failed',logs=?,error=?,runtime_job_id=?,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND cache_key=?`, result.Logs, runErr.Error(), result.RuntimeJobID, projectID, cacheKey)
				return changed, fmt.Errorf("clip %s procedure execution failed: %w", clip.UID, runErr)
			}
			format := strings.TrimPrefix(strings.ToLower(filepath.Ext(result.LocalPath)), ".")
			storageID := saveRenderOutputContext(ctx, appCtx, result.LocalPath, format, projectID, compositionID)
			if storageID <= 0 {
				err = errors.New("procedure artifact could not be saved to Storage")
				binding.Status, binding.Error = "failed", err.Error()
				_, _ = appCtx.AppDB().Exec(`UPDATE procedure_materializations SET status='failed',logs=?,error=?,runtime_job_id=?,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND cache_key=?`, result.Logs, err.Error(), result.RuntimeJobID, projectID, cacheKey)
				return changed, err
			}
			_, err = appCtx.AppDB().Exec(`UPDATE procedure_materializations SET status='complete',storage_id=?,artifact_kind=?,duration_seconds=?,runtime_job_id=?,logs=?,error='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND cache_key=?`, storageID, result.Kind, result.DurationSeconds, result.RuntimeJobID, result.Logs, projectID, cacheKey)
			if err != nil {
				return changed, err
			}
			applyProcedureArtifact(clip, binding, storageID, result.Kind)
			changed = true
		}
	}
	return changed, nil
}

func editHasProceduralAssets(edit *Edit) bool {
	if edit == nil {
		return false
	}
	for _, track := range edit.Timeline.Tracks {
		for _, clip := range track.Clips {
			if clip.Asset.Procedure != nil {
				return true
			}
		}
	}
	return false
}

func applyProcedureArtifact(clip *Clip, binding *ProceduralAsset, storageID int64, kind string) {
	binding.StorageID = storageID
	binding.Status = "ready"
	binding.Error = ""
	clip.Asset.Src = fmt.Sprintf("storage:%d", storageID)
	clip.Asset.Type = kind
}

func cachedProcedureMaterialization(db *sql.DB, projectID, cacheKey string) (int64, string, bool) {
	var storageID int64
	var kind string
	err := db.QueryRow(`SELECT storage_id,artifact_kind FROM procedure_materializations WHERE project_id=? AND cache_key=? AND status='complete' AND storage_id>0`, projectID, cacheKey).Scan(&storageID, &kind)
	return storageID, kind, err == nil && storageID > 0
}

func procedureMaterializationCacheKey(revision *ProcedureRevisionRecord, binding *ProceduralAsset, output Output, clip Clip) string {
	type inputIdentity struct {
		Name         string `json:"name"`
		Kind         string `json:"kind,omitempty"`
		Src          string `json:"src,omitempty"`
		StorageID    int64  `json:"storage_id,omitempty"`
		GenerationID int64  `json:"generation_id,omitempty"`
		CacheKey     string `json:"cache_key,omitempty"`
	}
	names := make([]string, 0, len(binding.Inputs))
	for name := range binding.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	inputs := make([]inputIdentity, 0, len(names))
	for _, name := range names {
		input := binding.Inputs[name]
		identity := inputIdentity{Name: name}
		if input != nil {
			identity.Kind, identity.Src = input.Kind, input.Src
			if input.AI != nil {
				identity.StorageID = input.AI.StorageID
				identity.GenerationID = input.AI.GenerationID
				identity.CacheKey = input.AI.CacheKey
			}
		}
		inputs = append(inputs, identity)
	}
	payload := map[string]any{
		"version": procedureJobVersion, "procedure_id": revision.ProcedureID, "revision": revision.Revision,
		"source_hash": revision.SourceHash, "runtime": revision.Runtime, "target": revision.Target,
		"output_kind": revision.OutputKind, "output_name": binding.OutputName, "parameters": binding.Parameters,
		"inputs": inputs, "cache_salt": binding.CacheKey, "duration": clipDuration(clip),
		"source_start": clip.SourceStart, "resolution": output.Resolution, "aspect": output.Aspect, "fps": output.FPS,
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func runProcedureLocal(parent context.Context, appCtx *sdk.AppCtx, revision *ProcedureRevisionRecord, binding *ProceduralAsset, output Output, timeline procedureTimeline) (procedureRunResult, error) {
	result := procedureRunResult{Kind: revision.OutputKind, RuntimeJobID: timeline.JobID}
	root, err := os.MkdirTemp("", "composer-procedure-")
	if err != nil {
		return result, fmt.Errorf("create procedure workspace: %w", err)
	}
	result.Cleanup = func() { _ = os.RemoveAll(root) }
	sourceDir := filepath.Join(root, "source")
	assetsDir := filepath.Join(root, "assets")
	outputDir := filepath.Join(root, "output")
	tmpDir := filepath.Join(root, "tmp")
	for _, dir := range []string{sourceDir, assetsDir, outputDir, tmpDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return result, err
		}
	}
	for name, content := range revision.Files {
		safe, pathErr := safeProcedurePath(name)
		if pathErr != nil {
			return result, fmt.Errorf("source path %q: %w", name, pathErr)
		}
		destination := filepath.Join(sourceDir, filepath.FromSlash(safe))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return result, err
		}
		if err := os.WriteFile(destination, []byte(content), 0o500); err != nil {
			return result, fmt.Errorf("write source %q: %w", name, err)
		}
	}
	assets := map[string]procedureJobAsset{}
	names := make([]string, 0, len(binding.Inputs))
	for name := range binding.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		input := binding.Inputs[name]
		if err := validateProcedureInputName(name); err != nil {
			return result, err
		}
		if input == nil || strings.TrimSpace(input.Src) == "" {
			return result, fmt.Errorf("procedure input %q has no materialized src", name)
		}
		resolved, err := resolveAssetLocal(appCtx, input.Src)
		if err != nil {
			return result, fmt.Errorf("resolve procedure input %q: %w", name, err)
		}
		extension := procedureInputExtension(resolved, input.Kind)
		destination := filepath.Join(assetsDir, name+extension)
		hash, size, err := copyProcedureInput(parent, resolved, destination)
		if err != nil {
			return result, fmt.Errorf("stage procedure input %q: %w", name, err)
		}
		_ = os.Chmod(destination, 0o400)
		assets[name] = procedureJobAsset{Path: destination, Kind: input.Kind, SHA256: hash, Bytes: size}
	}
	w, h := resolutionWH(output.Resolution, output.Aspect)
	job := procedureJob{
		APIVersion: procedureJobVersion, JobID: timeline.JobID, Target: revision.Target, Entrypoint: revision.Entrypoint,
		Timeline: timeline, Canvas: map[string]any{"width": w, "height": h, "pixel_aspect": 1, "color_space": "srgb"},
		Parameters: binding.Parameters, Assets: assets,
		Output: map[string]any{"directory": outputDir, "max_bytes": proceduralMaxOutputBytes(appCtx)},
		Tools:  map[string]string{"ffmpeg": ffmpegPath(), "ffprobe": ffprobePath()},
	}
	if job.Parameters == nil {
		job.Parameters = map[string]any{}
	}
	jobPath := filepath.Join(root, "job.json")
	jobJSON, _ := json.MarshalIndent(job, "", "  ")
	if err := os.WriteFile(jobPath, jobJSON, 0o400); err != nil {
		return result, err
	}
	executable := procedureExecutable(appCtx, revision.Runtime)
	if executable == "" || revision.Runtime == "web-canvas-1" {
		return result, fmt.Errorf("runtime %q is declared but local execution is not implemented", revision.Runtime)
	}
	args := []string{revision.Entrypoint}
	if revision.Runtime == "go-1.25-media" {
		args = []string{"run", revision.Entrypoint}
	}
	timeoutCtx, cancel := context.WithTimeout(parent, proceduralTimeout(appCtx))
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, executable, args...)
	cmd.Dir = sourceDir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + tmpDir, "TMPDIR=" + tmpDir,
		"COMPOSER_JOB=" + jobPath, "COMPOSER_OUTPUT=" + outputDir, "COMPOSER_TMP=" + tmpDir,
		"FFMPEG_PATH=" + ffmpegPath(), "FFPROBE_PATH=" + ffprobePath(),
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	logs := &boundedProcedureLog{limit: procedureLogLimit}
	cmd.Stdout, cmd.Stderr = logs, logs
	err = cmd.Run()
	result.Logs = logs.String()
	if timeoutCtx.Err() != nil {
		return result, fmt.Errorf("procedure stopped: %w", timeoutCtx.Err())
	}
	if err != nil {
		return result, fmt.Errorf("procedure process: %w", err)
	}
	artifact, duration, err := readProcedureResult(outputDir, binding.OutputName, revision.OutputKind, proceduralMaxOutputBytes(appCtx))
	if err != nil {
		return result, err
	}
	if err := validateProcedureArtifactMedia(timeoutCtx, artifact, revision.OutputKind); err != nil {
		return result, err
	}
	result.LocalPath, result.DurationSeconds = artifact, duration
	if result.Kind != "image" {
		probedDuration := probeProcedureDuration(parent, artifact)
		if probedDuration <= 0 {
			return result, errors.New("procedure audio/video artifact has no measurable duration")
		}
		if result.DurationSeconds > 0 {
			tolerance := math.Max(0.25, probedDuration*0.05)
			if math.Abs(result.DurationSeconds-probedDuration) > tolerance {
				return result, fmt.Errorf("procedure artifact duration %.3fs does not match declared %.3fs", probedDuration, result.DurationSeconds)
			}
		}
		result.DurationSeconds = probedDuration
	}
	return result, nil
}

func validateProcedureInputName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return errors.New("procedure input names must be 1..100 characters")
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return fmt.Errorf("procedure input name %q may contain only letters, numbers, '-' and '_'", name)
		}
	}
	return nil
}

func procedureInputExtension(source, kind string) string {
	value := source
	if parsed, err := url.Parse(source); err == nil && parsed.Path != "" {
		value = parsed.Path
	}
	extension := strings.ToLower(filepath.Ext(value))
	if len(extension) > 1 && len(extension) <= 10 {
		return extension
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image":
		return ".png"
	case "audio":
		return ".mp3"
	case "video":
		return ".mp4"
	default:
		return ".bin"
	}
}

func copyProcedureInput(ctx context.Context, source, destination string) (string, int64, error) {
	var reader io.ReadCloser
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return "", 0, err
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", 0, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return "", 0, fmt.Errorf("download returned %s", response.Status)
		}
		reader = response.Body
	} else {
		file, err := os.Open(source)
		if err != nil {
			return "", 0, err
		}
		reader = file
	}
	defer reader.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(reader, procedureInputLimit+1))
	closeErr := out.Close()
	if copyErr != nil {
		return "", written, copyErr
	}
	if closeErr != nil {
		return "", written, closeErr
	}
	if written > procedureInputLimit {
		return "", written, fmt.Errorf("input exceeds %d bytes", procedureInputLimit)
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func readProcedureResult(outputDir, outputName, expectedKind string, maxBytes int64) (string, float64, error) {
	manifestPath := filepath.Join(outputDir, "result.json")
	file, err := os.Open(manifestPath)
	if err != nil {
		return "", 0, errors.New("procedure did not write output/result.json")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return "", 0, errors.New("procedure result.json is unreadable or exceeds 1 MiB")
	}
	var manifest procedureResultManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", 0, fmt.Errorf("procedure result.json: %w", err)
	}
	if manifest.APIVersion != "" && manifest.APIVersion != procedureJobVersion {
		return "", 0, fmt.Errorf("unsupported procedure result api_version %q", manifest.APIVersion)
	}
	var chosen *struct {
		Name     string  `json:"name"`
		Kind     string  `json:"kind"`
		Path     string  `json:"path"`
		MimeType string  `json:"mime_type,omitempty"`
		Duration float64 `json:"duration,omitempty"`
	}
	for i := range manifest.Artifacts {
		artifact := &manifest.Artifacts[i]
		if outputName != "" && artifact.Name == outputName {
			chosen = artifact
			break
		}
		if outputName == "" && artifact.Kind == expectedKind && (chosen == nil || artifact.Name == "main") {
			chosen = artifact
		}
	}
	if chosen == nil {
		return "", 0, fmt.Errorf("procedure result has no %q artifact matching output_name %q", expectedKind, outputName)
	}
	if chosen.Kind != expectedKind {
		return "", 0, fmt.Errorf("procedure artifact kind %q does not match expected %q", chosen.Kind, expectedKind)
	}
	safe, err := safeProcedurePath(chosen.Path)
	if err != nil {
		return "", 0, fmt.Errorf("procedure artifact path: %w", err)
	}
	artifactPath := filepath.Join(outputDir, filepath.FromSlash(safe))
	info, err := os.Lstat(artifactPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, errors.New("procedure artifact is missing or is not a regular file")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.New("procedure artifact must not be a symlink")
	}
	if info.Size() <= 0 || info.Size() > maxBytes {
		return "", 0, fmt.Errorf("procedure artifact size must be between 1 and %d bytes", maxBytes)
	}
	realRoot, _ := filepath.EvalSymlinks(outputDir)
	realArtifact, err := filepath.EvalSymlinks(artifactPath)
	if err != nil || (realArtifact != realRoot && !strings.HasPrefix(realArtifact, realRoot+string(filepath.Separator))) {
		return "", 0, errors.New("procedure artifact escapes the output directory")
	}
	return artifactPath, chosen.Duration, nil
}

func probeProcedureDuration(parent context.Context, path string) float64 {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, ffprobePath(), "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := command.Output()
	if err != nil {
		return 0
	}
	var duration float64
	_, _ = fmt.Sscan(strings.TrimSpace(string(out)), &duration)
	return duration
}

func validateProcedureArtifactMedia(parent context.Context, path, expectedKind string) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, ffprobePath(), "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", path)
	out, err := command.Output()
	if err != nil {
		return fmt.Errorf("procedure artifact is not probeable media: %w", err)
	}
	streams := strings.Fields(strings.ToLower(string(out)))
	has := func(kind string) bool {
		for _, stream := range streams {
			if strings.TrimSpace(strings.Trim(stream, ",")) == kind {
				return true
			}
		}
		return false
	}
	extension := strings.ToLower(filepath.Ext(path))
	imageExtension := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".bmp": true, ".tif": true, ".tiff": true, ".ppm": true}
	switch expectedKind {
	case "audio":
		if !has("audio") {
			return errors.New("procedure artifact does not contain an audio stream")
		}
	case "image":
		if !has("video") || !imageExtension[extension] {
			return errors.New("procedure image artifact must be a recognized still-image file")
		}
	case "video":
		if !has("video") || imageExtension[extension] {
			return errors.New("procedure video artifact does not contain a video stream")
		}
	default:
		return fmt.Errorf("unsupported procedure artifact kind %q", expectedKind)
	}
	return nil
}
