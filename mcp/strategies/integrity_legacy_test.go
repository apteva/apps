package main

import (
	"database/sql"

	"errors"

	"time"
)

func validateWorkingPolicy(db *sql.DB, pf *Portfolio, o *Order) error {
	if isBuySide(o.Side) {
		v, err := portfolioUniverseViolation(db, pf, o.Symbol)
		if err != nil {
			return err
		}
		if v != nil {
			return errors.New(v.Detail)
		}
	}
	var sid int64
	var version int
	if err := db.QueryRow(`SELECT strategy_id,strategy_version FROM orders WHERE id=?`, o.ID).Scan(&sid, &version); err != nil {
		return err
	}
	if o.Source == "strategy" && sid == 0 {
		return errors.New("strategy order provenance unavailable")
	}
	if sid > 0 {
		s, err := dbGetStrategy(db, pf.ProjectID, sid)
		if err != nil {
			return err
		}
		if s.Version != version {
			return errors.New("strategy version changed")
		}
		if allowed, reason := scorecardAllowsExecution(db, pf, sid); !allowed {
			return errors.New(reason)
		}
	}
	return nil
}

func executionTime(db *sql.DB, portfolioID int64) time.Time {
	var at string
	if err := db.QueryRow(`SELECT replay_at FROM replay_steps WHERE portfolio_id=?`, portfolioID).Scan(&at); err == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, at); err == nil {
			return parsed
		}
	}
	return time.Now().UTC()
}
