package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRetainedVolumeTransferIsExclusiveAndScoped(t *testing.T) {
	db := testDB(t)
	a := &App{backend: fakeDockerBackend{}}
	owner := ownerIdentity{ProjectID: "a", InstallID: 7}
	first, _, err := a.prepareOwnedWorkload(nil, db, RunSpec{Name: "old", Image: "alpine:3.20", Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}}}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.destroyWorkload(context.Background(), nil, db, first.ID, false); err != nil {
		t.Fatal(err)
	}
	first, _ = getWorkload(db, first.ID)
	next := RunSpec{Name: "next", Image: "alpine:3.20", Volumes: []VolumeSpec{{Name: "data", MountPath: "/data", RetainedFrom: first.ID}}}
	if _, _, err = a.prepareOwnedWorkload(nil, db, next, ownerIdentity{ProjectID: "b", InstallID: 7}); err == nil {
		t.Fatal("cross-project transfer accepted")
	}
	second, _, err := a.prepareOwnedWorkload(nil, db, next, owner)
	if err != nil {
		t.Fatal(err)
	}
	second, _ = getWorkload(db, second.ID)
	if second.Volumes[0].DockerVolumeName != first.Volumes[0].DockerVolumeName {
		t.Fatal("did not reuse volume")
	}
	next.Name = "third"
	if _, _, err = a.prepareOwnedWorkload(nil, db, next, owner); err == nil {
		t.Fatal("second transfer accepted")
	}
	first, _ = getWorkload(db, first.ID)
	if len(first.Volumes) != 0 {
		t.Fatal("source still owns transferred volume")
	}
	if err = a.destroyWorkload(context.Background(), nil, db, second.ID, true); err != nil {
		t.Fatal(err)
	}
	second, _ = getWorkload(db, second.ID)
	if len(second.Volumes) != 0 {
		t.Fatal("deleted volume metadata remains")
	}
}

func TestHTTPMutationsAndHistoryCannotCrossProjects(t *testing.T) {
	db := testDB(t)
	a := &App{backend: fakeDockerBackend{}}
	m := a.Manifest()
	old := globalCtx
	globalCtx = sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, nil)
	defer func() { globalCtx = old }()
	w := testWorkload("wrk_http_scope", "http-scope", StatusRunning)
	w.ProjectID = "b"
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, action := range []struct{ method, path string }{{"GET", ""}, {"POST", "/stop"}, {"POST", "/restart"}, {"POST", "/health"}, {"DELETE", ""}, {"GET", "/executions"}, {"DELETE", "/sessions/shared"}} {
		t.Run(action.method+action.path, func(t *testing.T) {
			r := httptest.NewRequest(action.method, "/api/workloads/"+w.ID+action.path, nil)
			r.Header.Set("X-Apteva-Project-ID", "a")
			rec := httptest.NewRecorder()
			a.handleWorkloadItem(rec, r)
			if rec.Code != 404 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
		})
	}
	got, _ := getWorkload(db, w.ID)
	if got.Status != StatusRunning {
		t.Fatal("cross-project request mutated workload")
	}
}

func TestBlueprintExplicitEmptyAndZeroOverrides(t *testing.T) {
	db := testDB(t)
	if err := seedBlueprints(db); err != nil {
		t.Fatal(err)
	}
	bps, err := listBlueprints(db)
	if err != nil || len(bps) == 0 {
		t.Fatal(err)
	}
	for _, bp := range bps {
		var in RunSpec
		raw := fmt.Sprintf(`{"blueprint_slug":%q,"env":{},"ports":[],"volumes":[],"files":[],"resources":{"memory_mb":0,"cpu":0},"disable_health_check":true}`, bp.Slug)
		if err = json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		got, err := (&App{}).expandBlueprint(db, in)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Env) != 0 || len(got.Ports) != 0 || len(got.Volumes) != 0 || got.Resources.MemoryMB != 0 || got.Resources.CPU != 0 || !got.DisableHealthCheck {
			t.Fatalf("override failed: %+v", got)
		}
	}
}

func TestWorkloadGuardCancelsCreateAndSerializesDestroy(t *testing.T) {
	a := &App{}
	ctx, unlock, err := a.lockWorkload(context.Background(), "w", true)
	if err != nil {
		t.Fatal(err)
	}
	a.cancelCreation("w")
	select {
	case <-ctx.Done():
	default:
		t.Fatal("creation context not cancelled")
	}
	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, release, err := a.lockWorkload(context.Background(), "w", false)
		if err == nil {
			close(acquired)
			release()
		}
		close(done)
	}()
	select {
	case <-acquired:
		t.Fatal("destroy entered before create finished")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	<-done
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	if len(a.guards) != 0 {
		t.Fatal("idle guard leaked")
	}
}

type retryBackend struct {
	executionBackendStub
	failures        int
	cleanupFailures int
}

func (b *retryBackend) StopExecution(ctx context.Context, e *Execution) error {
	b.mu.Lock()
	if b.failures > 0 {
		b.failures--
		b.mu.Unlock()
		return errors.New("temporary stop failure")
	}
	b.mu.Unlock()
	return b.executionBackendStub.StopExecution(ctx, e)
}
func (b *retryBackend) RemoveExecution(ctx context.Context, e *Execution) error {
	b.mu.Lock()
	if b.cleanupFailures > 0 {
		b.cleanupFailures--
		b.mu.Unlock()
		return errors.New("temporary cleanup failure")
	}
	b.mu.Unlock()
	return b.executionBackendStub.RemoveExecution(ctx, e)
}
func TestCancellationAndRetentionRetryFailures(t *testing.T) {
	db := testDB(t)
	b := &retryBackend{failures: 1, cleanupFailures: 1}
	a := &App{backend: b}
	m := a.Manifest()
	app := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, nil)
	w := testWorkload("wrk_retry", "retry", StatusRunning)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	e, _, err := insertExecution(db, &Execution{ID: "exe_retry", WorkloadID: w.ID, Argv: []string{"true"}, Status: executionRunning, RuntimeContainerName: w.ContainerName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.cancelExecution(app, e); err == nil {
		t.Fatal("stop failure hidden")
	}
	e, _ = getExecution(db, e.ID)
	if e.Status != executionCancelling {
		t.Fatalf("abandoned cancellation as %s", e.Status)
	}
	if err = reconcileScopedExecutions(context.Background(), app, a, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e, _ = getExecution(db, e.ID)
		if e.Status == executionCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e.Status != executionCancelled {
		t.Fatal("worker did not retry cancellation")
	}
	if err = updateExecution(db, e.ID, map[string]any{"finished_at": time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339), "output": "kept"}); err != nil {
		t.Fatal(err)
	}
	if err = retainExecutionLogs(context.Background(), app, a); err != nil {
		t.Fatal(err)
	}
	e, _ = getExecution(db, e.ID)
	out, _ := executionLogs(db, e)
	if e.RuntimeContainerName == "" || out != "kept" {
		t.Fatal("cleanup failure erased retry metadata")
	}
	if err = retainExecutionLogs(context.Background(), app, a); err != nil {
		t.Fatal(err)
	}
	e, _ = getExecution(db, e.ID)
	if e.RuntimeContainerName != "" {
		t.Fatal("successful cleanup did not expire metadata")
	}
}
func TestOnlyOneConcurrentTerminalTransitionWins(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_cas", "cas", StatusRunning)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	e, _, err := insertExecution(db, &Execution{ID: "exe_cas", WorkloadID: w.ID, Status: executionRunning})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wins := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := transitionExecution(db, e.ID, []string{executionRunning}, map[string]any{"status": executionSucceeded})
			if err != nil {
				t.Error(err)
			}
			wins <- won
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for won := range wins {
		if won {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d terminal winners", n)
	}
}
func TestExecutionTotalInputAndScopedQueries(t *testing.T) {
	w := testWorkload("wrk_scope", "scope", StatusRunning)
	argv := make([]string, 32)
	for i := range argv {
		argv[i] = strings.Repeat("x", 65536)
	}
	if _, err := normalizeExecutionInput(executionInput{Argv: argv}, w, 60); err == nil {
		t.Fatal("oversized argv-only input accepted")
	}
	db := testDB(t)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{"", "a", "b"} {
		if _, _, err := insertExecution(db, &Execution{ID: fmt.Sprintf("exe_scope%d", i), WorkloadID: w.ID, ProjectID: p, Status: executionRunning}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"", "a", "b"} {
		rows, err := queryActiveExecutions(db, p, true)
		if err != nil || len(rows) != 1 || rows[0].ProjectID != p {
			t.Fatalf("scope %q: %v %v", p, rows, err)
		}
	}
}
func TestPublishedPortProtocolAndHealthDefaults(t *testing.T) {
	got, err := parsePublishedPorts(`{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"31000"}],"53/udp":[{"HostIp":"127.0.0.1","HostPort":"31001"}]}`, []PortSpec{{ContainerPort: 80, Protocol: "tcp", BindAddr: "0.0.0.0"}, {ContainerPort: 53, Protocol: "udp", BindAddr: "127.0.0.1"}})
	if err != nil || got[0].HostPort != 31000 || got[1].HostPort != 31001 {
		t.Fatalf("ports: %+v %v", got, err)
	}
	if publicWorkloadURL("remote.example", PortSpec{Protocol: "tcp", BindAddr: "127.0.0.1", HostPort: 80}) != "" {
		t.Fatal("remote loopback URL exposed")
	}
	for _, protocol := range []string{"tcp", "udp"} {
		s, err := normalizeRunSpec(RunSpec{Name: "db", Image: "redis:alpine", Ports: []PortSpec{{ContainerPort: 6379, Protocol: protocol}}})
		if err != nil || s.HealthPath != "" {
			t.Fatalf("implicit HTTP check: %+v %v", s, err)
		}
	}
}

func TestArchiveRejectsChecksumCorruptionAndConcatenation(t *testing.T) {
	good := testTarGzip(t, []*tar.Header{{Name: "data", Typeflag: tar.TypeReg, Mode: 0600, Size: 2}}, [][]byte{[]byte("ok")})
	corrupted := append([]byte(nil), good...)
	corrupted[len(corrupted)-8] ^= 1
	for _, data := range [][]byte{corrupted, append(append([]byte(nil), good...), good...)} {
		if err := validateTarGzip(data, 1<<20, 1<<20, 100); err == nil {
			t.Fatal("invalid gzip stream accepted")
		}
	}
}

func TestLateCreateCleanupRetriesWithoutDeletingRetainedVolumes(t *testing.T) {
	db := testDB(t)
	removed := []string{}
	volumes := []string{}
	a := &App{backend: fakeDockerBackend{removedContainers: &removed, removedVolumes: &volumes}}
	m := a.Manifest()
	app := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, nil)
	w := testWorkload("wrk_late", "late", StatusError)
	if err := insertWorkload(db, w, nil, []VolumeSpec{{Name: "data", MountPath: "/data", DockerVolumeName: "late-data"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO containers_runtime_cleanup(workload_id,project_id,retry_until) VALUES(?,?,?)`, w.ID, "", time.Now().Add(time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := a.recoverRuntimeCleanup(context.Background(), app); err != nil {
			t.Fatal(err)
		}
	}
	if len(removed) != 2 || len(volumes) != 0 {
		t.Fatalf("cleanup calls containers=%v volumes=%v", removed, volumes)
	}
	if err := a.probeWorkload(context.Background(), app, db, w.ID); !errors.Is(err, errConflict) {
		t.Fatalf("late workload became healthy: %v", err)
	}
	if _, err := a.startWorkload(context.Background(), app, db, w.ID); !errors.Is(err, errConflict) {
		t.Fatalf("start raced cleanup: %v", err)
	}
	if _, err := db.Exec(`UPDATE containers_runtime_cleanup SET retry_until=? WHERE workload_id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), w.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverRuntimeCleanup(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM containers_runtime_cleanup`).Scan(&count)
	if count != 0 {
		t.Fatal("settled cleanup record leaked")
	}
}

func TestRemoteAlreadyCancelledContextDoesNotStartWork(t *testing.T) {
	platform := &containersPlatformStub{}
	manifest := (&App{}).Manifest()
	app := sdk.NewAppCtxForTest(&manifest, nil, sdk.Config{}, platform, nil)
	remote := &RemoteDocker{app: app, instanceID: 7}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := remote.runRemote(ctx, "true", 30); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if len(platform.calls) != 0 {
		t.Fatal("remote work started after cancellation")
	}
}

type delayedObservationBackend struct {
	executionBackendStub
	observed, release chan struct{}
}

func (b *delayedObservationBackend) InspectExecution(context.Context, *Execution) (*ContainerState, error) {
	close(b.observed)
	<-b.release
	return nil, errors.New("stale observation failure")
}
func TestStaleObservationCannotCorruptCompletedMetadata(t *testing.T) {
	db := testDB(t)
	b := &delayedObservationBackend{observed: make(chan struct{}), release: make(chan struct{})}
	a := &App{backend: b}
	m := a.Manifest()
	app := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, nil)
	w := testWorkload("wrk_observe", "observe", StatusRunning)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	e, _, err := insertExecution(db, &Execution{ID: "exe_observe", WorkloadID: w.ID, Status: executionRunning, RuntimeContainerName: w.ContainerName, TimeoutSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = markExecutionStarted(db, e.ID, "runtime"); err != nil {
		t.Fatal(err)
	}
	e, _ = getExecution(db, e.ID)
	done := make(chan struct{})
	go func() { a.superviseExecution(app, e.ID); close(done) }()
	<-b.observed
	code := 0
	a.finishExecution(app, e, executionSucceeded, &code, "", "")
	close(b.release)
	<-done
	got, _ := getExecution(db, e.ID)
	if got.Status != executionSucceeded || got.Error != "" || got.ErrorCode != "" {
		t.Fatalf("completed metadata corrupted: %+v", got)
	}
}
