package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const workspaceColumns = `id, project_id, name, purpose, profile, image, workload_id,
	lifecycle_status, activity_status, runtime_status, health_status, host_label,
	network_policy, cpu, memory_mb, consumer_app, consumer_install_id,
	owner_agent_id, owner_thread_id, owner_label, resource_kind, resource_id,
	repo_label, branch_label, origin_label, origin_href, dirty_state, unpushed_state,
	source_digest, source_manifest_json, source_synced_at, last_error, created_at,
	updated_at, last_activity_at, expires_at, delete_at, destroyed_at`

func insertWorkspace(db *sql.DB, w *Workspace, idempotencyKey string) error {
	_, err := db.Exec(`INSERT INTO workspaces (
		id, project_id, name, purpose, profile, image, workload_id,
		lifecycle_status, activity_status, runtime_status, health_status, host_label,
		network_policy, cpu, memory_mb, consumer_app, consumer_install_id,
		owner_agent_id, owner_thread_id, owner_label, resource_kind, resource_id,
		repo_label, branch_label, origin_label, origin_href, dirty_state, unpushed_state,
		source_digest, source_manifest_json, source_synced_at,
		last_error, idempotency_key, created_at, updated_at, last_activity_at,
		expires_at, delete_at, destroyed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.ProjectID, w.Name, w.Purpose, w.Profile, w.Image, w.WorkloadID,
		w.LifecycleStatus, w.ActivityStatus, w.RuntimeStatus, w.HealthStatus, w.HostLabel,
		w.NetworkPolicy, w.CPU, w.MemoryMB, w.ConsumerApp, w.ConsumerInstallID,
		w.OwnerAgentID, w.OwnerThreadID, w.OwnerLabel, w.ResourceKind, w.ResourceID,
		w.RepoLabel, w.BranchLabel, w.OriginLabel, w.OriginHref, w.DirtyState, w.UnpushedState,
		w.SourceDigest, mustJSON(w.SourceManifest), w.SourceSyncedAt,
		w.LastError, idempotencyKey, w.CreatedAt, w.UpdatedAt, w.LastActivityAt,
		w.ExpiresAt, w.DeleteAt, w.DestroyedAt)
	return err
}

func scanWorkspace(scanner interface{ Scan(...any) error }) (*Workspace, error) {
	var w Workspace
	var sourceManifest string
	err := scanner.Scan(
		&w.ID, &w.ProjectID, &w.Name, &w.Purpose, &w.Profile, &w.Image, &w.WorkloadID,
		&w.LifecycleStatus, &w.ActivityStatus, &w.RuntimeStatus, &w.HealthStatus, &w.HostLabel,
		&w.NetworkPolicy, &w.CPU, &w.MemoryMB, &w.ConsumerApp, &w.ConsumerInstallID,
		&w.OwnerAgentID, &w.OwnerThreadID, &w.OwnerLabel, &w.ResourceKind, &w.ResourceID,
		&w.RepoLabel, &w.BranchLabel, &w.OriginLabel, &w.OriginHref, &w.DirtyState, &w.UnpushedState,
		&w.SourceDigest, &sourceManifest, &w.SourceSyncedAt,
		&w.LastError, &w.CreatedAt, &w.UpdatedAt, &w.LastActivityAt, &w.ExpiresAt, &w.DeleteAt, &w.DestroyedAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(sourceManifest), &w.SourceManifest)
	w.deriveStatus()
	return &w, nil
}

func getWorkspace(db *sql.DB, projectID, id string) (*Workspace, error) {
	w, err := scanWorkspace(db.QueryRow(`SELECT `+workspaceColumns+` FROM workspaces WHERE project_id=? AND id=?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

func getWorkspaceByIdempotency(db *sql.DB, projectID, key string) (*Workspace, error) {
	w, err := scanWorkspace(db.QueryRow(`SELECT `+workspaceColumns+` FROM workspaces WHERE project_id=? AND idempotency_key=?`, projectID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

func listWorkspaces(db *sql.DB, projectID, status string, includeDestroyed bool, limit int) ([]*Workspace, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + workspaceColumns + ` FROM workspaces WHERE project_id=?`
	args := []any{projectID}
	status = strings.TrimSpace(strings.ToLower(status))
	if status == "busy" {
		query += ` AND lifecycle_status='running' AND activity_status='executing'`
	} else if status != "" {
		query += ` AND lifecycle_status=?`
		args = append(args, status)
	} else if !includeDestroyed {
		query += ` AND lifecycle_status!='destroyed'`
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Workspace, 0)
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func listReconcileWorkspaces(db *sql.DB) ([]*Workspace, error) {
	rows, err := db.Query(`SELECT ` + workspaceColumns + ` FROM workspaces
		WHERE lifecycle_status IN ('provisioning','running','suspended','failed','expired')
		ORDER BY updated_at LIMIT 250`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Workspace, 0)
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func updateWorkspace(db *sql.DB, id string, fields map[string]any) error {
	allowed := []string{
		"name", "purpose", "workload_id", "lifecycle_status", "activity_status",
		"runtime_status", "health_status", "repo_label", "branch_label", "origin_label",
		"origin_href", "dirty_state", "unpushed_state", "last_error", "updated_at",
		"last_activity_at", "expires_at", "delete_at", "destroyed_at", "source_digest",
		"source_manifest_json", "source_synced_at",
	}
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
	_, err := db.Exec(`UPDATE workspaces SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	return err
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func insertCommand(db *sql.DB, c *Command, idempotencyKey string) error {
	argv, _ := json.Marshal(c.Argv)
	_, err := db.Exec(`INSERT INTO workspace_commands (
		id, workspace_id, project_id, execution_id, display_command, argv_json,
		working_directory, timeout_s, actor_kind, actor_id, actor_label, status,
		error_code, error, output_bytes, output_truncated, idempotency_key,
		created_at, started_at, finished_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.ProjectID, c.ExecutionID, c.DisplayCommand, string(argv),
		c.WorkingDirectory, c.TimeoutSeconds, c.ActorKind, c.ActorID, c.ActorLabel, c.Status,
		c.ErrorCode, c.Error, c.OutputBytes, boolInt(c.OutputTruncated), idempotencyKey,
		c.CreatedAt, c.StartedAt, c.FinishedAt, c.UpdatedAt)
	return err
}

const commandColumns = `id, workspace_id, project_id, execution_id, display_command,
	argv_json, working_directory, timeout_s, actor_kind, actor_id, actor_label,
	status, exit_code, error_code, error, output_bytes, output_truncated,
	created_at, started_at, finished_at, updated_at`

func scanCommand(scanner interface{ Scan(...any) error }) (*Command, error) {
	var c Command
	var argv string
	var exit sql.NullInt64
	var truncated int
	err := scanner.Scan(
		&c.ID, &c.WorkspaceID, &c.ProjectID, &c.ExecutionID, &c.DisplayCommand,
		&argv, &c.WorkingDirectory, &c.TimeoutSeconds, &c.ActorKind, &c.ActorID, &c.ActorLabel,
		&c.Status, &exit, &c.ErrorCode, &c.Error, &c.OutputBytes, &truncated,
		&c.CreatedAt, &c.StartedAt, &c.FinishedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(argv), &c.Argv)
	if exit.Valid {
		code := int(exit.Int64)
		c.ExitCode = &code
	}
	c.OutputTruncated = truncated != 0
	return &c, nil
}

func getCommand(db *sql.DB, projectID, workspaceID, id string) (*Command, error) {
	c, err := scanCommand(db.QueryRow(`SELECT `+commandColumns+` FROM workspace_commands WHERE project_id=? AND workspace_id=? AND id=?`, projectID, workspaceID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func getCommandByIdempotency(db *sql.DB, projectID, key string) (*Command, error) {
	c, err := scanCommand(db.QueryRow(`SELECT `+commandColumns+` FROM workspace_commands WHERE project_id=? AND idempotency_key=?`, projectID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func getCommandByExecution(db *sql.DB, executionID string) (*Command, error) {
	c, err := scanCommand(db.QueryRow(`SELECT `+commandColumns+` FROM workspace_commands WHERE execution_id=?`, executionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func listCommands(db *sql.DB, projectID, workspaceID string, limit int) ([]*Command, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT `+commandColumns+` FROM workspace_commands
		WHERE project_id=? AND workspace_id=? ORDER BY created_at DESC LIMIT ?`, projectID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Command, 0)
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func listActiveCommands(db *sql.DB, workspaceID string) ([]*Command, error) {
	rows, err := db.Query(`SELECT `+commandColumns+` FROM workspace_commands
		WHERE workspace_id=? AND status IN ('queued','running','cancelling') ORDER BY created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Command, 0)
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func updateCommand(db *sql.DB, id string, fields map[string]any) error {
	allowed := []string{
		"execution_id", "status", "exit_code", "error_code", "error", "output_bytes",
		"output_truncated", "started_at", "finished_at", "updated_at",
	}
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
	_, err := db.Exec(`UPDATE workspace_commands SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	return err
}

func recordActivity(db *sql.DB, workspaceID, projectID, eventType string, actor Actor, summary string, data any) error {
	raw := "{}"
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		raw = string(encoded)
	}
	_, err := db.Exec(`INSERT INTO workspace_activity (
		workspace_id, project_id, event_type, actor_kind, actor_id, actor_label,
		summary, data_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, workspaceID, projectID, eventType,
		actor.Kind, actor.ID, actor.Label, summary, raw, nowUTC())
	return err
}

func listActivity(db *sql.DB, projectID, workspaceID string, limit int) ([]*Activity, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT id, workspace_id, project_id, event_type,
		actor_kind, actor_id, actor_label, summary, data_json, created_at
		FROM workspace_activity WHERE project_id=? AND workspace_id=? ORDER BY id DESC LIMIT ?`,
		projectID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Activity, 0)
	for rows.Next() {
		var a Activity
		var raw string
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.ProjectID, &a.EventType,
			&a.ActorKind, &a.ActorID, &a.ActorLabel, &a.Summary, &raw, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Data = json.RawMessage(raw)
		out = append(out, &a)
	}
	return out, rows.Err()
}

func requireWorkspace(db *sql.DB, projectID, id string) (*Workspace, error) {
	w, err := getWorkspace(db, projectID, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errors.New("workspace not found")
	}
	return w, nil
}

func requireCommand(db *sql.DB, projectID, workspaceID, id string) (*Command, error) {
	c, err := getCommand(db, projectID, workspaceID, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("command not found")
	}
	return c, nil
}

func ensureNoActiveCommand(db *sql.DB, workspaceID string) error {
	active, err := listActiveCommands(db, workspaceID)
	if err != nil {
		return err
	}
	if len(active) > 0 {
		return fmt.Errorf("workspace already has active command %s", active[0].ID)
	}
	return nil
}

func commandTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "timed_out":
		return true
	default:
		return false
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
