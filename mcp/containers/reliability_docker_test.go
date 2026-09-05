package main

import (
	"context"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
	"time"
)

func waitDockerExecution(t *testing.T, ctx context.Context, e *Execution) {
	t.Helper()
	for {
		state, err := (LocalDocker{}).InspectExecution(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Running {
			if state.ExitCode != 0 {
				t.Fatalf("execution exit=%d", state.ExitCode)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func TestDockerExecutionIdentityIsDurableBeforeStart(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	d := LocalDocker{}
	for _, session := range []string{"", "identity-session"} {
		id := newExecutionID()
		recorded := ""
		spec := executionRuntimeSpec{ExecutionID: id, ContainerName: name, Argv: []string{"sh", "-c", "printf ready"}, SessionKey: session, RuntimeReady: func(runtimeID string) error { recorded = runtimeID; return nil }}
		runtimeID, err := d.StartExecution(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		if recorded == "" || recorded != runtimeID {
			t.Fatal("runtime started without durable identity callback")
		}
		e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: name, SessionKey: session}
		waitDockerExecution(t, ctx, e)
		out, _, _, err := d.ExecutionOutput(ctx, e)
		if err != nil || !strings.Contains(out, "ready") {
			t.Fatalf("capture %q %v", out, err)
		}
		_ = d.RemoveExecution(ctx, e)
	}
}
func TestDockerBoundedCaptureReportsBytesAndKeepsFullTail(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	d := LocalDocker{}
	id := newExecutionID()
	runtimeID, err := d.StartExecution(ctx, executionRuntimeSpec{ExecutionID: id, ContainerName: name, Argv: []string{"sh", "-c", "yes abcdefgh | head -c 3145728; printf TAIL-MARKER"}, MaxOutputBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: name}
	waitDockerExecution(t, ctx, e)
	output, total, truncated, err := d.ExecutionOutput(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) > 4096 || !strings.HasSuffix(output, "TAIL-MARKER") || total != 3145728+len("TAIL-MARKER") || !truncated {
		t.Fatalf("bytes=%d total=%d truncated=%v suffix=%q", len(output), total, truncated, output[len(output)-20:])
	}
	if err = d.RemoveExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
}
func TestDockerShellStdinAndMultilineAreSafe(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	d := LocalDocker{}
	for i, command := range []string{"read value; printf 'EOF:%s' \"$?\"", "printf 'first\\n'\nprintf 'second\\n'", "printf redirected >/tmp/redirected\ncat /tmp/redirected"} {
		id := newExecutionID()
		runtimeID, err := d.StartExecution(ctx, executionRuntimeSpec{ExecutionID: id, ContainerName: name, Argv: []string{"sh", "-c", command}, SessionKey: "stdin", StatefulCommand: true})
		if err != nil {
			t.Fatal(err)
		}
		e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: name}
		waitDockerExecution(t, ctx, e)
		out, _, _, err := d.ExecutionOutput(ctx, e)
		want := []string{"EOF:1", "first\nsecond", "redirected"}[i]
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("output %q want %q: %v", out, want, err)
		}
		persistentShells.Remove(e)
	}
}
func TestDockerAllocatesTCPAndUDPPorts(t *testing.T) {
	ctx, _ := auditContainer(t, "sleep", "300")
	name := "containers-ports-" + newExecutionID()
	d := LocalDocker{}
	spec, err := normalizeRunSpec(RunSpec{Name: "port-test", Image: "alpine:3.20", Command: []string{"sleep", "300"}, Ports: []PortSpec{{ContainerPort: 12345, Protocol: "tcp"}, {ContainerPort: 12345, Protocol: "udp"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Run(ctx, spec, name, "bridge"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.Remove(c, name, true)
	})
	ports, err := d.PublishedPorts(ctx, name, spec.Ports)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ports {
		if p.HostPort < 1 {
			t.Fatalf("unresolved binding: %+v", p)
		}
	}
}
func TestDockerArchiveExportLimitsAndCleanup(t *testing.T) {
	ctx, _ := auditContainer(t, "sleep", "300")
	name := "containers-limit-" + newExecutionID()
	d := LocalDocker{}
	if _, err := d.EnsureVolume(ctx, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.RemoveVolume(c, name)
	})
	if err := d.WriteOwnedVolumeFile(ctx, name, "data", []byte(strings.Repeat("x", 32768)), "0600", "0:0"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.exportVolumeArchiveLimited(ctx, name, ".", 1<<20, 1024, 100); err == nil {
		t.Fatal("expanded limit not enforced")
	}
	if _, err := d.exportVolumeArchiveLimited(ctx, name, ".", 16, 1<<20, 100); err == nil {
		t.Fatal("compressed limit not enforced")
	}
	list, err := docker(ctx, "ps", "-aq", "--filter", "name=containers-helper-", "--filter", "volume="+name)
	if err != nil || strings.TrimSpace(list) != "" {
		t.Fatalf("helper leaked: %s %v", list, err)
	}
}
func TestDockerNonRootRunSpecFileOwner(t *testing.T) {
	ctx, _ := auditContainer(t, "sleep", "300")
	d := LocalDocker{}
	volume := "containers-owner-" + newExecutionID()
	name := "containers-owner-test-" + newExecutionID()
	if _, err := d.EnsureVolume(ctx, volume); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.Remove(c, name, true)
		_ = d.RemoveVolume(c, volume)
	})
	spec, err := normalizeRunSpec(RunSpec{Name: "non-root", Image: "alpine:3.20", User: "1000:1000", Command: []string{"sh", "-c", "cat /data/file"}, Volumes: []VolumeSpec{{Name: "data", MountPath: "/data", DockerVolumeName: volume}}, Files: []FileSpec{{Path: "/data/file", Content: "readable"}}})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := d.FileOwner(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	writes, err := resolveFileWrites(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range writes {
		if err = d.WriteOwnedVolumeFile(ctx, w.VolumeName, w.RelPath, w.Content, w.Mode, owner); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = d.Run(ctx, spec, name, "bridge"); err != nil {
		t.Fatal(err)
	}
	code, err := docker(ctx, "wait", name)
	if err != nil || strings.TrimSpace(code) != "0" {
		t.Fatal(fmt.Sprintf("exit %s %v", code, err))
	}
	out, err := d.Logs(ctx, name, 10)
	if err != nil || !strings.Contains(out, "readable") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestDockerArchivePauseRecoversAfterInterruptedTransfer(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	db := testDB(t)
	a := &App{backend: LocalDocker{}}
	manifest := a.Manifest()
	app := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, nil, nil)
	w := testWorkload("wrk_pause", "pause", StatusRunning)
	w.ContainerName = name
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := a.pauseArchive(ctx, app, w)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := docker(ctx, "inspect", "--format", "{{.State.Paused}}", name)
	if err != nil || strings.TrimSpace(paused) != "true" {
		t.Fatalf("not paused: %q %v", paused, err)
	}
	_, _, err = insertExecution(db, &Execution{ID: "exe_paused_history", WorkloadID: w.ID, Status: executionSucceeded, SessionKey: "old-session"})
	if err != nil {
		t.Fatal(err)
	}
	restarted := &App{backend: LocalDocker{}}
	oldGlobal := globalCtx
	defer func() { globalCtx = oldGlobal }()
	if err = restarted.OnMount(app); err != nil {
		t.Fatal(err)
	}
	paused, err = docker(ctx, "inspect", "--format", "{{.State.Paused}}", name)
	if err != nil || strings.TrimSpace(paused) != "false" {
		t.Fatalf("not resumed: %q %v", paused, err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM containers_archive_pauses`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pause metadata: %d %v", count, err)
	}
}
func TestDockerLostPTYStateTerminatesOrphanCommand(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	oldManager := newPersistentShellManager()
	defer oldManager.CloseAll()
	id := newExecutionID()
	runtimeID, err := oldManager.Start(ctx, executionRuntimeSpec{ExecutionID: id, ContainerName: name, SessionKey: "lost", Argv: []string{"sh", "-c", "sleep 300"}})
	if err != nil {
		t.Fatal(err)
	}
	state := oldManager.execution(id)
	dir := state.session.controlDir
	if dir != shellRuntimeControlDir(runtimeID) {
		t.Fatal("runtime identity does not address session generation")
	}
	e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: name, SessionKey: "lost"}
	inspected, err := (LocalDocker{}).InspectExecution(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Running || inspected.ExitCode != 125 {
		t.Fatalf("lost session was reported successful: %+v", inspected)
	}
	select {
	case <-state.session.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("orphan shell survived recovery")
	}
}

func TestDockerCancellationEscalatesUncooperativeCommand(t *testing.T) {
	ctx, name := auditContainer(t, "sleep", "300")
	d := LocalDocker{}
	id := newExecutionID()
	runtimeID, err := d.StartExecution(ctx, executionRuntimeSpec{ExecutionID: id, ContainerName: name, Argv: []string{"sh", "-c", "trap '' TERM; sleep 300 & wait"}})
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: name}
	if err = d.StopExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err := d.InspectExecution(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Running {
			if state.ExitCode == 0 {
				t.Fatal("killed command returned success")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("uncooperative command survived cancellation")
}
