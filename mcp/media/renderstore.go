package main

// SQLite reads/writes for the renders table. The render queue lives
// in the same media.db as the catalog but never joins against it —
// renders reference storage.files.id directly, same as derivations.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RenderRow is the canonical shape exposed via tools + HTTP. Times
// stay as RFC3339 strings (sqlite TIMESTAMP DEFAULT CURRENT_TIMESTAMP
// stores ISO8601 already; we don't round-trip to time.Time so panels
// can render strings as-is).
type RenderRow struct {
	ID            int64    `json:"id"`
	ProjectID     string   `json:"project_id"`
	Operation     string   `json:"operation"`
	SourceFileIDs []string `json:"source_file_ids"`
	Params        json.RawMessage `json:"params"`
	Status        string   `json:"status"`
	ProgressPct   int      `json:"progress_pct"`
	OutputFileID  string   `json:"output_file_id,omitempty"`
	OutputName    string   `json:"output_name,omitempty"`
	// OutputFolder — where the result lands in storage. Set per-call
	// at submit time, falling back to the install's
	// render_output_folder config when empty.
	OutputFolder string `json:"output_folder,omitempty"`
	Error        string `json:"error,omitempty"`
	RequestedBy  string `json:"requested_by,omitempty"`
	CreatedAt    string `json:"created_at"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

// insertRender enqueues a new render and returns its id. Callers
// have already validated the operation + params; we just persist.
// outputFolder is optional — empty means "use the install's
// render_output_folder config at execution time".
func insertRender(db *sql.DB, projectID, operation string, sourceFileIDs []string, params map[string]any, outputName, outputFolder, requestedBy string) (int64, error) {
	if projectID == "" {
		return 0, errors.New("project_id required")
	}
	if operation == "" {
		return 0, errors.New("operation required")
	}
	if len(sourceFileIDs) == 0 {
		return 0, errors.New("at least one source file_id required")
	}
	srcJSON, err := json.Marshal(sourceFileIDs)
	if err != nil {
		return 0, fmt.Errorf("marshal source_file_ids: %w", err)
	}
	if params == nil {
		params = map[string]any{}
	}
	paramJSON, err := json.Marshal(params)
	if err != nil {
		return 0, fmt.Errorf("marshal params: %w", err)
	}
	res, err := db.Exec(`
		INSERT INTO renders (project_id, operation, source_file_ids, params, output_name, output_folder, requested_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, operation, string(srcJSON), string(paramJSON), outputName, outputFolder, requestedBy,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// claimNextPending atomically picks the oldest pending render and
// flips it to running. Returns sql.ErrNoRows when the queue is
// empty so the worker loop can sleep instead of busy-looping.
//
// SQLite's UPDATE … RETURNING (3.35+) gives us the atomic claim
// without a separate transaction. The subselect ensures we touch
// exactly one row even if multiple workers race.
func claimNextPending(db *sql.DB) (*RenderRow, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	row := db.QueryRow(`
		UPDATE renders
		   SET status = 'running', started_at = ?
		 WHERE id = (
		   SELECT id FROM renders
		    WHERE status = 'pending'
		    ORDER BY created_at
		    LIMIT 1
		 )
		 RETURNING id, project_id, operation, source_file_ids, params,
		           status, progress_pct, COALESCE(output_file_id,''),
		           COALESCE(output_name,''), COALESCE(output_folder,''), error, COALESCE(requested_by,''),
		           created_at, COALESCE(started_at,''), COALESCE(completed_at,'')`,
		now,
	)
	return scanRender(row)
}

// updateProgress writes the latest progress_pct without touching
// status. Cheap (no row movement) so the worker can call it on
// every ffmpeg progress chunk without coordination.
func renderUpdateProgress(db *sql.DB, id int64, pct int) error {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	_, err := db.Exec(`UPDATE renders SET progress_pct = ? WHERE id = ? AND status = 'running'`, pct, id)
	return err
}

// markOk flips a running render to ok with its final output and
// 100% progress. Idempotent on completed_at — subsequent calls are
// rejected by the WHERE clause.
func renderMarkOk(db *sql.DB, id int64, outputFileID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE renders
		   SET status = 'ok', progress_pct = 100,
		       output_file_id = ?, completed_at = ?
		 WHERE id = ? AND status = 'running'`,
		outputFileID, now, id,
	)
	return err
}

// markFailed records the error string and stamps completed_at.
// Status check guards against double-completion (e.g. cancellation
// racing against completion).
func renderMarkFailed(db *sql.DB, id int64, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE renders
		   SET status = 'failed', error = ?, completed_at = ?
		 WHERE id = ? AND status IN ('pending','running')`,
		errMsg, now, id,
	)
	return err
}

// markCancelled is the user-facing cancel path. Pending rows just
// flip to cancelled; running rows are flipped here and the worker
// notices via the in-memory cancelFunc map (renderpool.go).
func renderMarkCancelled(db *sql.DB, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE renders
		   SET status = 'cancelled', completed_at = ?
		 WHERE id = ? AND status IN ('pending','running')`,
		now, id,
	)
	return err
}

func getRender(db *sql.DB, projectID string, id int64) (*RenderRow, error) {
	q := `SELECT id, project_id, operation, source_file_ids, params,
	             status, progress_pct, COALESCE(output_file_id,''),
	             COALESCE(output_name,''), COALESCE(output_folder,''), error, COALESCE(requested_by,''),
	             created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
	      FROM renders WHERE id = ? AND project_id = ?`
	return scanRender(db.QueryRow(q, id, projectID))
}

// RenderFilters mirrors the search args exposed by media_list_renders.
type RenderFilters struct {
	Status    string
	Operation string
	Limit     int
}

// RenderQueueSummary is what the panel asks for on initial load (and
// occasionally as a re-sync against the event stream). One round-trip,
// everything the queue header + lists need.
type RenderQueueSummary struct {
	Counts  RenderCounts `json:"counts"`
	Running []RenderRow  `json:"running"`           // current running rows, oldest first (FIFO order)
	Pending []RenderRow  `json:"pending"`           // oldest pending rows (FIFO), up to 20
	Recent  []RenderRow  `json:"recent"`            // most recent terminal rows (ok/failed/cancelled), up to 10
}

// RenderCounts is a snapshot of pipeline state. ok_24h + failed_24h
// give the panel a "how's the queue been recently" feel without
// dumping the whole history; pending + running are point-in-time.
type RenderCounts struct {
	Pending    int `json:"pending"`
	Running    int `json:"running"`
	Ok24h      int `json:"ok_24h"`
	Failed24h  int `json:"failed_24h"`
	Cancelled24h int `json:"cancelled_24h"`
}

// queueSummary computes the panel's render-queue snapshot. Four
// queries — small for typical project sizes; the LIMIT N on the
// list queries caps memory regardless of queue depth.
func queueSummary(db *sql.DB, projectID string) (*RenderQueueSummary, error) {
	if projectID == "" {
		return nil, errors.New("project_id required")
	}
	out := &RenderQueueSummary{}

	// Counts. One scan per status — sqlite COUNT(*) on an indexed
	// (project_id, status) is fast even at queue depths in the
	// thousands. 24h windows use SQLite's datetime("-24 hours") which
	// is consistent across all platforms (no TZ surprises).
	// COALESCE wraps each SUM because SQLite returns NULL (not 0)
	// when no rows match the WHERE — Scan into int then fails. The
	// 0-default makes "empty project" indistinguishable from
	// "project with explicit 0 counts," which is correct.
	countQ := `
		SELECT
			COALESCE(SUM(CASE WHEN status='pending'   THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='running'   THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='ok'        AND completed_at >= datetime('now','-24 hours') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='failed'    AND completed_at >= datetime('now','-24 hours') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='cancelled' AND completed_at >= datetime('now','-24 hours') THEN 1 ELSE 0 END), 0)
		  FROM renders
		 WHERE project_id = ?`
	if err := db.QueryRow(countQ, projectID).Scan(
		&out.Counts.Pending, &out.Counts.Running,
		&out.Counts.Ok24h, &out.Counts.Failed24h, &out.Counts.Cancelled24h,
	); err != nil {
		return nil, fmt.Errorf("counts: %w", err)
	}

	// Running rows — usually small (== pool size, default 2). FIFO
	// by started_at so the panel can show "this one's been running
	// for 3 min" intuitively.
	runningRows, err := db.Query(`
		SELECT id, project_id, operation, source_file_ids, params,
		       status, progress_pct, COALESCE(output_file_id,''),
		       COALESCE(output_name,''), COALESCE(output_folder,''), error, COALESCE(requested_by,''),
		       created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
		  FROM renders
		 WHERE project_id = ? AND status = 'running'
		 ORDER BY started_at ASC, id ASC
		 LIMIT 32`, projectID)
	if err != nil {
		return nil, fmt.Errorf("running: %w", err)
	}
	defer runningRows.Close()
	for runningRows.Next() {
		r, err := scanRenderFromRows(runningRows)
		if err != nil {
			return nil, err
		}
		out.Running = append(out.Running, *r)
	}

	// Pending rows — oldest first (the order the worker will pick
	// them up). Cap at 20 so a 10k-deep queue doesn't bloat the
	// response; the counts.pending tells the panel there's more.
	pendingRows, err := db.Query(`
		SELECT id, project_id, operation, source_file_ids, params,
		       status, progress_pct, COALESCE(output_file_id,''),
		       COALESCE(output_name,''), COALESCE(output_folder,''), error, COALESCE(requested_by,''),
		       created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
		  FROM renders
		 WHERE project_id = ? AND status = 'pending'
		 ORDER BY created_at ASC, id ASC
		 LIMIT 20`, projectID)
	if err != nil {
		return nil, fmt.Errorf("pending: %w", err)
	}
	defer pendingRows.Close()
	for pendingRows.Next() {
		r, err := scanRenderFromRows(pendingRows)
		if err != nil {
			return nil, err
		}
		out.Pending = append(out.Pending, *r)
	}

	// Recent terminal rows — newest first. Capped at 10 since these
	// fall off the panel quickly anyway (drift past 24h or get pruned
	// by the recent-counts window).
	recentRows, err := db.Query(`
		SELECT id, project_id, operation, source_file_ids, params,
		       status, progress_pct, COALESCE(output_file_id,''),
		       COALESCE(output_name,''), COALESCE(output_folder,''), error, COALESCE(requested_by,''),
		       created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
		  FROM renders
		 WHERE project_id = ? AND status IN ('ok','failed','cancelled')
		 ORDER BY completed_at DESC, id DESC
		 LIMIT 10`, projectID)
	if err != nil {
		return nil, fmt.Errorf("recent: %w", err)
	}
	defer recentRows.Close()
	for recentRows.Next() {
		r, err := scanRenderFromRows(recentRows)
		if err != nil {
			return nil, err
		}
		out.Recent = append(out.Recent, *r)
	}
	return out, nil
}

func listRenders(db *sql.DB, projectID string, f RenderFilters) ([]RenderRow, error) {
	q := strings.Builder{}
	q.WriteString(`SELECT id, project_id, operation, source_file_ids, params,
	                      status, progress_pct, COALESCE(output_file_id,''),
	                      COALESCE(output_name,''), COALESCE(output_folder,''), error, COALESCE(requested_by,''),
	                      created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
	               FROM renders WHERE project_id = ?`)
	args := []any{projectID}
	if f.Status != "" {
		q.WriteString(" AND status = ?")
		args = append(args, f.Status)
	}
	if f.Operation != "" {
		q.WriteString(" AND operation = ?")
		args = append(args, f.Operation)
	}
	// id is monotonic in sqlite so it's a stable tiebreaker when
	// multiple rows land in the same second (tests + bursts).
	q.WriteString(" ORDER BY created_at DESC, id DESC")
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q.WriteString(" LIMIT ?")
	args = append(args, limit)

	rows, err := db.Query(q.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RenderRow, 0, limit)
	for rows.Next() {
		r, err := scanRenderFromRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// scanRender works with both *sql.Row and the shape returned by
// QueryRow. We have a separate helper for *sql.Rows below since the
// two types don't share an interface that exposes Scan in a way the
// type-checker is happy with.
func scanRender(row *sql.Row) (*RenderRow, error) {
	var r RenderRow
	var srcRaw, paramsRaw string
	err := row.Scan(
		&r.ID, &r.ProjectID, &r.Operation, &srcRaw, &paramsRaw,
		&r.Status, &r.ProgressPct, &r.OutputFileID,
		&r.OutputName, &r.OutputFolder, &r.Error, &r.RequestedBy,
		&r.CreatedAt, &r.StartedAt, &r.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(srcRaw), &r.SourceFileIDs); err != nil {
		return nil, fmt.Errorf("decode source_file_ids: %w", err)
	}
	r.Params = json.RawMessage(paramsRaw)
	return &r, nil
}

func scanRenderFromRows(rows *sql.Rows) (*RenderRow, error) {
	var r RenderRow
	var srcRaw, paramsRaw string
	err := rows.Scan(
		&r.ID, &r.ProjectID, &r.Operation, &srcRaw, &paramsRaw,
		&r.Status, &r.ProgressPct, &r.OutputFileID,
		&r.OutputName, &r.OutputFolder, &r.Error, &r.RequestedBy,
		&r.CreatedAt, &r.StartedAt, &r.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(srcRaw), &r.SourceFileIDs); err != nil {
		return nil, fmt.Errorf("decode source_file_ids: %w", err)
	}
	r.Params = json.RawMessage(paramsRaw)
	return &r, nil
}
