package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"howett.net/plist"
)

type cloudBuildPlatform struct {
	tk.BasePlatformClient
	provider       string
	artifactURL    string
	status         string
	actionName     string
	getBuildData   []byte
	getBuildStatus int
	cancelStatus   int
	calls          []integrationCall
}

func (p *cloudBuildPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		InstallID: 42, PublicURL: "https://deploy.test",
		Bindings: map[string]any{"cloud_build": int64(77)},
	}, nil
}

func (p *cloudBuildPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: p.provider, Status: "active"}, nil
}

func (p *cloudBuildPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	var data []byte
	switch tool {
	case "start_build":
		data = []byte(`{"buildId":"cm-42"}`)
	case "get_build":
		if p.getBuildData != nil {
			status := p.getBuildStatus
			if status == 0 {
				status = http.StatusOK
			}
			return &sdk.ExecuteResult{Success: status < 400, Status: status, Data: p.getBuildData}, nil
		}
		status := p.status
		if status == "" {
			status = "finished"
		}
		payload := map[string]any{
			"status": status,
			"commit": map[string]any{
				"commitHash": "abc123",
				"message":    "Add generic Apteva mobile capsule workflow",
			},
		}
		if p.artifactURL != "" {
			payload["artifacts"] = []any{map[string]any{
				"name": "apteva-build.zip", "short_lived_download_url": p.artifactURL,
			}}
		}
		data, _ = json.Marshal(payload)
	case "cancel_build":
		if p.cancelStatus != 0 {
			return &sdk.ExecuteResult{
				Success: p.cancelStatus < 400, Status: p.cancelStatus,
				Data: []byte(`{"error":"not_found"}`),
			}, nil
		}
		data = []byte(`{}`)
	case "list_build_actions":
		data, _ = json.Marshal(map[string]any{
			"actions": []map[string]any{{
				"name": p.actionName, "status": "failed",
			}},
		})
	case "trigger_workflow":
		data = []byte(`{}`)
	default:
		data = []byte(`{}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
}

func withCloudBuildContext(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	return ctx
}

func TestCloudBuildConfigValidation(t *testing.T) {
	if got := normalizeBuildBackend("github-actions"); got != buildBackendGitHubActions {
		t.Fatalf("normalize=%q", got)
	}
	if err := validateBuildBackendSelection("local", `{}`); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildBackendSelection("codemagic", `{"app_id":"app","workflow_id":"ios","branch":"main"}`); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildBackendSelection("github_actions", `{"owner":"acme","repo":"api","workflow_id":"deploy.yml","ref":"main"}`); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildBackendSelection("codemagic", `{"app_id":"app","workflow_id":"ios"}`); err == nil {
		t.Fatal("expected missing branch/tag validation error")
	}
	if err := validateBuildBackendSelection("codemagic", `{"app_id":"app","workflow_id":"ios","branch":"main","source_mode":"bundle"}`); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildBackendSelection("github_actions", `{"owner":"acme","repo":"api","workflow_id":"deploy.yml","ref":"main","source_mode":"bundle"}`); err == nil {
		t.Fatal("expected github bundle source validation error")
	}
	if err := validateBuildBackendSelection("github_actions", `{"owner":"acme","repo":"api","workflow_id":"deploy.yml","ref":"main","source_mode":"bundle","contract_inputs":true}`); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseCloudBuildConfig(
		"codemagic",
		`{"app_id":"app","workflow_id":"ios","branch":"main","xcode_version":"26.6"}`,
	)
	if err != nil || cfg.SoftwareVersions["xcode"] != "26.6" {
		t.Fatalf("xcode alias config=%+v err=%v", cfg, err)
	}
}

func TestStrictIOSPreflightRejectsStaleSigningBeforeCodemagicSubmission(t *testing.T) {
	root, _ := newIOSPreflightFixture(t)
	writeTestFile(t, filepath.Join(root, "project.yml"), `
settings:
  INFOPLIST_KEY_UISupportedInterfaceOrientations: UIInterfaceOrientationPortrait
  CODE_SIGN_ENTITLEMENTS: Example/App.entitlements
`)
	writeIOSPlist(t, filepath.Join(root, "Example", "App.entitlements"), plist.XMLFormat, map[string]any{
		"aps-environment": "production",
	})
	platform := &cloudBuildPlatform{provider: "codemagic"}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "stale-signing", TargetKind: "ios", SourceKind: "local", SourceRef: root,
		Framework: "ios", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"runner","workflow_id":"ios","branch":"main","source_mode":"bundle","preflight":"strict"}`,
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
	if _, err := dbUpsertMobileSigningSetup(ctx.AppDB(), &MobileSigningSetup{
		DeploymentID: d.ID, EnvironmentID: env.ID, Platform: "ios", Provider: "codemagic",
		BundleID: "com.example.app", Status: mobileSigningStatusReady,
		RequirementsHash: "stale", ProvisionedFeaturesJSON: "[]",
	}); err != nil {
		t.Fatal(err)
	}
	build, err := (&App{dataDir: t.TempDir()}).submitCloudBuild(t.Context(), effective)
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "failed" || !strings.Contains(build.Error, "PUSH_NOTIFICATIONS") {
		t.Fatalf("build=%+v", build)
	}
	if signingCallCount(platform.calls, "start_build") != 0 {
		t.Fatalf("Codemagic was submitted despite stale signing: calls=%v", platform.calls)
	}
}

func TestCodemagicBundleSourceCapsuleRoundTrip(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "App.swift"), []byte("print(\"hello\")\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceDir, "node_modules", "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "node_modules", "ignored", "index.js"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	platform := &cloudBuildPlatform{provider: "codemagic", status: "building"}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-source", TargetKind: "ios", SourceKind: "local", SourceRef: sourceDir,
		Framework: "ios", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"runner","workflow_id":"apteva-ios-runner","branch":"main","source_mode":"bundle","preflight":"off","software_versions":{"xcode":"26.6"}}`,
		EnvJSON:          `{"API_URL":"https://staging.example.test"}`,
		TargetConfigJSON: `{"bundle_id":"com.example.source"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{dataDir: t.TempDir(), retainRollbacks: 3}
	if err := os.MkdirAll(filepath.Join(app.dataDir, "builds"), 0o755); err != nil {
		t.Fatal(err)
	}
	build, err := app.submitCloudBuild(context.Background(), effectiveDeploymentForEnvironment(d, env))
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "running" || build.SourceSHA == "" {
		t.Fatalf("submitted build=%+v", build)
	}
	capsulePath := filepath.Join(app.buildDir(build.ID), sourceCapsuleFilename)
	if _, err := os.Stat(capsulePath); err != nil {
		t.Fatalf("source capsule missing: %v", err)
	}

	start := platform.calls[0]
	environment, ok := start.Input["environment"].(map[string]any)
	if !ok {
		t.Fatalf("environment=%#v", start.Input["environment"])
	}
	if start.Input["instanceType"] != "mac_mini_m2" {
		t.Fatalf("default instance type=%#v", start.Input["instanceType"])
	}
	softwareVersions, ok := environment["softwareVersions"].(map[string]string)
	if !ok || softwareVersions["xcode"] != "26.6" {
		t.Fatalf("software versions=%#v", environment["softwareVersions"])
	}
	variables, ok := environment["variables"].(map[string]string)
	if !ok {
		t.Fatalf("variables=%#v", environment["variables"])
	}
	sourceURL, err := url.Parse(variables["APTEVA_SOURCE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "/api/apps/deploy/_install/42/source-capsules/" + strconv.FormatInt(build.ID, 10) + "/source.zip"
	if sourceURL.Scheme != "https" || sourceURL.Host != "deploy.test" || sourceURL.Path != wantPath {
		t.Fatalf("source URL=%s", sourceURL.String())
	}
	if variables["APTEVA_SOURCE_FORMAT"] != sourceCapsuleFormat ||
		variables["APTEVA_PROTOCOL"] != cloudBuildProtocolVersion ||
		variables["APTEVA_TARGET_KIND"] != "ios" ||
		variables["APTEVA_SOURCE_SHA256"] != build.SourceSHA {
		t.Fatalf("source variables=%#v", variables)
	}
	targetConfig, err := base64.StdEncoding.DecodeString(variables["APTEVA_TARGET_CONFIG_B64"])
	if err != nil || string(targetConfig) != `{"bundle_id":"com.example.source"}` {
		t.Fatalf("target config=%q err=%v", targetConfig, err)
	}

	requestURL := "/source-capsules/" + strconv.FormatInt(build.ID, 10) + "/source.zip?" + sourceURL.RawQuery
	req := httptest.NewRequest(http.MethodGet, requestURL, nil)
	rec := httptest.NewRecorder()
	app.handleSourceCapsule(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capsule fetch status=%d body=%s", rec.Code, rec.Body.String())
	}
	sum := sha256.Sum256(rec.Body.Bytes())
	if got := hex.EncodeToString(sum[:]); got != variables["APTEVA_SOURCE_SHA256"] {
		t.Fatalf("download sha256=%s want=%s", got, variables["APTEVA_SOURCE_SHA256"])
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range zr.File {
		names = append(names, entry.Name)
	}
	if len(names) != 1 || names[0] != "App.swift" {
		t.Fatalf("capsule entries=%v", names)
	}

	// The signing key is persisted in DataDir, so a sidecar restart does
	// not invalidate a build already queued at the provider.
	restarted := &App{dataDir: app.dataDir}
	restartRec := httptest.NewRecorder()
	restarted.handleSourceCapsule(restartRec, httptest.NewRequest(http.MethodGet, requestURL, nil))
	if restartRec.Code != http.StatusOK {
		t.Fatalf("fetch after restart status=%d body=%s", restartRec.Code, restartRec.Body.String())
	}

	tampered := *sourceURL
	query := tampered.Query()
	query.Set("project_id", "other")
	tampered.RawQuery = query.Encode()
	tamperRec := httptest.NewRecorder()
	app.handleSourceCapsule(tamperRec, httptest.NewRequest(
		http.MethodGet,
		"/source-capsules/"+strconv.FormatInt(build.ID, 10)+"/source.zip?"+tampered.RawQuery,
		nil,
	))
	if tamperRec.Code != http.StatusForbidden {
		t.Fatalf("tampered project status=%d", tamperRec.Code)
	}

	cancelled, err := app.cancelCloudBuild(context.Background(), build)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("cancelled build=%+v", cancelled)
	}
	if _, err := os.Stat(capsulePath); !os.IsNotExist(err) {
		t.Fatalf("terminal build retained source capsule: %v", err)
	}
}

func TestSourceCapsuleRejectsExpiredURLAndCleanupRemovesIt(t *testing.T) {
	platform := &cloudBuildPlatform{provider: "codemagic"}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "expired-source", TargetKind: "ios", SourceKind: "local", SourceRef: t.TempDir(),
		Framework: "ios", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"runner","workflow_id":"ios","branch":"main"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuildForEnvBackend(ctx.AppDB(), d.ID, 0, "ios", "", "codemagic", d.BuildBackendJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{"status": "running"}); err != nil {
		t.Fatal(err)
	}
	app := &App{dataDir: t.TempDir()}
	if err := os.MkdirAll(app.buildDir(build.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("expired")
	sum := sha256.Sum256(body)
	sumHex := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(app.buildDir(build.ID), sourceCapsuleFilename), body, 0o600); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(-time.Minute).Unix()
	if err := app.writeSourceCapsuleMeta(build.ID, sourceCapsuleMeta{
		BuildID: build.ID, Project: "p1", SHA256: sumHex, Size: int64(len(body)),
		Format: sourceCapsuleFormat, Expires: expires, Created: nowUTC(),
	}); err != nil {
		t.Fatal(err)
	}
	key, err := app.sourceCapsuleSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	sig := signSourceCapsule(key, build.ID, "p1", sumHex, expires)
	requestURL := fmt.Sprintf(
		"/source-capsules/%d/source.zip?project_id=p1&exp=%d&sig=%s",
		build.ID, expires, sig,
	)
	rec := httptest.NewRecorder()
	app.handleSourceCapsule(rec, httptest.NewRequest(http.MethodGet, requestURL, nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("expired fetch status=%d body=%s", rec.Code, rec.Body.String())
	}
	fresh, err := dbGetBuild(ctx.AppDB(), build.ID)
	if err != nil {
		t.Fatal(err)
	}
	count, _ := app.cleanupSourceCapsules([]Build{*fresh}, time.Now())
	if count != 1 {
		t.Fatalf("removed capsules=%d", count)
	}
}

func TestCloudBuildPreflightFailureIsNotSubmitted(t *testing.T) {
	sourceDir, _ := newIOSPreflightFixture(t)
	writeTestFile(t, filepath.Join(sourceDir, "project.yml"), `
name: Example
targets:
  Example:
    type: application
    platform: iOS
`)

	platform := &cloudBuildPlatform{provider: "codemagic"}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-preflight", TargetKind: "ios", SourceKind: "local", SourceRef: sourceDir,
		Framework: "ios", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"runner","workflow_id":"ios","branch":"main","source_mode":"bundle"}`,
		TargetConfigJSON: `{"bundle_id":"com.example.app"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{dataDir: t.TempDir(), retainRollbacks: 3}
	build, err := app.submitCloudBuild(context.Background(), effectiveDeploymentForEnvironment(d, env))
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "failed" || build.ExternalStatus != "not_submitted" || build.ExternalJobID != "" {
		t.Fatalf("preflight build=%+v", build)
	}
	if !strings.Contains(build.Error, "does not declare supported interface orientations") {
		t.Fatalf("preflight error=%q", build.Error)
	}
	if len(platform.calls) != 0 {
		t.Fatalf("provider calls=%+v", platform.calls)
	}
}

func TestGitHubSubmitIncludesRequiredCorrelationInput(t *testing.T) {
	platform := &cloudBuildPlatform{provider: "github"}
	withCloudBuildContext(t, platform)
	cfg := cloudBuildConfig{
		Owner: "acme", Repo: "api", WorkflowID: "deploy.yml", Ref: "main",
		Inputs: map[string]any{"release": true},
	}
	job, err := (githubActionsBuildBackend{}).Submit(
		context.Background(),
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "github"},
		cfg, &Deployment{Name: "api"}, &Build{ID: 9}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || len(platform.calls) != 1 {
		t.Fatalf("job=%+v calls=%+v", job, platform.calls)
	}
	inputs, ok := platform.calls[0].Input["inputs"].(map[string]any)
	if !ok || len(inputs) != 2 || inputs["release"] != true || !strings.HasPrefix(fmt.Sprint(inputs["apteva_deploy_run_id"]), "apteva-deploy-9-") {
		t.Fatalf("workflow inputs=%#v", platform.calls[0].Input["inputs"])
	}
}

func TestCodemagicCloudBuildStagesBundle(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("cloud output"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()

	platform := &cloudBuildPlatform{provider: "codemagic", artifactURL: server.URL}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "site", TargetKind: "service", SourceKind: "code", SourceRef: "repo-1",
		Framework: "static", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"cm-app","workflow_id":"web","branch":"main","artifact_mode":"bundle"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	effective := effectiveDeploymentForEnvironment(d, env)
	app := &App{dataDir: t.TempDir(), retainRollbacks: 3}
	build, err := app.submitCloudBuild(context.Background(), effective)
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "running" || build.ExternalJobID != "cm-42" {
		t.Fatalf("submitted build=%+v", build)
	}
	if err := app.syncCloudBuild(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetBuild(ctx.AppDB(), build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "succeeded" || fresh.SourceSHA != "abc123" {
		t.Fatalf("completed build=%+v", fresh)
	}
	body, err := os.ReadFile(filepath.Join(fresh.ArtifactPath, "index.html"))
	if err != nil || string(body) != "cloud output" {
		t.Fatalf("artifact body=%q err=%v", body, err)
	}
}

func TestGitHubFileArtifactExtractsSelectedMobileBinary(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for name, body := range map[string]string{
		"outputs/app-release.aab": "signed-aab",
		"outputs/mapping.txt":     "symbols",
	} {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()

	platform := &cloudBuildPlatform{provider: "github"}
	withCloudBuildContext(t, platform)
	dist := t.TempDir()
	app := &App{dataDir: t.TempDir()}
	deployment := &Deployment{
		TargetKind: "android", TargetConfigJSON: `{"package_name":"com.example.app"}`,
	}
	build := &Build{ID: 12}
	err := app.downloadAndStageCloudArtifact(
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "github"},
		deployment,
		build,
		&cloudArtifact{
			Name: "apteva-build.zip", URL: server.URL, Archive: true,
			FileName: "outputs/app-release.aab",
		},
		"file",
		dist,
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dist, "app-release.aab"))
	if err != nil || string(body) != "signed-aab" {
		t.Fatalf("artifact body=%q err=%v", body, err)
	}
	manifest, err := readArtifactManifest(&Build{ArtifactPath: dist})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Primary != "app-release.aab" || manifest.Platform != "android" {
		t.Fatalf("manifest=%+v", manifest)
	}
}

func TestCloudBuildCancellation(t *testing.T) {
	platform := &cloudBuildPlatform{provider: "codemagic", status: "building"}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "api", TargetKind: "service", SourceKind: "code", SourceRef: "repo-1",
		Framework: "go", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"cm-app","workflow_id":"api","branch":"main"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	app := &App{dataDir: t.TempDir()}
	build, err := app.submitCloudBuild(context.Background(), effectiveDeploymentForEnvironment(d, env))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := app.cancelCloudBuild(context.Background(), build)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" || platform.calls[len(platform.calls)-1].Tool != "cancel_build" {
		t.Fatalf("cancelled=%+v calls=%+v", cancelled, platform.calls)
	}
}

func TestIOSCloudBuildAdoptsStoreUpload(t *testing.T) {
	platform := &cloudBuildPlatform{provider: "codemagic"}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-app", TargetKind: "ios", SourceKind: "code", SourceRef: "repo-1",
		Framework: "ios", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"cm-app","workflow_id":"ios-release","branch":"main","artifact_mode":"store_upload"}`,
		TargetConfigJSON: `{"bundle_id":"com.example.app","app_store_app_id":"app-1","version_name":"1.0.0","build_number":"42","device_families":["iphone"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	effective := effectiveDeploymentForEnvironment(d, env)
	app := &App{dataDir: t.TempDir(), retainRollbacks: 3}
	build, err := app.submitCloudBuild(context.Background(), effective)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{
		"release_requested":    true,
		"release_options_json": `{"channel":"internal"}`,
	}); err != nil {
		t.Fatal(err)
	}
	build, _ = dbGetBuild(ctx.AppDB(), build.ID)
	if err := app.syncCloudBuild(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	build, _ = dbGetBuild(ctx.AppDB(), build.ID)
	if build.ReleaseRequested {
		t.Fatal("deferred release request was not consumed")
	}
	releases, err := dbListReleasesForEnv(ctx.AppDB(), d.ID, env.ID, 1)
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	release := releases[0]
	if release.Status != "starting" || release.Provider != "app_store_connect" ||
		release.ExternalID != "uploaded-cm-42" || release.ExternalStatus != "uploaded_processing" {
		t.Fatalf("release=%+v", release)
	}
}

func TestAndroidCloudBuildAdoptsStoreUpload(t *testing.T) {
	platform := &cloudBuildPlatform{provider: "codemagic"}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-app", TargetKind: "android", SourceKind: "code", SourceRef: "repo-1",
		Framework: "android", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"cm-app","workflow_id":"android-release","branch":"main","artifact_mode":"store_upload"}`,
		TargetConfigJSON: `{"package_name":"com.example.app","version_name":"1.2.3","version_code":"123"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	effective := effectiveDeploymentForEnvironment(d, env)
	app := &App{dataDir: t.TempDir(), retainRollbacks: 3}
	build, err := app.submitCloudBuildWithOptions(context.Background(), effective, &releaseOptions{Channel: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	var buildCfg cloudBuildConfig
	if err := json.Unmarshal([]byte(build.BuildBackendJSON), &buildCfg); err != nil {
		t.Fatal(err)
	}
	if buildCfg.StoreChannel != "internal" {
		t.Fatalf("build config store_channel=%q", buildCfg.StoreChannel)
	}
	startEnvironment := platform.calls[0].Input["environment"].(map[string]any)
	startVariables := startEnvironment["variables"].(map[string]string)
	if _, exists := startEnvironment["groups"]; exists {
		t.Fatalf("unset Codemagic groups must be omitted, got %#v", startEnvironment["groups"])
	}
	if startVariables["APTEVA_TARGET_KIND"] != "android" ||
		startVariables["APTEVA_STORE_CHANNEL"] != "internal" ||
		startVariables["APTEVA_SIGNING_CONTRACT"] != mobileSigningArtifactContractVersion {
		t.Fatalf("contract variables=%#v", startVariables)
	}
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{
		"release_requested":    true,
		"release_options_json": `{"channel":"internal"}`,
	}); err != nil {
		t.Fatal(err)
	}
	build, _ = dbGetBuild(ctx.AppDB(), build.ID)
	if err := app.syncCloudBuild(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	releases, err := dbListReleasesForEnv(ctx.AppDB(), d.ID, env.ID, 1)
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	release := releases[0]
	if release.Status != "live" || release.Provider != "google_play" ||
		release.ExternalID != "123" || release.ExternalStatus != "completed" {
		t.Fatalf("release=%+v", release)
	}
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(release.ReleaseMetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.VersionCode != "123" || meta.PackageName != "com.example.app" {
		t.Fatalf("meta=%+v", meta)
	}
}

func TestCodemagicSubmissionIncludesConfiguredGroups(t *testing.T) {
	platform := &cloudBuildPlatform{provider: "codemagic"}
	withCloudBuildContext(t, platform)
	cfg, err := parseCloudBuildConfig("codemagic", `{
		"app_id":"cm-app","workflow_id":"ios","branch":"main",
		"groups":["appstore-signing"]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (codemagicBuildBackend{}).Submit(
		context.Background(),
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "codemagic"},
		cfg, &Deployment{TargetKind: "ios", TargetConfigJSON: `{}`},
		&Build{ID: 1}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	environment := platform.calls[0].Input["environment"].(map[string]any)
	groups, ok := environment["groups"].([]string)
	if !ok || len(groups) != 1 || groups[0] != "appstore-signing" {
		t.Fatalf("groups=%#v", environment["groups"])
	}
}

func TestCodemagicFailureUsesFailedActionNotCommitMessage(t *testing.T) {
	platform := &cloudBuildPlatform{
		provider: "codemagic", status: "failed", actionName: "Publishing",
	}
	withCloudBuildContext(t, platform)
	status, err := (codemagicBuildBackend{}).Inspect(
		context.Background(),
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "codemagic"},
		cloudBuildConfig{},
		&Build{ExternalJobID: "cm-failed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.Error != "Codemagic failed action: Publishing" {
		t.Fatalf("error=%q", status.Error)
	}
}

func TestCodemagicInspectRejectsNonObjectResponse(t *testing.T) {
	platform := &cloudBuildPlatform{
		provider: "codemagic", getBuildData: []byte(`"<!doctype html><html>Codemagic</html>"`),
	}
	withCloudBuildContext(t, platform)
	_, err := (codemagicBuildBackend{}).Inspect(
		context.Background(),
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "codemagic"},
		cloudBuildConfig{}, &Build{ExternalJobID: "cm-html"},
	)
	var unavailable *externalJobUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(unavailable.Reason, "non-object") {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestCodemagicInspectAcceptsWrappedBuildObject(t *testing.T) {
	platform := &cloudBuildPlatform{
		provider: "codemagic", getBuildData: []byte(`{"data":{"status":"building","commitHash":"abc"}}`),
	}
	withCloudBuildContext(t, platform)
	status, err := (codemagicBuildBackend{}).Inspect(
		context.Background(),
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "codemagic"},
		cloudBuildConfig{}, &Build{ExternalJobID: "cm-wrapped"},
	)
	if err != nil || status.Status != "building" || status.SourceSHA != "abc" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestUnavailableCodemagicJobIsBounded(t *testing.T) {
	platform := &cloudBuildPlatform{
		provider: "codemagic", getBuildData: []byte(`"<!doctype html><html>Codemagic</html>"`),
	}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-unavailable", TargetKind: "android", SourceKind: "code", SourceRef: "repo-1",
		Framework: "android", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"cm-app","workflow_id":"android","branch":"main"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuildForEnvBackend(ctx.AppDB(), d.ID, 0, "android", "", "codemagic", d.BuildBackendJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{
		"status": "running", "external_job_id": "cm-ghost",
		"external_submitted_at": nowUTC(),
	}); err != nil {
		t.Fatal(err)
	}
	build, _ = dbGetBuild(ctx.AppDB(), build.ID)
	app := &App{dataDir: t.TempDir()}
	if err := app.syncCloudBuild(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	fresh, _ := dbGetBuild(ctx.AppDB(), build.ID)
	if fresh.Status != "running" || fresh.ExternalStatus != "propagating" ||
		fresh.ExternalPollAttempts != 1 || fresh.ExternalNextPollAt == "" || fresh.ExternalLastPollErr == "" {
		t.Fatalf("propagating build=%+v", fresh)
	}

	old := time.Now().UTC().Add(-cloudBuildPropagationWait - time.Second).Format(time.RFC3339)
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{
		"external_submitted_at": old, "external_next_poll_at": "",
	}); err != nil {
		t.Fatal(err)
	}
	fresh, _ = dbGetBuild(ctx.AppDB(), build.ID)
	if err := app.syncCloudBuild(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	fresh, _ = dbGetBuild(ctx.AppDB(), build.ID)
	if fresh.Status != "failed" || fresh.ExternalStatus != "provider_job_unavailable" ||
		!strings.Contains(fresh.Error, "Codemagic returned build ID cm-ghost") {
		t.Fatalf("terminal build=%+v", fresh)
	}
	pending, err := dbListPendingCloudBuilds(ctx.AppDB(), 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestCodemagicCancelNotFoundConfirmsCancellation(t *testing.T) {
	platform := &cloudBuildPlatform{provider: "codemagic", cancelStatus: http.StatusNotFound}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "cancel-missing", TargetKind: "service", SourceKind: "code", SourceRef: "repo-1",
		Framework: "go", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"cm-app","workflow_id":"api","branch":"main"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	app := &App{dataDir: t.TempDir()}
	build, err := app.submitCloudBuild(context.Background(), effectiveDeploymentForEnvironment(d, env))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := app.cancelCloudBuild(context.Background(), build)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}

	platform.cancelStatus = http.StatusUnprocessableEntity
	build.Status = "running"
	if _, err := app.cancelCloudBuild(context.Background(), build); err == nil {
		t.Fatal("unrelated provider error was ignored")
	}
}

func TestCloudBuildPollingUsesPersistentDueTimeAndLease(t *testing.T) {
	ctx := withCloudBuildContext(t, &cloudBuildPlatform{provider: "codemagic"})
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "poll-lease", TargetKind: "service", SourceKind: "code", SourceRef: "repo-1",
		Framework: "go", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"cm-app","workflow_id":"api","branch":"main"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuildForEnvBackend(ctx.AppDB(), d.ID, 0, "go", "", "codemagic", d.BuildBackendJSON)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{
		"status": "running", "external_job_id": "submitted-job", "external_next_poll_at": now.Add(time.Minute).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	due, err := dbListPendingCloudBuilds(ctx.AppDB(), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("future build due=%+v err=%v", due, err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{
		"external_next_poll_at": now.Add(-time.Second).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	due, err = dbListPendingCloudBuilds(ctx.AppDB(), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	acquired, err := dbTryAcquireCloudBuildPoll(ctx.AppDB(), build.ID, now.Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339))
	if err != nil || !acquired {
		t.Fatalf("first acquire=%v err=%v", acquired, err)
	}
	acquired, err = dbTryAcquireCloudBuildPoll(ctx.AppDB(), build.ID, now.Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339))
	if err != nil || acquired {
		t.Fatalf("duplicate acquire=%v err=%v", acquired, err)
	}
}
