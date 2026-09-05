//go:build !linux

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

// lsof supplies actual socket ownership on macOS. Missing inspection support
// fails closed; it is never treated as a successful readiness probe.
func pidOwnsPort(pid, port int) bool {
	if pid <= 1 || port <= 0 || processIdentity(pid) == "" {
		return false
	}
	b, err := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fp").Output()
	if err != nil {
		return false
	}
	tree := map[int]bool{pid: true}
	processes, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return false
	}
	for changed := true; changed; {
		changed = false
		for _, line := range strings.Split(string(processes), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			child, _ := strconv.Atoi(f[0])
			parent, _ := strconv.Atoi(f[1])
			if tree[parent] && !tree[child] {
				tree[child] = true
				changed = true
			}
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "p") {
			owner, _ := strconv.Atoi(line[1:])
			if tree[owner] {
				return true
			}
		}
	}
	return false
}
func systemListeningPorts() map[int]bool { return map[int]bool{} }
func findPidListeningOn(port int) int    { return 0 }
