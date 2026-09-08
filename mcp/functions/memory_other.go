//go:build !linux

package main

// Linux cgroups are the authoritative production measurement. Do not report
// zero for unsupported platforms or spawn ps processes on the hot path.
func hostMemoryLimitMB() int64                       { return 0 }
func hostMemoryAvailableMB() int64                   { return -1 }
func workerMemory(w *worker) (*int64, string, int64) { return nil, "unavailable", 0 }
