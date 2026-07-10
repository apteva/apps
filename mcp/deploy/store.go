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
	ID               int64  `json:"id"`
	ProjectID        string `json:"project_id"`
	Name             string `json:"name"`
	TargetKind       string `json:"target_kind"`
	Description      string `json:"description"`
	SourceKind       string `json:"source_kind"`
	SourceRef        string `json:"source_ref"`
	SourceExtraJSON  string `json:"source_extra_json"`
	Framework        string `json:"framework"`
	BuildCmd         string `json:"build_cmd"`
	StartCmd         string `json:"start_cmd"`
	PortHint         int    `json:"port_hint"`
	EnvJSON          string `json:"env_json"`
	TargetConfigJSON string `json:"target_config_json"`
	Domain           string `json:"domain"`
	DomainRecordID   string `json:"domain_record_id,omitempty"`
	DomainAttachedAt string `json:"domain_attached_at,omitempty"`
	CurrentReleaseID *int64 `json:"current_release_id,omitempty"`
	ArchivedAt       string `json:"archived_at,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`

	EnvironmentID   int64  `json:"-"`
	EnvironmentName string `json:"environment,omitempty"`
}

type DeploymentEnvironment struct {
	ID               int64  `json:"id"`
	DeploymentID     int64  `json:"deployment_id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	SourceRef        string `json:"source_ref"`
	SourceExtraJSON  string `json:"source_extra_json"`
	Framework        string `json:"framework"`
	BuildCmd         string `json:"build_cmd"`
	StartCmd         string `json:"start_cmd"`
	PortHint         int    `json:"port_hint"`
	EnvJSON          string `json:"env_json"`
	TargetConfigJSON string `json:"target_config_json"`
	Domain           string `json:"domain"`
	DomainRecordID   string `json:"domain_record_id,omitempty"`
	DomainAttachedAt string `json:"domain_attached_at,omitempty"`
	CurrentReleaseID *int64 `json:"current_release_id,omitempty"`
	ArchivedAt       string `json:"archived_at,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type Build struct {
	ID                   int64  `json:"id"`
	DeploymentID         int64  `json:"deployment_id"`
	EnvironmentID        int64  `json:"environment_id,omitempty"`
	SourceSHA            string `json:"source_sha"`
	Framework            string `json:"framework"`
	BuildCmd             string `json:"build_cmd"`
	Status               string `json:"status"`
	StartedAt            string `json:"started_at,omitempty"`
	FinishedAt           string `json:"finished_at,omitempty"`
	DurationMs           int64  `json:"duration_ms"`
	ExitCode             int    `json:"exit_code"`
	ArtifactPath         string `json:"artifact_path"`
	ArtifactSize         int64  `json:"artifact_size"`
	ArtifactManifestJSON string `json:"artifact_manifest_json"`
	LogPath              string `json:"log_path"`
	Error                string `json:"error"`
	CreatedAt            string `json:"created_at"`
}

type Release struct {
	ID              int64  `json:"id"`
	DeploymentID    int64  `json:"deployment_id"`
	EnvironmentID   int64  `json:"environment_id,omitempty"`
	BuildID         int64  `json:"build_id"`
	Status          string `json:"status"`
	Port            int    `json:"port"`
	PID             int    `json:"pid"`
	StartedAt       string `json:"started_at,omitempty"`
	StoppedAt       string `json:"stopped_at,omitempty"`
	RestartCount    int    `json:"restart_count"`
	LastHealthAt    string `json:"last_health_at,omitempty"`
	LogPath         string `json:"log_path"`
	Error           string `json:"error"`
	Channel         string `json:"channel,omitempty"`
	Provider        string `json:"provider,omitempty"`
	ExternalID      string `json:"external_id,omitempty"`
	ExternalStatus  string `json:"external_status,omitempty"`
	ReleaseMetaJSON string `json:"release_meta_json,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type CreateDeploymentInput struct {
	Name             string
	TargetKind       string
	Description      string
	SourceKind       string
	SourceRef        string
	SourceExtraJSON  string
	Framework        string
	BuildCmd         string
	StartCmd         string
	PortHint         int
	EnvJSON          string
	TargetConfigJSON string
	Domain           string
}

type CreateEnvironmentInput struct {
	Name             string
	Description      string
	SourceRef        string
	SourceExtraJSON  string
	Framework        string
	BuildCmd         string
	StartCmd         string
	PortHint         int
	EnvJSON          string
	TargetConfigJSON string
	Domain           string
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
			project_id, name, target_kind, description,
			source_kind, source_ref, source_extra_json,
			framework, build_cmd, start_cmd, port_hint, env_json, target_config_json, domain,
			created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		projectID, in.Name, defaultStr(in.TargetKind, "service"), in.Description,
		in.SourceKind, in.SourceRef, defaultStr(in.SourceExtraJSON, "{}"),
		in.Framework, in.BuildCmd, in.StartCmd, in.PortHint, defaultStr(in.EnvJSON, "{}"), defaultStr(in.TargetConfigJSON, "{}"), in.Domain,
		now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	d, err := dbGetDeployment(db, projectID, id)
	if err != nil {
		return nil, err
	}
	if _, err := dbEnsureProductionEnvironment(db, d); err != nil {
		return nil, err
	}
	return d, nil
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

// dbGetDeploymentByID fetches by id without a project_id filter.
// Used by the release-lifecycle code paths (probeReady, watchdog)
// where the release row has deployment_id but not project_id; the
// project_id is on the deployment itself.
func dbGetDeploymentByID(db *sql.DB, id int64) (*Deployment, error) {
	row := db.QueryRow(`SELECT `+deploymentColumns+` FROM deployments WHERE id = ?`, id)
	d, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// dbListDeploymentsWithDomain returns every deployment in any project
// that has a non-empty Domain field. Used by the phantom-route sweep
// on boot: we need to know which hostnames are still legitimately
// owned by some deployment under this install, regardless of project.
func dbListDeploymentsWithDomain(db *sql.DB) ([]Deployment, error) {
	rows, err := db.Query(`SELECT ` + deploymentColumns + `
		FROM deployments d
		WHERE d.domain != ''
		  AND NOT EXISTS (
		      SELECT 1 FROM deployment_environments e
		       WHERE e.deployment_id = d.id AND e.domain != ''
		  )`)
	if err != nil {
		return nil, err
	}
	out := []Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *d)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = db.Query(`
		SELECT d.id, d.project_id, d.name, d.target_kind,
		       e.description, d.source_kind, e.source_ref, e.source_extra_json,
		       e.framework, e.build_cmd, e.start_cmd, e.port_hint, e.env_json, e.target_config_json, e.domain,
		       e.domain_record_id, COALESCE(e.domain_attached_at,''),
		       e.current_release_id, COALESCE(e.archived_at,''), e.created_at, e.updated_at,
		       e.id, e.name
		  FROM deployment_environments e
		  JOIN deployments d ON d.id = e.deployment_id
		 WHERE e.domain != ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDeploymentWithEnvironmentIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
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

// dbSetDeploymentDomain updates the domain link fields atomically.
// Pass empty strings + nil time to clear (detach). recordID is the
// stable handle returned by the domains app's records_set call so
// detach can target the right record.
func dbSetDeploymentDomain(db *sql.DB, id int64, domain, recordID, attachedAt string) error {
	var attached any = attachedAt
	if attachedAt == "" {
		attached = nil
	}
	_, err := db.Exec(
		`UPDATE deployments
		   SET domain = ?, domain_record_id = ?, domain_attached_at = ?, updated_at = ?
		 WHERE id = ?`,
		domain, recordID, attached, nowUTC(), id,
	)
	return err
}

// ─── Deployment environments ─────────────────────────────────────

const defaultEnvironmentName = "production"

func dbCreateEnvironment(db *sql.DB, deploymentID int64, in CreateEnvironmentInput) (*DeploymentEnvironment, error) {
	name := normalizeEnvironmentName(in.Name)
	if name == "" {
		return nil, errors.New("environment name required")
	}
	now := nowUTC()
	res, err := db.Exec(`
		INSERT INTO deployment_environments (
			deployment_id, name, description,
			source_ref, source_extra_json,
			framework, build_cmd, start_cmd, port_hint, env_json, target_config_json, domain,
			created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		deploymentID, name, in.Description,
		in.SourceRef, defaultStr(in.SourceExtraJSON, "{}"),
		in.Framework, in.BuildCmd, in.StartCmd, in.PortHint, defaultStr(in.EnvJSON, "{}"), defaultStr(in.TargetConfigJSON, "{}"), in.Domain,
		now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetEnvironment(db, id)
}

func dbEnsureProductionEnvironment(db *sql.DB, d *Deployment) (*DeploymentEnvironment, error) {
	if d == nil {
		return nil, errors.New("deployment required")
	}
	env, err := dbGetEnvironmentByName(db, d.ID, defaultEnvironmentName)
	if err != nil {
		return nil, err
	}
	if env != nil {
		return env, nil
	}
	env, err = dbCreateEnvironment(db, d.ID, CreateEnvironmentInput{
		Name: defaultEnvironmentName, Description: d.Description,
		SourceRef: d.SourceRef, SourceExtraJSON: d.SourceExtraJSON,
		Framework: d.Framework, BuildCmd: d.BuildCmd, StartCmd: d.StartCmd,
		PortHint: d.PortHint, EnvJSON: d.EnvJSON, TargetConfigJSON: d.TargetConfigJSON, Domain: d.Domain,
	})
	if err != nil {
		return nil, err
	}
	if d.DomainRecordID != "" || d.DomainAttachedAt != "" || d.CurrentReleaseID != nil {
		fields := map[string]any{
			"domain_record_id":   d.DomainRecordID,
			"domain_attached_at": d.DomainAttachedAt,
		}
		if d.CurrentReleaseID != nil {
			fields["current_release_id"] = *d.CurrentReleaseID
		}
		_ = dbUpdateEnvironment(db, env.ID, fields)
		env, _ = dbGetEnvironment(db, env.ID)
	}
	_, _ = db.Exec(`UPDATE builds SET environment_id = ? WHERE deployment_id = ? AND environment_id IS NULL`, env.ID, d.ID)
	_, _ = db.Exec(`UPDATE releases SET environment_id = ? WHERE deployment_id = ? AND environment_id IS NULL`, env.ID, d.ID)
	return env, nil
}

func dbListEnvironments(db *sql.DB, deploymentID int64, includeArchived bool) ([]DeploymentEnvironment, error) {
	q := `SELECT ` + environmentColumns + ` FROM deployment_environments WHERE deployment_id = ?`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY CASE WHEN name = 'production' THEN 0 WHEN name = 'staging' THEN 1 WHEN name = 'dev' THEN 2 ELSE 3 END, name`
	rows, err := db.Query(q, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeploymentEnvironment{}
	for rows.Next() {
		env, err := scanEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *env)
	}
	return out, rows.Err()
}

func dbGetEnvironment(db *sql.DB, id int64) (*DeploymentEnvironment, error) {
	row := db.QueryRow(`SELECT `+environmentColumns+` FROM deployment_environments WHERE id = ?`, id)
	env, err := scanEnvironment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return env, err
}

func dbGetEnvironmentByName(db *sql.DB, deploymentID int64, name string) (*DeploymentEnvironment, error) {
	row := db.QueryRow(`SELECT `+environmentColumns+` FROM deployment_environments WHERE deployment_id = ? AND name = ?`,
		deploymentID, normalizeEnvironmentName(name))
	env, err := scanEnvironment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return env, err
}

func dbUpdateEnvironment(db *sql.DB, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := []string{}
	args := []any{}
	for _, k := range []string{
		"description", "source_ref", "source_extra_json",
		"framework", "build_cmd", "start_cmd", "port_hint", "env_json", "target_config_json",
		"domain", "domain_record_id", "domain_attached_at", "current_release_id", "archived_at",
	} {
		if v, ok := fields[k]; ok {
			cols = append(cols, k+" = ?")
			args = append(args, v)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	cols = append(cols, "updated_at = ?")
	args = append(args, nowUTC(), id)
	_, err := db.Exec(`UPDATE deployment_environments SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

func dbSetEnvironmentCurrentRelease(db *sql.DB, environmentID int64, releaseID *int64) error {
	_, err := db.Exec(`UPDATE deployment_environments SET current_release_id = ?, updated_at = ? WHERE id = ?`, releaseID, nowUTC(), environmentID)
	return err
}

func dbSetEnvironmentDomain(db *sql.DB, id int64, domain, recordID, attachedAt string) error {
	var attached any = attachedAt
	if attachedAt == "" {
		attached = nil
	}
	_, err := db.Exec(
		`UPDATE deployment_environments
		   SET domain = ?, domain_record_id = ?, domain_attached_at = ?, updated_at = ?
		 WHERE id = ?`,
		domain, recordID, attached, nowUTC(), id,
	)
	return err
}

func effectiveDeploymentForEnvironment(d *Deployment, env *DeploymentEnvironment) *Deployment {
	if d == nil || env == nil {
		return d
	}
	out := *d
	out.Description = env.Description
	out.SourceRef = env.SourceRef
	out.SourceExtraJSON = env.SourceExtraJSON
	out.Framework = env.Framework
	out.BuildCmd = env.BuildCmd
	out.StartCmd = env.StartCmd
	out.PortHint = env.PortHint
	out.EnvJSON = env.EnvJSON
	out.TargetConfigJSON = env.TargetConfigJSON
	out.Domain = env.Domain
	out.DomainRecordID = env.DomainRecordID
	out.DomainAttachedAt = env.DomainAttachedAt
	out.CurrentReleaseID = env.CurrentReleaseID
	out.EnvironmentID = env.ID
	out.EnvironmentName = env.Name
	return &out
}

const environmentColumns = `id, deployment_id, name, description,
		source_ref, source_extra_json, framework, build_cmd, start_cmd,
		port_hint, env_json, target_config_json, domain, domain_record_id,
		COALESCE(domain_attached_at,''), current_release_id,
		COALESCE(archived_at,''), created_at, updated_at`

func scanEnvironment(r rowScanner) (*DeploymentEnvironment, error) {
	var env DeploymentEnvironment
	var current sql.NullInt64
	if err := r.Scan(
		&env.ID, &env.DeploymentID, &env.Name, &env.Description,
		&env.SourceRef, &env.SourceExtraJSON, &env.Framework, &env.BuildCmd, &env.StartCmd,
		&env.PortHint, &env.EnvJSON, &env.TargetConfigJSON, &env.Domain, &env.DomainRecordID,
		&env.DomainAttachedAt, &current, &env.ArchivedAt, &env.CreatedAt, &env.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if current.Valid {
		env.CurrentReleaseID = &current.Int64
	}
	return &env, nil
}

func normalizeEnvironmentName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return defaultEnvironmentName
	}
	return s
}

const deploymentColumns = `id, project_id, name, target_kind, description, source_kind, source_ref, source_extra_json,
		framework, build_cmd, start_cmd, port_hint, env_json, target_config_json, domain,
		domain_record_id, COALESCE(domain_attached_at,''),
		current_release_id, COALESCE(archived_at,''), created_at, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanDeployment(r rowScanner) (*Deployment, error) {
	var d Deployment
	var current sql.NullInt64
	if err := r.Scan(
		&d.ID, &d.ProjectID, &d.Name, &d.TargetKind, &d.Description, &d.SourceKind, &d.SourceRef, &d.SourceExtraJSON,
		&d.Framework, &d.BuildCmd, &d.StartCmd, &d.PortHint, &d.EnvJSON, &d.TargetConfigJSON, &d.Domain,
		&d.DomainRecordID, &d.DomainAttachedAt,
		&current, &d.ArchivedAt, &d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if current.Valid {
		d.CurrentReleaseID = &current.Int64
	}
	return &d, nil
}

func scanDeploymentWithEnvironmentIdentity(r rowScanner) (*Deployment, error) {
	var d Deployment
	var current sql.NullInt64
	if err := r.Scan(
		&d.ID, &d.ProjectID, &d.Name, &d.TargetKind, &d.Description, &d.SourceKind, &d.SourceRef, &d.SourceExtraJSON,
		&d.Framework, &d.BuildCmd, &d.StartCmd, &d.PortHint, &d.EnvJSON, &d.TargetConfigJSON, &d.Domain,
		&d.DomainRecordID, &d.DomainAttachedAt,
		&current, &d.ArchivedAt, &d.CreatedAt, &d.UpdatedAt,
		&d.EnvironmentID, &d.EnvironmentName,
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
	return dbCreateBuildForEnv(db, deploymentID, 0, framework, buildCmd)
}

func dbCreateBuildForEnv(db *sql.DB, deploymentID, environmentID int64, framework, buildCmd string) (*Build, error) {
	res, err := db.Exec(`
		INSERT INTO builds (deployment_id, environment_id, framework, build_cmd, status, created_at)
		VALUES (?,?,?,?,'pending',?)
	`, deploymentID, nullInt64(environmentID), framework, buildCmd, nowUTC())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetBuild(db, id)
}

// dbUpdateDeployment mutates an allowlist of deployment-level fields
// without touching identity (id, project_id, name, source_kind,
// source_ref, created_at). Mirrors dbUpdateBuild's dynamic SET +
// allowlist pattern. Used by the PATCH endpoint and deploy_set_env
// to change config (env_json, build_cmd, start_cmd, port_hint,
// description, framework) without delete+recreate. Note: domain
// stays managed by dbSetDeploymentDomain because the attach flow
// owns the (domain, record_id, attached_at) triple atomically.
func dbUpdateDeployment(db *sql.DB, projectID string, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := []string{}
	args := []any{}
	for _, k := range []string{
		"description", "framework", "build_cmd", "start_cmd",
		"port_hint", "env_json", "source_extra_json", "target_config_json",
	} {
		if v, ok := fields[k]; ok {
			cols = append(cols, k+" = ?")
			args = append(args, v)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	cols = append(cols, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id, projectID)
	_, err := db.Exec(
		`UPDATE deployments SET `+strings.Join(cols, ", ")+` WHERE id = ? AND project_id = ?`,
		args...)
	return err
}

func dbUpdateBuild(db *sql.DB, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := []string{}
	args := []any{}
	for _, k := range []string{"source_sha", "status", "started_at", "finished_at", "duration_ms", "exit_code", "artifact_path", "artifact_size", "artifact_manifest_json", "log_path", "error", "framework"} {
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

func dbListBuildsForEnv(db *sql.DB, deploymentID, environmentID int64, limit int) ([]Build, error) {
	if environmentID == 0 {
		return dbListBuilds(db, deploymentID, limit)
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(`SELECT `+buildColumns+` FROM builds WHERE deployment_id = ? AND environment_id = ? ORDER BY id DESC LIMIT ?`,
		deploymentID, environmentID, limit)
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
	return out, rows.Err()
}

const buildColumns = `id, deployment_id, COALESCE(environment_id,0), source_sha, framework, build_cmd, status,
		COALESCE(started_at,''), COALESCE(finished_at,''), duration_ms, exit_code,
		artifact_path, artifact_size, artifact_manifest_json, log_path, error, created_at`

func scanBuild(r rowScanner) (*Build, error) {
	var b Build
	if err := r.Scan(
		&b.ID, &b.DeploymentID, &b.EnvironmentID, &b.SourceSHA, &b.Framework, &b.BuildCmd, &b.Status,
		&b.StartedAt, &b.FinishedAt, &b.DurationMs, &b.ExitCode,
		&b.ArtifactPath, &b.ArtifactSize, &b.ArtifactManifestJSON, &b.LogPath, &b.Error, &b.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &b, nil
}

// ─── Releases ─────────────────────────────────────────────────────

func dbCreateRelease(db *sql.DB, deploymentID, buildID int64) (*Release, error) {
	return dbCreateReleaseForEnv(db, deploymentID, 0, buildID)
}

func dbCreateReleaseForEnv(db *sql.DB, deploymentID, environmentID, buildID int64) (*Release, error) {
	res, err := db.Exec(`
		INSERT INTO releases (deployment_id, environment_id, build_id, status, created_at)
		VALUES (?,?,?,'starting',?)
	`, deploymentID, nullInt64(environmentID), buildID, nowUTC())
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
	for _, k := range []string{"status", "port", "pid", "started_at", "stopped_at", "restart_count", "last_health_at", "log_path", "error", "channel", "provider", "external_id", "external_status", "release_meta_json"} {
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

func dbListReleasesForEnv(db *sql.DB, deploymentID, environmentID int64, limit int) ([]Release, error) {
	if environmentID == 0 {
		return dbListReleases(db, deploymentID, limit)
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.Query(`SELECT `+releaseColumns+` FROM releases WHERE deployment_id = ? AND environment_id = ? ORDER BY id DESC LIMIT ?`,
		deploymentID, environmentID, limit)
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
	return out, rows.Err()
}

func dbListLiveReleases(db *sql.DB) ([]Release, error) {
	rows, err := db.Query(`SELECT ` + releaseColumns + ` FROM releases WHERE provider = '' AND status IN ('starting','live') ORDER BY id`)
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

func dbListPendingMobileReleases(db *sql.DB, limit int) ([]Release, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Query(`SELECT `+releaseColumns+` FROM releases WHERE provider != '' AND status = 'starting' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rel)
	}
	return out, rows.Err()
}

const releaseColumns = `id, deployment_id, COALESCE(environment_id,0), build_id, status, port, pid,
		COALESCE(started_at,''), COALESCE(stopped_at,''), restart_count,
		COALESCE(last_health_at,''), log_path, error, channel, provider, external_id, external_status, release_meta_json, created_at`

func scanRelease(r rowScanner) (*Release, error) {
	var rl Release
	if err := r.Scan(
		&rl.ID, &rl.DeploymentID, &rl.EnvironmentID, &rl.BuildID, &rl.Status, &rl.Port, &rl.PID,
		&rl.StartedAt, &rl.StoppedAt, &rl.RestartCount, &rl.LastHealthAt,
		&rl.LogPath, &rl.Error, &rl.Channel, &rl.Provider, &rl.ExternalID, &rl.ExternalStatus, &rl.ReleaseMetaJSON, &rl.CreatedAt,
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

func nullInt64(v int64) any {
	if v == 0 {
		return nil
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
