package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func waitForOwnedProcesses(pids []int, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		alive := port > 0 && portInUse(port)
		for _, pid := range pids {
			if pid <= 1 {
				continue
			}
			if raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
				end := strings.LastIndexByte(string(raw), ')')
				if end >= 0 {
					fields := strings.Fields(string(raw)[end+1:])
					if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
						continue
					}
				}
			}
			if err := syscall.Kill(pid, 0); err == nil || err == syscall.EPERM {
				alive = true
			}
		}
		if !alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForScopes(systemctl string, units []string, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		alive := port > 0 && portInUse(port)
		for _, unit := range units {
			out, err := exec.Command(systemctl, "show", unit, "--property=ActiveState", "--value").Output()
			state := strings.TrimSpace(string(out))
			if err != nil || (state != "inactive" && state != "failed") {
				alive = true
			}
		}
		if !alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
