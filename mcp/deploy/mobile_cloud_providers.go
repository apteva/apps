package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Providers transport the same Deploy contract; recipes never depend on this file.
type bitriseBuildBackend struct{}

func (bitriseBuildBackend) Name() string { return buildBackendBitrise }

func cloudContractEnvironment(cfg cloudBuildConfig, d *Deployment, build *Build, capsule *sourceCapsule) (map[string]string, error) {
	values := cloneStringMap(cfg.Variables)
	contract, err := cloudBuildContractVariables(cfg, d, build, capsule)
	if err != nil {
		return nil, err
	}
	for k, v := range contract {
		values[k] = v
	}
	return values, nil
}

func bitriseEnvironments(values map[string]string) []map[string]any {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		result = append(result, map[string]any{"key": key, "value": values[key], "is_expand": false})
	}
	return result
}

func (bitriseBuildBackend) Submit(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, d *Deployment, build *Build, capsule *sourceCapsule) (*externalBuildJob, error) {
	values, err := cloudContractEnvironment(cfg, d, build, capsule)
	if err != nil {
		return nil, err
	}
	secrets, err := cloudSigningEnvironment(d)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"workflow_id": cfg.WorkflowID, "environments": bitriseEnvironments(values), "secrets": bitriseEnvironments(secrets)}
	if cfg.Branch != "" {
		params["branch"] = cfg.Branch
	} else {
		params["tag"] = cfg.Tag
	}
	if cfg.Stack != "" {
		params["stack"] = cfg.Stack
	}
	if cfg.InstanceType != "" {
		params["machine_type_id"] = cfg.InstanceType
	}
	raw, err := executeIntegration(bound, "trigger_build", map[string]any{"app_slug": cfg.AppID, "hook_info": map[string]any{"type": "bitrise"}, "build_params": params})
	// Bitrise returns build_slug at the root (not the nested application slug).
	// Re-executing submission after a missing ID could create another build.
	if err != nil {
		return nil, errors.New("Bitrise submission failed; inspect the provider before retrying")
	}
	object, err := providerObject(raw)
	if err != nil {
		return nil, err
	}
	var id string
	_ = json.Unmarshal(object["build_slug"], &id)
	if id == "" {
		return nil, errors.New("Bitrise returned no build_slug")
	}
	return &externalBuildJob{ID: id, Status: "queued", MetaJSON: mustJSON(map[string]string{"build_slug": id})}, nil
}

func providerObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("provider returned a non-object payload")
	}
	if data, ok := object["data"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(data, &inner) == nil && inner != nil {
			return inner, nil
		}
	}
	return object, nil
}

func providerStatus(provider string, raw json.RawMessage) (string, error) {
	object, err := providerObject(raw)
	value := raw
	if err == nil {
		value = object["status"]
	}
	if len(value) == 0 || strings.TrimSpace(string(value)) == "null" {
		return "", fmt.Errorf("%s returned missing build status", provider)
	}
	var code int
	if json.Unmarshal(value, &code) == nil {
		codes := map[int]string{0: "running", 1: "succeeded", 2: "failed", 3: "cancelled", 4: "cancelled"}
		if provider == buildBackendAppcircle {
			codes = map[int]string{0: "succeeded", 1: "failed", 2: "cancelled", 3: "timeout", 90: "queued", 91: "running", 92: "finishing", 99: "pending"}
		}
		if status, ok := codes[code]; ok {
			return status, nil
		}
		return "", fmt.Errorf("%s returned unknown status code %d", provider, code)
	}
	var status string
	if json.Unmarshal(value, &status) == nil {
		switch strings.ToLower(status) {
		case "success", "succeeded":
			return "succeeded", nil
		case "failed", "error":
			return "failed", nil
		case "aborted", "cancelled", "canceled", "aborted-with-success":
			return "cancelled", nil
		case "queued", "waiting":
			return "queued", nil
		case "running", "in_progress", "building", "completing", "finishing":
			return "running", nil
		case "timeout":
			return "timeout", nil
		}
	}
	return "", fmt.Errorf("%s returned missing or unknown build status", provider)
}

func unavailableProviderJob(provider string, build *Build, err error) error {
	var toolErr *integrationToolError
	if errors.As(err, &toolErr) {
		if toolErr.Status == http.StatusNotFound {
			return &externalJobUnavailableError{Provider: provider, JobID: build.ExternalJobID, Reason: "provider returned HTTP 404", NotFound: true}
		}
		if toolErr.Status >= 200 && toolErr.Status < 300 {
			var value struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(toolErr.Data, &value) == nil && (value.Error == "response contract violation" || value.Error == "invalid json response") {
				return &externalJobUnavailableError{Provider: provider, JobID: build.ExternalJobID, Reason: "provider response contract violation"}
			}
		}
	}
	return err
}

func (bitriseBuildBackend) Inspect(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build) (*externalBuildStatus, error) {
	raw, err := executeIntegration(bound, "get_build", map[string]any{"app_slug": cfg.AppID, "build_slug": build.ExternalJobID})
	if err != nil {
		return nil, unavailableProviderJob(buildBackendBitrise, build, err)
	}
	status, err := providerStatus(buildBackendBitrise, raw)
	if err != nil {
		return nil, &externalJobUnavailableError{Provider: buildBackendBitrise, JobID: build.ExternalJobID, Reason: err.Error()}
	}
	return &externalBuildStatus{Status: status, ProviderRaw: raw, SourceSHA: firstRecursiveString(raw, "commit_hash"), Error: firstRecursiveString(raw, "abort_reason")}, nil
}

func (bitriseBuildBackend) Cancel(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build) error {
	_, err := executeIntegration(bound, "abort_build", map[string]any{"app_slug": cfg.AppID, "build_slug": build.ExternalJobID, "abort_reason": "Cancelled by Deploy", "abort_with_success": false, "skip_git_status_report": false, "skip_notifications": false})
	return err
}

func (bitriseBuildBackend) Artifact(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build, _ *externalBuildStatus) (*cloudArtifact, error) {
	input := map[string]any{"app_slug": cfg.AppID, "build_slug": build.ExternalJobID, "limit": 50}
	for page := 0; page < 20; page++ {
		raw, err := executeIntegration(bound, "list_build_artifacts", input)
		if err != nil {
			return nil, err
		}
		var listing struct {
			Data []struct {
				Slug  string `json:"slug"`
				Title string `json:"title"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := json.Unmarshal(raw, &listing); err != nil {
			return nil, err
		}
		for _, artifact := range listing.Data {
			if artifact.Title != cfg.ArtifactName && artifact.Title != cfg.ArtifactName+".zip" {
				continue
			}
			details, err := executeIntegration(bound, "get_build_artifact", map[string]any{"app_slug": cfg.AppID, "build_slug": build.ExternalJobID, "artifact_slug": artifact.Slug})
			if err != nil {
				return nil, err
			}
			location := firstRecursiveString(details, "expiring_download_url")
			if location == "" {
				return nil, errors.New("Bitrise artifact has no download URL")
			}
			return &cloudArtifact{Name: artifact.Title, URL: location, Archive: true, FileName: cfg.ArtifactFile}, nil
		}
		if listing.Paging.Next == "" {
			break
		}
		input["next"] = listing.Paging.Next
	}
	return nil, fmt.Errorf("Bitrise artifact %q not found", cfg.ArtifactName)
}

type appcircleBuildBackend struct{}

func (appcircleBuildBackend) Name() string { return buildBackendAppcircle }

// Keep the commit and build identity in the durable job ID. Status polls may
// replace provider metadata and must not lose the IDs needed to fetch artifacts.
type appcircleJob struct {
	TaskID   string `json:"task_id"`
	CommitID string `json:"commit_id"`
	BuildID  string `json:"build_id,omitempty"`
}

func parseAppcircleJob(id string) (appcircleJob, error) {
	var job appcircleJob
	if err := json.Unmarshal([]byte(id), &job); err != nil || job.TaskID == "" || job.CommitID == "" {
		return job, errors.New("invalid Appcircle job identity")
	}
	return job, nil
}

func (appcircleBuildBackend) Submit(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, d *Deployment, build *Build, capsule *sourceCapsule) (*externalBuildJob, error) {
	commit := cfg.CommitID
	if commit == "" {
		raw, err := executeIntegration(bound, "get_last_commit", map[string]any{"profileId": cfg.ProfileID, "branchId": cfg.BranchID})
		if err != nil {
			return nil, err
		}
		commit = firstRecursiveString(raw, "id", "commitId")
		if commit == "" {
			return nil, errors.New("Appcircle returned no adapter commit ID")
		}
	}
	values, err := cloudContractEnvironment(cfg, d, build, capsule)
	if err != nil {
		return nil, err
	}
	raw, err := executeIntegration(bound, "start_build_with_environment", map[string]any{"commitId": commit, "workflowId": cfg.WorkflowID, "configurationId": cfg.ConfigurationID, "environment": values})
	if err != nil {
		return nil, err
	}
	task := firstRecursiveString(raw, "taskId", "task_id", "id")
	if task == "" {
		var value string
		_ = json.Unmarshal(raw, &value)
		task = value
	}
	if task == "" {
		return nil, errors.New("Appcircle returned no task ID")
	}
	job := appcircleJob{TaskID: task, CommitID: commit, BuildID: firstRecursiveString(raw, "buildId")}
	return &externalBuildJob{ID: mustJSON(job), Status: "queued", MetaJSON: mustJSON(job)}, nil
}

func (appcircleBuildBackend) Inspect(_ context.Context, bound *sdk.BoundIntegration, _ cloudBuildConfig, build *Build) (*externalBuildStatus, error) {
	job, err := parseAppcircleJob(build.ExternalJobID)
	if err != nil {
		return nil, err
	}
	var queue json.RawMessage
	if job.BuildID == "" {
		queue, err = executeIntegration(bound, "get_build_queue_item", map[string]any{"taskId": job.TaskID})
		if err != nil {
			return nil, unavailableProviderJob(buildBackendAppcircle, build, err)
		}
		if id := firstRecursiveString(queue, "buildId"); id != "" && id != "00000000-0000-0000-0000-000000000000" {
			job.BuildID = id
		}
	}
	raw := queue
	if job.BuildID != "" {
		raw, err = executeIntegration(bound, "get_build_status", map[string]any{"commitId": job.CommitID, "buildId": job.BuildID})
		if err != nil {
			return nil, unavailableProviderJob(buildBackendAppcircle, build, err)
		}
	}
	status, err := providerStatus(buildBackendAppcircle, raw)
	if job.BuildID == "" {
		status, err = appcircleQueueStatus(queue)
	}
	if err != nil {
		return nil, &externalJobUnavailableError{Provider: buildBackendAppcircle, JobID: build.ExternalJobID, Reason: err.Error()}
	}
	if status == "succeeded" && job.BuildID == "" {
		return nil, errors.New("Appcircle finished without a build ID")
	}
	if job.BuildID != "" && mustJSON(job) != build.ExternalJobID {
		if globalCtx != nil && globalCtx.AppDB() != nil {
			if err := dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"external_job_id": mustJSON(job)}); err != nil {
				return nil, err
			}
		}
		build.ExternalJobID = mustJSON(job)
	}
	return &externalBuildStatus{Status: status, ProviderRaw: raw, Error: firstRecursiveString(queue, "message", "error")}, nil
}

func (appcircleBuildBackend) Cancel(_ context.Context, bound *sdk.BoundIntegration, _ cloudBuildConfig, build *Build) error {
	job, err := parseAppcircleJob(build.ExternalJobID)
	if err != nil {
		return err
	}
	_, err = executeIntegration(bound, "cancel_build", map[string]any{"taskId": job.TaskID})
	return err
}

func appcircleToken(ctx context.Context, bound *sdk.BoundIntegration) (string, error) {
	credentials, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil || credentials == nil {
		return "", errors.New("Appcircle credentials unavailable")
	}
	fields := credentials.Fields
	if fields["api_key_name"] == "" || fields["api_key_secret"] == "" {
		return "", errors.New("Appcircle organization API key is incomplete")
	}
	form := url.Values{"name": {fields["api_key_name"]}, "secret": {fields["api_key_secret"]}}
	if fields["organization_id"] != "" {
		form.Set("organizationId", fields["organization_id"])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://auth.appcircle.io/auth/v1/api-key/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("Appcircle authentication request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Appcircle authentication returned HTTP %d", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&token) != nil || token.AccessToken == "" {
		return "", errors.New("Appcircle authentication returned no access token")
	}
	return token.AccessToken, nil
}

func appcircleDownloadURL(job appcircleJob, suffix string) string {
	return "https://api.appcircle.io/build/v2/commits/" + url.PathEscape(job.CommitID) + "/builds/" + url.PathEscape(job.BuildID) + suffix
}

func (appcircleBuildBackend) Artifact(ctx context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, build *Build, _ *externalBuildStatus) (*cloudArtifact, error) {
	job, err := parseAppcircleJob(build.ExternalJobID)
	if err != nil {
		return nil, err
	}
	if job.BuildID == "" {
		return nil, errors.New("Appcircle artifact requires a resolved build ID")
	}
	token, err := appcircleToken(ctx, bound)
	if err != nil {
		return nil, err
	}
	return &cloudArtifact{Name: cfg.ArtifactName + ".zip", URL: appcircleDownloadURL(job, ""), Headers: map[string]string{"Authorization": "Bearer " + token}, Archive: true, ArchiveEntry: cfg.ArtifactName + ".zip", FileName: cfg.ArtifactFile}, nil
}

// Only credential values enter provider secret fields, never build snapshots.
func cloudSigningEnvironment(d *Deployment) (map[string]string, error) {
	credentials, err := (&App{}).mobileSigningBuildCredentials(d)
	if err != nil {
		return nil, err
	}
	defer clearRunnerCredentials(&credentials)
	values := map[string]string{}
	for key, name := range map[string]string{"issuer_id": "APP_STORE_CONNECT_ISSUER_ID", "key_id": "APP_STORE_CONNECT_KEY_IDENTIFIER", "private_key": "APP_STORE_CONNECT_PRIVATE_KEY"} {
		if value := credentials.AppStore[key]; value != "" {
			values[name] = value
		}
	}
	for key, name := range map[string]string{"certificate_private_key": "CERTIFICATE_PRIVATE_KEY", "certificate_pem": "APTEVA_CERTIFICATE_PEM", "provisioning_profile_base64": "APTEVA_PROVISIONING_PROFILE_BASE64", "certificate_sha256": "APTEVA_APPLE_CERT_SHA256"} {
		if value := credentials.IOSSigning[key]; value != "" {
			values[name] = value
		}
	}
	for key, name := range map[string]string{"upload_keystore_base64": "ANDROID_UPLOAD_KEYSTORE_BASE64", "upload_key_alias": "ANDROID_UPLOAD_KEY_ALIAS", "upload_keystore_password": "ANDROID_UPLOAD_STORE_PASSWORD", "upload_key_password": "ANDROID_UPLOAD_KEY_PASSWORD", "upload_certificate_sha256": "ANDROID_UPLOAD_CERT_SHA256"} {
		if value := credentials.AndroidSigning[key]; value != "" {
			values[name] = value
		}
	}
	return values, nil
}

func appcircleQueueStatus(raw json.RawMessage) (string, error) {
	object, err := providerObject(raw)
	if err != nil {
		return "", err
	}
	var code *int
	if json.Unmarshal(object["queueItemStatus"], &code) != nil || code == nil {
		return "", errors.New("Appcircle returned no queueItemStatus")
	}
	// QueueItemStatus differs from BuildStatus; a completed queue alone is not evidence of a successful artifact.
	switch *code {
	case 0:
		return "queued", nil
	case 1:
		return "running", nil
	case 2:
		return "finishing", nil
	case 91:
		return "cancelled", nil
	case 92:
		return "failed", nil
	case 90:
		return "", errors.New("Appcircle queue completed without a build ID")
	}
	return "", fmt.Errorf("unknown Appcircle queue status %d", *code)
}

func managedSigningVariable(name string) bool {
	switch name {
	case "APP_STORE_CONNECT_ISSUER_ID", "APP_STORE_CONNECT_KEY_IDENTIFIER", "APP_STORE_CONNECT_PRIVATE_KEY", "CERTIFICATE_PRIVATE_KEY", "APTEVA_CERTIFICATE_PEM", "APTEVA_PROVISIONING_PROFILE_BASE64", "APTEVA_APPLE_CERT_SHA256", "ANDROID_UPLOAD_KEYSTORE_BASE64", "ANDROID_UPLOAD_KEY_ALIAS", "ANDROID_UPLOAD_STORE_PASSWORD", "ANDROID_UPLOAD_KEY_PASSWORD", "ANDROID_UPLOAD_CERT_SHA256", "GOOGLE_PLAY_SERVICE_ACCOUNT_CREDENTIALS":
		return true
	}
	return false
}
