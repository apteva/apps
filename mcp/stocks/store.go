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
	RefreshedAt *int64   `json:"refreshed_at,omitempty"`
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

// updateSnapshot writes the lazily-warmed price/change/yield columns and
// stamps refreshed_at. yieldPct may be nil (no dividends → unknown).
func (s *store) updateSnapshot(symbol string, price, changePct float64, yieldPct *float64) error {
	_, err := s.db.Exec(
		`UPDATE instrument
		    SET last_price = ?, last_change_pct = ?, last_yield_pct = ?, refreshed_at = ?
		  WHERE symbol = ?`,
		nullF(price), nullF(changePct), yieldPct, time.Now().Unix(), strings.ToUpper(symbol),
	)
	return err
}

// staleSymbols returns universe symbols whose snapshot is older than ttl
// (or never warmed). Used by list to bound how many Yahoo calls a refresh
// fans out.
func (s *store) staleSymbols(ttl time.Duration) ([]string, error) {
	cutoff := time.Now().Add(-ttl).Unix()
	rows, err := s.db.Query(
		`SELECT symbol FROM instrument
		  WHERE refreshed_at IS NULL OR refreshed_at < ?
		  ORDER BY symbol`, cutoff)
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
// sortBy ∈ {name, price, change, yield}; minYield (when non-nil) drops
// rows whose yield is unknown or below the floor.
func (s *store) listUniverse(sector, sortBy string, minYield *float64, limit int) ([]instrumentRow, error) {
	var where []string
	var args []any
	if sector != "" {
		where = append(where, "LOWER(sector) = LOWER(?)")
		args = append(args, sector)
	}
	if minYield != nil {
		where = append(where, "last_yield_pct IS NOT NULL AND last_yield_pct >= ?")
		args = append(args, *minYield)
	}
	q := `SELECT symbol, name, exchange, sector, currency,
	             last_price, last_change_pct, last_yield_pct, refreshed_at
	        FROM instrument`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// NULLS LAST on the metric sorts so un-warmed rows fall to the bottom.
	switch sortBy {
	case "price":
		q += " ORDER BY last_price IS NULL, last_price DESC"
	case "change":
		q += " ORDER BY last_change_pct IS NULL, last_change_pct DESC"
	case "yield":
		q += " ORDER BY last_yield_pct IS NULL, last_yield_pct DESC"
	default:
		q += " ORDER BY symbol ASC"
	}
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	return s.scanInstruments(q, args...)
}

// searchUniverse matches symbol prefix or name substring, case-insensitive.
func (s *store) searchUniverse(query string, limit int) ([]instrumentRow, error) {
	q := strings.TrimSpace(query)
	like := "%" + strings.ToLower(q) + "%"
	prefix := strings.ToUpper(q) + "%"
	return s.scanInstruments(
		`SELECT symbol, name, exchange, sector, currency,
		        last_price, last_change_pct, last_yield_pct, refreshed_at
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
		var price, change, yield sql.NullFloat64
		var refreshed sql.NullInt64
		if err := rows.Scan(&r.Symbol, &r.Name, &r.Exchange, &r.Sector, &r.Currency,
			&price, &change, &yield, &refreshed); err != nil {
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
