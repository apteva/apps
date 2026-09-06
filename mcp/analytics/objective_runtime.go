package main

import (
	"context"
	"database/sql"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"time"
)

func evaluateObjectiveWithPools(ctx context.Context, read *sql.DB, writer sqlRunner, project string, id int64) ([]TargetProgress, error) {
	tx, err := read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	plan := newEvaluationPlan(contextualDB{db: tx, ctx: ctx})
	revision, err := financialRevision(plan, project)
	if err != nil {
		return nil, err
	}
	started := time.Now().UnixMilli()
	objective, err := getObjective(plan, project, id)
	if err != nil {
		return nil, err
	}
	out := []TargetProgress{}
	for _, target := range objective.Targets {
		out = append(out, measureObjectiveTarget(plan, project, target, false))
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	for i, progress := range out {
		if err := persistObjectiveMeasurementAtRevision(writer, objective.Targets[i], progress, time.Now().UnixMilli(), project, revision, started); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func requestObjectiveProgress(r *http.Request, project string, id int64) ([]TargetProgress, error) {
	return evaluateObjectiveWithPools(r.Context(), readPool(globalCtx), requestWriteDB(r), project, id)
}
func toolObjectiveProgress(app *sdk.AppCtx, project string, id int64) ([]TargetProgress, error) {
	return evaluateObjectiveWithPools(toolContext(app), readPool(app), toolWriter(app), project, id)
}

// A read snapshot may finish after a target edit. Only cache against its original revision.
func persistObjectiveMeasurement(db sqlRunner, target ObjectiveTarget, progress TargetProgress, now int64) error {
	return persistObjectiveMeasurementAtRevision(db, target, progress, now, "", 0, now)
}

func persistObjectiveMeasurementAtRevision(db sqlRunner, target ObjectiveTarget, progress TargetProgress, now int64, project string, revision, started int64) error {
	raw, err := json.Marshal(progress.Details)
	if err != nil {
		return err
	}
	if string(raw) == "null" {
		raw = []byte("{}")
	}
	var actual any
	var measured any
	if progress.ActualValue != nil {
		actual = *progress.ActualValue
		measured = now
	}
	_, err = db.Exec(`INSERT INTO objective_progress(target_id,actual_value,measured_at,status,error,details_json,updated_at)
 SELECT ?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM objective_targets WHERE id=? AND updated_at=? AND retired_at IS NULL)
 AND (?='' OR EXISTS(SELECT 1 FROM financial_projects WHERE project_id=? AND revision=?))
 AND NOT EXISTS(SELECT 1 FROM objective_progress WHERE target_id=? AND updated_at>?)
 ON CONFLICT(target_id) DO UPDATE SET
 actual_value=CASE WHEN excluded.status='error' THEN objective_progress.actual_value ELSE excluded.actual_value END,
 measured_at=CASE WHEN excluded.status='error' THEN objective_progress.measured_at ELSE excluded.measured_at END,
 details_json=CASE WHEN excluded.status='error' THEN objective_progress.details_json ELSE excluded.details_json END,
 status=excluded.status,error=excluded.error,updated_at=excluded.updated_at`, target.ID, actual, measured, progress.Status, progress.Error, string(raw), now, target.ID, target.UpdatedAt, project, project, revision, target.ID, started)
	return err
}
