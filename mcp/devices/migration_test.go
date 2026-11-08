package main

import (
	"database/sql"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradeAndRestartPreserveDataAndRecovery(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "migration-token")
	t.Setenv("APTEVA_PROJECT_ID", "migration-scope")
	path := filepath.Join(t.TempDir(), "devices.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(on)&_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, name := range []string{"001_init.sql", "002_native_protocol.sql"} {
		raw, e := os.ReadFile(filepath.Join("migrations", name))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	_, err = db.Exec(`INSERT INTO devices(id,project_id,name,mqtt_username)VALUES('old','migration-scope','Old device','old')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO device_state(device_id,key,value_json,updated_at)VALUES('old','temperature','23','2026-11-01T00:00:00.000000000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO device_telemetry(device_id,payload_json,received_at)VALUES('old','{"temperature":23}','2026-11-01T00:00:00.000000000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/003_reliability.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	manifest, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		t.Fatal(err)
	}
	platform := &mqttStub{}
	ctx := sdk.NewAppCtxForTest(manifest, db, sdk.Config{}, platform, nil).WithProject("migration-scope")
	ctx.SetEmitter(tk.NewEmitRecorder())
	app := &App{}
	if err = app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := getDevice(db, "migration-scope", "old", true)
	if err != nil || old.Enabled || old.Status != "requires_enrollment" || len(old.State) != 1 || old.MQTTUsername != app.wireUsername("old") {
		t.Fatalf("upgraded=%#v %v", old, err)
	}
	history, err := app.queryHistory("data", map[string]any{"device_id": "old"})
	if err != nil || len(history.Items) != 1 {
		t.Fatalf("legacy history=%#v %v", history, err)
	}
	// Simulate process death after MQTT changed but before the local commit.
	_, err = db.Exec(`CREATE TRIGGER fail_device BEFORE INSERT ON devices BEGIN SELECT RAISE(ABORT,'crash');END`)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": "new", "device_id": "new", "idempotency_key": "restart"}
	pending, err := app.provision(args)
	if err != nil || pending["status"] != "pending" {
		t.Fatalf("pending=%#v %v", pending, err)
	}
	initial := platform.callsFor("mqtt_users_create")
	password := str(initial[len(initial)-1].Input, "password")
	_, _ = db.Exec(`DROP TRIGGER fail_device`)
	restarted := &App{}
	if err = restarted.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if err = restarted.reconcileOperations(); err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.provision(args)
	if err != nil || completed["status"] != "completed" || completed["mqtt"].(map[string]any)["password"] != password {
		t.Fatalf("restart=%#v %v", completed, err)
	}
	keyPath := filepath.Join(filepath.Dir(path), "devices-recovery.key")
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("recovery key permissions: %v %v", info, err)
	}
	_, _ = db.Exec(`UPDATE device_operations SET secret_expires_at='2000-01-01T00:00:00.000000000Z'`)
	expired, err := restarted.operationResult(completed["operation_id"].(string))
	if err != nil || expired["credential_recovery_expired"] != true || expired["password"] != nil {
		t.Fatalf("expired=%#v %v", expired, err)
	}
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err = (&App{}).OnMount(ctx); err == nil {
		t.Fatal("missing backup key silently replaced")
	}
}
