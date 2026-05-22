package main

// store — all SQLite access for the stocks app. Three tables: the seeded
// instrument universe (with a lazily-warmed price/yield snapshot), the
// dividend payment history, and a generic TTL blob cache for the
// volatile get/chart responses. See migrations/001_init.sql.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type store struct{ db *sql.DB }

// instrumentCols is the column list every instrumentRow query selects, in
// the order scanInstruments expects. Kept in one place so the two callers
// (list, search) can't drift from the scan.
const instrumentCols = `symbol, name, exchange, sector, currency,
	last_price, last_change_pct, last_yield_pct, last_pe, last_payout_pct,
	last_growth_pct, refreshed_at`

// instrumentRow is one universe entry plus its last-warmed snapshot.
// Pointer fields are nil until the symbol has been fetched at least once.
type instrumentRow struct {
	Symbol      string   `json:"symbol"`
	Name        string   `json:"name"`
	Exchange    string   `json:"exchange"`
	Sector      string   `json:"sector"`
	Currency    string   `json:"currency"`
	Price       *float64 `json:"price,omitempty"`
	ChangePct   *float64 `json:"change_pct,omitempty"`
	YieldPct    *float64 `json:"yield_pct,omitempty"`
	PE          *float64 `json:"pe,omitempty"`
	PayoutPct   *float64 `json:"payout_pct,omitempty"`
	GrowthPct   *float64 `json:"growth_pct,omitempty"`
	RefreshedAt *int64   `json:"refreshed_at,omitempty"`
}

// listFilter carries the optional screener constraints for listUniverse.
// Nil pointers mean "no constraint"; rows missing a filtered metric are
// excluded from that filter (and sorted last when it's the sort key).
type listFilter struct {
	Sector    string
	MinYield  *float64
	MaxPayout *float64
	MaxPE     *float64
	MinGrowth *float64
	Sort      string // name | price | change | yield | pe | payout | growth
	Limit     int
}

// ensureInstrument inserts a symbol into the universe if absent, leaving
// any existing row (and its snapshot) untouched. Used to grow the
// universe when search/get encounters a new valid ticker.
func (s *store) ensureInstrument(symbol, name, exchange, currency string) error {
	_, err := s.db.Exec(
		`INSERT INTO instrument (symbol, name, exchange, currency)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(symbol) DO UPDATE SET
		   name     = CASE WHEN instrument.name = '' THEN excluded.name ELSE instrument.name END,
		   exchange = CASE WHEN instrument.exchange = '' THEN excluded.exchange ELSE instrument.exchange END`,
		strings.ToUpper(symbol), name, exchange, orStr(currency, "USD"),
	)
	return err
}

// updateSnapshot writes the lazily-warmed price/change/yield/growth
// columns (all from the /chart fetch) and stamps refreshed_at. yieldPct
// and growthPct may be nil (unknown). Fundamentals (P/E, payout) come
// from a separate endpoint — see updateFundamentals.
func (s *store) updateSnapshot(symbol string, price, changePct float64, yieldPct, growthPct *float64) error {
	_, err := s.db.Exec(
		`UPDATE instrument
		    SET last_price = ?, last_change_pct = ?, last_yield_pct = ?,
		        last_growth_pct = ?, refreshed_at = ?
		  WHERE symbol = ?`,
		nullF(price), nullF(changePct), yieldPct, growthPct, time.Now().Unix(), strings.ToUpper(symbol),
	)
	return err
}

// updateFundamentals writes P/E + payout (%) from the quoteSummary
// endpoint. Left untouched by updateSnapshot so a /chart-only warm never
// clears a previously-fetched fundamental.
func (s *store) updateFundamentals(symbol string, pe, payoutPct *float64) error {
	_, err := s.db.Exec(
		`UPDATE instrument SET last_pe = ?, last_payout_pct = ? WHERE symbol = ?`,
		pe, payoutPct, strings.ToUpper(symbol))
	return err
}

// touch records that a symbol was just opened, putting it in the warmer's
// hot tier so the list snapshot for stocks the user actually looks at
// stays fresh on a short cycle.
func (s *store) touch(symbol string) error {
	_, err := s.db.Exec(`UPDATE instrument SET viewed_at = ? WHERE symbol = ?`,
		time.Now().Unix(), strings.ToUpper(symbol))
	return err
}

// warmCandidates returns up to `limit` symbols to refresh next, prioritized:
//  1. recently-viewed symbols stale beyond hotTTL (the hot tier),
//  2. then never-warmed symbols,
//  3. then the oldest cold symbols stale beyond coldTTL.
// This keeps what the user is looking at fresh while the rest of the
// universe trickles within the global rate budget.
func (s *store) warmCandidates(limit int, coldTTL, hotTTL, hotWindow time.Duration) ([]string, error) {
	now := time.Now().Unix()
	coldCut := now - int64(coldTTL.Seconds())
	hotCut := now - int64(hotTTL.Seconds())
	hotWin := now - int64(hotWindow.Seconds())
	rows, err := s.db.Query(
		`SELECT symbol FROM instrument
		  WHERE refreshed_at IS NULL
		     OR refreshed_at < ?
		     OR (viewed_at >= ? AND (refreshed_at IS NULL OR refreshed_at < ?))
		  ORDER BY (viewed_at IS NOT NULL AND viewed_at >= ?) DESC,
		           refreshed_at IS NOT NULL, refreshed_at ASC
		  LIMIT ?`,
		coldCut, hotWin, hotCut, hotWin, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sym string
		if err := rows.Scan(&sym); err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}

// listUniverse returns the universe filtered + sorted for the list tool.
func (s *store) listUniverse(f listFilter) ([]instrumentRow, error) {
	var where []string
	var args []any
	if f.Sector != "" {
		where = append(where, "LOWER(sector) = LOWER(?)")
		args = append(args, f.Sector)
	}
	if f.MinYield != nil {
		where = append(where, "last_yield_pct IS NOT NULL AND last_yield_pct >= ?")
		args = append(args, *f.MinYield)
	}
	if f.MaxPayout != nil {
		where = append(where, "last_payout_pct IS NOT NULL AND last_payout_pct <= ?")
		args = append(args, *f.MaxPayout)
	}
	if f.MaxPE != nil {
		where = append(where, "last_pe IS NOT NULL AND last_pe > 0 AND last_pe <= ?")
		args = append(args, *f.MaxPE)
	}
	if f.MinGrowth != nil {
		where = append(where, "last_growth_pct IS NOT NULL AND last_growth_pct >= ?")
		args = append(args, *f.MinGrowth)
	}
	q := "SELECT " + instrumentCols + " FROM instrument"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// "<col> IS NULL" first in ORDER BY pushes un-warmed rows to the
	// bottom (SQLite sorts false<true), so the metric sorts are dense.
	switch f.Sort {
	case "price":
		q += " ORDER BY last_price IS NULL, last_price DESC"
	case "change":
		q += " ORDER BY last_change_pct IS NULL, last_change_pct DESC"
	case "yield":
		q += " ORDER BY last_yield_pct IS NULL, last_yield_pct DESC"
	case "pe":
		q += " ORDER BY last_pe IS NULL OR last_pe <= 0, last_pe ASC"
	case "payout":
		q += " ORDER BY last_payout_pct IS NULL, last_payout_pct ASC"
	case "growth":
		q += " ORDER BY last_growth_pct IS NULL, last_growth_pct DESC"
	default:
		q += " ORDER BY symbol ASC"
	}
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	return s.scanInstruments(q, args...)
}

// searchUniverse matches symbol prefix or name substring, case-insensitive.
func (s *store) searchUniverse(query string, limit int) ([]instrumentRow, error) {
	q := strings.TrimSpace(query)
	like := "%" + strings.ToLower(q) + "%"
	prefix := strings.ToUpper(q) + "%"
	return s.scanInstruments(
		`SELECT `+instrumentCols+`
		   FROM instrument
		  WHERE symbol LIKE ? OR LOWER(name) LIKE ?
		  ORDER BY (symbol = ?) DESC, (symbol LIKE ?) DESC, symbol ASC
		  LIMIT ?`,
		prefix, like, strings.ToUpper(q), prefix, limit,
	)
}

func (s *store) scanInstruments(q string, args ...any) ([]instrumentRow, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []instrumentRow{}
	for rows.Next() {
		var r instrumentRow
		var price, change, yield, pe, payout, growth sql.NullFloat64
		var refreshed sql.NullInt64
		if err := rows.Scan(&r.Symbol, &r.Name, &r.Exchange, &r.Sector, &r.Currency,
			&price, &change, &yield, &pe, &payout, &growth, &refreshed); err != nil {
			return nil, err
		}
		if price.Valid {
			r.Price = &price.Float64
		}
		if change.Valid {
			r.ChangePct = &change.Float64
		}
		if yield.Valid {
			r.YieldPct = &yield.Float64
		}
		if pe.Valid {
			r.PE = &pe.Float64
		}
		if payout.Valid {
			r.PayoutPct = &payout.Float64
		}
		if growth.Valid {
			r.GrowthPct = &growth.Float64
		}
		if refreshed.Valid {
			r.RefreshedAt = &refreshed.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// saveDividends upserts the payment history for a symbol. Historical
// payments never change, so this is idempotent.
func (s *store) saveDividends(symbol string, divs []Dividend) error {
	if len(divs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT INTO dividend (symbol, ex_date, amount) VALUES (?, ?, ?)
		 ON CONFLICT(symbol, ex_date) DO UPDATE SET amount = excluded.amount`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	sym := strings.ToUpper(symbol)
	for _, d := range divs {
		if _, err := stmt.Exec(sym, d.ExDate, d.Amount); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// loadDividends returns a symbol's payment history, newest first.
func (s *store) loadDividends(symbol string) ([]Dividend, error) {
	rows, err := s.db.Query(
		`SELECT ex_date, amount FROM dividend WHERE symbol = ? ORDER BY ex_date DESC`,
		strings.ToUpper(symbol))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Dividend{}
	for rows.Next() {
		var d Dividend
		if err := rows.Scan(&d.ExDate, &d.Amount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// syncStats is the warming progress snapshot for the sync_status view.
type syncStats struct {
	Universe         int    `json:"universe"`
	Warmed           int    `json:"warmed"` // refreshed at least once
	WithPrice        int    `json:"with_price"`
	WithYield        int    `json:"with_yield"`
	WithFundamentals int    `json:"with_fundamentals"`
	Fresh            int    `json:"fresh"` // refreshed within ttl
	OldestRefresh    *int64 `json:"oldest_refresh,omitempty"`
	NewestRefresh    *int64 `json:"newest_refresh,omitempty"`
}

// stats counts how much of the universe has been warmed, for the sync view.
func (s *store) stats(ttl time.Duration) (syncStats, error) {
	cutoff := time.Now().Add(-ttl).Unix()
	var st syncStats
	var warmed, price, yield, fund, fresh, oldest, newest sql.NullInt64
	err := s.db.QueryRow(`SELECT
		COUNT(*),
		SUM(refreshed_at IS NOT NULL),
		SUM(last_price IS NOT NULL),
		SUM(last_yield_pct IS NOT NULL),
		SUM(last_pe IS NOT NULL OR last_payout_pct IS NOT NULL),
		SUM(refreshed_at >= ?),
		MIN(refreshed_at),
		MAX(refreshed_at)
	  FROM instrument`, cutoff).
		Scan(&st.Universe, &warmed, &price, &yield, &fund, &fresh, &oldest, &newest)
	if err != nil {
		return st, err
	}
	st.Warmed = int(warmed.Int64)
	st.WithPrice = int(price.Int64)
	st.WithYield = int(yield.Int64)
	st.WithFundamentals = int(fund.Int64)
	st.Fresh = int(fresh.Int64)
	if oldest.Valid {
		st.OldestRefresh = &oldest.Int64
	}
	if newest.Valid {
		st.NewestRefresh = &newest.Int64
	}
	return st, nil
}

// cacheGet returns the cached JSON for key when it's younger than maxAge.
func (s *store) cacheGet(key string, maxAge time.Duration) (json.RawMessage, bool) {
	var body string
	var fetched int64
	err := s.db.QueryRow(`SELECT json, fetched_at FROM cache WHERE key = ?`, key).Scan(&body, &fetched)
	if err != nil {
		return nil, false
	}
	if time.Since(time.Unix(fetched, 0)) > maxAge {
		return nil, false
	}
	return json.RawMessage(body), true
}

// cacheSet stores v as JSON under key with the current timestamp.
func (s *store) cacheSet(key string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO cache (key, json, fetched_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET json = excluded.json, fetched_at = excluded.fetched_at`,
		key, string(body), time.Now().Unix())
	return err
}

// nullF maps a zero float to SQL NULL so an absent price/change doesn't
// masquerade as a real 0.0 in the snapshot columns.
func nullF(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}
