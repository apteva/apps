package main

// Transcripts DB layer. Mirrors renderstore.go's shape — same
// state-machine + atomic-claim pattern. Lives in the same media.db
// as the catalog; no FK to media.file_id (sqlite indexer-style: rows
// can outlive their media row briefly during reindex).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TranscriptSegment is one timed chunk inside Segments. The fields
// follow the OpenAI/Whisper convention; Deepgram's response is
// normalised into this shape by the worker before persisting.
type TranscriptSegment struct {
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"` // empty when diarisation off
}

type TranscriptRow struct {
	FileID        string  `json:"file_id"`
	ProjectID     string  `json:"project_id"`
	SourceSHA256  string  `json:"source_sha256,omitempty"`
	Status        string  `json:"status"`
	Language      string  `json:"language,omitempty"`
	Text          string  `json:"text,omitempty"`
	Segments      json.RawMessage `json:"segments,omitempty"`
	Provider      string  `json:"provider,omitempty"`
	Model         string  `json:"model,omitempty"`
	DurationMs    int64   `json:"duration_ms,omitempty"`
	CostCents     float64 `json:"cost_cents,omitempty"`
	Error         string  `json:"error,omitempty"`
	SourceKind    string  `json:"source_kind,omitempty"`
	CreatedAt     string  `json:"created_at,omitempty"`
	StartedAt     string  `json:"started_at,omitempty"`
	CompletedAt   string  `json:"completed_at,omitempty"`
}

// insertPendingTranscript creates a transcripts row in pending state.
// Idempotent on file_id (UPSERT) so the worker can re-queue without
// stepping on running rows — the WHERE in claimNextPendingTranscript
// guards against double-claim.
func insertPendingTranscript(db *sql.DB, projectID, fileID, sourceKind string) error {
	if projectID == "" || fileID == "" {
		return errors.New("project_id and file_id required")
	}
	if sourceKind == "" {
		sourceKind = "auto"
	}
	_, err := db.Exec(`
		INSERT INTO transcripts (file_id, project_id, status, source_kind)
		VALUES (?, ?, 'pending', ?)
		ON CONFLICT(file_id) DO UPDATE SET
			status      = CASE WHEN transcripts.status IN ('failed','skipped') THEN 'pending' ELSE transcripts.status END,
			source_kind = excluded.source_kind
		WHERE transcripts.status IN ('failed','skipped')`,
		fileID, projectID, sourceKind,
	)
	return err
}

// upsertTranscript installs a complete row at status=ok. Used for
// imported transcripts (media_set_transcript) and as the final write
// after the worker finishes. Resets error to '' so retries clean up.
func upsertTranscript(db *sql.DB, t *TranscriptRow) error {
	if t.FileID == "" || t.ProjectID == "" {
		return errors.New("file_id and project_id required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	segs := string(t.Segments)
	if segs == "" {
		segs = "[]"
	}
	_, err := db.Exec(`
		INSERT INTO transcripts (
			file_id, project_id, source_sha256, status, language, text, segments,
			provider, model, duration_ms, cost_cents, raw, error, source_kind,
			completed_at
		) VALUES (?, ?, ?, 'ok', ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			project_id    = excluded.project_id,
			source_sha256 = excluded.source_sha256,
			status        = 'ok',
			language      = excluded.language,
			text          = excluded.text,
			segments      = excluded.segments,
			provider      = excluded.provider,
			model         = excluded.model,
			duration_ms   = excluded.duration_ms,
			cost_cents    = excluded.cost_cents,
			error         = '',
			source_kind   = excluded.source_kind,
			completed_at  = excluded.completed_at`,
		t.FileID, t.ProjectID, t.SourceSHA256, t.Language, t.Text, segs,
		t.Provider, t.Model, t.DurationMs, t.CostCents, t.SourceKind, now,
	)
	return err
}

// claimNextPendingTranscript atomically claims the oldest pending
// row and returns it as running. SQLite RETURNING (3.35+) gives us
// the claim without a tx.
func claimNextPendingTranscript(db *sql.DB) (*TranscriptRow, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	row := db.QueryRow(`
		UPDATE transcripts
		   SET status = 'running', started_at = ?
		 WHERE file_id = (
		   SELECT file_id FROM transcripts
		    WHERE status = 'pending'
		    ORDER BY created_at
		    LIMIT 1
		 )
		 RETURNING file_id, project_id, source_sha256, status, language, text, segments,
		           provider, model, COALESCE(duration_ms,0), COALESCE(cost_cents,0),
		           error, source_kind, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')`,
		now,
	)
	return scanTranscriptRow(row)
}

func transcriptMarkOk(db *sql.DB, t *TranscriptRow) error {
	now := time.Now().UTC().Format(time.RFC3339)
	segs := string(t.Segments)
	if segs == "" {
		segs = "[]"
	}
	_, err := db.Exec(`
		UPDATE transcripts
		   SET status        = 'ok',
		       source_sha256 = ?,
		       language      = ?,
		       text          = ?,
		       segments      = ?,
		       provider      = ?,
		       model         = ?,
		       duration_ms   = ?,
		       cost_cents    = ?,
		       raw           = ?,
		       error         = '',
		       completed_at  = ?
		 WHERE file_id = ? AND status = 'running'`,
		t.SourceSHA256, t.Language, t.Text, segs,
		t.Provider, t.Model, t.DurationMs, t.CostCents,
		"", now, t.FileID,
	)
	return err
}

func transcriptMarkFailed(db *sql.DB, fileID, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE transcripts
		   SET status = 'failed', error = ?, completed_at = ?
		 WHERE file_id = ? AND status IN ('pending','running')`,
		errMsg, now, fileID,
	)
	return err
}

// transcriptMarkSkipped pulls a row out of the auto-transcribe rotation
// without flagging it as a failure. Used when the file is too long,
// or an auto-transcribe gate fails (e.g. integration absent).
func transcriptMarkSkipped(db *sql.DB, fileID, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE transcripts
		   SET status = 'skipped', error = ?, completed_at = ?
		 WHERE file_id = ? AND status IN ('pending','running')`,
		reason, now, fileID,
	)
	return err
}

func getTranscript(db *sql.DB, projectID, fileID string) (*TranscriptRow, error) {
	row := db.QueryRow(`
		SELECT file_id, project_id, source_sha256, status, language, text, segments,
		       provider, model, COALESCE(duration_ms,0), COALESCE(cost_cents,0),
		       error, source_kind, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
		FROM transcripts WHERE project_id=? AND file_id=?`,
		projectID, fileID,
	)
	return scanTranscriptRow(row)
}

type TranscriptFilters struct {
	Status string
	Limit  int
}

func listTranscripts(db *sql.DB, projectID string, f TranscriptFilters) ([]TranscriptRow, error) {
	q := strings.Builder{}
	q.WriteString(`SELECT file_id, project_id, source_sha256, status, language, text, segments,
	                      provider, model, COALESCE(duration_ms,0), COALESCE(cost_cents,0),
	                      error, source_kind, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
	               FROM transcripts WHERE project_id = ?`)
	args := []any{projectID}
	if f.Status != "" {
		q.WriteString(" AND status = ?")
		args = append(args, f.Status)
	}
	q.WriteString(" ORDER BY created_at DESC")
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
	out := make([]TranscriptRow, 0, limit)
	for rows.Next() {
		t, err := scanTranscriptRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// transcribeCandidates returns media rows that the auto-transcriber
// should consider — files with audio that don't yet have a non-failed
// transcript (or whose source sha drifted, indicating a re-upload).
//
// Limit caps the per-tick batch so a fresh project with hundreds of
// files doesn't queue them all in a single sweep.
func transcribeCandidates(db *sql.DB, projectID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.Query(`
		SELECT m.file_id
		  FROM media m
		  LEFT JOIN transcripts t
		    ON t.file_id = m.file_id AND t.project_id = m.project_id
		 WHERE m.project_id = ?
		   AND m.probe_status = 'ok'
		   AND m.has_audio = 1
		   AND (
		     t.file_id IS NULL
		     OR (t.status IN ('ok') AND t.source_sha256 != m.source_sha256 AND t.source_sha256 != '')
		   )
		 ORDER BY m.created_at DESC
		 LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ─── scanners ───────────────────────────────────────────────────────

func scanTranscriptRow(row *sql.Row) (*TranscriptRow, error) {
	var t TranscriptRow
	var segs string
	err := row.Scan(
		&t.FileID, &t.ProjectID, &t.SourceSHA256, &t.Status, &t.Language, &t.Text, &segs,
		&t.Provider, &t.Model, &t.DurationMs, &t.CostCents,
		&t.Error, &t.SourceKind, &t.CreatedAt, &t.StartedAt, &t.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	if segs != "" && segs != "[]" {
		t.Segments = json.RawMessage(segs)
	}
	return &t, nil
}

func scanTranscriptRows(rows *sql.Rows) (*TranscriptRow, error) {
	var t TranscriptRow
	var segs string
	err := rows.Scan(
		&t.FileID, &t.ProjectID, &t.SourceSHA256, &t.Status, &t.Language, &t.Text, &segs,
		&t.Provider, &t.Model, &t.DurationMs, &t.CostCents,
		&t.Error, &t.SourceKind, &t.CreatedAt, &t.StartedAt, &t.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	if segs != "" && segs != "[]" {
		t.Segments = json.RawMessage(segs)
	}
	return &t, nil
}

// formatSegments serialises segments to JSON for storage. Wrapper for
// callers that have []TranscriptSegment in hand (e.g. the worker
// post-Deepgram-parse).
func formatSegments(segs []TranscriptSegment) (json.RawMessage, error) {
	if len(segs) == 0 {
		return json.RawMessage("[]"), nil
	}
	b, err := json.Marshal(segs)
	if err != nil {
		return nil, fmt.Errorf("marshal segments: %w", err)
	}
	return b, nil
}
