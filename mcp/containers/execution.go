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
	SessionKey           string            `json:"session_key,omitempty"`
	StatefulCommand      bool              `json:"stateful_command,omitempty"`
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
	SessionKey       string            `json:"session_key"`
}

type executionRuntimeSpec struct {
	RuntimeReady     func(string) error
	ExecutionID      string
	ContainerName    string
	Env              map[string]string
	Argv             []string
	WorkingDirectory string
	User             string
	SessionKey       string
	StatefulCommand  bool
	MaxOutputBytes   int
}

type executionBackend interface {
	StartExecution(context.Context, executionRuntimeSpec) (string, error)
	InspectExecution(context.Context, *Execution) (*ContainerState, error)
	StopExecution(context.Context, *Execution) error
	ExecutionLogs(context.Context, *Execution, int) (string, error)
	RemoveExecution(context.Context, *Execution) error
}

func normalizeExecutionInput(in executionInput, workload *Workload, defaultTimeout int) (executionInput, error) {
	in.SessionKey = strings.TrimSpace(in.SessionKey)
	if len(in.SessionKey) > 64 {
		return in, errors.New("session_key exceeds 64 bytes")
	}
	for _, r := range in.SessionKey {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return in, errors.New("session_key may contain only letters, numbers, '.', '-', and '_'")
		}
	}
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
	if in.WorkingDirectory == "" && in.SessionKey == "" {
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
	totalBytes := 0
	for _, arg := range in.Argv {
		totalBytes += len(arg)
	}
	for key, value := range in.Env {
		totalBytes += len(key) + len(value)
		if totalBytes > maxEnvironmentBytes {
			return in, errors.New("execution input exceeds total byte limit")
		}
		if !validEnvKey(key) {
			return in, fmt.Errorf("invalid env key %q", key)
		}
		if len(value) > 64*1024 || strings.IndexByte(value, 0) >= 0 {
			return in, fmt.Errorf("env value for %q is invalid", key)
		}
	}
	if totalBytes > maxEnvironmentBytes {
		return in, errors.New("execution input exceeds total byte limit")
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

func (a *App) startExecution(callCtx context.Context, app *sdk.AppCtx, workload *Workload, owner ownerIdentity, in executionInput) (*Execution, error) {
	a.executionMu.Lock()
	defer a.executionMu.Unlock()
	var active int
	if err := app.AppDB().QueryRow(`SELECT COUNT(*) FROM containers_executions WHERE status IN ('queued','running','cancelling')`).Scan(&active); err != nil {
		return nil, err
	}
	if active >= 64 {
		return nil, fmt.Errorf("%w: active execution limit reached", errConflict)
	}
	if workload.HostID != 0 || workload.InstanceID != 0 {
		return nil, errors.New("container execution currently supports local workloads only")
	}
	if workload.Status != StatusRunning {
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
		SessionKey:      in.SessionKey,
		StatefulCommand: in.ShellCommand != "",
		Status:          executionQueued, IdempotencyKey: in.IdempotencyKey,
		RuntimeContainerName: workload.ContainerName,
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
	a.executionMu.Lock()
	if a.launches == nil {
		a.launches = map[string]bool{}
	}
	if a.launches[execution.ID] {
		a.executionMu.Unlock()
		return
	}
	a.launches[execution.ID] = true
	a.executionMu.Unlock()
	defer func() { a.executionMu.Lock(); delete(a.launches, execution.ID); a.executionMu.Unlock() }()
	opCtx, unlock, lockErr := a.lockWorkload(context.Background(), workload.ID, false)
	if lockErr != nil {
		return
	}
	defer unlock()
	_ = opCtx
	fresh, err := getWorkload(app.AppDB(), workload.ID)
	if err != nil || fresh == nil || fresh.Status != StatusRunning {
		a.failExecution(app, execution.ID, "workload_unavailable", errors.New("workload is not running"))
		return
	}
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
		ExecutionID: execution.ID, ContainerName: workload.ContainerName,
		RuntimeReady: func(id string) error {
			_, err := app.AppDB().Exec(`UPDATE containers_executions SET runtime_container_id=?, updated_at=? WHERE id=? AND status IN ('running','cancelling')`, id, nowUTC(), execution.ID)
			return err
		},
		Env: env, Argv: execution.Argv,
		WorkingDirectory: execution.WorkingDirectory, User: workload.User,
		SessionKey:      execution.SessionKey,
		StatefulCommand: execution.StatefulCommand, MaxOutputBytes: configInt(app, "max_execution_output_bytes", 1048576),
	})
	cancel()
	if err != nil {
		current, _ := getExecution(app.AppDB(), execution.ID)
		if current != nil && current.RuntimeContainerID != "" {
			_, _ = transitionExecution(app.AppDB(), current.ID, []string{executionRunning}, map[string]any{"status": executionCancelling, "error_code": "start_failed", "error": err.Error()})
			go a.superviseExecution(app, current.ID)
		} else {
			a.failExecution(app, execution.ID, "start_failed", err)
		}
		return
	}
	started, err := markExecutionStarted(app.AppDB(), execution.ID, runtimeID)
	if err != nil || !started {
		// Identity was persisted before start. Reconciliation owns cancellation and
		// must verify termination before publishing a terminal result.
		_, _ = app.AppDB().Exec(`UPDATE containers_executions SET runtime_container_id=?, status='cancelling', updated_at=? WHERE id=? AND status IN ('running','cancelling')`, runtimeID, nowUTC(), execution.ID)
		go a.superviseExecution(app, execution.ID)
		return
	}
	current, _ := getExecution(app.AppDB(), execution.ID)
	emitExecution(app, "containers.exec.started", current)
	go a.superviseExecution(app, execution.ID)
}

func (a *App) superviseExecution(app *sdk.AppCtx, id string) {
	a.executionMu.Lock()
	if a.supervisors == nil {
		a.supervisors = map[string]bool{}
	}
	if a.supervisors[id] {
		a.executionMu.Unlock()
		return
	}
	a.supervisors[id] = true
	a.executionMu.Unlock()
	defer func() { a.executionMu.Lock(); delete(a.supervisors, id); a.executionMu.Unlock() }()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		execution, err := getExecution(app.AppDB(), id)
		if err != nil || execution == nil || executionTerminal(execution.Status) {
			return
		}
		a.executionMu.Lock()
		launchingNow := a.launches[id]
		a.executionMu.Unlock()
		if launchingNow {
			<-ticker.C
			continue
		}
		if execution.Status == executionCancelling {
			_, _ = a.cancelExecution(app, execution)
			<-ticker.C
			continue
		}
		if execution.StartedAt == "" {
			_, _ = transitionExecution(app.AppDB(), id, []string{executionRunning}, map[string]any{"error_code": "startup_interrupted", "error": "app stopped before execution startup was confirmed"})
			_, _ = a.cancelExecution(app, execution)
			<-ticker.C
			continue
		}
		if execution.RuntimeContainerID == "" {
			a.executionMu.Lock()
			launching := a.launches[id]
			a.executionMu.Unlock()
			if launching {
				<-ticker.C
				continue
			}
			_, _ = a.cancelExecution(app, execution)
			<-ticker.C
			continue
		}
		if execution.StartedAt != "" {
			started, _ := time.Parse(time.RFC3339, execution.StartedAt)
			if !started.IsZero() && time.Since(started) >= time.Duration(execution.TimeoutSeconds)*time.Second {
				_, _ = transitionExecution(app.AppDB(), id, []string{executionRunning}, map[string]any{"error_code": "timeout", "error": "execution exceeded timeout"})
				_, _ = a.cancelExecution(app, execution)
				<-ticker.C
				continue
			}
		}
		backend, err := a.executionBackendFor(app, execution)
		if err != nil {
			_, _ = transitionExecution(app.AppDB(), id, []string{executionRunning}, map[string]any{"error": err.Error()})
			<-ticker.C
			continue
		}
		inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		state, err := backend.InspectExecution(inspectCtx, execution)
		cancel()
		if err != nil {
			_, _ = transitionExecution(app.AppDB(), id, []string{executionRunning}, map[string]any{"error": err.Error()})
			<-ticker.C
			continue
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
		_ = backend.StopExecution(stopCtx, execution)
		cancel()
	}
	output := ""
	total := 0
	wasTruncated := false
	if backend != nil {
		logsCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		var err error
		output, total, wasTruncated, err = readExecutionOutput(logsCtx, backend, execution)
		cancel()
		if err != nil {
			message = strings.TrimSpace(message + "; capture logs: " + err.Error())
		}
	}
	capped, truncated := capExecutionOutput(output, executionOutputLimit(configInt(app, "max_execution_output_bytes", 1048576)))
	truncated = truncated || wasTruncated
	fields := map[string]any{
		"status": status, "error_code": errorCode, "error": message,
		"output": capped, "output_bytes": total, "output_truncated": boolInt(truncated),
		"env_json": "{}", "finished_at": nowUTC(), "updated_at": nowUTC(),
	}
	if exitCode != nil {
		fields["exit_code"] = *exitCode
	}
	allowed := []string{executionRunning}
	if status == executionCancelled {
		allowed = []string{executionQueued, executionRunning, executionCancelling}
	} else if errorCode == "timeout" || (errorCode == "start_failed" || errorCode == "startup_interrupted") && execution.Status == executionCancelling {
		allowed = []string{executionCancelling}
	} else if execution.Status == executionQueued {
		allowed = []string{executionQueued}
	}
	won, err := transitionExecution(app.AppDB(), execution.ID, allowed, fields)
	if err != nil {
		app.Logger().Warn("persist execution completion failed", "execution_id", execution.ID, "error", err)
		return
	}
	if !won {
		return
	}
	if persistentShellRuntime(execution.RuntimeContainerID) {
		persistentShells.Remove(execution)
	}
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
	workload, err := getWorkloadBase(app.AppDB(), "WHERE id=?", execution.WorkloadID)
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
	if executionTerminal(execution.Status) {
		return execution, nil
	}
	won, err := transitionExecution(app.AppDB(), execution.ID, []string{executionQueued}, map[string]any{"status": executionCancelled, "env_json": "{}", "error_code": "cancelled", "finished_at": nowUTC(), "updated_at": nowUTC()})
	if err != nil {
		return nil, err
	}
	if won {
		current, err := getExecution(app.AppDB(), execution.ID)
		if err == nil {
			emitExecution(app, "containers.exec.cancelled", current)
		}
		return current, err
	}
	_, err = transitionExecution(app.AppDB(), execution.ID, []string{executionRunning}, map[string]any{"status": executionCancelling, "updated_at": nowUTC()})
	if err != nil {
		return nil, err
	}
	current, err := getExecution(app.AppDB(), execution.ID)
	if err != nil || current == nil {
		return current, err
	}
	if executionTerminal(current.Status) {
		return current, nil
	}
	a.executionMu.Lock()
	launching := a.launches[current.ID]
	a.executionMu.Unlock()
	if launching {
		return current, errors.New("execution cancellation is pending startup completion")
	}
	backend, err := a.executionBackendFor(app, current)
	if err != nil {
		return current, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = backend.StopExecution(ctx, current); err != nil && !isDockerMissingResourceError(err, "container") {
		return current, err
	}
	if current.RuntimeContainerID != "" {
		state, inspectErr := backend.InspectExecution(ctx, current)
		if inspectErr != nil && !isDockerMissingResourceError(inspectErr, "container") {
			return current, inspectErr
		}
		if state != nil && state.Running {
			return current, errors.New("execution termination is pending")
		}
	}
	status, code := executionCancelled, "cancelled"
	if current.ErrorCode == "timeout" || current.ErrorCode == "start_failed" || current.ErrorCode == "startup_interrupted" {
		status, code = executionFailed, current.ErrorCode
	}
	a.finishExecution(app, current, status, nil, code, current.Error)
	return getExecution(app.AppDB(), execution.ID)
}

func reconcileExecutions(ctx context.Context, app *sdk.AppCtx, containerApp *App) error {
	return reconcileScopedExecutions(ctx, app, containerApp, app.CurrentProject() != "")
}
func reconcileScopedExecutions(ctx context.Context, app *sdk.AppCtx, containerApp *App, scoped bool) error {
	active, err := queryActiveExecutions(app.AppDB(), app.CurrentProject(), scoped)
	if err != nil {
		return err
	}
	for _, execution := range active {
		if app.CurrentProject() != "" && execution.ProjectID != app.CurrentProject() {
			continue
		}
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
			go containerApp.launchExecution(app.WithProject(execution.ProjectID), execution, workload)
		case executionRunning:
			go containerApp.superviseExecution(app.WithProject(execution.ProjectID), execution.ID)
		case executionCancelling:
			go containerApp.superviseExecution(app.WithProject(execution.ProjectID), execution.ID)
		}
	}
	return nil
}

func retainExecutionLogs(ctx context.Context, app *sdk.AppCtx, containerApp *App) error {
	hours := configInt(app, "execution_retention_hours", 24)
	if hours < 1 {
		hours = 1
	}
	rows, err := listExpiredExecutions(app.AppDB(), time.Now().UTC().Add(-time.Duration(hours)*time.Hour), app.CurrentProject())
	if err != nil {
		return err
	}
	for _, execution := range rows {
		if execution.ProjectID != app.CurrentProject() {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if execution.RuntimeContainerName != "" {
			backend, err := containerApp.executionBackendFor(app.WithProject(execution.ProjectID), execution)
			if err != nil {
				continue
			}
			rmCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = backend.RemoveExecution(rmCtx, execution)
			cancel()
			if err != nil {
				app.Logger().Warn("execution cleanup failed", "execution_id", execution.ID, "error", err)
				continue
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
		argv_json, working_directory, env_json, timeout_s, session_key, stateful_command, status,
		runtime_container_name, idempotency_key, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		execution.ID, execution.WorkloadID, execution.ProjectID, execution.OwnerAppInstallID,
		execution.OwnerAppName, string(argvJSON), execution.WorkingDirectory, string(envJSON),
		execution.TimeoutSeconds, execution.SessionKey, execution.StatefulCommand, execution.Status, execution.RuntimeContainerName,
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
	argv_json, working_directory, env_json, timeout_s, session_key, stateful_command, status, exit_code,
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
		&execution.SessionKey, &execution.StatefulCommand, &execution.Status, &exitCode, &execution.ErrorCode, &execution.Error,
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

func removeWorkloadExecutionRuntime(ctx context.Context, db *sql.DB, backend DockerBackend, workloadID string) []error {
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
		if err := execBackend.RemoveExecution(ctx, execution); err != nil && !isDockerMissingResourceError(err, "container") {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove execution %s: %w", execution.ID, err))
		}
	}
	return cleanupErrs
}

func listExpiredExecutions(db *sql.DB, before time.Time, project ...string) ([]*Execution, error) {
	query := `SELECT ` + executionColumns + ` FROM containers_executions WHERE status IN ('succeeded','failed','cancelled') AND finished_at < ? AND (output != '' OR runtime_container_name != '')`
	args := []any{before.Format(time.RFC3339)}
	if len(project) > 0 {
		query += " AND project_id=?"
		args = append(args, project[0])
	}
	query += " ORDER BY finished_at LIMIT 100"
	rows, err := db.Query(query, args...)
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
