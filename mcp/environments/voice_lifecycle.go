package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

var errVoiceCancelled = errors.New("voice operation cancelled by environment stop")

type voiceOperation struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}
type runStopOperation struct {
	done chan struct{}
	err  error
}

type RunShutdown struct {
	Status      string  `json:"status"`
	RequestedAt string  `json:"requested_at"`
	FinishedAt  *string `json:"finished_at,omitempty"`
	Error       string  `json:"error,omitempty"`
}

type VoiceCleanup struct {
	Status string   `json:"status"`
	Errors []string `json:"errors,omitempty"`
}

// Registration and the persisted stopping transition share a gate. No lock is
// held while cancelling, waiting for callbacks, or destroying a runtime.
func (s *service) beginVoiceOperation(parent context.Context, runID string) (context.Context, func(), error) {
	s.voiceMu.Lock()
	defer s.voiceMu.Unlock()
	run, err := s.db.getRun(runID)
	if err != nil {
		return nil, nil, err
	}
	if run == nil || run.Status != "running" {
		return nil, nil, errors.New("running environment required")
	}
	if op := s.runStops[runID]; op != nil {
		return nil, nil, errors.New("environment is stopping")
	}
	ctx, cancel := context.WithCancelCause(parent)
	op := &voiceOperation{cancel: cancel, done: make(chan struct{})}
	if s.voiceOperations == nil {
		s.voiceOperations = map[string]map[*voiceOperation]bool{}
	}
	if s.voiceOperations[runID] == nil {
		s.voiceOperations[runID] = map[*voiceOperation]bool{}
	}
	s.voiceOperations[runID][op] = true
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel(nil)
			s.voiceMu.Lock()
			defer s.voiceMu.Unlock()
			delete(s.voiceOperations[runID], op)
			if len(s.voiceOperations[runID]) == 0 {
				delete(s.voiceOperations, runID)
			}
			close(op.done)
		})
	}, nil
}

func (s *service) stopRun(run *Run) error {
	s.voiceMu.Lock()
	if existing := s.runStops[run.ID]; existing != nil {
		s.voiceMu.Unlock()
		<-existing.done
		return existing.err
	}
	current, err := s.db.getRun(run.ID)
	if err != nil || current == nil {
		s.voiceMu.Unlock()
		return err
	}
	if current.Status == "stopped" {
		s.voiceMu.Unlock()
		return nil
	}
	if _, err = s.db.db.Exec(`UPDATE environment_runs SET status='stopping' WHERE id=?`, run.ID); err != nil {
		s.voiceMu.Unlock()
		return err
	}
	if _, err = s.db.db.Exec(`INSERT INTO environment_run_shutdown(run_id,requested_at,status) VALUES(?,?,'finalizing') ON CONFLICT(run_id) DO UPDATE SET status='finalizing',error=''`, run.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		s.voiceMu.Unlock()
		return err
	}
	op := &runStopOperation{done: make(chan struct{})}
	if s.runStops == nil {
		s.runStops = map[string]*runStopOperation{}
	}
	s.runStops[run.ID] = op
	var handlers []*voiceOperation
	for handler := range s.voiceOperations[run.ID] {
		handlers = append(handlers, handler)
	}
	s.voiceMu.Unlock()
	defer func() { s.voiceMu.Lock(); delete(s.runStops, run.ID); close(op.done); s.voiceMu.Unlock() }()
	grace := s.voiceStopTimeout
	if grace <= 0 {
		grace = 20 * time.Second
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	for _, handler := range handlers {
		handler.cancel(errVoiceCancelled)
	}
	timedOut := false
wait:
	for _, handler := range handlers {
		select {
		case <-handler.done:
		case <-deadline.C:
			timedOut = true
			break wait
		}
	}
	// On restart there may be persisted voice rows with no surviving handler.
	// Preserve those partial results rather than claiming finalization succeeded.
	if calls, e := s.db.listVoiceCalls(run.ID); e == nil {
		for i := range calls {
			if calls[i].Status == "running" {
				calls[i].Status = "cancelled"
				calls[i].CancellationReason = "environment_stopped"
				calls[i].Cleanup = &VoiceCleanup{Status: "finalization_incomplete", Errors: []string{"voice handler did not finish before runtime cleanup"}}
				finished := time.Now().UTC()
				calls[i].FinishedAt = &finished
				_ = s.db.saveVoiceCall(&calls[i])
			}
		}
	}
	err = s.destroyStoppedRun(current)
	status, message := "complete", ""
	if timedOut {
		status = "forced_cleanup"
		message = "voice_finalization_timeout"
	}
	if err != nil {
		status = "destroy_failed"
		message = err.Error()
	}
	_, persistErr := s.db.db.Exec(`UPDATE environment_run_shutdown SET status=?,error=?,finished_at=COALESCE(finished_at,?) WHERE run_id=?`, status, message, time.Now().UTC().Format(time.RFC3339Nano), run.ID)
	op.err = errors.Join(err, persistErr)
	if timedOut {
		op.err = errors.Join(op.err, fmt.Errorf("voice_finalization_timeout: runtime cleanup forced after %s", grace))
	}
	return op.err
}

func (s store) runShutdown(id string) (*RunShutdown, error) {
	var out RunShutdown
	// sql.NullString keeps stored inspection independent of runtime existence.
	var finishedText *string
	err := s.db.QueryRow(`SELECT status,requested_at,finished_at,error FROM environment_run_shutdown WHERE run_id=?`, id).Scan(&out.Status, &out.RequestedAt, &finishedText, &out.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out.FinishedAt = finishedText
	return &out, nil
}

func (s *service) abortedVoiceCleanup(parent context.Context, runtime string, call *VoiceCall) {
	if call.Cleanup != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer cancel()
	call.Cleanup = &VoiceCleanup{Status: "partial"}
	// Startup failures have no media finalizer yet. Deferred thread stops run
	// first; retain whatever persisted evidence is available for both sides.
	for _, side := range []struct {
		agent, thread string
		events        bool
	}{{call.Spec.TargetAgent, call.TargetThreadID, true}, {call.CallerAgentAlias, call.CallerThreadID, false}} {
		if side.agent == "" || side.thread == "" || ctx.Err() != nil {
			continue
		}
		events, err := newTelemetryHistory(s.runtime()).list(runtime, side.agent, call.StartedAt, 1000)
		if err != nil {
			call.Cleanup.Errors = append(call.Cleanup.Errors, err.Error())
			continue
		}
		if side.events {
			call.TargetTelemetry = events
		} else {
			call.CallerTelemetry = events
		}
	}
	call.Transcript = voiceTranscript(call.TargetTelemetry, call.TargetThreadID, call.StartedAt)
}

func setVoiceCleanup(call *VoiceCall, errs ...error) {
	call.Cleanup = &VoiceCleanup{Status: "complete"}
	for _, err := range errs {
		if err != nil {
			call.Cleanup.Status = "partial"
			call.Cleanup.Errors = append(call.Cleanup.Errors, err.Error())
		}
	}
	if call.FinalExchange != nil && call.FinalExchange.FinalizationStatus != "finalized" {
		call.Cleanup.Status = "partial"
		call.Cleanup.Errors = append(call.Cleanup.Errors, call.FinalExchange.FinalizationStatus)
	}
}

func cancelVoiceResult(ctx context.Context, call *VoiceCall) {
	if ctx.Err() != nil {
		call.Status = "cancelled"
		call.CancellationReason = context.Cause(ctx).Error()
		call.Validity = VoiceCallValidity{Status: "cancelled"}
	}
}

func (s *service) inspectStoredRun(run *Run) (map[string]any, error) {
	shutdown, err := s.db.runShutdown(run.ID)
	if err != nil {
		return nil, err
	}
	calls, err := s.db.listVoiceCalls(run.ID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"run": run, "runtime": nil, "runtime_state": "stopped", "shutdown": shutdown, "voice_calls": calls, "web_fixtures": run.WebFixtures}
	if run.Status == "starting" || run.Status == "running" || run.Status == "stopping" {
		rt, lookupErr := s.runtime().GetRuntime(run.RuntimeID)
		if lookupErr == nil {
			out["runtime"] = rt
			out["runtime_state"] = "available"
		} else {
			out["runtime_state"] = "unavailable"
			out["runtime_error"] = lookupErr.Error()
		}
	}
	return out, nil
}
