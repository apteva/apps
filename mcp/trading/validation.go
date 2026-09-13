package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

type validationCaseResult struct {
	Ordinal           int            `json:"ordinal"`
	RunID             int64          `json:"run_id"`
	SelectedCandidate int            `json:"selected_candidate"`
	Status            string         `json:"status"`
	Error             string         `json:"error,omitempty"`
	Summary           map[string]any `json:"summary,omitempty"`
}
type validationSuite struct {
	ID          int64                  `json:"id"`
	ProjectID   string                 `json:"project_id"`
	PortfolioID int64                  `json:"portfolio_id"`
	SourceRunID int64                  `json:"source_backtest_id"`
	Name        string                 `json:"name"`
	Status      string                 `json:"status"`
	Error       string                 `json:"error,omitempty"`
	Config      validationConfig       `json:"config"`
	Source      validationSource       `json:"-"`
	Plan        []validationCase       `json:"plan"`
	PlanHash    string                 `json:"plan_sha256"`
	Cases       []validationCaseResult `json:"cases"`
	Report      map[string]any         `json:"report"`
}

var validationWorkers = struct {
	sync.Mutex
	entries  map[int64]*validationWorker
	stopping bool
}{entries: map[int64]*validationWorker{}}

type validationWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func validationPlanHash(source validationSource, cfg validationConfig, plan []validationCase) string {
	return sim.Hash(struct {
		Version string
		Source  validationSource
		Config  validationConfig
		Plan    []validationCase
	}{validationVersion, source, cfg, plan})
}

func createValidation(ctx *sdk.AppCtx, project string, sourceID int64, name string, cfg validationConfig) (*validationSuite, error) {
	run, err := dbGetBacktestRun(ctx.AppDB(), project, sourceID)
	if err != nil {
		return nil, err
	}
	if !eventBacktest(run) {
		return nil, errors.New("source must be an event backtest with captured inputs")
	}
	if run.Summary["agent_replay_only"] == true {
		return nil, errors.New("recorded agent replay cannot be retuned; select an original agent simulation")
	}
	record, err := loadSimulation(ctx.AppDB(), sourceID)
	if err != nil {
		return nil, err
	}
	source := validationSource{Spec: record.Spec, Inputs: record.Inputs, InputHash: record.InputHash}
	plan, err := planValidation(source, &cfg)
	if err != nil {
		return nil, err
	}
	configRaw, _ := json.Marshal(cfg)
	sourceRaw, _ := json.Marshal(source)
	planRaw, _ := json.Marshal(plan)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO validation_suites(project_id,portfolio_id,source_run_id,name,spec_json,source_json,plan_json,plan_sha256) VALUES(?,?,?,?,?,?,?,?)`, project, run.PortfolioID, run.ID, nonEmpty(name, run.Name+" · "+cfg.Mode), string(configRaw), string(sourceRaw), string(planRaw), validationPlanHash(source, cfg, plan))
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, c := range plan {
		if _, err = tx.Exec(`INSERT INTO validation_cases(suite_id,ordinal) VALUES(?,?)`, id, c.Ordinal); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return readValidation(ctx.AppDB(), project, id)
}

func readValidation(db *sql.DB, project string, id int64) (*validationSuite, error) {
	s := &validationSuite{}
	var configRaw, sourceRaw, planRaw string
	err := db.QueryRow(`SELECT id,project_id,portfolio_id,source_run_id,name,status,error,spec_json,source_json,plan_json,plan_sha256 FROM validation_suites WHERE id=? AND project_id=?`, id, project).Scan(&s.ID, &s.ProjectID, &s.PortfolioID, &s.SourceRunID, &s.Name, &s.Status, &s.Error, &configRaw, &sourceRaw, &planRaw, &s.PlanHash)
	if err != nil {
		return nil, err
	}
	for _, pair := range []struct {
		raw string
		out any
	}{{configRaw, &s.Config}, {sourceRaw, &s.Source}, {planRaw, &s.Plan}} {
		if err = json.Unmarshal([]byte(pair.raw), pair.out); err != nil {
			return nil, err
		}
	}
	rows, err := db.Query(`SELECT c.ordinal,COALESCE(c.run_id,0),c.selected_candidate,COALESCE(r.status,'queued'),COALESCE(r.error,''),COALESCE(r.summary_json,'{}') FROM validation_cases c LEFT JOIN backtest_runs r ON r.id=c.run_id WHERE c.suite_id=? ORDER BY c.ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c validationCaseResult
		var summary string
		if err = rows.Scan(&c.Ordinal, &c.RunID, &c.SelectedCandidate, &c.Status, &c.Error, &summary); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(summary), &c.Summary); err != nil {
			return nil, err
		}
		s.Cases = append(s.Cases, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	s.Report = validationReport(s)
	return s, nil
}

func validationMetrics(c validationCaseResult) map[string]float64 {
	result := map[string]float64{}
	if raw, ok := c.Summary["metrics"].(map[string]any); ok {
		for k, v := range raw {
			if n, ok := v.(float64); ok {
				result[k] = n
			}
		}
	}
	return result
}
func selectValidationCandidate(s *validationSuite, fold int) (int, error) {
	selected := -1
	best := math.Inf(-1)
	for _, c := range s.Plan {
		if c.Fold != fold || c.Phase != "train" {
			continue
		}
		r := s.Cases[c.Ordinal]
		if r.Status != "completed" {
			return -1, errors.New("all training candidates must complete before selection")
		}
		score, ok := validationMetrics(r)[s.Config.SelectionMetric]
		if !ok || !finite(score) {
			return -1, errors.New("training selection metric unavailable")
		}
		if selected < 0 || score > best {
			selected = c.Candidate
			best = score
		}
	}
	if selected < 0 {
		return -1, errors.New("training candidates missing")
	}
	return selected, nil
}

func validationReport(s *validationSuite) map[string]any {
	groups := map[string][]validationCaseResult{}
	baselines := map[string]float64{}
	completed, failed := 0, 0
	for _, r := range s.Cases {
		if r.Status == "completed" {
			completed++
		}
		if r.Status == "failed" {
			failed++
		}
		c := s.Plan[r.Ordinal]
		if c.Phase == "baseline" && r.Status == "completed" {
			if ret, ok := validationMetrics(r)["return_pct"]; ok {
				baselines[s.Config.Candidates[c.Candidate].Name] = ret
			}
		}
		if c.Phase == "train" || c.Phase == "baseline" {
			continue
		}
		key := "out_of_sample"
		if c.Phase != "test" {
			key = s.Config.Candidates[c.Candidate].Name
		}
		groups[key] = append(groups[key], r)
	}
	summaries := map[string]any{}
	for name, rows := range groups {
		returns, drawdowns, excess := []float64{}, []float64{}, []float64{}
		losses, breaches, errorsCount := 0, 0, 0
		compounded := 1.0
		for _, r := range rows {
			if r.Status == "failed" {
				errorsCount++
			}
			if r.Status != "completed" {
				continue
			}
			m := validationMetrics(r)
			ret, rok := m["return_pct"]
			dd, dok := m["max_drawdown_pct"]
			ex, eok := m["excess_return_pct"]
			if !rok || !dok || !eok {
				continue
			}
			returns = append(returns, ret)
			drawdowns = append(drawdowns, dd)
			excess = append(excess, ex)
			if ret < -s.Config.LossThresholdPct {
				losses++
			}
			if dd <= -s.Config.DrawdownThresholdPct {
				breaches++
			}
			compounded *= 1 + ret/100
		}
		summary := map[string]any{"planned": len(rows), "completed": len(returns), "failed": errorsCount, "return_pct": validationStats(returns), "drawdown_pct": validationStats(drawdowns), "excess_return_pct": validationStats(excess)}
		if len(returns) > 0 {
			summary["loss_frequency"] = float64(losses) / float64(len(returns))
			summary["drawdown_breach_frequency"] = float64(breaches) / float64(len(returns))
			if s.Config.Mode == "out_of_sample" || s.Config.Mode == "walk_forward" {
				summary["compounded_fold_return_pct"] = (compounded - 1) * 100
			}
		}
		if baseline, ok := baselines[name]; ok {
			delta := make([]float64, len(returns))
			for i, ret := range returns {
				delta[i] = ret - baseline
			}
			summary["baseline_return_pct"] = baseline
			summary["delta_return_pct"] = validationStats(delta)
		}
		summaries[name] = summary
	}
	return map[string]any{"total": len(s.Plan), "completed": completed, "failed": failed, "groups": summaries, "independent_window_capital": true, "selection_metric": s.Config.SelectionMetric, "monte_carlo_method": "uniform_execution_uncertainty", "sample_frequencies_are_conditional": true}
}

func validationChild(db *sql.DB, id int64) (int64, error) {
	var suite int64
	err := db.QueryRow(`SELECT suite_id FROM validation_cases WHERE run_id=?`, id).Scan(&suite)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return suite, err
}
func forbidValidationChild(db *sql.DB, id int64) error {
	id, err := validationChild(db, id)
	if err != nil {
		return err
	}
	if id > 0 {
		return fmt.Errorf("run belongs to validation suite %d; use suite controls", id)
	}
	return nil
}
func validationDecisionBudget(db *sql.DB, runID int64) error {
	suite, err := validationChild(db, runID)
	if err != nil || suite == 0 {
		return err
	}
	var status, raw string
	if err = db.QueryRow(`SELECT status,spec_json FROM validation_suites WHERE id=?`, suite).Scan(&status, &raw); err != nil {
		return err
	}
	if status != "running" {
		return errors.New("validation suite is not running")
	}
	var cfg validationConfig
	if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
		return err
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM backtest_agent_decisions d JOIN validation_cases c ON c.run_id=d.run_id WHERE c.suite_id=?`, suite).Scan(&count); err != nil {
		return err
	}
	if count >= cfg.MaxAgentDecisions {
		return errors.New("suite agent decision budget exhausted")
	}
	return nil
}

func ensureValidationRun(s *validationSuite, c validationCase) (*BacktestRun, error) {
	db := globalCtx.AppDB()
	r := s.Cases[c.Ordinal]
	ci := c.Candidate
	if ci < 0 {
		var err error
		ci, err = selectValidationCandidate(s, c.Fold)
		if err != nil {
			return nil, err
		}
	}
	spec, tape, _, err := validationCaseData(s.Source, s.Config, c, ci)
	if err != nil {
		return nil, err
	}
	if r.RunID == 0 {
		steps := 0
		for _, in := range tape {
			steps = maxInt(steps, int(in.Data["step"]))
		}
		run := &BacktestRun{ProjectID: s.ProjectID, PortfolioID: s.PortfolioID, Name: fmt.Sprintf("%s · fold %d · %s · %s · %s", s.Name, c.Fold+1, c.Phase, s.Config.Candidates[ci].Name, c.Shock.Name), RunKind: "strategy", TotalSteps: steps, Symbols: spec.Symbols, Interval: spec.Interval, StartingCash: spec.Config.StartingCash, Status: "queued", StartAt: c.Start.Format("2006-01-02"), EndAt: c.End.Format("2006-01-02"), Summary: map[string]any{"validation_suite_id": s.ID, "validation_ordinal": c.Ordinal, "validation_period": c.Phase, "validation_candidate": ci}}
		if spec.DecisionMode == "agent" {
			run.RunKind = "agent"
			run.SourceAgentID = spec.Agent.SourceAgentID
		}
		id, err := dbCreateBacktestRun(db, run)
		if err != nil {
			return nil, err
		}
		r.RunID = id
		if _, err = db.Exec(`UPDATE validation_cases SET run_id=?,selected_candidate=? WHERE suite_id=? AND ordinal=?`, id, ci, s.ID, c.Ordinal); err != nil {
			return nil, err
		}
	}
	run, err := dbGetBacktestRun(db, s.ProjectID, r.RunID)
	if err != nil {
		return nil, err
	}
	_, err = loadSimulation(db, run.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = storeSimulation(db, run, spec, tape)
	}
	if err != nil {
		return nil, err
	}
	return dbGetBacktestRun(db, s.ProjectID, r.RunID)
}

func runValidationSuite(ctx context.Context, project string, id int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s, err := readValidation(globalCtx.AppDB(), project, id)
		if err != nil {
			return err
		}
		if s.Status != "running" {
			return nil
		}
		if validationPlanHash(s.Source, s.Config, s.Plan) != s.PlanHash {
			return errors.New("validation plan hash mismatch")
		}
		if s.Source.Spec.SourceHash != simulationSourceHash() {
			return errors.New("validation requires its captured engine version")
		}
		next := -1
		for _, r := range s.Cases {
			if r.Status != "completed" {
				next = r.Ordinal
				break
			}
		}
		if next < 0 {
			_, err = globalCtx.AppDB().Exec(`UPDATE validation_suites SET status='completed',error='',updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running'`, id)
			emit("trading.validation.completed", map[string]any{"suite_id": id, "report": s.Report})
			return err
		}
		// Serialize child creation/start with suite pause/cancel so a stopped suite
		// cannot leave a newly started worker or an untracked running child.
		validationWorkers.Lock()
		var status string
		err = globalCtx.AppDB().QueryRow(`SELECT status FROM validation_suites WHERE id=?`, id).Scan(&status)
		if err != nil || status != "running" {
			validationWorkers.Unlock()
			return err
		}
		child, err := ensureValidationRun(s, s.Plan[next])
		if err == nil && child.Status == "cancelled" {
			err = errors.New("validation child was cancelled")
		}
		if err == nil {
			err = dbSetBacktestStatus(globalCtx.AppDB(), child.ID, "running", "")
		}
		validationWorkers.Unlock()
		if err != nil {
			return err
		}
		emit("trading.validation.progress", map[string]any{"suite_id": id, "case": next, "total": len(s.Plan), "backtest_id": child.ID})
		_, err = runEventSimulation(ctx, child, false)
		if err != nil {
			_, _ = globalCtx.AppDB().Exec(`UPDATE backtest_runs SET status='failed',error=? WHERE id=? AND status='running'`, err.Error(), child.ID)
			return err
		}
	}
}

func controlValidation(project string, id int64, action string) (*validationSuite, error) {
	db := globalCtx.AppDB()
	if _, err := readValidation(db, project, id); err != nil {
		return nil, err
	}
	if action == "status" {
		return readValidation(db, project, id)
	}
	validationWorkers.Lock()
	if action == "run" {
		if validationWorkers.stopping {
			validationWorkers.Unlock()
			return nil, errors.New("app is stopping")
		}
		if validationWorkers.entries[id] != nil {
			validationWorkers.Unlock()
			return readValidation(db, project, id)
		}
		res, err := db.Exec(`UPDATE validation_suites SET status='running',error='',updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND status IN ('queued','paused','failed')`, id, project)
		if err != nil {
			validationWorkers.Unlock()
			return nil, err
		}
		changed, _ := res.RowsAffected()
		if changed == 0 {
			validationWorkers.Unlock()
			return nil, errors.New("suite cannot run in its current state")
		}
		ctx, cancel := context.WithCancel(context.Background())
		worker := &validationWorker{cancel: cancel, done: make(chan struct{})}
		validationWorkers.entries[id] = worker
		validationWorkers.Unlock()
		go func() {
			defer func() {
				cancel()
				validationWorkers.Lock()
				delete(validationWorkers.entries, id)
				close(worker.done)
				validationWorkers.Unlock()
			}()
			if err := runValidationSuite(ctx, project, id); err != nil {
				res, _ := db.Exec(`UPDATE validation_suites SET status='failed',error=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running'`, err.Error(), id)
				if res != nil {
					if n, _ := res.RowsAffected(); n > 0 {
						emit("trading.validation.failed", map[string]any{"suite_id": id, "error": err.Error()})
					}
				}
			}
		}()
	} else if action == "pause" || action == "cancel" {
		status := "paused"
		if action == "cancel" {
			status = "cancelled"
		}
		tx, err := db.Begin()
		if err != nil {
			validationWorkers.Unlock()
			return nil, err
		}
		_, err = tx.Exec(`UPDATE validation_suites SET status=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status NOT IN ('completed','cancelled')`, status, id)
		if err == nil {
			_, err = tx.Exec(`UPDATE backtest_runs SET status=? WHERE id IN (SELECT run_id FROM validation_cases WHERE suite_id=?) AND status IN ('queued','running','paused','failed')`, status, id)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if w := validationWorkers.entries[id]; w != nil {
			w.cancel()
		}
		validationWorkers.Unlock()
		if err != nil {
			return nil, err
		}
	} else {
		validationWorkers.Unlock()
		return nil, errors.New("action must be run, pause, cancel or status")
	}
	return readValidation(db, project, id)
}

func stopValidationWorkers(db *sql.DB) error {
	validationWorkers.Lock()
	validationWorkers.stopping = true
	_, err := db.Exec(`UPDATE validation_suites SET status='paused' WHERE status='running'`)
	if err == nil {
		_, err = db.Exec(`UPDATE backtest_runs SET status='paused' WHERE status='running' AND id IN (SELECT run_id FROM validation_cases)`)
	}
	workers := []*validationWorker{}
	for _, w := range validationWorkers.entries {
		w.cancel()
		workers = append(workers, w)
	}
	validationWorkers.Unlock()
	for _, w := range workers {
		<-w.done
	}
	return err
}

func (a *App) toolValidationCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args["config"])
	if err != nil {
		return nil, err
	}
	var cfg validationConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	s, err := createValidation(ctx, project, int64Arg(args, "source_backtest_id", 0), strArg(args, "name"), cfg)
	return map[string]any{"suite": s}, err
}
func (a *App) toolValidationControl(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	s, err := controlValidation(project, int64Arg(args, "suite_id", 0), strArg(args, "action"))
	return map[string]any{"suite": s}, err
}
func listValidation(db *sql.DB, project string, source int64) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT id,name,status,source_run_id,error FROM validation_suites WHERE project_id=? AND (?=0 OR source_run_id=?) ORDER BY id DESC LIMIT 100`, project, source, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, sourceID int64
		var name, status, e string
		if err = rows.Scan(&id, &name, &status, &sourceID, &e); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "status": status, "source_backtest_id": sourceID, "error": e})
	}
	return out, rows.Err()
}
func (a *App) toolValidationList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := listValidation(ctx.AppDB(), project, int64Arg(args, "source_backtest_id", 0))
	return map[string]any{"suites": rows}, err
}
func validationArtifact(db *sql.DB, project string, id int64) (map[string]any, error) {
	validationWorkers.Lock()
	active := validationWorkers.entries[id] != nil
	validationWorkers.Unlock()
	if active {
		return nil, errors.New("validation worker is still active; wait for cleanup before exporting")
	}
	s, err := readValidation(db, project, id)
	if err != nil {
		return nil, err
	}
	if s.Status == "running" {
		return nil, errors.New("pause the suite or wait for completion before exporting")
	}
	if validationPlanHash(s.Source, s.Config, s.Plan) != s.PlanHash {
		return nil, errors.New("validation plan hash mismatch")
	}
	children := []map[string]any{}
	for _, c := range s.Cases {
		if c.RunID == 0 {
			continue
		}
		run, err := dbGetBacktestRun(db, project, c.RunID)
		if err != nil {
			return nil, err
		}
		bundle, err := simulationBundle(db, run)
		if err != nil {
			return nil, err
		}
		children = append(children, map[string]any{"ordinal": c.Ordinal, "selected_candidate": c.SelectedCandidate, "artifact": bundle})
	}
	payload := map[string]any{"schema": validationVersion, "config": s.Config, "source": s.Source, "plan": s.Plan, "plan_sha256": s.PlanHash, "report": s.Report, "children": children, "status": s.Status}
	return map[string]any{"artifact": payload, "sha256": sim.Hash(payload)}, nil
}
func (a *App) toolValidationReport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "suite_id", 0)
	if args["artifact"] == true {
		return validationArtifact(ctx.AppDB(), project, id)
	}
	s, err := readValidation(ctx.AppDB(), project, id)
	return map[string]any{"suite": s}, err
}
func (a *App) handleHTTPValidation(w http.ResponseWriter, r *http.Request) {
	project, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/validations"), "/")
	var out any
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			source, _ := strconv.ParseInt(r.URL.Query().Get("source_backtest_id"), 10, 64)
			var rows []map[string]any
			rows, err = listValidation(globalCtx.AppDB(), project, source)
			out = map[string]any{"suites": rows}
		case http.MethodPost:
			var args map[string]any
			err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&args)
			if err == nil && args == nil {
				err = errors.New("request must be an object")
			}
			if err == nil {
				args["_project_id"] = project
				out, err = a.toolValidationCreate(globalCtx, args)
			}
		default:
			httpErr(w, 405, "GET or POST required")
			return
		}
	} else {
		parts := strings.Split(rest, "/")
		id, e := strconv.ParseInt(parts[0], 10, 64)
		if e != nil || id <= 0 {
			httpErr(w, 400, "invalid suite id")
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			var s *validationSuite
			s, err = readValidation(globalCtx.AppDB(), project, id)
			out = map[string]any{"suite": s}
		} else if len(parts) == 2 && parts[1] == "artifact" && r.Method == http.MethodGet {
			out, err = validationArtifact(globalCtx.AppDB(), project, id)
		} else if len(parts) == 2 && r.Method == http.MethodPost {
			var s *validationSuite
			s, err = controlValidation(project, id, parts[1])
			out = map[string]any{"suite": s}
		} else {
			httpErr(w, 405, "unsupported validation operation")
			return
		}
	}
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 200, out)
}

func (a *App) validationTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "validation_create", Description: "Plan out-of-sample, walk-forward, robustness, stress or Monte Carlo validation from a captured event backtest. Creates a queued suite; never invokes a model at creation.", InputSchema: schemaObject(map[string]any{"source_backtest_id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}, "config": map[string]any{"type": "object", "description": "mode, seed, train_steps/test_steps/step_steps (market timestamps), expanding, warmup_steps, candidates (name plus strategy definition or agent directive), scenarios, samples, execution uncertainty bounds, max_agent_decisions, loss/drawdown thresholds"}}, []string{"source_backtest_id", "config"}), Handler: a.toolValidationCreate},
		{Name: "validation_list", Description: "List project-scoped validation suites, optionally for a source backtest.", InputSchema: schemaObject(map[string]any{"source_backtest_id": map[string]any{"type": "integer"}}, nil), Handler: a.toolValidationList},
		{Name: "validation_control", Description: "Run/resume, pause, cancel or inspect a validation suite. Run returns immediately; suite budget applies across all agent decisions.", InputSchema: schemaObject(map[string]any{"suite_id": map[string]any{"type": "integer"}, "action": map[string]any{"type": "string", "enum": []string{"run", "pause", "cancel", "status"}}}, []string{"suite_id", "action"}), Handler: a.toolValidationControl},
		{Name: "validation_report", Description: "Read progress, training selections, scenario results, percentiles and conditional loss frequencies. artifact=true exports a hash-verified manifest with replayable child bundles; pause first.", InputSchema: schemaObject(map[string]any{"suite_id": map[string]any{"type": "integer"}, "artifact": map[string]any{"type": "boolean"}}, []string{"suite_id"}), Handler: a.toolValidationReport},
	}
}
