package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	sdk "github.com/apteva/app-sdk"
)

const videoStagingReserveBytes uint64 = 16 << 20

func sourceErrorCode(err error) string {
	var source *sourceDeliveryError
	if errors.As(err, &source) && source.Code != "" {
		return source.Code
	}
	return "source_delivery"
}

func isSourceDeliveryFailure(code string) bool {
	switch code {
	case "source_delivery", "local_staging_full", "local_staging_unavailable", "source_truncated", "source_changed", "checksum_mismatch":
		return true
	}
	return false
}

func localStagingError(err error) error {
	code := "local_staging_unavailable"
	reason := "the install's video staging directory could not be written"
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		code = "local_staging_full"
		reason = "the install's video staging filesystem has insufficient space"
	}
	return &sourceDeliveryError{Code: code, Reason: reason}
}

func (a *App) videoStagingDirectory(ctx *sdk.AppCtx, size int64) (string, error) {
	root := ctx.DataDir()
	if root == "" {
		return "", &sourceDeliveryError{Code: "local_staging_unavailable", Reason: "the install needs a persistent data directory for video staging"}
	}
	directory := filepath.Join(root, "video-staging")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", localStagingError(err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", &sourceDeliveryError{Code: "local_staging_unavailable", Reason: "the install's video staging path must be a directory"}
	}
	probe := a.stagingSpace
	if probe == nil {
		probe = videoStagingSpace
	}
	available, memoryBacked, err := probe(directory)
	if err != nil {
		return "", localStagingError(err)
	}
	if memoryBacked {
		return "", &sourceDeliveryError{Code: "local_staging_unavailable", Reason: "video staging requires a disk-backed install data directory"}
	}
	if available < uint64(max(size, 0))+videoStagingReserveBytes {
		return "", &sourceDeliveryError{Code: "local_staging_full", Reason: "the install's video staging filesystem has insufficient space"}
	}
	return directory, nil
}

// Track destination writes independently from the response reader. A local
// write failure must never trigger another network route.
type videoStagingWriter struct {
	writer io.Writer
	err    error
}

func (w *videoStagingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

func stageVideoBody(destination io.Writer, source io.Reader, limit int64) (int64, string, error) {
	digest := sha256.New()
	writer := &videoStagingWriter{writer: destination}
	n, err := io.Copy(io.MultiWriter(writer, digest), io.LimitReader(source, limit+1))
	if writer.err != nil {
		return n, "", localStagingError(writer.err)
	}
	if err != nil {
		return n, "", &sourceDeliveryError{Code: "source_truncated", Reason: "source download ended before its declared size", Transient: true}
	}
	return n, hex.EncodeToString(digest.Sum(nil)), nil
}
