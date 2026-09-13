package main

import (
	"database/sql"
	"fmt"
	rr "github.com/teambition/rrule-go"
	"strings"
	"time"
)

// v0.3.x splits stored COUNT and UNTIL together. Convert their intersection to
// a finite UNTIL rule so upgrading preserves the old series end and new writes
// can enforce RFC 5545's mutually-exclusive COUNT/UNTIL contract.
func repairLegacyRules(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id,start_at,rrule FROM events WHERE upper(rrule) LIKE '%COUNT=%' AND upper(rrule) LIKE '%UNTIL=%'`)
	if err != nil {
		return err
	}
	type legacy struct {
		id          int64
		start, rule string
	}
	items := []legacy{}
	for rows.Next() {
		var e legacy
		if err := rows.Scan(&e.id, &e.start, &e.rule); err != nil {
			rows.Close()
			return err
		}
		items = append(items, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range items {
		start, err := parseFlexibleTime(e.start)
		if err != nil {
			return fmt.Errorf("legacy event %d: %w", e.id, err)
		}
		o, err := rr.StrToROption(strings.ToUpper(e.rule))
		if err != nil {
			return err
		}
		if start.Year() < 1900 || o.Count < 1 || o.Count > 110000 || o.Freq > rr.DAILY {
			return fmt.Errorf("legacy event %d has unsupported recurrence bounds", e.id)
		}
		o.Dtstart = start
		if o.Until.Year() > 2200 {
			return fmt.Errorf("legacy event %d has unsupported recurrence end", e.id)
		}
		rule, err := rr.NewRRule(*o)
		if err != nil {
			return err
		}
		next := rule.Iterator()
		last := start.Add(-time.Second)
		for at, ok := next(); ok; at, ok = next() {
			last = at
		}
		fixed := setRRulePart(setRRulePart(e.rule, "COUNT", ""), "UNTIL", last.UTC().Format("20060102T150405Z"))
		if _, err := tx.Exec(`UPDATE events SET rrule=? WHERE id=?`, fixed, e.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
