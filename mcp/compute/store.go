package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type ComputeJob struct {
	ID             int64             `json:"id"`
	ProjectID      string            `json:"project_id"`
	OwnerApp       string            `json:"owner_app,omitempty"`
	OwnerRef       string            `json:"owner_ref,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Priority       int               `json:"priority"`
	ResourceClass  string            `json:"resource_class"`
	Pool           string            `json:"pool,omitempty"`
	HostID         *int64            `json:"host_id,omitempty"`
	Executor       string            `json:"executor"`
	Kind           string            `json:"kind"`
	Command        string            `json:"command"`
	CWD            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_s"`
	Status         string            `json:"status"`
	Attempt        int               `json:"attempt"`
	ProgressPct    int               `json:"progress_pct"`
	Output         string            `json:"output,omitempty"`
	Error          string            `json:"error,omitempty"`
	ExitCode       *int              `json:"exit_code,omitempty"`
	QueuedAt       string            `json:"queued_at,omitempty"`
	StartedAt      string            `json:"started_at,omitempty"`
	CompletedAt    string            `json:"completed_at,omitempty"`
	UpdatedAt      string            `json:"updated_at,omitempty"`
	CancelledAt    string            `json:"cancelled_at,omitempty"`
}

type JobFilter struct {
	Status        string
	OwnerApp      string
	ResourceClass string
	Pool          string
	HostID        int64
	Limit         int
}

func submitJob(ctx *sdk.AppCtx, pid string, args map[string]any) (*ComputeJob, error) {
	if pid == "" {
		return nil, errors.New("project_id required")
	}
	command := strings.TrimSpace(strArg(args, "command"))
	if command == "" {
		return nil, errors.New("command required")
	}
	kind := strArg(args, "kind")
	if kind == "" {
		kind = "shell"
	}
	if kind != "shell" {
		return nil, fmt.Errorf("kind %q not supported yet", kind)
	}
	resourceClass := strArg(args, "resource_class")
	if resourceClass == "" {
		resourceClass = "default"
	}
	priority := parsePriority(args["priority"])
	timeout := intArg(args, "timeout_s", configInt(ctx, "default_timeout_seconds", 1800))
	if timeout <= 0 {
		timeout = 1800
	}
	env, err := parseEnv(args["env"])
	if err != nil {
		return nil, err
	}
	envJSON, _ := json.Marshal(env)
	executor := "local"
	hostID := int64Arg(args, "host_id")
	if hostID > 0 {
		executor = "instances"
		if !configBool(ctx, "instances_enabled", false) {
			return nil, errors.New("host_id requested but instances_enabled=false")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO compute_jobs (
			project_id, owner_app, owner_ref, idempotency_key, priority, resource_class,
			pool, host_id, executor, kind, command, cwd, env_json, timeout_s,
			status, queued_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?)`,
		pid, strArg(args, "owner_app"), strArg(args, "owner_ref"), strArg(args, "idempotency_key"),
		priority, resourceClass, strArg(args, "pool"), nullableInt64(hostID), executor, kind, command,
		strArg(args, "cwd"), string(envJSON), timeout, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return getJobByIdempotency(ctx.AppDB(), pid, strArg(args, "idempotency_key"))
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getJob(ctx.AppDB(), pid, id)
}

func getJob(db *sql.DB, pid string, id int64) (*ComputeJob, error) {
	row := db.QueryRow(selectJobSQL()+` WHERE id = ? AND project_id = ?`, id, pid)
	j, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

func getJobByIdempotency(db *sql.DB, pid, key string) (*ComputeJob, error) {
	if key == "" {
		return nil, errors.New("idempotency_key required")
	}
	row := db.QueryRow(selectJobSQL()+` WHERE project_id = ? AND idempotency_key = ?`, pid, key)
	return scanJob(row)
}

func listJobs(db *sql.DB, pid string, f JobFilter) ([]*ComputeJob, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.OwnerApp != "" {
		where = append(where, "owner_app = ?")
		args = append(args, f.OwnerApp)
	}
	if f.ResourceClass != "" {
		where = append(where, "resource_class = ?")
		args = append(args, f.ResourceClass)
	}
	if f.Pool != "" {
		where = append(where, "pool = ?")
		args = append(args, f.Pool)
	}
	if f.HostID != 0 {
		where = append(where, "host_id = ?")
		args = append(args, f.HostID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	args = append(args, limit)
	rows, err := db.Query(selectJobSQL()+` WHERE `+strings.Join(where, " AND ")+
		` ORDER BY queued_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ComputeJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func claimNextJob(db *sql.DB) (*ComputeJob, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row := tx.QueryRow(`SELECT id FROM compute_jobs
		WHERE status = 'queued' AND executor = 'local'
		ORDER BY priority ASC, queued_at ASC, id ASC
		LIMIT 1`)
	var id int64
	if err := row.Scan(&id); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`UPDATE compute_jobs
		SET status = 'running', started_at = ?, updated_at = ?, attempt = attempt + 1
		WHERE id = ? AND status = 'queued'`,
		now, now, id)
	if err != nil {
		return nil, err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return nil, sql.ErrNoRows
	}
	row2 := tx.QueryRow(selectJobSQL()+` WHERE id = ?`, id)
	job, err := scanJob(row2)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func markOK(db *sql.DB, pid string, id int64, output string, exitCode int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE compute_jobs
		SET status = 'ok', progress_pct = 100, output = ?, error = '', exit_code = ?,
		    completed_at = ?, updated_at = ?
		WHERE id = ? AND project_id = ?`,
		output, exitCode, now, now, id, pid)
	return err
}

func markFailed(db *sql.DB, pid string, id int64, output string, exitCode int, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE compute_jobs
		SET status = 'failed', output = ?, error = ?, exit_code = ?,
		    completed_at = ?, updated_at = ?
		WHERE id = ? AND project_id = ?`,
		output, truncate(errMsg, 4000), exitCode, now, now, id, pid)
	return err
}

func markCancelled(db *sql.DB, pid string, id int64, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE compute_jobs
		SET status = 'cancelled', error = ?, completed_at = COALESCE(completed_at, ?),
		    cancelled_at = COALESCE(cancelled_at, ?), updated_at = ?
		WHERE id = ? AND project_id = ?`,
		truncate(reason, 4000), now, now, now, id, pid)
	return err
}

func markCancelling(db *sql.DB, pid string, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE compute_jobs
		SET status = 'cancelling', cancelled_at = COALESCE(cancelled_at, ?), updated_at = ?
		WHERE id = ? AND project_id = ? AND status = 'running'`,
		now, now, id, pid)
	return err
}

func queueSummary(db *sql.DB, pid string) (map[string]any, error) {
	rows, err := db.Query(`SELECT status, COUNT(*) FROM compute_jobs WHERE project_id = ? GROUP BY status`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	pending, err := listJobs(db, pid, JobFilter{Status: statusQueued, Limit: 20})
	if err != nil {
		return nil, err
	}
	running, err := listJobs(db, pid, JobFilter{Status: statusRunning, Limit: 20})
	if err != nil {
		return nil, err
	}
	recent, err := listJobs(db, pid, JobFilter{Limit: 20})
	if err != nil {
		return nil, err
	}
	return map[string]any{"counts": counts, "pending": pending, "running": running, "recent": recent}, nil
}

func selectJobSQL() string {
	return `SELECT id, project_id, COALESCE(owner_app,''), COALESCE(owner_ref,''), COALESCE(idempotency_key,''),
		priority, resource_class, COALESCE(pool,''), host_id, executor, kind, command, COALESCE(cwd,''), env_json,
		timeout_s, status, attempt, progress_pct, output, error, exit_code,
		queued_at, started_at, completed_at, updated_at, cancelled_at
		FROM compute_jobs`
}

type rowScanner interface{ Scan(dest ...any) error }

func scanJob(row rowScanner) (*ComputeJob, error) {
	j := &ComputeJob{}
	var hostID sql.NullInt64
	var exitCode sql.NullInt64
	var startedAt, completedAt, cancelledAt sql.NullString
	var envJSON string
	err := row.Scan(&j.ID, &j.ProjectID, &j.OwnerApp, &j.OwnerRef, &j.IdempotencyKey,
		&j.Priority, &j.ResourceClass, &j.Pool, &hostID, &j.Executor, &j.Kind, &j.Command, &j.CWD, &envJSON,
		&j.TimeoutSeconds, &j.Status, &j.Attempt, &j.ProgressPct, &j.Output, &j.Error, &exitCode,
		&j.QueuedAt, &startedAt, &completedAt, &j.UpdatedAt, &cancelledAt)
	if err != nil {
		return nil, err
	}
	if hostID.Valid {
		v := hostID.Int64
		j.HostID = &v
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		j.ExitCode = &v
	}
	j.StartedAt = startedAt.String
	j.CompletedAt = completedAt.String
	j.CancelledAt = cancelledAt.String
	if envJSON != "" {
		_ = json.Unmarshal([]byte(envJSON), &j.Env)
	}
	if j.Env == nil {
		j.Env = map[string]string{}
	}
	return j, nil
}

func parsePriority(v any) int {
	switch x := v.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "high":
			return priorityHigh
		case "heavy", "low":
			return priorityHeavy
		case "normal", "":
			return priorityNormal
		default:
			if n, err := strconv.Atoi(x); err == nil {
				return clampPriority(n)
			}
		}
	case int:
		return clampPriority(x)
	case int64:
		return clampPriority(int(x))
	case float64:
		return clampPriority(int(x))
	}
	return priorityNormal
}

func clampPriority(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

func parseEnv(raw any) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("env must be an object")
	}
	out := map[string]string{}
	for k, v := range m {
		if !safeEnvKey(k) {
			return nil, fmt.Errorf("env key %q is invalid", k)
		}
		out[k] = fmt.Sprint(v)
	}
	return out, nil
}
