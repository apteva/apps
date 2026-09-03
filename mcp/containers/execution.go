package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	executionQueued     = "queued"
	executionRunning    = "running"
	executionSucceeded  = "succeeded"
	executionFailed     = "failed"
	executionCancelling = "cancelling"
	executionCancelled  = "cancelled"
)

type Execution struct {
	ID                   string            `json:"id"`
	WorkloadID           string            `json:"workload_id"`
	ProjectID            string            `json:"project_id,omitempty"`
	OwnerAppInstallID    int64             `json:"owner_app_install_id"`
	OwnerAppName         string            `json:"owner_app_name,omitempty"`
	Argv                 []string          `json:"argv"`
	WorkingDirectory     string            `json:"working_directory,omitempty"`
	Env                  map[string]string `json:"-"`
	EnvKeys              []string          `json:"env_keys,omitempty"`
	TimeoutSeconds       int               `json:"timeout_s"`
	Status               string            `json:"status"`
	ExitCode             *int              `json:"exit_code,omitempty"`
	ErrorCode            string            `json:"error_code,omitempty"`
	Error                string            `json:"error,omitempty"`
	RuntimeContainerID   string            `json:"-"`
	RuntimeContainerName string            `json:"-"`
	OutputBytes          int               `json:"output_bytes"`
	OutputTruncated      bool              `json:"output_truncated"`
	IdempotencyKey       string            `json:"-"`
	CreatedAt            string            `json:"created_at"`
	StartedAt            string            `json:"started_at,omitempty"`
	FinishedAt           string            `json:"finished_at,omitempty"`
	UpdatedAt            string            `json:"updated_at"`
}

type executionInput struct {
	Argv             []string          `json:"argv"`
	ShellCommand     string            `json:"shell_command"`
	WorkingDirectory string            `json:"working_directory"`
	Env              map[string]string `json:"env"`
	TimeoutSeconds   int               `json:"timeout_s"`
	IdempotencyKey   string            `json:"idempotency_key"`
}

type executionRuntimeSpec struct {
	ExecutionID      string
	ContainerName    string
	Image            string
	NetworkName      string
	Volumes          []VolumeSpec
	Env              map[string]string
	Resources        ResourceSpec
	Argv             []string
	WorkingDirectory string
	User             string
}

type executionBackend interface {
	StartExecution(context.Context, executionRuntimeSpec) (string, error)
	InspectExecution(context.Context, string) (*ContainerState, error)
	StopExecution(context.Context, string) error
	ExecutionLogs(context.Context, string, int) (string, error)
	RemoveExecution(context.Context, string) error
}

func normalizeExecutionInput(in executionInput, workload *Workload, defaultTimeout int) (executionInput, error) {
	in.ShellCommand = strings.TrimSpace(in.ShellCommand)
	if len(in.Argv) > 0 && in.ShellCommand != "" {
		return in, errors.New("set either argv or shell_command, not both")
	}
	if in.ShellCommand != "" {
		if len(in.ShellCommand) > 64*1024 {
			return in, errors.New("shell_command exceeds 65536 bytes")
		}
		in.Argv = []string{"/bin/sh", "-c", in.ShellCommand}
	}
	if len(in.Argv) == 0 {
		return in, errors.New("argv is required")
	}
	if len(in.Argv) > 256 {
		return in, errors.New("argv may contain at most 256 arguments")
	}
	for i, arg := range in.Argv {
		if i == 0 && strings.TrimSpace(arg) == "" {
			return in, errors.New("argv[0] is required")
		}
		if strings.IndexByte(arg, 0) >= 0 || len(arg) > 64*1024 {
			return in, fmt.Errorf("argv[%d] is invalid", i)
		}
	}
	if in.WorkingDirectory == "" {
		in.WorkingDirectory = workload.WorkingDirectory
	}
	if in.WorkingDirectory != "" {
		clean, err := cleanContainerPath(in.WorkingDirectory)
		if err != nil {
			return in, fmt.Errorf("working_directory: %w", err)
		}
		in.WorkingDirectory = clean
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = defaultTimeout
	}
	if in.TimeoutSeconds < 1 || in.TimeoutSeconds > 86400 {
		return in, errors.New("timeout_s must be between 1 and 86400")
	}
	if len(in.Env) > 256 {
		return in, errors.New("env may contain at most 256 entries")
	}
	for key, value := range in.Env {
		if !validEnvKey(key) {
			return in, fmt.Errorf("invalid env key %q", key)
		}
		if len(value) > 64*1024 || strings.IndexByte(value, 0) >= 0 {
			return in, fmt.Errorf("env value for %q is invalid", key)
		}
	}
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if len(in.IdempotencyKey) > 256 {
		return in, errors.New("idempotency_key exceeds 256 bytes")
	}
	return in, nil
}

func cleanContainerPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("must be an absolute container path")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == ".." {
			return "", errors.New("must not contain '..'")
		}
	}
	return path.Clean(value), nil
}

func (d LocalDocker) StartExecution(ctx context.Context, spec executionRuntimeSpec) (string, error) {
	args := []string{"run", "-d", "--name", spec.ContainerName,
		"--label", "apteva.containers.execution=" + spec.ExecutionID,
		"--network", spec.NetworkName,
	}
	for _, volume := range spec.Volumes {
		args = append(args, "-v", volume.DockerVolumeName+":"+volume.MountPath)
	}
	keys := envKeys(spec.Env)
	for _, key := range keys {
		args = append(args, "-e", key+"="+spec.Env[key])
	}
	if spec.Resources.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(spec.Resources.MemoryMB)+"m")
	}
	if spec.Resources.CPU > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.Resources.CPU, 'f', -1, 64))
	}
	if spec.WorkingDirectory != "" {
		args = append(args, "--workdir", spec.WorkingDirectory)
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Argv...)
	out, err := docker(ctx, args...)
	return strings.TrimSpace(out), err
}

func (d LocalDocker) InspectExecution(ctx context.Context, name string) (*ContainerState, error) {
	return d.Inspect(ctx, name)
}

func (d LocalDocker) StopExecution(ctx context.Context, name string) error {
	_, err := docker(ctx, "kill", name)
	return err
}

func (d LocalDocker) ExecutionLogs(ctx context.Context, name string, tail int) (string, error) {
	return d.Logs(ctx, name, tail)
}

func (d LocalDocker) RemoveExecution(ctx context.Context, name string) error {
	_, err := docker(ctx, "rm", "-f", name)
	return err
}

func (a *App) startExecution(callCtx context.Context, app *sdk.AppCtx, workload *Workload, owner ownerIdentity, in executionInput) (*Execution, error) {
	if workload.HostID != 0 || workload.InstanceID != 0 {
		return nil, errors.New("container execution currently supports local workloads only")
	}
	if workload.Status == StatusDestroyed || workload.Status == StatusCreating {
		return nil, fmt.Errorf("workload is not executable in status %q", workload.Status)
	}
	in, err := normalizeExecutionInput(in, workload, configInt(app, "default_execution_timeout_seconds", 1800))
	if err != nil {
		return nil, err
	}
	if in.IdempotencyKey == "" {
		if caller := sdk.CallerFrom(callCtx); caller != nil {
			in.IdempotencyKey = caller.ToolCallID
		}
	}
	executionID := newExecutionID()
	execution := &Execution{
		ID: executionID, WorkloadID: workload.ID, ProjectID: workload.ProjectID,
		OwnerAppInstallID: owner.InstallID, OwnerAppName: owner.AppName,
		Argv: append([]string(nil), in.Argv...), WorkingDirectory: in.WorkingDirectory,
		Env: in.Env, EnvKeys: envKeys(in.Env), TimeoutSeconds: in.TimeoutSeconds,
		Status: executionQueued, IdempotencyKey: in.IdempotencyKey,
		RuntimeContainerName: "containers-exec-" + strings.TrimPrefix(executionID, "exe_"),
	}
	created, duplicate, err := insertExecution(app.AppDB(), execution)
	if err != nil {
		return nil, err
	}
	if duplicate {
		return created, nil
	}
	go a.launchExecution(app, created, workload)
	return created, nil
}

func (a *App) launchExecution(app *sdk.AppCtx, execution *Execution, workload *Workload) {
	claimed, err := claimExecution(app.AppDB(), execution.ID)
	if err != nil || !claimed {
		return
	}
	backend, err := a.backendForWorkload(app, workload)
	if err != nil {
		a.failExecution(app, execution.ID, "backend_unavailable", err)
		return
	}
	execBackend, ok := backend.(executionBackend)
	if !ok {
		a.failExecution(app, execution.ID, "unsupported_backend", errors.New("execution is not supported by this container backend"))
		return
	}
	env := make(map[string]string, len(execution.Env))
	for key, value := range execution.Env {
		env[key] = value
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	runtimeID, err := execBackend.StartExecution(startCtx, executionRuntimeSpec{
		ExecutionID: execution.ID, ContainerName: execution.RuntimeContainerName,
		Image: workload.Image, NetworkName: workload.NetworkName, Volumes: workload.Volumes,
		Env: env, Resources: workload.Resources, Argv: execution.Argv,
		WorkingDirectory: execution.WorkingDirectory, User: workload.User,
	})
	cancel()
	if err != nil {
		current, _ := getExecution(app.AppDB(), execution.ID)
		if current != nil && (current.Status == executionCancelling || current.Status == executionCancelled) {
			a.finishExecution(app, current, executionCancelled, nil, "cancelled", "")
		} else {
			a.failExecution(app, execution.ID, "start_failed", err)
		}
		return
	}
	started, err := markExecutionStarted(app.AppDB(), execution.ID, runtimeID)
	if err != nil || !started {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = execBackend.StopExecution(stopCtx, execution.RuntimeContainerName)
		_ = execBackend.RemoveExecution(stopCtx, execution.RuntimeContainerName)
		stopCancel()
		current, _ := getExecution(app.AppDB(), execution.ID)
		if current != nil && !executionTerminal(current.Status) {
			a.finishExecution(app, current, executionCancelled, nil, "cancelled", "")
		}
		return
	}
	current, _ := getExecution(app.AppDB(), execution.ID)
	emitExecution(app, "containers.exec.started", current)
	go a.superviseExecution(app, execution.ID)
}

func (a *App) superviseExecution(app *sdk.AppCtx, id string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		execution, err := getExecution(app.AppDB(), id)
		if err != nil || execution == nil || executionTerminal(execution.Status) {
			return
		}
		if execution.Status == executionCancelling {
			return
		}
		if execution.StartedAt != "" {
			started, _ := time.Parse(time.RFC3339, execution.StartedAt)
			if !started.IsZero() && time.Since(started) >= time.Duration(execution.TimeoutSeconds)*time.Second {
				a.finishExecution(app, execution, executionFailed, nil, "timeout", "execution exceeded timeout")
				return
			}
		}
		backend, err := a.executionBackendFor(app, execution)
		if err != nil {
			a.failExecution(app, id, "backend_unavailable", err)
			return
		}
		inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		state, err := backend.InspectExecution(inspectCtx, execution.RuntimeContainerName)
		cancel()
		if err != nil {
			a.failExecution(app, id, "inspect_failed", err)
			return
		}
		if !state.Running {
			status := executionSucceeded
			code := state.ExitCode
			if code != 0 {
				status = executionFailed
			}
			a.finishExecution(app, execution, status, &code, statusErrorCode(status), "")
			return
		}
		<-ticker.C
	}
}

func (a *App) finishExecution(app *sdk.AppCtx, execution *Execution, status string, exitCode *int, errorCode, message string) {
	backend, _ := a.executionBackendFor(app, execution)
	if errorCode == "timeout" && backend != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = backend.StopExecution(stopCtx, execution.RuntimeContainerName)
		cancel()
	}
	output := ""
	if backend != nil {
		logsCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		output, _ = backend.ExecutionLogs(logsCtx, execution.RuntimeContainerName, 2000)
		cancel()
	}
	capped, truncated := capExecutionOutput(output, configInt(app, "max_execution_output_bytes", 1048576))
	fields := map[string]any{
		"status": status, "error_code": errorCode, "error": message,
		"output": capped, "output_bytes": len(output), "output_truncated": boolInt(truncated),
		"env_json": "{}", "finished_at": nowUTC(), "updated_at": nowUTC(),
	}
	if exitCode != nil {
		fields["exit_code"] = *exitCode
	}
	_ = updateExecution(app.AppDB(), execution.ID, fields)
	current, _ := getExecution(app.AppDB(), execution.ID)
	topic := "containers.exec.failed"
	switch status {
	case executionSucceeded:
		topic = "containers.exec.completed"
	case executionCancelled:
		topic = "containers.exec.cancelled"
	}
	emitExecution(app, topic, current)
}

func (a *App) failExecution(app *sdk.AppCtx, id, code string, err error) {
	execution, _ := getExecution(app.AppDB(), id)
	if execution == nil {
		return
	}
	a.finishExecution(app, execution, executionFailed, nil, code, err.Error())
}

func (a *App) executionBackendFor(app *sdk.AppCtx, execution *Execution) (executionBackend, error) {
	workload, err := getWorkload(app.AppDB(), execution.WorkloadID)
	if err != nil || workload == nil {
		return nil, errors.New("execution workload no longer exists")
	}
	backend, err := a.backendForWorkload(app, workload)
	if err != nil {
		return nil, err
	}
	execBackend, ok := backend.(executionBackend)
	if !ok {
		return nil, errors.New("execution is not supported by this container backend")
	}
	return execBackend, nil
}

func (a *App) cancelExecution(app *sdk.AppCtx, execution *Execution) (*Execution, error) {
	switch execution.Status {
	case executionQueued:
		if err := updateExecution(app.AppDB(), execution.ID, map[string]any{
			"status": executionCancelled, "error_code": "cancelled", "env_json": "{}", "finished_at": nowUTC(), "updated_at": nowUTC(),
		}); err != nil {
			return nil, err
		}
		current, _ := getExecution(app.AppDB(), execution.ID)
		emitExecution(app, "containers.exec.cancelled", current)
		return current, nil
	case executionRunning, executionCancelling:
		_ = updateExecution(app.AppDB(), execution.ID, map[string]any{"status": executionCancelling, "updated_at": nowUTC()})
		backend, err := a.executionBackendFor(app, execution)
		if err != nil {
			return nil, err
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = backend.StopExecution(stopCtx, execution.RuntimeContainerName)
		cancel()
		if err != nil && !isDockerMissingResourceError(err, "container") {
			return nil, err
		}
		if err != nil && execution.RuntimeContainerID == "" {
			// The launch goroutine may still be pulling/creating the execution
			// container. Mark cancelling now; launchExecution observes that state,
			// kills any container it did create, and finalizes the record.
			return getExecution(app.AppDB(), execution.ID)
		}
		a.finishExecution(app, execution, executionCancelled, nil, "cancelled", "")
		return getExecution(app.AppDB(), execution.ID)
	default:
		return execution, nil
	}
}

func reconcileExecutions(ctx context.Context, app *sdk.AppCtx, containerApp *App) error {
	active, err := listActiveExecutions(app.AppDB())
	if err != nil {
		return err
	}
	for _, execution := range active {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch execution.Status {
		case executionQueued:
			workload, _ := getWorkload(app.AppDB(), execution.WorkloadID)
			if workload == nil {
				containerApp.failExecution(app, execution.ID, "workload_missing", errors.New("execution workload no longer exists"))
				continue
			}
			go containerApp.launchExecution(app, execution, workload)
		case executionRunning:
			go containerApp.superviseExecution(app, execution.ID)
		case executionCancelling:
			if execution.RuntimeContainerID == "" {
				if backend, backendErr := containerApp.executionBackendFor(app, execution); backendErr == nil {
					stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					_ = backend.StopExecution(stopCtx, execution.RuntimeContainerName)
					cancel()
				}
				containerApp.finishExecution(app, execution, executionCancelled, nil, "cancelled", "")
			} else {
				_, _ = containerApp.cancelExecution(app, execution)
			}
		}
	}
	return nil
}

func retainExecutionLogs(ctx context.Context, app *sdk.AppCtx, containerApp *App) error {
	hours := configInt(app, "execution_retention_hours", 24)
	if hours < 1 {
		hours = 1
	}
	rows, err := listExpiredExecutions(app.AppDB(), time.Now().UTC().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return err
	}
	for _, execution := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if execution.RuntimeContainerName != "" {
			if backend, err := containerApp.executionBackendFor(app, execution); err == nil {
				rmCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				_ = backend.RemoveExecution(rmCtx, execution.RuntimeContainerName)
				cancel()
			}
		}
		_ = expireExecutionOutput(app.AppDB(), execution.ID)
	}
	return nil
}

func insertExecution(db *sql.DB, execution *Execution) (*Execution, bool, error) {
	now := nowUTC()
	argvJSON, _ := json.Marshal(execution.Argv)
	envJSON, _ := json.Marshal(execution.Env)
	_, err := db.Exec(`INSERT INTO containers_executions (
		id, workload_id, project_id, owner_app_install_id, owner_app_name,
		argv_json, working_directory, env_json, timeout_s, status,
		runtime_container_name, idempotency_key, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		execution.ID, execution.WorkloadID, execution.ProjectID, execution.OwnerAppInstallID,
		execution.OwnerAppName, string(argvJSON), execution.WorkingDirectory, string(envJSON),
		execution.TimeoutSeconds, execution.Status, execution.RuntimeContainerName,
		execution.IdempotencyKey, now, now)
	if err != nil {
		if execution.IdempotencyKey != "" && strings.Contains(strings.ToLower(err.Error()), "unique") {
			existing, getErr := getExecutionByIdempotency(db, execution.OwnerAppInstallID, execution.ProjectID, execution.IdempotencyKey)
			return existing, true, getErr
		}
		return nil, false, err
	}
	created, err := getExecution(db, execution.ID)
	return created, false, err
}

const executionColumns = `id, workload_id, project_id, owner_app_install_id, owner_app_name,
	argv_json, working_directory, env_json, timeout_s, status, exit_code,
	error_code, error, runtime_container_id, runtime_container_name,
	output_bytes, output_truncated, idempotency_key,
	created_at, COALESCE(started_at,''), COALESCE(finished_at,''), updated_at`

func scanExecution(scanner interface{ Scan(...any) error }) (*Execution, error) {
	var execution Execution
	var argvJSON, envJSON string
	var exitCode sql.NullInt64
	var truncated int
	err := scanner.Scan(
		&execution.ID, &execution.WorkloadID, &execution.ProjectID,
		&execution.OwnerAppInstallID, &execution.OwnerAppName,
		&argvJSON, &execution.WorkingDirectory, &envJSON, &execution.TimeoutSeconds,
		&execution.Status, &exitCode, &execution.ErrorCode, &execution.Error,
		&execution.RuntimeContainerID, &execution.RuntimeContainerName,
		&execution.OutputBytes, &truncated, &execution.IdempotencyKey,
		&execution.CreatedAt, &execution.StartedAt, &execution.FinishedAt, &execution.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(argvJSON), &execution.Argv)
	_ = json.Unmarshal([]byte(envJSON), &execution.Env)
	execution.EnvKeys = envKeys(execution.Env)
	if exitCode.Valid {
		code := int(exitCode.Int64)
		execution.ExitCode = &code
	}
	execution.OutputTruncated = truncated != 0
	return &execution, nil
}

func getExecution(db *sql.DB, id string) (*Execution, error) {
	execution, err := scanExecution(db.QueryRow(`SELECT `+executionColumns+` FROM containers_executions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return execution, err
}

func getExecutionByIdempotency(db *sql.DB, ownerID int64, projectID, key string) (*Execution, error) {
	return scanExecution(db.QueryRow(`SELECT `+executionColumns+` FROM containers_executions
		WHERE owner_app_install_id=? AND project_id=? AND idempotency_key=?`, ownerID, projectID, key))
}

func claimExecution(db *sql.DB, id string) (bool, error) {
	res, err := db.Exec(`UPDATE containers_executions SET status='running', updated_at=? WHERE id=? AND status='queued'`, nowUTC(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func markExecutionStarted(db *sql.DB, id, runtimeID string) (bool, error) {
	now := nowUTC()
	res, err := db.Exec(`UPDATE containers_executions
		SET runtime_container_id=?, env_json='{}', started_at=?, updated_at=?
		WHERE id=? AND status='running'`, runtimeID, now, now, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func updateExecution(db *sql.DB, id string, fields map[string]any) error {
	allowed := []string{"status", "exit_code", "error_code", "error", "env_json", "runtime_container_id", "runtime_container_name", "output", "output_bytes", "output_truncated", "started_at", "finished_at", "updated_at"}
	sets := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)
	for _, key := range allowed {
		if value, ok := fields[key]; ok {
			sets = append(sets, key+"=?")
			args = append(args, value)
		}
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := db.Exec(`UPDATE containers_executions SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	return err
}

func listActiveExecutions(db *sql.DB) ([]*Execution, error) {
	rows, err := db.Query(`SELECT ` + executionColumns + ` FROM containers_executions WHERE status IN ('queued','running','cancelling') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var executions []*Execution
	for rows.Next() {
		execution, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func listActiveExecutionsForWorkload(db *sql.DB, workloadID string) ([]*Execution, error) {
	rows, err := db.Query(`SELECT `+executionColumns+` FROM containers_executions
		WHERE workload_id=? AND status IN ('queued','running','cancelling') ORDER BY created_at`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var executions []*Execution
	for rows.Next() {
		execution, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func listExecutionsForWorkload(db *sql.DB, workloadID string) ([]*Execution, error) {
	rows, err := db.Query(`SELECT `+executionColumns+` FROM containers_executions
		WHERE workload_id=? AND runtime_container_name != '' ORDER BY created_at`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var executions []*Execution
	for rows.Next() {
		execution, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func (a *App) cancelWorkloadExecutions(app *sdk.AppCtx, workloadID string) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	executions, err := listActiveExecutionsForWorkload(app.AppDB(), workloadID)
	if err != nil {
		return err
	}
	for _, execution := range executions {
		if _, err := a.cancelExecution(app, execution); err != nil {
			return err
		}
	}
	return nil
}

func removeWorkloadExecutionContainers(ctx context.Context, db *sql.DB, backend DockerBackend, workloadID string) []error {
	executions, err := listExecutionsForWorkload(db, workloadID)
	if err != nil {
		return []error{fmt.Errorf("list workload executions: %w", err)}
	}
	if len(executions) == 0 {
		return nil
	}
	execBackend, ok := backend.(executionBackend)
	if !ok {
		return []error{errors.New("execution cleanup is not supported by this container backend")}
	}
	var cleanupErrs []error
	for _, execution := range executions {
		if execution.RuntimeContainerName == "" {
			continue
		}
		if err := execBackend.RemoveExecution(ctx, execution.RuntimeContainerName); err != nil && !isDockerMissingResourceError(err, "container") {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove execution %s: %w", execution.ID, err))
		}
	}
	return cleanupErrs
}

func listExpiredExecutions(db *sql.DB, before time.Time) ([]*Execution, error) {
	rows, err := db.Query(`SELECT `+executionColumns+` FROM containers_executions
		WHERE status IN ('succeeded','failed','cancelled') AND finished_at < ?
		AND (output != '' OR runtime_container_name != '')`, before.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var executions []*Execution
	for rows.Next() {
		execution, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func expireExecutionOutput(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE containers_executions SET output='', runtime_container_id='', runtime_container_name='', updated_at=? WHERE id=?`, nowUTC(), id)
	return err
}

func executionLogs(db *sql.DB, execution *Execution) (string, error) {
	var output string
	err := db.QueryRow(`SELECT output FROM containers_executions WHERE id=?`, execution.ID).Scan(&output)
	return output, err
}

func executionTerminal(status string) bool {
	switch status {
	case executionSucceeded, executionFailed, executionCancelled:
		return true
	default:
		return false
	}
}

func statusErrorCode(status string) string {
	if status == executionFailed {
		return "nonzero_exit"
	}
	return ""
}

func emitExecution(app *sdk.AppCtx, topic string, execution *Execution) {
	if execution == nil {
		return
	}
	durationMS := int64(0)
	started, _ := time.Parse(time.RFC3339, execution.StartedAt)
	finished, _ := time.Parse(time.RFC3339, execution.FinishedAt)
	if !started.IsZero() && !finished.IsZero() {
		durationMS = finished.Sub(started).Milliseconds()
	}
	payload := map[string]any{
		"workload_id": execution.WorkloadID, "execution_id": execution.ID,
		"status": execution.Status, "duration_ms": durationMS,
	}
	if execution.ExitCode != nil {
		payload["exit_code"] = *execution.ExitCode
	}
	if execution.ErrorCode != "" {
		payload["error_code"] = execution.ErrorCode
	}
	app.EmitWithProject(topic, execution.ProjectID, payload)
}

func capExecutionOutput(output string, limit int) (string, bool) {
	if limit < 0 {
		limit = 0
	}
	if len(output) <= limit {
		return output, false
	}
	return output[len(output)-limit:], true
}

func tailLines(value string, count int) string {
	if count <= 0 || count > 2000 {
		count = 200
	}
	lines := strings.Split(value, "\n")
	if len(lines) <= count {
		return value
	}
	return strings.Join(lines[len(lines)-count:], "\n")
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func configInt(app *sdk.AppCtx, key string, fallback int) int {
	if app == nil {
		return fallback
	}
	value := strings.TrimSpace(app.Config()[key])
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func newExecutionID() string {
	return strings.Replace(newWorkloadID(), "wrk_", "exe_", 1)
}
