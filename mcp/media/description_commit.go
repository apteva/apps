package main

import (
	"database/sql"
	"time"
)

// Model work owns neither the row nor manual prose. Commit only against the
// source and field revisions it read; deletion, reindexing or editing wins.
func commitAutoDescription(db *sql.DB, media *MediaRow, proseRevision, audienceRevision int64, parsed parsedDescribe, writeProse bool) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var changed int64
	if writeProse && parsed.Description != "" {
		res, err := tx.Exec(`UPDATE media SET description=?,description_source='ai-generated',description_updated_at=?,description_error='',description_attempted_at=?,describe_requested=0 WHERE project_id=? AND file_id=? AND source_sha256=? AND prose_revision=? AND description='' AND description_source NOT IN ('human','agent')`, parsed.Description, now, now, media.ProjectID, media.FileID, media.SourceSHA256, proseRevision)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		changed += n
	}
	if parsed.AudienceRating != "" {
		res, err := tx.Exec(`UPDATE media SET audience_rating=?,audience_reasoning=?,audience_updated_at=?,description_attempted_at=?,describe_requested=0 WHERE project_id=? AND file_id=? AND source_sha256=? AND audience_revision=? AND audience_rating IN ('','unrated')`, parsed.AudienceRating, parsed.AudienceReasoning, now, now, media.ProjectID, media.FileID, media.SourceSHA256, audienceRevision)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		changed += n
	}
	return changed > 0, tx.Commit()
}
