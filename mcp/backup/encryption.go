package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	sdk "github.com/apteva/app-sdk"
)

func backupPassphrase(ctx *sdk.AppCtx) string {
	if ctx == nil {
		return ""
	}
	return ctx.Config().Get("encryption_passphrase")
}

func prepareStoredSnapshot(ctx *sdk.AppCtx, rawPath string, rawSize int64, rawSHA string) (path string, size int64, sha string, encrypted bool, cleanup func(), err error) {
	cleanup = func() {}
	passphrase := backupPassphrase(ctx)
	if passphrase == "" {
		return rawPath, rawSize, rawSHA, false, cleanup, nil
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return "", 0, "", false, cleanup, err
	}
	in, err := os.Open(rawPath)
	if err != nil {
		return "", 0, "", false, cleanup, err
	}
	defer in.Close()
	out, err := os.CreateTemp("", "apteva-snapshot-*.tar.gz.age")
	if err != nil {
		return "", 0, "", false, cleanup, err
	}
	path = out.Name()
	cleanup = func() { _ = os.Remove(path) }
	hash := sha256.New()
	encryptedWriter, err := age.Encrypt(io.MultiWriter(out, hash), recipient)
	if err == nil {
		if _, err = io.Copy(encryptedWriter, in); err == nil {
			err = encryptedWriter.Close()
		} else {
			_ = encryptedWriter.Close()
		}
	}
	if syncErr := out.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return "", 0, "", false, func() {}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		cleanup()
		return "", 0, "", false, func() {}, err
	}
	return path, info.Size(), hex.EncodeToString(hash.Sum(nil)), true, cleanup, nil
}

func decryptStoredSnapshot(ctx *sdk.AppCtx, encryptedPath string) (string, func(), error) {
	passphrase := backupPassphrase(ctx)
	if passphrase == "" {
		return "", func() {}, fmt.Errorf("backup is encrypted but encryption_passphrase is not configured")
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return "", func() {}, err
	}
	in, err := os.Open(encryptedPath)
	if err != nil {
		return "", func() {}, err
	}
	defer in.Close()
	reader, err := age.Decrypt(in, identity)
	if err != nil {
		return "", func() {}, fmt.Errorf("decrypt backup: %w", err)
	}
	out, err := os.CreateTemp("", "apteva-restored-*.tar.gz")
	if err != nil {
		return "", func() {}, err
	}
	path := out.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := io.Copy(out, reader); err != nil {
		_ = out.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("decrypt backup: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := out.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}
