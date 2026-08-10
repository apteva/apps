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
	Platform   string `json:"platform"` // "android" | "ios"
	Runtime    string `json:"runtime"`
	DeviceType string `json:"device_type"`
	Status     string `json:"status"` // shutdown | booting | booted | crashed | orphaned
	PID        int64  `json:"pid"`
	Serial     string `json:"serial"`
	RunnerKind string `json:"runner"`      // "local" | "instances"
	InstanceID int64  `json:"instance_id"` // 0 for local
	BackendID  string `json:"-"`           // host-native AVD name / simctl UDID
	CreatedAt  string `json:"created_at,omitempty"`
	BootedAt   string `json:"booted_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (s Sim) IsRemote() bool { return s.RunnerKind == "instances" && s.InstanceID > 0 }

func (s Sim) NativeID() string {
	if s.BackendID != "" {
		return s.BackendID
	}
	return s.ID
}

type SimRun struct {
	ID           int64  `json:"id"`
	SimID        string `json:"sim_id"`
	ProjectID    string `json:"project_id"`
	SourceApp    string `json:"source_app"` // "code" | "manual"
	SourceRef    string `json:"source_ref"`
	Framework    string `json:"framework"` // "android" | "ios"
	BundleID     string `json:"bundle_id"`
	ArtifactPath string `json:"artifact_path"`
	ArtifactID   string `json:"artifact_id"`
	RunnerKind   string `json:"runner"`
	InstanceID   int64  `json:"instance_id"`
	Status       string `json:"status"` // building | installing | running | stopped | crashed
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

type SimulatorHost struct {
	InstanceID       int64  `json:"instance_id"`
	InstanceName     string `json:"instance_name,omitempty"`
	WorkerVersion    string `json:"worker_version,omitempty"`
	WorkerPort       int    `json:"worker_port"`
	WorkerToken      string `json:"-"`
	CapabilitiesJSON string `json:"capabilities_json,omitempty"`
	Status           string `json:"status"`
	LastSeenAt       string `json:"last_seen_at,omitempty"`
	Error            string `json:"error,omitempty"`
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
	if s.RunnerKind == "" {
		s.RunnerKind = "local"
	}
	if s.BackendID == "" {
		s.BackendID = s.ID
	}
	// ON CONFLICT update preserves created_at and lets the caller flip
	// status / pid / serial / booted_at / error.
	_, err := db.Exec(`
		INSERT INTO sims (id, project_id, platform, runtime, device_type, status, pid, serial,
		                  runner_kind, instance_id, backend_id, booted_at, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  project_id  = excluded.project_id,
		  platform    = excluded.platform,
		  runtime     = excluded.runtime,
		  device_type = excluded.device_type,
		  status      = excluded.status,
		  pid         = excluded.pid,
		  serial      = excluded.serial,
		  runner_kind = excluded.runner_kind,
		  instance_id = excluded.instance_id,
		  backend_id  = excluded.backend_id,
		  booted_at   = excluded.booted_at,
		  error       = excluded.error
	`, s.ID, s.ProjectID, s.Platform, s.Runtime, s.DeviceType, s.Status, s.PID, s.Serial,
		s.RunnerKind, s.InstanceID, s.BackendID, nullIfEmpty(s.BootedAt), s.Error)
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
		       runner_kind, instance_id, backend_id,
		       COALESCE(created_at,''), COALESCE(booted_at,''), error
		FROM sims WHERE id = ?`, id)
	var s Sim
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Platform, &s.Runtime, &s.DeviceType,
		&s.Status, &s.PID, &s.Serial, &s.RunnerKind, &s.InstanceID, &s.BackendID,
		&s.CreatedAt, &s.BootedAt, &s.Error); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func dbFindSimByBackend(db *sql.DB, projectID, platform, runnerKind string, instanceID int64, backendID string) (*Sim, error) {
	row := db.QueryRow(`
		SELECT id, project_id, platform, runtime, device_type, status, pid, serial,
		       runner_kind, instance_id, backend_id,
		       COALESCE(created_at,''), COALESCE(booted_at,''), error
		FROM sims
		WHERE project_id = ? AND platform = ? AND runner_kind = ? AND instance_id = ? AND backend_id = ?
		LIMIT 1`, projectID, platform, runnerKind, instanceID, backendID)
	var s Sim
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Platform, &s.Runtime, &s.DeviceType,
		&s.Status, &s.PID, &s.Serial, &s.RunnerKind, &s.InstanceID, &s.BackendID,
		&s.CreatedAt, &s.BootedAt, &s.Error); err != nil {
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
		       runner_kind, instance_id, backend_id,
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
			&s.Status, &s.PID, &s.Serial, &s.RunnerKind, &s.InstanceID, &s.BackendID,
			&s.CreatedAt, &s.BootedAt, &s.Error); err != nil {
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
		       runner_kind, instance_id, backend_id,
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
			&s.Status, &s.PID, &s.Serial, &s.RunnerKind, &s.InstanceID, &s.BackendID,
			&s.CreatedAt, &s.BootedAt, &s.Error); err != nil {
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
		                     artifact_path, artifact_id, runner_kind, instance_id,
		                     status, log_path, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.SimID, r.ProjectID, r.SourceApp, r.SourceRef, r.Framework, r.BundleID,
		r.ArtifactPath, r.ArtifactID, valueOr(r.RunnerKind, "local"), r.InstanceID,
		r.Status, r.LogPath, nullIfEmpty(r.StartedAt))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	r.ID = id
	return &r, nil
}

func dbStopActiveSimRuns(db *sql.DB, simID string) error {
	_, err := db.Exec(`
		UPDATE sim_runs
		SET status = 'stopped', stopped_at = ?
		WHERE sim_id = ? AND status IN ('building','installing','running')
	`, time.Now().UTC().Format(time.RFC3339), simID)
	return err
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
		       artifact_path, artifact_id, runner_kind, instance_id, status, log_path,
		       COALESCE(started_at,''), COALESCE(stopped_at,''), error
		FROM sim_runs WHERE sim_id = ? ORDER BY id DESC LIMIT 1`, simID)
	var r SimRun
	if err := row.Scan(&r.ID, &r.SimID, &r.ProjectID, &r.SourceApp, &r.SourceRef, &r.Framework,
		&r.BundleID, &r.ArtifactPath, &r.ArtifactID, &r.RunnerKind, &r.InstanceID,
		&r.Status, &r.LogPath, &r.StartedAt, &r.StoppedAt, &r.Error); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// ─── sim_streams ────────────────────────────────────────────────────

// dbMintStreamToken creates an independent stream token for a sim. Multiple
// panels may watch the same device, so minting a URL must not invalidate URLs
// already held by Code or another Simulator panel. Expired rows are reaped and
// a small per-sim cap prevents abandoned browser tabs growing the table forever.
func dbMintStreamToken(db *sql.DB, simID string, ttl time.Duration) (*SimStream, error) {
	tok, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expires := now.Add(ttl).Format(time.RFC3339)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM sim_streams WHERE expires_at <= ?`, now.Format(time.RFC3339)); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`
		INSERT INTO sim_streams (sim_id, ws_token, expires_at)
		VALUES (?, ?, ?)
	`, simID, tok, expires); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`
		DELETE FROM sim_streams
		WHERE sim_id = ? AND ws_token NOT IN (
			SELECT ws_token FROM sim_streams
			WHERE sim_id = ?
			ORDER BY created_at DESC, rowid DESC
			LIMIT 8
		)
	`, simID, simID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
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
	if err != nil {
		return "", errors.New("token expiry is invalid")
	}
	if time.Now().UTC().After(t) {
		return "", errors.New("token expired")
	}
	return simID, nil
}

func dbDeleteStreamToken(db *sql.DB, simID string) error {
	_, err := db.Exec(`DELETE FROM sim_streams WHERE sim_id = ?`, simID)
	return err
}

// ─── simulator_hosts ───────────────────────────────────────────────

func dbGetSimulatorHost(db *sql.DB, instanceID int64) (*SimulatorHost, error) {
	row := db.QueryRow(`
		SELECT instance_id, instance_name, worker_version, worker_port, worker_token,
		       capabilities_json, status, COALESCE(last_seen_at,''), error
		FROM simulator_hosts WHERE instance_id = ?`, instanceID)
	var h SimulatorHost
	if err := row.Scan(&h.InstanceID, &h.InstanceName, &h.WorkerVersion, &h.WorkerPort,
		&h.WorkerToken, &h.CapabilitiesJSON, &h.Status, &h.LastSeenAt, &h.Error); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &h, nil
}

func dbUpsertSimulatorHost(db *sql.DB, h SimulatorHost) error {
	if h.InstanceID <= 0 || h.WorkerPort <= 0 || h.WorkerToken == "" {
		return errors.New("remote simulator host requires instance_id, worker_port, and token")
	}
	if h.Status == "" {
		h.Status = "unknown"
	}
	_, err := db.Exec(`
		INSERT INTO simulator_hosts
		  (instance_id, instance_name, worker_version, worker_port, worker_token,
		   capabilities_json, status, last_seen_at, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET
		  instance_name = excluded.instance_name,
		  worker_version = excluded.worker_version,
		  worker_port = excluded.worker_port,
		  worker_token = excluded.worker_token,
		  capabilities_json = excluded.capabilities_json,
		  status = excluded.status,
		  last_seen_at = excluded.last_seen_at,
		  error = excluded.error
	`, h.InstanceID, h.InstanceName, h.WorkerVersion, h.WorkerPort, h.WorkerToken,
		h.CapabilitiesJSON, h.Status, nullIfEmpty(h.LastSeenAt), h.Error)
	return err
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
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
