package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

type transcriptAttemptStatus struct {
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}

type transcriptQuerier interface{ QueryRow(string, ...any) *sql.Row }

func transcriptRevision(t *TranscriptRow) string {
	b, _ := json.Marshal([]any{t.ProjectID, t.FileID, t.SourceSHA256, t.Text, t.Segments, t.Diagnostics, t.CompletedAt})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func snapshotTranscript(tx *sql.Tx, t *TranscriptRow) error {
	if t == nil || t.Status != "ok" {
		return nil
	}
	copy := *t
	copy.LastAttempt = nil
	b, err := json.Marshal(&copy)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO transcript_versions(project_id,file_id,revision,document) VALUES(?,?,?,?) ON CONFLICT(project_id,file_id,revision) DO NOTHING`, t.ProjectID, t.FileID, transcriptRevision(t), string(b))
	return err
}

// Completed versions are never removed to make room for a transcription attempt.
// The candidate state remains independently inspectable as last_attempt.
func getTranscript(db *sql.DB, projectID, fileID string) (*TranscriptRow, error) {
	current, err := getTranscriptAttempt(db, projectID, fileID)
	if err != nil {
		return nil, err
	}
	if current.Status == "ok" {
		return current, nil
	}
	var raw string
	err = db.QueryRow(`SELECT document FROM transcript_versions WHERE project_id=? AND file_id=? ORDER BY id DESC LIMIT 1`, projectID, fileID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return current, nil
	}
	if err != nil {
		return nil, err
	}
	var active TranscriptRow
	if err = json.Unmarshal([]byte(raw), &active); err != nil {
		return nil, err
	}
	active.LastAttempt = &transcriptAttemptStatus{Status: current.Status, Error: current.Error, StartedAt: current.StartedAt, CompletedAt: current.CompletedAt}
	return &active, nil
}

func queueTranscript(db *sql.DB, projectID, fileID, kind string, force, requireWords bool) error {
	if projectID == "" || fileID == "" {
		return errors.New("project_id and file_id required")
	}
	if kind == "" {
		kind = "auto"
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := getTranscriptAttempt(tx, projectID, fileID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec(`INSERT INTO transcripts(file_id,project_id,status,source_kind,require_words) VALUES(?,?,'pending',?,?)`, fileID, projectID, kind, boolInt(requireWords))
	} else if err == nil {
		if current.Status == "running" || current.Status == "pending" {
			if requireWords {
				if _, err = tx.Exec(`UPDATE transcripts SET require_words=1 WHERE project_id=? AND file_id=?`, projectID, fileID); err != nil {
					return err
				}
			}
			return tx.Commit()
		}
		var source string
		sourceErr := tx.QueryRow(`SELECT source_sha256 FROM media WHERE project_id=? AND file_id=?`, projectID, fileID).Scan(&source)
		changed := sourceErr == nil && source != current.SourceSHA256
		if sourceErr != nil && !errors.Is(sourceErr, sql.ErrNoRows) {
			return sourceErr
		}
		if current.Status == "ok" && !force && !changed && (!requireWords || transcriptHasWords(current)) {
			return tx.Commit()
		}
		if err = snapshotTranscript(tx, current); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE transcripts SET status='pending',source_kind=?,source_sha256='',text='',language='',segments='[]',diagnostics='{}',error='',started_at=NULL,completed_at=NULL,require_words=MAX(require_words,?) WHERE project_id=? AND file_id=? AND status NOT IN ('pending','running')`, kind, boolInt(requireWords), projectID, fileID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func transcriptHasWords(t *TranscriptRow) bool {
	var segments []TranscriptSegment
	if json.Unmarshal(t.Segments, &segments) != nil {
		return false
	}
	for _, s := range segments {
		if len(s.Words) > 0 {
			if validateTranscriptWords(transcriptStoredWords(segments), t.DurationMs) != nil {
				return false
			}
			if t.Provider == "deepgram" {
				timing := transcriptTimingOf(t)
				return timing != nil && timing.Validated && timing.SourceSHA256 == t.SourceSHA256
			}
			return true
		}
	}
	return false
}

func transcriptRequiresWords(db *sql.DB, row *TranscriptRow) bool {
	var required int
	_ = db.QueryRow(`SELECT require_words FROM transcripts WHERE project_id=? AND file_id=?`, row.ProjectID, row.FileID).Scan(&required)
	return required != 0
}

func failedTranscriptCommit(db *sql.DB, row *TranscriptRow, err error) {
	_ = transcriptMarkFailed(db, row.ProjectID, row.FileID, fmt.Sprintf("commit transcript: %v", err), row.StartedAt)
}
