package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

const financialReconcile = 5 * time.Minute

var errFinancialChanged = errors.New("financial inputs changed during refresh; queued for retry")

func financialRevision(db sqlRunner, project string) (int64, error) {
	var revision int64
	err := db.QueryRow(`SELECT revision FROM financial_projects WHERE project_id=?`, project).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}
func queueFinancial(db sqlRunner, project string) error {
	_, err := db.Exec(`INSERT INTO financial_projects(project_id) VALUES(?) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1`, project)
	return err
}
func financialBackoff(failures int) time.Duration {
	if failures > 6 {
		failures = 6
	}
	if failures < 1 {
		failures = 1
	}
	return time.Minute * time.Duration(1<<uint(failures-1))
}

// The SDK dispatches this once per project. No project enumeration or ambient
// global-install authority is used to evaluate source objectives.
func (a *App) financialRefreshWorker(parent context.Context, app *sdk.AppCtx) error {
	project := app.CurrentProject()
	if project == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	db := contextualDB{db: app.AppDB(), ctx: ctx}
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`INSERT OR IGNORE INTO financial_projects(project_id) VALUES(?)`, project); err != nil {
		return err
	}
	token := uuid.NewString()
	res, err := db.Exec(`UPDATE financial_projects SET lease_token=?,lease_until=?,last_attempt=? WHERE project_id=? AND enabled=1 AND lease_until<?`, token, now+30000, now, project, now)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = app.AppDB().ExecContext(cleanup, `UPDATE financial_projects SET lease_token='',lease_until=0 WHERE project_id=? AND lease_token=?`, project, token)
	}()
	// FX work is persisted before networking. Limit each tick to one provider call;
	// a bad optional provider must not prevent unrelated or identity-currency work.
	fxErr := maintainFinancialFX(ctx, app, project)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	mappingErr := refreshFinancialMappings(ctx, app, project, token)
	rows, err := db.Query(`SELECT t.id,t.objective_id FROM objective_targets t JOIN objectives o ON o.id=t.objective_id
 LEFT JOIN financial_targets f ON f.target_id=t.id JOIN financial_projects p ON p.project_id=o.project_id
 WHERE o.project_id=? AND o.status='active' AND t.retired_at IS NULL
 AND (COALESCE(f.input_revision,0)!=p.revision OR COALESCE(f.definition_revision,0)!=t.updated_at OR COALESCE(f.last_attempt,0)<? OR COALESCE(f.last_error,'')!='')
 AND (COALESCE(f.next_retry,0)<=? OR COALESCE(f.input_revision,0)!=p.revision OR COALESCE(f.definition_revision,0)!=t.updated_at)
 ORDER BY COALESCE(f.last_attempt,0),t.id LIMIT 8`, project, now-financialReconcile.Milliseconds(), now)
	if err != nil {
		return err
	}
	type item struct{ id, objective int64 }
	pending := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.objective); err != nil {
			break
		}
		pending = append(pending, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var failures []string
	for _, v := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		changed, e := refreshFinancialTarget(ctx, app, project, v.objective, v.id, token)
		if e != nil {
			failures = append(failures, e.Error())
		}
		if changed {
			app.EmitWithProject("objective.progress.updated", project, map[string]any{"objective_id": v.objective, "target_id": v.id})
		}
	}
	if fxErr != nil {
		failures = append(failures, fxErr.Error())
	}
	if mappingErr != nil {
		failures = append(failures, mappingErr.Error())
	}
	message := strings.Join(failures, "; ")
	if len(message) > 2000 {
		message = message[:2000]
	}
	_, err = db.Exec(`UPDATE financial_projects SET last_error=?,last_success=CASE WHEN ?='' AND NOT EXISTS(
 SELECT 1 FROM objective_targets t JOIN objectives o ON o.id=t.objective_id LEFT JOIN financial_targets f ON f.target_id=t.id
 WHERE o.project_id=financial_projects.project_id AND o.status='active' AND t.retired_at IS NULL AND (f.target_id IS NULL OR f.input_revision!=financial_projects.revision OR f.definition_revision!=t.updated_at OR f.last_error!=''))
 AND (fx_enabled=0 OR NOT EXISTS(SELECT 1 FROM financial_fx_requests x WHERE x.project_id=financial_projects.project_id AND (x.last_success=0 OR x.last_error!='')))
 THEN ? ELSE last_success END WHERE project_id=? AND lease_token=?`, message, message, time.Now().UnixMilli(), project, token)
	return err
}

func refreshFinancialTarget(ctx context.Context, app *sdk.AppCtx, project string, objectiveID, targetID int64, token string) (bool, error) {
	evaluationCtx, cancelEvaluation := context.WithTimeout(ctx, 2*time.Second)
	defer cancelEvaluation()
	read, err := readPool(app).BeginTx(evaluationCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer read.Rollback()
	plan := newEvaluationPlan(contextualDB{db: read, ctx: evaluationCtx})
	revision, err := financialRevision(plan, project)
	if err != nil {
		return false, err
	}
	target, err := financialTarget(plan, project, targetID)
	if err != nil {
		return false, err
	}
	if target.ObjectiveID != objectiveID {
		return false, sql.ErrNoRows
	}
	progress := measureObjectiveTarget(plan, project, target, false)
	if progress.Status == "ok" {
		if e := financialFXCoverage(plan, project, target); e != nil {
			progress.Status = "error"
			progress.Error = e.Error()
		}
		if e := combinedTargetError(plan, project, target.ID); e != nil {
			progress.Status = "error"
			progress.Error = e.Error()
		}
	}
	if err = read.Commit(); err != nil {
		read.Rollback()
		if evaluationCtx.Err() == nil || ctx.Err() != nil {
			return false, err
		}
		// Read deadlines must leave a retry checkpoint, otherwise one slow
		// target would remain first in the queue and starve later objectives.
		progress = progressBase(target, time.Now().UnixMilli())
		progress.Status = "error"
		progress.Error = "objective evaluation timed out; retry queued"
	}
	cancelEvaluation()
	tx, err := app.AppDB().BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	// Acquiring the write lock before checking avoids a check/write race.
	result, err := tx.ExecContext(ctx, `UPDATE financial_projects SET lease_until=lease_until WHERE project_id=? AND revision=? AND lease_token=? AND lease_until>?`, project, revision, token, now)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, errFinancialChanged
	}
	var definition int64
	if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM objective_targets WHERE id=? AND retired_at IS NULL`, target.ID).Scan(&definition); err != nil {
		return false, err
	}
	if definition != target.UpdatedAt {
		return false, errFinancialChanged
	}
	if err = persistObjectiveMeasurement(tx, target, progress, now); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO financial_targets(target_id) VALUES(?)`, target.ID); err != nil {
		return false, err
	}
	var retries int
	if err = tx.QueryRowContext(ctx, `SELECT retry_count FROM financial_targets WHERE target_id=?`, target.ID).Scan(&retries); err != nil {
		return false, err
	}
	state := "current"
	next := int64(0)
	if progress.Status == "error" {
		retries++
		next = now + financialBackoff(retries).Milliseconds()
		state = "source_unavailable"
		if strings.Contains(progress.Error, "FX") {
			state = "missing_fx"
		}
	} else {
		retries = 0
		if progress.Status == "no_data" {
			state = "unverified"
		}
	}
	if progress.ActualValue != nil && *progress.ActualValue == 0 && progress.Status == "ok" {
		var verified, through int64
		if err = tx.QueryRowContext(ctx, `SELECT verified_revision,verified_through FROM financial_targets WHERE target_id=?`, target.ID).Scan(&verified, &through); err != nil {
			return false, err
		}
		required := now - financialReconcile.Milliseconds()
		if target.PeriodEnd < required {
			required = target.PeriodEnd
		}
		state = "unverified"
		if verified == revision && through >= required {
			state = "confirmed_zero"
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE financial_targets SET input_revision=?,definition_revision=?,last_attempt=?,last_success=CASE WHEN ?!='error' THEN ? ELSE last_success END,retry_count=?,next_retry=?,last_error=?,state=? WHERE target_id=?`, revision, definition, now, progress.Status, now, retries, next, progress.Error, state, target.ID)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	if progress.Status == "error" {
		return true, fmt.Errorf("target %d: %s", target.ID, progress.Error)
	}
	return true, nil
}
