package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func initTestSSHCredentials(t *testing.T, db *sql.DB, dir string) {
	t.Helper()
	if err := initializeSSHCredentials(db, dir, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sshCredentialVaults.Delete(db) })
}

func newInstancesTestCtx(t *testing.T, manifest string, options ...tk.Option) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, manifest, options...)
	initTestSSHCredentials(t, ctx.AppDB(), t.TempDir())
	return ctx
}

func TestSSHCredentialsEncryptedRoundTrip(t *testing.T) {
	db := openTestDB(t)
	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journalMode); err != nil || journalMode != "wal" {
		t.Fatal("test requires an active WAL")
	}
	private, public, err := generateSSHKeypair()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := dbCreateInstance(db, CreateInstanceInput{Name: "host", Provider: "external", SSHPrivateKey: private, SSHPublicKey: public})
	if err != nil {
		t.Fatal(err)
	}
	if inst.SSHPrivateKey != private {
		t.Fatal("private key did not round trip")
	}
	var ref string
	if err := db.QueryRow(`SELECT ssh_private_key FROM instances WHERE id=?`, inst.ID).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if !validSSHCredentialRef(ref) {
		t.Fatal("database does not hold a credential reference")
	}
	v, _ := sshVault(db)
	sealed, err := os.ReadFile(filepath.Join(v.dir, strings.TrimPrefix(ref, sshCredentialPrefix)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(private)) || bytes.Contains(sealed, []byte("PRIVATE KEY")) {
		t.Fatal("plaintext key in vault file")
	}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("instance", "instance", inst)
	for _, value := range []any{inst, inst.stripSecrets(), CreateInstanceInput{SSHPrivateKey: private}, ovhOrderState{PrivateKey: private}} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte("PRIVATE KEY")) {
			t.Fatal("private key in JSON")
		}
		if strings.Contains(fmt.Sprintf("%+v %#v", value, value), "PRIVATE KEY") {
			t.Fatal("private key in formatted diagnostics")
		}
	}
	if strings.Contains(logs.String(), "PRIVATE KEY") {
		t.Fatal("private key in logs")
	}
	// Current DB pages and WALs never receive plaintext on new writes.
	var dbPath string
	var seq int
	var name string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &dbPath); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-journal"} {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("PRIVATE KEY")) {
			t.Fatal("plaintext key in database storage")
		}
	}
	// Updating credentials also encrypts; clearing them requires no vault read.
	other, _, _ := generateSSHKeypair()
	if err := dbUpdateInstance(db, inst.ID, map[string]any{"ssh_private_key": other}); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetInstance(db, inst.ID)
	if err != nil || fresh.SSHPrivateKey != other {
		t.Fatal("credential update did not round trip")
	}
	if err := dbUpdateInstance(db, inst.ID, map[string]any{"ssh_private_key": ""}); err != nil {
		t.Fatal(err)
	}
	fresh, err = dbGetInstance(db, inst.ID)
	if err != nil || fresh.SSHPrivateKey != "" {
		t.Fatal("credential was not cleared")
	}
}

func TestSSHCredentialsFailClosed(t *testing.T) {
	db := openTestDB(t)
	v, _ := sshVault(db)
	ref, err := v.put("instance:1", "test-private-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.get("instance:2", ref); err == nil {
		t.Fatal("credential accepted for another host")
	}
	for _, invalid := range []string{"plaintext-private-key", sshCredentialPrefix + "../../etc/passwd", "ssh-vault:v2:" + strings.Repeat("a", 64)} {
		if _, err := v.get("instance:1", invalid); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
	path := filepath.Join(v.dir, strings.TrimPrefix(ref, sshCredentialPrefix))
	sealed, _ := os.ReadFile(path)
	sealed[len(sealed)-1] ^= 1
	if err := os.WriteFile(path, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.get("instance:1", ref); err == nil {
		t.Fatal("tampered credential accepted")
	}
	sshCredentialVaults.Delete(db)
	if _, err := dbCreateInstance(db, CreateInstanceInput{Provider: "external", SSHPrivateKey: "test-private-key"}); err == nil {
		t.Fatal("plaintext fallback when vault unavailable")
	}
}

func TestSSHCredentialsRestartAndMissingKey(t *testing.T) {
	db := openTestDB(t)
	v, _ := sshVault(db)
	dir := filepath.Dir(v.dir)
	inst, err := dbCreateInstance(db, CreateInstanceInput{Provider: "external", SSHPrivateKey: "test-private-key"})
	if err != nil {
		t.Fatal(err)
	}
	sshCredentialVaults.Delete(db)
	if err := initializeSSHCredentials(db, dir, ""); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetInstance(db, inst.ID)
	if err != nil || fresh.SSHPrivateKey != "test-private-key" {
		t.Fatal("restart could not decrypt original credential")
	}
	keyPath := filepath.Join(dir, "ssh-credentials.key")
	key, _ := os.ReadFile(keyPath)
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := initializeSSHCredentials(db, dir, ""); err == nil {
		t.Fatal("lost master key was replaced")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("replacement key was created")
	}
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initializeSSHCredentials(db, dir, ""); err == nil {
		t.Fatal("wrong master key accepted")
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := initializeSSHCredentials(db, dir, ""); err != nil {
		t.Fatal(err)
	}
}

func TestSSHCredentialsExternalKeyAndPermissions(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "mounted-secret")
	if _, err := openSSHCredentialVault(db, dir, keyPath); err == nil {
		t.Fatal("missing external key was generated")
	}
	for _, key := range [][]byte{nil, bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{1}, 32)} {
		if err := os.WriteFile(keyPath, key, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := openSSHCredentialVault(db, dir, keyPath)
		if (err == nil) != (len(key) == 32) {
			t.Fatal("unexpected master key validation")
		}
	}
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := openSSHCredentialVault(db, dir, keyPath); err == nil {
		t.Fatal("world-readable master key accepted")
	}
}

func TestSSHCredentialsMigrateLegacy(t *testing.T) {
	db := openTestDB(t)
	v, _ := sshVault(db)
	dir := filepath.Dir(v.dir)
	// Simulate an upgraded DB with pre-migration plaintext, including both
	// known OVH copies. Existing triggers protect all subsequent writes.
	if _, err := db.Exec(`DROP TRIGGER instances_ssh_credential_insert; DROP TRIGGER instances_ssh_credential_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO instances(id,name,provider,ssh_private_key) VALUES(2,'legacy','external','legacy-instance-private-key')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_orders(id,provider,connection_id,request_key,input_json,state_json,created_at,updated_at) VALUES('legacy-order','ovhcloud',7,'legacy-request','{"SSHPrivateKey":"legacy-input-private-key"}','{"private_key":"legacy-order-private-key","phase":"quoted"}',?,?)`, nowUTC(), nowUTC()); err != nil {
		t.Fatal(err)
	}
	if err := initializeSSHCredentials(db, dir, ""); err != nil {
		t.Fatal(err)
	}
	inst, err := dbGetInstance(db, 2)
	if err != nil || inst.SSHPrivateKey != "legacy-instance-private-key" {
		t.Fatal("legacy instance key lost")
	}
	var input, state string
	if err := db.QueryRow(`SELECT input_json,state_json FROM provider_orders WHERE id='legacy-order'`).Scan(&input, &state); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input+state, "private-key") || strings.Contains(input+state, "private_key") {
		t.Fatal("legacy order key remains in DB")
	}
	var order ovhOrderState
	if err := json.Unmarshal([]byte(state), &order); err != nil {
		t.Fatal(err)
	}
	key, err := loadSSHCredential(db, orderCredentialOwner("legacy-order"), order.CredentialRef)
	if err != nil || key != "legacy-order-private-key" {
		t.Fatal("legacy order credential lost")
	}
	if err := initializeSSHCredentials(db, dir, ""); err != nil {
		t.Fatal("migration is not idempotent")
	}
	files, err := os.ReadDir(v.dir)
	if err != nil || len(files) != 3 {
		t.Fatal("repeated migration rewrote credentials")
	}
}

func TestSSHCredentialsRejectDirectPlaintextWrites(t *testing.T) {
	db := openTestDB(t)
	inst, err := dbCreateInstance(db, CreateInstanceInput{Provider: "external"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instances SET ssh_private_key=? WHERE id=?`, "private-key", inst.ID); err == nil {
		t.Fatal("direct plaintext update accepted")
	}
	if _, err := db.Exec(`INSERT INTO instances(id,name,provider,ssh_private_key) VALUES(10,'unsafe','external','private-key')`); err == nil {
		t.Fatal("direct plaintext insert accepted")
	}
}

func TestSSHCredentialsMigrationFailurePreservesLegacyRows(t *testing.T) {
	db := openTestDB(t)
	v, _ := sshVault(db)
	ref, err := v.put("instance:999", "wrong-owner-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER instances_ssh_credential_insert; INSERT INTO instances(id,name,provider,ssh_private_key) VALUES(1,'legacy','external','legacy-private-key')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO instances(id,name,provider,ssh_private_key) VALUES(2,'broken','external',?)`, ref); err != nil {
		t.Fatal(err)
	}
	if err := initializeSSHCredentials(db, filepath.Dir(v.dir), ""); err == nil {
		t.Fatal("migration accepted wrong-owner credential")
	}
	var stored string
	if err := db.QueryRow(`SELECT ssh_private_key FROM instances WHERE id=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "legacy-private-key" {
		t.Fatal("failed migration partially modified live rows")
	}
	if _, err := sshVault(db); err == nil {
		t.Fatal("failed migration left the credential vault available")
	}
}

func TestSSHCredentialsOVHOrderRoundTrip(t *testing.T) {
	ctx, _, in := newOVHTest(t, "vps")
	in.SSHPrivateKey = "input-private-key-must-not-be-persisted"
	order, err := ovhQuote(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var input, state string
	if err := ctx.AppDB().QueryRow(`SELECT input_json,state_json FROM provider_orders WHERE id=?`, order.ID).Scan(&input, &state); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input+state, "PRIVATE KEY") || strings.Contains(input+state, in.SSHPrivateKey) {
		t.Fatal("OVH persisted private key material")
	}
	if !validSSHCredentialRef(order.State.CredentialRef) {
		t.Fatal("OVH order has no credential reference")
	}
	loaded, err := ovhReadOrder(ctx, order.ID)
	if err != nil || loaded.State.PrivateKey == "" || loaded.State.PrivateKey != order.State.PrivateKey {
		t.Fatal("OVH order credential did not round trip")
	}
	if err := ovhSaveOrder(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	// Redaction must preserve the exact legacy digest representation, which
	// included an empty SSHPrivateKey field at its original struct position.
	clean := in
	clean.SSHPrivateKey = ""
	type legacyInput CreateInstanceInput
	before, _ := json.Marshal(legacyInput(clean))
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("serialization changed legacy input digest representation")
	}
}
