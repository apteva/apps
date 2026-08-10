package main

import (
	"encoding/json"
	"net/http"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type cloudBootstrapPlatform struct {
	tk.BasePlatformClient
	existing bool
	listed   json.RawMessage
	calls    []integrationCall
}

func (p *cloudBootstrapPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		InstallID: 42, PublicURL: "https://deploy.test",
		Bindings: map[string]any{"cloud_build": int64(77)},
	}, nil
}

func (p *cloudBootstrapPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "codemagic", Status: "active"}, nil
}

func (p *cloudBootstrapPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	var data []byte
	switch tool {
	case "list_personal_apps":
		if len(p.listed) > 0 {
			data = p.listed
		} else if p.existing {
			data = []byte(`{"apps":[{"_id":"existing-app","repositoryUrl":"https://github.com/apteva/deploy-build-adapter.git"}]}`)
		} else {
			data = []byte(`{"apps":[]}`)
		}
	case "add_app":
		data = []byte(`{"_id":"created-app"}`)
	default:
		data = []byte(`{}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
}

func TestCloudBackendSetupCreatesSharedCodemagicAdapter(t *testing.T) {
	platform := &cloudBootstrapPlatform{}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-app", TargetKind: "ios", SourceKind: "code", SourceRef: "ios-repo",
		Framework: "ios", BuildBackend: "local",
		TargetConfigJSON: `{"bundle_id":"com.example.app","version_name":"1.0","build_number":"1"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	effective := effectiveDeploymentForEnvironment(d, env)
	app := &App{}
	result, err := app.setupCloudBackend(t.Context(), effective, cloudBackendSetupInput{
		Provider: buildBackendCodemagic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.AppID != "created-app" ||
		result.RepositoryURL != defaultCodemagicAdapterRepository {
		t.Fatalf("result=%+v", result)
	}
	if len(platform.calls) != 2 || platform.calls[0].Tool != "list_personal_apps" ||
		platform.calls[1].Tool != "add_app" {
		t.Fatalf("calls=%+v", platform.calls)
	}
	freshEnv, err := dbGetEnvironment(ctx.AppDB(), env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshEnv.BuildBackend != buildBackendCodemagic {
		t.Fatalf("backend=%q", freshEnv.BuildBackend)
	}
	var cfg cloudBuildConfig
	if err := json.Unmarshal([]byte(freshEnv.BuildBackendJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AppID != "created-app" || cfg.WorkflowID != defaultCodemagicMobileWorkflow ||
		cfg.SourceMode != "bundle" || cfg.ArtifactMode != "store_upload" ||
		cfg.InstanceType != "mac_mini_m2" {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestCloudBackendSetupReusesExistingCodemagicAdapter(t *testing.T) {
	platform := &cloudBootstrapPlatform{existing: true}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-app", TargetKind: "android", SourceKind: "code", SourceRef: "android-repo",
		Framework: "android", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"old","workflow_id":"old","branch":"main"}`,
		TargetConfigJSON: `{"package_name":"com.example.app"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	result, err := (&App{}).setupCloudBackend(
		t.Context(), effectiveDeploymentForEnvironment(d, env), cloudBackendSetupInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.AppID != "existing-app" {
		t.Fatalf("result=%+v", result)
	}
	if len(platform.calls) != 1 || platform.calls[0].Tool != "list_personal_apps" {
		t.Fatalf("calls=%+v", platform.calls)
	}
	var cfg cloudBuildConfig
	if err := json.Unmarshal([]byte(result.ConfigJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ArtifactMode != "file" || cfg.ArtifactFile != "app.aab" {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestCloudBackendSetupReusesConfiguredAdapterWhenListOmitsRepository(t *testing.T) {
	platform := &cloudBootstrapPlatform{}
	platform.existing = true
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-app", TargetKind: "ios", SourceKind: "code", SourceRef: "ios-repo",
		Framework: "ios", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"existing-app","workflow_id":"apteva-mobile-capsule","branch":"main"}`,
		TargetConfigJSON: `{"bundle_id":"com.example.app","version_name":"1.0","build_number":"1"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	platform.calls = nil
	platform.existing = false
	platform.listed = json.RawMessage(`{"data":[{"id":"existing-app","name":"deploy-build-adapter"}]}`)

	result, err := (&App{}).setupCloudBackend(
		t.Context(), effectiveDeploymentForEnvironment(d, env), cloudBackendSetupInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.AppID != "existing-app" {
		t.Fatalf("result=%+v", result)
	}
}
