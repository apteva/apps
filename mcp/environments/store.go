package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type store struct{ db *sql.DB }

func (s store) listDefinitions() ([]Definition, error) {
	rows, err := s.db.Query(`SELECT id,name,description,desired_state,spec_version,spec_json,created_at,updated_at FROM environment_definitions ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Definition{}
	for rows.Next() {
		var d Definition
		var raw, created, updated string
		if err := rows.Scan(&d.ID, &d.Name, &d.Description, &d.DesiredState, &d.SpecVersion, &raw, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &d.Spec); err != nil {
			return nil, fmt.Errorf("definition %s spec: %w", d.ID, err)
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if run, _ := s.activeRun(d.ID); run != nil {
			d.ActiveRun = run
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s store) getDefinition(id string) (*Definition, error) {
	var d Definition
	var raw, created, updated string
	err := s.db.QueryRow(`SELECT id,name,description,desired_state,spec_version,spec_json,created_at,updated_at FROM environment_definitions WHERE id=?`, id).Scan(&d.ID, &d.Name, &d.Description, &d.DesiredState, &d.SpecVersion, &raw, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &d.Spec); err != nil {
		return nil, err
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	d.ActiveRun, _ = s.activeRun(id)
	return &d, nil
}

func (s store) saveDefinition(d *Definition) error {
	raw, err := rawSpec(d.Spec)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	if d.SpecVersion == 0 {
		d.SpecVersion = 1
	}
	if d.DesiredState == "" {
		d.DesiredState = "stopped"
	}
	_, err = s.db.Exec(`INSERT INTO environment_definitions(id,name,description,desired_state,spec_version,spec_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,desired_state=excluded.desired_state,spec_version=excluded.spec_version,spec_json=excluded.spec_json,updated_at=excluded.updated_at`, d.ID, d.Name, d.Description, d.DesiredState, d.SpecVersion, raw, d.CreatedAt.Format(time.RFC3339Nano), d.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s store) deleteDefinition(id string) error {
	_, err := s.db.Exec(`DELETE FROM environment_definitions WHERE id=?`, id)
	return err
}
func (s store) setDesired(id, state string) error {
	_, err := s.db.Exec(`UPDATE environment_definitions SET desired_state=?,updated_at=? WHERE id=?`, state, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s store) createRun(r *Run) error {
	_, err := s.db.Exec(`INSERT INTO environment_runs(id,environment_id,runtime_id,kind,status,error,started_at) VALUES(?,?,?,?,?,?,?)`, r.ID, r.EnvironmentID, r.RuntimeID, r.Kind, r.Status, r.Error, r.StartedAt.Format(time.RFC3339Nano))
	return err
}
func (s store) updateRun(id, status, msg string) error {
	var stopped any
	if status == "stopped" || status == "failed" || status == "expired" {
		stopped = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`UPDATE environment_runs SET status=?,error=?,stopped_at=COALESCE(?,stopped_at) WHERE id=?`, status, msg, stopped, id)
	return err
}
func (s store) activeRun(environmentID string) (*Run, error) {
	row := s.db.QueryRow(`SELECT id,environment_id,runtime_id,kind,status,error,started_at,stopped_at FROM environment_runs WHERE environment_id=? AND status IN ('starting','running','stopping') ORDER BY started_at DESC LIMIT 1`, environmentID)
	return scanRun(row)
}
func (s store) getRun(id string) (*Run, error) {
	return scanRun(s.db.QueryRow(`SELECT id,environment_id,runtime_id,kind,status,error,started_at,stopped_at FROM environment_runs WHERE id=? OR runtime_id=? LIMIT 1`, id, id))
}
func (s store) listRuns() ([]Run, error) {
	rows, err := s.db.Query(`SELECT id,environment_id,runtime_id,kind,status,error,started_at,stopped_at FROM environment_runs ORDER BY started_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanRun(row rowScanner) (*Run, error) {
	var r Run
	var started string
	var stopped sql.NullString
	err := row.Scan(&r.ID, &r.EnvironmentID, &r.RuntimeID, &r.Kind, &r.Status, &r.Error, &started, &stopped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if stopped.Valid {
		t, _ := time.Parse(time.RFC3339Nano, stopped.String)
		r.StoppedAt = &t
	}
	return &r, nil
}

func (s store) saveSnapshot(x Snapshot) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO environment_snapshots(id,environment_id,description,created_at) VALUES(?,?,?,?)`, x.ID, x.EnvironmentID, x.Description, x.CreatedAt.Format(time.RFC3339Nano))
	return err
}
func (s store) deleteSnapshot(id string) error {
	_, err := s.db.Exec(`DELETE FROM environment_snapshots WHERE id=?`, id)
	return err
}

func (s store) setting(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM environment_settings WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s store) setSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO environment_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
