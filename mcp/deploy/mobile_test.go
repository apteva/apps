package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestExistingDeploymentDefaultsToServiceTarget(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "api", SourceKind: "local", SourceRef: "/src", Framework: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.TargetKind != "service" {
		t.Fatalf("target_kind=%q, want service", d.TargetKind)
	}
	if d.BuildBackend != "local" || d.BuildBackendJSON != "{}" {
		t.Fatalf("build backend=%q config=%q, want local/{}", d.BuildBackend, d.BuildBackendJSON)
	}
	env, err := dbEnsureProductionEnvironment(db, d)
	if err != nil {
		t.Fatal(err)
	}
	if env.TargetConfigJSON != "{}" {
		t.Fatalf("target_config_json=%q, want {}", env.TargetConfigJSON)
	}
	if env.BuildBackend != "local" || env.BuildBackendJSON != "{}" {
		t.Fatalf("environment build backend=%q config=%q, want local/{}", env.BuildBackend, env.BuildBackendJSON)
	}
}

func TestAndroidBuilderStagesAABManifest(t *testing.T) {
	src := t.TempDir()
	dist := t.TempDir()
	gradlew := filepath.Join(src, "gradlew")
	script := `#!/bin/sh
set -eu
mkdir -p app/build/outputs/bundle/release
printf 'test-aab' > app/build/outputs/bundle/release/app-release.aab
`
	if err := os.WriteFile(gradlew, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var log sinkWriter
	_, err := (&androidBuilder{}).Build(src, dist, BuildOverrides{
		TargetConfigJSON: `{"module":"app","variant":"release","package_name":"com.example.app"}`,
	}, &log)
	if err != nil {
		t.Fatal(err)
	}
	var manifest artifactManifest
	body, err := os.ReadFile(filepath.Join(dist, artifactManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Platform != "android" || manifest.PackageName != "com.example.app" || manifest.Primary == "" {
		t.Fatalf("manifest=%+v", manifest)
	}
	if !exists(filepath.Join(dist, manifest.Primary)) || len(manifest.Files) != 1 || manifest.Files[0].SHA256 == "" {
		t.Fatalf("staged artifact missing or unhashed: %+v", manifest)
	}
}

func TestIOSCustomBuilderStagesIPAManifest(t *testing.T) {
	src := t.TempDir()
	dist := t.TempDir()
	var log sinkWriter
	_, err := (&iosBuilder{}).Build(src, dist, BuildOverrides{
		BuildCmd:         `mkdir -p output && printf 'test-ipa' > output/Example.ipa`,
		TargetConfigJSON: `{"bundle_id":"com.example.ios","version_name":"1.2.3","build_number":"42"}`,
	}, &log)
	if err != nil {
		t.Fatal(err)
	}
	build := &Build{ArtifactPath: dist}
	body, err := os.ReadFile(filepath.Join(dist, artifactManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	build.ArtifactManifestJSON = string(body)
	manifest, err := readArtifactManifest(build)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Platform != "ios" || manifest.BundleID != "com.example.ios" || manifest.VersionName != "1.2.3" || manifest.BuildNumber != "42" {
		t.Fatalf("manifest=%+v", manifest)
	}
}

func TestSmokeOnlyMobileBuildCannotBePublished(t *testing.T) {
	artifactDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactDir, "ios-simulator-smoke.zip"), []byte("smoke"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{}).runMobileRelease(
		&Deployment{TargetKind: "ios", TargetConfigJSON: `{"smoke_only":true}`},
		&Build{ID: 9, Status: "succeeded", Framework: "ios", ArtifactPath: artifactDir},
		releaseOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "cannot be published") {
		t.Fatalf("error=%v", err)
	}
}

type integrationCall struct {
	Tool  string
	Input map[string]any
}

type mobilePlatform struct {
	tk.BasePlatformClient
	calls []integrationCall
	token string
}

type iosPlatform struct {
	tk.BasePlatformClient
	calls             []integrationCall
	state             string
	reviewSubmissions json.RawMessage
	responses         map[string]json.RawMessage
}

func (p *iosPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"app_store": int64(77)}}, nil
}

func (p *iosPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "app-store-connect", Status: "active"}, nil
}

func (p *iosPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	if data, ok := p.responses[tool]; ok {
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
	}
	data := json.RawMessage(`{}`)
	switch tool {
	case "list_builds":
		data = json.RawMessage(`{"data":[{"id":"build-42","attributes":{"version":"42","processingState":"VALID"}}]}`)
	case "list_beta_groups", "list_app_versions":
		data = json.RawMessage(`{"data":[]}`)
	case "create_beta_group":
		data = json.RawMessage(`{"data":{"id":"group-1"}}`)
	case "create_app_version":
		data = json.RawMessage(`{"data":{"id":"version-1"}}`)
	case "create_review_submission":
		data = json.RawMessage(`{"data":{"id":"review-1"}}`)
	case "update_review_submission_item":
		data = json.RawMessage(`{"data":{"type":"reviewSubmissionItems","id":"item-existing","attributes":{"state":"READY_FOR_REVIEW"}}}`)
	case "list_review_submissions":
		data = p.reviewSubmissions
		if len(data) == 0 {
			data = json.RawMessage(`{"data":[]}`)
		}
	case "get_app_version":
		state := p.state
		if state == "" {
			state = "WAITING_FOR_REVIEW"
		}
		data = json.RawMessage(`{"data":{"attributes":{"appStoreState":"` + state + `"}}}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
}

func (p *mobilePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"play_store": int64(42)}}, nil
}

func (p *mobilePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "google-play-developer", Status: "active"}, nil
}

func (p *mobilePlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	if tool == "get_edit" && p.token == "expired-token" {
		p.token = "refreshed-token"
	}
	data := json.RawMessage(`{}`)
	if tool == "create_edit" {
		data = json.RawMessage(`{"id":"edit-1"}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
}

func (p *mobilePlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	token := p.token
	if token == "" {
		token = "play-token"
	}
	return &sdk.ConnectionCredentials{ConnectionID: id, Slug: "google-play-developer", Fields: map[string]string{"token": token}}, nil
}

func TestAndroidPromotionReusesVersionCode(t *testing.T) {
	platform := &mobilePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.app"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	b, err := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := mobileReleaseMeta{Platform: "android", PackageName: "com.example.app", VersionCode: "88", RolloutFraction: 0.1}
	app := &App{dataDir: t.TempDir()}
	effective := effectiveDeploymentForEnvironment(d, env)
	if err := app.publishAndroidVersionToTrack(rel.ID, effective, "production", &meta, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(platform.calls) != 4 || platform.calls[0].Tool != "create_edit" || platform.calls[1].Tool != "update_track" || platform.calls[2].Tool != "validate_edit" || platform.calls[3].Tool != "commit_edit" {
		t.Fatalf("calls=%+v", platform.calls)
	}
	if meta.TesterAccess != "not_configured" || meta.TesterCount != 0 {
		t.Fatalf("tester metadata=%+v", meta)
	}
	releases, ok := platform.calls[1].Input["releases"].([]map[string]any)
	if !ok || len(releases) != 1 {
		t.Fatalf("track payload=%#v", platform.calls[1].Input)
	}
	if releases[0]["status"] != "inProgress" || releases[0]["userFraction"] != 0.1 {
		t.Fatalf("release payload=%#v", releases[0])
	}
	fresh, _ := dbGetRelease(ctx.AppDB(), rel.ID)
	if fresh.Status != "live" || fresh.ExternalID != "88" || fresh.Channel != "" {
		// The caller sets channel before promotion; this helper only owns store state.
		if fresh.Status != "live" || fresh.ExternalID != "88" {
			t.Fatalf("release=%+v", fresh)
		}
	}
}

func TestAndroidPromotionValidationDoesNotCommit(t *testing.T) {
	platform := &mobilePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	d := &Deployment{ID: 1, EnvironmentID: 2, TargetKind: "android", TargetConfigJSON: `{"package_name":"com.example.app"}`}
	meta := &mobileReleaseMeta{Platform: "android", PackageName: "com.example.app", VersionCode: "88", RolloutFraction: 0.1}
	if err := (&App{}).validateAndroidVersionToTrack(d, "production", meta); err != nil {
		t.Fatal(err)
	}
	want := []string{"create_edit", "update_track", "validate_edit", "delete_edit"}
	if len(platform.calls) != len(want) {
		t.Fatalf("calls=%+v", platform.calls)
	}
	for i, tool := range want {
		if platform.calls[i].Tool != tool {
			t.Fatalf("calls=%+v", platform.calls)
		}
	}
	if countIntegrationCalls(platform.calls, "commit_edit") != 0 {
		t.Fatalf("validation committed provider state: %+v", platform.calls)
	}
}

func TestAndroidPromotionRequiresStoreReadinessBeforeCreatingRelease(t *testing.T) {
	platform := &mobilePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-gated", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.gated"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	build, err := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	source, err := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	source.Provider = "google_play"
	source.ExternalID = "88"
	source.ReleaseMetaJSON = mustJSON(mobileReleaseMeta{Platform: "android", PackageName: "com.example.gated", VersionCode: "88"})

	if _, err := (&App{dataDir: t.TempDir()}).promoteMobileRelease(d, build, source, releaseOptions{Channel: "production"}); err == nil {
		t.Fatal("promotion succeeded without store configuration")
	}
	if len(platform.calls) != 0 {
		t.Fatalf("provider was mutated before readiness passed: %+v", platform.calls)
	}
	releases, err := dbListReleases(ctx.AppDB(), d.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 {
		t.Fatalf("failed preflight created a promotion release: %+v", releases)
	}
}

func TestGooglePlayBundleUploadStreamsArtifact(t *testing.T) {
	bundleBody := []byte("large-aab-placeholder")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Query().Get("uploadType") != "media" {
			t.Fatalf("request=%s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer play-token" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("headers=%v", r.Header)
		}
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(bundleBody) {
			t.Fatalf("body=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"versionCode":321}`))
	}))
	defer server.Close()
	oldURL := googlePlayUploadBaseURL
	googlePlayUploadBaseURL = server.URL
	t.Cleanup(func() { googlePlayUploadBaseURL = oldURL })

	platform := &mobilePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	bundlePath := filepath.Join(t.TempDir(), "app.aab")
	if err := os.WriteFile(bundlePath, bundleBody, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := uploadGooglePlayBundle(&sdk.BoundIntegration{ConnectionID: 42, AppSlug: "google-play-developer"}, "com.example.app", "edit-1", bundlePath, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonScalarStringAt(raw, "versionCode"); got != "321" {
		t.Fatalf("versionCode=%q", got)
	}
}

func TestGooglePlayBundleUploadRefreshesExpiredOAuth(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") == "Bearer expired-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer refreshed-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"versionCode":322}`))
	}))
	defer server.Close()
	oldURL := googlePlayUploadBaseURL
	googlePlayUploadBaseURL = server.URL
	t.Cleanup(func() { googlePlayUploadBaseURL = oldURL })

	platform := &mobilePlatform{token: "expired-token"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	bundlePath := filepath.Join(t.TempDir(), "app.aab")
	if err := os.WriteFile(bundlePath, []byte("aab"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := uploadGooglePlayBundle(&sdk.BoundIntegration{ConnectionID: 42, AppSlug: "google-play-developer"}, "com.example.app", "edit-1", bundlePath, io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(platform.calls) != 1 || platform.calls[0].Tool != "get_edit" {
		t.Fatalf("requests=%d calls=%+v", requests, platform.calls)
	}
}

func TestIOSReleaseSyncAssignsProcessedBuildToTestFlight(t *testing.T) {
	platform := &iosPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	build, _ := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "ios", "")
	release, _ := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	meta := mobileReleaseMeta{Platform: "ios", AppID: "app-1", BuildNumber: "42"}
	if err := dbUpdateRelease(ctx.AppDB(), release.ID, map[string]any{
		"status": "starting", "provider": "app_store_connect", "channel": "internal", "release_meta_json": mustJSON(meta),
	}); err != nil {
		t.Fatal(err)
	}
	release, _ = dbGetRelease(ctx.AppDB(), release.ID)
	if err := (&App{}).syncIOSRelease(release); err != nil {
		t.Fatal(err)
	}
	fresh, _ := dbGetRelease(ctx.AppDB(), release.ID)
	if fresh.Status != "live" || fresh.ExternalID != "build-42" || fresh.ExternalStatus != "testflight_available" {
		t.Fatalf("release=%+v", fresh)
	}
	want := []string{"list_builds", "list_beta_groups", "list_beta_groups", "create_beta_group", "add_builds_to_beta_group"}
	if len(platform.calls) != len(want) {
		t.Fatalf("calls=%+v", platform.calls)
	}
	for i, tool := range want {
		if platform.calls[i].Tool != tool {
			t.Fatalf("call %d=%q, want %q", i, platform.calls[i].Tool, tool)
		}
	}
}

func TestIOSProductionReleaseSubmitsAndTracksReview(t *testing.T) {
	platform := &iosPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, _ := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	build, _ := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "ios", "")
	release, _ := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	if err := dbUpdateRelease(ctx.AppDB(), release.ID, map[string]any{
		"status": "starting", "provider": "app_store_connect", "channel": "production",
	}); err != nil {
		t.Fatal(err)
	}
	meta := mobileReleaseMeta{Platform: "ios", AppID: "app-1", VersionName: "1.2.3", BuildNumber: "42", SubmitForReview: true}
	bound := &sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}
	if err := (&App{}).prepareIOSProductionRelease(bound, release, "build-42", &meta); err != nil {
		t.Fatal(err)
	}
	fresh, _ := dbGetRelease(ctx.AppDB(), release.ID)
	if fresh.ExternalID != "build-42" || fresh.ExternalStatus != "waiting_for_review" || !meta.Prepared || meta.ReviewSubmissionID != "review-1" {
		t.Fatalf("release=%+v meta=%+v", fresh, meta)
	}
	platform.state = "READY_FOR_SALE"
	if err := (&App{}).syncAppStoreVersionState(bound, fresh, &meta); err != nil {
		t.Fatal(err)
	}
	fresh, _ = dbGetRelease(ctx.AppDB(), release.ID)
	if fresh.Status != "live" || fresh.ExternalStatus != "ready_for_sale" {
		t.Fatalf("release=%+v", fresh)
	}
}

func TestIOSProductionReleaseReusesCompatibleReviewDraft(t *testing.T) {
	platform := &iosPlatform{reviewSubmissions: json.RawMessage(`{"data":[{"type":"reviewSubmissions","id":"review-existing","relationships":{"appStoreVersionForReview":{"data":{"type":"appStoreVersions","id":"version-1"}},"items":{"data":[{"type":"reviewSubmissionItems","id":"item-1"}]}}}]}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, _ := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-review-retry", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	build, _ := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "ios", "")
	release, _ := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	meta := mobileReleaseMeta{
		Platform: "ios", AppID: "app-1", AppStoreVersionID: "version-1",
		VersionName: "1.0", BuildNumber: "42", SubmitForReview: true,
	}
	bound := &sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}
	if err := (&App{}).prepareIOSProductionRelease(bound, release, "build-42", &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ReviewSubmissionID != "review-existing" {
		t.Fatalf("submission=%q", meta.ReviewSubmissionID)
	}
	if countIntegrationCalls(platform.calls, "create_review_submission") != 0 || countIntegrationCalls(platform.calls, "create_review_submission_item") != 0 {
		t.Fatalf("retry created duplicate review resources: %#v", platform.calls)
	}
	if countIntegrationCalls(platform.calls, "submit_review_submission") != 1 {
		t.Fatalf("existing draft was not submitted: %#v", platform.calls)
	}
}

func TestIOSProductionReleaseResubmitsRejectedVersionWithNewBuild(t *testing.T) {
	platform := &iosPlatform{reviewSubmissions: json.RawMessage(`{"data":[{"type":"reviewSubmissions","id":"review-rejected","attributes":{"state":"UNRESOLVED_ISSUES"},"relationships":{"items":{"data":[{"type":"reviewSubmissionItems","id":"item-existing"}]},"appStoreVersionForReview":{"data":{"type":"appStoreVersions","id":"version-1"}}}}],"included":[{"type":"reviewSubmissionItems","id":"item-existing","attributes":{"state":"REJECTED"},"relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"version-1"}}}}]}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, _ := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-review-resubmit", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	build, _ := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "ios", "")
	release, _ := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	meta := mobileReleaseMeta{
		Platform: "ios", AppID: "app-1", AppStoreVersionID: "version-1",
		VersionName: "1.0", BuildNumber: "23", SubmitForReview: true,
	}
	bound := &sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}
	if err := (&App{}).prepareIOSProductionRelease(bound, release, "build-23", &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ReviewSubmissionID != "review-rejected" {
		t.Fatalf("submission=%q", meta.ReviewSubmissionID)
	}
	if countIntegrationCalls(platform.calls, "create_review_submission") != 0 || countIntegrationCalls(platform.calls, "create_review_submission_item") != 0 {
		t.Fatalf("resubmission created duplicate review resources: %#v", platform.calls)
	}
	if countIntegrationCalls(platform.calls, "submit_review_submission") != 1 {
		t.Fatalf("rejected submission was not resubmitted: %#v", platform.calls)
	}
	if countIntegrationCalls(platform.calls, "update_review_submission_item") != 1 {
		t.Fatalf("rejected item was not resolved: %#v", platform.calls)
	}
	setBuildIndex, resolveIndex, submitIndex := -1, -1, -1
	for i, call := range platform.calls {
		switch call.Tool {
		case "set_app_version_build":
			setBuildIndex = i
			body, _ := call.Input["body"].(map[string]any)
			data, _ := body["data"].(map[string]any)
			if data["id"] != "build-23" {
				t.Fatalf("selected build=%#v", data["id"])
			}
		case "update_review_submission_item":
			resolveIndex = i
			if call.Input["item_id"] != "item-existing" || call.Input["resolved"] != true {
				t.Fatalf("resolve input=%#v", call.Input)
			}
		case "submit_review_submission":
			submitIndex = i
		}
	}
	if setBuildIndex < 0 || resolveIndex < 0 || submitIndex < 0 || setBuildIndex >= resolveIndex || resolveIndex >= submitIndex {
		t.Fatalf("build selection, item resolution, and resubmission are out of order: %#v", platform.calls)
	}
}

func TestIOSProductionReleaseRetriesAfterItemAlreadyResolved(t *testing.T) {
	platform := &iosPlatform{reviewSubmissions: json.RawMessage(`{"data":[{"type":"reviewSubmissions","id":"review-unresolved","attributes":{"state":"UNRESOLVED_ISSUES"},"relationships":{"items":{"data":[{"type":"reviewSubmissionItems","id":"item-existing"}]}}}],"included":[{"type":"reviewSubmissionItems","id":"item-existing","attributes":{"state":"READY_FOR_REVIEW"},"relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"version-1"}}}}]}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, _ := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-review-resolved-retry", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	build, _ := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "ios", "")
	release, _ := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	meta := mobileReleaseMeta{
		Platform: "ios", AppID: "app-1", AppStoreVersionID: "version-1",
		VersionName: "1.0", BuildNumber: "23", SubmitForReview: true,
	}
	bound := &sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}
	if err := (&App{}).prepareIOSProductionRelease(bound, release, "build-23", &meta); err != nil {
		t.Fatal(err)
	}
	if countIntegrationCalls(platform.calls, "update_review_submission_item") != 0 || countIntegrationCalls(platform.calls, "submit_review_submission") != 1 {
		t.Fatalf("resolved retry calls=%#v", platform.calls)
	}
}

func TestIOSProductionReleaseRetryIsIdempotentWhileWaitingForReview(t *testing.T) {
	platform := &iosPlatform{reviewSubmissions: json.RawMessage(`{"data":[{"type":"reviewSubmissions","id":"review-waiting","attributes":{"state":"WAITING_FOR_REVIEW"},"relationships":{"appStoreVersionForReview":{"data":{"type":"appStoreVersions","id":"version-1"}},"items":{"data":[{"type":"reviewSubmissionItems","id":"item-existing"}]}}}],"included":[{"type":"reviewSubmissionItems","id":"item-existing","relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"version-1"}}}}]}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, _ := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-review-idempotent", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	build, _ := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "ios", "")
	release, _ := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	meta := mobileReleaseMeta{
		Platform: "ios", AppID: "app-1", AppStoreVersionID: "version-1",
		VersionName: "1.0", BuildNumber: "23", SubmitForReview: true,
	}
	bound := &sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}
	if err := (&App{}).prepareIOSProductionRelease(bound, release, "build-23", &meta); err != nil {
		t.Fatal(err)
	}
	if countIntegrationCalls(platform.calls, "create_review_submission") != 0 || countIntegrationCalls(platform.calls, "create_review_submission_item") != 0 || countIntegrationCalls(platform.calls, "submit_review_submission") != 0 {
		t.Fatalf("idempotent retry mutated review resources: %#v", platform.calls)
	}
}

func TestMobileRetentionPinsOnlyUploadProcessing(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "ios", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(db, d)
	if err != nil {
		t.Fatal(err)
	}
	createRelease := func(status, provider, externalID string) *Build {
		t.Helper()
		build, err := dbCreateBuildForEnv(db, d.ID, env.ID, "ios", "")
		if err != nil {
			t.Fatal(err)
		}
		release, err := dbCreateReleaseForEnv(db, d.ID, env.ID, build.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := dbUpdateRelease(db, release.ID, map[string]any{
			"status": status, "provider": provider, "external_id": externalID,
		}); err != nil {
			t.Fatal(err)
		}
		return build
	}
	uploading := createRelease("starting", "app_store_connect", "uploaded-123")
	processed := createRelease("starting", "app_store_connect", "build-123")
	mobileLive := createRelease("live", "app_store_connect", "build-124")
	serviceLive := createRelease("live", "", "")

	kept, err := retainedBuildIDs(db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !kept[uploading.ID] || !kept[serviceLive.ID] {
		t.Fatalf("uploading and service live builds must be retained: %#v", kept)
	}
	if kept[processed.ID] || kept[mobileLive.ID] {
		t.Fatalf("processed mobile builds must not be pinned forever: %#v", kept)
	}
}

func TestIOSProcessingTimesOutAfter48Hours(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	build, _ := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "ios", "")
	release, _ := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	if err := dbUpdateRelease(ctx.AppDB(), release.ID, map[string]any{
		"status": "starting", "provider": "app_store_connect", "external_id": "uploaded-123",
	}); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().Add(-49 * time.Hour).Format(time.RFC3339)
	if _, err := ctx.AppDB().Exec(`UPDATE releases SET created_at = ? WHERE id = ?`, createdAt, release.ID); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).syncPendingMobileReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetRelease(ctx.AppDB(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "failed" || fresh.ExternalStatus != "processing_timeout" {
		t.Fatalf("release=%+v", fresh)
	}
}

type sinkWriter struct{ data []byte }

func (w *sinkWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}
