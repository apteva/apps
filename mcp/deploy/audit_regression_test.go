package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func auditApp(t *testing.T) (*App, *sdk.AppCtx, *Deployment, *DeploymentEnvironment, *bootRecoveryRuntime) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	setBootRecoveryGlobalCtx(t, ctx)
	rt := &bootRecoveryRuntime{}
	a := &App{dataDir: t.TempDir(), runtime: rt, registry: NewSupervisorRegistry(), portRangeStart: availableTestPort(t), autoRestartState: map[int64]autoRestartInfo{}, retainRollbacks: 3}
	a.portRangeEnd = a.portRangeStart
	d, e := createBootRecoveryDeployment(t, ctx)
	return a, ctx, d, e, rt
}
func TestAuditProjectScopeLogs(t *testing.T) {
	a, ctx, _, _, _ := auditApp(t)
	d, err := dbCreateDeployment(ctx.AppDB(), "other-project", CreateDeploymentInput{Name: "private", SourceKind: "local", SourceRef: "/src"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := dbCreateBuild(ctx.AppDB(), d.ID, "static", "")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "build.log")
	os.WriteFile(log, []byte("other-project-private-log"), 0600)
	dbUpdateBuild(ctx.AppDB(), b.ID, map[string]any{"log_path": log})
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/builds/%d/log?project_id=p1", b.ID), nil)
	w := httptest.NewRecorder()
	a.handleBuildItem(w, req)
	if w.Code == 200 && bytes.Contains(w.Body.Bytes(), []byte("other-project-private-log")) {
		t.Fatal("p1 can read other-project build logs by ID")
	}
}
func TestAuditRestartUsesReleasedBuild(t *testing.T) {
	a, ctx, d, e, rt := auditApp(t)
	released := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "released")
	newer := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "not-approved")
	rel := createBootRecoveryRelease(t, ctx, d.ID, e.ID, released.ID, a.portRangeStart)
	dbUpdateRelease(ctx.AppDB(), rel.ID, map[string]any{"status": "crashed"})
	dbReleasePortLease(ctx.AppDB(), rel.Port)
	a.autoRestart(d.ID, e.ID, "audit")
	time.Sleep(30 * time.Millisecond)
	starts := rt.started()
	if len(starts) != 1 {
		t.Fatalf("starts=%d", len(starts))
	}
	if starts[0].ArtifactDir == newer.ArtifactPath {
		t.Fatal("crash recovery deployed a newer build that had never been released")
	}
}
func TestAuditStopCancelsRestartIntent(t *testing.T) {
	a, ctx, d, e, rt := auditApp(t)
	b := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "released")
	dbUpdateEnvironment(ctx.AppDB(), e.ID, map[string]any{"domain": "example.test", "current_release_id": nil})
	a.autoRestart(d.ID, e.ID, "queued-before-operator-stop")
	time.Sleep(30 * time.Millisecond)
	if len(rt.started()) > 0 {
		t.Fatalf("restart started build %d even with operator-cleared current release", b.ID)
	}
}
func TestAuditCanStopStartingRelease(t *testing.T) {
	a, ctx, d, e, _ := auditApp(t)
	b := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "starting")
	rel, err := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, e.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.toolStop(ctx, map[string]any{"id": float64(d.ID), "_project_id": "p1"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetRelease(ctx.AppDB(), rel.ID)
	if got.Status == "starting" {
		t.Fatal("stop left an unpromoted starting release untouched")
	}
}
func TestAuditPendingCloudPollEligibility(t *testing.T) {
	_, ctx, d, e, _ := auditApp(t)
	b, err := dbCreateBuildForEnvBackend(ctx.AppDB(), d.ID, e.ID, "static", "", "codemagic", `{"app_id":"app","workflow_id":"w","branch":"main"}`)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := dbListPendingCloudBuilds(ctx.AppDB(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.ID == b.ID && p.ExternalJobID == "" {
			t.Fatal("cloud poller selects a build before provider submission has produced a job ID")
		}
	}
}
func TestAuditBlankBuilderKeepsOutput(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	_, err := (&blankBuilder{}).Build(src, dst, BuildOverrides{BuildCmd: "mkdir -p dist; echo ready > dist/app"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dst, "dist", "app")); err != nil {
		t.Fatal("successful blank build discarded dist/app from artifact")
	}
}
func TestAuditNodeArtifactWithSymlinkDownloads(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "node_modules", ".bin"), 0755)
	os.WriteFile(filepath.Join(root, "node_modules", "cli.js"), []byte("cli"), 0644)
	os.Symlink("../cli.js", filepath.Join(root, "node_modules", ".bin", "cli"))
	var out bytes.Buffer
	if err := streamArtifactDirectoryZip(&out, root); err != nil {
		t.Fatalf("normal Node package symlink makes artifact download fail: %v", err)
	}
}
func TestAuditMacReadinessRequiresListener(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux behavior")
	}
	if pidOwnsPort(99999999, availableTestPort(t)) {
		t.Fatal("nonexistent PID with unbound port is reported ready")
	}
}
func TestAuditBuildDoesNotInheritPlatformToken(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "audit-synthetic-token")
	for _, entry := range buildEnv(nil) {
		if entry == "APTEVA_APP_TOKEN=audit-synthetic-token" {
			t.Fatal("build subprocess receives the sidecar platform token")
		}
	}
}

type auditGitHubPlatform struct{ tk.BasePlatformClient }

func (*auditGitHubPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	var data string
	switch tool {
	case "list_workflow_runs":
		data = `{"workflow_runs":[{"id":22,"created_at":"2026-09-05T12:00:03Z","head_branch":"main"},{"id":11,"created_at":"2026-09-05T12:00:00Z","head_branch":"main"}]}`
	default:
		return nil, errors.New("unexpected tool " + tool)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(data)}, nil
}
func TestAuditGitHubRunDiscoveryCollision(t *testing.T) {
	withCloudBuildContext(t, &auditGitHubPlatform{})
	cfg := cloudBuildConfig{Owner: "org", Repo: "repo", WorkflowID: "w", Ref: "main"}
	first, _, err := discoverGitHubRun(&sdk.BoundIntegration{ConnectionID: 1}, cfg, "2026-09-05T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := discoverGitHubRun(&sdk.BoundIntegration{ConnectionID: 1}, cfg, "2026-09-05T12:00:03Z")
	if err != nil {
		t.Fatal(err)
	}
	if first != "" && first == second {
		t.Fatalf("two independent submissions both attach to workflow run %s", first)
	}
}

func TestAuditAndroidRotationFailurePreservesKey(t *testing.T) {
	platform := &mobileSigningPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	setBootRecoveryGlobalCtx(t, ctx)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{Name: "android-app", TargetKind: "android", SourceKind: "local", SourceRef: t.TempDir(), Framework: "android", BuildBackend: "codemagic", BuildBackendJSON: `{"app_id":"runner-app","workflow_id":"android","branch":"main","source_mode":"bundle"}`, TargetConfigJSON: `{"package_name":"com.example.android"}`})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{dataDir: t.TempDir()}
	first, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	platform.failTool = "update_group_variable"
	_, err = app.setupMobileSigning(t.Context(), d, "", true)
	if err == nil {
		t.Fatal("expected synthetic provider failure")
	}
	after, err := dbGetMobileSigningIdentity(ctx.AppDB(), "p1", "android", "", "com.example.android")
	if err != nil {
		t.Fatal(err)
	}
	if after.CertificateSHA256 != first.Identity.CertificateSHA256 {
		t.Fatal("failed provider rotation overwrote the previously working Android private key")
	}
}

type auditApplePlatform struct {
	tk.BasePlatformClient
	calls          []string
	failSubmission bool
	modernState    bool
}

func (p *auditApplePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"app_store": int64(71)}}, nil
}
func (p *auditApplePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "app-store-connect", Status: "active"}, nil
}
func (p *auditApplePlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, tool)
	if tool == "list_review_submissions" && p.failSubmission {
		return nil, errors.New("synthetic temporary network failure")
	}
	data := `{"data":[]}`
	if tool == "create_review_submission" {
		data = `{"data":{"id":"submission1"}}`
	}
	if tool == "get_app_version" {
		data = `{"data":{"id":"v1","attributes":{"appStoreState":"PREPARE_FOR_SUBMISSION"}}}`
		if p.modernState {
			data = `{"data":{"id":"v1","attributes":{"appVersionState":"READY_FOR_DISTRIBUTION"}}}`
		}
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(data)}, nil
}
func TestAuditAppleSubmissionRetriesAfterTransientFailure(t *testing.T) {
	p := &auditApplePlatform{failSubmission: true}
	ctx := withCloudBuildContext(t, p)
	a := &App{}
	d, _ := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{Name: "ios", SourceKind: "local", SourceRef: "/src", TargetKind: "ios", Framework: "ios"})
	b, _ := dbCreateBuild(ctx.AppDB(), d.ID, "ios", "")
	rel, _ := dbCreateRelease(ctx.AppDB(), d.ID, b.ID)
	meta := mobileReleaseMeta{AppID: "app", AppStoreVersionID: "v1", VersionName: "1.0", BuildNumber: "1", SubmitForReview: true}
	err := a.prepareIOSProductionRelease(&sdk.BoundIntegration{ConnectionID: 71}, rel, "build1", &meta)
	if err == nil {
		t.Fatal("expected temporary failure")
	}
	p.failSubmission = false
	p.calls = nil
	fresh, _ := dbGetRelease(ctx.AppDB(), rel.ID)
	if err := a.syncIOSRelease(fresh); err != nil {
		t.Fatal(err)
	}
	for _, tool := range p.calls {
		if tool == "submit_review_submission" {
			return
		}
	}
	t.Fatal("retry only observes version state; it never resumes the requested review submission")
}
func TestAuditAppleModernLiveState(t *testing.T) {
	p := &auditApplePlatform{modernState: true}
	ctx := withCloudBuildContext(t, p)
	a := &App{}
	d, _ := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{Name: "ios", SourceKind: "local", SourceRef: "/src", TargetKind: "ios", Framework: "ios"})
	b, _ := dbCreateBuild(ctx.AppDB(), d.ID, "ios", "")
	rel, _ := dbCreateRelease(ctx.AppDB(), d.ID, b.ID)
	meta := mobileReleaseMeta{AppID: "app", AppStoreVersionID: "v1", VersionName: "1.0", BuildNumber: "1", Prepared: true}
	if err := a.syncAppStoreVersionState(&sdk.BoundIntegration{ConnectionID: 71}, rel, &meta); err != nil {
		t.Fatal(err)
	}
	fresh, _ := dbGetRelease(ctx.AppDB(), rel.ID)
	if fresh.Status != "live" {
		t.Fatalf("Apple READY_FOR_DISTRIBUTION leaves release status=%s", fresh.Status)
	}
}

type auditFastRuntime struct {
	bootRecoveryRuntime
	app *App
}

func (r *auditFastRuntime) Start(spec ReleaseSpec) (*RunningRelease, error) {
	rr, err := r.bootRecoveryRuntime.Start(spec)
	if err != nil {
		return nil, err
	}
	r.app.promoteToLive(spec.ReleaseID, rr.PID, spec.Port)
	return rr, nil
}
func TestAuditFastReadinessNotOverwritten(t *testing.T) {
	a, ctx, d, e, _ := auditApp(t)
	a.runtime = &auditFastRuntime{app: a}
	b := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "fast")
	rel, err := a.runServiceRelease(effectiveDeploymentForEnvironment(d, e), b)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if rel.Status != "live" {
		t.Fatalf("successful readiness callback was overwritten with %s by startup", rel.Status)
	}
}
func TestAuditRetentionProtectsRequestedRelease(t *testing.T) {
	a, ctx, d, e, _ := auditApp(t)
	a.retainRollbacks = 0
	b := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "release-next")
	dbUpdateBuild(ctx.AppDB(), b.ID, map[string]any{"release_requested": true})
	if _, err := a.pruneBuildArtifacts(ctx.AppDB(), "audit"); err != nil {
		t.Fatal(err)
	}
	if !buildArtifactAvailable(b) {
		t.Fatal("retention removed successful artifact while its automatic release was still requested")
	}
}
func TestAuditCloudCancelledBuildCannotBecomeSucceeded(t *testing.T) {
	p := &cloudBuildPlatform{provider: "codemagic"}
	ctx := withCloudBuildContext(t, p)
	a := &App{dataDir: t.TempDir(), retainRollbacks: 3}
	d, _ := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{Name: "cloud", SourceKind: "local", SourceRef: "/src", Framework: "static", BuildBackend: "codemagic", BuildBackendJSON: `{"app_id":"app","workflow_id":"w","branch":"main","artifact_mode":"none"}`})
	b, _ := dbCreateBuildForEnvBackend(ctx.AppDB(), d.ID, 0, "static", "", "codemagic", d.BuildBackendJSON)
	dbUpdateBuild(ctx.AppDB(), b.ID, map[string]any{"status": "cancelled", "external_status": "cancelled"})
	cfg, err := parseCloudBuildConfig("codemagic", d.BuildBackendJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.finalizeCloudBuild(context.Background(), codemagicBuildBackend{}, &sdk.BoundIntegration{ConnectionID: 77}, cfg, b, &externalBuildStatus{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	fresh, _ := dbGetBuild(ctx.AppDB(), b.ID)
	if fresh.Status != "cancelled" {
		t.Fatalf("in-flight poll completion changed a cancelled build to %s", fresh.Status)
	}
}
