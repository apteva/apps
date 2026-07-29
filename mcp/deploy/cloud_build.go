package main

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	cloudBuildProtocolVersion = "apteva.build/v1"
	buildBackendLocal         = "local"
	buildBackendRunner        = "runner"
	buildBackendCodemagic     = "codemagic"
	buildBackendGitHubActions = "github_actions"
	defaultCloudArtifactName  = "apteva-build"
	maxCloudArtifactBytes     = int64(2 << 30)
)

type cloudBuildConfig struct {
	Owner               string            `json:"owner,omitempty"`
	Repo                string            `json:"repo,omitempty"`
	WorkflowID          string            `json:"workflow_id"`
	Ref                 string            `json:"ref,omitempty"`
	AppID               string            `json:"app_id,omitempty"`
	Branch              string            `json:"branch,omitempty"`
	Tag                 string            `json:"tag,omitempty"`
	InstanceType        string            `json:"instance_type,omitempty"`
	ArtifactName        string            `json:"artifact_name,omitempty"`
	ArtifactMode        string            `json:"artifact_mode,omitempty"`
	ArtifactFile        string            `json:"artifact_file,omitempty"`
	StoreChannel        string            `json:"store_channel,omitempty"`
	Preflight           string            `json:"preflight,omitempty"`
	ContractInputs      bool              `json:"contract_inputs,omitempty"`
	Inputs              map[string]any    `json:"inputs,omitempty"`
	Variables           map[string]string `json:"variables,omitempty"`
	Groups              []string          `json:"groups,omitempty"`
	SourceMode          string            `json:"source_mode,omitempty"`
	SourceBaseURL       string            `json:"source_base_url,omitempty"`
	SourceURLTTLSeconds int               `json:"source_url_ttl_seconds,omitempty"`
	AdapterRepository   string            `json:"adapter_repository,omitempty"`
	RunnerURL           string            `json:"runner_url,omitempty"`
	RunnerTokenEnv      string            `json:"runner_token_env,omitempty"`
}

type externalBuildJob struct {
	ID        string
	Status    string
	MetaJSON  string
	SourceSHA string
}

type externalBuildStatus struct {
	Status      string
	ProviderRaw json.RawMessage
	SourceSHA   string
	Error       string
}

type cloudArtifact struct {
	Name            string
	URL             string
	NeedsGitHubAuth bool
	Headers         map[string]string
	Archive         bool
	FileName        string
}

type cloudBuildBackend interface {
	Name() string
	Submit(context.Context, *sdk.BoundIntegration, cloudBuildConfig, *Deployment, *Build, *sourceCapsule) (*externalBuildJob, error)
	Inspect(context.Context, *sdk.BoundIntegration, cloudBuildConfig, *Build) (*externalBuildStatus, error)
	Cancel(context.Context, *sdk.BoundIntegration, cloudBuildConfig, *Build) error
	Artifact(context.Context, *sdk.BoundIntegration, cloudBuildConfig, *Build, *externalBuildStatus) (*cloudArtifact, error)
}

func normalizeBuildBackend(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", buildBackendLocal:
		return buildBackendLocal
	case "http_runner", "capsule_runner", buildBackendRunner:
		return buildBackendRunner
	case "github", "github-actions", buildBackendGitHubActions:
		return buildBackendGitHubActions
	case buildBackendCodemagic:
		return buildBackendCodemagic
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func parseCloudBuildConfig(backend, raw string) (cloudBuildConfig, error) {
	var cfg cloudBuildConfig
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("build_backend_config_json: %w", err)
	}
	cfg.WorkflowID = strings.TrimSpace(cfg.WorkflowID)
	cfg.ArtifactName = strings.TrimSpace(cfg.ArtifactName)
	cfg.ArtifactMode = strings.ToLower(strings.TrimSpace(cfg.ArtifactMode))
	cfg.SourceMode = strings.ToLower(strings.TrimSpace(cfg.SourceMode))
	cfg.StoreChannel = strings.ToLower(strings.TrimSpace(cfg.StoreChannel))
	cfg.Preflight = strings.ToLower(strings.TrimSpace(cfg.Preflight))
	if cfg.SourceMode == "" && normalizeBuildBackend(backend) == buildBackendRunner {
		cfg.SourceMode = "bundle"
	} else if cfg.SourceMode == "" {
		cfg.SourceMode = "repository"
	}
	if cfg.ArtifactName == "" {
		cfg.ArtifactName = defaultCloudArtifactName
	}
	if cfg.SourceMode != "repository" && cfg.SourceMode != "bundle" {
		return cfg, errors.New("source_mode must be repository or bundle")
	}
	if cfg.Preflight != "" && cfg.Preflight != "strict" && cfg.Preflight != "off" {
		return cfg, errors.New("preflight must be strict or off")
	}
	switch cfg.ArtifactMode {
	case "", "bundle", "file", "store_upload", "none":
	default:
		return cfg, errors.New("artifact_mode must be bundle, file, store_upload, or none")
	}
	maxTTLSeconds := int(maxSourceCapsuleTTL / time.Second)
	if cfg.SourceURLTTLSeconds < 0 || cfg.SourceURLTTLSeconds > maxTTLSeconds {
		return cfg, fmt.Errorf("source_url_ttl_seconds must be 0 (default) or between 1 and %d", maxTTLSeconds)
	}
	switch normalizeBuildBackend(backend) {
	case buildBackendRunner:
		if cfg.SourceMode != "bundle" {
			return cfg, errors.New("runner backend requires source_mode=bundle")
		}
		if err := validateRunnerBaseURL(cfg.RunnerURL); err != nil {
			return cfg, err
		}
		cfg.RunnerURL = strings.TrimRight(strings.TrimSpace(cfg.RunnerURL), "/")
		cfg.RunnerTokenEnv = strings.TrimSpace(cfg.RunnerTokenEnv)
		if cfg.RunnerTokenEnv == "" {
			cfg.RunnerTokenEnv = defaultRunnerTokenEnv
		}
	case buildBackendCodemagic:
		if strings.TrimSpace(cfg.AppID) == "" || cfg.WorkflowID == "" {
			return cfg, errors.New("codemagic backend requires app_id and workflow_id")
		}
		if strings.TrimSpace(cfg.Branch) == "" && strings.TrimSpace(cfg.Tag) == "" {
			return cfg, errors.New("codemagic backend requires branch or tag")
		}
		if cfg.Branch != "" && cfg.Tag != "" {
			return cfg, errors.New("codemagic backend accepts branch or tag, not both")
		}
	case buildBackendGitHubActions:
		if cfg.SourceMode == "bundle" && !cfg.ContractInputs {
			return cfg, errors.New("github_actions source_mode=bundle requires contract_inputs=true and matching workflow_dispatch inputs")
		}
		if strings.TrimSpace(cfg.Owner) == "" || strings.TrimSpace(cfg.Repo) == "" || cfg.WorkflowID == "" || strings.TrimSpace(cfg.Ref) == "" {
			return cfg, errors.New("github_actions backend requires owner, repo, workflow_id, and ref")
		}
	default:
		return cfg, fmt.Errorf("unsupported build_backend %q", backend)
	}
	return cfg, nil
}

func validateBuildBackendSelection(backend, raw string) error {
	backend = normalizeBuildBackend(backend)
	if backend == buildBackendLocal {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return fmt.Errorf("build_backend_config_json: %w", err)
		}
		return nil
	}
	_, err := parseCloudBuildConfig(backend, raw)
	return err
}

func cloudBackendFor(name string) (cloudBuildBackend, error) {
	switch normalizeBuildBackend(name) {
	case buildBackendRunner:
		return runnerBuildBackend{}, nil
	case buildBackendCodemagic:
		return codemagicBuildBackend{}, nil
	case buildBackendGitHubActions:
		return githubActionsBuildBackend{}, nil
	default:
		return nil, fmt.Errorf("unsupported build_backend %q", name)
	}
}

func cloudIntegrationFor(backend string) (*sdk.BoundIntegration, error) {
	if normalizeBuildBackend(backend) == buildBackendRunner {
		return nil, nil
	}
	if globalCtx == nil {
		return nil, errors.New("platform unavailable")
	}
	wantSlug := map[string]string{
		buildBackendCodemagic:     "codemagic",
		buildBackendGitHubActions: "github",
	}[normalizeBuildBackend(backend)]
	for _, bound := range globalCtx.IntegrationsFor("cloud_build") {
		if bound != nil && bound.Kind == "integration" && bound.ConnectionID > 0 && bound.AppSlug == wantSlug {
			return bound, nil
		}
	}
	return nil, fmt.Errorf("no %s connection is bound to Deploy's cloud_build role", wantSlug)
}

func (a *App) submitCloudBuild(ctx context.Context, d *Deployment) (*Build, error) {
	return a.submitCloudBuildWithOptions(ctx, d, nil)
}

func (a *App) submitCloudBuildWithOptions(ctx context.Context, d *Deployment, releaseOpts *releaseOptions) (*Build, error) {
	backendName := normalizeBuildBackend(d.BuildBackend)
	cfg, err := parseCloudBuildConfig(backendName, d.BuildBackendJSON)
	if err != nil {
		return nil, err
	}
	buildBackendJSON := d.BuildBackendJSON
	if releaseOpts != nil && (d.TargetKind == "ios" || d.TargetKind == "android") {
		channel, channelErr := normalizeMobileChannel(d.TargetKind, releaseOpts.Channel)
		if channelErr != nil {
			return nil, channelErr
		}
		cfg.StoreChannel = channel
		body, marshalErr := json.Marshal(cfg)
		if marshalErr != nil {
			return nil, marshalErr
		}
		buildBackendJSON = string(body)
	}
	if err := validateMobileCloudContract(d, cfg); err != nil {
		return nil, err
	}
	backend, err := cloudBackendFor(backendName)
	if err != nil {
		return nil, err
	}
	bound, err := cloudIntegrationFor(backendName)
	if err != nil {
		return nil, err
	}
	build, err := dbCreateBuildForEnvBackend(
		globalCtx.AppDB(), d.ID, d.EnvironmentID, d.Framework, d.BuildCmd,
		backendName, buildBackendJSON,
	)
	if err != nil {
		return nil, err
	}
	buildDir := filepath.Join(a.dataDir, "builds", strconv.FormatInt(build.ID, 10))
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return a.failBuild(build, "mkdir: "+err.Error()), nil
	}
	logPath := filepath.Join(buildDir, "build.log")
	if err := appendCloudBuildLog(logPath, "preparing "+backendName+" build"); err != nil {
		return a.failBuild(build, "open log: "+err.Error()), nil
	}
	startedAt := nowUTC()
	_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{
		"status": "running", "started_at": startedAt, "log_path": logPath,
		"external_status": "not_submitted",
	})
	emit("deploy.build.started", map[string]any{
		"deployment_id": d.ID, "environment": d.EnvironmentName,
		"build_id": build.ID, "backend": backendName,
	})
	var capsule *sourceCapsule
	if cfg.SourceMode == "bundle" {
		_ = appendCloudBuildLog(logPath, "preparing signed source capsule")
		capsule, err = a.prepareSourceCapsule(ctx, d, build, cfg)
		if err != nil {
			return a.failBuild(build, "prepare source capsule: "+err.Error()), nil
		}
		_ = appendCloudBuildLog(logPath, fmt.Sprintf(
			"source capsule ready sha256=%s size=%d expires=%d",
			capsule.SHA256, capsule.Size, capsule.Expires,
		))
	}
	_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"external_status": "submitting"})
	_ = appendCloudBuildLog(logPath, "submitting "+backendName+" build")
	job, err := backend.Submit(ctx, bound, cfg, d, build, capsule)
	if err != nil {
		_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"external_status": "submission_failed"})
		return a.failBuild(build, "submit "+backendName+": "+err.Error()), nil
	}
	fields := map[string]any{
		"external_job_id":    job.ID,
		"external_status":    defaultStr(job.Status, "queued"),
		"external_meta_json": defaultStr(job.MetaJSON, "{}"),
	}
	if job.SourceSHA != "" && cfg.SourceMode != "bundle" {
		fields["source_sha"] = job.SourceSHA
	}
	_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, fields)
	_ = appendCloudBuildLog(logPath, fmt.Sprintf("submitted job %s status=%s", job.ID, defaultStr(job.Status, "queued")))
	return dbGetBuild(globalCtx.AppDB(), build.ID)
}

func (a *App) syncPendingCloudBuilds(ctx context.Context) error {
	a.cloudBuildMu.Lock()
	defer a.cloudBuildMu.Unlock()
	defer func() {
		if builds, err := dbListAllBuilds(globalCtx.AppDB()); err == nil {
			a.cleanupSourceCapsules(builds, time.Now())
		}
	}()
	builds, err := dbListPendingCloudBuilds(globalCtx.AppDB(), 100)
	if err != nil {
		return err
	}
	var firstErr error
	for i := range builds {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := a.syncCloudBuild(ctx, &builds[i]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *App) syncCloudBuild(ctx context.Context, build *Build) error {
	cfg, err := parseCloudBuildConfig(build.BuildBackend, build.BuildBackendJSON)
	if err != nil {
		a.failBuild(build, err.Error())
		return nil
	}
	backend, err := cloudBackendFor(build.BuildBackend)
	if err != nil {
		a.failBuild(build, err.Error())
		return nil
	}
	bound, err := cloudIntegrationFor(build.BuildBackend)
	if err != nil {
		return err
	}
	status, err := backend.Inspect(ctx, bound, cfg, build)
	if err != nil {
		_ = appendCloudBuildLog(build.LogPath, "status check failed: "+err.Error())
		return err
	}
	if status == nil {
		return errors.New("cloud backend returned no status")
	}
	fields := map[string]any{"external_status": status.Status}
	if len(status.ProviderRaw) > 0 {
		fields["external_meta_json"] = string(status.ProviderRaw)
	}
	if status.SourceSHA != "" && cfg.SourceMode != "bundle" {
		fields["source_sha"] = status.SourceSHA
	}
	if status.Status != build.ExternalStatus {
		_ = appendCloudBuildLog(build.LogPath, "status="+status.Status)
	}
	switch status.Status {
	case "queued", "pending", "requested", "waiting", "in_progress", "building", "running":
		_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, fields)
		return nil
	case "cancelled", "canceled":
		fields["status"] = "cancelled"
		fields["finished_at"] = nowUTC()
		fields["error"] = defaultStr(status.Error, "cloud build cancelled")
		_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, fields)
		a.removeSourceCapsule(build.ID)
		emit("deploy.build.cancelled", map[string]any{
			"deployment_id": build.DeploymentID, "environment_id": build.EnvironmentID,
			"build_id": build.ID, "backend": build.BuildBackend,
		})
		return nil
	case "failed", "failure", "timed_out", "timeout", "action_required", "stale", "skipped", "neutral":
		fields["status"] = "failed"
		fields["finished_at"] = nowUTC()
		fields["error"] = defaultStr(status.Error, "cloud build "+status.Status)
		_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, fields)
		a.removeSourceCapsule(build.ID)
		emit("deploy.build.failed", map[string]any{
			"deployment_id": build.DeploymentID, "environment_id": build.EnvironmentID,
			"build_id": build.ID, "backend": build.BuildBackend, "error": fields["error"],
		})
		return nil
	case "succeeded", "success", "finished", "completed":
		return a.finalizeCloudBuild(ctx, backend, bound, cfg, build, status)
	default:
		_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, fields)
		return nil
	}
}

func (a *App) finalizeCloudBuild(ctx context.Context, backend cloudBuildBackend, bound *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build, status *externalBuildStatus) error {
	d, err := a.deploymentForBuild(build)
	if err != nil {
		return err
	}
	distDir := filepath.Join(a.dataDir, "builds", strconv.FormatInt(build.ID, 10), "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		a.failBuild(build, "create artifact directory: "+err.Error())
		return nil
	}
	mode := resolvedCloudArtifactMode(cfg, d)
	var manifestJSON string
	switch mode {
	case "store_upload":
		manifest, err := externalStoreArtifactManifest(d, build)
		if err != nil {
			a.failBuild(build, err.Error())
			return nil
		}
		if err := writeArtifactManifest(distDir, manifest); err != nil {
			a.failBuild(build, err.Error())
			return nil
		}
		body, _ := json.Marshal(manifest)
		manifestJSON = string(body)
	case "bundle", "file":
		artifact, err := backend.Artifact(ctx, bound, cfg, build, status)
		if err != nil {
			a.failBuild(build, "collect cloud artifact: "+err.Error())
			return nil
		}
		if err := a.downloadAndStageCloudArtifact(bound, d, build, artifact, mode, distDir); err != nil {
			a.failBuild(build, "stage cloud artifact: "+err.Error())
			return nil
		}
		if body, err := os.ReadFile(filepath.Join(distDir, artifactManifestFilename)); err == nil && json.Valid(body) {
			manifestJSON = string(body)
		}
	case "none":
		// A successful workflow may intentionally have no deployable output.
	default:
		a.failBuild(build, "unsupported cloud artifact_mode "+mode)
		return nil
	}
	size, _ := dirSize(distDir)
	finished := nowUTC()
	duration := elapsedBuildMs(build.StartedAt, finished)
	fields := map[string]any{
		"status": "succeeded", "finished_at": finished, "duration_ms": duration,
		"artifact_path": distDir, "artifact_size": size,
		"external_status": "succeeded",
	}
	if manifestJSON != "" {
		fields["artifact_manifest_json"] = manifestJSON
	}
	if status.SourceSHA != "" && cfg.SourceMode != "bundle" {
		fields["source_sha"] = status.SourceSHA
	}
	_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, fields)
	a.removeSourceCapsule(build.ID)
	_ = appendCloudBuildLog(build.LogPath, fmt.Sprintf("build succeeded artifact=%s size=%d", distDir, size))
	fresh, _ := dbGetBuild(globalCtx.AppDB(), build.ID)
	emit("deploy.build.succeeded", map[string]any{
		"deployment_id": build.DeploymentID, "environment_id": build.EnvironmentID,
		"build_id": build.ID, "backend": build.BuildBackend, "duration_ms": duration, "size": size,
	})
	a.pruneBuildArtifactsAsync("cloud_build_succeeded")
	if fresh != nil && fresh.ReleaseRequested {
		return a.runRequestedCloudRelease(d, fresh)
	}
	return nil
}

func (a *App) runRequestedCloudRelease(d *Deployment, build *Build) error {
	var opts releaseOptions
	if raw := strings.TrimSpace(build.ReleaseOptionsJSON); raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &opts); err != nil {
			return err
		}
	}
	_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"release_requested": false})
	rel, err := a.runReleaseWithOptions(d, build, opts)
	if err != nil {
		_ = appendCloudBuildLog(build.LogPath, "automatic release failed: "+err.Error())
		emit("deploy.release.failed", map[string]any{
			"deployment_id": build.DeploymentID, "build_id": build.ID, "error": err.Error(),
		})
		return err
	}
	_ = appendCloudBuildLog(build.LogPath, fmt.Sprintf("automatic release created release=%d", rel.ID))
	return nil
}

func (a *App) cancelCloudBuild(ctx context.Context, build *Build) (*Build, error) {
	if build == nil {
		return nil, errors.New("build required")
	}
	if normalizeBuildBackend(build.BuildBackend) == buildBackendLocal {
		return nil, errors.New("local build cancellation is not supported")
	}
	if build.Status != "pending" && build.Status != "running" {
		return build, nil
	}
	cfg, err := parseCloudBuildConfig(build.BuildBackend, build.BuildBackendJSON)
	if err != nil {
		return nil, err
	}
	backend, err := cloudBackendFor(build.BuildBackend)
	if err != nil {
		return nil, err
	}
	bound, err := cloudIntegrationFor(build.BuildBackend)
	if err != nil {
		return nil, err
	}
	if err := backend.Cancel(ctx, bound, cfg, build); err != nil {
		return nil, err
	}
	_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{
		"status": "cancelled", "external_status": "cancelled",
		"finished_at": nowUTC(), "error": "cancelled by operator",
	})
	a.removeSourceCapsule(build.ID)
	_ = appendCloudBuildLog(build.LogPath, "cancelled by operator")
	emit("deploy.build.cancelled", map[string]any{
		"deployment_id": build.DeploymentID, "environment_id": build.EnvironmentID,
		"build_id": build.ID, "backend": build.BuildBackend,
	})
	return dbGetBuild(globalCtx.AppDB(), build.ID)
}

func (a *App) deploymentForBuild(build *Build) (*Deployment, error) {
	d, err := dbGetDeploymentByID(globalCtx.AppDB(), build.DeploymentID)
	if err != nil || d == nil {
		return nil, errors.New("deployment not found for cloud build")
	}
	if build.EnvironmentID > 0 {
		env, err := dbGetEnvironment(globalCtx.AppDB(), build.EnvironmentID)
		if err != nil || env == nil {
			return nil, errors.New("environment not found for cloud build")
		}
		d = effectiveDeploymentForEnvironment(d, env)
	}
	return d, nil
}

type codemagicBuildBackend struct{}

func (codemagicBuildBackend) Name() string { return buildBackendCodemagic }

func (codemagicBuildBackend) Submit(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, d *Deployment, build *Build, capsule *sourceCapsule) (*externalBuildJob, error) {
	input := map[string]any{"appId": cfg.AppID, "workflowId": cfg.WorkflowID}
	if cfg.Branch != "" {
		input["branch"] = cfg.Branch
	} else {
		input["tag"] = cfg.Tag
	}
	if cfg.InstanceType != "" {
		input["instanceType"] = cfg.InstanceType
	}
	variables := cloneStringMap(cfg.Variables)
	contract, err := cloudBuildContractVariables(cfg, d, build, capsule)
	if err != nil {
		return nil, err
	}
	for key, value := range contract {
		variables[key] = value
	}
	input["environment"] = map[string]any{"variables": variables, "groups": cfg.Groups}
	data, err := executeIntegration(bound, "start_build", input)
	if err != nil {
		return nil, err
	}
	id := firstRecursiveString(data, "buildId", "build_id", "_id", "id")
	if id == "" {
		return nil, errors.New("Codemagic start_build returned no buildId")
	}
	return &externalBuildJob{ID: id, Status: "queued", MetaJSON: rawOrEmpty(data)}, nil
}

func (codemagicBuildBackend) Inspect(_ context.Context, bound *sdk.BoundIntegration, _ cloudBuildConfig, build *Build) (*externalBuildStatus, error) {
	data, err := executeIntegration(bound, "get_build", map[string]any{"build_id": build.ExternalJobID})
	if err != nil {
		return nil, err
	}
	status := strings.ToLower(firstRecursiveString(data, "status"))
	if status == "" {
		return nil, errors.New("Codemagic get_build returned no status")
	}
	if status == "finished" {
		status = "succeeded"
	}
	errorText := topLevelProviderError(data)
	if isFailedCloudStatus(status) {
		if actions, actionErr := executeIntegration(bound, "list_build_actions", map[string]any{
			"build_id": build.ExternalJobID, "page_size": 100,
		}); actionErr == nil {
			errorText = defaultStr(codemagicFailedActionSummary(actions), errorText)
		}
		errorText = defaultStr(errorText, "Codemagic build failed")
	}
	return &externalBuildStatus{
		Status: status, ProviderRaw: append(json.RawMessage(nil), data...),
		SourceSHA: firstRecursiveString(data, "commitHash", "commit_hash", "head_sha"),
		Error:     errorText,
	}, nil
}

func topLevelProviderError(raw json.RawMessage) string {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range []string{"error", "error_message", "failure_reason"} {
		switch item := value[key].(type) {
		case string:
			if strings.TrimSpace(item) != "" {
				return strings.TrimSpace(item)
			}
		case map[string]any:
			for _, nested := range []string{"message", "detail", "reason"} {
				if text, ok := item[nested].(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func isFailedCloudStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "failure", "timed_out", "timeout", "action_required":
		return true
	default:
		return false
	}
}

func codemagicFailedActionSummary(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	var names []string
	var visit func(any)
	visit = func(current any) {
		switch item := current.(type) {
		case []any:
			for _, child := range item {
				visit(child)
			}
		case map[string]any:
			status := ""
			for _, key := range []string{"status", "conclusion"} {
				if text, ok := item[key].(string); ok {
					status = strings.ToLower(strings.TrimSpace(text))
					if status != "" {
						break
					}
				}
			}
			if isFailedCloudStatus(status) {
				name := ""
				for _, key := range []string{"name", "actionName", "displayName", "title"} {
					if text, ok := item[key].(string); ok && strings.TrimSpace(text) != "" {
						name = strings.TrimSpace(text)
						break
					}
				}
				if name != "" {
					names = append(names, name)
				}
			}
			for _, child := range item {
				visit(child)
			}
		}
	}
	visit(value)
	if len(names) == 0 {
		return ""
	}
	return "Codemagic failed action: " + strings.Join(uniqueCloudStrings(names), ", ")
}

func uniqueCloudStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func (codemagicBuildBackend) Cancel(_ context.Context, bound *sdk.BoundIntegration, _ cloudBuildConfig, build *Build) error {
	_, err := executeIntegration(bound, "cancel_build", map[string]any{"build_id": build.ExternalJobID})
	return err
}

func (codemagicBuildBackend) Artifact(_ context.Context, _ *sdk.BoundIntegration, cfg cloudBuildConfig, _ *Build, status *externalBuildStatus) (*cloudArtifact, error) {
	candidates := recursiveArtifactURLs(status.ProviderRaw)
	for _, candidate := range candidates {
		if cfg.ArtifactName == "" || strings.Contains(strings.ToLower(candidate.Name), strings.ToLower(cfg.ArtifactName)) {
			return &candidate, nil
		}
	}
	return nil, fmt.Errorf("Codemagic artifact %q not found", cfg.ArtifactName)
}

type githubActionsBuildBackend struct{}

func (githubActionsBuildBackend) Name() string { return buildBackendGitHubActions }

func (githubActionsBuildBackend) Submit(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, d *Deployment, build *Build, capsule *sourceCapsule) (*externalBuildJob, error) {
	inputs := cloneAnyMap(cfg.Inputs)
	if cfg.ContractInputs {
		contract, err := cloudBuildContractVariables(cfg, d, build, capsule)
		if err != nil {
			return nil, err
		}
		for key, value := range contract {
			inputs[strings.ToLower(key)] = value
		}
	}
	submittedAt := time.Now().UTC()
	data, err := executeIntegration(bound, "trigger_workflow", map[string]any{
		"owner": cfg.Owner, "repo": cfg.Repo, "workflow_id": cfg.WorkflowID,
		"ref": cfg.Ref, "inputs": inputs,
	})
	if err != nil {
		return nil, err
	}
	id := firstRecursiveString(data, "workflow_run_id", "run_id", "id")
	if id == "" {
		id = "discover:" + submittedAt.Format(time.RFC3339Nano)
	}
	return &externalBuildJob{ID: id, Status: "queued", MetaJSON: rawOrEmpty(data)}, nil
}

func (githubActionsBuildBackend) Inspect(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build) (*externalBuildStatus, error) {
	jobID := build.ExternalJobID
	if strings.HasPrefix(jobID, "discover:") {
		discovered, data, err := discoverGitHubRun(bound, cfg, strings.TrimPrefix(jobID, "discover:"))
		if err != nil {
			return nil, err
		}
		if discovered == "" {
			return &externalBuildStatus{Status: "queued", ProviderRaw: data}, nil
		}
		jobID = discovered
		_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"external_job_id": jobID})
	}
	runID, err := strconv.ParseInt(jobID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub Actions run id %q", jobID)
	}
	data, err := executeIntegration(bound, "get_workflow_run", map[string]any{
		"owner": cfg.Owner, "repo": cfg.Repo, "run_id": runID,
	})
	if err != nil {
		return nil, err
	}
	status := strings.ToLower(firstRecursiveString(data, "status"))
	conclusion := strings.ToLower(firstRecursiveString(data, "conclusion"))
	if status == "completed" {
		if conclusion == "success" {
			status = "succeeded"
		} else if conclusion != "" {
			status = conclusion
		}
	}
	return &externalBuildStatus{
		Status: status, ProviderRaw: append(json.RawMessage(nil), data...),
		SourceSHA: firstRecursiveString(data, "head_sha", "headSha"),
		Error:     defaultStr(firstRecursiveString(data, "error", "message"), conclusion),
	}, nil
}

func (githubActionsBuildBackend) Cancel(_ context.Context, bound *sdk.BoundIntegration, _ cloudBuildConfig, build *Build) error {
	if strings.HasPrefix(build.ExternalJobID, "discover:") {
		return errors.New("GitHub Actions run has not been discovered yet")
	}
	runID, err := strconv.ParseInt(build.ExternalJobID, 10, 64)
	if err != nil {
		return err
	}
	var cfg cloudBuildConfig
	if err := json.Unmarshal([]byte(defaultStr(build.BuildBackendJSON, "{}")), &cfg); err != nil {
		return err
	}
	_, err = executeIntegration(bound, "cancel_workflow_run", map[string]any{
		"owner": cfg.Owner, "repo": cfg.Repo, "run_id": runID,
	})
	return err
}

func (githubActionsBuildBackend) Artifact(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build, _ *externalBuildStatus) (*cloudArtifact, error) {
	runID, err := strconv.ParseInt(build.ExternalJobID, 10, 64)
	if err != nil {
		return nil, err
	}
	data, err := executeIntegration(bound, "list_workflow_run_artifacts", map[string]any{
		"owner": cfg.Owner, "repo": cfg.Repo, "run_id": runID,
		"name": cfg.ArtifactName, "per_page": 100,
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Artifacts []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Expired bool   `json:"expired"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	for _, item := range payload.Artifacts {
		if item.Expired || (cfg.ArtifactName != "" && item.Name != cfg.ArtifactName) {
			continue
		}
		return &cloudArtifact{
			Name: item.Name + ".zip",
			URL: fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/artifacts/%d/zip",
				url.PathEscape(cfg.Owner), url.PathEscape(cfg.Repo), item.ID),
			NeedsGitHubAuth: true,
			Archive:         true,
			FileName:        cfg.ArtifactFile,
		}, nil
	}
	return nil, fmt.Errorf("GitHub Actions artifact %q not found", cfg.ArtifactName)
}

func discoverGitHubRun(bound *sdk.BoundIntegration, cfg cloudBuildConfig, submitted string) (string, json.RawMessage, error) {
	input := map[string]any{
		"owner": cfg.Owner, "repo": cfg.Repo, "workflow_id": cfg.WorkflowID,
		"event": "workflow_dispatch", "per_page": 20,
	}
	if cfg.Ref != "" {
		input["branch"] = cfg.Ref
	}
	data, err := executeIntegration(bound, "list_workflow_runs", input)
	if err != nil {
		return "", nil, err
	}
	submittedAt, _ := time.Parse(time.RFC3339Nano, submitted)
	var payload struct {
		Runs []struct {
			ID         int64  `json:"id"`
			CreatedAt  string `json:"created_at"`
			HeadBranch string `json:"head_branch"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", data, err
	}
	for _, run := range payload.Runs {
		created, _ := time.Parse(time.RFC3339, run.CreatedAt)
		if run.ID > 0 && (submittedAt.IsZero() || !created.Before(submittedAt.Add(-5*time.Second))) {
			return strconv.FormatInt(run.ID, 10), data, nil
		}
	}
	return "", data, nil
}

func externalStoreArtifactManifest(d *Deployment, build *Build) (artifactManifest, error) {
	cfg, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return artifactManifest{}, err
	}
	platform := d.TargetKind
	if platform == "" || platform == "service" {
		platform = build.Framework
	}
	provider := map[string]string{"ios": "app_store_connect", "android": "google_play"}[platform]
	if provider == "" {
		return artifactManifest{}, errors.New("store_upload artifact mode requires an ios or android target")
	}
	if platform == "android" && strings.TrimSpace(cfg.VersionCode) == "" {
		return artifactManifest{}, errors.New("Android store_upload requires target_config_json.version_code")
	}
	cloudCfg, err := parseCloudBuildConfig(build.BuildBackend, build.BuildBackendJSON)
	if err != nil {
		return artifactManifest{}, err
	}
	return artifactManifest{
		Platform: platform, BundleID: cfg.BundleID, PackageName: cfg.PackageName,
		VersionName: cfg.VersionName, BuildNumber: cfg.BuildNumber, VersionCode: cfg.VersionCode,
		Channel:          cloudCfg.StoreChannel,
		ExternalProvider: provider, ExternalID: build.ExternalJobID,
		ExternalStatus: map[string]string{"ios": "uploaded_processing", "android": "completed"}[platform],
	}, nil
}

func cloudBuildContractVariables(cfg cloudBuildConfig, d *Deployment, build *Build, capsule *sourceCapsule) (map[string]string, error) {
	if d == nil || build == nil {
		return nil, errors.New("deployment and build are required")
	}
	mode := resolvedCloudArtifactMode(cfg, d)
	spec := runnerBuildSpec{
		BuildID: build.ID, ProjectID: d.ProjectID, Deployment: d.Name,
		Environment: d.EnvironmentName, TargetKind: d.TargetKind,
		Framework: d.Framework, BuildCmd: d.BuildCmd,
		Env: parseEnvJSON(d.EnvJSON), TargetConfigJSON: defaultStr(d.TargetConfigJSON, "{}"),
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"APTEVA_PROTOCOL":          cloudBuildProtocolVersion,
		"APTEVA_BUILD_ID":          strconv.FormatInt(build.ID, 10),
		"APTEVA_DEPLOYMENT":        d.Name,
		"APTEVA_ENVIRONMENT":       d.EnvironmentName,
		"APTEVA_TARGET_KIND":       d.TargetKind,
		"APTEVA_FRAMEWORK":         d.Framework,
		"APTEVA_BUILD_CMD":         d.BuildCmd,
		"APTEVA_SOURCE_MODE":       cfg.SourceMode,
		"APTEVA_ARTIFACT_MODE":     mode,
		"APTEVA_ARTIFACT_NAME":     cfg.ArtifactName,
		"APTEVA_ARTIFACT_FILE":     cfg.ArtifactFile,
		"APTEVA_STORE_CHANNEL":     cfg.StoreChannel,
		"APTEVA_TARGET_CONFIG_B64": base64.StdEncoding.EncodeToString([]byte(defaultStr(d.TargetConfigJSON, "{}"))),
		"APTEVA_ENV_B64":           base64.StdEncoding.EncodeToString([]byte(defaultStr(d.EnvJSON, "{}"))),
		"APTEVA_BUILD_SPEC_B64":    base64.StdEncoding.EncodeToString(specJSON),
	}
	if cfg.SourceMode == "bundle" {
		if capsule == nil {
			return nil, errors.New("bundle build requires a prepared source capsule")
		}
		values["APTEVA_SOURCE_URL"] = capsule.URL
		values["APTEVA_SOURCE_SHA256"] = capsule.SHA256
		values["APTEVA_SOURCE_SIZE"] = strconv.FormatInt(capsule.Size, 10)
		values["APTEVA_SOURCE_FORMAT"] = capsule.Format
		values["APTEVA_SOURCE_FILENAME"] = sourceCapsuleFilename
	}
	return values, nil
}

func resolvedCloudArtifactMode(cfg cloudBuildConfig, d *Deployment) string {
	if cfg.ArtifactMode != "" {
		return cfg.ArtifactMode
	}
	if d != nil && d.TargetKind == "ios" && cfg.SourceMode == "bundle" {
		return "file"
	}
	if d != nil && d.TargetKind == "ios" {
		return "store_upload"
	}
	return "bundle"
}

func (a *App) downloadAndStageCloudArtifact(bound *sdk.BoundIntegration, d *Deployment, build *Build, artifact *cloudArtifact, mode, distDir string) error {
	if artifact == nil || strings.TrimSpace(artifact.URL) == "" {
		return errors.New("cloud backend returned no artifact URL")
	}
	tmp, err := os.CreateTemp(filepath.Dir(distDir), "cloud-artifact-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if err := downloadCloudArtifact(bound, artifact, tmpPath); err != nil {
		return err
	}
	if mode == "bundle" {
		zr, err := zip.OpenReader(tmpPath)
		if err != nil {
			return errors.New("bundle artifact must be a zip archive")
		}
		defer zr.Close()
		if err := unpackZip(&zr.Reader, distDir); err != nil {
			return err
		}
		return nil
	}
	sourcePath := tmpPath
	sourceName := artifact.Name
	if artifact.Archive {
		unpackedDir, err := os.MkdirTemp(filepath.Dir(distDir), "cloud-file-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(unpackedDir)
		zr, err := zip.OpenReader(tmpPath)
		if err != nil {
			return errors.New("cloud file artifact must be a zip archive")
		}
		if err := unpackZip(&zr.Reader, unpackedDir); err != nil {
			zr.Close()
			return err
		}
		zr.Close()
		sourcePath, sourceName, err = selectCloudArtifactFile(unpackedDir, artifact.FileName)
		if err != nil {
			return err
		}
	}
	name := safeArtifactFilename(sourceName)
	if name == "" {
		name = "artifact"
	}
	dst := filepath.Join(distDir, name)
	if err := os.Rename(sourcePath, dst); err != nil {
		if err := copyMobileFile(sourcePath, dst); err != nil {
			return err
		}
	}
	if d.TargetKind == "android" || d.TargetKind == "ios" {
		cfg, err := parseMobileTargetConfig(d.TargetConfigJSON)
		if err != nil {
			return err
		}
		manifest := artifactManifest{
			Platform: d.TargetKind, Primary: name, PackageName: cfg.PackageName,
			BundleID: cfg.BundleID, VersionName: cfg.VersionName, BuildNumber: cfg.BuildNumber,
			VersionCode: cfg.VersionCode,
			Files:       []artifactFile{mobileArtifactFile(dst, strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), "."))},
		}
		return writeArtifactManifest(distDir, manifest)
	}
	return nil
}

func selectCloudArtifactFile(root, configured string) (string, string, error) {
	configured = filepath.ToSlash(strings.TrimSpace(configured))
	type candidate struct {
		path string
		rel  string
	}
	var files []candidate
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if filepath.Base(rel) == artifactManifestFilename {
			return nil
		}
		if configured != "" && rel != configured && filepath.Base(rel) != filepath.Base(configured) {
			return nil
		}
		files = append(files, candidate{path: path, rel: rel})
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if len(files) == 0 {
		if configured != "" {
			return "", "", fmt.Errorf("artifact_file %q not found in cloud artifact", configured)
		}
		return "", "", errors.New("cloud artifact contains no files")
	}
	if len(files) > 1 {
		return "", "", errors.New("cloud artifact contains multiple files; set artifact_file")
	}
	return files[0].path, filepath.Base(files[0].rel), nil
}

func downloadCloudArtifact(bound *sdk.BoundIntegration, artifact *cloudArtifact, dst string) error {
	req, err := http.NewRequest(http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	if artifact.NeedsGitHubAuth {
		creds, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
		if err != nil {
			return err
		}
		token := strings.TrimSpace(creds.Fields["token"])
		if token == "" {
			return errors.New("GitHub connection has no token")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	for key, value := range artifact.Headers {
		req.Header.Set(key, value)
	}
	client := &http.Client{
		Timeout: 30 * time.Minute,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > 0 && next.URL.Host != via[0].URL.Host {
				next.Header.Del("Authorization")
			}
			if len(via) >= 10 {
				return errors.New("too many artifact redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("artifact download returned HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, maxCloudArtifactBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxCloudArtifactBytes {
		return fmt.Errorf("artifact exceeds %d bytes", maxCloudArtifactBytes)
	}
	return nil
}

func recursiveArtifactURLs(raw json.RawMessage) []cloudArtifact {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	out := []cloudArtifact{}
	var walk func(any)
	walk = func(current any) {
		switch item := current.(type) {
		case map[string]any:
			name := firstMapString(item, "name", "file_name", "filename", "path")
			link := firstMapString(item, "short_lived_download_url", "download_url", "downloadUrl", "secure_url", "url")
			if name != "" && (strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://")) {
				out = append(out, cloudArtifact{Name: name, URL: link})
			}
			for _, child := range item {
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(value)
	return out
}

func firstRecursiveString(raw json.RawMessage, keys ...string) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[key] = true
	}
	var find func(any) string
	find = func(current any) string {
		switch item := current.(type) {
		case map[string]any:
			for key, child := range item {
				if wanted[key] {
					switch v := child.(type) {
					case string:
						return v
					case float64:
						return strconv.FormatInt(int64(v), 10)
					}
				}
			}
			for _, child := range item {
				if found := find(child); found != "" {
					return found
				}
			}
		case []any:
			for _, child := range item {
				if found := find(child); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return find(value)
}

func firstMapString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func appendCloudBuildLog(path, message string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", nowUTC(), message)
	return err
}

func rawOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 || !json.Valid(raw) {
		return "{}"
	}
	return string(raw)
}

func safeArtifactFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func elapsedBuildMs(started, finished string) int64 {
	start, err1 := time.Parse(time.RFC3339, started)
	end, err2 := time.Parse(time.RFC3339, finished)
	if err1 != nil || err2 != nil || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
