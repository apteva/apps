//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

const nativeSnapshotsSupported = true

// A rolling app upgrade can briefly have two processes using the same backend
// database/cache. Kernel-owned locks survive neither a crash nor process exit.
func nativeStateFileLock(root string) (func(), error) {
	f, e := os.OpenFile(filepath.Join(root, ".state-operation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, e
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
