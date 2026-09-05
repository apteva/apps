package main

import (
	"database/sql"
	"encoding/json"
	"errors"
)

func updateObjectiveTargets(tx *sql.Tx, objectiveID int64, targets []ObjectiveTarget, now int64) error {
	rows, err := tx.Query(`SELECT id,name,query_json FROM objective_targets WHERE objective_id=? AND retired_at IS NULL`, objectiveID)
	if err != nil {
		return err
	}
	type identity struct {
		id          int64
		name, query string
	}
	previous := []identity{}
	for rows.Next() {
		var v identity
		if err = rows.Scan(&v.id, &v.name, &v.query); err != nil {
			rows.Close()
			return err
		}
		previous = append(previous, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	used := map[int64]bool{}
	for _, target := range targets {
		raw, err := json.Marshal(target.Query)
		if err != nil {
			return err
		}
		// Compatibility for old clients: an unambiguous unchanged named query keeps its ID.
		if target.ID == 0 {
			for _, old := range previous {
				if old.name == target.Name && old.query == string(raw) && !used[old.id] {
					if target.ID != 0 {
						return errors.New("ambiguous target identity; supply target IDs")
					}
					target.ID = old.id
				}
			}
		}
		if target.ID == 0 {
			if err := replaceObjectiveTargets(tx, objectiveID, []ObjectiveTarget{target}, now); err != nil {
				return err
			}
			continue
		}
		found := false
		for _, old := range previous {
			if old.id == target.ID {
				found = true
			}
		}
		if !found || used[target.ID] {
			return errors.New("target ID must identify a unique active target of this objective")
		}
		used[target.ID] = true
		// Clear cached measurements whenever query or accounting period changes.
		_, err = tx.Exec(`DELETE FROM objective_progress WHERE target_id=? AND EXISTS(SELECT 1 FROM objective_targets WHERE id=? AND (query_json!=? OR period_start!=? OR period_end!=?))`, target.ID, target.ID, string(raw), target.PeriodStart, target.PeriodEnd)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE objective_targets SET name=?,metric_key=?,target_value=?,unit=?,currency=?,direction=?,period_start=?,period_end=?,timezone=?,query_json=?,updated_at=MAX(updated_at+1,?) WHERE id=? AND objective_id=?`, target.Name, target.MetricKey, target.TargetValue, target.Unit, target.Currency, target.Direction, target.PeriodStart, target.PeriodEnd, target.Timezone, string(raw), now, target.ID, objectiveID)
		if err != nil {
			return err
		}
	}
	for _, old := range previous {
		if !used[old.id] {
			if _, err := tx.Exec(`UPDATE objective_targets SET retired_at=? WHERE id=?`, now, old.id); err != nil {
				return err
			}
		}
	}
	return nil
}
