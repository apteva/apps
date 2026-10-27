package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
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
	id := "voiceop_" + token(12)
	insert, err := s.db.db.Exec(`INSERT INTO environment_voice_operations(id,run_id,status,started_at) SELECT ?,id,'active',? FROM environment_runs WHERE id=? AND status='running'`, id, time.Now().UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return nil, nil, err
	}
	changed, err := insert.RowsAffected()
	if err != nil {
		return nil, nil, err
	}
	if changed != 1 {
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
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var status string
				err := s.db.db.QueryRow(`SELECT status FROM environment_runs WHERE id=?`, runID).Scan(&status)
				if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "running") {
					cancel(errVoiceCancelled)
					return
				}
			}
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel(nil)
			_, _ = s.db.db.Exec(`UPDATE environment_voice_operations SET status='finished',finished_at=COALESCE(finished_at,?) WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
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
	// Other serving processes and restarted sidecars share this persisted gate.
	// Wait for their handlers too, without decoding saved voice/spec payloads.
	var stateErr error
	if !timedOut {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			var pending int
			queryErr := s.db.db.QueryRow(`SELECT (SELECT COUNT(*) FROM environment_voice_operations WHERE run_id=? AND status='active')+(SELECT COUNT(*) FROM environment_voice_calls WHERE run_id=? AND status='running')`, run.ID, run.ID).Scan(&pending)
			if queryErr != nil {
				stateErr = queryErr
				timedOut = true
				break
			}
			if pending == 0 {
				break
			}
			select {
			case <-deadline.C:
				timedOut = true
			case <-ticker.C:
			}
			if timedOut {
				break
			}
		}
	}
	if timedOut {
		_, _ = s.db.db.Exec(`UPDATE environment_voice_operations SET status='abandoned',finished_at=COALESCE(finished_at,?) WHERE run_id=? AND status='active'`, time.Now().UTC().Format(time.RFC3339Nano), run.ID)
	}
	// On restart there may be persisted voice rows with no surviving handler.
	// Preserve those partial results rather than claiming finalization succeeded.
	var voiceErrors []string
	if stateErr != nil {
		voiceErrors = append(voiceErrors, stateErr.Error())
	}
	if calls, e := s.db.listVoiceCalls(run.ID); e == nil {
		for i := range calls {
			if calls[i].Status == "running" {
				calls[i].Status = "cancelled"
				calls[i].CancellationReason = "environment_stopped"
				calls[i].Cleanup = &VoiceCleanup{Status: "finalization_incomplete", Errors: []string{"voice handler did not finish before runtime cleanup"}}
				finished := time.Now().UTC()
				calls[i].FinishedAt = &finished
				if saveErr := s.db.saveVoiceCall(&calls[i]); saveErr != nil {
					voiceErrors = append(voiceErrors, saveErr.Error())
				}
			}
			if calls[i].Cleanup != nil && calls[i].Cleanup.Status != "complete" {
				voiceErrors = append(voiceErrors, "voice finalization partial: "+calls[i].Cleanup.Status)
			}
		}
	} else {
		voiceErrors = append(voiceErrors, e.Error())
	}
	err = s.destroyStoppedRun(current)
	status, message := "complete", ""
	if len(voiceErrors) > 0 {
		status = "partial"
		message = strings.Join(voiceErrors, "; ")
	}
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
	if len(voiceErrors) > 0 {
		op.err = errors.Join(op.err, errors.New(strings.Join(voiceErrors, "; ")))
	}
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
	if call.Cleanup != nil && call.Cleanup.Status != "pending" {
		return
	}
	ctx, cancel := voiceCleanupContext(parent, call)
	defer cancel()
	var prior []string
	if call.Cleanup != nil {
		prior = call.Cleanup.Errors
	}
	call.Cleanup = &VoiceCleanup{Status: "complete", Errors: prior}
	if len(prior) > 0 {
		call.Cleanup.Status = "partial"
	}
	for agent, thread := range call.startedThreads {
		var lastErr error
		var events []sdk.RuntimeTelemetryEvent
		history := newTelemetryHistory(s.runtime())
		finalized := waitForVoiceCompletionEvidence(ctx, 15*time.Second, voiceCompletionPoll, func() bool {
			latest, err := history.list(runtime, agent, call.StartedAt, 1000)
			lastErr = err
			if latest != nil {
				events = latest
			}
			return err == nil && voiceThreadFinalized(events, thread)
		})
		if agent == call.Spec.TargetAgent {
			call.TargetTelemetry = events
		} else {
			call.CallerTelemetry = events
		}
		if !finalized {
			call.Cleanup.Status = "partial"
			call.Cleanup.Errors = append(call.Cleanup.Errors, "finalization_timeout")
		}
		if lastErr != nil {
			call.Cleanup.Errors = append(call.Cleanup.Errors, lastErr.Error())
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
	if run == nil {
		return nil, errors.New("run not found")
	}
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

func recordVoiceCleanupError(call *VoiceCall, err error) {
	if err == nil {
		return
	}
	if call.Cleanup == nil {
		call.Cleanup = &VoiceCleanup{Status: "pending"}
	}
	call.Cleanup.Errors = append(call.Cleanup.Errors, err.Error())
}

func voiceCleanupContext(parent context.Context, call *VoiceCall) (context.Context, context.CancelFunc) {
	if call.cleanupDeadline.IsZero() {
		call.cleanupDeadline = time.Now().Add(15 * time.Second)
	}
	return context.WithDeadline(context.WithoutCancel(parent), call.cleanupDeadline)
}
