package main

import "golang.org/x/sys/unix"

func publishTenantDirectory(stage, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
}
