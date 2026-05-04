package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// All time fields are stored as TEXT in RFC3339 — SQLite's DATETIME
// is just a TEXT alias and the JSON marshalling stays human-readable.

type Deployment struct {
	ID                int64  `json:"id"`
	ProjectID         string `json:"project_id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	SourceKind        string `json:"source_kind"`
	SourceRef         string `json:"source_ref"`
	SourceExtraJSON   string `json:"source_extra_json"`
	Framework         string `json:"framework"`
	BuildCmd          string `json:"build_cmd"`
	StartCmd          string `json:"start_cmd"`
	PortHint          int    `json:"port_hint"`
	EnvJSON           string `json:"env_json"`
	Domain            string `json:"domain"`
	CurrentReleaseID  *int64 `json:"current_release_id,omitempty"`
	ArchivedAt        string `json:"archived_at,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type Build struct {
	ID            int64  `json:"id"`
	DeploymentID  int64  `json:"deployment_id"`
	SourceSHA     string `json:"source_sha"`
	Framework     string `json:"framework"`
	BuildCmd      string `json:"build_cmd"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
	DurationMs    int64  `json:"duration_ms"`
	ExitCode      int    `json:"exit_code"`
	ArtifactPath  string `json:"artifact_path"`
	ArtifactSize  int64  `json:"artifact_size"`
	LogPath       string `json:"log_path"`
	Error         string `json:"error"`
	CreatedAt     string `json:"created_at"`
}

type Release struct {
	ID            int64  `json:"id"`
	DeploymentID  int64  `json:"deployment_id"`
	BuildID       int64  `json:"build_id"`
	Status        string `json:"status"`
	Port          int    `json:"port"`
	PID           int    `json:"pid"`
	StartedAt     string `json:"started_at,omitempty"`
	StoppedAt     string `json:"stopped_at,omitempty"`
	RestartCount  int    `json:"restart_count"`
	LastHealthAt  string `json:"last_health_at,omitempty"`
	LogPath       string `json:"log_path"`
	Error         string `json:"error"`
	CreatedAt     string `json:"created_at"`
}

type CreateDeploymentInput struct {
	Name            string
	Description     string
	SourceKind      string
	SourceRef       string
	SourceExtraJSON string
	Framework       string
	BuildCmd        string
	StartCmd        string
	PortHint        int
	EnvJSON         string
	Domain          string
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// ─── Deployments ──────────────────────────────────────────────────

func dbCreateDeployment(db *sql.DB, projectID string, in CreateDeploymentInput) (*Deployment, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("name required")
	}
	if in.SourceKind == "" {
		return nil, errors.New("source_kind required")
	}
	now := nowUTC()
	res, err := db.Exec(`
		INSERT INTO deployments (
			project_id, name, description,
			source_kind, source_ref, source_extra_json,
			framework, build_cmd, start_cmd, port_hint, env_json, domain,
			created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		projectID, in.Name, in.Description,
		in.SourceKind, in.SourceRef, defaultStr(in.SourceExtraJSON, "{}"),
		in.Framework, in.BuildCmd, in.StartCmd, in.PortHint, defaultStr(in.EnvJSON, "{}"), in.Domain,
		now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetDeployment(db, projectID, id)
}

func dbListDeployments(db *sql.DB, projectID string, includeArchived bool) ([]Deployment, error) {
	q := `SELECT ` + deploymentColumns + ` FROM deployments WHERE project_id = ?`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY id DESC`
	rows, err := db.Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, nil
}

func dbGetDeployment(db *sql.DB, projectID string, id int64) (*Deployment, error) {
	row := db.QueryRow(`SELECT `+deploymentColumns+` FROM deployments WHERE project_id = ? AND id = ?`, projectID, id)
	return scanDeployment(row)
}

func dbGetDeploymentByName(db *sql.DB, projectID, name string) (*Deployment, error) {
	row := db.QueryRow(`SELECT `+deploymentColumns+` FROM deployments WHERE project_id = ? AND name = ?`, projectID, name)
	d, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func dbSetCurrentRelease(db *sql.DB, deploymentID int64, releaseID *int64) error {
	_, err := db.Exec(`UPDATE deployments SET current_release_id = ?, updated_at = ? WHERE id = ?`, releaseID, nowUTC(), deploymentID)
	return err
}

func dbDeleteDeployment(db *sql.DB, projectID string, id int64) error {
	_, err := db.Exec(`DELETE FROM deployments WHERE project_id = ? AND id = ?`, projectID, id)
	return err
}

const deploymentColumns = `id, project_id, name, description, source_kind, source_ref, source_extra_json,
		framework, build_cmd, start_cmd, port_hint, env_json, domain,
		current_release_id, COALESCE(archived_at,''), created_at, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanDeployment(r rowScanner) (*Deployment, error) {
	var d Deployment
	var current sql.NullInt64
	if err := r.Scan(
		&d.ID, &d.ProjectID, &d.Name, &d.Description, &d.SourceKind, &d.SourceRef, &d.SourceExtraJSON,
		&d.Framework, &d.BuildCmd, &d.StartCmd, &d.PortHint, &d.EnvJSON, &d.Domain,
		&current, &d.ArchivedAt, &d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if current.Valid {
		d.CurrentReleaseID = &current.Int64
	}
	return &d, nil
}

// ─── Builds ───────────────────────────────────────────────────────

func dbCreateBuild(db *sql.DB, deploymentID int64, framework, buildCmd string) (*Build, error) {
	res, err := db.Exec(`
		INSERT INTO builds (deployment_id, framework, build_cmd, status, created_at)
		VALUES (?,?,?,'pending',?)
	`, deploymentID, framework, buildCmd, nowUTC())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetBuild(db, id)
}

func dbUpdateBuild(db *sql.DB, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := []string{}
	args := []any{}
	for _, k := range []string{"source_sha", "status", "started_at", "finished_at", "duration_ms", "exit_code", "artifact_path", "artifact_size", "log_path", "error", "framework"} {
		if v, ok := fields[k]; ok {
			cols = append(cols, k+" = ?")
			args = append(args, v)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := db.Exec(`UPDATE builds SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

func dbGetBuild(db *sql.DB, id int64) (*Build, error) {
	row := db.QueryRow(`SELECT `+buildColumns+` FROM builds WHERE id = ?`, id)
	return scanBuild(row)
}

func dbListBuilds(db *sql.DB, deploymentID int64, limit int) ([]Build, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(`SELECT `+buildColumns+` FROM builds WHERE deployment_id = ? ORDER BY id DESC LIMIT ?`, deploymentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Build{}
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, nil
}

const buildColumns = `id, deployment_id, source_sha, framework, build_cmd, status,
		COALESCE(started_at,''), COALESCE(finished_at,''), duration_ms, exit_code,
		artifact_path, artifact_size, log_path, error, created_at`

func scanBuild(r rowScanner) (*Build, error) {
	var b Build
	if err := r.Scan(
		&b.ID, &b.DeploymentID, &b.SourceSHA, &b.Framework, &b.BuildCmd, &b.Status,
		&b.StartedAt, &b.FinishedAt, &b.DurationMs, &b.ExitCode,
		&b.ArtifactPath, &b.ArtifactSize, &b.LogPath, &b.Error, &b.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &b, nil
}

// ─── Releases ─────────────────────────────────────────────────────

func dbCreateRelease(db *sql.DB, deploymentID, buildID int64) (*Release, error) {
	res, err := db.Exec(`
		INSERT INTO releases (deployment_id, build_id, status, created_at)
		VALUES (?,?,'starting',?)
	`, deploymentID, buildID, nowUTC())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetRelease(db, id)
}

func dbUpdateRelease(db *sql.DB, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := []string{}
	args := []any{}
	for _, k := range []string{"status", "port", "pid", "started_at", "stopped_at", "restart_count", "last_health_at", "log_path", "error"} {
		if v, ok := fields[k]; ok {
			cols = append(cols, k+" = ?")
			args = append(args, v)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := db.Exec(`UPDATE releases SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

func dbGetRelease(db *sql.DB, id int64) (*Release, error) {
	row := db.QueryRow(`SELECT `+releaseColumns+` FROM releases WHERE id = ?`, id)
	return scanRelease(row)
}

func dbListReleases(db *sql.DB, deploymentID int64, limit int) ([]Release, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.Query(`SELECT `+releaseColumns+` FROM releases WHERE deployment_id = ? ORDER BY id DESC LIMIT ?`, deploymentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		rl, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rl)
	}
	return out, nil
}

func dbListLiveReleases(db *sql.DB) ([]Release, error) {
	rows, err := db.Query(`SELECT ` + releaseColumns + ` FROM releases WHERE status IN ('starting','live') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		rl, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rl)
	}
	return out, nil
}

const releaseColumns = `id, deployment_id, build_id, status, port, pid,
		COALESCE(started_at,''), COALESCE(stopped_at,''), restart_count,
		COALESCE(last_health_at,''), log_path, error, created_at`

func scanRelease(r rowScanner) (*Release, error) {
	var rl Release
	if err := r.Scan(
		&rl.ID, &rl.DeploymentID, &rl.BuildID, &rl.Status, &rl.Port, &rl.PID,
		&rl.StartedAt, &rl.StoppedAt, &rl.RestartCount, &rl.LastHealthAt,
		&rl.LogPath, &rl.Error, &rl.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &rl, nil
}

// ─── Release events ───────────────────────────────────────────────

func dbAppendReleaseEvent(db *sql.DB, releaseID int64, kind, payloadJSON string) error {
	if payloadJSON == "" {
		payloadJSON = "{}"
	}
	_, err := db.Exec(`INSERT INTO release_events (release_id, kind, payload_json, created_at) VALUES (?,?,?,?)`,
		releaseID, kind, payloadJSON, nowUTC())
	return err
}

// ─── Port leases ──────────────────────────────────────────────────

// dbAcquirePortLease tries to claim a port. Returns true if claimed.
// Uses INSERT OR IGNORE so concurrent calls race safely.
func dbAcquirePortLease(db *sql.DB, port int, releaseID int64) (bool, error) {
	res, err := db.Exec(`INSERT OR IGNORE INTO port_leases (port, release_id, acquired_at) VALUES (?,?,?)`,
		port, releaseID, nowUTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func dbReleasePortLease(db *sql.DB, port int) error {
	_, err := db.Exec(`DELETE FROM port_leases WHERE port = ?`, port)
	return err
}

func dbHeldPorts(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT port FROM port_leases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, nil
}

// ─── Helpers ──────────────────────────────────────────────────────

func defaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// validateName mirrors what storage/code apps do — restrict to a
// safe slug so it can land in a URL path component.
func validateName(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("name required")
	}
	if len(s) > 64 {
		return fmt.Errorf("name too long (max 64)")
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' && c != '_' {
			return fmt.Errorf("name must be lowercase alphanumeric, '-' or '_'")
		}
	}
	return nil
}
