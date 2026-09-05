package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestSensitiveToolsAreAppOnly(t *testing.T) {
	want := map[string]bool{
		"containers_exec_start":    true,
		"containers_exec_get":      true,
		"containers_exec_logs":     true,
		"containers_exec_cancel":   true,
		"containers_volume_import": true,
		"containers_volume_export": true,
	}
	for _, tool := range (&App{}).MCPTools() {
		if !want[tool.Name] {
			continue
		}
		if tool.Exposure != sdk.ToolExposureAppOnly || tool.HandlerCtx == nil {
			t.Fatalf("tool %s must be app_only with caller-aware handler", tool.Name)
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing private tools: %+v", want)
	}
}

func TestOwnedWorkloadCannotBeAccessedByAnotherApp(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_owned", "owned", StatusRunning)
	w.OwnerAppInstallID = 41
	w.OwnerAppName = "code"
	w.ProjectID = "project-a"
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	if _, err := requireOwnedWorkload(db, w.ID, ownerIdentity{InstallID: 41, ProjectID: "project-a"}); err != nil {
		t.Fatalf("owner denied: %v", err)
	}
	for _, owner := range []ownerIdentity{
		{InstallID: 42, ProjectID: "project-a"},
		{InstallID: 41, ProjectID: "project-b"},
		{},
	} {
		if _, err := requireOwnedWorkload(db, w.ID, owner); !errors.Is(err, errWorkloadNotFound) {
			t.Fatalf("non-owner %+v received err %v", owner, err)
		}
	}
}

func TestPrepareWorkloadRedactsEnvironmentAndStoresOwner(t *testing.T) {
	db := testDB(t)
	app := &App{backend: fakeDockerBackend{}}
	w, _, err := app.prepareOwnedWorkload(nil, db, RunSpec{
		Name: "owned", Image: "alpine:3.20", Env: map[string]string{"TOKEN": "secret"},
	}, ownerIdentity{InstallID: 41, AppName: "code", ProjectID: "project-a"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(w.ConfigJSON, "secret") {
		t.Fatalf("config_json leaked environment value: %s", w.ConfigJSON)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.OwnerAppInstallID != 41 || got.OwnerAppName != "code" || got.ProjectID != "project-a" {
		t.Fatalf("owner not persisted: %+v", got)
	}
	if len(got.EnvKeys) != 1 || got.EnvKeys[0] != "TOKEN" || got.Env["TOKEN"] != redactedValue {
		t.Fatalf("stored environment was not redacted safely: %+v", got)
	}
}

func TestCallerAwareRunRecordsOwnerAndFiltersLists(t *testing.T) {
	db := testDB(t)
	app := &App{backend: fakeDockerBackend{}}
	manifest := app.Manifest()
	appCtx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, nil, nil)
	ownerCtx := sdk.WithCaller(context.Background(), &sdk.Caller{
		AppInstallID: 41, AppName: "code", ProjectID: "project-a",
	})
	result, err := app.toolRunCtx(ownerCtx, appCtx, map[string]any{
		"name": "owned-run", "image": "alpine:3.20", "command": []any{"sleep", "infinity"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	workload := result.(map[string]any)["workload"].(*Workload)
	if workload.OwnerAppInstallID != 41 || workload.OwnerAppName != "code" || workload.ProjectID != "project-a" {
		t.Fatalf("caller owner not recorded: %+v", workload)
	}
	otherCtx := sdk.WithCaller(context.Background(), &sdk.Caller{
		AppInstallID: 42, AppName: "other", ProjectID: "project-a",
	})
	listed, err := app.toolListCtx(otherCtx, appCtx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listed.(map[string]any)["count"].(int) != 0 {
		t.Fatalf("another app saw owned workload: %+v", listed)
	}
}

func TestDockerRunArgsIncludesCommandWorkingDirectoryAndUser(t *testing.T) {
	args, err := dockerRunArgs(RunSpec{
		Image: "alpine:3.20", RestartPolicy: "no", WorkingDirectory: "/workspace",
		User: "1000:1000", Command: []string{"sleep", "infinity"},
	}, "containers-workspace", "containers-workspace")
	if err != nil {
		t.Fatalf("docker args: %v", err)
	}
	joined := strings.Join(args, "|")
	if !strings.Contains(joined, "--workdir|/workspace|--user|1000:1000") || !strings.Contains(joined, "--|alpine:3.20|sleep|infinity") {
		t.Fatalf("missing command overrides: %v", args)
	}
}

func TestArchiveValidationRejectsTraversalAndEscapingLinks(t *testing.T) {
	good := testTarGzip(t, []*tar.Header{
		{Name: "workspace/file.txt", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg},
		{Name: "workspace/link", Linkname: "file.txt", Typeflag: tar.TypeSymlink},
	}, [][]byte{[]byte("ok"), nil})
	if err := validateTarGzip(good, 1<<20, 1<<20, 10); err != nil {
		t.Fatalf("safe archive rejected: %v", err)
	}
	bad := []struct {
		name string
		h    *tar.Header
	}{
		{"traversal", &tar.Header{Name: "../escape", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}},
		{"absolute", &tar.Header{Name: "/escape", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}},
		{"escaping symlink", &tar.Header{Name: "workspace/link", Linkname: "../../escape", Typeflag: tar.TypeSymlink}},
		{"device", &tar.Header{Name: "workspace/device", Typeflag: tar.TypeChar}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(nil)
			if tc.h.Size > 0 {
				body = bytes.Repeat([]byte("x"), int(tc.h.Size))
			}
			archive := testTarGzip(t, []*tar.Header{tc.h}, [][]byte{body})
			if err := validateTarGzip(archive, 1<<20, 1<<20, 10); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestExecutionRunsInsideOwnedWorkloadContainerAndRetainsLogs(t *testing.T) {
	db := testDB(t)
	backend := &executionBackendStub{
		state: &ContainerState{ID: "cid-exec", Status: "exited", Running: false, ExitCode: 0},
		logs:  "tests passed\n",
	}
	app := &App{backend: backend}
	manifest := app.Manifest()
	appCtx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, nil, nil)
	recorder := tk.NewEmitRecorder()
	appCtx.SetEmitter(recorder)
	w := testWorkload("wrk_exec", "exec", StatusRunning)
	w.OwnerAppInstallID = 41
	w.OwnerAppName = "code"
	w.ProjectID = "project-a"
	w.Volumes = []VolumeSpec{{Name: "workspace", DockerVolumeName: "containers-exec-workspace", MountPath: "/workspace"}}
	if err := insertWorkload(db, w, nil, w.Volumes); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	w, _ = getWorkload(db, w.ID)
	execution, err := app.startExecution(context.Background(), appCtx, w,
		ownerIdentity{InstallID: 41, AppName: "code", ProjectID: "project-a"},
		executionInput{Argv: []string{"bun", "test"}, WorkingDirectory: "/workspace", Env: map[string]string{"CI": "true"}, TimeoutSeconds: 30, SessionKey: "workspace"})
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		execution, err = getExecution(db, execution.ID)
		if err != nil {
			t.Fatalf("get execution: %v", err)
		}
		if execution.Status == executionSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if execution.Status != executionSucceeded || execution.ExitCode == nil || *execution.ExitCode != 0 {
		t.Fatalf("execution did not succeed: %+v", execution)
	}
	if len(execution.Env) != 0 {
		t.Fatalf("execution environment was retained after start: %+v", execution.EnvKeys)
	}
	logs, err := executionLogs(db, execution)
	if err != nil || logs != "tests passed\n" {
		t.Fatalf("logs=%q err=%v", logs, err)
	}
	started := backend.startedSpec()
	if started.ContainerName != w.ContainerName {
		t.Fatalf("execution targeted %q, want persistent workload container %q", started.ContainerName, w.ContainerName)
	}
	if started.SessionKey != "workspace" || execution.SessionKey != "workspace" {
		t.Fatalf("persistent session key was not retained: started=%q execution=%q", started.SessionKey, execution.SessionKey)
	}
	if len(started.Env) != 1 || started.Env["CI"] != "true" {
		t.Fatalf("execution environment was not isolated: %+v", started.Env)
	}
	event, ok := recorder.WaitForTopic("containers.exec.completed", time.Second)
	if !ok || event.ProjectID != "project-a" {
		t.Fatalf("completion event missing or unscoped: %+v", event)
	}
	payload, _ := event.Data.(map[string]any)
	if _, leaked := payload["env"]; leaked {
		t.Fatalf("event leaked environment: %+v", payload)
	}
	if _, leaked := payload["argv"]; leaked {
		t.Fatalf("event leaked argv: %+v", payload)
	}
	if removed := backend.removedExecutions(); len(removed) != 0 {
		t.Fatalf("completed execution was removed before workload destruction: %v", removed)
	}
	if err := app.destroyWorkload(context.Background(), appCtx, db, w.ID, true); err != nil {
		t.Fatalf("destroy workload with retained execution: %v", err)
	}
	removed := backend.removedExecutions()
	if len(removed) != 1 || removed[0] != execution.RuntimeContainerName {
		t.Fatalf("destroy removed execution containers %v, want %q", removed, execution.RuntimeContainerName)
	}
}

func TestExecutionCancellationStopsCommandInsideWorkloadContainer(t *testing.T) {
	db := testDB(t)
	backend := &executionBackendStub{
		state: &ContainerState{ID: "cid-exec", Status: "running", Running: true},
		logs:  "partial output\n",
	}
	app := &App{backend: backend}
	manifest := app.Manifest()
	appCtx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, nil, nil)
	w := testWorkload("wrk_cancel", "cancel", StatusRunning)
	w.OwnerAppInstallID = 41
	w.OwnerAppName = "code"
	w.ProjectID = "project-a"
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	w, _ = getWorkload(db, w.ID)
	execution, err := app.startExecution(context.Background(), appCtx, w,
		ownerIdentity{InstallID: 41, AppName: "code", ProjectID: "project-a"},
		executionInput{Argv: []string{"sleep", "60"}, TimeoutSeconds: 30})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		execution, _ = getExecution(db, execution.ID)
		if execution.RuntimeContainerID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	execution, err = app.cancelExecution(appCtx, execution)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if execution.Status != executionCancelled {
		t.Fatalf("status=%q, want cancelled", execution.Status)
	}
	backend.mu.Lock()
	stopped := backend.stopped
	startedName := backend.started.ContainerName
	backend.mu.Unlock()
	if !stopped || startedName != w.ContainerName {
		t.Fatalf("cancel did not target command in workload container: stopped=%t execution=%q workload=%q", stopped, startedName, w.ContainerName)
	}
}

func testTarGzip(t *testing.T, headers []*tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for i, header := range headers {
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if len(bodies[i]) > 0 {
			if _, err := io.Copy(tw, bytes.NewReader(bodies[i])); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type executionBackendStub struct {
	fakeDockerBackend
	mu      sync.Mutex
	started executionRuntimeSpec
	state   *ContainerState
	logs    string
	stopped bool
	removed []string
}

func (f *executionBackendStub) StartExecution(_ context.Context, spec executionRuntimeSpec) (string, error) {
	f.mu.Lock()
	f.started = spec
	f.mu.Unlock()
	return "cid-exec", nil
}

func (f *executionBackendStub) InspectExecution(context.Context, *Execution) (*ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return &ContainerState{Status: "exited", ExitCode: 137}, nil
	}
	if f.state == nil {
		return &ContainerState{Status: "exited"}, nil
	}
	copy := *f.state
	return &copy, nil
}

func (f *executionBackendStub) StopExecution(context.Context, *Execution) error {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
	return nil
}

func (f *executionBackendStub) ExecutionLogs(context.Context, *Execution, int) (string, error) {
	return f.logs, nil
}

func (f *executionBackendStub) RemoveExecution(_ context.Context, execution *Execution) error {
	f.mu.Lock()
	f.removed = append(f.removed, execution.RuntimeContainerName)
	f.mu.Unlock()
	return nil
}

func (f *executionBackendStub) removedExecutions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *executionBackendStub) startedSpec() executionRuntimeSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}
