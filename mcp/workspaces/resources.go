package main

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// SharedResources is what a local workspace actually shares with Apteva: the
// local driver enforces no CPU or memory limit, so a workspace can use up to
// whatever the surrounding container (its cgroup limits) or machine has.
type SharedResources struct {
	CPU      float64 `json:"cpu"`
	MemoryMB int     `json:"memory_mb"`
	// Source is "container" when a cgroup limit set at least one number, else
	// "machine".
	Source string `json:"source"`
}

var (
	sharedResourcesOnce  sync.Once
	sharedResourcesValue SharedResources
	// probeSharedResources is swappable for tests.
	probeSharedResources = readSharedResources
)

func sharedResources() SharedResources {
	sharedResourcesOnce.Do(func() { sharedResourcesValue = probeSharedResources() })
	return sharedResourcesValue
}

func resetSharedResources() {
	sharedResourcesOnce = sync.Once{}
}

func readSharedResources() SharedResources {
	res := SharedResources{CPU: float64(runtime.NumCPU()), MemoryMB: machineMemoryMB(), Source: "machine"}
	if cpu, ok := cgroupCPULimit(); ok && cpu < res.CPU {
		res.CPU, res.Source = cpu, "container"
	}
	if mem, ok := cgroupMemoryLimitMB(); ok && (res.MemoryMB == 0 || mem < res.MemoryMB) {
		res.MemoryMB, res.Source = mem, "container"
	}
	return res
}

func readTrimmed(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

// cgroupCPULimit reads cgroup v2 cpu.max, then v1 cfs quota/period.
func cgroupCPULimit() (float64, bool) {
	if text, ok := readTrimmed("/sys/fs/cgroup/cpu.max"); ok {
		return parseCgroupCPUMax(text)
	}
	quota, okQ := readTrimmed("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period, okP := readTrimmed("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if okQ && okP {
		return parseCgroupCPUMax(quota + " " + period)
	}
	return 0, false
}

// parseCgroupCPUMax parses "<quota> <period>"; quota "max" or -1 means none.
func parseCgroupCPUMax(text string) (float64, bool) {
	fields := strings.Fields(text)
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	quota, err1 := strconv.ParseFloat(fields[0], 64)
	period, err2 := strconv.ParseFloat(fields[1], 64)
	if err1 != nil || err2 != nil || quota <= 0 || period <= 0 {
		return 0, false
	}
	return math.Round(quota/period*100) / 100, true
}

// cgroupMemoryLimitMB reads cgroup v2 memory.max, then v1 limit_in_bytes.
func cgroupMemoryLimitMB() (int, bool) {
	if text, ok := readTrimmed("/sys/fs/cgroup/memory.max"); ok {
		return parseCgroupMemoryMax(text)
	}
	if text, ok := readTrimmed("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok {
		return parseCgroupMemoryMax(text)
	}
	return 0, false
}

// parseCgroupMemoryMax parses a byte limit; "max" and v1's near-2^63
// "unlimited" value mean none.
func parseCgroupMemoryMax(text string) (int, bool) {
	if text == "" || text == "max" {
		return 0, false
	}
	bytes, err := strconv.ParseUint(text, 10, 64)
	if err != nil || bytes == 0 || bytes >= 1<<62 {
		return 0, false
	}
	return int(bytes / (1 << 20)), true
}

// parseMeminfoTotalMB reads MemTotal (kB) from /proc/meminfo text.
func parseMeminfoTotalMB(text string) int {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil {
				return int(kb / 1024)
			}
		}
	}
	return 0
}
