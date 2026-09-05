package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (m *persistentShellManager) List(container string) []map[string]any {
	out := []map[string]any{}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.containerName != container || s.isClosed() {
			continue
		}
		s.mu.Lock()
		active := s.current != nil && s.current.isRunning()
		out = append(out, map[string]any{"session_key": s.sessionKey, "active": active, "last_used": s.lastUsed.UTC().Format(time.RFC3339)})
		s.mu.Unlock()
	}
	return out
}
func listWorkloadHistory(db *sql.DB, id string) ([]*Execution, error) {
	rows, err := db.Query(`SELECT `+executionColumns+` FROM containers_executions WHERE workload_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Execution{}
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (a *App) handleOperatorAction(w http.ResponseWriter, r *http.Request, app *sdk.AppCtx, workload *Workload, parts []string) bool {
	if len(parts) < 2 {
		return false
	}
	switch parts[1] {
	case "executions":
		if r.Method == http.MethodGet && len(parts) == 3 {
			e, err := getExecution(app.AppDB(), parts[2])
			if err != nil || e == nil || e.WorkloadID != workload.ID {
				writeResult(w, nil, errWorkloadNotFound)
				return true
			}
			output, err := executionLogs(app.AppDB(), e)
			if !executionTerminal(e.Status) {
				backend, bErr := a.executionBackendFor(app, e)
				if bErr != nil {
					err = bErr
				} else {
					var total int
					var truncated bool
					output, total, truncated, err = readExecutionOutput(r.Context(), backend, e)
					e.OutputBytes = total
					e.OutputTruncated = truncated
				}
			}
			writeResult(w, map[string]any{"output": output, "output_truncated": e.OutputTruncated}, err)
			return true
		}
		if r.Method == http.MethodGet && len(parts) == 2 {
			rows, err := listWorkloadHistory(app.AppDB(), workload.ID)
			writeResult(w, map[string]any{"executions": rows, "sessions": persistentShells.List(workload.ContainerName)}, err)
			return true
		}
		if r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "cancel" {
			e, err := getExecution(app.AppDB(), parts[2])
			if err != nil || e == nil || e.WorkloadID != workload.ID {
				writeResult(w, nil, errWorkloadNotFound)
				return true
			}
			e, err = a.cancelExecution(app, e)
			writeResult(w, map[string]any{"execution": e}, err)
			return true
		}
	case "sessions":
		if r.Method == http.MethodDelete && len(parts) == 3 {
			key, err := url.PathUnescape(parts[2])
			if err == nil {
				err = persistentShells.CloseSession(workload.ContainerName, key)
			}
			writeResult(w, map[string]any{"closed": err == nil}, err)
			return true
		}
	}
	return false
}
func (a *App) toolSessions(call context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner, err := requireAppOwner(call, app)
	if err != nil {
		return nil, err
	}
	w, err := requireOwnedWorkload(app.AppDB(), getStr(args, "workload_id"), owner)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sessions": persistentShells.List(w.ContainerName)}, nil
}
func (a *App) toolSessionClose(call context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner, err := requireAppOwner(call, app)
	if err != nil {
		return nil, err
	}
	w, err := requireOwnedWorkload(app.AppDB(), getStr(args, "workload_id"), owner)
	if err != nil {
		return nil, err
	}
	err = persistentShells.CloseSession(w.ContainerName, getStr(args, "session_key"))
	return map[string]any{"closed": err == nil}, err
}

// Scope legacy maintenance once per tick across SDK project dispatches.
func (a *App) maintenance(ctx context.Context, app *sdk.AppCtx) error {
	persistentShells.ExpireIdle()
	if err := a.recoverRuntimeCleanup(ctx, app); err != nil {
		return err
	}
	if err := a.recoverArchivePauses(ctx, app); err != nil {
		return err
	}
	if a.claimLegacyTick(app, false) {
		legacy := app.WithProject("")
		if err := a.recoverRuntimeCleanup(ctx, legacy); err != nil {
			return err
		}
		if err := a.recoverArchivePauses(ctx, legacy); err != nil {
			return err
		}
		if err := reconcileScopedExecutions(ctx, legacy, a, true); err != nil {
			return err
		}
		if err := retainExecutionLogs(ctx, legacy, a); err != nil {
			return err
		}
	}
	if err := reconcileScopedExecutions(ctx, app, a, true); err != nil {
		return err
	}
	return retainExecutionLogs(ctx, app, a)
}
func decodeHTTPRun(r *http.Request) (RunSpec, error) {
	var spec RunSpec
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&spec)
	if err != nil {
		return spec, typedInputError(err)
	}
	return spec, nil
}
func (a *App) claimLegacyTick(app *sdk.AppCtx, health bool) bool {
	if app.CurrentProject() == "" || (globalCtx != nil && globalCtx.CurrentProject() != "") {
		return false
	}
	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()
	last := &a.legacyExecution
	interval := 10 * time.Second
	if health {
		last = &a.legacyHealth
		interval = 30 * time.Second
	}
	if time.Since(*last) < interval {
		return false
	}
	*last = time.Now()
	return true
}
func queryActiveExecutions(db *sql.DB, project string, scoped bool) ([]*Execution, error) {
	q := `SELECT ` + executionColumns + ` FROM containers_executions WHERE status IN ('queued','running','cancelling')`
	args := []any{}
	if scoped {
		q += " AND project_id=?"
		args = append(args, project)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*Execution{}
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
