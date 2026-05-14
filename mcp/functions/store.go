package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ─── Domain types ──────────────────────────────────────────────────

type Function struct {
	ID              int64             `json:"id"`
	ProjectID       string            `json:"project_id,omitempty"`
	Name            string            `json:"name"`
	Runtime         string            `json:"runtime"`
	SourceKind      string            `json:"source_kind"`
	Source          string            `json:"source,omitempty"`
	RepoID          *int64            `json:"repo_id,omitempty"`
	RepoPath        string            `json:"repo_path,omitempty"`
	SourceHash      string            `json:"source_hash"`
	Env             map[string]string `json:"env,omitempty"`
	TimeoutMS       int               `json:"timeout_ms"`
	MaxMemoryMB     int               `json:"max_memory_mb"`
	Status          string            `json:"status"`
	ActiveVersionID *int64            `json:"active_version_id,omitempty"`
	CreatedAt       string            `json:"created_at,omitempty"`
	UpdatedAt       string            `json:"updated_at,omitempty"`
}

// FunctionVersion is one immutable deploy of a function. Created by
// functions_deploy (and by functions_create for v1); built once;
// becomes the function's active version on a successful build.
type FunctionVersion struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	FunctionID  int64  `json:"function_id"`
	Version     int    `json:"version"`
	SourceKind  string `json:"source_kind"`
	Source      string `json:"source,omitempty"`
	RepoID      *int64 `json:"repo_id,omitempty"`
	RepoPath    string `json:"repo_path,omitempty"`
	SourceHash  string `json:"source_hash"`
	PackageJSON string `json:"package_json,omitempty"`
	BuildStatus string `json:"build_status"`        // pending | building | ready | failed
	BuildLog    string `json:"build_log,omitempty"`
	BuildDir    string `json:"build_dir,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type Invocation struct {
	ID           int64  `json:"id"`
	FunctionID   int64  `json:"function_id"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
	Status       string `json:"status"`
	ExitCode     int    `json:"exit_code"`
	TriggerKind  string `json:"trigger_kind"`
	EventJSON    string `json:"event_json,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
	Stderr       string `json:"stderr,omitempty"`
	Error        string `json:"error,omitempty"`
}

type FunctionFilter struct {
	Runtime string
	Status  string
	Limit   int
}

// ─── Validation ────────────────────────────────────────────────────

// nameRE constrains function names to URL-safe slugs. Names appear
// in the auto-routed /fn/<name> path so anything outside this set
// would either need escaping or produce 404s — better to reject
// at create time.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validRuntimes is the create-time guard. node + go; see runtime.go
// for why bun is out and python is deferred.
var validRuntimes = map[string]bool{
	"node": true, "go": true,
}

const (
	maxTimeoutMS    = 300_000 // 5 minutes
	maxMemoryMB     = 1024
	defaultTimeout  = 30_000
	defaultMemoryMB = 256
)

// ─── Hash helper ───────────────────────────────────────────────────

func hashSource(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ─── CRUD ──────────────────────────────────────────────────────────

// dbCreateFunction inserts a function row. Caller is responsible for
// resolving the source (inline or repo) and computing source_hash —
// kept out of this layer so the DB code stays pure SQL.
func dbCreateFunction(db *sql.DB, pid string, fn *Function) (*Function, error) {
	if !nameRE.MatchString(fn.Name) {
		return nil, errors.New("name must match [a-z0-9][a-z0-9-]{0,62}")
	}
	if !validRuntimes[fn.Runtime] {
		return nil, fmt.Errorf("runtime %q not supported (node|go)", fn.Runtime)
	}
	if fn.SourceKind == "" {
		fn.SourceKind = "inline"
	}
	if fn.SourceKind != "inline" && fn.SourceKind != "repo" {
		return nil, fmt.Errorf("source_kind %q must be inline|repo", fn.SourceKind)
	}
	if fn.SourceKind == "inline" && fn.Source == "" {
		return nil, errors.New("source required for source_kind=inline")
	}
	if fn.SourceKind == "repo" && (fn.RepoID == nil || fn.RepoPath == "") {
		return nil, errors.New("repo_id and repo_path required for source_kind=repo")
	}
	fn.TimeoutMS = clampInt(fn.TimeoutMS, defaultTimeout, 1, maxTimeoutMS)
	fn.MaxMemoryMB = clampInt(fn.MaxMemoryMB, defaultMemoryMB, 1, maxMemoryMB)

	envJSON, err := encodeEnv(fn.Env)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := db.Exec(
		`INSERT INTO functions (
			project_id, name, runtime, source_kind, source, repo_id, repo_path,
			source_hash, env_json, timeout_ms, max_memory_mb, status,
			created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		pid, fn.Name, fn.Runtime, fn.SourceKind,
		nullStr(fn.Source), nullableInt64Ptr(fn.RepoID), nullStr(fn.RepoPath),
		fn.SourceHash, envJSON, fn.TimeoutMS, fn.MaxMemoryMB,
		now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetFunction(db, pid, id, "")
}

// dbUpdateFunction merges patch fields into an existing row. Caller
// supplies the source_hash (recomputed when source changed) so this
// stays a pure CRUD primitive.
func dbUpdateFunction(db *sql.DB, pid string, id int64, patch map[string]any, newSourceHash string) (*Function, error) {
	cur, err := dbGetFunction(db, pid, id, "")
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errors.New("function not found")
	}

	sets := []string{}
	args := []any{}

	if v, ok := patch["runtime"].(string); ok && v != "" {
		if !validRuntimes[v] {
			return nil, fmt.Errorf("runtime %q not supported", v)
		}
		sets = append(sets, "runtime = ?")
		args = append(args, v)
	}
	if v, ok := patch["source_kind"].(string); ok && v != "" {
		if v != "inline" && v != "repo" {
			return nil, fmt.Errorf("source_kind %q must be inline|repo", v)
		}
		sets = append(sets, "source_kind = ?")
		args = append(args, v)
	}
	if _, has := patch["source"]; has {
		sets = append(sets, "source = ?")
		args = append(args, nullStr(strArg(patch, "source")))
	}
	if _, has := patch["repo_id"]; has {
		sets = append(sets, "repo_id = ?")
		args = append(args, nullableInt64(int64Arg(patch, "repo_id")))
	}
	if _, has := patch["repo_path"]; has {
		sets = append(sets, "repo_path = ?")
		args = append(args, nullStr(strArg(patch, "repo_path")))
	}
	if _, has := patch["env"]; has {
		envMap, _ := patch["env"].(map[string]any)
		envStrMap := map[string]string{}
		for k, v := range envMap {
			if s, ok := v.(string); ok {
				envStrMap[k] = s
			}
		}
		envJSON, err := encodeEnv(envStrMap)
		if err != nil {
			return nil, err
		}
		sets = append(sets, "env_json = ?")
		args = append(args, envJSON)
	}
	if _, has := patch["timeout_ms"]; has {
		sets = append(sets, "timeout_ms = ?")
		args = append(args, clampInt(intArg(patch, "timeout_ms", cur.TimeoutMS), cur.TimeoutMS, 1, maxTimeoutMS))
	}
	if _, has := patch["max_memory_mb"]; has {
		sets = append(sets, "max_memory_mb = ?")
		args = append(args, clampInt(intArg(patch, "max_memory_mb", cur.MaxMemoryMB), cur.MaxMemoryMB, 1, maxMemoryMB))
	}
	if v, ok := patch["status"].(string); ok && v != "" {
		if v != "active" && v != "disabled" {
			return nil, fmt.Errorf("status %q must be active|disabled", v)
		}
		sets = append(sets, "status = ?")
		args = append(args, v)
	}
	if newSourceHash != "" {
		sets = append(sets, "source_hash = ?")
		args = append(args, newSourceHash)
	}

	if len(sets) == 0 {
		return cur, nil
	}

	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339))
	args = append(args, id, pid)

	q := `UPDATE functions SET ` + strings.Join(sets, ", ") + ` WHERE id = ? AND project_id = ?`
	if _, err := db.Exec(q, args...); err != nil {
		return nil, err
	}
	return dbGetFunction(db, pid, id, "")
}

// dbGetFunction looks up by id (when id != 0) or by name (when name
// != ""). Returns nil, nil on not-found.
func dbGetFunction(db *sql.DB, pid string, id int64, name string) (*Function, error) {
	var (
		row *sql.Row
	)
	switch {
	case id != 0:
		row = db.QueryRow(`SELECT `+fnColumns+` FROM functions WHERE id = ? AND project_id = ?`, id, pid)
	case name != "":
		row = db.QueryRow(`SELECT `+fnColumns+` FROM functions WHERE name = ? AND project_id = ?`, name, pid)
	default:
		return nil, errors.New("id or name required")
	}
	fn, err := scanFunction(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return fn, err
}

func dbListFunctions(db *sql.DB, pid string, f FunctionFilter) ([]*Function, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.Runtime != "" {
		where = append(where, "runtime = ?")
		args = append(args, f.Runtime)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + fnColumns + ` FROM functions WHERE ` +
		strings.Join(where, " AND ") +
		` ORDER BY name ASC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Function{}
	for rows.Next() {
		fn, err := scanFunction(rows)
		if err != nil {
			continue
		}
		out = append(out, fn)
	}
	return out, nil
}

func dbDeleteFunction(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(`DELETE FROM functions WHERE id = ? AND project_id = ?`, id, pid)
	return err
}

// fnColumns is the SELECT list for scanFunction. Centralised so
// every read of the functions table goes through the same column
// order — see scanJob in jobs for the same pattern.
const fnColumns = `id, project_id, name, runtime, source_kind,
		COALESCE(source,''), repo_id, COALESCE(repo_path,''),
		source_hash, COALESCE(env_json,''),
		timeout_ms, max_memory_mb, status,
		active_version_id, created_at, updated_at`

type scanRow interface {
	Scan(dest ...any) error
}

func scanFunction(row scanRow) (*Function, error) {
	fn := &Function{}
	var repoID, activeVer sql.NullInt64
	var envJSON string
	err := row.Scan(
		&fn.ID, &fn.ProjectID, &fn.Name, &fn.Runtime, &fn.SourceKind,
		&fn.Source, &repoID, &fn.RepoPath,
		&fn.SourceHash, &envJSON,
		&fn.TimeoutMS, &fn.MaxMemoryMB, &fn.Status,
		&activeVer, &fn.CreatedAt, &fn.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if repoID.Valid {
		v := repoID.Int64
		fn.RepoID = &v
	}
	if activeVer.Valid {
		v := activeVer.Int64
		fn.ActiveVersionID = &v
	}
	if envJSON != "" {
		_ = json.Unmarshal([]byte(envJSON), &fn.Env)
	}
	return fn, nil
}

// ─── Versions ──────────────────────────────────────────────────────

const fnVerColumns = `id, project_id, function_id, version,
		source_kind, COALESCE(source,''), repo_id, COALESCE(repo_path,''),
		source_hash, COALESCE(package_json,''),
		build_status, COALESCE(build_log,''), COALESCE(build_dir,''),
		created_at`

func scanVersion(row scanRow) (*FunctionVersion, error) {
	v := &FunctionVersion{}
	var repoID sql.NullInt64
	err := row.Scan(
		&v.ID, &v.ProjectID, &v.FunctionID, &v.Version,
		&v.SourceKind, &v.Source, &repoID, &v.RepoPath,
		&v.SourceHash, &v.PackageJSON,
		&v.BuildStatus, &v.BuildLog, &v.BuildDir,
		&v.CreatedAt)
	if err != nil {
		return nil, err
	}
	if repoID.Valid {
		r := repoID.Int64
		v.RepoID = &r
	}
	return v, nil
}

// dbCreateVersion inserts a version row, stamping the next monotonic
// version number for the function. Caller resolves the source bytes
// and computes source_hash; build_status starts at "building".
func dbCreateVersion(db *sql.DB, pid string, v *FunctionVersion) (*FunctionVersion, error) {
	var next int
	if err := db.QueryRow(
		`SELECT COALESCE(MAX(version),0)+1 FROM function_versions WHERE function_id = ?`,
		v.FunctionID).Scan(&next); err != nil {
		return nil, err
	}
	v.Version = next
	if v.BuildStatus == "" {
		v.BuildStatus = "pending"
	}
	res, err := db.Exec(
		`INSERT INTO function_versions (
			project_id, function_id, version, source_kind, source,
			repo_id, repo_path, source_hash, package_json, build_status
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, v.FunctionID, v.Version, v.SourceKind, nullStr(v.Source),
		nullableInt64Ptr(v.RepoID), nullStr(v.RepoPath), v.SourceHash,
		nullStr(v.PackageJSON), v.BuildStatus)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetVersion(db, pid, id)
}

func dbGetVersion(db *sql.DB, pid string, id int64) (*FunctionVersion, error) {
	row := db.QueryRow(`SELECT `+fnVerColumns+` FROM function_versions WHERE id = ? AND project_id = ?`, id, pid)
	v, err := scanVersion(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func dbGetVersionByNumber(db *sql.DB, pid string, fnID int64, version int) (*FunctionVersion, error) {
	row := db.QueryRow(
		`SELECT `+fnVerColumns+` FROM function_versions WHERE function_id = ? AND version = ? AND project_id = ?`,
		fnID, version, pid)
	v, err := scanVersion(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func dbListVersions(db *sql.DB, pid string, fnID int64, limit int) ([]*FunctionVersion, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := db.Query(
		`SELECT `+fnVerColumns+` FROM function_versions
		 WHERE function_id = ? AND project_id = ? ORDER BY version DESC LIMIT ?`,
		fnID, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*FunctionVersion{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// dbUpdateVersionBuild records the outcome of a build attempt.
func dbUpdateVersionBuild(db *sql.DB, pid string, id int64, status, buildLog, buildDir string) error {
	_, err := db.Exec(
		`UPDATE function_versions SET build_status = ?, build_log = ?, build_dir = ?
		 WHERE id = ? AND project_id = ?`,
		status, nullStr(buildLog), nullStr(buildDir), id, pid)
	return err
}

// dbSetActiveVersion points the function at v and denormalises v's
// source columns onto the functions row so the hot invoke path needs
// no join. Used by both deploy and rollback.
func dbSetActiveVersion(db *sql.DB, pid string, fnID int64, v *FunctionVersion) error {
	_, err := db.Exec(
		`UPDATE functions SET active_version_id = ?, source_kind = ?, source = ?,
			repo_id = ?, repo_path = ?, source_hash = ?, updated_at = ?
		 WHERE id = ? AND project_id = ?`,
		v.ID, v.SourceKind, nullStr(v.Source),
		nullableInt64Ptr(v.RepoID), nullStr(v.RepoPath), v.SourceHash,
		time.Now().UTC().Format(time.RFC3339),
		fnID, pid)
	return err
}

// ─── Invocations ───────────────────────────────────────────────────

func dbInsertInvocation(db *sql.DB, pid string, inv *Invocation) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO function_invocations (
			project_id, function_id, started_at, finished_at, duration_ms,
			status, exit_code, trigger_kind, event_json, response_body, stderr, error
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, inv.FunctionID,
		inv.StartedAt, nullStr(inv.FinishedAt), inv.DurationMS,
		inv.Status, inv.ExitCode, inv.TriggerKind,
		nullStr(inv.EventJSON), nullStr(inv.ResponseBody),
		nullStr(inv.Stderr), nullStr(inv.Error))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func dbListInvocations(db *sql.DB, pid string, fnID int64, limit int) ([]*Invocation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(
		`SELECT id, function_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(exit_code,0),
			trigger_kind, COALESCE(event_json,''), COALESCE(response_body,''),
			COALESCE(stderr,''), COALESCE(error,'')
		 FROM function_invocations
		 WHERE project_id = ? AND function_id = ?
		 ORDER BY started_at DESC LIMIT ?`,
		pid, fnID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Invocation{}
	for rows.Next() {
		inv := &Invocation{}
		if err := rows.Scan(&inv.ID, &inv.FunctionID, &inv.StartedAt, &inv.FinishedAt,
			&inv.DurationMS, &inv.Status, &inv.ExitCode,
			&inv.TriggerKind, &inv.EventJSON, &inv.ResponseBody,
			&inv.Stderr, &inv.Error); err == nil {
			out = append(out, inv)
		}
	}
	return out, nil
}

func dbGetInvocation(db *sql.DB, pid string, id int64) (*Invocation, error) {
	row := db.QueryRow(
		`SELECT id, function_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(exit_code,0),
			trigger_kind, COALESCE(event_json,''), COALESCE(response_body,''),
			COALESCE(stderr,''), COALESCE(error,'')
		 FROM function_invocations
		 WHERE project_id = ? AND id = ?`,
		pid, id)
	inv := &Invocation{}
	err := row.Scan(&inv.ID, &inv.FunctionID, &inv.StartedAt, &inv.FinishedAt,
		&inv.DurationMS, &inv.Status, &inv.ExitCode,
		&inv.TriggerKind, &inv.EventJSON, &inv.ResponseBody,
		&inv.Stderr, &inv.Error)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func dbRecentInvocations(db *sql.DB, pid string, limit int) ([]*Invocation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(
		`SELECT id, function_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(exit_code,0),
			trigger_kind, COALESCE(event_json,''), COALESCE(response_body,''),
			COALESCE(stderr,''), COALESCE(error,'')
		 FROM function_invocations
		 WHERE project_id = ?
		 ORDER BY started_at DESC LIMIT ?`,
		pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Invocation{}
	for rows.Next() {
		inv := &Invocation{}
		if err := rows.Scan(&inv.ID, &inv.FunctionID, &inv.StartedAt, &inv.FinishedAt,
			&inv.DurationMS, &inv.Status, &inv.ExitCode,
			&inv.TriggerKind, &inv.EventJSON, &inv.ResponseBody,
			&inv.Stderr, &inv.Error); err == nil {
			out = append(out, inv)
		}
	}
	return out, nil
}

// ─── Encoders ──────────────────────────────────────────────────────

func encodeEnv(env map[string]string) (sql.NullString, error) {
	if len(env) == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(env)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}
