package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

func transitionExecution(db *sql.DB, id string, from []string, fields map[string]any) (bool, error) {
	allowed := []string{"status", "exit_code", "error_code", "error", "env_json", "output", "output_bytes", "output_truncated", "finished_at", "updated_at"}
	sets := []string{}
	args := []any{}
	for _, key := range allowed {
		if val, ok := fields[key]; ok {
			sets = append(sets, key+"=?")
			args = append(args, val)
		}
	}
	if len(sets) == 0 || len(from) == 0 {
		return false, errors.New("empty execution transition")
	}
	args = append(args, id)
	marks := []string{}
	for _, state := range from {
		marks = append(marks, "?")
		args = append(args, state)
	}
	result, err := db.Exec(`UPDATE containers_executions SET `+strings.Join(sets, ",")+` WHERE id=? AND status IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

type executionOutputReader interface {
	ExecutionOutput(context.Context, *Execution) (string, int, bool, error)
}

func readExecutionOutput(ctx context.Context, backend executionBackend, e *Execution) (string, int, bool, error) {
	if reader, ok := backend.(executionOutputReader); ok {
		return reader.ExecutionOutput(ctx, e)
	}
	out, err := backend.ExecutionLogs(ctx, e, 2000)
	return out, len(out), false, err
}
func (d LocalDocker) ExecutionOutput(ctx context.Context, e *Execution) (string, int, bool, error) {
	if persistentShellRuntime(e.RuntimeContainerID) {
		state := persistentShells.execution(e.ID)
		if state == nil {
			return "", 0, false, nil
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		return strings.ReplaceAll(string(state.output), "\r\n", "\n"), state.totalBytes, state.totalBytes > len(state.output), nil
	}
	if legacyExecutionContainer(e) {
		out, err := d.Logs(ctx, e.RuntimeContainerName, 2000)
		return out, len(out), false, err
	}
	out, err := dockerWithInputLimit(ctx, nil, maxDockerOutputBytes+128, "exec", e.RuntimeContainerName, "sh", "-c", `if [ -f "$1/output" ]; then cat "$1/bytes"; printf '\n'; cat "$1/output"; else printf '0\n'; fi`, "sh", executionControlDir(e.ID))
	if err != nil {
		return "", 0, false, err
	}
	count, body, _ := strings.Cut(out, "\n")
	total, _ := strconv.Atoi(strings.TrimSpace(count))
	if total < len(body) {
		total = len(body)
	}
	return body, total, total > len(body), nil
}
