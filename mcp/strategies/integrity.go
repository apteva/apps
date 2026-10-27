package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"sync"
)

var rebalanceMu sync.Mutex
var strategyReplayMu sync.Mutex

func workingPortfolioOrders(db *sql.DB, id int64) ([]*Order, error) {

	return dbListOrdersInternal(db, id, "working", -1)
}

func persistRebalance(db *sql.DB, pf *Portfolio, s *Strategy, a *StrategyAssignment, eval *StrategyEvaluation) error {
	raw, err := json.Marshal(eval.TargetAllocations)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO strategy_rebalances(assignment_id,portfolio_id,strategy_id,strategy_version,targets_json) VALUES(?,?,?,?,?) ON CONFLICT(assignment_id) DO UPDATE SET targets_json=excluded.targets_json,strategy_version=excluded.strategy_version,status='pending',updated_at=CURRENT_TIMESTAMP WHERE strategy_rebalances.status='completed' OR strategy_rebalances.strategy_version!=excluded.strategy_version`, a.ID, pf.ID, s.ID, s.Version, string(raw))
	return err
}

func commitStrategyStep(db *sql.DB, runID int64, step int, summary map[string]any, status string, snap *BacktestSnapshot) error {
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE backtest_runs SET current_step=?,summary_json=json_patch(COALESCE(summary_json,'{}'),?),status=?,updated_at=CURRENT_TIMESTAMP,completed_at=CASE WHEN ?='completed' THEN CURRENT_TIMESTAMP ELSE completed_at END WHERE id=? AND status='running' AND current_step=?`, step, string(raw), status, status, runID, step-1)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("backtest step cancelled, paused, or already committed")
	}
	if err := dbUpsertBacktestSnapshot(tx, snap); err != nil {
		return err
	}
	return tx.Commit()
}

type replayExecutionPolicy struct {
	Risk        *PortfolioRiskPolicy             `json:"risk"`
	Allowed     map[string]bool                  `json:"allowed"`
	Profiles    map[string]VenueExecutionProfile `json:"profiles"`
	Instruments map[string]*Instrument           `json:"instruments"`
}

func captureReplayPolicy(db *sql.DB, pf *Portfolio, symbols []string) (*replayExecutionPolicy, error) {
	risk, err := dbGetPortfolioRiskPolicy(db, pf)
	if err != nil {
		return nil, err
	}
	p := &replayExecutionPolicy{Risk: risk, Allowed: map[string]bool{}, Profiles: map[string]VenueExecutionProfile{}, Instruments: map[string]*Instrument{}}
	for _, symbol := range symbols {
		v, err := portfolioUniverseViolation(db, pf, symbol)
		if err != nil {
			return nil, err
		}
		p.Allowed[symbol] = v == nil && contains(pf.AllowedClasses, inferAssetClass(symbol))
		p.Profiles[symbol] = resolveVenueProfile(db, pf, symbol, inferAssetClass(symbol))
		p.Instruments[symbol], _ = dbGetInstrument(db, symbol)
	}
	return p, nil
}
func decodeReplayPolicy(run *BacktestRun) *replayExecutionPolicy {
	raw, err := json.Marshal(run.Summary["execution_policy"])
	if err != nil {
		return nil
	}
	var p replayExecutionPolicy
	if json.Unmarshal(raw, &p) != nil || p.Risk == nil {
		return nil
	}
	return &p
}
func exposureBreach(policy *PortfolioRiskPolicy, equity, orderNotional, symbolValue, gross float64) *RiskBreach {
	if equity <= 0 {
		return &RiskBreach{Code: "risk_no_equity", Detail: "positive equity required"}
	}
	checks := []struct {
		actual, limit float64
		code          string
	}{{orderNotional / equity * 100, policy.MaxOrderPct, "risk_max_order"}, {(symbolValue + orderNotional) / equity * 100, policy.MaxPositionPct, "risk_max_position"}, {(gross + orderNotional) / equity * 100, policy.MaxGrossExposurePct, "risk_max_gross_exposure"}}
	for _, c := range checks {
		if c.actual > c.limit+1e-9 {
			return &RiskBreach{Code: c.code, Detail: fmt.Sprintf("projected %.2f%% exceeds %.2f%% maximum", c.actual, c.limit), ActualPct: c.actual, LimitPct: c.limit}
		}
	}
	return nil
}

func replayPolicyHash(policy *replayExecutionPolicy) string {
	raw, _ := json.Marshal(policy)
	var value any
	_ = json.Unmarshal(raw, &value)
	var strip func(any)
	strip = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range []string{"updated_at", "created_at", "received_at", "runtime"} {
				delete(x, k)
			}
			for _, child := range x {
				strip(child)
			}
		case []any:
			for _, child := range x {
				strip(child)
			}
		}
	}
	strip(value)
	raw, _ = json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
