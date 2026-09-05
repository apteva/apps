package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAllRecordRoutesRejectOtherProjects(t *testing.T) {
	a, ctx, _, _, _ := auditApp(t)
	d, err := dbCreateDeployment(ctx.AppDB(), "other", CreateDeploymentInput{Name: "private", SourceKind: "local", SourceRef: "/src"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := dbCreateBuild(ctx.AppDB(), d.ID, "static", "")
	r, _ := dbCreateRelease(ctx.AppDB(), d.ID, b.ID)
	for _, path := range []string{fmt.Sprintf("builds/%d/log", b.ID), fmt.Sprintf("builds/%d/cancel", b.ID), fmt.Sprintf("builds/%d/artifact", b.ID), fmt.Sprintf("releases/%d", r.ID), fmt.Sprintf("releases/%d/log", r.ID), fmt.Sprintf("releases/%d/sync", r.ID), fmt.Sprintf("releases/%d/rollout", r.ID), fmt.Sprintf("releases/%d/halt", r.ID), fmt.Sprintf("releases/%d/release-approved", r.ID)} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/"+path+"?project_id=p1", strings.NewReader(`{"fraction":1}`))
			w := httptest.NewRecorder()
			if strings.HasPrefix(path, "builds/") {
				a.handleBuildItem(w, request)
			} else {
				a.handleReleaseItem(w, request)
			}
			if w.Code != 404 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	for _, call := range []func() error{func() error {
		_, e := a.toolLogs(ctx, map[string]any{"_project_id": "p1", "release_id": float64(r.ID)})
		return e
	}, func() error {
		_, e := a.toolHalt(ctx, map[string]any{"_project_id": "p1", "release_id": float64(r.ID)})
		return e
	}, func() error {
		_, e := a.toolReleaseSync(ctx, map[string]any{"_project_id": "p1", "release_id": float64(r.ID)})
		return e
	}} {
		if call() == nil {
			t.Fatal("cross-project MCP call succeeded")
		}
	}
}
func TestRuntimeEnvReservesPortAndPlatformNamespace(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "synthetic-platform-secret")
	t.Setenv("UNRELATED_SECRET", "synthetic-host-secret")
	env := strings.Join(mergeEnv(map[string]string{"PORT": "22", "APTEVA_APP_TOKEN": "injected", "APP_SECRET": "explicit"}, 7101), "\n")
	if strings.Contains(env, "synthetic") || strings.Contains(env, "APTEVA_APP_TOKEN") || !strings.Contains(env, "PORT=7101") || !strings.Contains(env, "APP_SECRET=explicit") {
		t.Fatal(env)
	}
}
func TestOwnedShutdownHonorsGraceAndRefusesChangedIdentity(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "stopped")
	cmd := exec.Command("sh", "-c", `trap 'echo stopped > "$1"; exit 0' TERM; echo ready; while :; do sleep 1; done`, "sh", marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	ready := make([]byte, 6)
	if _, err = io.ReadFull(stdout, ready); err != nil {
		t.Fatal(err)
	}
	identity := processIdentity(cmd.Process.Pid)
	if identity == "" {
		t.Fatal("missing process identity")
	}
	if err = terminateOwnedGroup(cmd.Process.Pid, "wrong-identity", time.Second); err == nil {
		t.Fatal("changed identity accepted")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err = terminateOwnedGroup(cmd.Process.Pid, identity, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("process did not exit")
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("TERM hook did not run", err)
	}
}
func TestAutomaticReleaseCreationIsAtomicAndIdempotent(t *testing.T) {
	a, ctx, d, e, _ := auditApp(t)
	b := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "auto")
	if err := dbUpdateBuild(ctx.AppDB(), b.ID, map[string]any{"release_requested": true}); err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, e)
	d.AutomaticRelease = true
	first, created, err := a.createOperationRelease(d, b)
	if err != nil || !created {
		t.Fatalf("%v %v", created, err)
	}
	second, created, err := a.createOperationRelease(d, b)
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("duplicate dispatch: %v %v", created, err)
	}
	fresh, _ := dbGetBuild(ctx.AppDB(), b.ID)
	if fresh.ReleaseRequested {
		t.Fatal("release intent was not acknowledged atomically")
	}
}
func TestBoundedArchiveRejectsExpansionDuplicateAndEscapingLinks(t *testing.T) {
	for _, scenario := range []string{"expansion", "duplicate", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			var buffer bytes.Buffer
			writer := zip.NewWriter(&buffer)
			entry, _ := writer.Create("data")
			entry.Write([]byte("hello"))
			switch scenario {
			case "duplicate":
				entry, _ = writer.Create("data")
				entry.Write([]byte("again"))
			case "symlink":
				header := &zip.FileHeader{Name: "escape"}
				header.SetMode(os.ModeSymlink | 0777)
				entry, _ = writer.CreateHeader(header)
				entry.Write([]byte("../outside"))
			}
			writer.Close()
			reader, _ := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
			budget := int64(1024)
			if scenario == "expansion" {
				budget = 2
			}
			if err := extractBoundedZip(reader, t.TempDir(), budget, true); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
func TestNodeArtifactSymlinkRoundTrip(t *testing.T) {
	source := t.TempDir()
	os.MkdirAll(filepath.Join(source, "node_modules/.bin"), 0755)
	os.WriteFile(filepath.Join(source, "node_modules/cli.js"), []byte("cli"), 0755)
	os.Symlink("../cli.js", filepath.Join(source, "node_modules/.bin/cli"))
	var buffer bytes.Buffer
	if err := streamArtifactDirectoryZip(&buffer, source); err != nil {
		t.Fatal(err)
	}
	reader, _ := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	dest := t.TempDir()
	if err := extractBoundedZip(reader, dest, 1024, true); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "node_modules/.bin/cli"))
	if err != nil || string(body) != "cli" {
		t.Fatalf("%s %v", body, err)
	}
}
func TestMobileArtifactIdentityRejectsWrongVersions(t *testing.T) {
	for _, platform := range []string{"android", "ios"} {
		t.Run(platform, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.zip")
			writeMobileFixture(t, path, platform, "com.example.app", "2.0", "42")
			cfg := mobileTargetConfig{BundleID: "com.example.app", PackageName: "com.example.app", VersionName: "1.0", BuildNumber: "42", VersionCode: "42"}
			if _, err := verifyMobileBinaryIdentity(path, platform, cfg); err == nil {
				t.Fatal("wrong version accepted")
			}
			cfg.VersionName = "2.0"
			if _, err := verifyMobileBinaryIdentity(path, platform, cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSourceCopySkipsEntireDependencyTrees(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(source, "node_modules/nested"), 0755)
	os.WriteFile(filepath.Join(source, "node_modules/nested/file"), []byte("large dependency"), 0644)
	os.WriteFile(filepath.Join(source, "source.txt"), []byte("source"), 0644)
	if err := copyTree(source, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("excluded tree was traversed/copied")
	}
}
func TestLogTailAndRotationStayBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Truncate(maxLogBytes + 10)
	writer := newBoundedLogWriter(f)
	if _, err = writer.Write([]byte("new\nlast\n")); err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	if info.Size() > maxLogBytes {
		t.Fatal("log grew past limit")
	}
	prior, err := os.Stat(path + ".1")
	if err != nil || prior.Size() > maxLogBytes {
		t.Fatalf("backup %v %v", prior, err)
	}
	tail, err := tailFile(path, 1)
	if err != nil || !strings.Contains(tail, "last") {
		t.Fatalf("tail %q %v", tail, err)
	}
}

type flakyIngress struct {
	deployIngressPlatform
	fail bool
}

func (p *flakyIngress) ExposeIngress(r sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	if p.fail {
		return nil, errors.New("temporary ingress outage")
	}
	return p.deployIngressPlatform.ExposeIngress(r)
}
func TestIngressFailureSurvivesRestartAndRetries(t *testing.T) {
	platform := &flakyIngress{fail: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	setBootRecoveryGlobalCtx(t, ctx)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	d := seedLiveDeploymentForIngress(t, ctx, "api", "api.example.test", listener.Addr().(*net.TCPAddr).Port, os.Getpid())
	a := &App{}
	if err = registerRouteForDeployment(ctx, a, d); err == nil {
		t.Fatal("ingress failure hidden")
	}
	var pending int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ingress_work WHERE applied_at=''`).Scan(&pending)
	if pending != 1 {
		t.Fatal("missing durable ingress work")
	}
	platform.fail = false
	a = &App{}
	if err = a.reconcileIngress(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ingress_work WHERE applied_at=''`).Scan(&pending)
	if pending != 0 || len(platform.exposed) != 1 {
		t.Fatal("ingress work was not applied")
	}
}
func TestLocalBuildCancellationStopsHungCommand(t *testing.T) {
	a, ctx, d, e, _ := auditApp(t)
	a.buildSem = make(chan struct{}, 1)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "input"), []byte("source"), 0644)
	d = effectiveDeploymentForEnvironment(d, e)
	d.SourceKind = "local"
	d.SourceRef = src
	d.Framework = "blank"
	d.BuildCmd = "sleep 30"
	b, err := dbCreateBuildForEnv(ctx.AppDB(), d.ID, e.ID, "blank", d.BuildCmd)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = a.runLocalBuildRecord(d, b) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.localBuildMu.Lock()
		cancel := a.localBuilds[b.ID]
		a.localBuildMu.Unlock()
		if cancel != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = a.cancelLocalBuild(b); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("cancelled build still running")
	}
	fresh, _ := dbGetBuild(ctx.AppDB(), b.ID)
	if fresh.Status != "cancelled" {
		t.Fatalf("status=%s", fresh.Status)
	}
}

func TestFailedReplacementPreservesLiveServiceAndIntent(t *testing.T) {
	a, ctx, d, e, rt := auditApp(t)
	oldBuild := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "old")
	old := createBootRecoveryRelease(t, ctx, d.ID, e.ID, oldBuild.ID, a.portRangeStart)
	candidate := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "candidate")
	a.portRangeEnd = a.portRangeStart + 20
	rt.startErr = errors.New("synthetic failed start")
	effective := effectiveDeploymentForEnvironment(d, e)
	_, err := a.runServiceRelease(effective, candidate)
	if err == nil {
		t.Fatal("expected failed start")
	}
	fresh, _ := dbGetRelease(ctx.AppDB(), old.ID)
	if fresh.Status != "live" {
		t.Fatalf("healthy old release was stopped: %+v", fresh)
	}
	var intended int64
	if err := ctx.AppDB().QueryRow(`SELECT release_id FROM deployment_intents WHERE deployment_id=? AND environment_id=?`, d.ID, e.ID).Scan(&intended); err != nil || intended != old.ID {
		t.Fatalf("rollback intent=%d err=%v", intended, err)
	}
}
func TestSupersededCandidateCannotPromoteOrRemainRunning(t *testing.T) {
	a, ctx, d, e, _ := auditApp(t)
	b := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "candidate")
	first, err := a.runServiceRelease(effectiveDeploymentForEnvironment(d, e), b)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.runServiceRelease(effectiveDeploymentForEnvironment(d, e), b)
	if err != nil {
		t.Fatal(err)
	}
	a.promoteToLive(first.ID, first.PID, first.Port)
	old, _ := dbGetRelease(ctx.AppDB(), first.ID)
	if old.Status != "stopped" {
		t.Fatalf("superseded candidate status=%s", old.Status)
	}
	if a.registry.Get(first.ID) != nil {
		t.Fatal("superseded runtime retained")
	}
	var intended int64
	ctx.AppDB().QueryRow(`SELECT release_id FROM deployment_intents WHERE deployment_id=? AND environment_id=?`, d.ID, e.ID).Scan(&intended)
	if intended != second.ID {
		t.Fatal("old callback overwrote new intent")
	}
}
func TestLegacyProcessIdentityIsBoundToLaunchTime(t *testing.T) {
	_, ctx, d, e, _ := auditApp(t)
	b, _ := dbCreateBuildForEnv(ctx.AppDB(), d.ID, e.ID, "blank", "")
	r, _ := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, e.ID, b.ID)
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	dbUpdateRelease(ctx.AppDB(), r.ID, map[string]any{"pid": cmd.Process.Pid, "started_at": "2000-01-01T00:00:00Z"})
	if _, err := verifiedReleaseProcessIdentity(r.ID, cmd.Process.Pid); err == nil {
		t.Fatal("unrelated process accepted")
	}
	dbUpdateRelease(ctx.AppDB(), r.ID, map[string]any{"started_at": nowUTC()})
	got, err := verifiedReleaseProcessIdentity(r.ID, cmd.Process.Pid)
	if err != nil || got == "" {
		t.Fatalf("legacy adoption: %s %v", got, err)
	}
	ctx.AppDB().Exec(`UPDATE release_runtime SET process_identity='different-generation' WHERE release_id=?`, r.ID)
	if _, err := verifiedReleaseProcessIdentity(r.ID, cmd.Process.Pid); err == nil {
		t.Fatal("changed process generation accepted")
	}
}
func TestMobileProcessingFailuresAreVisible(t *testing.T) {
	for _, state := range []string{"INVALID", "FAILED"} {
		id, got, err := appleProcessingBuild([]byte(fmt.Sprintf(`{"data":[{"id":"binary","attributes":{"version":"42","processingState":%q}}]}`, state)), "42")
		if err == nil || id != "binary" || got != state {
			t.Fatalf("%s: %s %s %v", state, id, got, err)
		}
	}
}
func TestArchivedSigningRevisionRemainsDecryptable(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	a := &App{dataDir: t.TempDir()}
	input := mobileSigningIdentityInput{ProjectID: "p1", Platform: "android", ApplicationIdentifier: "com.example", Format: "pkcs12"}
	first, err := a.createMobileSigningIdentity(db, input, mobileSigningSecretPayload{StorePassword: "old-key"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.replaceMobileSigningIdentity(db, first, input, mobileSigningSecretPayload{StorePassword: "new-key"})
	if err != nil || second.Revision != 2 {
		t.Fatalf("%+v %v", second, err)
	}
	var encrypted []byte
	if err := db.QueryRow(`SELECT encrypted_payload FROM signing_identity_revisions WHERE identity_id=? AND revision=1`, first.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("old-key")) {
		t.Fatal("archived plaintext secret")
	}
	first.EncryptedPayload = encrypted
	payload, err := (&App{dataDir: a.dataDir}).decryptMobileSigningPayload(first)
	if err != nil || payload.StorePassword != "old-key" {
		t.Fatalf("archived key cannot be recovered: %v", err)
	}
}

func TestHealthyReleaseCrashDoesNotRollBackToPreviousBuild(t *testing.T) {
	a, ctx, d, e, _ := auditApp(t)
	oldBuild := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "old")
	old := createBootRecoveryRelease(t, ctx, d.ID, e.ID, oldBuild.ID, a.portRangeStart)
	newBuild := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "new")
	rel, _, err := a.createOperationRelease(effectiveDeploymentForEnvironment(d, e), newBuild)
	if err != nil {
		t.Fatal(err)
	}
	dbUpdateRelease(ctx.AppDB(), rel.ID, map[string]any{"status": "crashed", "last_health_at": nowUTC()})
	rel, _ = dbGetRelease(ctx.AppDB(), rel.ID)
	a.restorePreviousIntent(rel)
	var intended int64
	ctx.AppDB().QueryRow(`SELECT release_id FROM deployment_intents WHERE deployment_id=? AND environment_id=?`, d.ID, e.ID).Scan(&intended)
	if intended != rel.ID {
		t.Fatalf("healthy release %d crash rolled back to %d (previous %d)", rel.ID, intended, old.ID)
	}
}

type correlatedGitHubPlatform struct{ tk.BasePlatformClient }

func (*correlatedGitHubPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	body := `{}`
	switch tool {
	case "list_workflow_runs":
		body = `{"workflow_runs":[{"id":99,"display_title":"manual run"},{"id":11,"display_title":"apteva-deploy-42-token"}]}`
	case "get_workflow_run":
		if fmt.Sprint(input["run_id"]) != "11" {
			return nil, errors.New("wrong correlated run")
		}
		body = `{"id":11,"status":"completed","conclusion":"success"}`
	case "list_workflow_run_artifacts":
		if fmt.Sprint(input["run_id"]) != "11" {
			return nil, errors.New("artifact used stale discovery ID")
		}
		body = `{"artifacts":[{"id":123,"name":"output","expired":false}]}`
	default:
		return nil, fmt.Errorf("unexpected %s", tool)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: []byte(body)}, nil
}
func TestGitHubFirstPollCompletionUsesCorrelatedIDForArtifact(t *testing.T) {
	withCloudBuildContext(t, &correlatedGitHubPlatform{})
	b := &Build{ID: 42, ExternalJobID: "discover:apteva-deploy-42-token"}
	cfg := cloudBuildConfig{Owner: "org", Repo: "repo", WorkflowID: "build.yml", ArtifactName: "output"}
	backend := githubActionsBuildBackend{}
	status, err := backend.Inspect(t.Context(), &sdk.BoundIntegration{ConnectionID: 1}, cfg, b)
	if err != nil || b.ExternalJobID != "11" || status.Status != "succeeded" {
		t.Fatalf("%+v %+v %v", b, status, err)
	}
	if _, err = backend.Artifact(t.Context(), &sdk.BoundIntegration{ConnectionID: 1}, cfg, b, status); err != nil {
		t.Fatal(err)
	}
}
func TestAutomaticMobileReleaseResumesSamePendingRecord(t *testing.T) {
	a, ctx, d, e, _ := auditApp(t)
	b := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, e.ID, "mobile")
	dbUpdateBuild(ctx.AppDB(), b.ID, map[string]any{"release_requested": true})
	effective := effectiveDeploymentForEnvironment(d, e)
	effective.TargetKind = "ios"
	effective.AutomaticRelease = true
	first, created, err := a.createOperationRelease(effective, b)
	if err != nil || !created || first.Provider != "pending_mobile" {
		t.Fatalf("%+v %v", first, err)
	}
	restarted := &App{}
	same, resume, err := restarted.createOperationRelease(effective, b)
	if err != nil || !resume || same.ID != first.ID {
		t.Fatalf("lost or duplicated pending mobile release: %+v %v", same, err)
	}
	fresh, _ := dbGetBuild(ctx.AppDB(), b.ID)
	if !fresh.ReleaseRequested {
		t.Fatal("incomplete mobile job is no longer retryable")
	}
}
func TestSigningResourceLockWaitHonorsCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	unlock, err := lockIOSSigningResources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if release, err := lockIOSSigningResources(ctx); err == nil {
		release()
		t.Fatal("concurrent signing resource owner admitted")
	}
}
