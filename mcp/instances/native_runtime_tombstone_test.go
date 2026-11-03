package main

import (
	"testing"
	"time"
)

// Apteva Server deletes a candidate whose prepare response may have been lost.
// An unknown ID must settle as deleted and stay deleted.
func TestNativeDeleteOfUnknownDeploymentLeavesTombstone(t *testing.T) {
	b, f, req := newNativeTest(t)
	for _, action := range []string{"inspect", "stop"} {
		if _, w := nativeCall(t, b, action, req); w.Code != 404 {
			t.Fatalf("%s of unknown deployment: %d %s", action, w.Code, w.Body.String())
		}
	}
	out, w := nativeCall(t, b, "delete", req)
	if w.Code != 200 || out.State != "deleted" || !out.StoppedConfirmed || out.Handle != "" {
		t.Fatalf("delete of unknown deployment: %d %s", w.Code, w.Body.String())
	}
	if out, w = nativeCall(t, b, "delete", req); w.Code != 200 || out.State != "deleted" {
		t.Fatalf("repeated delete: %d %s", w.Code, w.Body.String())
	}
	// A late or retried prepare gets the deleted record, which carries no
	// handle or host, so it can never become a running service.
	if out, w = nativeCall(t, b, "prepare", req); w.Code != 200 || out.State != "deleted" || out.Handle != "" || out.HostKey != "" {
		t.Fatalf("prepare after tombstone: %d %s", w.Code, w.Body.String())
	}
	changed := req
	changed.Generation = 2
	if _, w = nativeCall(t, b, "prepare", changed); w.Code != 409 {
		t.Fatalf("changed prepare after tombstone: %d", w.Code)
	}
	time.Sleep(20 * time.Millisecond)
	if d, e := b.record(req.DeploymentID); e != nil || d.Started || d.Execution.State != "deleted" || f.starts != 0 {
		t.Fatalf("tombstone changed: %+v %v starts=%d", d.Execution, e, f.starts)
	}
	foreign := req
	foreign.DeploymentID, foreign.IdempotencyKey = "deployment-2", "operation-1:deployment-2"
	foreign.Target.InstallID = 41
	if _, w = nativeCall(t, b, "delete", foreign); w.Code == 200 {
		t.Fatal("delete for another backend's target accepted")
	}
	if _, e := b.record(foreign.DeploymentID); e == nil {
		t.Fatal("delete for another backend's target left a record")
	}
}
