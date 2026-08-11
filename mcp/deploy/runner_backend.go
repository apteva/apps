package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	runnerProtocolVersion = "apteva.runner/v1"
	defaultRunnerTokenEnv = "APTEVA_RUNNER_TOKEN"
)

type runnerSource struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Format string `json:"format"`
}

type runnerBuildSpec struct {
	BuildID          int64             `json:"build_id"`
	ProjectID        string            `json:"project_id,omitempty"`
	Deployment       string            `json:"deployment"`
	Environment      string            `json:"environment,omitempty"`
	TargetKind       string            `json:"target_kind,omitempty"`
	Framework        string            `json:"framework,omitempty"`
	BuildCmd         string            `json:"build_cmd,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	TargetConfigJSON string            `json:"target_config_json,omitempty"`
	MachineClass     string            `json:"machine_class,omitempty"`
	SoftwareVersions map[string]string `json:"software_versions,omitempty"`
}

type runnerCredentials struct {
	AppStore       map[string]string `json:"app_store,omitempty"`
	IOSSigning     map[string]string `json:"ios_signing,omitempty"`
	AndroidSigning map[string]string `json:"android_signing,omitempty"`
}

type runnerJobRequest struct {
	Protocol       string            `json:"protocol"`
	IdempotencyKey string            `json:"idempotency_key"`
	Source         runnerSource      `json:"source"`
	Build          runnerBuildSpec   `json:"build"`
	Credentials    runnerCredentials `json:"credentials,omitempty"`
}

type runnerJobResponse struct {
	Protocol       string `json:"protocol,omitempty"`
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	FinishedAt     string `json:"finished_at,omitempty"`
	ArtifactReady  bool   `json:"artifact_ready,omitempty"`
}

type runnerBuildBackend struct{}

func (runnerBuildBackend) Name() string { return buildBackendRunner }

func (runnerBuildBackend) Submit(ctx context.Context, _ *sdk.BoundIntegration, cfg cloudBuildConfig, d *Deployment, build *Build, capsule *sourceCapsule) (*externalBuildJob, error) {
	if capsule == nil {
		return nil, errors.New("runner build requires a prepared source capsule")
	}
	idempotencyHash := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\n%d\n%s\n%s",
		d.ProjectID, build.ID, capsule.SHA256, capsule.URL,
	)))
	request := runnerJobRequest{
		Protocol:       runnerProtocolVersion,
		IdempotencyKey: hex.EncodeToString(idempotencyHash[:]),
		Source: runnerSource{
			URL: capsule.URL, SHA256: capsule.SHA256, Size: capsule.Size, Format: capsule.Format,
		},
		Build: runnerBuildSpec{
			BuildID: build.ID, ProjectID: d.ProjectID, Deployment: d.Name,
			Environment: d.EnvironmentName, TargetKind: d.TargetKind,
			Framework: d.Framework, BuildCmd: d.BuildCmd,
			Env: parseEnvJSON(d.EnvJSON), TargetConfigJSON: defaultStr(d.TargetConfigJSON, "{}"),
			MachineClass: resolvedMachineClass(cfg), SoftwareVersions: cfg.SoftwareVersions,
		},
		Credentials: runnerBuildCredentials(d),
	}
	var response runnerJobResponse
	raw, err := runnerDoJSON(ctx, cfg, http.MethodPost, "/v1/jobs", request, &response)
	clearRunnerCredentials(&request.Credentials)
	if err != nil {
		return nil, err
	}
	if response.ID == "" {
		return nil, errors.New("runner returned no job id")
	}
	return &externalBuildJob{
		ID: response.ID, Status: defaultStr(response.Status, "queued"),
		MetaJSON: string(raw),
	}, nil
}

func (runnerBuildBackend) Inspect(ctx context.Context, _ *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build) (*externalBuildStatus, error) {
	var response runnerJobResponse
	raw, err := runnerDoJSON(ctx, cfg, http.MethodGet, "/v1/jobs/"+url.PathEscape(build.ExternalJobID), nil, &response)
	if err != nil {
		return nil, err
	}
	if response.Status == "" {
		return nil, errors.New("runner returned no job status")
	}
	return &externalBuildStatus{
		Status: strings.ToLower(response.Status), ProviderRaw: raw, Error: response.Error,
	}, nil
}

func (runnerBuildBackend) Cancel(ctx context.Context, _ *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build) error {
	_, err := runnerDoJSON(ctx, cfg, http.MethodDelete, "/v1/jobs/"+url.PathEscape(build.ExternalJobID), nil, nil)
	return err
}

func (runnerBuildBackend) Artifact(_ context.Context, _ *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build, _ *externalBuildStatus) (*cloudArtifact, error) {
	token, err := runnerAuthToken(cfg)
	if err != nil {
		return nil, err
	}
	return &cloudArtifact{
		Name:    cfg.ArtifactName + ".zip",
		URL:     cfg.RunnerURL + "/v1/jobs/" + url.PathEscape(build.ExternalJobID) + "/artifact",
		Headers: map[string]string{"Authorization": "Bearer " + token},
		Archive: true, FileName: cfg.ArtifactFile,
	}, nil
}

func validateRunnerBaseURL(raw string) error {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return errors.New("runner backend requires runner_url")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("runner_url must be an absolute URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return errors.New("runner_url must use https except for a loopback runner")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("runner_url cannot contain credentials, a query string, or a fragment")
	}
	return nil
}

func runnerAuthToken(cfg cloudBuildConfig) (string, error) {
	name := strings.TrimSpace(cfg.RunnerTokenEnv)
	if name == "" {
		name = defaultRunnerTokenEnv
	}
	token := strings.TrimSpace(os.Getenv(name))
	if token == "" && name == defaultRunnerTokenEnv && globalCtx != nil {
		token = strings.TrimSpace(configOr(globalCtx, "runner_token", ""))
	}
	if token == "" {
		return "", fmt.Errorf("runner authentication token is missing from %s and Deploy's runner_token config", name)
	}
	if len(token) < 32 {
		return "", errors.New("runner authentication token must contain at least 32 characters")
	}
	return token, nil
}

func runnerDoJSON(ctx context.Context, cfg cloudBuildConfig, method, endpoint string, input, output any) (json.RawMessage, error) {
	token, err := runnerAuthToken(cfg)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	requestURL := strings.TrimRight(cfg.RunnerURL, "/") + path.Clean("/"+endpoint)
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > 0 && next.URL.Host != via[0].URL.Host {
				next.Header.Del("Authorization")
			}
			if len(via) >= 10 {
				return errors.New("too many runner redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &apiErr)
		if apiErr.Error == "" {
			apiErr.Error = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("runner returned HTTP %d: %s", resp.StatusCode, defaultStr(apiErr.Error, http.StatusText(resp.StatusCode)))
	}
	if output != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, output); err != nil {
			return nil, fmt.Errorf("decode runner response: %w", err)
		}
	}
	if !json.Valid(raw) {
		raw = json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), raw...), nil
}

func runnerBuildCredentials(d *Deployment) runnerCredentials {
	var out runnerCredentials
	if d == nil {
		return out
	}
	if d.TargetKind == "ios" || d.Framework == "ios" {
		out.AppStore = selectedBoundCredentialFields("app_store", []string{"issuer_id", "key_id", "private_key"})
		out.IOSSigning, _ = (&App{}).iosSigningCredentials(d)
	}
	if d.TargetKind == "android" || d.Framework == "android" {
		out.AndroidSigning, _ = (&App{}).androidSigningCredentials(d)
	}
	return out
}

func (a *App) mobileSigningBuildCredentials(d *Deployment) runnerCredentials {
	if d == nil {
		return runnerCredentials{}
	}
	out := runnerBuildCredentials(d)
	if d.TargetKind == "android" || d.Framework == "android" {
		out.AndroidSigning, _ = a.androidSigningCredentials(d)
	}
	if d.TargetKind == "ios" || d.Framework == "ios" {
		out.IOSSigning, _ = a.iosSigningCredentials(d)
	}
	return out
}

func selectedBoundCredentialFields(role string, keys []string) map[string]string {
	if globalCtx == nil {
		return nil
	}
	creds, err := boundConnectionCredentials(role)
	if err != nil || creds == nil {
		return nil
	}
	out := map[string]string{}
	for _, key := range keys {
		if value := creds.Fields[key]; value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func clearRunnerCredentials(credentials *runnerCredentials) {
	if credentials == nil {
		return
	}
	for _, fields := range []map[string]string{credentials.AppStore, credentials.IOSSigning, credentials.AndroidSigning} {
		for key := range fields {
			fields[key] = strings.Repeat("0", len(fields[key]))
			delete(fields, key)
		}
	}
	credentials.AppStore = nil
	credentials.IOSSigning = nil
	credentials.AndroidSigning = nil
}

func runnerBuildLabel(spec runnerBuildSpec) string {
	parts := []string{spec.ProjectID, spec.Deployment, spec.Environment, strconv.FormatInt(spec.BuildID, 10)}
	return strings.Join(parts, "/")
}
