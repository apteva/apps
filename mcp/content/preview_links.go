package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

func previewTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func recordPreviewLink(db *sql.DB, projectID string, siteID int64, token string, expires time.Time) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO content_preview_links(project_id,site_id,token_hash,expires_at) VALUES(?,?,?,?)`, projectID, siteID, previewTokenHash(token), expires.Unix())
	return err
}

func previewLinkActive(db *sql.DB, projectID string, siteID int64, token string) bool {
	var expires int64
	var revoked sql.NullInt64
	err := db.QueryRow(`SELECT expires_at,revoked_at FROM content_preview_links WHERE project_id=? AND site_id=? AND token_hash=?`, projectID, siteID, previewTokenHash(token)).Scan(&expires, &revoked)
	if err == sql.ErrNoRows {
		return true
	} // signed links created before registry migration remain valid
	return err == nil && !revoked.Valid && time.Now().Unix() <= expires
}

func revokePreviewLink(db *sql.DB, projectID string, token string) error {
	_, err := db.Exec(`UPDATE content_preview_links SET revoked_at=? WHERE project_id=? AND token_hash=?`, time.Now().Unix(), projectID, previewTokenHash(token))
	return err
}
