package main

import (
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTenantSpawnEnvForModeQuarantineIsOptIn(t *testing.T) {
	t.Setenv("APTEVA_CLONE_QUARANTINE", "")
	normal := tenantSpawnEnvForMode(t.TempDir(), 7100, "tenant-1", false)
	for _, entry := range normal {
		if entry == "APTEVA_CLONE_QUARANTINE=1" {
			t.Fatalf("normal spawn inherited quarantine: %q", entry)
		}
	}
	quarantined := tenantSpawnEnvForMode(t.TempDir(), 7100, "tenant-1", true)
	found := false
	for _, entry := range quarantined {
		if entry == "APTEVA_CLONE_QUARANTINE=1" {
			found = true
		}
	}
	if !found {
		t.Fatal("quarantine spawn did not set APTEVA_CLONE_QUARANTINE=1")
	}
}

func TestRequireTargetAppsHealthyMatchesExactInstall(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(health.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)

	app := &App{}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "apps", "deploy", "1.2.3", "bin")
	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("bin"), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "apteva.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE apps (id INTEGER PRIMARY KEY, name TEXT);
		CREATE TABLE app_installs (id INTEGER PRIMARY KEY, app_id INTEGER, version TEXT, status TEXT, local_bin_path TEXT, local_port INTEGER);
		INSERT INTO apps(id,name) VALUES (1,'deploy');
		INSERT INTO app_installs(id,app_id,version,status,local_bin_path,local_port) VALUES (11,1,'1.2.3','running',?,?);`, binPath, port); err != nil {
		t.Fatal(err)
	}
	required := []tenantAppRuntime{{ID: 11, Name: "deploy", Version: "1.2.3"}}
	if err := app.requireTargetAppsHealthy(nil, fleetHost{}, dir, required); err != nil {
		t.Fatalf("healthy exact runtime rejected: %v", err)
	}
	required[0].Version = "1.2.2"
	if err := app.requireTargetAppsHealthy(nil, fleetHost{}, dir, required); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("version mismatch accepted: %v", err)
	}
}

func TestSupportsCloneRuntimeRecovery(t *testing.T) {
	for _, version := range []string{"0.25.4", "v0.25.4", "0.25.5", "0.26.0", "1.0.0"} {
		if !supportsCloneRuntimeRecovery(version) {
			t.Errorf("%q should support clone recovery", version)
		}
	}
	for _, version := range []string{"", "latest", "0.25.3", "0.24.99", "0.25"} {
		if supportsCloneRuntimeRecovery(version) {
			t.Errorf("%q should not support clone recovery", version)
		}
	}
}

func TestWaitForQuarantinedRuntimesRequiresEveryBinary(t *testing.T) {
	app := &App{}
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "apteva.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE apps (id INTEGER PRIMARY KEY, name TEXT);
		CREATE TABLE app_installs (id INTEGER PRIMARY KEY, app_id INTEGER, version TEXT, status TEXT, local_bin_path TEXT, local_port INTEGER);
		INSERT INTO apps(id,name) VALUES (1,'deploy'),(2,'code');
		INSERT INTO app_installs(id,app_id,version,status,local_bin_path,local_port) VALUES
			(11,1,'1.2.3','running',?,7200),
			(12,2,'2.3.4','running',?,7201);`,
		filepath.Join(dir, "apps", "deploy", "1.2.3", "bin"),
		filepath.Join(dir, "apps", "code", "2.3.4", "bin")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "apps", "deploy", "1.2.3", "bin"),
		filepath.Join(dir, "apps", "code", "2.3.4", "bin"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("bin"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	host := fleetHost{}
	required, err := app.tenantAppRuntimes(nil, host, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.waitForQuarantinedRuntimes(nil, host, dir, required, 10*time.Millisecond); err != nil {
		t.Fatalf("ready runtimes rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "apps", "code", "2.3.4", "bin")); err != nil {
		t.Fatal(err)
	}
	if err := app.waitForQuarantinedRuntimes(nil, host, dir, required, 10*time.Millisecond); err == nil || !strings.Contains(err.Error(), "code@2.3.4") {
		t.Fatalf("missing runtime not reported: %v", err)
	}
}
