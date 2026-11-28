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
	// Trading 0.19.0 fails legacy strategy provenance closed; Strategies
	// gates its own orders before they reach Trading.
	var sid int64
	if err := db.QueryRow(`SELECT strategy_id FROM orders WHERE id=?`, o.ID).Scan(&sid); err != nil {
		return err
	}
	if o.Source == "strategy" || sid > 0 {
		return errors.New("legacy Trading strategy order")
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
