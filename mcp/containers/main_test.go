package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifestValid(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
	if m.Name != "containers" {
		t.Fatalf("name=%q", m.Name)
	}
	gotScopes := map[string]bool{}
	for _, scope := range m.Scopes {
		gotScopes[string(scope)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !gotScopes[want] {
			t.Fatalf("manifest missing %s scope", want)
		}
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(files)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration %s: %v", file, err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply migration %s: %v", file, err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestManifestFileValid(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatalf("validate apteva.yaml: %v", err)
	}
	if m.Name != "containers" {
		t.Fatalf("name=%q", m.Name)
	}
	gotScopes := map[string]bool{}
	for _, scope := range m.Scopes {
		gotScopes[string(scope)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !gotScopes[want] {
			t.Fatalf("manifest missing %s scope", want)
		}
	}
}

func TestNormalizeRunSpecDefaults(t *testing.T) {
	spec, err := normalizeRunSpec(RunSpec{
		Name:    "demo-nginx",
		Image:   "nginx:alpine",
		Ports:   []PortSpec{{ContainerPort: 80}},
		Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if spec.RestartPolicy != "unless-stopped" {
		t.Fatalf("restart_policy=%q", spec.RestartPolicy)
	}
	if spec.HealthPath != "/" {
		t.Fatalf("health_path=%q", spec.HealthPath)
	}
	if spec.Ports[0].BindAddr != "127.0.0.1" || spec.Ports[0].Protocol != "tcp" {
		t.Fatalf("port defaults not applied: %+v", spec.Ports[0])
	}
}

func TestNormalizeRunSpecRejectsUnsafeInputs(t *testing.T) {
	bad := []RunSpec{
		{Name: "Bad Name", Image: "nginx"},
		{Name: "ok", Image: ""},
		{Name: "ok", Image: "nginx", HostID: 7},
		{Name: "ok", Image: "nginx", Ports: []PortSpec{{ContainerPort: 70000}}},
		{Name: "ok", Image: "nginx", Volumes: []VolumeSpec{{Name: "data", MountPath: "relative"}}},
	}
	for _, spec := range bad {
		if _, err := normalizeRunSpec(spec); err == nil {
			t.Fatalf("expected error for %+v", spec)
		}
	}
}

func TestListWorkloadsHidesDestroyedByDefault(t *testing.T) {
	db := testDB(t)
	running := &Workload{
		ID: "wrk_running", Name: "running", Kind: "container", Image: "nginx:alpine",
		Status: StatusRunning, DesiredStatus: StatusRunning, ContainerName: "containers-running",
		NetworkName: "containers-running", HealthStatus: "healthy", HealthPath: "/",
		ConfigJSON: `{}`, EnvJSON: `{}`, ResourcesJSON: `{}`, RestartPolicy: "unless-stopped",
	}
	destroyed := &Workload{
		ID: "wrk_destroyed", Name: "destroyed", Kind: "container", Image: "nginx:alpine",
		Status: StatusRunning, DesiredStatus: StatusRunning, ContainerName: "containers-destroyed",
		NetworkName: "containers-destroyed", HealthStatus: "healthy", HealthPath: "/",
		ConfigJSON: `{}`, EnvJSON: `{}`, ResourcesJSON: `{}`, RestartPolicy: "unless-stopped",
	}
	if err := insertWorkload(db, running, nil, nil); err != nil {
		t.Fatalf("insert running: %v", err)
	}
	if err := insertWorkload(db, destroyed, nil, nil); err != nil {
		t.Fatalf("insert destroyed: %v", err)
	}
	if err := deleteWorkloadRows(db, destroyed.ID); err != nil {
		t.Fatalf("destroy workload: %v", err)
	}
	rows, err := listWorkloads(db, "")
	if err != nil {
		t.Fatalf("list workloads: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != running.ID {
		t.Fatalf("default list = %+v, want only %s", rows, running.ID)
	}
	destroyedRows, err := listWorkloads(db, StatusDestroyed)
	if err != nil {
		t.Fatalf("list destroyed: %v", err)
	}
	if len(destroyedRows) != 1 || destroyedRows[0].ID != destroyed.ID {
		t.Fatalf("destroyed list = %+v, want only %s", destroyedRows, destroyed.ID)
	}
	if destroyedRows[0].HealthStatus != StatusDestroyed || destroyedRows[0].LastError != "" {
		t.Fatalf("destroyed state not cleaned up: %+v", destroyedRows[0])
	}
}

func TestDestroyedWorkloadNameCanBeReused(t *testing.T) {
	db := testDB(t)
	first := testWorkload("wrk_first", "demo", StatusRunning)
	if err := insertWorkload(db, first, nil, nil); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	if err := deleteWorkloadRows(db, first.ID); err != nil {
		t.Fatalf("destroy first: %v", err)
	}
	second := testWorkload("wrk_second", "demo", StatusRunning)
	if err := insertWorkload(db, second, nil, nil); err != nil {
		t.Fatalf("reuse destroyed name: %v", err)
	}
	third := testWorkload("wrk_third", "demo", StatusRunning)
	if err := insertWorkload(db, third, nil, nil); err == nil {
		t.Fatalf("expected duplicate active name to fail")
	}
}

func TestDestroyDoesNotHideWorkloadWhenDockerCleanupFails(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_running", "demo", StatusRunning)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	app := &App{backend: fakeDockerBackend{removeErr: errors.New("docker unavailable")}}
	err := app.destroyWorkload(context.Background(), db, w.ID, false)
	if err == nil {
		t.Fatalf("expected destroy failure")
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if got.Status != StatusError || !strings.Contains(got.LastError, "docker unavailable") {
		t.Fatalf("workload was not kept visible with error: %+v", got)
	}
	rows, err := listWorkloads(db, "")
	if err != nil {
		t.Fatalf("list workloads: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != w.ID {
		t.Fatalf("failed destroy should remain visible, got %+v", rows)
	}
}

func TestDestroyIgnoresAlreadyMissingDockerResources(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_running", "demo", StatusRunning)
	if err := insertWorkload(db, w, nil, []VolumeSpec{{Name: "data", DockerVolumeName: "containers-demo-data", MountPath: "/data"}}); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	app := &App{backend: fakeDockerBackend{
		removeErr:        errors.New("docker rm: No such container: containers-demo"),
		removeNetworkErr: errors.New("docker network rm: No such network: containers-demo"),
		removeVolumeErr:  errors.New("docker volume rm: No such volume: containers-demo-data"),
	}}
	if err := app.destroyWorkload(context.Background(), db, w.ID, true); err != nil {
		t.Fatalf("destroy missing resources should succeed: %v", err)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if got.Status != StatusDestroyed {
		t.Fatalf("status=%q, want destroyed", got.Status)
	}
}

func TestHealthPollRetriesErrorWorkloads(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_error", "demo", StatusError)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	app := &App{backend: fakeDockerBackend{inspectState: &ContainerState{ID: "cid", Running: true}}}
	if err := app.pollHealth(context.Background(), db); err != nil {
		t.Fatalf("poll health: %v", err)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if got.Status != StatusRunning || got.HealthStatus != "running" || got.LastError != "" {
		t.Fatalf("workload did not recover: %+v", got)
	}
}

func TestDockerErrorRedactsEnvValues(t *testing.T) {
	err := formatDockerError([]string{"run", "-e", "SECRET=value", "--env", "TOKEN=abc", "--env=PASS=def", "nginx"}, "failed")
	msg := err.Error()
	for _, secret := range []string{"value", "abc", "def"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("secret %q leaked in %q", secret, msg)
		}
	}
	for _, redacted := range []string{"SECRET=<redacted>", "TOKEN=<redacted>", "--env=PASS=<redacted>"} {
		if !strings.Contains(msg, redacted) {
			t.Fatalf("missing redaction %q in %q", redacted, msg)
		}
	}
}

func testWorkload(id, name, status string) *Workload {
	return &Workload{
		ID: id, Name: name, Kind: "container", Image: "nginx:alpine",
		Status: status, DesiredStatus: StatusRunning, ContainerName: "containers-" + name,
		NetworkName: "containers-" + name, HealthStatus: "unknown", HealthPath: "/",
		ConfigJSON: `{}`, EnvJSON: `{}`, ResourcesJSON: `{}`, RestartPolicy: "unless-stopped",
	}
}

type fakeDockerBackend struct {
	removeErr        error
	removeNetworkErr error
	removeVolumeErr  error
	inspectState     *ContainerState
	inspectErr       error
}

func (f fakeDockerBackend) Probe(context.Context) error { return nil }
func (f fakeDockerBackend) CreateNetwork(context.Context, string) error {
	return nil
}
func (f fakeDockerBackend) CreateVolume(context.Context, string) error {
	return nil
}
func (f fakeDockerBackend) Run(context.Context, RunSpec, string, string) (string, error) {
	return "cid", nil
}
func (f fakeDockerBackend) Start(context.Context, string) error   { return nil }
func (f fakeDockerBackend) Stop(context.Context, string) error    { return nil }
func (f fakeDockerBackend) Restart(context.Context, string) error { return nil }
func (f fakeDockerBackend) Remove(context.Context, string, bool) error {
	return f.removeErr
}
func (f fakeDockerBackend) RemoveNetwork(context.Context, string) error {
	return f.removeNetworkErr
}
func (f fakeDockerBackend) RemoveVolume(context.Context, string) error {
	return f.removeVolumeErr
}
func (f fakeDockerBackend) Logs(context.Context, string, int) (string, error) {
	return "", nil
}
func (f fakeDockerBackend) Inspect(context.Context, string) (*ContainerState, error) {
	if f.inspectErr != nil {
		return nil, f.inspectErr
	}
	if f.inspectState != nil {
		return f.inspectState, nil
	}
	return &ContainerState{ID: "cid", Running: true}, nil
}
