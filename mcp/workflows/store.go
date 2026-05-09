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

type Workflow struct {
	ID          int64        `json:"id"`
	ProjectID   string       `json:"project_id,omitempty"`
	Name        string       `json:"name"`
	Version     int          `json:"version"`
	SourceKind  string       `json:"source_kind"`
	Source      string       `json:"source,omitempty"`
	RepoID      *int64       `json:"repo_id,omitempty"`
	RepoPath    string       `json:"repo_path,omitempty"`
	SourceHash  string       `json:"source_hash"`
	TriggerKind string       `json:"trigger_kind"`
	TriggerJSON string       `json:"trigger_json,omitempty"`
	Status      string       `json:"status"`
	CreatedAt   string       `json:"created_at,omitempty"`
	UpdatedAt   string       `json:"updated_at,omitempty"`
	// Definition is the parsed source — populated lazily by callers
	// that need to execute the workflow. Not persisted; recomputed
	// from Source on read.
	Definition *WorkflowDef `json:"definition,omitempty"`
}

type Run struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	WorkflowID      int64  `json:"workflow_id"`
	WorkflowName    string `json:"workflow_name"`
	WorkflowVersion int    `json:"workflow_version"`
	TriggerKind     string `json:"trigger_kind"`
	InputJSON       string `json:"input_json,omitempty"`
	Status          string `json:"status"`
	CurrentStepID   string `json:"current_step_id,omitempty"`
	Error           string `json:"error,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	FinishedAt      string `json:"finished_at,omitempty"`
	DurationMS      int64  `json:"duration_ms"`
	// Steps is populated by Run-status / replay views; not stored
	// inline on the runs table.
	Steps []*StepExecution `json:"steps,omitempty"`
}

type StepExecution struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	StepID     string `json:"step_id"`
	StepKind   string `json:"step_kind"`
	Attempt    int    `json:"attempt"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
	InputJSON  string `json:"input_json,omitempty"`
	OutputJSON string `json:"output_json,omitempty"`
	Error      string `json:"error,omitempty"`
}

type WorkflowFilter struct {
	Status      string
	TriggerKind string
	Limit       int
}

// ─── Validation ────────────────────────────────────────────────────

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

const (
	maxInputJSON  = 16 * 1024
	maxOutputJSON = 64 * 1024
	maxErrorMsg   = 1024
)

// ─── Hash helper ───────────────────────────────────────────────────

func hashSource(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ─── Workflow CRUD ─────────────────────────────────────────────────

// dbCreateWorkflow inserts a workflow row. Caller is responsible
// for resolving the source (inline or repo) and computing
// source_hash — same split as functions.
func dbCreateWorkflow(db *sql.DB, pid string, w *Workflow) (*Workflow, error) {
	if !nameRE.MatchString(w.Name) {
		return nil, errors.New("name must match [a-z0-9][a-z0-9-]{0,62}")
	}
	if w.SourceKind == "" {
		w.SourceKind = "inline"
	}
	if w.SourceKind != "inline" && w.SourceKind != "repo" {
		return nil, fmt.Errorf("source_kind %q must be inline|repo", w.SourceKind)
	}
	if w.SourceKind == "inline" && w.Source == "" {
		return nil, errors.New("source required for source_kind=inline")
	}
	if w.SourceKind == "repo" && (w.RepoID == nil || w.RepoPath == "") {
		return nil, errors.New("repo_id and repo_path required for source_kind=repo")
	}
	if w.TriggerKind == "" {
		w.TriggerKind = "manual"
	}
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := db.Exec(
		`INSERT INTO workflows (
			project_id, name, version, source_kind, source, repo_id, repo_path,
			source_hash, trigger_kind, trigger_json, status,
			created_at, updated_at
		 ) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		pid, w.Name, w.SourceKind,
		nullStr(w.Source), nullableInt64Ptr(w.RepoID), nullStr(w.RepoPath),
		w.SourceHash, w.TriggerKind, nullStr(w.TriggerJSON),
		now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetWorkflow(db, pid, id, "")
}

// dbUpdateWorkflow merges patch fields. Source-affecting changes
// bump version and set source_hash; non-source changes keep version.
func dbUpdateWorkflow(db *sql.DB, pid string, id int64, patch map[string]any, newSourceHash string) (*Workflow, error) {
	cur, err := dbGetWorkflow(db, pid, id, "")
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errors.New("workflow not found")
	}

	sets := []string{}
	args := []any{}

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
	if v, ok := patch["trigger_kind"].(string); ok && v != "" {
		sets = append(sets, "trigger_kind = ?")
		args = append(args, v)
	}
	if _, has := patch["trigger_json"]; has {
		sets = append(sets, "trigger_json = ?")
		args = append(args, nullStr(strArg(patch, "trigger_json")))
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
		sets = append(sets, "version = version + 1")
	}

	if len(sets) == 0 {
		return cur, nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339))
	args = append(args, id, pid)

	q := `UPDATE workflows SET ` + strings.Join(sets, ", ") + ` WHERE id = ? AND project_id = ?`
	if _, err := db.Exec(q, args...); err != nil {
		return nil, err
	}
	return dbGetWorkflow(db, pid, id, "")
}

const wfColumns = `id, project_id, name, version, source_kind,
		COALESCE(source,''), repo_id, COALESCE(repo_path,''),
		source_hash, trigger_kind, COALESCE(trigger_json,''),
		status, created_at, updated_at`

func dbGetWorkflow(db *sql.DB, pid string, id int64, name string) (*Workflow, error) {
	var row *sql.Row
	switch {
	case id != 0:
		row = db.QueryRow(`SELECT `+wfColumns+` FROM workflows WHERE id = ? AND project_id = ?`, id, pid)
	case name != "":
		row = db.QueryRow(`SELECT `+wfColumns+` FROM workflows WHERE name = ? AND project_id = ?`, name, pid)
	default:
		return nil, errors.New("id or name required")
	}
	w, err := scanWorkflow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return w, err
}

func dbListWorkflows(db *sql.DB, pid string, f WorkflowFilter) ([]*Workflow, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.TriggerKind != "" {
		where = append(where, "trigger_kind = ?")
		args = append(args, f.TriggerKind)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + wfColumns + ` FROM workflows WHERE ` +
		strings.Join(where, " AND ") +
		` ORDER BY name ASC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Workflow{}
	for rows.Next() {
		w, err := scanWorkflow(rows)
		if err != nil {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

func dbDeleteWorkflow(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(`DELETE FROM workflows WHERE id = ? AND project_id = ?`, id, pid)
	return err
}

type scanRow interface {
	Scan(dest ...any) error
}

func scanWorkflow(row scanRow) (*Workflow, error) {
	w := &Workflow{}
	var repoID sql.NullInt64
	err := row.Scan(
		&w.ID, &w.ProjectID, &w.Name, &w.Version, &w.SourceKind,
		&w.Source, &repoID, &w.RepoPath,
		&w.SourceHash, &w.TriggerKind, &w.TriggerJSON,
		&w.Status, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if repoID.Valid {
		v := repoID.Int64
		w.RepoID = &v
	}
	return w, nil
}

// ─── Run CRUD ──────────────────────────────────────────────────────

func dbInsertRun(db *sql.DB, pid string, r *Run) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if r.StartedAt == "" {
		r.StartedAt = now
	}
	if r.Status == "" {
		r.Status = "pending"
	}
	res, err := db.Exec(
		`INSERT INTO workflow_runs (
			project_id, workflow_id, workflow_name, workflow_version,
			trigger_kind, input_json, status, current_step_id,
			started_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, r.WorkflowID, r.WorkflowName, r.WorkflowVersion,
		r.TriggerKind, nullStr(r.InputJSON), r.Status, nullStr(r.CurrentStepID),
		r.StartedAt)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func dbUpdateRunState(db *sql.DB, pid string, id int64, status, currentStep, errStr string, finished bool) error {
	args := []any{status, nullStr(currentStep), nullStr(truncate(errStr, maxErrorMsg))}
	q := `UPDATE workflow_runs SET status = ?, current_step_id = ?, error = ?`
	if finished {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		q += `, finished_at = ?, duration_ms = CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER)`
		args = append(args, now, now)
	}
	q += ` WHERE id = ? AND project_id = ?`
	args = append(args, id, pid)
	_, err := db.Exec(q, args...)
	return err
}

const runColumns = `id, project_id, workflow_id, workflow_name, workflow_version,
		trigger_kind, COALESCE(input_json,''), status, COALESCE(current_step_id,''),
		COALESCE(error,''), started_at, COALESCE(finished_at,''),
		COALESCE(duration_ms,0)`

func dbGetRun(db *sql.DB, pid string, id int64) (*Run, error) {
	row := db.QueryRow(`SELECT `+runColumns+` FROM workflow_runs WHERE id = ? AND project_id = ?`, id, pid)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func dbListRuns(db *sql.DB, pid string, workflowID int64, limit int) ([]*Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + runColumns + ` FROM workflow_runs WHERE project_id = ? AND workflow_id = ?
		   ORDER BY started_at DESC LIMIT ?`
	rows, err := db.Query(q, pid, workflowID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func scanRun(row scanRow) (*Run, error) {
	r := &Run{}
	err := row.Scan(
		&r.ID, &r.ProjectID, &r.WorkflowID, &r.WorkflowName, &r.WorkflowVersion,
		&r.TriggerKind, &r.InputJSON, &r.Status, &r.CurrentStepID,
		&r.Error, &r.StartedAt, &r.FinishedAt, &r.DurationMS)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ─── Step execution log ────────────────────────────────────────────

func dbInsertStepExecution(db *sql.DB, runID int64, s *StepExecution) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO workflow_step_executions (
			run_id, step_id, step_kind, attempt,
			started_at, finished_at, duration_ms, status,
			input_json, output_json, error
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, s.StepID, s.StepKind, s.Attempt,
		s.StartedAt, nullStr(s.FinishedAt), s.DurationMS, s.Status,
		nullStr(truncate(s.InputJSON, maxInputJSON)),
		nullStr(truncate(s.OutputJSON, maxOutputJSON)),
		nullStr(truncate(s.Error, maxErrorMsg)))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func dbListStepExecutions(db *sql.DB, runID int64) ([]*StepExecution, error) {
	rows, err := db.Query(
		`SELECT id, run_id, step_id, step_kind, attempt,
			started_at, COALESCE(finished_at,''), COALESCE(duration_ms,0), status,
			COALESCE(input_json,''), COALESCE(output_json,''), COALESCE(error,'')
		 FROM workflow_step_executions WHERE run_id = ?
		 ORDER BY started_at, id`,
		runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*StepExecution{}
	for rows.Next() {
		s := &StepExecution{}
		if err := rows.Scan(&s.ID, &s.RunID, &s.StepID, &s.StepKind, &s.Attempt,
			&s.StartedAt, &s.FinishedAt, &s.DurationMS, &s.Status,
			&s.InputJSON, &s.OutputJSON, &s.Error); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

// ─── Boot-time sweeper ─────────────────────────────────────────────
//
// Sidecar restart kills any in-flight run; we have no resumability
// in v0.1. The cleanest thing is to mark stuck runs failed at boot
// so they don't show as "running forever" in dashboards.

func dbSweepStuckRuns(db *sql.DB) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`UPDATE workflow_runs SET status = 'failed',
			error = COALESCE(error,'') || 'sidecar restarted with run in flight',
			finished_at = ?, duration_ms = CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER)
		 WHERE status IN ('pending','running')`,
		now, now)
	return err
}

// ─── Encoders ──────────────────────────────────────────────────────

func mustJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
