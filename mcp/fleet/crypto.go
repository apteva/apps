package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	sdk "github.com/apteva/app-sdk"
)

// keyring wraps an AES-GCM AEAD bound to the fleet master key. We
// encrypt tenant api_keys at rest with this; the master key itself
// lives in env (FLEET_MASTER_KEY) or in the app's data dir.
type keyring struct{ aead cipher.AEAD }

// loadKeyring resolves the fleet master key in this order:
//   1. FLEET_MASTER_KEY env var (base64-encoded 32 bytes)
//   2. <DataDir>/master.key (auto-created with 32 random bytes on first run)
//
// The on-disk path is only as safe as the parent host's filesystem.
// Operators wanting HSM/KMS-backed keys set FLEET_MASTER_KEY from a
// secret manager and never let the on-disk path get touched.
func loadKeyring(ctx *sdk.AppCtx) (*keyring, error) {
	key, err := resolveMasterKey(ctx)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm init: %w", err)
	}
	return &keyring{aead: gcm}, nil
}

func resolveMasterKey(ctx *sdk.AppCtx) ([]byte, error) {
	if env := os.Getenv("FLEET_MASTER_KEY"); env != "" {
		key, err := base64.StdEncoding.DecodeString(env)
		if err != nil {
			return nil, fmt.Errorf("FLEET_MASTER_KEY: %w", err)
		}
		if len(key) != 32 {
			return nil, errors.New("FLEET_MASTER_KEY must decode to 32 bytes")
		}
		return key, nil
	}
	path := filepath.Join(ctx.DataDir(), "master.key")
	if existing, err := os.ReadFile(path); err == nil {
		if len(existing) != 32 {
			return nil, fmt.Errorf("master.key has wrong size %d", len(existing))
		}
		return existing, nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(ctx.DataDir(), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	ctx.Logger().Info("fleet: generated new master.key", "path", path)
	return key, nil
}

func (k *keyring) seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, plain, nil), nil
}

func (k *keyring) open(blob []byte) ([]byte, error) {
	ns := k.aead.NonceSize()
	if len(blob) < ns+k.aead.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	return k.aead.Open(nil, blob[:ns], blob[ns:], nil)
}
