package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestIsolatedGoTelemetryDisabled(t *testing.T) {
	dir := t.TempDir()
	if e := disableBuildTelemetry(dir); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("go", "env", "GOTELEMETRY")
	cmd.Dir = dir
	cmd.Env = buildCmdEnv(dir, dir)
	out, e := cmd.CombinedOutput()
	if e != nil || strings.TrimSpace(string(out)) != "off" {
		t.Fatalf("telemetry: %s %v", out, e)
	}
}
func TestBuildCancellationDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := runBuildCmd(ctx, t.TempDir(), "slow build", "sh", "-c", "sleep 20")
	var failure *BuildCommandError
	if !errors.As(err, &failure) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost cause: %v", err)
	}
	if failure.Diagnostics.Reason != "parent_deadline" || failure.Diagnostics.ElapsedMS < 90 {
		t.Fatalf("bad diagnostics: %+v", failure.Diagnostics)
	}
	if failure.Diagnostics.MemoryLimitMB != 1024 {
		t.Fatal("build allowance changed with worker policy")
	}
}
func TestBuildDiskWatchdogDiagnostics(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 65 MiB")
	}
	t.Setenv("APTEVA_FUNCTIONS_BUILD_DISK_MB", "64")
	err := runBuildCmd(context.Background(), t.TempDir(), "large build", "sh", "-c", "dd if=/dev/zero of=large bs=1048576 count=65 2>/dev/null; sleep 20")
	var failure *BuildCommandError
	if !errors.As(err, &failure) || failure.Diagnostics.Reason != "build_disk_limit" || !errors.Is(err, errBuildDisk) {
		t.Fatalf("disk cause: %v", err)
	}
}
func TestBuildCgroupCountersBeforeCleanup(t *testing.T) {
	if os.Getenv("RUN_FUNCTIONS_CGROUP_TESTS") != "1" {
		t.Skip("requires Linux delegated cgroups")
	}
	err := runBuildCmd(context.Background(), t.TempDir(), "failure", "sh", "-c", "exit 7")
	var failure *BuildCommandError
	if !errors.As(err, &failure) || failure.Diagnostics.Source != "cgroup_v2" || failure.Diagnostics.MemoryPeakBytes == nil || failure.Diagnostics.MemoryEvents == nil || failure.Diagnostics.PIDEvents == nil {
		t.Fatalf("missing counters: %v", err)
	}
	b, _ := json.Marshal(failure.Diagnostics)
	t.Logf("saved diagnostics: %s", b)
}
