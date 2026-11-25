package main

// MCP tools and panel routes for evaluation.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// evalJobs allows one dataset build or run at a time: both are paced by
// the same public sources (GDELT allows one call every few seconds).
var evalJobs sync.Mutex

// reapOrphanedEvalJobs marks builds and runs left in progress by a previous
// process (the sidecar restarted mid-job) as failed. Callers must hold
// evalJobs, so no job of this process can be running.
func reapOrphanedEvalJobs(db *sql.DB, now time.Time) {
	const msg = "interrupted: the app restarted while this job was running; start it again"
	_, _ = db.Exec(`UPDATE intel_eval_runs SET status = 'failed', error = ?, finished_at = ? WHERE status = 'running'`, msg, ts(now))
	_, _ = db.Exec(`UPDATE intel_eval_datasets SET status = 'failed', error = ? WHERE status = 'building'`, msg)
}

// reapIfIdle reaps orphaned jobs when no job is running in this process.
func reapIfIdle(db *sql.DB) {
	if evalJobs.TryLock() {
		reapOrphanedEvalJobs(db, nowUTC())
		evalJobs.Unlock()
	}
}

func (a *App) evalTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "evaluation_dataset_build", Description: "Freeze a historical evaluation dataset: resolved YES/NO questions from Manifold and Kalshi with their trade-by-trade price history, filtered for liquidity and lifetime. Runs in the background; poll evaluation_datasets. Args: venues? (manifold,kalshi), since_days? (30), max_per_venue? (150), min_traders? (15), min_trades? (10), min_open_days? (8), max_per_event? (2), name?.",
			InputSchema: schemaObject(map[string]any{"venues": prop("string"), "since_days": prop("integer"), "max_per_venue": prop("integer"), "min_traders": prop("integer"),
				"min_trades": prop("integer"), "min_open_days": prop("number"), "max_per_event": prop("integer"), "name": prop("string")}, nil), Handler: a.toolEvalDatasetBuild},
		{Name: "evaluation_datasets", Description: "Frozen evaluation datasets with status, size, content hash and skip reasons. Args: id?.",
			InputSchema: schemaObject(map[string]any{"id": prop("integer")}, nil), Handler: a.toolEvalDatasets},
		{Name: "evaluation_run", Description: "Backtest on a frozen dataset with time-ordered holdouts: uniform, base rate, market price and recalibrated market price at each horizon, optionally the AI-assisted method (market + capped LLM evidence shift on news seen before the forecast time). Reports Brier, log loss, calibration, cluster-bootstrap intervals, differences from the market, failures and chronological blocks. Runs in the background; poll evaluation_runs. Args: dataset_id, horizons_hours? (\"24,168\"), min_train? (30), blocks? (4), bootstrap? (1000), seed? (1), ai? (false), ai_knowledge_cutoff (required with ai: the model's training-data cutoff), ai_horizon_hours?, ai_max_items? (30), ai_evidence_days? (14).",
			InputSchema: schemaObject(map[string]any{"dataset_id": prop("integer"), "horizons_hours": prop("string"), "min_train": prop("integer"), "blocks": prop("integer"),
				"bootstrap": prop("integer"), "seed": prop("integer"), "ai": prop("boolean"), "ai_knowledge_cutoff": prop("string"), "ai_horizon_hours": prop("number"),
				"ai_max_items": prop("integer"), "ai_evidence_days": prop("integer")}, []string{"dataset_id"}), Handler: a.toolEvalRun},
		{Name: "evaluation_runs", Description: "Evaluation runs with status and progress.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolEvalRuns},
		{Name: "evaluation_report", Description: "Evaluation report: the stored backtest of a run (latest finished if run_id is omitted), plus prospective results from live forecasts recorded before outcomes and the follow-up plan for unresolved questions, both computed now and kept separate from the backtest. Args: run_id?, lock_hours? (24).",
			InputSchema: schemaObject(map[string]any{"run_id": prop("integer"), "lock_hours": prop("number")}, nil), Handler: a.toolEvalReport},
	}
}

func (a *App) evalRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/eval-datasets", Handler: toolRoute(http.MethodGet, a.toolEvalDatasets)},
		{Pattern: "/eval-dataset-build", Handler: toolRoute(http.MethodPost, a.toolEvalDatasetBuild)},
		{Pattern: "/eval-run", Handler: toolRoute(http.MethodPost, a.toolEvalRun)},
		{Pattern: "/eval-runs", Handler: toolRoute(http.MethodGet, a.toolEvalRuns)},
		{Pattern: "/eval-report", Handler: toolRoute(http.MethodGet, a.toolEvalReport)},
	}
}

func (a *App) toolEvalDatasetBuild(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if !evalJobs.TryLock() {
		return nil, errors.New("another evaluation job is running; try again when it finishes")
	}
	p := parseDatasetParams(args)
	db := ctx.AppDB()
	reapOrphanedEvalJobs(db, nowUTC())
	id, err := createEvalDataset(db, pid, p, nowUTC())
	if err != nil {
		evalJobs.Unlock()
		return nil, err
	}
	sc := a.client()
	go func() {
		defer evalJobs.Unlock()
		items, skipped, buildErr := buildEvalDataset(sc, p, nowUTC())
		if err := finishEvalDataset(db, id, items, skipped, nowUTC(), buildErr); err != nil {
			log.Printf("[EVAL] dataset %d: %v", id, err)
			_, _ = db.Exec(`UPDATE intel_eval_datasets SET status = 'failed', error = ? WHERE id = ?`, err.Error(), id)
		}
	}()
	return map[string]any{"dataset_id": id, "status": "building", "params": p, "note": "Poll evaluation_datasets; building fetches each question's full trade history and can take several minutes."}, nil
}

func (a *App) toolEvalDatasets(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	reapIfIdle(ctx.AppDB())
	ds, err := listEvalDatasets(ctx.AppDB(), pid, intArgAny(args, "id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"datasets": ds}, nil
}

func (a *App) toolEvalRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := parseEvalParams(args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	dss, err := listEvalDatasets(db, pid, p.DatasetID)
	if err != nil {
		return nil, err
	}
	if len(dss) == 0 {
		return nil, fmt.Errorf("dataset %d not found", p.DatasetID)
	}
	ds := dss[0]
	if ds.Status != "ready" {
		return nil, fmt.Errorf("dataset %d is %s", ds.ID, ds.Status)
	}
	env := a.env(ctx, pid)
	if p.AI && env.llm != nil {
		p.AIModel = env.llm.Model()
	}
	if !evalJobs.TryLock() {
		return nil, errors.New("another evaluation job is running; try again when it finishes")
	}
	reapOrphanedEvalJobs(db, nowUTC())
	runID, err := createEvalRun(db, pid, p, env.now)
	if err != nil {
		evalJobs.Unlock()
		return nil, err
	}
	go func() {
		defer evalJobs.Unlock()
		finish := func(report map[string]any, runErr error) {
			if runErr != nil {
				_, _ = db.Exec(`UPDATE intel_eval_runs SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`, runErr.Error(), ts(nowUTC()), runID)
				return
			}
			b, err := json.Marshal(report)
			if err != nil {
				_, _ = db.Exec(`UPDATE intel_eval_runs SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`, "encoding report: "+err.Error(), ts(nowUTC()), runID)
				return
			}
			_, _ = db.Exec(`UPDATE intel_eval_runs SET status = 'done', error = '', report = ?, finished_at = ? WHERE id = ?`, string(b), ts(nowUTC()), runID)
		}
		items, err := loadEvalItems(db, ds.ID)
		if err != nil {
			finish(nil, err)
			return
		}
		finish(executeEvalRun(env, runID, ds, items, p))
	}()
	return map[string]any{"run_id": runID, "status": "running", "params": p}, nil
}

type evalRunRow struct {
	ID          int64          `json:"id"`
	DatasetID   int64          `json:"dataset_id"`
	Status      string         `json:"status"`
	Progress    string         `json:"progress,omitempty"`
	Error       string         `json:"error,omitempty"`
	Params      map[string]any `json:"params"`
	CodeVersion string         `json:"code_version"`
	CreatedAt   string         `json:"created_at"`
	FinishedAt  string         `json:"finished_at,omitempty"`
}

func listEvalRuns(db *sql.DB, pid string) ([]evalRunRow, error) {
	rows, err := db.Query(`SELECT id, dataset_id, status, error, params, code_version, created_at, COALESCE(finished_at,'') FROM intel_eval_runs
		WHERE project_id = ? ORDER BY id DESC LIMIT 50`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []evalRunRow{}
	for rows.Next() {
		var r evalRunRow
		var msg, params string
		if err := rows.Scan(&r.ID, &r.DatasetID, &r.Status, &msg, &params, &r.CodeVersion, &r.CreatedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		if r.Status == "running" {
			r.Progress = msg
		} else {
			r.Error = msg
		}
		_ = json.Unmarshal([]byte(params), &r.Params)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (a *App) toolEvalRuns(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	reapIfIdle(ctx.AppDB())
	runs, err := listEvalRuns(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"runs": runs}, nil
}

func (a *App) toolEvalReport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	lock := 24.0
	if v, ok := floatArgAny(args, "lock_hours"); ok && v >= 0 {
		lock = v
	}
	out := map[string]any{}
	runID := intArgAny(args, "run_id")
	var raw sql.NullString
	var status, msg string
	q := `SELECT id, status, error, report FROM intel_eval_runs WHERE project_id = ? AND status = 'done' ORDER BY id DESC LIMIT 1`
	qargs := []any{pid}
	if runID > 0 {
		q = `SELECT id, status, error, report FROM intel_eval_runs WHERE project_id = ? AND id = ?`
		qargs = append(qargs, runID)
	}
	err = db.QueryRow(q, qargs...).Scan(&runID, &status, &msg, &raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		out["backtest_note"] = "No finished backtest run yet."
	case err != nil:
		return nil, err
	default:
		out["run"] = map[string]any{"id": runID, "status": status, "message": msg}
		if raw.Valid {
			var rep map[string]any
			if err := json.Unmarshal([]byte(raw.String), &rep); err == nil {
				out["historical_backtest"] = rep
			}
		}
	}
	now := nowUTC()
	pro, err := prospectiveSection(db, pid, lock, 1000, 11, now)
	if err != nil {
		return nil, err
	}
	out["prospective_live"] = pro
	n, _ := pro["n_scored"].(int)
	plan, err := followUpPlan(db, pid, lock, n, now)
	if err != nil {
		return nil, err
	}
	out["follow_up"] = plan
	out["separation"] = "historical_backtest comes from frozen past data and is computed once per run; prospective_live comes only from forecasts recorded before their outcomes and is recomputed on every read. Never pool the two."
	return out, nil
}
