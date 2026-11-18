package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const sshCredentialPrefix = "ssh-vault:v1:"
const sshCredentialKeyEnv = "APTEVA_INSTANCES_SSH_KEY_FILE"
const sshMigrationSnapshotOwner = "migration:pre-ssh-vault:v1"

// The database holds opaque references, never private keys. Each vault file is
// AES-256-GCM encrypted and authenticated for its reference and owning record.
// The master key can be mounted separately from app data using the environment
// override; the SDK currently has no secrets-store API.
type sshCredentialVault struct {
	dir  string
	aead cipher.AEAD
}

var sshCredentialVaults sync.Map // *sql.DB -> *sshCredentialVault

func sshVault(db *sql.DB) (*sshCredentialVault, error) {
	v, ok := sshCredentialVaults.Load(db)
	if !ok {
		return nil, errors.New("SSH credential vault is not initialized")
	}
	return v.(*sshCredentialVault), nil
}

func validSSHCredentialRef(ref string) bool {
	if !strings.HasPrefix(ref, sshCredentialPrefix) {
		return false
	}
	id := strings.TrimPrefix(ref, sshCredentialPrefix)
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 32 && id == strings.ToLower(id)
}

func openSSHCredentialVault(db *sql.DB, dataDir, keyPath string) (*sshCredentialVault, error) {
	if dataDir == "" {
		return nil, errors.New("SSH credential vault requires a persistent data directory")
	}
	dir := filepath.Join(dataDir, "ssh-credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("create SSH credential vault directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("SSH credential vault directory must be a private directory (0700)")
	}
	if err := syncSSHCredentialDirectory(dataDir); err != nil {
		return nil, err
	}
	externalKey := keyPath != ""
	if !externalKey {
		keyPath = filepath.Join(dataDir, "ssh-credentials.key")
	}
	info, err = os.Lstat(keyPath)
	if errors.Is(err, os.ErrNotExist) && !externalKey {
		// Never silently replace a lost key, even if only the DB was restored.
		var references int
		if err := db.QueryRow(`SELECT (SELECT count(*) FROM instances WHERE ssh_private_key LIKE 'ssh-vault:%') + (SELECT count(*) FROM provider_orders WHERE state_json LIKE '%ssh_credential_ref%') + (SELECT count(*) FROM ssh_credential_migrations)`).Scan(&references); err != nil {
			return nil, errors.New("inspect existing SSH credential references")
		}
		files, err := os.ReadDir(dir)
		if err != nil || references != 0 || len(files) != 0 {
			return nil, errors.New("SSH credential master key is missing; restore the original key")
		}
		key := make([]byte, 32)
		defer clear(key)
		if _, err := rand.Read(key); err != nil {
			return nil, errors.New("generate SSH credential master key")
		}
		// Exclusive creation prevents replacing another process's master key.
		f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.Write(key)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				return nil, errors.New("persist SSH credential master key")
			}
			if err := syncSSHCredentialDirectory(filepath.Dir(keyPath)); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return nil, errors.New("create SSH credential master key")
		}
	}
	info, err = os.Lstat(keyPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("SSH credential master key must be an existing private regular file (0600)")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil || len(key) != 32 {
		return nil, errors.New("SSH credential master key must contain exactly 32 raw bytes")
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, errors.New("initialize SSH credential cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize SSH credential cipher")
	}
	return &sshCredentialVault{dir: dir, aead: aead}, nil
}

func (v *sshCredentialVault) put(owner, privateKey string) (string, error) {
	if privateKey == "" {
		return "", nil
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return "", errors.New("generate SSH credential reference")
	}
	ref := sshCredentialPrefix + hex.EncodeToString(id)
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("generate SSH credential nonce")
	}
	plain := []byte(privateKey)
	sealed := v.aead.Seal(nonce, nonce, plain, []byte(ref+"\n"+owner))
	clear(plain)
	f, err := os.OpenFile(filepath.Join(v.dir, hex.EncodeToString(id)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("create encrypted SSH credential")
	}
	_, err = f.Write(sealed)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return "", errors.New("persist encrypted SSH credential")
	}
	// Persist the directory entry before a DB reference can be committed.
	if err := syncSSHCredentialDirectory(v.dir); err != nil {
		return "", err
	}
	return ref, nil
}

func syncSSHCredentialDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return errors.New("open SSH credential vault directory")
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return errors.New("persist SSH credential vault directory")
	}
	return nil
}

func (v *sshCredentialVault) get(owner, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if !validSSHCredentialRef(ref) {
		return "", errors.New("invalid SSH credential reference; plaintext reads are disabled")
	}
	path := filepath.Join(v.dir, strings.TrimPrefix(ref, sshCredentialPrefix))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("encrypted SSH credential is missing or has unsafe permissions")
	}
	sealed, err := os.ReadFile(path)
	if err != nil || len(sealed) < v.aead.NonceSize()+v.aead.Overhead() {
		return "", errors.New("invalid encrypted SSH credential")
	}
	plain, err := v.aead.Open(nil, sealed[:v.aead.NonceSize()], sealed[v.aead.NonceSize():], []byte(ref+"\n"+owner))
	if err != nil {
		return "", errors.New("SSH credential authentication failed")
	}
	defer clear(plain)
	return string(plain), nil
}

func storeSSHCredential(db *sql.DB, owner, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	v, err := sshVault(db)
	if err != nil {
		return "", err
	}
	return v.put(owner, key)
}

func loadSSHCredential(db *sql.DB, owner, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	v, err := sshVault(db)
	if err != nil {
		return "", err
	}
	return v.get(owner, ref)
}

// Deletes one vault entry. A missing entry is already removed.
func removeSSHCredential(db *sql.DB, ref string) error {
	if ref == "" {
		return nil
	}
	if !validSSHCredentialRef(ref) {
		return errors.New("invalid SSH credential reference")
	}
	v, err := sshVault(db)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(v.dir, strings.TrimPrefix(ref, sshCredentialPrefix)))
	if err != nil && !os.IsNotExist(err) {
		return errors.New("remove encrypted SSH credential")
	}
	return nil
}

func instanceCredentialOwner(id int64) string { return fmt.Sprintf("instance:%d", id) }
func orderCredentialOwner(id string) string   { return "ovh-order:" + id }

// Migration deliberately leaves old pages, WALs and backups untouched: incident
// evidence must be preserved and old authorized keys must still be rotated.
// All live rows change in one transaction, after durable encrypted files exist.
func initializeSSHCredentials(db *sql.DB, dataDir, keyPath string) error {
	sshCredentialVaults.Delete(db)
	v, err := openSSHCredentialVault(db, dataDir, keyPath)
	if err != nil {
		return err
	}
	if err := migrateSSHCredentials(db, v); err != nil {
		return err
	}
	sshCredentialVaults.Store(db, v)
	return nil
}

func migrateSSHCredentials(db *sql.DB, v *sshCredentialVault) error {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type change struct {
		id    any
		value string
		query string
	}
	var changes []change
	rows, err := tx.Query(`SELECT id, ssh_private_key FROM instances WHERE ssh_private_key != ''`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var key string
		if err = rows.Scan(&id, &key); err != nil {
			break
		}
		owner := instanceCredentialOwner(id)
		if strings.HasPrefix(key, "ssh-vault:") {
			_, err = v.get(owner, key)
		} else {
			var ref string
			ref, err = v.put(owner, key)
			if err == nil {
				changes = append(changes, change{id: id, value: ref, query: `UPDATE instances SET ssh_private_key=? WHERE id=?`})
			}
		}
		if err != nil {
			break
		}
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil || readErr != nil {
		return errors.New("migrate instance SSH credentials failed")
	}
	rows, err = tx.Query(`SELECT id, state_json, input_json FROM provider_orders`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, state, input string
		if err = rows.Scan(&id, &state, &input); err != nil {
			break
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal([]byte(state), &fields); err != nil {
			break
		}
		var key, ref string
		if raw, ok := fields["private_key"]; ok {
			if err = json.Unmarshal(raw, &key); err != nil {
				break
			}
		}
		if raw, ok := fields["ssh_credential_ref"]; ok {
			if err = json.Unmarshal(raw, &ref); err != nil {
				break
			}
		}
		if ref != "" {
			_, err = v.get(orderCredentialOwner(id), ref)
		} else if key != "" {
			ref, err = v.put(orderCredentialOwner(id), key)
		}
		if err != nil {
			break
		}
		if _, legacy := fields["private_key"]; legacy {
			delete(fields, "private_key")
			fields["ssh_credential_ref"], _ = json.Marshal(ref)
			var encoded []byte
			encoded, err = json.Marshal(fields)
			if err != nil {
				break
			}
			changes = append(changes, change{id: id, value: string(encoded), query: `UPDATE provider_orders SET state_json=? WHERE id=?`})
		}
		// Historical CreateInstanceInput JSON could also include the key.
		var in map[string]json.RawMessage
		if err = json.Unmarshal([]byte(input), &in); err != nil {
			break
		}
		_, hasUpperKey := in["SSHPrivateKey"]
		_, hasLowerKey := in["ssh_private_key"]
		if hasUpperKey || hasLowerKey {
			delete(in, "SSHPrivateKey")
			delete(in, "ssh_private_key")
			encoded, _ := json.Marshal(in)
			// Queue separately; no writes while the rows cursor is active.
			changes = append(changes, change{id: id, value: string(encoded), query: `UPDATE provider_orders SET input_json=? WHERE id=?`})
		}
	}
	readErr = rows.Err()
	rows.Close()
	if err != nil || readErr != nil {
		return errors.New("migrate order SSH credentials failed")
	}
	if len(changes) != 0 {
		// Serialize the same SQLite connection/read transaction, including WAL
		// data. No plaintext backup ever reaches disk. Refuse migration unless
		// the complete snapshot has been encrypted, synced and authenticated.
		var snapshot []byte
		err = conn.Raw(func(driverConn any) error {
			serializer, ok := driverConn.(interface{ Serialize() ([]byte, error) })
			if !ok {
				return errors.New("SQLite snapshot serialization is unavailable")
			}
			var err error
			snapshot, err = serializer.Serialize()
			return err
		})
		if err != nil || len(snapshot) < 100 || string(snapshot[:16]) != "SQLite format 3\x00" {
			clear(snapshot)
			return errors.New("create pre-migration SSH credential database snapshot")
		}
		ref, err := v.put(sshMigrationSnapshotOwner, string(snapshot))
		clear(snapshot)
		if err != nil {
			return err
		}
		if _, err := v.get(sshMigrationSnapshotOwner, ref); err != nil {
			return errors.New("verify pre-migration SSH credential database snapshot")
		}
		if _, err := tx.Exec(`INSERT INTO ssh_credential_migrations(snapshot_ref,created_at) VALUES(?,?)`, ref, nowUTC()); err != nil {
			return err
		}
	}
	for _, c := range changes {
		_, err = tx.Exec(c.query, c.value, c.id)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
