package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	sdk "github.com/apteva/app-sdk"
)

type decryptedReadCloser struct {
	io.Reader
	io.Closer
}

type countWriter struct{ n int64 }

func (w *countWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

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

// putStoredSnapshot streams validated plaintext directly to the destination,
// encrypting through a pipe when configured. This avoids keeping raw and
// encrypted copies on the local disk at the same time.
func putStoredSnapshot(opCtx context.Context, ctx *sdk.AppCtx, destination Destination_writer, key, rawPath string, rawSize int64, rawSHA string) (size int64, sha string, encrypted bool, err error) {
	passphrase := backupPassphrase(ctx)
	if passphrase == "" {
		in, openErr := os.Open(rawPath)
		if openErr != nil {
			return 0, "", false, openErr
		}
		defer in.Close()
		if putErr := destination.Put(opCtx, key, in, rawSize); putErr != nil {
			return 0, "", false, putErr
		}
		return rawSize, rawSHA, false, nil
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return 0, "", false, err
	}
	in, err := os.Open(rawPath)
	if err != nil {
		return 0, "", false, err
	}
	pipeReader, pipeWriter := io.Pipe()
	type streamResult struct {
		size int64
		sha  string
		err  error
	}
	result := make(chan streamResult, 1)
	go func() {
		defer in.Close()
		hash := sha256.New()
		counter := &countWriter{}
		encryptedWriter, encryptErr := age.Encrypt(io.MultiWriter(pipeWriter, hash, counter), recipient)
		if encryptErr == nil {
			_, encryptErr = io.Copy(encryptedWriter, contextReader{ctx: opCtx, r: in})
			if closeErr := encryptedWriter.Close(); encryptErr == nil {
				encryptErr = closeErr
			}
		}
		if encryptErr != nil {
			_ = pipeWriter.CloseWithError(encryptErr)
		} else {
			_ = pipeWriter.Close()
		}
		result <- streamResult{size: counter.n, sha: hex.EncodeToString(hash.Sum(nil)), err: encryptErr}
	}()
	putErr := destination.Put(opCtx, key, pipeReader, -1)
	if putErr != nil {
		_ = pipeReader.CloseWithError(putErr)
	} else {
		_ = pipeReader.Close()
	}
	streamed := <-result
	if putErr != nil {
		return streamed.size, streamed.sha, true, putErr
	}
	if streamed.err != nil {
		return streamed.size, streamed.sha, true, streamed.err
	}
	return streamed.size, streamed.sha, true, nil
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

// openDecryptedSnapshot avoids materialising a second plaintext archive during
// restore. The stored encrypted object has already been downloaded and
// SHA-verified before this is called, so streaming preserves verify-before-
// restore semantics while cutting temporary disk usage roughly in half.
func openDecryptedSnapshot(ctx *sdk.AppCtx, encryptedPath string) (io.ReadCloser, error) {
	passphrase := backupPassphrase(ctx)
	if passphrase == "" {
		return nil, fmt.Errorf("backup is encrypted but encryption_passphrase is not configured")
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	in, err := os.Open(encryptedPath)
	if err != nil {
		return nil, err
	}
	reader, err := age.Decrypt(in, identity)
	if err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("decrypt backup: %w", err)
	}
	return &decryptedReadCloser{Reader: reader, Closer: in}, nil
}
