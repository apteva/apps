package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestNativeInspectionPersistentSessionAndIdentity(t *testing.T) {
	p := newNativeInspectionPool(context.Background())
	defer p.close()
	var dials atomic.Int32
	p.dial = func(*Instance, time.Duration) (*ssh.Client, error) {
		dials.Add(1)
		return auditSSHClient(t, t.TempDir()), nil
	}
	i := &Instance{ID: 90, SSHHostKey: "first"}
	for n := 0; n < 4; n++ {
		out, e := p.run(context.Background(), i, "printf ready")
		if e != nil || out != "ready" {
			t.Fatal(out, e)
		}
	}
	if dials.Load() != 1 {
		t.Fatal("routine polls opened new SSH sessions", dials.Load())
	}
	i.SSHHostKey = "changed"
	if _, e := p.run(context.Background(), i, "printf changed"); e != nil {
		t.Fatal(e)
	}
	if dials.Load() != 2 {
		t.Fatal("host identity change reused old transport")
	}
	if _, e := p.run(context.Background(), i, "exit 7"); e == nil {
		t.Fatal("failed command accepted")
	}
	if _, e := p.run(context.Background(), i, "printf recovered"); e != nil {
		t.Fatal(e)
	}
	if dials.Load() != 3 {
		t.Fatal("failed worker not replaced")
	}
}
func TestNativeInspectionCancellationDoesNotInterruptAdministration(t *testing.T) {
	dir := t.TempDir()
	admin := auditSSHClient(t, dir)
	p := newNativeInspectionPool(context.Background())
	defer p.close()
	p.dial = func(*Instance, time.Duration) (*ssh.Client, error) { return auditSSHClient(t, dir), nil }
	done := make(chan error, 1)
	go func() {
		out, code, e := runSSHOnce(admin, "touch admin-started; sleep 0.3; printf survived", 2*time.Second)
		if code != 0 || out != "survived" {
			e = errors.New("admin interrupted")
		}
		done <- e
	}()
	waitSSHTestFile(t, filepath.Join(dir, "admin-started"))
	c, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	if _, e := p.run(c, &Instance{ID: 91}, "sleep 1"); e == nil {
		t.Fatal("inspection timeout ignored")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if _, e := p.run(context.Background(), &Instance{ID: 91}, "printf recovered"); e != nil {
		t.Fatal(e)
	}
}

type nativeCountingState struct {
	*nativeStateFake
	checks int
}

func (f *nativeCountingState) RestoreStatus(c context.Context, i *Instance, d nativeRecord, peers []nativeRecord) (string, error) {
	f.checks++
	return f.nativeStateFake.RestoreStatus(c, i, d, peers)
}
func TestNativeCompletedRestoreVerifiedOncePerController(t *testing.T) {
	b, f, r := newNativeTest(t)
	prepareNativeTest(t, b, r)
	d, _ := b.record(r.DeploymentID)
	sha := nativeHash("restored state")
	before := nativeJSON(d)
	d.Restore = &nativeRestore{SHA256: sha, Key: nativeHash("restore")}
	if e := b.saveCAS(before, d); e != nil {
		t.Fatal(e)
	}
	state := &nativeCountingState{nativeStateFake: &nativeStateFake{nativeFake: f, digest: sha}}
	b.transport = state
	i, _ := dbGetInstance(b.db, d.InstanceID)
	for n := 0; n < 4; n++ {
		d, _ = b.record(d.ID)
		if _, e := b.inspectRemote(context.Background(), i, &d); e != nil {
			t.Fatal(e)
		}
	}
	if state.checks != 1 {
		t.Fatal("completed restore rechecked", state.checks)
	}
	d, _ = b.record(d.ID)
	if !d.Restore.Completed || d.Execution.SnapshotSHA256 != sha {
		t.Fatal("completion not durable")
	}
	b.restoreVerified = nil // A new controller must revalidate even persisted completion.
	if _, e := b.inspectRemote(context.Background(), i, &d); e != nil {
		t.Fatal(e)
	}
	if state.checks != 2 {
		t.Fatal("controller recovery skipped receipt verification")
	}
	i.SSHHostKey = "rotated"
	if _, e := b.inspectRemote(context.Background(), i, &d); e != nil {
		t.Fatal(e)
	}
	if state.checks != 3 {
		t.Fatal("identity change skipped receipt verification")
	}
	b.invalidateRestore(d)
	state.digest = ""
	if _, e := b.inspectRemote(context.Background(), i, &d); e == nil {
		t.Fatal("missing committed receipt accepted")
	}
}
func TestNativeInspectionOneSystemdQueryAndStructuredResult(t *testing.T) {
	d := nativeRecord{ReleaseDir: "/tmp/release", DataDir: "/tmp/data", Artifact: nativeArtifact{Executable: "app"}}
	cmd := nativeInspectionCommand(d, nil, false)
	if strings.Count(cmd, "systemctl show") != 1 || strings.Contains(cmd, "systemctl is-active") {
		t.Fatal("inspection repeats systemd queries")
	}
	// A missing owner is handled without running host-specific Linux commands.
	p := newNativeInspectionPool(context.Background())
	defer p.close()
	p.dial = func(*Instance, time.Duration) (*ssh.Client, error) { return auditSSHClient(t, t.TempDir()), nil }
	s := nativeSSH{inspections: p}
	d.ReleaseDir = filepath.Join(t.TempDir(), "absent")
	obs, e := s.InspectState(context.Background(), &Instance{ID: 92}, d, nil, false)
	if e != nil || obs.State != "unknown" {
		t.Fatal(obs, e)
	}
	// A malicious unterminated response must not allocate unbounded memory.
	if _, e = p.run(context.Background(), &Instance{ID: 92}, "head -c 70000 /dev/zero"); e == nil {
		t.Fatal("unbounded response accepted")
	}
}

func TestNativeReleasedListenersWithoutPortsIsValidShell(t *testing.T) {
	cmd := exec.Command("sh", "-c", "set -eu; root="+quoteShellArg(t.TempDir())+"\n"+nativeReleasedListeners(nativeRecord{})+"printf valid")
	out, e := cmd.CombinedOutput()
	if e != nil || string(out) != "valid" {
		t.Fatal(string(out), e)
	}
}

func TestNativePendingRestoreAllowsStoppedUploadRetry(t *testing.T) {
	b, f, r := newNativeTest(t)
	prepareNativeTest(t, b, r)
	raw := archiveFixture(t, nil, nil)
	sha := nativeHash(string(raw))
	state := &nativeStateFake{nativeFake: f}
	b.transport = state
	d, _ := b.record(r.DeploymentID)
	before := nativeJSON(d)
	d.Restore = &nativeRestore{SHA256: sha, Key: nativeHash("retry")}
	if e := b.saveCAS(before, d); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.states[d.ID] = nativeObservation{State: "stopped", Stopped: true, RestorePending: true}
	f.mu.Unlock()
	w := stateRequest(b, "restore", d.ID, "retry", sha, raw)
	if w.Code != 200 || state.restores != 1 {
		t.Fatal("partial upload became unresumable", w.Code, w.Body.String(), state.restores)
	}
	d, _ = b.record(d.ID)
	if !d.Restore.Completed || d.Execution.SnapshotSHA256 != sha {
		t.Fatal("retry completion not persisted")
	}
}
