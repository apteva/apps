package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// Signing private keys are stored as "enc:v1:" + base64(nonce|ciphertext),
// sealed with AES-256-GCM under a key-encryption key (KEK) kept outside the
// database: AUTH_SIGNING_KEK (base64, 32 bytes) from the environment, or
// <DataDir>/auth-signing.kek created 0600 on first start. A copy of auth.db
// (backup, snapshot, export, SQL read access) no longer yields a key that can
// mint tokens. The KEK file sits next to the database on the same host, so
// operators who want separation set AUTH_SIGNING_KEK from a secret manager.
// The row's project, organization and kid are bound as associated data, so a
// sealed key cannot be swapped onto another organization's row.
const sealedKeyPrefix = "enc:v1:"

var (
	signingKEKMu sync.RWMutex
	signingKEK   cipher.AEAD
)

func setSigningKEK(aead cipher.AEAD) {
	signingKEKMu.Lock()
	signingKEK = aead
	signingKEKMu.Unlock()
}

func currentSigningKEK() cipher.AEAD {
	signingKEKMu.RLock()
	defer signingKEKMu.RUnlock()
	return signingKEK
}

func loadSigningKEK(ctx *sdk.AppCtx) (cipher.AEAD, error) {
	key, err := resolveSigningKEK(ctx)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func resolveSigningKEK(ctx *sdk.AppCtx) ([]byte, error) {
	if encoded := strings.TrimSpace(os.Getenv("AUTH_SIGNING_KEK")); encoded != "" {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("AUTH_SIGNING_KEK: %w", err)
		}
		if len(key) != 32 {
			return nil, errors.New("AUTH_SIGNING_KEK must decode to 32 bytes")
		}
		return key, nil
	}
	if ctx == nil || strings.TrimSpace(ctx.DataDir()) == "" {
		return nil, errors.New("signing-key encryption needs AUTH_SIGNING_KEK, APTEVA_DATA_DIR or DB_PATH")
	}
	if err := os.MkdirAll(ctx.DataDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(ctx.DataDir(), "auth-signing.kek")
	if existing, err := os.ReadFile(path); err == nil {
		if len(existing) != 32 {
			return nil, fmt.Errorf("auth-signing.kek has wrong size %d", len(existing))
		}
		return existing, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return resolveSigningKEK(ctx)
	}
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(key); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func signingKeyAD(pid string, oid int64, kid string) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("auth-signing-key|%s|%d|%s", pid, oid, kid)))
	return sum[:]
}

// sealSigningKey encrypts a PEM private key for storage. Without a KEK (unit
// tests that never mount the app) the PEM is stored as before.
func sealSigningKey(pid string, oid int64, kid, privPEM string) (string, error) {
	aead := currentSigningKEK()
	if aead == nil {
		return privPEM, nil
	}
	return sealSigningKeyWith(aead, pid, oid, kid, privPEM)
}

func sealSigningKeyWith(aead cipher.AEAD, pid string, oid int64, kid, privPEM string) (string, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(privPEM), signingKeyAD(pid, oid, kid))
	return sealedKeyPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

var errSigningKeySealed = errors.New("signing key is encrypted and cannot be opened")

func openSigningKey(pid string, oid int64, kid, stored string) (string, error) {
	if !strings.HasPrefix(stored, sealedKeyPrefix) {
		return stored, nil
	}
	aead := currentSigningKEK()
	if aead == nil {
		return "", errSigningKeySealed
	}
	return openSigningKeyWith(aead, pid, oid, kid, stored)
}

func openSigningKeyWith(aead cipher.AEAD, pid string, oid int64, kid, stored string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, sealedKeyPrefix))
	if err != nil || len(blob) < aead.NonceSize()+aead.Overhead() {
		return "", errSigningKeySealed
	}
	plain, err := aead.Open(nil, blob[:aead.NonceSize()], blob[aead.NonceSize():], signingKeyAD(pid, oid, kid))
	if err != nil {
		return "", errSigningKeySealed
	}
	return string(plain), nil
}

// sealStoredSigningKeys encrypts every plaintext private key in place, then
// checks each organization's active key opens. An active key that cannot be
// opened (the KEK file was lost or replaced) is replaced by an emergency
// rotation: users sign in again instead of Auth refusing to issue tokens.
func sealStoredSigningKeys(ctx *sdk.AppCtx) error {
	db := ctx.AppDB()
	rows, err := db.Query(`SELECT id, project_id, organization_id, kid, private_pem FROM signing_keys WHERE private_pem<>'' AND private_pem NOT LIKE 'enc:v1:%'`)
	if err != nil {
		return err
	}
	type plain struct {
		id, oid       int64
		pid, kid, pem string
	}
	var pending []plain
	for rows.Next() {
		var p plain
		if err := rows.Scan(&p.id, &p.pid, &p.oid, &p.kid, &p.pem); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range pending {
		sealed, err := sealSigningKey(p.pid, p.oid, p.kid, p.pem)
		if err != nil {
			return err
		}
		if _, err = db.Exec(`UPDATE signing_keys SET private_pem=? WHERE id=? AND private_pem=?`, sealed, p.id, p.pem); err != nil {
			return err
		}
	}
	if len(pending) > 0 {
		ctx.Logger().Info("auth: encrypted stored signing keys", "count", len(pending))
	}
	orgRows, err := db.Query(`SELECT project_id, id FROM organizations WHERE status='active'`)
	if err != nil {
		return err
	}
	type orgRef struct {
		pid string
		id  int64
	}
	var orgs []orgRef
	for orgRows.Next() {
		var o orgRef
		if err := orgRows.Scan(&o.pid, &o.id); err != nil {
			orgRows.Close()
			return err
		}
		orgs = append(orgs, o)
	}
	if err := orgRows.Close(); err != nil {
		return err
	}
	for _, org := range orgs {
		if _, _, err := dbActiveSigningKey(db, org.pid, org.id); errors.Is(err, errSigningKeySealed) {
			ctx.Logger().Error("auth: active signing key cannot be decrypted; rotating", "organization_id", org.id)
			if err := rotateSigningKey(db, org.pid, org.id, true, auditSource{ActorType: "system"}); err != nil {
				return fmt.Errorf("replace unreadable signing key: %w", err)
			}
		}
	}
	return nil
}
