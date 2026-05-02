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
	Error         string   `json:"error,omitempty"`
	RequestedBy   string   `json:"requested_by,omitempty"`
	CreatedAt     string   `json:"created_at"`
	StartedAt     string   `json:"started_at,omitempty"`
	CompletedAt   string   `json:"completed_at,omitempty"`
}

// insertRender enqueues a new render and returns its id. Callers
// have already validated the operation + params; we just persist.
func insertRender(db *sql.DB, projectID, operation string, sourceFileIDs []string, params map[string]any, outputName, requestedBy string) (int64, error) {
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
		INSERT INTO renders (project_id, operation, source_file_ids, params, output_name, requested_by)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, operation, string(srcJSON), string(paramJSON), outputName, requestedBy,
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
		           COALESCE(output_name,''), error, COALESCE(requested_by,''),
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
	             COALESCE(output_name,''), error, COALESCE(requested_by,''),
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

func listRenders(db *sql.DB, projectID string, f RenderFilters) ([]RenderRow, error) {
	q := strings.Builder{}
	q.WriteString(`SELECT id, project_id, operation, source_file_ids, params,
	                      status, progress_pct, COALESCE(output_file_id,''),
	                      COALESCE(output_name,''), error, COALESCE(requested_by,''),
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
		&r.OutputName, &r.Error, &r.RequestedBy,
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
		&r.OutputName, &r.Error, &r.RequestedBy,
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
