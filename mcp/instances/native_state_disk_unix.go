//go:build linux || darwin

package main

import "syscall"

func nativeDiskAvailable(path string) (int64, error) {
	var s syscall.Statfs_t
	if e := syscall.Statfs(path, &s); e != nil {
		return 0, e
	}
	return int64(s.Bavail) * int64(s.Bsize), nil
}
