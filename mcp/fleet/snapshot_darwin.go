package main

import "golang.org/x/sys/unix"

func tryCloneFile(src, dst string) bool { return unix.Clonefile(src, dst, 0) == nil }
