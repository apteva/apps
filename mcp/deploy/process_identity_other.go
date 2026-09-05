//go:build !linux

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func processIdentity(pid int) string {
	if pid <= 0 {
		return ""
	}
	b, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "stat=").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(b))
	if len(fields) < 6 || strings.HasPrefix(fields[len(fields)-1], "Z") {
		return ""
	}
	return strings.Join(fields[:5], " ")
}
func processGroupAlive(group int) bool {
	b, err := exec.Command("ps", "-axo", "pgid=,stat=").Output()
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == strconv.Itoa(group) && !strings.HasPrefix(f[1], "Z") {
			return true
		}
	}
	return false
}

func processStartedAt(pid int) time.Time {
	value, _ := time.ParseInLocation("Mon Jan 2 15:04:05 2006", processIdentity(pid), time.Local)
	return value
}
