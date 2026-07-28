package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type store struct{ db *sql.DB }

func (s store) listSuites() ([]Suite, error) {
	rows, err := s.db.Query(`SELECT id,name,description,environment_id,judge_model,continuous_targets_json,schedule_minutes,required_pass_rate,enabled,revision,next_run_at,created_at,updated_at FROM eval_suites ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suite{}
	for rows.Next() {
		suite, err := scanSuite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *suite)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Cases, err = s.listCases(out[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s store) getSuite(id string) (*Suite, error) {
	suite, err := scanSuite(s.db.QueryRow(`SELECT id,name,description,environment_id,judge_model,continuous_targets_json,schedule_minutes,required_pass_rate,enabled,revision,next_run_at,created_at,updated_at FROM eval_suites WHERE id=?`, id))
	if err != nil || suite == nil {
		return suite, err
	}
	suite.Cases, err = s.listCases(id)
	return suite, err
}

func (s store) saveSuite(suite *Suite) error {
	now := time.Now().UTC()
	if suite.CreatedAt.IsZero() {
		suite.CreatedAt = now
	}
	suite.UpdatedAt = now
	if suite.Revision <= 0 {
		suite.Revision = 1
	}
	if suite.RequiredPassRate <= 0 {
		suite.RequiredPassRate = 1
	}
	_, err := s.db.Exec(`INSERT INTO eval_suites(id,name,description,environment_id,judge_model,continuous_targets_json,schedule_minutes,required_pass_rate,enabled,revision,next_run_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,environment_id=excluded.environment_id,judge_model=excluded.judge_model,continuous_targets_json=excluded.continuous_targets_json,schedule_minutes=excluded.schedule_minutes,required_pass_rate=excluded.required_pass_rate,enabled=excluded.enabled,revision=eval_suites.revision+1,next_run_at=excluded.next_run_at,updated_at=excluded.updated_at`, suite.ID, suite.Name, suite.Description, suite.EnvironmentID, suite.JudgeModel, encodeJSON(suite.ContinuousTargets), suite.ScheduleMinutes, suite.RequiredPassRate, boolInt(suite.Enabled), suite.Revision, nullableTime(suite.NextRunAt), formatTime(suite.CreatedAt), formatTime(suite.UpdatedAt))
	return err
}

func (s store) deleteSuite(id string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM eval_experiments WHERE suite_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("suite has experiment history and cannot be deleted")
	}
	_, err := s.db.Exec(`DELETE FROM eval_suites WHERE id=?`, id)
	return err
}

func scanSuite(row interface{ Scan(...any) error }) (*Suite, error) {
	var suite Suite
	var enabled int
	var targets string
	var next sql.NullString
	var created, updated string
	err := row.Scan(&suite.ID, &suite.Name, &suite.Description, &suite.EnvironmentID, &suite.JudgeModel, &targets, &suite.ScheduleMinutes, &suite.RequiredPassRate, &enabled, &suite.Revision, &next, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	suite.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(targets), &suite.ContinuousTargets)
	suite.CreatedAt = parseTime(created)
	suite.UpdatedAt = parseTime(updated)
	if next.Valid {
		value := parseTime(next.String)
		suite.NextRunAt = &value
	}
	return &suite, nil
}

func (s store) listCases(suiteID string) ([]Case, error) {
	rows, err := s.db.Query(`SELECT id,suite_id,name,prompt,mode,voice_json,goals_json,assertions_json,environment_id,weight,timeout_seconds,max_turns,enabled,revision,created_at,updated_at FROM eval_cases WHERE suite_id=? ORDER BY created_at`, suiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Case{}
	for rows.Next() {
		item, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s store) getCase(id string) (*Case, error) {
	return scanCase(s.db.QueryRow(`SELECT id,suite_id,name,prompt,mode,voice_json,goals_json,assertions_json,environment_id,weight,timeout_seconds,max_turns,enabled,revision,created_at,updated_at FROM eval_cases WHERE id=?`, id))
}

func (s store) saveCase(item *Case) error {
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if item.Revision <= 0 {
		item.Revision = 1
	}
	if item.Weight <= 0 {
		item.Weight = 1
	}
	if item.TimeoutSeconds <= 0 {
		item.TimeoutSeconds = 600
	}
	if item.MaxTurns <= 0 {
		item.MaxTurns = 10
	}
	var voiceJSON any
	if item.Voice != nil {
		voiceJSON = encodeJSON(item.Voice)
	}
	_, err := s.db.Exec(`INSERT INTO eval_cases(id,suite_id,name,prompt,mode,voice_json,goals_json,assertions_json,environment_id,weight,timeout_seconds,max_turns,enabled,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET suite_id=excluded.suite_id,name=excluded.name,prompt=excluded.prompt,mode=excluded.mode,voice_json=excluded.voice_json,goals_json=excluded.goals_json,assertions_json=excluded.assertions_json,environment_id=excluded.environment_id,weight=excluded.weight,timeout_seconds=excluded.timeout_seconds,max_turns=excluded.max_turns,enabled=excluded.enabled,revision=eval_cases.revision+1,updated_at=excluded.updated_at`, item.ID, item.SuiteID, item.Name, item.Prompt, item.Mode, voiceJSON, encodeJSON(item.Goals), encodeJSON(item.Assertions), item.EnvironmentID, item.Weight, item.TimeoutSeconds, item.MaxTurns, boolInt(item.Enabled), item.Revision, formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
	if err == nil {
		_, _ = s.db.Exec(`UPDATE eval_suites SET revision=revision+1,updated_at=? WHERE id=?`, formatTime(now), item.SuiteID)
	}
	return err
}

func (s store) deleteCase(id string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM eval_runs WHERE case_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("case has run history and cannot be deleted")
	}
	_, err := s.db.Exec(`DELETE FROM eval_cases WHERE id=?`, id)
	return err
}

func scanCase(row interface{ Scan(...any) error }) (*Case, error) {
	var item Case
	var goals, assertions, created, updated string
	var voice sql.NullString
	var enabled int
	err := row.Scan(&item.ID, &item.SuiteID, &item.Name, &item.Prompt, &item.Mode, &voice, &goals, &assertions, &item.EnvironmentID, &item.Weight, &item.TimeoutSeconds, &item.MaxTurns, &enabled, &item.Revision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(goals), &item.Goals); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(assertions), &item.Assertions); err != nil {
		return nil, err
	}
	if voice.Valid {
		item.Voice = &VoiceCase{}
		if err := json.Unmarshal([]byte(voice.String), item.Voice); err != nil {
			return nil, err
		}
	}
	item.Enabled = enabled != 0
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return &item, nil
}

func (s store) createExperiment(exp *Experiment, cases []Case) error {
	if len(cases) == 0 {
		return errors.New("suite has no enabled cases")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO eval_experiments(id,suite_id,suite_revision,name,trigger_type,status,targets_json,repetitions,judge_model,baseline_target,created_at) VALUES(?,?,?,?,?,'queued',?,?,?,?,?)`, exp.ID, exp.SuiteID, exp.SuiteRevision, exp.Name, exp.TriggerType, encodeJSON(exp.Targets), exp.Repetitions, exp.JudgeModel, exp.BaselineTarget, formatTime(exp.CreatedAt))
	if err != nil {
		return err
	}
	for caseIndex := range cases {
		for targetIndex, target := range exp.Targets {
			for repetition := 1; repetition <= exp.Repetitions; repetition++ {
				id := "run_" + token(12)
				_, err = tx.Exec(`INSERT INTO eval_runs(id,experiment_id,case_id,case_revision,target_index,repetition,status,case_snapshot_json,target_snapshot_json,created_at) VALUES(?,?,?,?,?,?,'queued',?,?,?)`, id, exp.ID, cases[caseIndex].ID, cases[caseIndex].Revision, targetIndex, repetition, encodeJSON(cases[caseIndex]), encodeJSON(target), formatTime(exp.CreatedAt))
				if err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func (s store) listExperiments(limit int) ([]Experiment, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id,suite_id,suite_revision,name,trigger_type,status,targets_json,repetitions,judge_model,baseline_target,created_at,started_at,finished_at,error FROM eval_experiments ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Experiment{}
	for rows.Next() {
		exp, err := scanExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *exp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		runs, err := s.listRuns(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Summary = summarize(runs, out[i].Targets)
	}
	return out, nil
}

func (s store) getExperiment(id string) (*Experiment, error) {
	exp, err := scanExperiment(s.db.QueryRow(`SELECT id,suite_id,suite_revision,name,trigger_type,status,targets_json,repetitions,judge_model,baseline_target,created_at,started_at,finished_at,error FROM eval_experiments WHERE id=?`, id))
	if err != nil || exp == nil {
		return exp, err
	}
	exp.Runs, err = s.listRuns(id)
	if err == nil {
		exp.Summary = summarize(exp.Runs, exp.Targets)
	}
	return exp, err
}

func scanExperiment(row interface{ Scan(...any) error }) (*Experiment, error) {
	var exp Experiment
	var targets, created string
	var started, finished sql.NullString
	err := row.Scan(&exp.ID, &exp.SuiteID, &exp.SuiteRevision, &exp.Name, &exp.TriggerType, &exp.Status, &targets, &exp.Repetitions, &exp.JudgeModel, &exp.BaselineTarget, &created, &started, &finished, &exp.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(targets), &exp.Targets); err != nil {
		return nil, err
	}
	exp.CreatedAt = parseTime(created)
	if started.Valid {
		value := parseTime(started.String)
		exp.StartedAt = &value
	}
	if finished.Valid {
		value := parseTime(finished.String)
		exp.FinishedAt = &value
	}
	return &exp, nil
}

func (s store) listRuns(experimentID string) ([]Run, error) {
	rows, err := s.db.Query(runSelect+` WHERE experiment_id=? ORDER BY created_at,target_index,repetition`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Suggestions, err = s.listSuggestions(out[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s store) getRun(id string) (*Run, error) {
	run, err := scanRun(s.db.QueryRow(runSelect+` WHERE id=?`, id))
	if err != nil || run == nil {
		return run, err
	}
	run.Suggestions, err = s.listSuggestions(run.ID)
	return run, err
}

const runSelect = `SELECT id,experiment_id,case_id,case_revision,target_index,repetition,simulation_attempt,status,stage,case_snapshot_json,target_snapshot_json,environment_run_id,execution_json,voice_call_json,assertions_json,judge_json,correctness_score,judge_score,overall_score,started_at,finished_at,error,created_at FROM eval_runs`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var run Run
	var caseRaw, targetRaw, assertionsRaw, created string
	var executionRaw, voiceCallRaw, judgeRaw, started, finished sql.NullString
	var correctness, judgeScore, overall sql.NullFloat64
	err := row.Scan(&run.ID, &run.ExperimentID, &run.CaseID, &run.CaseRevision, &run.TargetIndex, &run.Repetition, &run.SimulationAttempt, &run.Status, &run.Stage, &caseRaw, &targetRaw, &run.EnvironmentRunID, &executionRaw, &voiceCallRaw, &assertionsRaw, &judgeRaw, &correctness, &judgeScore, &overall, &started, &finished, &run.Error, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(caseRaw), &run.CaseSnapshot); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(targetRaw), &run.TargetSnapshot); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(assertionsRaw), &run.Assertions)
	if executionRaw.Valid {
		run.Execution = &sdk.RuntimeAgentExecution{}
		_ = json.Unmarshal([]byte(executionRaw.String), run.Execution)
	}
	if voiceCallRaw.Valid {
		run.VoiceCall = &EnvironmentVoiceCall{}
		_ = json.Unmarshal([]byte(voiceCallRaw.String), run.VoiceCall)
	}
	if judgeRaw.Valid {
		run.Judge = &JudgeVerdict{}
		_ = json.Unmarshal([]byte(judgeRaw.String), run.Judge)
	}
	if correctness.Valid {
		value := correctness.Float64
		run.CorrectnessScore = &value
	}
	if judgeScore.Valid {
		value := judgeScore.Float64
		run.JudgeScore = &value
	}
	if overall.Valid {
		value := overall.Float64
		run.OverallScore = &value
	}
	if started.Valid {
		value := parseTime(started.String)
		run.StartedAt = &value
	}
	if finished.Valid {
		value := parseTime(finished.String)
		run.FinishedAt = &value
	}
	run.CreatedAt = parseTime(created)
	return &run, nil
}

func (s store) claimRun() (*Run, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	run, err := scanRun(tx.QueryRow(runSelect + ` WHERE status='queued' ORDER BY created_at LIMIT 1`))
	if err != nil || run == nil {
		return run, err
	}
	now := formatTime(time.Now().UTC())
	result, err := tx.Exec(`UPDATE eval_runs SET status='running',stage='starting',started_at=? WHERE id=? AND status='queued'`, now, run.ID)
	if err != nil {
		return nil, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil, nil
	}
	_, _ = tx.Exec(`UPDATE eval_experiments SET status='running',started_at=COALESCE(started_at,?) WHERE id=?`, now, run.ExperimentID)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	value := parseTime(now)
	run.Status, run.Stage, run.StartedAt = "running", "starting", &value
	return run, nil
}

func (s store) updateRunProgress(id, stage, environmentRunID string) error {
	_, err := s.db.Exec(`UPDATE eval_runs SET stage=?,environment_run_id=? WHERE id=?`, stage, environmentRunID, id)
	return err
}

func (s store) finishRun(run *Run) error {
	var executionJSON, voiceCallJSON, judgeJSON any
	if run.Execution != nil {
		executionJSON = encodeJSON(run.Execution)
	}
	if run.Judge != nil {
		judgeJSON = encodeJSON(run.Judge)
	}
	if run.VoiceCall != nil {
		voiceCallJSON = encodeJSON(run.VoiceCall)
	}
	_, err := s.db.Exec(`UPDATE eval_runs SET status=?,stage=?,environment_run_id=?,execution_json=?,voice_call_json=?,assertions_json=?,judge_json=?,correctness_score=?,judge_score=?,overall_score=?,finished_at=?,error=? WHERE id=?`, run.Status, run.Stage, run.EnvironmentRunID, executionJSON, voiceCallJSON, encodeJSON(run.Assertions), judgeJSON, nullableFloat(run.CorrectnessScore), nullableFloat(run.JudgeScore), nullableFloat(run.OverallScore), nullableTime(run.FinishedAt), run.Error, run.ID)
	if err != nil {
		return err
	}
	return s.rollupExperiment(run.ExperimentID)
}

func (s store) retryInvalidSimulation(run *Run) (bool, error) {
	if run == nil || run.SimulationAttempt >= 1 {
		return false, nil
	}
	result, err := s.db.Exec(`
		UPDATE eval_runs
		SET status='queued',
			stage='retrying_simulation',
			simulation_attempt=simulation_attempt+1,
			environment_run_id='',
			execution_json=NULL,
			voice_call_json=NULL,
			assertions_json='[]',
			judge_json=NULL,
			correctness_score=NULL,
			judge_score=NULL,
			overall_score=NULL,
			started_at=NULL,
			finished_at=NULL,
			error=''
		WHERE id=? AND simulation_attempt=?
	`, run.ID, run.SimulationAttempt)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return false, err
	}
	_, err = s.db.Exec(`UPDATE eval_experiments SET status='queued',finished_at=NULL WHERE id=?`, run.ExperimentID)
	return true, err
}

func (s store) rollupExperiment(id string) error {
	var queued, running, complete int
	if err := s.db.QueryRow(`SELECT SUM(CASE WHEN status='queued' THEN 1 ELSE 0 END),SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),SUM(CASE WHEN status IN ('pass','fail','error','cancelled') THEN 1 ELSE 0 END) FROM eval_runs WHERE experiment_id=?`, id).Scan(&queued, &running, &complete); err != nil {
		return err
	}
	status := "running"
	var finished any
	if queued == 0 && running == 0 {
		status, finished = "completed", formatTime(time.Now().UTC())
	}
	_, err := s.db.Exec(`UPDATE eval_experiments SET status=?,finished_at=COALESCE(?,finished_at) WHERE id=?`, status, finished, id)
	return err
}

func (s store) cancelExperiment(id string) error {
	_, err := s.db.Exec(`UPDATE eval_runs SET status='cancelled',finished_at=? WHERE experiment_id=? AND status='queued'`, formatTime(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	return s.rollupExperiment(id)
}

func (s store) retryRun(source *Run) (*Run, error) {
	run := *source
	run.ID = "run_" + token(12)
	run.Status = "queued"
	run.Stage = ""
	run.Repetition++
	run.EnvironmentRunID, run.Execution, run.Judge, run.Error = "", nil, nil, ""
	run.Assertions, run.CorrectnessScore, run.JudgeScore, run.OverallScore = nil, nil, nil, nil
	run.StartedAt, run.FinishedAt = nil, nil
	run.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO eval_runs(id,experiment_id,case_id,case_revision,target_index,repetition,status,case_snapshot_json,target_snapshot_json,created_at) VALUES(?,?,?,?,?,?,'queued',?,?,?)`, run.ID, run.ExperimentID, run.CaseID, run.CaseRevision, run.TargetIndex, run.Repetition, encodeJSON(run.CaseSnapshot), encodeJSON(run.TargetSnapshot), formatTime(run.CreatedAt))
	if err == nil {
		_, _ = s.db.Exec(`UPDATE eval_experiments SET status='queued',finished_at=NULL WHERE id=?`, run.ExperimentID)
	}
	return &run, err
}

func (s store) saveSuggestion(item *Suggestion) error {
	_, err := s.db.Exec(`INSERT INTO eval_suggestions(id,run_id,agent_id,directive,expected_etag,reason,status,created_at) VALUES(?,?,?,?,?,?,?,?)`, item.ID, item.RunID, item.AgentID, item.Directive, item.ExpectedETag, item.Reason, item.Status, formatTime(item.CreatedAt))
	return err
}

func (s store) getSuggestion(id string) (*Suggestion, error) {
	var item Suggestion
	var created string
	var applied sql.NullString
	err := s.db.QueryRow(`SELECT id,run_id,agent_id,directive,expected_etag,reason,status,created_at,applied_at FROM eval_suggestions WHERE id=?`, id).Scan(&item.ID, &item.RunID, &item.AgentID, &item.Directive, &item.ExpectedETag, &item.Reason, &item.Status, &created, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.CreatedAt = parseTime(created)
	if applied.Valid {
		value := parseTime(applied.String)
		item.AppliedAt = &value
	}
	return &item, nil
}

func (s store) listSuggestions(runID string) ([]Suggestion, error) {
	rows, err := s.db.Query(`SELECT id,run_id,agent_id,directive,expected_etag,reason,status,created_at,applied_at FROM eval_suggestions WHERE run_id=? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suggestion{}
	for rows.Next() {
		var item Suggestion
		var created string
		var applied sql.NullString
		if err := rows.Scan(&item.ID, &item.RunID, &item.AgentID, &item.Directive, &item.ExpectedETag, &item.Reason, &item.Status, &created, &applied); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		if applied.Valid {
			value := parseTime(applied.String)
			item.AppliedAt = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s store) markSuggestionApplied(id string) error {
	_, err := s.db.Exec(`UPDATE eval_suggestions SET status='applied',applied_at=? WHERE id=? AND status='proposed'`, formatTime(time.Now().UTC()), id)
	return err
}

func (s store) dueSuites(now time.Time) ([]Suite, error) {
	rows, err := s.db.Query(`SELECT id,name,description,environment_id,judge_model,continuous_targets_json,schedule_minutes,required_pass_rate,enabled,revision,next_run_at,created_at,updated_at FROM eval_suites WHERE enabled=1 AND schedule_minutes>0 AND (next_run_at IS NULL OR next_run_at<=?)`, formatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suite{}
	for rows.Next() {
		item, err := scanSuite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s store) scheduleNext(suite Suite, now time.Time) error {
	next := now.Add(time.Duration(suite.ScheduleMinutes) * time.Minute)
	_, err := s.db.Exec(`UPDATE eval_suites SET next_run_at=? WHERE id=?`, formatTime(next), suite.ID)
	return err
}

func summarize(runs []Run, targets []Target) *Summary {
	summary := &Summary{Total: len(runs), Targets: make([]TargetSummary, len(targets))}
	for i := range targets {
		summary.Targets[i] = TargetSummary{TargetIndex: i, Target: targets[i]}
	}
	var scoreTotal float64
	var scored int
	for _, run := range runs {
		switch run.Status {
		case "queued":
			summary.Queued++
		case "running":
			summary.Running++
		case "pass":
			summary.Passed++
		case "fail":
			summary.Failed++
		case "error":
			summary.Errors++
		}
		if run.OverallScore != nil {
			scoreTotal += *run.OverallScore
			scored++
		}
		if run.TargetIndex < 0 || run.TargetIndex >= len(summary.Targets) {
			continue
		}
		target := &summary.Targets[run.TargetIndex]
		if run.Status == "pass" || run.Status == "fail" || run.Status == "error" {
			target.Runs++
		}
		if run.Status == "pass" {
			target.Passed++
		}
		if run.OverallScore != nil {
			target.AverageScore += *run.OverallScore
		}
		if run.Execution != nil {
			target.AverageCost += run.Execution.Metrics.CostUSD
			target.AverageTokens += float64(run.Execution.Metrics.TokensIn + run.Execution.Metrics.TokensOut)
		}
	}
	completed := summary.Passed + summary.Failed + summary.Errors
	if completed > 0 {
		summary.PassRate = float64(summary.Passed) / float64(completed)
	}
	if scored > 0 {
		summary.AverageScore = scoreTotal / float64(scored)
	}
	for i := range summary.Targets {
		target := &summary.Targets[i]
		if target.Runs > 0 {
			target.PassRate = float64(target.Passed) / float64(target.Runs)
			target.AverageScore /= float64(target.Runs)
			target.AverageCost /= float64(target.Runs)
			target.AverageTokens /= float64(target.Runs)
		}
	}
	return summary
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}
func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
func (s store) assertReady() error {
	if s.db == nil {
		return fmt.Errorf("eval database unavailable")
	}
	return nil
}
