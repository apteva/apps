package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// PortfolioUniversePolicy is the portfolio-level mandate boundary. Market and
// reference universes describe what data exists; this policy describes what
// the portfolio is actually permitted to trade.
type PortfolioUniversePolicy struct {
	PortfolioID          int64    `json:"portfolio_id"`
	ProjectID            string   `json:"project_id,omitempty"`
	SelectionMode        string   `json:"selection_mode"`
	IncludeSymbols       []string `json:"include_symbols"`
	ExcludeSymbols       []string `json:"exclude_symbols"`
	ReferenceUniverseID  string   `json:"reference_universe_id,omitempty"`
	RequireActiveListing bool     `json:"require_active_listing"`
	EnforcementEnabled   bool     `json:"enforcement_enabled"`
	UpdatedAt            string   `json:"updated_at,omitempty"`
}

type UniverseViolation struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type ScorecardCriterion struct {
	Metric    string  `json:"metric"`
	Operator  string  `json:"operator"`
	Threshold float64 `json:"threshold"`
	Required  bool    `json:"required"`
}

type StrategyScorecardPolicy struct {
	ID                 int64                `json:"id,omitempty"`
	ProjectID          string               `json:"project_id,omitempty"`
	PortfolioID        int64                `json:"portfolio_id"`
	StrategyID         int64                `json:"strategy_id"`
	Criteria           []ScorecardCriterion `json:"criteria"`
	MinCompletedRuns   int                  `json:"min_completed_runs"`
	RequireOutOfSample bool                 `json:"require_out_of_sample"`
	EnforcementEnabled bool                 `json:"enforcement_enabled"`
	PromotionStage     string               `json:"promotion_stage"`
	PolicyHash         string               `json:"policy_hash"`
	CreatedAt          string               `json:"created_at,omitempty"`
	UpdatedAt          string               `json:"updated_at,omitempty"`
}

type ScorecardCheck struct {
	Metric    string   `json:"metric"`
	Operator  string   `json:"operator"`
	Threshold float64  `json:"threshold"`
	Actual    *float64 `json:"actual,omitempty"`
	Required  bool     `json:"required"`
	Passed    bool     `json:"passed"`
	Detail    string   `json:"detail,omitempty"`
}

type StrategyScorecardEvaluation struct {
	ID              int64                    `json:"id"`
	ProjectID       string                   `json:"project_id,omitempty"`
	PortfolioID     int64                    `json:"portfolio_id"`
	StrategyID      int64                    `json:"strategy_id"`
	StrategyVersion int                      `json:"strategy_version"`
	BacktestRunID   int64                    `json:"backtest_run_id"`
	EvaluationScope string                   `json:"evaluation_scope"`
	Passed          bool                     `json:"passed"`
	Verdict         string                   `json:"verdict"`
	Metrics         map[string]float64       `json:"metrics"`
	Checks          []ScorecardCheck         `json:"checks"`
	Policy          *StrategyScorecardPolicy `json:"policy"`
	PolicyHash      string                   `json:"policy_hash"`
	DatasetSHA256   string                   `json:"dataset_sha256,omitempty"`
	EvaluatedAt     string                   `json:"evaluated_at"`
}

func defaultUniversePolicy(portfolio *Portfolio) *PortfolioUniversePolicy {
	p := &PortfolioUniversePolicy{SelectionMode: "all_allowed_classes", IncludeSymbols: []string{}, ExcludeSymbols: []string{}, EnforcementEnabled: true}
	if portfolio != nil {
		p.PortfolioID, p.ProjectID = portfolio.ID, portfolio.ProjectID
	}
	return p
}

func validateUniversePolicy(p *PortfolioUniversePolicy) error {
	if p == nil || p.PortfolioID <= 0 || strings.TrimSpace(p.ProjectID) == "" {
		return errors.New("portfolio universe policy requires portfolio and project")
	}
	p.SelectionMode = strings.ToLower(strings.TrimSpace(p.SelectionMode))
	if !oneOfString(p.SelectionMode, "all_allowed_classes", "symbol_allowlist", "reference_universe") {
		return errors.New("selection_mode must be all_allowed_classes, symbol_allowlist or reference_universe")
	}
	p.IncludeSymbols = cleanSymbols(p.IncludeSymbols)
	p.ExcludeSymbols = cleanSymbols(p.ExcludeSymbols)
	p.ReferenceUniverseID = strings.TrimSpace(p.ReferenceUniverseID)
	if p.SelectionMode == "symbol_allowlist" && len(p.IncludeSymbols) == 0 {
		return errors.New("symbol_allowlist requires at least one include_symbols entry")
	}
	if p.SelectionMode == "reference_universe" && p.ReferenceUniverseID == "" {
		return errors.New("reference_universe requires reference_universe_id")
	}
	excluded := map[string]bool{}
	for _, symbol := range p.ExcludeSymbols {
		excluded[symbol] = true
	}
	for _, symbol := range p.IncludeSymbols {
		if excluded[symbol] {
			return fmt.Errorf("symbol %s cannot be both included and excluded", symbol)
		}
	}
	return nil
}

func dbGetPortfolioUniversePolicy(db *sql.DB, portfolio *Portfolio) (*PortfolioUniversePolicy, error) {
	p := defaultUniversePolicy(portfolio)
	var includes, excludes string
	var requireActive, enabled int
	err := db.QueryRow(`SELECT selection_mode,include_symbols,exclude_symbols,reference_universe_id,require_active_listing,enforcement_enabled,updated_at
		FROM portfolio_universe_policies WHERE portfolio_id=? AND project_id=?`, portfolio.ID, portfolio.ProjectID).
		Scan(&p.SelectionMode, &includes, &excludes, &p.ReferenceUniverseID, &requireActive, &enabled, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(includes), &p.IncludeSymbols)
	_ = json.Unmarshal([]byte(excludes), &p.ExcludeSymbols)
	p.RequireActiveListing, p.EnforcementEnabled = requireActive != 0, enabled != 0
	return p, nil
}

func dbUpsertPortfolioUniversePolicy(db *sql.DB, portfolio *Portfolio, p PortfolioUniversePolicy) (*PortfolioUniversePolicy, error) {
	orderPlacementMu.Lock()
	defer orderPlacementMu.Unlock()
	p.PortfolioID, p.ProjectID = portfolio.ID, portfolio.ProjectID
	if err := validateUniversePolicy(&p); err != nil {
		return nil, err
	}
	includes, _ := json.Marshal(p.IncludeSymbols)
	excludes, _ := json.Marshal(p.ExcludeSymbols)
	_, err := db.Exec(`INSERT INTO portfolio_universe_policies
		(portfolio_id,project_id,selection_mode,include_symbols,exclude_symbols,reference_universe_id,require_active_listing,enforcement_enabled)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(portfolio_id) DO UPDATE SET project_id=excluded.project_id,
		selection_mode=excluded.selection_mode,include_symbols=excluded.include_symbols,exclude_symbols=excluded.exclude_symbols,
		reference_universe_id=excluded.reference_universe_id,require_active_listing=excluded.require_active_listing,
		enforcement_enabled=excluded.enforcement_enabled,updated_at=CURRENT_TIMESTAMP`,
		p.PortfolioID, p.ProjectID, p.SelectionMode, string(includes), string(excludes), p.ReferenceUniverseID,
		boolInt(p.RequireActiveListing), boolInt(p.EnforcementEnabled))
	if err != nil {
		return nil, err
	}
	return dbGetPortfolioUniversePolicy(db, portfolio)
}

func portfolioUniverseViolation(db *sql.DB, portfolio *Portfolio, symbol string) (*UniverseViolation, error) {
	policy, err := dbGetPortfolioUniversePolicy(db, portfolio)
	if err != nil || !policy.EnforcementEnabled {
		return nil, err
	}
	symbol = canonicalSymbol(symbol)
	if contains(policy.ExcludeSymbols, symbol) {
		return &UniverseViolation{Code: "universe_symbol_excluded", Detail: fmt.Sprintf("%s is explicitly excluded by the portfolio universe policy", symbol)}, nil
	}
	included := contains(policy.IncludeSymbols, symbol)
	switch policy.SelectionMode {
	case "symbol_allowlist":
		if !included {
			return &UniverseViolation{Code: "universe_not_allowed", Detail: fmt.Sprintf("%s is not in the portfolio symbol allowlist", symbol)}, nil
		}
	case "reference_universe":
		member, err := referenceUniverseContainsSymbol(db, policy.ReferenceUniverseID, symbol, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if !member && !included {
			return &UniverseViolation{Code: "universe_not_member", Detail: fmt.Sprintf("%s is not an active member of reference universe %s", symbol, policy.ReferenceUniverseID)}, nil
		}
	}
	if policy.RequireActiveListing {
		active, err := activeListingExists(db, symbol, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if !active {
			return &UniverseViolation{Code: "universe_inactive_listing", Detail: fmt.Sprintf("%s has no active security-master listing", symbol)}, nil
		}
	}
	return nil, nil
}

func activeListingExists(db *sql.DB, symbol string, asOf time.Time) (bool, error) {
	var one int
	day := asOf.Format("2006-01-02")
	err := db.QueryRow(`SELECT 1 FROM security_listings WHERE symbol=? AND active=1
		AND (valid_from='' OR valid_from<=?) AND (valid_to='' OR valid_to>?) LIMIT 1`, canonicalSymbol(symbol), day, day).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func referenceUniverseContainsSymbol(db *sql.DB, universeID, symbol string, asOf time.Time) (bool, error) {
	var one int
	day := asOf.Format("2006-01-02")
	err := db.QueryRow(`SELECT 1 FROM universe_memberships u JOIN security_listings l ON l.security_id=u.security_id
		WHERE u.universe_id=? AND l.symbol=? AND l.active=1
		AND u.valid_from<=? AND (u.valid_to='' OR u.valid_to>?)
		AND (l.valid_from='' OR l.valid_from<=?) AND (l.valid_to='' OR l.valid_to>?) LIMIT 1`,
		universeID, canonicalSymbol(symbol), day, day, day, day).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func validateStrategyUniverseForPortfolio(db *sql.DB, portfolio *Portfolio, def *StrategyDefinition) error {
	if portfolio == nil || def == nil {
		return errors.New("portfolio and strategy definition required")
	}
	for _, symbol := range def.Universe {
		class := inferAssetClass(symbol)
		if !contains(portfolio.AllowedClasses, class) {
			return fmt.Errorf("strategy symbol %s has class %s outside portfolio allowed_classes", symbol, class)
		}
		violation, err := portfolioUniverseViolation(db, portfolio, symbol)
		if err != nil {
			return err
		}
		if violation != nil {
			return fmt.Errorf("strategy universe: %s", violation.Detail)
		}
	}
	return nil
}

// Exclusions must never trap an existing holding, but the exception is only
// for a genuinely risk-reducing sell. Outstanding sells are reserved so a
// broker-live portfolio cannot accidentally open a short via duplicate exits.
func portfolioCanReducePosition(db *sql.DB, portfolioID int64, symbol, outcome string, qty float64) bool {
	outcome = strings.ToUpper(strings.TrimSpace(outcome))
	var held float64
	err := db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=?`,
		portfolioID, canonicalSymbol(symbol), outcome).Scan(&held)
	if err != nil {
		return false
	}
	var reserved float64
	_ = db.QueryRow(`SELECT COALESCE(SUM(MAX(qty-filled_qty,0)),0) FROM orders
		WHERE portfolio_id=? AND symbol=? AND side='sell' AND status='working' AND COALESCE(outcome,'')=?`,
		portfolioID, canonicalSymbol(symbol), outcome).Scan(&reserved)
	return qty > 0 && held-reserved >= qty-1e-9
}

func defaultScorecardPolicy(portfolioID, strategyID int64, projectID string) *StrategyScorecardPolicy {
	return &StrategyScorecardPolicy{
		ProjectID: projectID, PortfolioID: portfolioID, StrategyID: strategyID,
		Criteria: []ScorecardCriterion{
			{Metric: "return_pct", Operator: "min", Threshold: 0, Required: true},
			{Metric: "sharpe_ratio", Operator: "min", Threshold: 0, Required: true},
			{Metric: "max_drawdown_abs_pct", Operator: "max", Threshold: 20, Required: true},
		},
		MinCompletedRuns: 1, RequireOutOfSample: true, PromotionStage: "research",
	}
}

func validateScorecardPolicy(p *StrategyScorecardPolicy) error {
	if p == nil || p.PortfolioID <= 0 || p.StrategyID <= 0 || strings.TrimSpace(p.ProjectID) == "" {
		return errors.New("scorecard policy requires project, portfolio and strategy")
	}
	if p.MinCompletedRuns <= 0 || p.MinCompletedRuns > 1000 {
		return errors.New("min_completed_runs must be between 1 and 1000")
	}
	if !validPromotionStage(p.PromotionStage) {
		return errors.New("invalid promotion_stage")
	}
	if len(p.Criteria) == 0 {
		return errors.New("at least one scorecard criterion required")
	}
	seen := map[string]bool{}
	for i := range p.Criteria {
		c := &p.Criteria[i]
		c.Metric, c.Operator = strings.TrimSpace(c.Metric), strings.ToLower(strings.TrimSpace(c.Operator))
		if c.Metric == "" || !oneOfString(c.Operator, "min", "max") || math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) {
			return fmt.Errorf("invalid scorecard criterion at index %d", i)
		}
		if seen[c.Metric] {
			return fmt.Errorf("duplicate scorecard metric %s", c.Metric)
		}
		seen[c.Metric] = true
	}
	return nil
}

func validPromotionStage(stage string) bool {
	return oneOfString(stage, "research", "paper_candidate", "paper", "live_candidate", "live", "suspended")
}

func dbGetStrategyScorecardPolicy(db *sql.DB, projectID string, portfolioID, strategyID int64) (*StrategyScorecardPolicy, error) {
	p := defaultScorecardPolicy(portfolioID, strategyID, projectID)
	var criteria string
	var requireOOS, enforced int
	err := db.QueryRow(`SELECT id,criteria_json,min_completed_runs,require_out_of_sample,enforcement_enabled,promotion_stage,created_at,updated_at
		FROM strategy_scorecard_policies WHERE project_id=? AND portfolio_id=? AND strategy_id=?`, projectID, portfolioID, strategyID).
		Scan(&p.ID, &criteria, &p.MinCompletedRuns, &requireOOS, &enforced, &p.PromotionStage, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		p.PolicyHash = scorecardPolicyHash(p)
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(criteria), &p.Criteria)
	p.RequireOutOfSample, p.EnforcementEnabled = requireOOS != 0, enforced != 0
	p.PolicyHash = scorecardPolicyHash(p)
	return p, nil
}

func dbUpsertStrategyScorecardPolicy(db *sql.DB, p StrategyScorecardPolicy) (*StrategyScorecardPolicy, error) {
	if p.PromotionStage == "" {
		p.PromotionStage = "research"
	}
	if err := validateScorecardPolicy(&p); err != nil {
		return nil, err
	}
	// A promoted stage is evidence for one exact policy. Tightening or otherwise
	// changing its material criteria must send the strategy back through review;
	// toggling enforcement alone does not invalidate the evidence.
	existing, err := dbGetStrategyScorecardPolicy(db, p.ProjectID, p.PortfolioID, p.StrategyID)
	if err != nil {
		return nil, err
	}
	if existing.ID > 0 && scorecardPolicyHash(existing) != scorecardPolicyHash(&p) {
		p.PromotionStage = "research"
	}
	criteria, _ := json.Marshal(p.Criteria)
	_, err = db.Exec(`INSERT INTO strategy_scorecard_policies
		(project_id,portfolio_id,strategy_id,criteria_json,min_completed_runs,require_out_of_sample,enforcement_enabled,promotion_stage)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project_id,portfolio_id,strategy_id) DO UPDATE SET
		criteria_json=excluded.criteria_json,min_completed_runs=excluded.min_completed_runs,
		require_out_of_sample=excluded.require_out_of_sample,enforcement_enabled=excluded.enforcement_enabled,
		promotion_stage=excluded.promotion_stage,updated_at=CURRENT_TIMESTAMP`,
		p.ProjectID, p.PortfolioID, p.StrategyID, string(criteria), p.MinCompletedRuns,
		boolInt(p.RequireOutOfSample), boolInt(p.EnforcementEnabled), p.PromotionStage)
	if err != nil {
		return nil, err
	}
	return dbGetStrategyScorecardPolicy(db, p.ProjectID, p.PortfolioID, p.StrategyID)
}

func scorecardScope(run *BacktestRun) string {
	if run != nil && run.Summary != nil {
		if scope := strings.TrimSpace(fmt.Sprint(run.Summary["validation_period"])); scope != "" && scope != "<nil>" {
			return scope
		}
	}
	return "backtest"
}

func evaluateScorecardMetrics(policy *StrategyScorecardPolicy, metrics map[string]float64, scope string) ([]ScorecardCheck, bool) {
	values := make(map[string]float64, len(metrics)+1)
	for k, v := range metrics {
		values[k] = v
	}
	if drawdown, ok := values["max_drawdown_pct"]; ok {
		values["max_drawdown_abs_pct"] = math.Abs(drawdown)
	}
	checks := make([]ScorecardCheck, 0, len(policy.Criteria)+1)
	passed := true
	if policy.RequireOutOfSample {
		ok := scope == "out_of_sample"
		checks = append(checks, ScorecardCheck{Metric: "evaluation_scope", Operator: "out_of_sample", Required: true, Passed: ok, Detail: scope})
		passed = passed && ok
	}
	for _, criterion := range policy.Criteria {
		actual, exists := values[criterion.Metric]
		check := ScorecardCheck{Metric: criterion.Metric, Operator: criterion.Operator, Threshold: criterion.Threshold, Required: criterion.Required}
		if exists {
			check.Actual = &actual
			if criterion.Operator == "min" {
				check.Passed = actual >= criterion.Threshold
			} else {
				check.Passed = actual <= criterion.Threshold
			}
		} else {
			check.Detail = "metric unavailable"
			check.Passed = !criterion.Required
		}
		if criterion.Required && !check.Passed {
			passed = false
		}
		checks = append(checks, check)
	}
	return checks, passed
}

func scorecardPolicyHash(policy *StrategyScorecardPolicy) string {
	payload := struct {
		Criteria           []ScorecardCriterion `json:"criteria"`
		MinCompletedRuns   int                  `json:"min_completed_runs"`
		RequireOutOfSample bool                 `json:"require_out_of_sample"`
	}{Criteria: sortedCriteria(policy.Criteria), MinCompletedRuns: policy.MinCompletedRuns, RequireOutOfSample: policy.RequireOutOfSample}
	raw, _ := json.Marshal(payload)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func evaluateAndStoreStrategyScorecard(db *sql.DB, projectID string, portfolioID, strategyID, runID int64) (*StrategyScorecardEvaluation, error) {
	portfolio, err := dbGetPortfolio(db, projectID, portfolioID)
	if err != nil {
		return nil, err
	}
	if _, err := dbGetStrategy(db, projectID, strategyID); err != nil {
		return nil, err
	}
	run, err := dbGetBacktestRun(db, projectID, runID)
	if err != nil {
		return nil, err
	}
	if run.PortfolioID != portfolio.ID || run.StrategyID != strategyID {
		return nil, errors.New("backtest run does not belong to this portfolio and strategy")
	}
	if run.Status != "completed" {
		return nil, fmt.Errorf("backtest run is %s; a completed run is required", run.Status)
	}
	policy, err := dbGetStrategyScorecardPolicy(db, projectID, portfolioID, strategyID)
	if err != nil {
		return nil, err
	}
	performance, err := backtestPerformance(run)
	if err != nil {
		return nil, err
	}
	scope := scorecardScope(run)
	checks, passed := evaluateScorecardMetrics(policy, performance.Metrics, scope)
	verdict := "pass"
	if !passed {
		verdict = "fail"
	}
	datasetSHA := ""
	if run.Summary != nil {
		if value, ok := run.Summary["dataset_sha256"]; ok && value != nil {
			datasetSHA = strings.TrimSpace(fmt.Sprint(value))
		}
	}

	metricsJSON, _ := json.Marshal(performance.Metrics)
	checksJSON, _ := json.Marshal(checks)
	policyJSON, _ := json.Marshal(policy)
	policyHash := scorecardPolicyHash(policy)
	res, err := db.Exec(`INSERT INTO strategy_scorecard_evaluations
		(project_id,portfolio_id,strategy_id,strategy_version,backtest_run_id,evaluation_scope,passed,verdict,metrics_json,checks_json,policy_json,policy_hash,dataset_sha256)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, projectID, portfolioID, strategyID, run.StrategyVersion, runID, scope,
		boolInt(passed), verdict, string(metricsJSON), string(checksJSON), string(policyJSON), policyHash, datasetSHA)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetStrategyScorecardEvaluation(db, projectID, id)
}

func dbGetStrategyScorecardEvaluation(db *sql.DB, projectID string, id int64) (*StrategyScorecardEvaluation, error) {
	row := db.QueryRow(`SELECT id,project_id,portfolio_id,strategy_id,strategy_version,backtest_run_id,evaluation_scope,passed,verdict,
		metrics_json,checks_json,policy_json,policy_hash,dataset_sha256,evaluated_at FROM strategy_scorecard_evaluations WHERE id=? AND project_id=?`, id, projectID)
	return scanStrategyScorecardEvaluation(row)
}

func dbListStrategyScorecardEvaluations(db *sql.DB, projectID string, portfolioID, strategyID int64, limit int) ([]*StrategyScorecardEvaluation, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := db.Query(`SELECT id,project_id,portfolio_id,strategy_id,strategy_version,backtest_run_id,evaluation_scope,passed,verdict,
		metrics_json,checks_json,policy_json,policy_hash,dataset_sha256,evaluated_at FROM strategy_scorecard_evaluations
		WHERE project_id=? AND portfolio_id=? AND strategy_id=? ORDER BY id DESC LIMIT ?`, projectID, portfolioID, strategyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*StrategyScorecardEvaluation{}
	for rows.Next() {
		evaluation, err := scanStrategyScorecardEvaluation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, evaluation)
	}
	return out, rows.Err()
}

type scorecardScanner interface{ Scan(...any) error }

func scanStrategyScorecardEvaluation(scanner scorecardScanner) (*StrategyScorecardEvaluation, error) {
	e := &StrategyScorecardEvaluation{}
	var passed int
	var metrics, checks, policy string
	if err := scanner.Scan(&e.ID, &e.ProjectID, &e.PortfolioID, &e.StrategyID, &e.StrategyVersion, &e.BacktestRunID,
		&e.EvaluationScope, &passed, &e.Verdict, &metrics, &checks, &policy, &e.PolicyHash, &e.DatasetSHA256, &e.EvaluatedAt); err != nil {
		return nil, err
	}
	e.Passed = passed != 0
	_ = json.Unmarshal([]byte(metrics), &e.Metrics)
	_ = json.Unmarshal([]byte(checks), &e.Checks)
	_ = json.Unmarshal([]byte(policy), &e.Policy)
	return e, nil
}

func scorecardStageRank(stage string) int {
	switch stage {
	case "research":
		return 0
	case "paper_candidate":
		return 1
	case "paper":
		return 2
	case "live_candidate":
		return 3
	case "live":
		return 4
	default:
		return -1
	}
}

func scorecardAllowsExecution(db *sql.DB, portfolio *Portfolio, strategyID int64) (bool, string) {
	policy, err := dbGetStrategyScorecardPolicy(db, portfolio.ProjectID, portfolio.ID, strategyID)
	if err != nil {
		return false, err.Error()
	}
	if policy.PromotionStage == "suspended" {
		return false, "strategy scorecard is suspended"
	}
	if !policy.EnforcementEnabled {
		return true, "scorecard enforcement disabled"
	}
	required := 0
	switch normalizeExecutionEnvironment(portfolio.ExecutionEnvironment, portfolio.Mode, portfolio.BrokerSlug) {
	case "broker_paper":
		required = 2
	case "broker_live":
		required = 4
	}
	if scorecardStageRank(policy.PromotionStage) < required {
		return false, fmt.Sprintf("strategy stage %s is below the required stage for %s", policy.PromotionStage, portfolio.ExecutionEnvironment)
	}
	if required > 0 {
		count, err := currentPassingRuns(db, policy)
		if err != nil || count < policy.MinCompletedRuns {
			return false, "current strategy version lacks passing evidence"
		}
	}
	return true, "scorecard gate passed"
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func promoteStrategyScorecard(db *sql.DB, policy *StrategyScorecardPolicy, target string) (*StrategyScorecardPolicy, error) {
	if policy == nil {
		return nil, errors.New("scorecard policy required")
	}
	target = strings.ToLower(strings.TrimSpace(target))
	if !validPromotionStage(target) {
		return nil, errors.New("invalid promotion_stage")
	}
	if target == "suspended" {
		policy.PromotionStage = target
		return dbUpsertStrategyScorecardPolicy(db, *policy)
	}
	if policy.PromotionStage == "suspended" {
		if target != "research" {
			return nil, errors.New("a suspended scorecard must return to research before promotion")
		}
		policy.PromotionStage = target
		return dbUpsertStrategyScorecardPolicy(db, *policy)
	}
	currentRank, targetRank := scorecardStageRank(policy.PromotionStage), scorecardStageRank(target)
	if targetRank < currentRank {
		policy.PromotionStage = target
		return dbUpsertStrategyScorecardPolicy(db, *policy)
	}
	if targetRank == currentRank {
		return policy, nil
	}
	if targetRank != currentRank+1 {
		return nil, errors.New("promotion must advance one stage at a time")
	}
	passingRuns, err := currentPassingRuns(db, policy)
	if err != nil {
		return nil, err
	}
	if passingRuns < policy.MinCompletedRuns {
		return nil, fmt.Errorf("promotion requires %d passing completed run(s); have %d", policy.MinCompletedRuns, passingRuns)
	}
	policy.PromotionStage = target
	return dbUpsertStrategyScorecardPolicy(db, *policy)
}

func currentPassingRuns(db *sql.DB, policy *StrategyScorecardPolicy) (int, error) {
	pf, err := dbGetPortfolio(db, policy.ProjectID, policy.PortfolioID)
	if err != nil {
		return 0, err
	}
	strategy, err := dbGetStrategy(db, policy.ProjectID, policy.StrategyID)
	if err != nil {
		return 0, err
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return 0, err
	}
	current, err := captureReplayPolicy(db, pf, def.Universe)
	if err != nil {
		return 0, err
	}
	currentHash := replayPolicyHash(current)
	rows, err := db.Query(`SELECT DISTINCT e.backtest_run_id,b.summary_json FROM strategy_scorecard_evaluations e JOIN backtest_runs b ON b.id=e.backtest_run_id
 WHERE e.project_id=? AND e.portfolio_id=? AND e.strategy_id=? AND e.policy_hash=? AND e.passed=1 AND e.strategy_version=? AND b.status='completed'`, policy.ProjectID, policy.PortfolioID, policy.StrategyID, scorecardPolicyHash(policy), strategy.Version)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return 0, err
		}
		var summary map[string]any
		if err := json.Unmarshal([]byte(raw), &summary); err != nil {
			return 0, err
		}
		run := &BacktestRun{Summary: summary}
		captured := decodeReplayPolicy(run)
		if captured != nil && replayPolicyHash(captured) == currentHash {
			count++
		}
	}
	return count, rows.Err()
}

func sortedCriteria(criteria []ScorecardCriterion) []ScorecardCriterion {
	out := append([]ScorecardCriterion(nil), criteria...)
	sort.Slice(out, func(i, j int) bool { return out[i].Metric < out[j].Metric })
	return out
}

// MCP handlers live here to keep Trading governance cohesive and independent
// from the generic Analytics app.
func (a *App) toolPortfolioUniverseGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolio, err := dbGetPortfolio(ctx.AppDB(), pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, err
	}
	policy, err := dbGetPortfolioUniversePolicy(ctx.AppDB(), portfolio)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": policy, "allowed_classes": portfolio.AllowedClasses}, nil
}

func (a *App) toolPortfolioUniverseUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolio, err := dbGetPortfolio(ctx.AppDB(), pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, err
	}
	current, err := dbGetPortfolioUniversePolicy(ctx.AppDB(), portfolio)
	if err != nil {
		return nil, err
	}
	if value := strings.TrimSpace(strArg(args, "selection_mode")); value != "" {
		current.SelectionMode = value
	}
	if raw, exists := args["include_symbols"]; exists {
		value, ok := stringSliceValue(raw)
		if !ok {
			return nil, errors.New("include_symbols must be an array of symbols")
		}
		current.IncludeSymbols = value
	}
	if raw, exists := args["exclude_symbols"]; exists {
		value, ok := stringSliceValue(raw)
		if !ok {
			return nil, errors.New("exclude_symbols must be an array of symbols")
		}
		current.ExcludeSymbols = value
	}
	if _, ok := args["reference_universe_id"]; ok {
		current.ReferenceUniverseID = strArg(args, "reference_universe_id")
	}
	if value, ok := boolValue(args["require_active_listing"]); ok {
		current.RequireActiveListing = value
	}
	if value, ok := boolValue(args["enforcement_enabled"]); ok {
		current.EnforcementEnabled = value
	}
	saved, err := dbUpsertPortfolioUniversePolicy(ctx.AppDB(), portfolio, *current)
	if err != nil {
		return nil, err
	}
	emit("portfolio.universe.changed", map[string]any{"portfolio_id": portfolio.ID, "policy": saved})
	return map[string]any{"policy": saved, "allowed_classes": portfolio.AllowedClasses}, nil
}

func (a *App) toolStrategyScorecardGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID, strategyID := int64Arg(args, "portfolio_id", 0), int64Arg(args, "strategy_id", 0)
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID); err != nil {
		return nil, err
	}
	if _, err := dbGetStrategy(ctx.AppDB(), pid, strategyID); err != nil {
		return nil, err
	}
	policy, err := dbGetStrategyScorecardPolicy(ctx.AppDB(), pid, portfolioID, strategyID)
	if err != nil {
		return nil, err
	}
	evaluations, err := dbListStrategyScorecardEvaluations(ctx.AppDB(), pid, portfolioID, strategyID, intArg(args, "limit", 30))
	if err != nil {
		return nil, err
	}
	policy.Criteria = sortedCriteria(policy.Criteria)
	return map[string]any{"policy": policy, "evaluations": evaluations}, nil
}

func (a *App) toolStrategyScorecardUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID, strategyID := int64Arg(args, "portfolio_id", 0), int64Arg(args, "strategy_id", 0)
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID); err != nil {
		return nil, err
	}
	if _, err := dbGetStrategy(ctx.AppDB(), pid, strategyID); err != nil {
		return nil, err
	}
	policy, err := dbGetStrategyScorecardPolicy(ctx.AppDB(), pid, portfolioID, strategyID)
	if err != nil {
		return nil, err
	}
	if raw, ok := args["criteria"]; ok {
		buf, _ := json.Marshal(raw)
		if err := json.Unmarshal(buf, &policy.Criteria); err != nil {
			return nil, errors.New("criteria must be an array")
		}
	}
	if _, ok := args["min_completed_runs"]; ok {
		policy.MinCompletedRuns = intArg(args, "min_completed_runs", policy.MinCompletedRuns)
	}
	if value, ok := boolValue(args["require_out_of_sample"]); ok {
		policy.RequireOutOfSample = value
	}
	if value, ok := boolValue(args["enforcement_enabled"]); ok {
		policy.EnforcementEnabled = value
	}
	saved, err := dbUpsertStrategyScorecardPolicy(ctx.AppDB(), *policy)
	if err != nil {
		return nil, err
	}
	emit("strategy.scorecard.policy.changed", map[string]any{"portfolio_id": portfolioID, "strategy_id": strategyID, "policy": saved})
	return map[string]any{"policy": saved}, nil
}

func (a *App) toolStrategyScorecardEvaluate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	evaluation, err := evaluateAndStoreStrategyScorecard(ctx.AppDB(), pid, int64Arg(args, "portfolio_id", 0), int64Arg(args, "strategy_id", 0), int64Arg(args, "backtest_run_id", 0))
	if err != nil {
		return nil, err
	}
	emit("strategy.scorecard.evaluated", map[string]any{"portfolio_id": evaluation.PortfolioID, "strategy_id": evaluation.StrategyID, "evaluation": evaluation})
	return map[string]any{"evaluation": evaluation}, nil
}

func (a *App) toolStrategyPromotionUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID, strategyID := int64Arg(args, "portfolio_id", 0), int64Arg(args, "strategy_id", 0)
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID); err != nil {
		return nil, err
	}
	if _, err := dbGetStrategy(ctx.AppDB(), pid, strategyID); err != nil {
		return nil, err
	}
	policy, err := dbGetStrategyScorecardPolicy(ctx.AppDB(), pid, portfolioID, strategyID)
	if err != nil {
		return nil, err
	}
	saved, err := promoteStrategyScorecard(ctx.AppDB(), policy, strArg(args, "promotion_stage"))
	if err != nil {
		return nil, err
	}
	emit("strategy.promotion.changed", map[string]any{"portfolio_id": portfolioID, "strategy_id": strategyID, "promotion_stage": saved.PromotionStage})
	return map[string]any{"policy": saved}, nil
}

func stringSliceValue(value any) ([]string, bool) {
	if value == nil {
		return nil, false
	}
	buf, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var out []string
	if json.Unmarshal(buf, &out) != nil {
		return nil, false
	}
	return cleanSymbols(out), true
}

func boolValue(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	case float64:
		return v != 0, true
	case int:
		return v != 0, true
	}
	return false, false
}
