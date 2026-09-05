package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

const maxLegacyBackupBytes int64 = 32 << 20

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("legacy backup exceeds 32 MiB; use streaming")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func preflightSnapshotSpace(src, dst string) error {
	var size uint64
	if err := filepath.Walk(src, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			size += uint64(info.Size())
		}
		return nil
	}); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(dst, &fs); err != nil {
		return err
	}
	free := uint64(fs.Bavail) * uint64(fs.Bsize)
	if free < size+(64<<20) {
		return fmt.Errorf("insufficient snapshot space: need %d bytes plus 64 MiB, available %d", size, free)
	}
	return nil
}
