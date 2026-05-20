package main

// DB types + ops for the simulator app. Three tables:
//
//   sims        — devices we've booted at least once. id = AVD name on
//                 android or simctl UDID on ios. Stored across reboots
//                 so re-booting the same device is a status flip, not
//                 a create.
//   sim_runs    — one row per build+install+launch cycle on a sim.
//   sim_streams — short-lived WS-stream bearer tokens minted by
//                 sims_stream_url and validated on WS upgrade.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ─── Types ──────────────────────────────────────────────────────────

type Sim struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	Platform   string `json:"platform"`     // "android" | "ios"
	Runtime    string `json:"runtime"`
	DeviceType string `json:"device_type"`
	Status     string `json:"status"`       // shutdown | booting | booted | crashed
	PID        int64  `json:"pid"`
	Serial     string `json:"serial"`
	CreatedAt  string `json:"created_at,omitempty"`
	BootedAt   string `json:"booted_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

type SimRun struct {
	ID           int64  `json:"id"`
	SimID        string `json:"sim_id"`
	ProjectID    string `json:"project_id"`
	SourceApp    string `json:"source_app"`     // "code" | "manual"
	SourceRef    string `json:"source_ref"`
	Framework    string `json:"framework"`      // "android" | "ios"
	BundleID     string `json:"bundle_id"`
	ArtifactPath string `json:"artifact_path"`
	Status       string `json:"status"`         // building | installing | running | stopped | crashed
	LogPath      string `json:"log_path"`
	StartedAt    string `json:"started_at,omitempty"`
	StoppedAt    string `json:"stopped_at,omitempty"`
	Error        string `json:"error,omitempty"`
}

type SimStream struct {
	SimID     string `json:"sim_id"`
	WSToken   string `json:"ws_token"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

// ─── sims ───────────────────────────────────────────────────────────

func dbUpsertSim(db *sql.DB, s Sim) error {
	if s.ID == "" {
		return errors.New("sim.id required")
	}
	if s.Platform != "android" && s.Platform != "ios" {
		return fmt.Errorf("sim.platform %q invalid", s.Platform)
	}
	if s.Status == "" {
		s.Status = "shutdown"
	}
	// ON CONFLICT update preserves created_at and lets the caller flip
	// status / pid / serial / booted_at / error.
	_, err := db.Exec(`
		INSERT INTO sims (id, project_id, platform, runtime, device_type, status, pid, serial, booted_at, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  project_id  = excluded.project_id,
		  platform    = excluded.platform,
		  runtime     = excluded.runtime,
		  device_type = excluded.device_type,
		  status      = excluded.status,
		  pid         = excluded.pid,
		  serial      = excluded.serial,
		  booted_at   = excluded.booted_at,
		  error       = excluded.error
	`, s.ID, s.ProjectID, s.Platform, s.Runtime, s.DeviceType, s.Status, s.PID, s.Serial, nullIfEmpty(s.BootedAt), s.Error)
	return err
}

func dbUpdateSim(db *sql.DB, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)
	for k, v := range fields {
		cols = append(cols, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id)
	_, err := db.Exec(`UPDATE sims SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

func dbGetSim(db *sql.DB, id string) (*Sim, error) {
	row := db.QueryRow(`
		SELECT id, project_id, platform, runtime, device_type, status, pid, serial,
		       COALESCE(created_at,''), COALESCE(booted_at,''), error
		FROM sims WHERE id = ?`, id)
	var s Sim
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Platform, &s.Runtime, &s.DeviceType,
		&s.Status, &s.PID, &s.Serial, &s.CreatedAt, &s.BootedAt, &s.Error); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func dbListSims(db *sql.DB, projectID string) ([]Sim, error) {
	rows, err := db.Query(`
		SELECT id, project_id, platform, runtime, device_type, status, pid, serial,
		       COALESCE(created_at,''), COALESCE(booted_at,''), error
		FROM sims WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sim{}
	for rows.Next() {
		var s Sim
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Platform, &s.Runtime, &s.DeviceType,
			&s.Status, &s.PID, &s.Serial, &s.CreatedAt, &s.BootedAt, &s.Error); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// dbListLiveSims is the boot-reconcile feed: every sim that the DB
// thinks is currently up. The reconciler decides per-row whether the
// process is actually alive and demotes mismatches.
func dbListLiveSims(db *sql.DB) ([]Sim, error) {
	rows, err := db.Query(`
		SELECT id, project_id, platform, runtime, device_type, status, pid, serial,
		       COALESCE(created_at,''), COALESCE(booted_at,''), error
		FROM sims WHERE status IN ('booting','booted')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sim{}
	for rows.Next() {
		var s Sim
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Platform, &s.Runtime, &s.DeviceType,
			&s.Status, &s.PID, &s.Serial, &s.CreatedAt, &s.BootedAt, &s.Error); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── sim_runs ───────────────────────────────────────────────────────

func dbInsertSimRun(db *sql.DB, r SimRun) (*SimRun, error) {
	if r.SimID == "" {
		return nil, errors.New("sim_run.sim_id required")
	}
	if r.Status == "" {
		r.Status = "building"
	}
	res, err := db.Exec(`
		INSERT INTO sim_runs (sim_id, project_id, source_app, source_ref, framework, bundle_id,
		                     artifact_path, status, log_path, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.SimID, r.ProjectID, r.SourceApp, r.SourceRef, r.Framework, r.BundleID,
		r.ArtifactPath, r.Status, r.LogPath, nullIfEmpty(r.StartedAt))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	r.ID = id
	return &r, nil
}

func dbUpdateSimRun(db *sql.DB, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)
	for k, v := range fields {
		cols = append(cols, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id)
	_, err := db.Exec(`UPDATE sim_runs SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

func dbLatestSimRun(db *sql.DB, simID string) (*SimRun, error) {
	row := db.QueryRow(`
		SELECT id, sim_id, project_id, source_app, source_ref, framework, bundle_id,
		       artifact_path, status, log_path,
		       COALESCE(started_at,''), COALESCE(stopped_at,''), error
		FROM sim_runs WHERE sim_id = ? ORDER BY id DESC LIMIT 1`, simID)
	var r SimRun
	if err := row.Scan(&r.ID, &r.SimID, &r.ProjectID, &r.SourceApp, &r.SourceRef, &r.Framework,
		&r.BundleID, &r.ArtifactPath, &r.Status, &r.LogPath, &r.StartedAt, &r.StoppedAt, &r.Error); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// ─── sim_streams ────────────────────────────────────────────────────

// dbMintStreamToken creates (or replaces) the stream token for a sim.
// Each call rotates the token — calling sims_stream_url twice in a row
// invalidates the first URL, which is the desired behavior.
func dbMintStreamToken(db *sql.DB, simID string, ttl time.Duration) (*SimStream, error) {
	tok, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(ttl).Format(time.RFC3339)
	_, err = db.Exec(`
		INSERT INTO sim_streams (sim_id, ws_token, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(sim_id) DO UPDATE SET
		  ws_token   = excluded.ws_token,
		  created_at = CURRENT_TIMESTAMP,
		  expires_at = excluded.expires_at
	`, simID, tok, expires)
	if err != nil {
		return nil, err
	}
	return &SimStream{SimID: simID, WSToken: tok, ExpiresAt: expires}, nil
}

// dbResolveStreamToken returns the sim_id for a token, refusing
// expired ones. Used by stream.go on WS upgrade.
func dbResolveStreamToken(db *sql.DB, token string) (string, error) {
	row := db.QueryRow(`SELECT sim_id, expires_at FROM sim_streams WHERE ws_token = ?`, token)
	var simID, expires string
	if err := row.Scan(&simID, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("token not found")
		}
		return "", err
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err == nil && time.Now().UTC().After(t) {
		return "", errors.New("token expired")
	}
	return simID, nil
}

func dbDeleteStreamToken(db *sql.DB, simID string) error {
	_, err := db.Exec(`DELETE FROM sim_streams WHERE sim_id = ?`, simID)
	return err
}

// ─── Helpers ────────────────────────────────────────────────────────

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
