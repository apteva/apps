package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// These regression tests express the desired behavior. Failures reproduce v0.4.0 defects.
func TestAuditRetainedVolumeMetadata(t *testing.T) {
	db := testDB(t)
	a := &App{backend: fakeDockerBackend{}}
	w, _, err := a.prepareWorkload(nil, db, RunSpec{Name: "retain-audit", Image: "alpine:3.20", Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.destroyWorkload(context.Background(), nil, db, w.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Volumes) == 0 {
		t.Fatal("destroy(delete_volumes=false) erased the retained volume association")
	}
}
func TestAuditHealthMustNotResurrectDestroyed(t *testing.T) {
	db := testDB(t)
	a := &App{backend: fakeDockerBackend{}}
	w := testWorkload("wrk_audit_destroy", "audit-destroy", StatusRunning)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.destroyWorkload(context.Background(), nil, db, w.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := a.probeWorkload(context.Background(), nil, db, w.ID); err == nil {
		got, _ := getWorkload(db, w.ID)
		if got.Status != StatusDestroyed {
			t.Fatalf("health resurrected destroyed workload as %q", got.Status)
		}
	}
}
func TestAuditHealthMustLeaveCreatingAlone(t *testing.T) {
	db := testDB(t)
	a := &App{backend: fakeDockerBackend{inspectErr: fmt.Errorf("No such container")}}
	w := testWorkload("wrk_audit_creating", "audit-creating", StatusCreating)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.pollHealth(context.Background(), nil, db); err != nil {
		t.Fatal(err)
	}
	got, _ := getWorkload(db, w.ID)
	if got.Status != StatusCreating {
		t.Fatalf("health overwrote creating with %q", got.Status)
	}
}
func TestAuditHTTPProjectIsolation(t *testing.T) {
	db := testDB(t)
	a := &App{backend: fakeDockerBackend{}}
	m := a.Manifest()
	old := globalCtx
	globalCtx = sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, nil)
	defer func() { globalCtx = old }()
	w := testWorkload("wrk_other_project", "other-project", StatusRunning)
	w.ProjectID = "project-b"
	w.OwnerAppInstallID = 42
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/workloads", nil)
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	rec := httptest.NewRecorder()
	a.handleWorkloads(rec, req)
	if strings.Contains(rec.Body.String(), w.ID) {
		t.Fatal("project-a HTTP response contains project-b app-owned workload")
	}
}
func TestAuditStaleCompletionCannotOverwriteCancellation(t *testing.T) {
	db := testDB(t)
	backend := &executionBackendStub{}
	a := &App{backend: backend}
	m := a.Manifest()
	app := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, nil)
	w := testWorkload("wrk_race", "race", StatusRunning)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	e, _, err := insertExecution(db, &Execution{ID: "exe_race", WorkloadID: w.ID, OwnerAppInstallID: 1, Argv: []string{"true"}, Status: executionRunning, RuntimeContainerName: w.ContainerName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.cancelExecution(app, e); err != nil {
		t.Fatal(err)
	}
	code := 0
	a.finishExecution(app, e, executionSucceeded, &code, "", "")
	got, _ := getExecution(db, e.ID)
	if got.Status != executionCancelled {
		t.Fatalf("stale supervisor changed cancelled to %q", got.Status)
	}
}

func auditContainer(t *testing.T, command ...string) (context.Context, string) {
	t.Helper()
	if os.Getenv("RUN_CONTAINERS_TESTS") != "1" {
		t.Skip("Docker tests opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	name := "containers-audit-" + newWorkloadID()[4:]
	image := os.Getenv("AUDIT_DOCKER_IMAGE")
	if image == "" {
		image = "oven/bun:1-debian"
	}
	args := []string{"run", "-d", "--name", name, image}
	args = append(args, command...)
	if _, err := docker(ctx, args...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = docker(c, "rm", "-f", name)
	})
	return ctx, name
}
func auditExec(t *testing.T, ctx context.Context, name, session string, argv []string) (*Execution, string) {
	t.Helper()
	backend := LocalDocker{}
	e := &Execution{ID: newExecutionID(), RuntimeContainerName: name}
	runtimeID, err := backend.StartExecution(ctx, executionRuntimeSpec{ExecutionID: e.ID, ContainerName: name, SessionKey: session, Argv: argv})
	if err != nil {
		t.Fatal(err)
	}
	e.RuntimeContainerID = runtimeID
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		st, err := backend.InspectExecution(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Running {
			out, err := backend.ExecutionLogs(ctx, e, 2000)
			if err != nil {
				t.Fatal(err)
			}
			code := st.ExitCode
			e.ExitCode = &code
			return e, out
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("execution failed to complete in four seconds")
	return nil, ""
}
func TestAuditLocalLogsIncludeStderr(t *testing.T) {
	ctx, name := auditContainer(t, "sh", "-c", "echo stdout-line; echo stderr-line >&2")
	if _, err := docker(ctx, "wait", name); err != nil {
		t.Fatal(err)
	}
	out, err := (LocalDocker{}).Logs(ctx, name, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stderr-line") {
		t.Fatalf("stderr missing from logs: %q", out)
	}
}
func TestAuditImageCannotInjectCLIOptions(t *testing.T) {
	if os.Getenv("RUN_CONTAINERS_TESTS") != "1" {
		t.Skip("Docker tests opt-in")
	}
	// Harmless --entrypoint demonstrates flag interpretation; no privileged container or host mount.
	spec, err := normalizeRunSpec(RunSpec{Name: "image-audit", Image: "--entrypoint=/bin/echo", Command: []string{"alpine:3.20", "OPTION_INJECTION"}, RestartPolicy: "no"})
	if err != nil {
		return
	}
	name := "containers-audit-" + newWorkloadID()[4:]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = docker(c, "rm", "-f", name)
	})
	_, err = (LocalDocker{}).Run(ctx, spec, name, "bridge")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = docker(ctx, "wait", name)
	out, _ := (LocalDocker{}).Logs(ctx, name, 20)
	if strings.Contains(out, "OPTION_INJECTION") {
		t.Fatal("image interpreted as Docker --entrypoint flag; actual image supplied through command")
	}
}
func TestAuditShellLongCommand(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	defer persistentShells.CloseAll()
	_, out := auditExec(t, ctx, name, "long", []string{"/bin/sh", "-c", "printf '%s' '" + strings.Repeat("x", 6000) + "'"})
	if len(out) != 6000 {
		t.Fatalf("expected 6000 output bytes, got %d", len(out))
	}
}
func TestAuditShellExplicitExitZero(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	defer persistentShells.CloseAll()
	e, out := auditExec(t, ctx, name, "exit", []string{"/bin/sh", "-c", "exit 0"})
	if *e.ExitCode != 0 {
		t.Fatalf("exit 0 recorded as %d; output=%q", *e.ExitCode, out)
	}
}
func TestAuditExecutionOutputFileBounded(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	e, _ := auditExec(t, ctx, name, "", []string{"/bin/sh", "-c", "head -c 3145728 /dev/zero"})
	out, err := docker(ctx, "exec", name, "wc", "-c", executionControlDir(e.ID)+"/output")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "3145728") {
		t.Fatal("3 MiB retained on container disk despite default 1 MiB execution output cap")
	}
}

func TestAuditAlpineShellInitialization(t *testing.T) {
	t.Setenv("AUDIT_DOCKER_IMAGE", "alpine:3.20")
	ctx, name := auditContainer(t, "sleep", "300")
	defer persistentShells.CloseAll()
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := (LocalDocker{}).StartExecution(short, executionRuntimeSpec{ExecutionID: newExecutionID(), ContainerName: name, SessionKey: "alpine", Argv: []string{"true"}})
	if err != nil {
		t.Fatalf("Alpine persistent shell initialization failed: %v", err)
	}
}

func TestAuditExplicitBashPreserved(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	defer persistentShells.CloseAll()
	_, out := auditExec(t, ctx, name, "bash", []string{"/bin/bash", "-c", `printf '%s' "${BASH_VERSION:-wrong-shell}"`})
	if strings.Contains(out, "wrong-shell") {
		t.Fatalf("explicit bash command executed by /bin/sh: %q", out)
	}
}

type auditCancelPlatform struct {
	containersPlatformStub
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *auditCancelPlatform) CallAppResult(_ string, _ string, _ map[string]any, out any) error {
	p.cancel()
	// Cancellation wakes runRemote before this shared response is populated.
	err := json.Unmarshal([]byte(`{"output":"late output","exit_code":7}`), out)
	close(p.done)
	return err
}
func TestAuditRemoteCancellationRace(t *testing.T) {
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		p := &auditCancelPlatform{cancel: cancel, done: make(chan struct{})}
		m := (&App{}).Manifest()
		app := sdk.NewAppCtxForTest(&m, nil, sdk.Config{}, p, nil)
		d := &RemoteDocker{app: app, instanceID: 1}
		_, _, _ = d.runRemote(ctx, "true", 1)
		<-p.done
	}
}

func TestAuditNonRootInjectedFileReadable(t *testing.T) {
	if os.Getenv("RUN_CONTAINERS_TESTS") != "1" {
		t.Skip("Docker tests opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	vol := "containers-audit-volume-" + newWorkloadID()[4:]
	if _, err := docker(ctx, "volume", "create", vol); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = docker(c, "volume", "rm", vol)
	})
	if err := (LocalDocker{}).WriteOwnedVolumeFile(ctx, vol, "secret", []byte("audit-test-value"), "0600", "1000:1000"); err != nil {
		t.Fatal(err)
	}
	_, err := docker(ctx, "run", "--rm", "--user", "1000:1000", "-v", vol+":/data", "alpine:3.20", "cat", "/data/secret")
	if err != nil {
		t.Fatalf("default injected secret is unreadable by workload user: %v", err)
	}
}
func TestAuditArchiveDestinationSymlink(t *testing.T) {
	if os.Getenv("RUN_CONTAINERS_TESTS") != "1" {
		t.Skip("Docker tests opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	vol := "containers-audit-volume-" + newWorkloadID()[4:]
	if _, err := docker(ctx, "volume", "create", vol); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = docker(c, "volume", "rm", vol)
	})
	_, err := docker(ctx, "run", "--rm", "-v", vol+":/volume", "alpine:3.20", "sh", "-c", "mkdir /volume/other; ln -s other /volume/requested")
	if err != nil {
		t.Fatal(err)
	}
	archive := testTarGzip(t, []*tar.Header{{Name: "proof", Mode: 0644, Size: 2, Typeflag: tar.TypeReg}}, [][]byte{[]byte("ok")})
	if err := validateTarGzip(archive, 1<<20, 1<<20, 10); err != nil {
		t.Fatal(err)
	}
	if err := (LocalDocker{}).ImportVolumeArchive(ctx, vol, "requested", archive); err != nil {
		return
	}
	out, err := docker(ctx, "run", "--rm", "-v", vol+":/volume:ro", "alpine:3.20", "cat", "/volume/other/proof")
	if err == nil && out == "ok" {
		t.Fatal("archive followed existing destination symlink and wrote into sibling directory")
	}
}

func TestAuditMalformedRunSpecRejected(t *testing.T) {
	db := testDB(t)
	a := &App{backend: fakeDockerBackend{}}
	m := a.Manifest()
	app := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, nil)
	_, err := a.toolRunCtx(context.Background(), app, map[string]any{"name": "malformed-resources", "image": "alpine:3.20", "resources": map[string]any{"memory_mb": "512"}})
	if err == nil {
		t.Fatal("malformed resource type accepted; requested memory cap discarded")
	}
}
