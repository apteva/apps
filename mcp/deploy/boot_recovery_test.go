package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type bootRecoveryRuntime struct {
	mu       sync.Mutex
	adoptErr error
	startErr error
	starts   []ReleaseSpec
}

func (r *bootRecoveryRuntime) Start(spec ReleaseSpec) (*RunningRelease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, spec)
	if r.startErr != nil {
		return nil, r.startErr
	}
	return &RunningRelease{
		ReleaseID: spec.ReleaseID,
		Port:      spec.Port,
		PID:       900000 + int(spec.ReleaseID),
		stopCh:    make(chan struct{}),
	}, nil
}

func (r *bootRecoveryRuntime) Stop(*RunningRelease) error { return nil }

func (r *bootRecoveryRuntime) Adopt(releaseID int64, pid, port int) (*RunningRelease, error) {
	if r.adoptErr != nil {
		return nil, r.adoptErr
	}
	return &RunningRelease{
		ReleaseID: releaseID,
		Port:      port,
		PID:       pid,
		stopCh:    make(chan struct{}),
	}, nil
}

func (r *bootRecoveryRuntime) LogPath(releaseID int64) string {
	return filepath.Join("releases", itoa(int(releaseID)), "runtime.log")
}

func (r *bootRecoveryRuntime) started() []ReleaseSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReleaseSpec(nil), r.starts...)
}

func TestReconcileReleasesRecoversCurrentOrphanFromExactBuild(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	setBootRecoveryGlobalCtx(t, ctx)

	port := availableTestPort(t)
	runtime := &bootRecoveryRuntime{adoptErr: errors.New("process gone")}
	app := &App{
		dataDir:          t.TempDir(),
		runtime:          runtime,
		registry:         NewSupervisorRegistry(),
		portRangeStart:   port,
		portRangeEnd:     port,
		autoRestartState: map[int64]autoRestartInfo{},
	}
	d, env := createBootRecoveryDeployment(t, ctx)
	releasedBuild := createBootRecoveryBuild(t, ctx, app.dataDir, d.ID, env.ID, "released")
	newerBuild := createBootRecoveryBuild(t, ctx, app.dataDir, d.ID, env.ID, "not-promoted")
	orphan := createBootRecoveryRelease(t, ctx, d.ID, env.ID, releasedBuild.ID, port)

	if err := app.reconcileReleases(); err != nil {
		t.Fatal(err)
	}

	old, err := dbGetRelease(ctx.AppDB(), orphan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != "stopped" || old.Error != "supervisor restarted; process did not survive" {
		t.Fatalf("orphan = %+v", old)
	}
	releases, err := dbListReleasesForEnv(ctx.AppDB(), d.ID, env.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("got %d releases, want orphan + replacement", len(releases))
	}
	replacement := releases[0]
	if replacement.ID == orphan.ID || replacement.BuildID != releasedBuild.ID || replacement.Status != "starting" {
		t.Fatalf("replacement = %+v, want exact build %d in starting", replacement, releasedBuild.ID)
	}
	if replacement.BuildID == newerBuild.ID {
		t.Fatalf("boot recovery selected newer unpromoted build %d", newerBuild.ID)
	}
	starts := runtime.started()
	if len(starts) != 1 || starts[0].ArtifactDir != releasedBuild.ArtifactPath {
		t.Fatalf("starts = %+v", starts)
	}
	freshEnv, err := dbGetEnvironment(ctx.AppDB(), env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshEnv.CurrentReleaseID == nil || *freshEnv.CurrentReleaseID != orphan.ID {
		t.Fatalf("current release changed before health verification: %v", freshEnv.CurrentReleaseID)
	}
	if !hasReleaseEvent(t, ctx, orphan.ID, "boot_recovery_started") {
		t.Fatal("missing boot_recovery_started event")
	}
}

func TestRecoverBootOrphanSkipsStaleNonCurrentRelease(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	setBootRecoveryGlobalCtx(t, ctx)
	runtime := &bootRecoveryRuntime{}
	app := &App{runtime: runtime, registry: NewSupervisorRegistry()}
	d, env := createBootRecoveryDeployment(t, ctx)
	b := createBootRecoveryBuild(t, ctx, t.TempDir(), d.ID, env.ID, "released")
	stale := createBootRecoveryRelease(t, ctx, d.ID, env.ID, b.ID, availableTestPort(t))
	current, err := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbSetEnvironmentCurrentRelease(ctx.AppDB(), env.ID, &current.ID); err != nil {
		t.Fatal(err)
	}

	recovered, err := app.recoverBootOrphan(stale)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != nil || len(runtime.started()) != 0 {
		t.Fatalf("stale release recovered: %+v starts=%+v", recovered, runtime.started())
	}
}

func TestRecoverBootOrphanLeavesStoppedWhenArtifactMissing(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	setBootRecoveryGlobalCtx(t, ctx)
	runtime := &bootRecoveryRuntime{}
	app := &App{runtime: runtime, registry: NewSupervisorRegistry()}
	d, env := createBootRecoveryDeployment(t, ctx)
	b, err := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "static", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), b.ID, map[string]any{
		"status": "succeeded", "artifact_path": filepath.Join(t.TempDir(), "missing"),
	}); err != nil {
		t.Fatal(err)
	}
	orphan := createBootRecoveryRelease(t, ctx, d.ID, env.ID, b.ID, availableTestPort(t))
	if err := dbUpdateRelease(ctx.AppDB(), orphan.ID, map[string]any{"status": "stopped"}); err != nil {
		t.Fatal(err)
	}
	if err := dbReleasePortLease(ctx.AppDB(), orphan.Port); err != nil {
		t.Fatal(err)
	}

	recovered, err := app.recoverBootOrphan(orphan)
	if err == nil || recovered != nil {
		t.Fatalf("recovered=%+v err=%v, want unavailable-artifact failure", recovered, err)
	}
	if len(runtime.started()) != 0 {
		t.Fatalf("runtime started with missing artifact: %+v", runtime.started())
	}
	if !hasReleaseEvent(t, ctx, orphan.ID, "boot_recovery_failed") {
		t.Fatal("missing boot_recovery_failed event")
	}
}

func TestRecoverBootOrphanKeepsIngressPendingWhenStartFails(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	setBootRecoveryGlobalCtx(t, ctx)
	port := availableTestPort(t)
	runtime := &bootRecoveryRuntime{startErr: errors.New("exec failed")}
	app := &App{
		runtime: runtime, registry: NewSupervisorRegistry(),
		portRangeStart: port, portRangeEnd: port,
	}
	d, env := createBootRecoveryDeployment(t, ctx)
	b := createBootRecoveryBuild(t, ctx, t.TempDir(), d.ID, env.ID, "released")
	orphan := createBootRecoveryRelease(t, ctx, d.ID, env.ID, b.ID, port)
	if err := dbUpdateRelease(ctx.AppDB(), orphan.ID, map[string]any{"status": "stopped"}); err != nil {
		t.Fatal(err)
	}
	if err := dbReleasePortLease(ctx.AppDB(), orphan.Port); err != nil {
		t.Fatal(err)
	}

	recovered, err := app.recoverBootOrphan(orphan)
	if err == nil || recovered != nil {
		t.Fatalf("recovered=%+v err=%v, want start failure", recovered, err)
	}
	releases, err := dbListReleasesForEnv(ctx.AppDB(), d.ID, env.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 || releases[0].Status != "failed" {
		t.Fatalf("releases = %+v, want failed replacement", releases)
	}
	freshEnv, err := dbGetEnvironment(ctx.AppDB(), env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshEnv.CurrentReleaseID == nil || *freshEnv.CurrentReleaseID != orphan.ID {
		t.Fatalf("current release changed after failed recovery: %v", freshEnv.CurrentReleaseID)
	}
	if !hasReleaseEvent(t, ctx, orphan.ID, "boot_recovery_failed") {
		t.Fatal("missing boot_recovery_failed event")
	}
}

func setBootRecoveryGlobalCtx(t *testing.T, ctx *sdk.AppCtx) {
	t.Helper()
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
}

func createBootRecoveryDeployment(t *testing.T, ctx *sdk.AppCtx) (*Deployment, *DeploymentEnvironment) {
	t.Helper()
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "site", SourceKind: "local", SourceRef: "/src", Framework: "static",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	return d, env
}

func createBootRecoveryBuild(t *testing.T, ctx *sdk.AppCtx, dataDir string, deploymentID, environmentID int64, marker string) *Build {
	t.Helper()
	b, err := dbCreateBuildForEnv(ctx.AppDB(), deploymentID, environmentID, "static", "")
	if err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(dataDir, "builds", itoa(int(b.ID)), "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), b.ID, map[string]any{
		"status": "succeeded", "artifact_path": dist,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := dbGetBuild(ctx.AppDB(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func createBootRecoveryRelease(t *testing.T, ctx *sdk.AppCtx, deploymentID, environmentID, buildID int64, port int) *Release {
	t.Helper()
	rel, err := dbCreateReleaseForEnv(ctx.AppDB(), deploymentID, environmentID, buildID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateRelease(ctx.AppDB(), rel.ID, map[string]any{
		"status": "live", "port": port, "pid": 899999,
	}); err != nil {
		t.Fatal(err)
	}
	if err := dbSetEnvironmentCurrentRelease(ctx.AppDB(), environmentID, &rel.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := dbAcquirePortLease(ctx.AppDB(), port, rel.ID); err != nil || !ok {
		t.Fatalf("port lease: ok=%v err=%v", ok, err)
	}
	out, err := dbGetRelease(ctx.AppDB(), rel.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func availableTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func hasReleaseEvent(t *testing.T, ctx *sdk.AppCtx, releaseID int64, kind string) bool {
	t.Helper()
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM release_events WHERE release_id = ? AND kind = ?`, releaseID, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count > 0
}
