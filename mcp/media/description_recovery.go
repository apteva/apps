package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const maxDescriptionAttempts = 3

type descriptionRecoveryState struct {
	State         string      `json:"state"`
	Attempts      int         `json:"attempts"`
	MaxAttempts   int         `json:"max_attempts"`
	NextAttemptAt string      `json:"next_attempt_at,omitempty"`
	Failure       *askAttempt `json:"last_failure,omitempty"`
}

func claimDescriptionRecovery(db *sql.DB, m *MediaRow, prose, audience int64, now time.Time, timeout time.Duration) (bool, error) {
	tx, e := db.Begin()
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	_, e = tx.Exec(`INSERT INTO description_recovery(project_id,file_id,source_sha256,prose_revision,audience_revision) VALUES(?,?,?,?,?) ON CONFLICT(project_id,file_id) DO UPDATE SET source_sha256=excluded.source_sha256,prose_revision=excluded.prose_revision,audience_revision=excluded.audience_revision,attempts=0,state='pending',next_attempt_at='',last_error='{}' WHERE description_recovery.source_sha256<>excluded.source_sha256 OR description_recovery.prose_revision<>excluded.prose_revision OR description_recovery.audience_revision<>excluded.audience_revision`, m.ProjectID, m.FileID, m.SourceSHA256, prose, audience)
	if e != nil {
		return false, e
	}
	res, e := tx.Exec(`UPDATE description_recovery SET attempts=attempts+1,state='running',next_attempt_at=? WHERE project_id=? AND file_id=? AND attempts<? AND state IN ('pending','retry_wait','running') AND next_attempt_at<=?`, now.Add(timeout+time.Minute).UTC().Format(time.RFC3339), m.ProjectID, m.FileID, maxDescriptionAttempts, now.UTC().Format(time.RFC3339))
	if e != nil {
		return false, e
	}
	n, _ := res.RowsAffected()
	return n > 0, tx.Commit()
}

func finishDescriptionRecovery(db *sql.DB, m *MediaRow, prose, audience int64, failure *askAttempt, upstream time.Time, now time.Time) error {
	var attempts int
	if e := db.QueryRow(`SELECT attempts FROM description_recovery WHERE project_id=? AND file_id=? AND source_sha256=? AND prose_revision=? AND audience_revision=?`, m.ProjectID, m.FileID, m.SourceSHA256, prose, audience).Scan(&attempts); e != nil {
		return e
	}
	state, next := "ready", ""
	if failure != nil {
		state = "failed"
		if failure.Retryable {
			state = "exhausted"
			if attempts < maxDescriptionAttempts {
				state = "retry_wait"
				delay := time.Minute * time.Duration(1<<uint(attempts-1))
				when := now.Add(delay)
				if upstream.After(when) {
					when = upstream
				}
				next = when.UTC().Add(time.Second - time.Nanosecond).Truncate(time.Second).Format(time.RFC3339)
			}
		}
		failure.Attempt = attempts
	}
	raw, _ := json.Marshal(failure)
	_, e := db.Exec(`UPDATE description_recovery SET state=?,next_attempt_at=?,last_error=? WHERE project_id=? AND file_id=? AND source_sha256=? AND prose_revision=? AND audience_revision=?`, state, next, string(raw), m.ProjectID, m.FileID, m.SourceSHA256, prose, audience)
	return e
}

func getDescriptionRecovery(db *sql.DB, m *MediaRow) *descriptionRecoveryState {
	r := &descriptionRecoveryState{MaxAttempts: maxDescriptionAttempts}
	var raw string
	e := db.QueryRow(`SELECT state,attempts,next_attempt_at,last_error FROM description_recovery r WHERE project_id=? AND file_id=? AND source_sha256=? AND prose_revision=(SELECT prose_revision FROM media WHERE project_id=r.project_id AND file_id=r.file_id) AND audience_revision=(SELECT audience_revision FROM media WHERE project_id=r.project_id AND file_id=r.file_id)`, m.ProjectID, m.FileID, m.SourceSHA256).Scan(&r.State, &r.Attempts, &r.NextAttemptAt, &raw)
	if e == nil {
		_ = json.Unmarshal([]byte(raw), &r.Failure)
		if r.State == "running" && r.NextAttemptAt < time.Now().UTC().Format(time.RFC3339) && r.Attempts >= maxDescriptionAttempts {
			r.State = "exhausted"
		}
		return r
	}
	if m.Description != "" && m.AudienceRating != "" && m.AudienceRating != "unrated" {
		r.State = "ready"
	} else if m.HasAudio && m.TranscriptStatus != "ok" {
		r.State = "waiting_for_transcript"
	} else {
		r.State = "pending"
	}
	return r
}

func resetDescriptionRecovery(db *sql.DB, project, fid string) error {
	_, e := db.Exec(`DELETE FROM description_recovery WHERE project_id=? AND file_id=?`, project, fid)
	return e
}

func descriptionRecoveryFailure(err error) *askAttempt {
	e, _ := classifyAskFailure(nil, err)
	if e.Code == "upstream_error" {
		e.Code = "invalid_provider_response"
		e.Retryable = true
	}
	return &e
}
func descriptionRecoveryMissing() *askAttempt {
	return descriptionRecoveryFailure(fmt.Errorf("description/rating response was incomplete"))
}
