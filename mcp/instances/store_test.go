package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCreateRemote_AllowsMultipleRowsBeforeProviderID(t *testing.T) {
	db := openTestDB(t)
	for _, name := range []string{"first", "second"} {
		if _, err := dbCreateInstance(db, CreateInstanceInput{Name: name, Provider: "hetzner", Status: "provisioning"}); err != nil {
			t.Fatalf("create %s with empty provider id: %v", name, err)
		}
	}
	if _, err := dbCreateInstance(db, CreateInstanceInput{Name: "known", Provider: "hetzner", ProviderID: "42"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbCreateInstance(db, CreateInstanceInput{Name: "duplicate", Provider: "hetzner", ProviderID: "42"}); err == nil {
		t.Fatal("duplicate non-empty provider id was accepted")
	}
	if _, err := dbCreateInstance(db, CreateInstanceInput{Name: "other-account", Provider: "hetzner", ProviderConnectionID: 8, ProviderID: "42"}); err != nil {
		t.Fatalf("same provider id in another connection should be allowed: %v", err)
	}
}

func TestTransitionStatus_IsCompareAndSwap(t *testing.T) {
	db := openTestDB(t)
	inst, err := dbCreateInstance(db, CreateInstanceInput{Name: "host", Provider: "hetzner", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := dbTransitionStatus(db, inst.ID, []string{"ready"}, "upgrading", map[string]any{"pending_size": "cx32"})
	if err != nil || !ok {
		t.Fatalf("first transition ok=%v err=%v", ok, err)
	}
	ok, err = dbTransitionStatus(db, inst.ID, []string{"ready"}, "destroying", nil)
	if err != nil || ok {
		t.Fatalf("stale transition ok=%v err=%v", ok, err)
	}
}

func TestPinSSHHostKey_FirstKeyWins(t *testing.T) {
	db := openTestDB(t)
	inst, err := dbCreateInstance(db, CreateInstanceInput{Name: "host", Provider: "manual", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := dbPinSSHHostKey(db, inst.ID, "key-a"); err != nil || got != "key-a" {
		t.Fatalf("first pin=%q err=%v", got, err)
	}
	if got, err := dbPinSSHHostKey(db, inst.ID, "key-b"); err != nil || got != "key-a" {
		t.Fatalf("repin=%q err=%v", got, err)
	}
}

func TestLifecycleMigration_PreservesExistingRows(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"001_init.sql", "002_generic_runtime_fields.sql"} {
		body, readErr := os.ReadFile(filepath.Join("migrations", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := db.Exec(string(body)); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
	if _, err := db.Exec(`INSERT INTO instances (id,name,provider,provider_id,status,ssh_host,ssh_port,resources_json) VALUES (7,'imported','manual','','ready','host.test',2202,'{"cpu":{"cores":2}}')`); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("migrations", "003_lifecycle_safety.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join("migrations", "004_platform_metadata.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join("migrations", "005_storage.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"006_object_storage.sql", "007_operations_and_inventory.sql"} {
		body, err = os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	body, err = os.ReadFile(filepath.Join("migrations", "008_durable_operations.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}

	inst, err := dbGetInstance(db, 7)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Name != "imported" || inst.SSHHost != "host.test" || inst.SSHPort != 2202 || inst.ResourcesJSON == "" {
		t.Fatalf("migrated row=%#v", inst)
	}
	if _, err := dbCreateInstance(db, CreateInstanceInput{Name: "another", Provider: "manual", Status: "ready"}); err != nil {
		t.Fatalf("partial provider id uniqueness after migration: %v", err)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "test.db") + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(filepath.Join("migrations", f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	if err := ensureLocalInstance(db); err != nil {
		t.Fatalf("seed local: %v", err)
	}
	return db
}

// TestEnsureLocal_Idempotent — OnMount runs at every sidecar start;
// the local row must survive the second seed call without dup-key
// errors or row duplication.
func TestEnsureLocal_Idempotent(t *testing.T) {
	db := openTestDB(t)
	if err := ensureLocalInstance(db); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	rows, _ := dbListInstances(db, "local", "")
	if len(rows) != 1 || rows[0].ID != 0 {
		t.Fatalf("expected one local row id=0, got %v", rows)
	}
	if rows[0].Status != "ready" {
		t.Errorf("local status = %q, want ready", rows[0].Status)
	}
}

// TestLocalInstance_Immutable — id=0 cannot be created or destroyed
// via the public path. Only ensureLocalInstance touches it.
func TestLocalInstance_Immutable(t *testing.T) {
	db := openTestDB(t)
	if _, err := dbCreateInstance(db, CreateInstanceInput{Name: "fake-local", Provider: "local"}); !errors.Is(err, ErrLocalInstanceImmutable) {
		t.Errorf("dbCreateInstance(provider=local) = %v, want ErrLocalInstanceImmutable", err)
	}
	if err := dbDeleteInstance(db, 0); !errors.Is(err, ErrLocalInstanceImmutable) {
		t.Errorf("dbDeleteInstance(0) = %v, want ErrLocalInstanceImmutable", err)
	}
	if err := dbUpdateInstance(db, 0, map[string]any{"status": "error"}); !errors.Is(err, ErrLocalInstanceImmutable) {
		t.Errorf("dbUpdateInstance(0) = %v, want ErrLocalInstanceImmutable", err)
	}
}

// TestCreateRemote_RoundTrip — basic CRUD on a hetzner-shaped row.
func TestCreateRemote_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	in := CreateInstanceInput{
		Name: "test-1", Provider: "hetzner", ProviderID: "12345",
		PublicIPv4: "1.2.3.4", Status: "provisioning",
		SSHUser: "root", SSHPrivateKey: "PRIV", SSHPublicKey: "PUB",
		SSHHost: "ssh.example.test", SSHPort: 22022,
		ResourcesJSON: `{"cpu":{"cores":2}}`,
		PortsJSON:     `{"22":22022}`,
	}
	created, err := dbCreateInstance(db, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Errorf("new instance got id=0 (collides with local)")
	}
	got, err := dbGetInstance(db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicIPv4 != "1.2.3.4" || got.Provider != "hetzner" || got.SSHHost != "ssh.example.test" || got.SSHPort != 22022 {
		t.Errorf("read back wrong: %+v", got)
	}
	if got.ResourcesJSON != `{"cpu":{"cores":2}}` || got.PortsJSON != `{"22":22022}` {
		t.Errorf("generic metadata wrong: resources=%q ports=%q", got.ResourcesJSON, got.PortsJSON)
	}
	// stripSecrets clears the private key but leaves the public.
	stripped := got.stripSecrets()
	if stripped.SSHPrivateKey != "" {
		t.Errorf("stripSecrets leaked private key")
	}
	if stripped.SSHPublicKey != "PUB" {
		t.Errorf("stripSecrets dropped public key")
	}

	// Update path: status transition.
	if err := dbUpdateInstance(db, created.ID, map[string]any{
		"status":   "ready",
		"ready_at": "2026-05-08T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	got2, _ := dbGetInstance(db, created.ID)
	if got2.Status != "ready" {
		t.Errorf("status after update = %q", got2.Status)
	}
}

// TestList_FiltersAndOrder — local first, filters work.
func TestList_FiltersAndOrder(t *testing.T) {
	db := openTestDB(t)
	dbCreateInstance(db, CreateInstanceInput{Name: "h1", Provider: "hetzner", ProviderID: "1", Status: "ready"})
	dbCreateInstance(db, CreateInstanceInput{Name: "h2", Provider: "hetzner", ProviderID: "2", Status: "provisioning"})

	all, _ := dbListInstances(db, "", "")
	if len(all) != 3 || all[0].ID != 0 {
		t.Fatalf("expected 3 (incl. local first), got %v", all)
	}
	hetzner, _ := dbListInstances(db, "hetzner", "")
	if len(hetzner) != 2 {
		t.Errorf("hetzner filter returned %d", len(hetzner))
	}
	ready, _ := dbListInstances(db, "", "ready")
	if len(ready) != 2 {
		t.Errorf("ready filter returned %d (expected 2: local + h1)", len(ready))
	}
}
