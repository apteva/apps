package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"math"
)

// Refuse valuations that would otherwise silently use the legacy 1:1 fallback.
// The target may be empty for account-local views, which only need holding FX.
func validateValuationRates(ctx *sdk.AppCtx, target string, accountID int64) error {
	rows, err := ctx.AppDB().Query(`
 SELECT DISTINCT a.currency, ? FROM accounts a WHERE a.project_id=? AND (?=0 OR a.id=?)
 UNION
 SELECT DISTINCT i.quote_currency,a.currency FROM holdings h
 JOIN accounts a ON a.id=h.account_id JOIN instruments i ON i.id=h.instrument_id
 WHERE a.project_id=? AND (?=0 OR a.id=?)`, target, projectID(ctx), accountID, accountID, projectID(ctx), accountID, accountID)
	if err != nil {
		return err
	}
	pairs := [][2]string{}
	for rows.Next() {
		var p [2]string
		if err = rows.Scan(&p[0], &p[1]); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if p[0] == p[1] || p[1] == "" {
			continue
		}
		available := false
		for _, pair := range [][2]string{p, {p[1], p[0]}} {
			var rate float64
			e := ctx.AppDB().QueryRow(`SELECT rate FROM fx_rates WHERE base=? AND quote=? ORDER BY as_of DESC LIMIT 1`, pair[0], pair[1]).Scan(&rate)
			if e == nil && rate > 0 && !math.IsNaN(rate) && !math.IsInf(rate, 0) {
				available = true
				break
			}
		}
		if !available {
			return fmt.Errorf("missing exchange rate %s/%s; set an FX rate before calculating this valuation", p[0], p[1])
		}
	}
	return nil
}
