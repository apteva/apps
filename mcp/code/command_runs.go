package main

// Command runs (Code 0.21): every repos_run_command call is kept with its
// exit code, duration and the tail of its output, so test and build results
// stay visible in the Code panel after the call that produced them. The Git
// operation log (repo_git_operations) is exposed next to it.

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const commandRunOutputCap = 64 << 10
const commandRunsKept = 200

type CommandRun struct {
	ID          int64  `json:"id"`
	Runtime     string `json:"runtime"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Command     string `json:"command"`
	Status      string `json:"status"`
	ExitCode    int    `json:"exit_code"`
	TimedOut    bool   `json:"timed_out"`
	DurationMS  int64  `json:"duration_ms"`
	Actor       string `json:"actor,omitempty"`
	OutputTail  string `json:"output_tail,omitempty"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type GitOperation struct {
	ID        int64  `json:"id"`
	Operation string `json:"operation"`
	Actor     string `json:"actor"`
	FromSHA   string `json:"from_sha,omitempty"`
	ToSHA     string `json:"to_sha,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
}

// recordCommandRun stores one finished (or failed) command and keeps the
// newest commandRunsKept rows per repository.
func recordCommandRun(app *sdk.AppCtx, repo *Repo, runtime, command, actor string, res *repoCommandResult, runErr error) {
	if app == nil || app.AppDB() == nil || repo == nil {
		return
	}
	run := CommandRun{Runtime: runtime, Command: command, Actor: actor, Status: "error"}
	if res != nil {
		run.Status, run.ExitCode, run.TimedOut, run.DurationMS, run.WorkspaceID = res.Status, res.ExitCode, res.TimedOut, res.DurationMS, res.WorkspaceID
		run.OutputTail = firstNonEmpty(res.LogTail, strings.TrimSpace(res.StdoutTail+"\n"+res.StderrTail))
		run.Error = res.Error
	}
	if runErr != nil {
		run.Status, run.Error = "error", runErr.Error()
	}
	if len(run.OutputTail) > commandRunOutputCap {
		run.OutputTail = run.OutputTail[len(run.OutputTail)-commandRunOutputCap:]
	}
	db := app.AppDB()
	result, err := db.Exec(`INSERT INTO repo_command_runs(repo_id, runtime, workspace_id, command, status, exit_code, timed_out, duration_ms, actor, output_tail, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, repo.ID, run.Runtime, run.WorkspaceID, run.Command, run.Status, run.ExitCode, run.TimedOut, run.DurationMS, run.Actor, run.OutputTail, run.Error)
	if err != nil {
		return
	}
	id, _ := result.LastInsertId()
	_, _ = db.Exec(`DELETE FROM repo_command_runs WHERE repo_id = ? AND id <= ?`, repo.ID, id-commandRunsKept)
	app.Emit("repo.command.finished", map[string]any{"id": repo.ID, "slug": repo.Slug, "run_id": id, "status": run.Status, "exit_code": run.ExitCode})
}

func dbCommandRuns(db *sql.DB, repoID int64, limit int, id int64) ([]CommandRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, runtime, workspace_id, command, status, exit_code, timed_out, duration_ms, actor, error, created_at, ` +
		`CASE WHEN ? THEN output_tail ELSE substr(output_tail, -2000) END FROM repo_command_runs WHERE repo_id = ?`
	args := []any{id > 0, repoID}
	if id > 0 {
		query += ` AND id = ?`
		args = append(args, id)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommandRun{}
	for rows.Next() {
		var run CommandRun
		var created sql.NullString
		if err := rows.Scan(&run.ID, &run.Runtime, &run.WorkspaceID, &run.Command, &run.Status, &run.ExitCode, &run.TimedOut, &run.DurationMS, &run.Actor, &run.Error, &created, &run.OutputTail); err != nil {
			return nil, err
		}
		run.CreatedAt = created.String
		out = append(out, run)
	}
	return out, rows.Err()
}

func dbGitOperations(db *sql.DB, repoID int64, limit int) ([]GitOperation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT id, operation, actor, from_sha, to_sha, status, error, created_at FROM repo_git_operations WHERE repo_id = ? ORDER BY id DESC LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitOperation{}
	for rows.Next() {
		var op GitOperation
		var created sql.NullString
		if err := rows.Scan(&op.ID, &op.Operation, &op.Actor, &op.FromSHA, &op.ToSHA, &op.Status, &op.Error, &created); err != nil {
			return nil, err
		}
		op.CreatedAt = created.String
		out = append(out, op)
	}
	return out, rows.Err()
}

// httpRepoRuns serves GET /repos/<slug>/runs (?limit=) and /runs/<id>.
func (a *App) httpRepoRuns(w http.ResponseWriter, r *http.Request, slug, rest string) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := requireRepoSlug(globalCtx, pid, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	if rest != "" {
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || id <= 0 {
			httpErr(w, http.StatusNotFound, "no such run")
			return
		}
		runs, err := dbCommandRuns(globalCtx.AppDB(), repo.ID, 1, id)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(runs) == 0 {
			httpErr(w, http.StatusNotFound, "no such run")
			return
		}
		httpJSON(w, runs[0])
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := dbCommandRuns(globalCtx.AppDB(), repo.ID, limit, 0)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"runs": runs, "count": len(runs)})
}
