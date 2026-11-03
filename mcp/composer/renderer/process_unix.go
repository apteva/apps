//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func executionPlatform() string      { return runtime.GOOS + "-" + runtime.GOARCH }
func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func processStart(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
func groupAlive(pid int) bool {
	out, err := exec.Command("ps", "-eo", "pgid=,stat=").Output()
	if err != nil {
		return true
	}
	group := strconv.Itoa(pid)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == group && !strings.HasPrefix(fields[1], "Z") {
			return true
		}
	}
	return false
}
func stopRecorded(pid int, start string) error {
	if pid <= 1 {
		return fmt.Errorf("invalid process identity")
	}
	if current := processStart(pid); current != "" && (start == "" || current != start) {
		return fmt.Errorf("process identity changed; stop unconfirmed")
	}
	if !groupAlive(pid) {
		return nil
	}
	// This session belongs to this exact attempt; never signal a host or another worker.
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for i := 0; i < 10; i++ {
		if !groupAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	for i := 0; i < 20; i++ {
		if !groupAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("processing group still alive")
}
