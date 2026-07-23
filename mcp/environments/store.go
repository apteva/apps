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
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if run, err := s.activeRun(out[i].ID); err != nil {
			return nil, err
		} else if run != nil {
			out[i].ActiveRun = run
		}
	}
	return out, nil
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
	_, err := s.db.Exec(`UPDATE environment_runs SET status=?,error=?,stopped_at=? WHERE id=?`, status, msg, stopped, id)
	return err
}

func (s store) transitionRun(id, from, status, msg string) (bool, error) {
	var stopped any
	if status == "stopped" || status == "failed" || status == "expired" {
		stopped = time.Now().UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.Exec(`UPDATE environment_runs SET status=?,error=?,stopped_at=? WHERE id=? AND status=?`, status, msg, stopped, id, from)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s store) activeRun(environmentID string) (*Run, error) {
	row := s.db.QueryRow(`SELECT id,environment_id,runtime_id,kind,status,error,started_at,stopped_at FROM environment_runs WHERE environment_id=? AND status IN ('starting','running','stopping') ORDER BY started_at DESC LIMIT 1`, environmentID)
	return scanRun(row)
}

func (s store) activeRuns(environmentID string) ([]Run, error) {
	rows, err := s.db.Query(`SELECT id,environment_id,runtime_id,kind,status,error,started_at,stopped_at FROM environment_runs WHERE environment_id=? AND status IN ('starting','running','stopping') ORDER BY started_at DESC`, environmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		if run != nil {
			out = append(out, *run)
		}
	}
	return out, rows.Err()
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

func (s store) saveVoiceCall(call *VoiceCall) error {
	spec, err := json.Marshal(call.Spec)
	if err != nil {
		return err
	}
	result, err := json.Marshal(call)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	created := call.StartedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err = s.db.Exec(`INSERT INTO environment_voice_calls(id,run_id,status,spec_json,result_json,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status,result_json=excluded.result_json,error=excluded.error,updated_at=excluded.updated_at`,
		call.ID, call.RunID, call.Status, string(spec), string(result), call.Error, created.Format(time.RFC3339Nano), now)
	return err
}

func (s store) getVoiceCall(id string) (*VoiceCall, error) {
	var raw string
	err := s.db.QueryRow(`SELECT result_json FROM environment_voice_calls WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var call VoiceCall
	if err := json.Unmarshal([]byte(raw), &call); err != nil {
		return nil, err
	}
	return &call, nil
}

func (s store) listVoiceCalls(runID string) ([]VoiceCall, error) {
	rows, err := s.db.Query(`SELECT result_json FROM environment_voice_calls WHERE run_id=? ORDER BY created_at DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VoiceCall{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var call VoiceCall
		if err := json.Unmarshal([]byte(raw), &call); err != nil {
			return nil, err
		}
		out = append(out, call)
	}
	return out, rows.Err()
}

func (s store) createWebFixture(x *WebFixtureInstance) error {
	seed, err := json.Marshal(x.Seed)
	if err != nil {
		return err
	}
	initial, err := json.Marshal(x.InitialState)
	if err != nil {
		return err
	}
	state, err := json.Marshal(x.State)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if x.CreatedAt.IsZero() {
		x.CreatedAt = now
	}
	x.UpdatedAt = now
	if x.Status == "" {
		x.Status = "running"
	}
	_, err = s.db.Exec(`INSERT INTO environment_web_fixtures(run_id,fixture_id,pack,pack_version,scenario,token,seed_json,initial_state_json,state_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, x.RunID, x.ID, x.Pack, x.Version, x.Scenario, x.Token, string(seed), string(initial), string(state), x.Status, x.CreatedAt.Format(time.RFC3339Nano), x.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s store) getWebFixture(runID, fixtureID string) (*WebFixtureInstance, error) {
	row := s.db.QueryRow(`SELECT run_id,fixture_id,pack,pack_version,scenario,token,seed_json,initial_state_json,state_json,status,created_at,updated_at FROM environment_web_fixtures WHERE run_id=? AND fixture_id=?`, runID, fixtureID)
	return scanWebFixture(row)
}

func (s store) getWebFixtureByToken(runID, fixtureID, token string) (*WebFixtureInstance, error) {
	row := s.db.QueryRow(`SELECT run_id,fixture_id,pack,pack_version,scenario,token,seed_json,initial_state_json,state_json,status,created_at,updated_at FROM environment_web_fixtures WHERE run_id=? AND fixture_id=? AND token=?`, runID, fixtureID, token)
	return scanWebFixture(row)
}

func (s store) listWebFixtures(runID string) ([]WebFixtureInstance, error) {
	rows, err := s.db.Query(`SELECT run_id,fixture_id,pack,pack_version,scenario,token,seed_json,initial_state_json,state_json,status,created_at,updated_at FROM environment_web_fixtures WHERE run_id=? ORDER BY fixture_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebFixtureInstance{}
	for rows.Next() {
		x, err := scanWebFixture(rows)
		if err != nil {
			return nil, err
		}
		if x != nil {
			out = append(out, *x)
		}
	}
	return out, rows.Err()
}

func scanWebFixture(row rowScanner) (*WebFixtureInstance, error) {
	var x WebFixtureInstance
	var seed, initial, state, created, updated string
	err := row.Scan(&x.RunID, &x.ID, &x.Pack, &x.Version, &x.Scenario, &x.Token, &seed, &initial, &state, &x.Status, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(seed), &x.Seed); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(initial), &x.InitialState); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(state), &x.State); err != nil {
		return nil, err
	}
	x.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	x.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &x, nil
}

func (s store) updateWebFixtureState(runID, fixtureID string, state map[string]any) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE environment_web_fixtures SET state_json=?,updated_at=? WHERE run_id=? AND fixture_id=?`, string(raw), time.Now().UTC().Format(time.RFC3339Nano), runID, fixtureID)
	return err
}

func (s store) setWebFixturesStatus(runID, status string) error {
	_, err := s.db.Exec(`UPDATE environment_web_fixtures SET status=?,updated_at=? WHERE run_id=?`, status, time.Now().UTC().Format(time.RFC3339Nano), runID)
	return err
}

func (s store) resetWebFixture(runID, fixtureID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE environment_web_fixtures SET state_json=initial_state_json,updated_at=? WHERE run_id=? AND fixture_id=?`, now, runID, fixtureID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM environment_web_fixture_events WHERE run_id=? AND fixture_id=?`, runID, fixtureID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s store) appendWebFixtureEvent(event *WebFixtureEvent) error {
	raw, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.Exec(`INSERT INTO environment_web_fixture_events(run_id,fixture_id,type,data_json,created_at) VALUES(?,?,?,?,?)`, event.RunID, event.FixtureID, event.Type, string(raw), event.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	event.ID, _ = result.LastInsertId()
	return nil
}

func (s store) listWebFixtureEvents(runID, fixtureID string) ([]WebFixtureEvent, error) {
	rows, err := s.db.Query(`SELECT id,run_id,fixture_id,type,data_json,created_at FROM environment_web_fixture_events WHERE run_id=? AND fixture_id=? ORDER BY id`, runID, fixtureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebFixtureEvent{}
	for rows.Next() {
		var event WebFixtureEvent
		var raw, created string
		if err := rows.Scan(&event.ID, &event.RunID, &event.FixtureID, &event.Type, &raw, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &event.Data); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s store) saveWebFixtureSnapshots(snapshotID, runID string) error {
	fixtures, err := s.listWebFixtures(runID)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM environment_web_fixture_snapshots WHERE snapshot_id=?`, snapshotID); err != nil {
		return err
	}
	for _, x := range fixtures {
		seed, _ := json.Marshal(x.Seed)
		state, _ := json.Marshal(x.State)
		if _, err = tx.Exec(`INSERT INTO environment_web_fixture_snapshots(snapshot_id,fixture_id,pack,pack_version,scenario,seed_json,state_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, snapshotID, x.ID, x.Pack, x.Version, x.Scenario, string(seed), string(state), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s store) webFixtureSnapshot(snapshotID, fixtureID string) (map[string]any, bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT state_json FROM environment_web_fixture_snapshots WHERE snapshot_id=? AND fixture_id=?`, snapshotID, fixtureID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, false, err
	}
	return state, true, nil
}

func (s store) deleteWebFixtureSnapshots(snapshotID string) error {
	_, err := s.db.Exec(`DELETE FROM environment_web_fixture_snapshots WHERE snapshot_id=?`, snapshotID)
	return err
}
