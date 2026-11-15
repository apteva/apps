package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Run with INSTANCES_UPGRADE_MIGRATIONS pointing at a prior release's actual
// migrations for a release rehearsal. The default covers the shared 0.16.1 /
// 0.17.0 schema, which had identical migrations through 013.
func TestSSHCredentialsUpgradePreservesDataAndRestorableSnapshot(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "app.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sshCredentialVaults.Delete(db); db.Close() })
	oldMigrations := os.Getenv("INSTANCES_UPGRADE_MIGRATIONS")
	if oldMigrations == "" {
		oldMigrations = "migrations"
	}
	entries, err := os.ReadDir(oldMigrations)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") && e.Name() < "014" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) != 13 {
		t.Fatal("expected complete pre-encryption schema")
	}
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(oldMigrations, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureLocalInstance(db); err != nil {
		t.Fatal(err)
	}
	private, public, err := generateSSHKeypair()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{2, 7} {
		// Include reused legacy credentials: encryption must preserve access
		// while incident responders arrange rotation, never silently replace it.
		if _, err := db.Exec(`INSERT INTO instances(id,name,provider,status,ssh_user,ssh_host,ssh_port,ssh_private_key,ssh_public_key,tags_json,resources_json,provider_metadata_json,setup_json,monthly_cost_cents) VALUES(?,'legacy-host','external','ready','runner','host.test',2222,?,?,'["keep-me"]','{"cpu":4}','{"custom":"retained"}','{"phase":"complete"}',1234)`, id, private, public); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO instance_volumes(instance_id,provider,provider_volume_id,name,size_gb,status,mount_path) VALUES(2,'hetzner','volume-123','keep-volume',50,'attached','/srv/data'); CREATE TABLE release_preservation_probe(id INTEGER PRIMARY KEY, payload BLOB); INSERT INTO release_preservation_probe VALUES(1,x'000102ff')`); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(map[string]any{"private_key": private, "public_key": public, "phase": "delivery", "unknown_metadata": map[string]any{"keep": true}})
	input, _ := json.Marshal(map[string]any{"Name": "pending-order", "SSHPrivateKey": private, "unknown_input": "keep"})
	if _, err := db.Exec(`INSERT INTO provider_orders(id,provider,connection_id,request_key,instance_id,input_json,state_json,created_at,updated_at) VALUES('upgrade-order','ovhcloud',7,'request-123',2,?,?,?,?)`, string(input), string(state), nowUTC(), nowUTC()); err != nil {
		t.Fatal(err)
	}
	before := upgradeDatabaseRows(t, db, false)
	beforePublic := upgradeDatabaseRows(t, db, true)
	migration, err := os.ReadFile("migrations/014_ssh_credential_refs.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if err := initializeSSHCredentials(db, dir, ""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforePublic, upgradeDatabaseRows(t, db, true)) {
		t.Fatal("upgrade changed non-credential data")
	}
	for _, id := range []int64{2, 7} {
		inst, err := dbGetInstance(db, id)
		if err != nil || inst.SSHPrivateKey != private || inst.SSHPublicKey != public {
			t.Fatal("upgrade lost existing SSH identity")
		}
	}
	var migratedInput, migratedState string
	if err := db.QueryRow(`SELECT input_json,state_json FROM provider_orders WHERE id='upgrade-order'`).Scan(&migratedInput, &migratedState); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(migratedInput), &fields); err != nil || fields["unknown_input"] != "keep" {
		t.Fatal("order input metadata lost")
	}
	if err := json.Unmarshal([]byte(migratedState), &fields); err != nil || fields["unknown_metadata"] == nil {
		t.Fatal("order state metadata lost")
	}
	var snapshotRef string
	if err := db.QueryRow(`SELECT snapshot_ref FROM ssh_credential_migrations`).Scan(&snapshotRef); err != nil {
		t.Fatal(err)
	}
	v, _ := sshVault(db)
	snapshot, err := v.get(sshMigrationSnapshotOwner, snapshotRef)
	if err != nil {
		t.Fatal(err)
	}
	// Restore the encrypted SQLite image to a disposable DB and compare every
	// original table/value, including original keys and binary metadata.
	path := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(path, []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if !bytes.Equal(before, upgradeDatabaseRows(t, restored, false)) {
		t.Fatal("encrypted snapshot did not restore every original value")
	}
	var integrity string
	if err := restored.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("restored snapshot failed integrity check")
	}
	filesBefore, _ := os.ReadDir(v.dir)
	if err := initializeSSHCredentials(db, dir, ""); err != nil {
		t.Fatal(err)
	}
	filesAfter, _ := os.ReadDir(v.dir)
	if len(filesBefore) != len(filesAfter) {
		t.Fatal("restart created duplicate credentials or snapshots")
	}
}

func upgradeDatabaseRows(t *testing.T, db *sql.DB, publicOnly bool) []byte {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'ssh_credential_migrations' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	all := map[string][]map[string]any{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			value := map[string]any{}
			for i, col := range cols {
				if publicOnly && (table == "instances" && col == "ssh_private_key" || table == "provider_orders" && (col == "state_json" || col == "input_json")) {
					continue
				}
				value[col] = values[i]
			}
			all[table] = append(all[table], value)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	b, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSSHCredentialsUpgradeWriteFailureRollsBackAllRows(t *testing.T) {
	for _, failure := range []string{"snapshot", "second-row"} {
		t.Run(failure, func(t *testing.T) {
			db := openTestDB(t)
			v, _ := sshVault(db)
			if _, err := db.Exec(`DROP TRIGGER instances_ssh_credential_insert; INSERT INTO instances(id,name,provider,ssh_private_key) VALUES(1,'one','external','legacy-one'),(2,'two','external','legacy-two')`); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER simulate_write_failure BEFORE INSERT ON ssh_credential_migrations BEGIN SELECT RAISE(ABORT,'simulated snapshot write failure'); END`
			if failure == "second-row" {
				trigger = `CREATE TRIGGER simulate_write_failure BEFORE UPDATE OF ssh_private_key ON instances WHEN OLD.id=2 BEGIN SELECT RAISE(ABORT,'simulated second row write failure'); END`
			}
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			before := upgradeDatabaseRows(t, db, false)
			if err := initializeSSHCredentials(db, filepath.Dir(v.dir), ""); err == nil {
				t.Fatal("injected write failure was ignored")
			}
			if !bytes.Equal(before, upgradeDatabaseRows(t, db, false)) {
				t.Fatal("failed upgrade changed existing data")
			}
			var snapshots int
			if err := db.QueryRow(`SELECT count(*) FROM ssh_credential_migrations`).Scan(&snapshots); err != nil || snapshots != 0 {
				t.Fatal("failed upgrade committed partial snapshot metadata")
			}
			if _, err := sshVault(db); err == nil {
				t.Fatal("failed upgrade activated the vault")
			}
		})
	}
}
