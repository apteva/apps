package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"

	"sync"
)

var rebalanceMu sync.Mutex
var strategyReplayMu sync.Mutex

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

// captureReplayPolicy reads the portfolio's execution policy for symbols
// from Trading. Backtests freeze it into their summary.
func captureReplayPolicy(ctx *sdk.AppCtx, pid string, portfolioID int64, symbols []string) (*TradingState, *replayExecutionPolicy, error) {
	state, err := newTradingReader(ctx).State(pid, portfolioID, symbols)
	if err != nil {
		return nil, nil, err
	}
	return state, replayPolicyFromState(state, symbols), nil
}

func replayPolicyFromState(state *TradingState, symbols []string) *replayExecutionPolicy {
	pf := state.Portfolio
	p := &replayExecutionPolicy{Risk: state.RiskPolicy, Allowed: map[string]bool{}, Profiles: map[string]VenueExecutionProfile{}, Instruments: map[string]*Instrument{}}
	for _, symbol := range symbols {
		key := canonicalSymbol(symbol)
		p.Allowed[symbol] = state.Violations[key] == nil && contains(pf.AllowedClasses, inferAssetClass(symbol))
		p.Profiles[symbol] = state.Profile(symbol)
		p.Instruments[symbol] = state.Instruments[key]
	}
	return p
}
