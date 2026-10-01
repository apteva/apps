package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// reconcileProjectSuppressionState stages one Messaging snapshot and applies it
// to a project in two set-based updates. Unchanged routes execute no UPDATE at
// all. The snapshot timestamp prevents this older full-list read from
// overwriting a newer event or outbound preflight check.
func reconcileProjectSuppressionState(db *sql.DB, pid string, items []messagingSuppression, snapshotStarted string) (routes int64, changed int64, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	if _, err = tx.Exec(`CREATE TEMP TABLE crm_suppression_snapshot (
		transport TEXT NOT NULL,
		kind TEXT NOT NULL,
		match_key TEXT NOT NULL,
		address TEXT NOT NULL,
		reason TEXT NOT NULL,
		source TEXT NOT NULL,
		first_seen TEXT NOT NULL,
		delivery_status TEXT NOT NULL,
		PRIMARY KEY (transport, kind, match_key)
	) WITHOUT ROWID`); err != nil {
		return 0, 0, fmt.Errorf("create suppression snapshot: %w", err)
	}
	insert, err := tx.Prepare(`INSERT OR IGNORE INTO crm_suppression_snapshot
		(transport,kind,match_key,address,reason,source,first_seen,delivery_status)
		VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, 0, err
	}
	for _, item := range items {
		key := canonicalDeliveryRecipient(item.Channel, item.Address)
		if item.Kind == "domain" {
			key = strings.ToLower(strings.TrimPrefix(item.Address, "@"))
		}
		if key == "" || (item.Kind != "address" && item.Kind != "domain") {
			continue
		}
		if _, err = insert.Exec(item.Channel, item.Kind, key, item.Address, item.Reason, item.Source, item.FirstSeen, statusForSuppression(item.Reason)); err != nil {
			insert.Close()
			return 0, 0, fmt.Errorf("stage suppression: %w", err)
		}
	}
	if err = insert.Close(); err != nil {
		return 0, 0, err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM contact_channel_delivery_state s
		JOIN contact_channels c ON c.project_id=s.project_id AND c.id=s.channel_id
		WHERE s.project_id=? AND c.kind IN ('email','phone')`, pid).Scan(&routes); err != nil {
		return 0, 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	matched, err := tx.Exec(`WITH desired AS (
		SELECT s.project_id, s.channel_id, s.transport,
			COALESCE(exact.kind, domain.kind) AS kind,
			COALESCE(exact.address, domain.address) AS address,
			COALESCE(exact.reason, domain.reason) AS reason,
			COALESCE(exact.source, domain.source) AS source,
			COALESCE(exact.first_seen, domain.first_seen) AS first_seen,
			COALESCE(exact.delivery_status, domain.delivery_status) AS delivery_status
		FROM contact_channel_delivery_state s
		JOIN contact_channels c ON c.project_id=s.project_id AND c.id=s.channel_id
		LEFT JOIN crm_suppression_snapshot exact
			ON exact.transport=s.transport AND exact.kind='address'
			AND exact.match_key=CASE WHEN s.transport='email' THEN LOWER(c.value) ELSE c.value END
		LEFT JOIN crm_suppression_snapshot domain
			ON domain.transport=s.transport AND domain.kind='domain'
			AND INSTR(c.value,'@')>0
			AND domain.match_key=LOWER(SUBSTR(c.value,INSTR(c.value,'@')+1))
		WHERE s.project_id=? AND c.kind IN ('email','phone')
	)
	UPDATE contact_channel_delivery_state AS target
	SET suppressed=1, suppression_kind=desired.kind,
		suppression_match=desired.address, suppression_reason=desired.reason,
		suppression_source=desired.source, suppressed_at=NULLIF(desired.first_seen,''),
		suppression_checked_at=?,
		status=CASE WHEN target.delivery_evidence IS NOT NULL THEN target.delivery_evidence
			WHEN desired.delivery_status<>'' THEN desired.delivery_status ELSE target.status END,
		status_reason=CASE WHEN target.delivery_evidence IS NOT NULL THEN target.status_reason
			WHEN desired.delivery_status<>'' THEN desired.reason ELSE target.status_reason END,
		status_updated_at=CASE WHEN desired.delivery_status<>'' THEN ? ELSE target.status_updated_at END,
		updated_at=CURRENT_TIMESTAMP
	FROM desired
	WHERE target.project_id=desired.project_id AND target.channel_id=desired.channel_id
		AND target.transport=desired.transport AND desired.address IS NOT NULL
		AND (target.suppression_checked_at IS NULL OR julianday(target.suppression_checked_at)<=julianday(?))
		AND (target.suppression_checked_at IS NULL OR target.suppressed<>1
			OR COALESCE(target.suppression_kind,'')<>desired.kind
			OR COALESCE(target.suppression_match,'')<>desired.address
			OR COALESCE(target.suppression_reason,'')<>desired.reason
			OR COALESCE(target.suppression_source,'')<>desired.source)`, pid, now, now, snapshotStarted)
	if err != nil {
		return 0, 0, fmt.Errorf("apply matched suppressions: %w", err)
	}
	n, err := matched.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	changed += n
	cleared, err := tx.Exec(`UPDATE contact_channel_delivery_state AS target
	SET suppressed=0, suppression_kind=NULL, suppression_match=NULL,
		suppression_reason=NULL, suppression_source=NULL, suppressed_at=NULL,
		suppression_checked_at=?,
		status=CASE WHEN target.delivery_evidence IS NOT NULL THEN target.delivery_evidence
			WHEN target.status IN ('hard_bounced','complained','unsubscribed') THEN 'active' ELSE target.status END,
		status_reason=CASE WHEN target.delivery_evidence IS NOT NULL THEN target.status_reason
			WHEN target.status IN ('hard_bounced','complained','unsubscribed') THEN NULL ELSE target.status_reason END,
		updated_at=CURRENT_TIMESTAMP
	FROM contact_channels c
	WHERE target.project_id=? AND c.project_id=target.project_id AND c.id=target.channel_id
		AND c.kind IN ('email','phone')
		AND NOT EXISTS (SELECT 1 FROM crm_suppression_snapshot exact
			WHERE exact.transport=target.transport AND exact.kind='address'
			AND exact.match_key=CASE WHEN target.transport='email' THEN LOWER(c.value) ELSE c.value END)
		AND NOT EXISTS (SELECT 1 FROM crm_suppression_snapshot domain
			WHERE domain.transport=target.transport AND domain.kind='domain'
			AND INSTR(c.value,'@')>0
			AND domain.match_key=LOWER(SUBSTR(c.value,INSTR(c.value,'@')+1)))
		AND (target.suppression_checked_at IS NULL OR julianday(target.suppression_checked_at)<=julianday(?))
		AND (target.suppressed<>0 OR target.suppression_checked_at IS NULL)`, now, pid, snapshotStarted)
	if err != nil {
		return 0, 0, fmt.Errorf("clear removed suppressions: %w", err)
	}
	n, err = cleared.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	changed += n
	if _, err = tx.Exec(`DROP TABLE crm_suppression_snapshot`); err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return routes, changed, nil
}
