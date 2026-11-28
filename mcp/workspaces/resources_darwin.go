package main

import "golang.org/x/sys/unix"

func machineMemoryMB() int {
	bytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int(bytes / (1 << 20))
}
