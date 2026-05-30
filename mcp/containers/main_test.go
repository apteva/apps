package main

import (
	"database/sql"
	"os"
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
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	raw, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatalf("apply migration: %v", err)
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
