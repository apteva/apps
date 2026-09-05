//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func processIdentity(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	end := strings.LastIndex(string(b), ")")
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(b)[end+1:])
	if len(fields) < 20 || fields[0] == "Z" {
		return ""
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(boot)) + ":" + fields[19]
}
func processGroupAlive(group int) bool {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			continue
		}
		end := strings.LastIndex(string(b), ")")
		if end < 0 {
			continue
		}
		f := strings.Fields(string(b)[end+1:])
		if len(f) > 2 && f[0] != "Z" && f[2] == strconv.Itoa(group) {
			return true
		}
	}
	return false
}

func processStartedAt(pid int) time.Time {
	identity := strings.Split(processIdentity(pid), ":")
	if len(identity) != 2 {
		return time.Time{}
	}
	ticks, err := strconv.ParseInt(identity[1], 10, 64)
	if err != nil {
		return time.Time{}
	}
	body, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			boot, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return time.Time{}
			}
			// Linux exposes procfs starttime in USER_HZ (100 on supported amd64/arm64).
			return time.Unix(boot+ticks/100, (ticks%100)*int64(time.Second)/100)
		}
	}
	return time.Time{}
}
