package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	defaultCodemagicAdapterRepository = "https://github.com/apteva/deploy-build-adapter"
	defaultCodemagicMobileWorkflow    = "apteva-mobile-capsule"
)

type cloudBackendSetupInput struct {
	Provider      string
	RepositoryURL string
	TeamID        string
	WorkflowID    string
	Branch        string
	ArtifactMode  string
}

type cloudBackendSetupResult struct {
	Provider      string `json:"provider"`
	AppID         string `json:"app_id,omitempty"`
	RepositoryURL string `json:"repository_url"`
	WorkflowID    string `json:"workflow_id"`
	ConfigJSON    string `json:"build_backend_config_json"`
	Created       bool   `json:"created"`
}

type cloudBackendBootstrapper interface {
	Name() string
	Setup(context.Context, *sdk.BoundIntegration, *Deployment, cloudBackendSetupInput) (*cloudBackendSetupResult, error)
}

func cloudBackendBootstrapperFor(provider string) (cloudBackendBootstrapper, error) {
	switch normalizeBuildBackend(provider) {
	case buildBackendCodemagic:
		return codemagicCloudBootstrapper{}, nil
	default:
		return nil, fmt.Errorf("build provider %q does not expose an automated bootstrap adapter", provider)
	}
}

func (a *App) setupCloudBackend(ctx context.Context, d *Deployment, input cloudBackendSetupInput) (*cloudBackendSetupResult, error) {
	if d == nil {
		return nil, errors.New("deployment required")
	}
	provider := normalizeBuildBackend(defaultStr(input.Provider, d.BuildBackend))
	bootstrapper, err := cloudBackendBootstrapperFor(provider)
	if err != nil {
		return nil, err
	}
	bound, err := cloudIntegrationFor(provider)
	if err != nil {
		return nil, err
	}
	result, err := bootstrapper.Setup(ctx, bound, d, input)
	if err != nil {
		return nil, err
	}
	if err := persistEffectiveDeploymentConfig(d, map[string]any{
		"build_backend":             provider,
		"build_backend_config_json": result.ConfigJSON,
	}); err != nil {
		return nil, err
	}
	emit("deploy.cloud_backend.ready", map[string]any{
		"deployment_id": d.ID, "environment_id": d.EnvironmentID,
		"provider": provider, "app_id": result.AppID,
	})
	return result, nil
}

type codemagicCloudBootstrapper struct{}

func (codemagicCloudBootstrapper) Name() string { return buildBackendCodemagic }

func (codemagicCloudBootstrapper) Setup(
	_ context.Context,
	bound *sdk.BoundIntegration,
	d *Deployment,
	input cloudBackendSetupInput,
) (*cloudBackendSetupResult, error) {
	if d.TargetKind != "ios" && d.TargetKind != "android" {
		return nil, errors.New("the maintained Codemagic adapter currently supports iOS and Android targets")
	}
	repositoryURL := strings.TrimRight(strings.TrimSpace(defaultStr(input.RepositoryURL, defaultCodemagicAdapterRepository)), "/")
	workflowID := strings.TrimSpace(defaultStr(input.WorkflowID, defaultCodemagicMobileWorkflow))
	branch := strings.TrimSpace(defaultStr(input.Branch, "main"))
	if !strings.HasPrefix(repositoryURL, "https://") {
		return nil, errors.New("Codemagic adapter repository_url must use https")
	}
	var cfg cloudBuildConfig
	if normalizeBuildBackend(d.BuildBackend) == buildBackendCodemagic {
		_ = json.Unmarshal([]byte(defaultStr(d.BuildBackendJSON, "{}")), &cfg)
	}

	var listed json.RawMessage
	var err error
	if strings.TrimSpace(input.TeamID) == "" {
		listed, err = executeIntegration(bound, "list_personal_apps", map[string]any{
			"page_size": 100, "page": 1,
		})
	} else {
		listed, err = executeIntegration(bound, "list_team_apps", map[string]any{
			"team_id": input.TeamID, "page_size": 100, "page": 1,
		})
	}
	if err != nil {
		return nil, err
	}
	appID := ""
	configuredRepository := normalizeRepositoryURL(cfg.AdapterRepository)
	if cfg.AppID != "" && cfg.WorkflowID == workflowID &&
		(configuredRepository == normalizeRepositoryURL(repositoryURL) ||
			(configuredRepository == "" && strings.TrimSpace(input.RepositoryURL) == "")) &&
		recursiveAppIDExists(listed, cfg.AppID) {
		appID = cfg.AppID
	}
	if appID == "" {
		appID = recursiveRepositoryAppID(listed, repositoryURL)
	}
	if appID == "" {
		appID = recursiveNamedAppID(listed, repositoryName(repositoryURL))
	}
	created := false
	if appID == "" {
		addInput := map[string]any{"repositoryUrl": repositoryURL}
		if strings.TrimSpace(input.TeamID) != "" {
			addInput["teamId"] = strings.TrimSpace(input.TeamID)
		}
		added, err := executeIntegration(bound, "add_app", addInput)
		if err != nil {
			return nil, err
		}
		appID = firstRecursiveString(added, "_id", "appId", "app_id", "id")
		if appID == "" {
			return nil, errors.New("Codemagic add_app returned no app id")
		}
		created = true
	}

	cfg.AppID = appID
	cfg.WorkflowID = workflowID
	cfg.Branch = branch
	cfg.Tag = ""
	cfg.SourceMode = "bundle"
	cfg.AdapterRepository = repositoryURL
	cfg.Preflight = "strict"
	cfg.InstanceType = defaultStr(cfg.InstanceType, "mac_mini_m4")
	cfg.ArtifactName = defaultCloudArtifactName
	cfg.ArtifactMode = strings.ToLower(strings.TrimSpace(input.ArtifactMode))
	if cfg.ArtifactMode == "" {
		if d.TargetKind == "ios" {
			cfg.ArtifactMode = "store_upload"
		} else {
			cfg.ArtifactMode = "file"
			cfg.ArtifactFile = "app.aab"
		}
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := validateBuildBackendSelection(buildBackendCodemagic, string(body)); err != nil {
		return nil, err
	}
	if err := validateMobileCloudContract(d, cfg); err != nil {
		return nil, err
	}
	return &cloudBackendSetupResult{
		Provider: buildBackendCodemagic, AppID: appID,
		RepositoryURL: repositoryURL, WorkflowID: workflowID,
		ConfigJSON: string(body), Created: created,
	}, nil
}

func recursiveRepositoryAppID(raw json.RawMessage, repositoryURL string) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	wanted := normalizeRepositoryURL(repositoryURL)
	var found string
	var walk func(any)
	walk = func(current any) {
		if found != "" {
			return
		}
		switch item := current.(type) {
		case map[string]any:
			repository := firstMapString(item,
				"repositoryUrl", "repository_url", "cloneUrl", "clone_url", "url",
			)
			if normalizeRepositoryURL(repository) == wanted {
				found = firstMapString(item, "_id", "appId", "app_id", "id")
				if found != "" {
					return
				}
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
	return found
}

func recursiveAppIDExists(raw json.RawMessage, appID string) bool {
	return recursiveAppMatch(raw, func(item map[string]any) bool {
		return firstMapString(item, "_id", "appId", "app_id", "id") == appID
	}) != ""
}

func recursiveNamedAppID(raw json.RawMessage, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return recursiveAppMatch(raw, func(item map[string]any) bool {
		return strings.ToLower(firstMapString(item, "name", "appName", "app_name")) == name
	})
}

func recursiveAppMatch(raw json.RawMessage, matches func(map[string]any) bool) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	first := ""
	withBuild := ""
	var walk func(any)
	walk = func(current any) {
		switch item := current.(type) {
		case map[string]any:
			id := firstMapString(item, "_id", "appId", "app_id", "id")
			if id != "" && matches(item) {
				if first == "" {
					first = id
				}
				if firstMapString(item, "last_build_id", "lastBuildId") != "" {
					withBuild = id
				}
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
	if withBuild != "" {
		return withBuild
	}
	return first
}

func repositoryName(repositoryURL string) string {
	normalized := normalizeRepositoryURL(repositoryURL)
	if index := strings.LastIndex(normalized, "/"); index >= 0 {
		return normalized[index+1:]
	}
	return normalized
}

func normalizeRepositoryURL(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, "/")
	value = strings.TrimSuffix(value, ".git")
	return value
}
