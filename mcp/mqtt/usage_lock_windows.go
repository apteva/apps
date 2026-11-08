//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"os"
)

func lockBrokerFile(f *os.File) error {
	var offset windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &offset)
}
