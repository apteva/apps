package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func tryCloneFile(src, dst string) bool {
	in, err := os.Open(src)
	if err != nil {
		return false
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return false
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return false
	}
	err = unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(dst)
		return false
	}
	return true
}
