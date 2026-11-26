package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// killUID kills every process owned by uid, the workspace user of a
// container-mode workload. Commands can leave their process group (setsid,
// daemons); they cannot leave their uid. Linux only: elsewhere there is no
// /proc and container mode never runs.
func killUID(uid int) {
	if uid < localUIDBase {
		return // never a shared or system uid
	}
	for round := 0; round < 3; round++ {
		pids := pidsOfUID(uid)
		if len(pids) == 0 {
			return
		}
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func pidsOfUID(uid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		if processUID(filepath.Join("/proc", entry.Name(), "status")) == uid {
			out = append(out, pid)
		}
	}
	return out
}

// processUID reads the real uid from /proc/<pid>/status ("Uid: real eff ...").
func processUID(statusPath string) int {
	file, err := os.Open(statusPath)
	if err != nil {
		return -1
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
			if len(fields) > 0 {
				if value, err := strconv.Atoi(fields[0]); err == nil {
					return value
				}
			}
			return -1
		}
	}
	return -1
}

// containerExposures lists platform files that a workspace user could read
// in container mode: readable by others, with every parent folder enterable.
// Workspaces does not change platform files; it reports them.
func containerExposures() []string {
	dataDir := firstNonEmpty(os.Getenv("DATA_DIR"), "/data")
	dbPath := firstNonEmpty(os.Getenv("DB_PATH"), filepath.Join(dataDir, "apteva.db"))
	var out []string
	for _, candidate := range []string{dbPath, dbPath + "-wal", filepath.Join(dataDir, ".secret")} {
		if readableByOthers(candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func readableByOthers(target string) bool {
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm()&0o004 == 0 {
		return false
	}
	for dir := filepath.Dir(target); ; dir = filepath.Dir(dir) {
		parent, err := os.Stat(dir)
		if err != nil || parent.Mode().Perm()&0o001 == 0 {
			return false
		}
		if dir == filepath.Dir(dir) {
			return true
		}
	}
}
