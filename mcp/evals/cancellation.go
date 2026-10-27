package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type RunAttempt struct {
	Attempt          int             `json:"attempt"`
	EnvironmentRunID string          `json:"environment_run_id"`
	RuntimeID        string          `json:"runtime_id"`
	Status           string          `json:"status"`
	PrimaryError     string          `json:"primary_error,omitempty"`
	VoiceCall        json.RawMessage `json:"voice_call,omitempty"`
	Cleanup          json.RawMessage `json:"cleanup,omitempty"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
}

type evalRunControl struct {
	mu          sync.Mutex
	experiment  string
	environment string
	runtime     string
	requested   chan struct{}
	stopWatch   chan struct{}
	notifyOnce  sync.Once
	stopOnce    sync.Once
	cleanup     json.RawMessage
	cleanupErr  error
}

func (s store) cancellationRequested(id string) (bool, error) {
	var status string
	var requested sql.NullString
	err := s.db.QueryRow(`SELECT status,cancel_requested_at FROM eval_runs WHERE id=?`, id).Scan(&status, &requested)
	return status == "cancelled" || requested.Valid, err
}

func (s store) finishCancelled(id string) error {
	_, err := s.db.Exec(`UPDATE eval_runs SET status='cancelled',stage='cancelled',outcome='cancelled',finished_at=COALESCE(finished_at,?),correctness_score=NULL,judge_score=NULL,quality_score=NULL,overall_score=NULL,judge_json=NULL WHERE id=? AND (cancel_requested_at IS NOT NULL OR status='cancelled')`, formatTime(time.Now().UTC()), id)
	return err
}

func (s store) saveAttempt(run *Run, attempt int, runtime string, cleanup json.RawMessage) error {
	var voice any
	if run.VoiceCall != nil {
		voice = encodeJSON(run.VoiceCall)
	}
	var clean any
	if len(cleanup) > 0 {
		clean = string(cleanup)
	}
	_, err := s.db.Exec(`INSERT INTO eval_run_attempts(run_id,attempt,environment_run_id,runtime_id,status,primary_error,voice_call_json,cleanup_json,started_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(run_id,attempt) DO UPDATE SET environment_run_id=CASE WHEN excluded.environment_run_id='' THEN eval_run_attempts.environment_run_id ELSE excluded.environment_run_id END,runtime_id=CASE WHEN excluded.runtime_id='' THEN eval_run_attempts.runtime_id ELSE excluded.runtime_id END,status=excluded.status,primary_error=CASE WHEN eval_run_attempts.primary_error='' THEN excluded.primary_error ELSE eval_run_attempts.primary_error END,voice_call_json=COALESCE(excluded.voice_call_json,eval_run_attempts.voice_call_json),cleanup_json=COALESCE(excluded.cleanup_json,eval_run_attempts.cleanup_json),finished_at=COALESCE(eval_run_attempts.finished_at,excluded.finished_at)`, run.ID, attempt+1, run.EnvironmentRunID, runtime, run.Status, run.Error, voice, clean, nullableTime(run.StartedAt), nullableTime(run.FinishedAt))
	return err
}

func (s store) listAttempts(id string) ([]RunAttempt, error) {
	rows, err := s.db.Query(`SELECT attempt,environment_run_id,runtime_id,status,primary_error,voice_call_json,cleanup_json,started_at,finished_at FROM eval_run_attempts WHERE run_id=? ORDER BY attempt`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunAttempt
	for rows.Next() {
		var a RunAttempt
		var voice, clean, start, finish sql.NullString
		if err := rows.Scan(&a.Attempt, &a.EnvironmentRunID, &a.RuntimeID, &a.Status, &a.PrimaryError, &voice, &clean, &start, &finish); err != nil {
			return nil, err
		}
		if voice.Valid {
			a.VoiceCall = json.RawMessage(voice.String)
		}
		if clean.Valid {
			a.Cleanup = json.RawMessage(clean.String)
		}
		if start.Valid {
			t := parseTime(start.String)
			a.StartedAt = &t
		}
		if finish.Valid {
			t := parseTime(finish.String)
			a.FinishedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *service) stopControlledEnvironment(c *evalRunControl) {
	c.mu.Lock()
	environment := c.environment
	c.mu.Unlock()
	if environment == "" {
		return
	}
	c.stopOnce.Do(func() {
		var result map[string]any
		err := s.ctx.PlatformAPI().CallAppResult("environments", "environment_run_stop", map[string]any{"id": environment}, &result)
		if err != nil {
			if result == nil {
				result = map[string]any{}
			}
			result["request_error"] = err.Error()
			var stored map[string]any
			if inspectErr := s.ctx.PlatformAPI().CallAppResult("environments", "environment_run_get", map[string]any{"id": environment}, &stored); inspectErr == nil {
				result["inspection"] = stored
			} else {
				result["inspection_error"] = inspectErr.Error()
			}
		}
		inspection := result
		if stored, ok := result["inspection"].(map[string]any); ok {
			inspection = stored
		}
		var runtime string
		if run, ok := inspection["run"].(map[string]any); ok {
			runtime, _ = run["runtime_id"].(string)
		}
		raw, _ := json.Marshal(result)
		c.mu.Lock()
		if c.runtime == "" {
			c.runtime = runtime
		}
		c.cleanup = raw
		c.cleanupErr = err
		c.mu.Unlock()
	})
}

func (s *service) registerRunControl(run *Run) *evalRunControl {
	c := &evalRunControl{experiment: run.ExperimentID, requested: make(chan struct{}), stopWatch: make(chan struct{})}
	s.controlMu.Lock()
	if s.controls == nil {
		s.controls = map[string]*evalRunControl{}
	}
	s.controls[run.ID] = c
	s.controlMu.Unlock()
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-c.stopWatch:
				return
			case <-c.requested:
				s.stopControlledEnvironment(c)
				select {
				case <-c.stopWatch:
					return
				case <-ticker.C:
				}
			case <-ticker.C:
				requested, err := s.db.cancellationRequested(run.ID)
				if err == nil && requested {
					c.notifyOnce.Do(func() { close(c.requested) })
					s.stopControlledEnvironment(c)
				}
			}
		}
	}()
	return c
}

func (s *service) completeRunControl(run *Run, attempt int, c *evalRunControl) error {
	s.stopControlledEnvironment(c)
	close(c.stopWatch)
	c.mu.Lock()
	runtime, cleanup := c.runtime, append(json.RawMessage(nil), c.cleanup...)
	c.mu.Unlock()
	requested, stateErr := s.db.cancellationRequested(run.ID)
	var persistErr error
	if requested {
		run.Status = "cancelled"
		run.Outcome = "cancelled"
		run.Stage = "cancelled"
		run.CorrectnessScore = nil
		run.JudgeScore = nil
		run.QualityScore = nil
		run.OverallScore = nil
		persistErr = s.db.finishCancelled(run.ID)
		if s.ctx != nil {
			s.ctx.Emit("eval.run.cancelled", map[string]any{"run_id": run.ID, "experiment_id": run.ExperimentID, "stage": "cancelled"})
		}
	}
	if attempt == run.SimulationAttempt {
		if run.FinishedAt == nil {
			finished := time.Now().UTC()
			run.FinishedAt = &finished
		}
		persistErr = errors.Join(persistErr, s.db.saveAttempt(run, attempt, runtime, cleanup))
	}
	persistErr = errors.Join(persistErr, s.db.rollupExperiment(run.ExperimentID))
	s.controlMu.Lock()
	delete(s.controls, run.ID)
	s.controlMu.Unlock()
	return errors.Join(stateErr, persistErr)
}

func (s *service) prepareRetry(run *Run, issues []string) error {
	s.controlMu.Lock()
	c := s.controls[run.ID]
	s.controlMu.Unlock()
	var runtime string
	var cleanup json.RawMessage
	var cleanupErr error
	if c != nil {
		s.stopControlledEnvironment(c)
		c.mu.Lock()
		runtime = c.runtime
		cleanupErr = c.cleanupErr
		cleanup = append(json.RawMessage(nil), c.cleanup...)
		c.mu.Unlock()
	}
	snapshot := *run
	snapshot.Status = "invalid_simulation"
	snapshot.Error = "Voice simulation invalid: " + joinIssues(issues)
	t := time.Now().UTC()
	snapshot.FinishedAt = &t
	if err := s.db.saveAttempt(&snapshot, run.SimulationAttempt, runtime, cleanup); err != nil {
		return err
	}
	requested, err := s.db.cancellationRequested(run.ID)
	if err != nil {
		return err
	}
	if requested {
		return errRunCancelled
	}
	if cleanupErr != nil {
		return fmt.Errorf("previous attempt cleanup incomplete: %w", cleanupErr)
	}
	return nil
}

func joinIssues(issues []string) string { return strings.Join(issues, "; ") }

func (s *service) acceptVoiceCancellation(run *Run, call *EnvironmentVoiceCall) error {
	if call.Status != "cancelled" && call.Validity.Status != "cancelled" {
		return nil
	}
	_, err := s.db.db.Exec(`UPDATE eval_runs SET cancel_requested_at=COALESCE(cancel_requested_at,?),stage='cancelling' WHERE id=? AND status='running'`, formatTime(time.Now().UTC()), run.ID)
	if err != nil {
		return err
	}
	return errRunCancelled
}

// Resume persisted cancellation after a sidecar restart. This reads only ids
// for pending work; idle workers do not decode every saved case or experiment.
func (s *service) recoverCancellations() error {
	rows, err := s.db.db.Query(`SELECT id,experiment_id,environment_run_id,simulation_attempt FROM eval_runs WHERE status='running' AND cancel_requested_at IS NOT NULL LIMIT 50`)
	if err != nil {
		return err
	}
	var pending []Run
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.ExperimentID, &run.EnvironmentRunID, &run.SimulationAttempt); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, run := range pending {
		s.controlMu.Lock()
		active := s.controls[run.ID] != nil
		s.controlMu.Unlock()
		if active {
			continue
		}
		stored, loadErr := s.db.getRun(run.ID)
		if loadErr != nil {
			return loadErr
		}
		if stored == nil {
			continue
		}
		run = *stored
		c := &evalRunControl{environment: run.EnvironmentRunID}
		s.stopControlledEnvironment(c)
		run.Status = "cancelled"
		if err := s.db.saveAttempt(&run, run.SimulationAttempt, c.runtime, c.cleanup); err != nil {
			return err
		}
		if err := s.db.finishCancelled(run.ID); err != nil {
			return err
		}
		if err := s.db.rollupExperiment(run.ExperimentID); err != nil {
			return err
		}
	}
	return nil
}
