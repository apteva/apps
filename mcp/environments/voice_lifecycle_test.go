package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVoiceStopWaitsForFinalizationAndIsIdempotent(t *testing.T) {
	s, p := auditService(t)
	run := &Run{ID: "run", RuntimeID: "rt", Status: "running", Error: "original provider failure", StartedAt: time.Now()}
	if err := s.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, finish, err := s.beginVoiceOperation(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelSeen := make(chan struct{})
	release := make(chan struct{})
	persisted := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(cancelSeen)
		<-release
		call := &VoiceCall{ID: "voice", RunID: run.ID, Status: "cancelled", Error: "original provider failure", Cleanup: &VoiceCleanup{Status: "complete"}}
		if e := s.db.saveVoiceCall(call); e != nil {
			panic(e)
		}
		close(persisted)
		finish()
	}()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.stopRun(run) }()
	}
	<-cancelSeen
	if p.destroyed != 0 {
		t.Fatal("runtime destroyed before voice finalization")
	}
	if _, _, e := s.beginVoiceOperation(context.Background(), run.ID); e == nil {
		t.Fatal("stopping run admitted a voice operation")
	}
	close(release)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	select {
	case <-persisted:
	default:
		t.Fatal("final result missing")
	}
	if p.destroyed != 1 {
		t.Fatalf("destroy count=%d", p.destroyed)
	}
	first, e := s.db.getRun(run.ID)
	if e != nil || first.StoppedAt == nil || first.Error != "original provider failure" {
		t.Fatalf("stored=%+v err=%v", first, e)
	}
	if e := s.stopRun(first); e != nil {
		t.Fatal(e)
	}
	second, _ := s.db.getRun(run.ID)
	if p.destroyed != 1 || !first.StoppedAt.Equal(*second.StoppedAt) {
		t.Fatal("repeat stop changed destruction or terminal timestamp")
	}
	out, e := s.inspectStoredRun(second)
	if e != nil || out["runtime"] != nil || out["runtime_state"] != "stopped" {
		t.Fatalf("stored inspection=%+v err=%v", out, e)
	}
	calls := out["voice_calls"].([]VoiceCall)
	if len(calls) != 1 || calls[0].Cleanup.Status != "complete" {
		t.Fatalf("voice results=%+v", calls)
	}
}

func TestVoiceStopForcesBoundedCleanupAndPreservesPartialResult(t *testing.T) {
	s, p := auditService(t)
	s.voiceStopTimeout = 20 * time.Millisecond
	run := &Run{ID: "run", RuntimeID: "rt", Status: "running", StartedAt: time.Now()}
	if err := s.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, finish, err := s.beginVoiceOperation(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if err := s.db.saveVoiceCall(&VoiceCall{ID: "voice", RunID: run.ID, Status: "running", Error: "context_injection_incomplete"}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = s.stopRun(run)
	if err == nil || !strings.Contains(err.Error(), "voice_finalization_timeout") || time.Since(start) > time.Second || p.destroyed != 1 {
		t.Fatalf("forced cleanup: err=%v duration=%s destroys=%d", err, time.Since(start), p.destroyed)
	}
	if !errors.Is(context.Cause(ctx), errVoiceCancelled) {
		t.Fatalf("cause=%v", context.Cause(ctx))
	}
	call, _ := s.db.getVoiceCall("voice")
	if call.Status != "cancelled" || call.Error != "context_injection_incomplete" || call.Cleanup.Status != "finalization_incomplete" {
		t.Fatalf("partial=%+v", call)
	}
	// A handler returning late cannot overwrite a forced terminal outcome.
	if err := s.db.saveVoiceCall(&VoiceCall{ID: "voice", RunID: run.ID, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	late, _ := s.db.getVoiceCall("voice")
	if late.Status != "cancelled" || late.Error != call.Error {
		t.Fatalf("late result erased cancellation: %+v", late)
	}
	shutdown, _ := s.db.runShutdown(run.ID)
	if shutdown.Status != "forced_cleanup" {
		t.Fatalf("shutdown=%+v", shutdown)
	}
}

func TestVoiceShutdownCoordinatesAcrossServingProcesses(t *testing.T) {
	owner, p := auditService(t)
	stopper := &service{db: owner.db, ctx: owner.ctx}
	run := &Run{ID: "run", RuntimeID: "rt", Status: "running", StartedAt: time.Now()}
	if err := owner.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, finish, err := owner.beginVoiceOperation(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	done := make(chan error, 1)
	go func() { done <- stopper.stopRun(run) }()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("persisted stop did not cancel the other process's voice handler")
	}
	if p.destroyed != 0 {
		t.Fatal("other serving process destroyed an active voice handler")
	}
	if _, _, err := owner.beginVoiceOperation(context.Background(), run.ID); err == nil {
		t.Fatal("persisted stop admitted new work")
	}
	finish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("persisted voice lease did not release")
	}
	if p.destroyed != 1 {
		t.Fatalf("destroyed=%d", p.destroyed)
	}
}
