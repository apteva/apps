//go:build !windows

package main

import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRemoteCancellationDefaultShellStopsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("bash", "-c", "echo $$ > pid; sleep 120 & wait")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "pid")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker startup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Execute through sh, matching a host whose default shell rejects kill --.
	out, err := exec.Command("sh", "-c", remoteCancellationScript(dir)).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "REMOTE_CANCEL_CONFIRMED") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestRemoteCancellationRejectsUnrelatedGroupAndGuardsStartup(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("sh", "-c", remoteCancellationScript(dir)).CombinedOutput(); err != nil || !strings.Contains(string(out), "GUARDED_NOT_STARTED") {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "cancel.requested")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", remoteCancellationScript(dir)).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "UNSAFE_GROUP") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestRemoteCancellationFailureIsJoinedAndReported(t *testing.T) {
	previous := runRemoteCancellation
	t.Cleanup(func() { runRemoteCancellation = previous })
	calls := 0
	runRemoteCancellation = func(_ context.Context, _ *sdk.AppCtx, _ int64, _ string, _ int) (string, int, error) {
		calls++
		return "REMOTE_CANCEL_TERM_FAILED", 6, nil
	}
	app := newTestCtx(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finish := registerRemoteKill(ctx, app, 1, 123, "/test/render-123-token")
	if err := finish(); !errors.Is(err, errRemoteCancellation) {
		t.Fatalf("%v", err)
	}
	if calls != 1 {
		t.Fatalf("kill attempts=%d", calls)
	}
	// Successful completion must stop the watcher before deferred cancellation.
	calls = 0
	ctx, cancel = context.WithCancel(context.Background())
	finish = registerRemoteKill(ctx, app, 1, 124, "/test/render-124-token")
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(10 * time.Millisecond)
	if calls != 0 {
		t.Fatalf("late kill after completion: %d", calls)
	}
}

func TestRemoteCancellationEscalatesUnresponsiveWorker(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("python3", "-c", `import os,signal,time
signal.signal(signal.SIGTERM,signal.SIG_IGN)
open('pid','w').write(str(os.getpid()))
time.sleep(120)`)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "pid")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, err := exec.Command("sh", "-c", remoteCancellationScript(dir)).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "REMOTE_CANCEL_CONFIRMED") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestOrphanRemoteCancellationFailureRemainsVisible(t *testing.T) {
	previous := runRemoteCancellation
	t.Cleanup(func() { runRemoteCancellation = previous })
	runRemoteCancellation = func(_ context.Context, _ *sdk.AppCtx, host int64, _ string, _ int) (string, int, error) {
		if host != 7 {
			t.Fatalf("host=%d", host)
		}
		return "permission denied", 6, nil
	}
	app := newTestCtx(t)
	id, err := insertRender(app.AppDB(), testProj, "trim", []string{"1"}, nil, "out.mov", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	recordRenderMetric(app, row, "remote_host_id", 7)
	recordRenderMetric(app, row, "remote_work_dir", "/test/orphan-job")
	row, _ = getRender(app.AppDB(), testProj, id)
	if err := cancelOrphanRemoteRender(app, row); !errors.Is(err, errRemoteCancellation) {
		t.Fatal(err)
	}
	row, _ = getRender(app.AppDB(), testProj, id)
	if row.Status != "failed" || row.ErrorCode != "remote_cancellation_failed" || row.CompletedAt == "" {
		t.Fatalf("%+v", row)
	}
}

func TestRemoteCancellationCannotConfirmFailedProcessInspection(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", remoteCancellationScript(dir))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "REMOTE_CANCEL_VERIFY_FAILED") || strings.Contains(string(out), "REMOTE_CANCEL_CONFIRMED") {
		t.Fatalf("false stop confirmation: %v %s", err, out)
	}
}
