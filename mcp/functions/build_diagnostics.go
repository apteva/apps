package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var errBuildDeadline = errors.New("build deadline exceeded")
var errBuildDisk = errors.New("build disk watchdog stopped build")

type BuildDiagnostics struct {
	ElapsedMS       int64            `json:"elapsed_ms"`
	Reason          string           `json:"reason"`
	Cancellation    string           `json:"cancellation_reason,omitempty"`
	MemoryLimitMB   int              `json:"memory_limit_mb"`
	MemoryPeakBytes *int64           `json:"memory_peak_bytes"`
	MemoryEvents    map[string]int64 `json:"memory_events"`
	PIDEvents       map[string]int64 `json:"pid_events"`
	Source          string           `json:"resource_source"`
}
type BuildCommandError struct {
	Step        string
	Diagnostics BuildDiagnostics
	Cause       error
	Output      string
}

func (e *BuildCommandError) Error() string {
	b, _ := json.Marshal(e.Diagnostics)
	return fmt.Sprintf("%s failed: %v\nbuild_diagnostics: %s\n%s", e.Step, e.Cause, b, e.Output)
}
func (e *BuildCommandError) Unwrap() error { return e.Cause }
func readBuildEvents(path string) map[string]int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]int64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			if n, e := strconv.ParseInt(f[1], 10, 64); e == nil {
				out[f[0]] = n
			}
		}
	}
	return out
}
func captureBuildDiagnostics(pid int, elapsed time.Duration, cause, runErr error) BuildDiagnostics {
	d := BuildDiagnostics{ElapsedMS: elapsed.Milliseconds(), Reason: "ok", MemoryLimitMB: envInt("APTEVA_FUNCTIONS_BUILD_MEMORY_MB", 1024, 128, 8192), Source: "unavailable"}
	if runErr != nil {
		d.Reason = "build_failed"
	}
	if runtime.GOOS == "linux" && pid > 0 {
		root := os.Getenv("APTEVA_FUNCTIONS_CGROUP_ROOT")
		if root == "" {
			root = "/sys/fs/cgroup/apteva-functions"
		}
		dir := filepath.Join(root, fmt.Sprintf("build-%d", pid))
		if n := readIntFile(filepath.Join(dir, "memory.peak")); n >= 0 {
			d.MemoryPeakBytes = &n
			d.Source = "cgroup_v2"
		}
		d.MemoryEvents = readBuildEvents(filepath.Join(dir, "memory.events"))
		d.PIDEvents = readBuildEvents(filepath.Join(dir, "pids.events"))
	}
	if cause != nil {
		d.Cancellation = cause.Error()
		switch {
		case errors.Is(cause, errBuildDisk):
			d.Reason = "build_disk_limit"
		case errors.Is(cause, errBuildDeadline):
			d.Reason = "build_timeout"
		case errors.Is(cause, context.DeadlineExceeded):
			d.Reason = "parent_deadline"
		default:
			d.Reason = "caller_canceled"
		}
	}
	if runErr != nil && d.MemoryEvents["oom_kill"] > 0 {
		d.Reason = "build_oom"
	}
	if runErr != nil && cause == nil && d.Reason == "build_failed" && d.PIDEvents["max"] > 0 {
		d.Reason = "build_pid_limit"
	}
	return d
}

// Go reads a mode file, not GOTELEMETRY=off. Both paths stay inside the build HOME.
func disableBuildTelemetry(dir string) error {
	home := filepath.Join(dir, ".sandbox-home")
	for _, config := range []string{filepath.Join(home, ".config"), filepath.Join(home, "Library", "Application Support")} {
		path := filepath.Join(config, "go", "telemetry")
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(path, "mode"), []byte("off\n"), 0600); err != nil {
			return err
		}
	}
	return nil
}
