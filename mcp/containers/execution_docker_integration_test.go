package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalDockerExecutionPersistsInsideWorkspaceContainer(t *testing.T) {
	if os.Getenv("RUN_CONTAINERS_TESTS") != "1" {
		t.Skip("set RUN_CONTAINERS_TESTS=1 to run Docker integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	persistentShells.CloseAll()
	t.Cleanup(persistentShells.CloseAll)
	name := "containers-persistent-test-" + strings.ToLower(newExecutionID()[4:12])
	if _, err := docker(ctx, "run", "-d", "--name", name, "oven/bun:1-debian", "/bin/sh", "-c", "while :; do sleep 3600; done"); err != nil {
		t.Fatalf("start test workspace: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = docker(cleanupCtx, "rm", "-f", name)
	})

	backend := LocalDocker{}
	run := func(id string, argv ...string) (*Execution, string) {
		t.Helper()
		execution := &Execution{ID: id, RuntimeContainerName: name}
		runtimeID, err := backend.StartExecution(ctx, executionRuntimeSpec{
			ExecutionID: id, ContainerName: name, SessionKey: "workspace", Argv: argv, StatefulCommand: true,
		})
		if err != nil {
			t.Fatalf("start execution %s: %v", id, err)
		}
		execution.RuntimeContainerID = runtimeID
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			state, err := backend.InspectExecution(ctx, execution)
			if err != nil {
				t.Fatalf("inspect execution %s: %v", id, err)
			}
			if !state.Running {
				logs, err := backend.ExecutionLogs(ctx, execution, 100)
				if err != nil {
					t.Fatalf("logs for execution %s: %v", id, err)
				}
				if state.ExitCode != 0 {
					t.Fatalf("execution %s exited %d: %s", id, state.ExitCode, logs)
				}
				return execution, logs
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("execution %s did not finish", id)
		return nil, ""
	}

	_, firstLogs := run("exe_persist_prepare", "/bin/sh", "-c",
		`cd /tmp
export WORKSPACE_SESSION_VALUE=shell-state-ok
printf '#!/bin/sh\necho system-state-ok\n' > /usr/local/bin/workspace-proof
chmod +x /usr/local/bin/workspace-proof
nohup /bin/sh -c 'while :; do sleep 60; done' >/tmp/workspace-bg.log 2>&1 &
echo $! >/tmp/workspace-bg.pid
echo prepared`)
	if !strings.Contains(firstLogs, "prepared") {
		t.Fatalf("prepare logs = %q", firstLogs)
	}
	_, secondLogs := run("exe_persist_verify", "/bin/sh", "-c",
		`pwd; echo "$WORKSPACE_SESSION_VALUE"; workspace-proof; kill -0 "$(cat /tmp/workspace-bg.pid)" && echo process-state-ok`)
	if !strings.Contains(secondLogs, "/tmp") || !strings.Contains(secondLogs, "shell-state-ok") ||
		!strings.Contains(secondLogs, "system-state-ok") || !strings.Contains(secondLogs, "process-state-ok") {
		t.Fatalf("workspace state did not persist: %q", secondLogs)
	}

	cancelled := &Execution{ID: "exe_persist_cancel", RuntimeContainerName: name}
	runtimeID, err := backend.StartExecution(ctx, executionRuntimeSpec{
		ExecutionID: cancelled.ID, ContainerName: name, SessionKey: "workspace", StatefulCommand: true,
		Argv: []string{"/bin/sh", "-c", "echo cancellation-started; sleep 60; echo cancellation-failed"},
	})
	if err != nil {
		t.Fatalf("start cancellable execution: %v", err)
	}
	cancelled.RuntimeContainerID = runtimeID
	time.Sleep(250 * time.Millisecond)
	if err := backend.StopExecution(ctx, cancelled); err != nil {
		t.Fatalf("cancel execution: %v", err)
	}
	state, err := backend.InspectExecution(ctx, cancelled)
	if err != nil {
		t.Fatalf("inspect cancelled execution: %v", err)
	}
	if state.Running {
		t.Fatal("cancelled execution is still running")
	}
	cancelledLogs, err := backend.ExecutionLogs(ctx, cancelled, 100)
	if err != nil {
		t.Fatalf("cancelled execution logs: %v", err)
	}
	if !strings.Contains(cancelledLogs, "cancellation-started") || strings.Contains(cancelledLogs, "cancellation-failed") {
		t.Fatalf("unexpected cancelled execution logs: %q", cancelledLogs)
	}
	_, afterCancelLogs := run("exe_persist_after_cancel", "/bin/sh", "-c", `pwd; echo "$WORKSPACE_SESSION_VALUE"`)
	if !strings.Contains(afterCancelLogs, "/tmp") || !strings.Contains(afterCancelLogs, "shell-state-ok") {
		t.Fatalf("persistent shell did not survive command cancellation: %q", afterCancelLogs)
	}

	if err := backend.Stop(ctx, name); err != nil {
		t.Fatalf("stop workspace: %v", err)
	}
	if err := backend.Start(ctx, name); err != nil {
		t.Fatalf("resume workspace: %v", err)
	}
	_, resumedLogs := run("exe_persist_resume", "/bin/sh", "-c", `workspace-proof`)
	if !strings.Contains(resumedLogs, "system-state-ok") {
		t.Fatalf("writable-layer state did not survive stop/resume: %q", resumedLogs)
	}

	count, err := docker(ctx, "ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}")
	if err != nil {
		t.Fatalf("list workspace containers: %v", err)
	}
	if strings.TrimSpace(count) != name {
		t.Fatalf("expected exactly the workspace container, got %q", count)
	}
}
