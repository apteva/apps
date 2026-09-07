//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func hostMemoryLimitMB() int64 {
	var limit int64
	if b, e := os.ReadFile("/proc/meminfo"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) > 1 && f[0] == "MemTotal:" {
				v, _ := strconv.ParseInt(f[1], 10, 64)
				limit = v / 1024
			}
		}
	}
	// Include every enclosing cgroup limit, not just the host's physical RAM.
	if b, e := os.ReadFile("/proc/self/cgroup"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "0::") {
				dir := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
				for {
					if n := readIntFile(filepath.Join(dir, "memory.max")); n > 0 && (limit == 0 || n>>20 < limit) {
						limit = n >> 20
					}
					if dir == "/sys/fs/cgroup" || dir == "/" {
						break
					}
					dir = filepath.Dir(dir)
				}
			}
		}
	}
	if n := readIntFile("/sys/fs/cgroup/memory.max"); n > 0 && (limit == 0 || n>>20 < limit) {
		limit = n >> 20
	}
	return limit
}
func workerMemory(w *worker) (*int64, string, int64) {
	if w == nil || w.cmd == nil || w.cmd.Process == nil {
		return nil, "unavailable", 0
	}
	pid := w.cmd.Process.Pid
	root := os.Getenv("APTEVA_FUNCTIONS_CGROUP_ROOT")
	if root == "" {
		root = "/sys/fs/cgroup/apteva-functions"
	}
	dir := filepath.Join(root, "worker-"+strconv.Itoa(pid))
	var oom int64
	if b, e := os.ReadFile(filepath.Join(dir, "memory.events")); e == nil {
		f := strings.Fields(string(b))
		for i := 0; i+1 < len(f); i += 2 {
			if f[i] == "oom_kill" {
				oom, _ = strconv.ParseInt(f[i+1], 10, 64)
			}
		}
	}
	if n := readIntFile(filepath.Join(dir, "memory.current")); n >= 0 {
		return &n, "cgroup_v2", oom
	}
	if b, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) > 1 && f[0] == "VmRSS:" {
				n, _ := strconv.ParseInt(f[1], 10, 64)
				n *= 1024
				return &n, "process_rss_no_descendants", oom
			}
		}
	}
	return nil, "unavailable", oom
}
