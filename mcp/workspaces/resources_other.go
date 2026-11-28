//go:build !darwin

package main

import "os"

func machineMemoryMB() int {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseMeminfoTotalMB(string(raw))
}
