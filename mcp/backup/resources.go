package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const diskReserve = int64(512 << 20)
const platformArchiveLimit = int64(64 << 30)
const platformExpandedLimit = int64(256 << 30)
const platformFileLimit = int64(128 << 30)
const platformEntryLimit = 10000

func requireDiskSpace(path string, additional int64) error {
	free, err := diskAvailable(path)
	if err != nil {
		return err
	}
	return checkDiskSpace(free, additional)
}
func checkDiskSpace(free, additional int64) error {
	if additional < 0 || free < diskReserve || additional > free-diskReserve {
		return fmt.Errorf("backup needs %d bytes plus %d bytes disk reserve; %d available", additional, diskReserve, free)
	}
	return nil
}

type archiveDiskWriter struct {
	ctx              context.Context
	out              io.Writer
	path             string
	remaining, since int64
}

func (w *archiveDiskWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("snapshot exceeds compressed archive limit")
	}
	if w.since == 0 || w.since >= 4<<20 {
		if err := requireDiskSpace(w.path, int64(len(p))+4<<20); err != nil {
			return 0, err
		}
		w.since = 0
	}
	n, err := w.out.Write(p)
	w.remaining -= int64(n)
	w.since += int64(n)
	return n, err
}

// age deliberately spends 256 MiB on passphrase derivation. Keep its security
// setting; fail before allocating if a Linux host/container lacks headroom.
func requireEncryptionMemory() error { return requireMemoryHeadroom(384 << 20) }
func requireMemoryHeadroom(required int64) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	return checkMemoryHeadroom(encryptionMemoryAvailable(os.ReadFile), required)
}
func encryptionMemoryAvailable(readFile func(string) ([]byte, error)) int64 {
	available := int64(-1)
	if b, err := readFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "MemAvailable:" {
				n, e := strconv.ParseInt(f[1], 10, 64)
				if e == nil {
					available = n * 1024
				}
			}
		}
	}
	// Resolve the process's actual cgroup, including nested service/container
	// groups. Inspect every ancestor because its limit may be the tighter one.
	if b, err := readFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.SplitN(line, ":", 3)
			if len(f) != 3 {
				continue
			}
			root, limitName, usageName := "/sys/fs/cgroup", "memory.max", "memory.current"
			if f[1] != "" {
				if !strings.Contains(","+f[1]+",", ",memory,") {
					continue
				}
				root, limitName, usageName = "/sys/fs/cgroup/memory", "memory.limit_in_bytes", "memory.usage_in_bytes"
			}
			for path := filepath.Join(root, filepath.Clean("/"+f[2])); ; path = filepath.Dir(path) {
				l, le := readFile(filepath.Join(path, limitName))
				u, ue := readFile(filepath.Join(path, usageName))
				limit, e1 := strconv.ParseInt(strings.TrimSpace(string(l)), 10, 64)
				used, e2 := strconv.ParseInt(strings.TrimSpace(string(u)), 10, 64)
				if stat, e := readFile(filepath.Join(path, "memory.stat")); e == nil {
					for _, line := range strings.Split(string(stat), "\n") {
						f := strings.Fields(line)
						if len(f) == 2 && (f[0] == "inactive_file" || f[0] == "total_inactive_file") {
							n, e := strconv.ParseInt(f[1], 10, 64)
							if e == nil && n > 0 && n <= used {
								used -= n
							}
							break
						}
					}
				}
				if le == nil && ue == nil && e1 == nil && e2 == nil && (available < 0 || max(0, limit-used) < available) {
					available = max(0, limit-used)
				}
				if path == root || filepath.Dir(path) == path {
					break
				}
			}
		}
	}
	return available
}
func checkEncryptionMemory(available int64) error { return checkMemoryHeadroom(available, 384<<20) }
func checkMemoryHeadroom(available, required int64) error {
	if available < 0 {
		return fmt.Errorf("cannot determine available memory for passphrase encryption")
	}
	if available < required {
		return fmt.Errorf("backup requires %d bytes available memory (including encryption/upload buffers and headroom); %d available", required, available)
	}
	return nil
}
