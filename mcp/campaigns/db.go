// SQL layer for campaigns.
//
// Helpers are project-scoped where possible — the install supports
// both `scope: project` (env var enforces) and `scope: global` (every
// query passes pid explicitly). Cross-project bleed would be a
// security violation; keeping pid in every signature is the cheapest
// guard.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ─── Campaigns ────────────────────────────────────────────────────

func dbCampaignCreate(db *sql.DB, pid string, c *Campaign) (*Campaign, error) {
	if c.Name == "" {
		return nil, errors.New("name required")
	}
	if !validChannel(c.Channel) {
		return nil, fmt.Errorf("channel must be one of email, sms, whatsapp; got %q", c.Channel)
	}
	if c.Status == "" {
		c.Status = StatusDraft
	}
	if c.ScheduleKind == "" {
		c.ScheduleKind = "immediate"
	}
	var listIDArg, segmentIDArg, scheduledAtArg, batchArg, tickArg any
	if c.ListID != nil && *c.ListID != 0 {
		listIDArg = *c.ListID
	}
	if c.SegmentID != nil && *c.SegmentID != 0 {
		segmentIDArg = *c.SegmentID
	}
	if c.ScheduledAt != "" {
		scheduledAtArg = c.ScheduledAt
	}
	if c.BatchSize != 0 {
		batchArg = c.BatchSize
	}
	if c.TickIntervalSeconds != 0 {
		tickArg = c.TickIntervalSeconds
	}
	res, err := db.Exec(
		`INSERT INTO campaigns
			(project_id, name, description, status, channel, sender_address,
			 subject, body_text, body_html, template_name,
			 list_id, segment_id, schedule_kind, scheduled_at,
			 batch_size, tick_interval_seconds,
			 open_tracking, click_tracking,
			 created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		pid, c.Name, nullStr(c.Description), c.Status, c.Channel, nullStr(c.SenderAddress),
		nullStr(c.Subject), nullStr(c.BodyText), nullStr(c.BodyHTML), nullStr(c.TemplateName),
		listIDArg, segmentIDArg, c.ScheduleKind, scheduledAtArg,
		batchArg, tickArg,
		boolToInt(c.OpenTracking), boolToInt(c.ClickTracking),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbCampaignGet(db, pid, id)
}

func dbCampaignGet(db *sql.DB, pid string, id int64) (*Campaign, error) {
	row := db.QueryRow(
		`SELECT id, name, COALESCE(description,''), status, channel,
				COALESCE(sender_address,''), COALESCE(subject,''),
				COALESCE(body_text,''), COALESCE(body_html,''),
				COALESCE(template_name,''),
				list_id, segment_id, schedule_kind, COALESCE(scheduled_at,''),
				COALESCE(batch_size, 0), COALESCE(tick_interval_seconds, 0),
				open_tracking, click_tracking,
				COALESCE(job_ids,''),
				created_at, updated_at,
				COALESCE(started_at,''), COALESCE(completed_at,''),
				COALESCE(archived_at,''), COALESCE(error,'')
		 FROM campaigns WHERE project_id = ? AND id = ?`,
		pid, id,
	)
	c := &Campaign{}
	var listID, segmentID sql.NullInt64
	var openT, clickT int
	if err := row.Scan(&c.ID, &c.Name, &c.Description, &c.Status, &c.Channel,
		&c.SenderAddress, &c.Subject, &c.BodyText, &c.BodyHTML, &c.TemplateName,
		&listID, &segmentID, &c.ScheduleKind, &c.ScheduledAt,
		&c.BatchSize, &c.TickIntervalSeconds,
		&openT, &clickT, &c.JobIDs,
		&c.CreatedAt, &c.UpdatedAt, &c.StartedAt, &c.CompletedAt, &c.ArchivedAt, &c.Error); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if listID.Valid {
		v := listID.Int64
		c.ListID = &v
	}
	if segmentID.Valid {
		v := segmentID.Int64
		c.SegmentID = &v
	}
	c.OpenTracking = openT != 0
	c.ClickTracking = clickT != 0
	return c, nil
}

func dbCampaignsList(db *sql.DB, pid, status, channel string, includeArchived bool) ([]*Campaign, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if !includeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if channel != "" {
		where = append(where, "channel = ?")
		args = append(args, channel)
	}
	rows, err := db.Query(
		`SELECT id, name, COALESCE(description,''), status, channel,
				COALESCE(sender_address,''), COALESCE(subject,''),
				COALESCE(body_text,''), COALESCE(body_html,''),
				COALESCE(template_name,''),
				list_id, segment_id, schedule_kind, COALESCE(scheduled_at,''),
				COALESCE(batch_size, 0), COALESCE(tick_interval_seconds, 0),
				open_tracking, click_tracking,
				COALESCE(job_ids,''),
				created_at, updated_at,
				COALESCE(started_at,''), COALESCE(completed_at,''),
				COALESCE(archived_at,''), COALESCE(error,'')
		 FROM campaigns WHERE `+strings.Join(where, " AND ")+
			` ORDER BY COALESCE(scheduled_at, updated_at) DESC, id DESC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Campaign{}
	for rows.Next() {
		c := &Campaign{}
		var listID, segmentID sql.NullInt64
		var openT, clickT int
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Status, &c.Channel,
			&c.SenderAddress, &c.Subject, &c.BodyText, &c.BodyHTML, &c.TemplateName,
			&listID, &segmentID, &c.ScheduleKind, &c.ScheduledAt,
			&c.BatchSize, &c.TickIntervalSeconds,
			&openT, &clickT, &c.JobIDs,
			&c.CreatedAt, &c.UpdatedAt, &c.StartedAt, &c.CompletedAt, &c.ArchivedAt, &c.Error); err != nil {
			continue
		}
		if listID.Valid {
			v := listID.Int64
			c.ListID = &v
		}
		if segmentID.Valid {
			v := segmentID.Int64
			c.SegmentID = &v
		}
		c.OpenTracking = openT != 0
		c.ClickTracking = clickT != 0
		out = append(out, c)
	}
	return out, nil
}

func dbCampaignUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Campaign, error) {
	allowed := map[string]bool{
		"name": true, "description": true,
		"channel": true, "sender_address": true,
		"subject": true, "body_text": true, "body_html": true, "template_name": true,
		"list_id": true, "segment_id": true,
		"scheduled_at":          true,
		"batch_size":            true,
		"tick_interval_seconds": true,
		"open_tracking":         true,
		"click_tracking":        true,
	}
	sets := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		switch k {
		case "open_tracking", "click_tracking":
			b, _ := v.(bool)
			sets = append(sets, k+" = ?")
			args = append(args, boolToInt(b))
		case "list_id", "segment_id":
			sets = append(sets, k+" = ?")
			if v == nil {
				args = append(args, nil)
			} else {
				switch x := v.(type) {
				case float64:
					if x == 0 {
						args = append(args, nil)
					} else {
						args = append(args, int64(x))
					}
				default:
					args = append(args, v)
				}
			}
		default:
			sets = append(sets, k+" = ?")
			if s, ok := v.(string); ok && s == "" {
				args = append(args, nil)
			} else {
				args = append(args, v)
			}
		}
	}
	if len(sets) == 0 {
		return dbCampaignGet(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, pid, id)
	if _, err := db.Exec(
		`UPDATE campaigns SET `+strings.Join(sets, ", ")+
			` WHERE project_id = ? AND id = ?`,
		args...,
	); err != nil {
		return nil, err
	}
	return dbCampaignGet(db, pid, id)
}

// dbCampaignSetStatus is the canonical state-machine writer. Returns
// the post-transition row. Caller is responsible for legality of the
// transition; this function just persists.
func dbCampaignSetStatus(db *sql.DB, pid string, id int64, status, errMsg string, started, completed bool) (*Campaign, error) {
	sets := []string{"status = ?", "updated_at = CURRENT_TIMESTAMP"}
	args := []any{status}
	if errMsg == "" {
		sets = append(sets, "error = NULL")
	} else {
		sets = append(sets, "error = ?")
		args = append(args, errMsg)
	}
	if started {
		sets = append(sets, "started_at = CURRENT_TIMESTAMP")
	}
	if completed {
		sets = append(sets, "completed_at = CURRENT_TIMESTAMP")
	}
	args = append(args, pid, id)
	if _, err := db.Exec(
		`UPDATE campaigns SET `+strings.Join(sets, ", ")+
			` WHERE project_id = ? AND id = ?`,
		args...,
	); err != nil {
		return nil, err
	}
	return dbCampaignGet(db, pid, id)
}

func dbCampaignSetJobIDs(db *sql.DB, pid string, id int64, jobIDs string) error {
	_, err := db.Exec(
		`UPDATE campaigns SET job_ids = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ?`,
		nullStr(jobIDs), pid, id,
	)
	return err
}

func dbCampaignArchive(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(
		`UPDATE campaigns SET archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ? AND archived_at IS NULL`,
		pid, id,
	)
	return err
}

// ─── Recipients ───────────────────────────────────────────────────

// dbRecipientsBulkInsert is the materialise-time bulk write. Uses
// INSERT OR IGNORE so re-materialising is idempotent (existing rows
// are kept; new contact_ids appear). Returns inserted count.
func dbRecipientsBulkInsert(db *sql.DB, pid string, campaignID int64, rows []Recipient) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO campaign_recipients
			(campaign_id, project_id, contact_id, address, status, created_at)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var inserted int64
	for _, r := range rows {
		status := r.Status
		if status == "" {
			status = RecipPending
		}
		res, err := stmt.Exec(campaignID, pid, r.ContactID, r.Address, status)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		inserted += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// dbRecipientClaimBatch atomically marks up to `batch` pending rows
// as 'sending' and returns them. The UPDATE+RETURNING shape is the
// simplest way to avoid two ticks claiming the same row when running
// on a single dispatcher (which jobs is, today).
func dbRecipientClaimBatch(db *sql.DB, pid string, campaignID int64, batch int) ([]Recipient, error) {
	if batch <= 0 {
		batch = 100
	}
	rows, err := db.Query(
		`UPDATE campaign_recipients
		 SET status = 'sending',
		     attempt_count = attempt_count + 1,
		     last_attempt_at = CURRENT_TIMESTAMP
		 WHERE id IN (
		   SELECT id FROM campaign_recipients
		   WHERE campaign_id = ? AND project_id = ? AND status = 'pending'
		   ORDER BY id LIMIT ?
		 )
		 RETURNING id, contact_id, address, attempt_count`,
		campaignID, pid, batch,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recipient{}
	for rows.Next() {
		r := Recipient{CampaignID: campaignID, Status: RecipSending}
		if err := rows.Scan(&r.ID, &r.ContactID, &r.Address, &r.AttemptCount); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func dbRecipientMarkSent(db *sql.DB, pid string, recipID, messagingID int64) error {
	_, err := db.Exec(
		`UPDATE campaign_recipients
		 SET status = 'sent', messaging_id = ?, sent_at = CURRENT_TIMESTAMP, error = NULL
		 WHERE id = ? AND project_id = ?`,
		messagingID, recipID, pid,
	)
	return err
}

func dbRecipientMarkFailed(db *sql.DB, pid string, recipID int64, msg string) error {
	_, err := db.Exec(
		`UPDATE campaign_recipients
		 SET status = 'failed', error = ?
		 WHERE id = ? AND project_id = ?`,
		truncate(msg, 500), recipID, pid,
	)
	return err
}

func dbRecipientMarkSkipped(db *sql.DB, pid string, recipID int64, reason string) error {
	_, err := db.Exec(
		`UPDATE campaign_recipients
		 SET status = 'skipped', error = ?
		 WHERE id = ? AND project_id = ?`,
		truncate(reason, 500), recipID, pid,
	)
	return err
}

func dbRecipientMarkUnsubscribed(db *sql.DB, pid string, recipID int64) error {
	_, err := db.Exec(
		`UPDATE campaign_recipients
		 SET status = 'unsubscribed'
		 WHERE id = ? AND project_id = ?`,
		recipID, pid,
	)
	return err
}

// dbRecipientCancelPending bulk-marks every pending recipient of a
// campaign as 'skipped' (with reason 'campaign cancelled'). Used by
// campaigns_cancel.
func dbRecipientCancelPending(db *sql.DB, pid string, campaignID int64) (int64, error) {
	res, err := db.Exec(
		`UPDATE campaign_recipients
		 SET status = 'skipped', error = 'campaign cancelled'
		 WHERE campaign_id = ? AND project_id = ? AND status IN ('pending', 'sending')`,
		campaignID, pid,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func dbRecipientsList(db *sql.DB, pid string, campaignID int64, status string, limit int) ([]*Recipient, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	where := []string{"project_id = ?", "campaign_id = ?"}
	args := []any{pid, campaignID}
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, campaign_id, contact_id, address, status,
				messaging_id, attempt_count,
				COALESCE(last_attempt_at,''), COALESCE(sent_at,''),
				COALESCE(delivered_at,''), COALESCE(error,''), created_at
		 FROM campaign_recipients WHERE `+strings.Join(where, " AND ")+
			` ORDER BY id DESC LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Recipient{}
	for rows.Next() {
		r := &Recipient{}
		var msgID sql.NullInt64
		if err := rows.Scan(&r.ID, &r.CampaignID, &r.ContactID, &r.Address, &r.Status,
			&msgID, &r.AttemptCount, &r.LastAttemptAt, &r.SentAt, &r.DeliveredAt, &r.Error, &r.CreatedAt); err == nil {
			if msgID.Valid {
				v := msgID.Int64
				r.MessagingID = &v
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// dbRecipientByID fetches one row, project-scoped. Used by the public
// unsubscribe endpoint (after token validation).
func dbRecipientByID(db *sql.DB, pid string, id int64) (*Recipient, error) {
	row := db.QueryRow(
		`SELECT id, campaign_id, contact_id, address, status,
				messaging_id, attempt_count,
				COALESCE(last_attempt_at,''), COALESCE(sent_at,''),
				COALESCE(delivered_at,''), COALESCE(error,''), created_at
		 FROM campaign_recipients WHERE id = ? AND project_id = ?`,
		id, pid,
	)
	r := &Recipient{}
	var msgID sql.NullInt64
	if err := row.Scan(&r.ID, &r.CampaignID, &r.ContactID, &r.Address, &r.Status,
		&msgID, &r.AttemptCount, &r.LastAttemptAt, &r.SentAt, &r.DeliveredAt, &r.Error, &r.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if msgID.Valid {
		v := msgID.Int64
		r.MessagingID = &v
	}
	return r, nil
}

// dbRecipientStats returns counts per status. Drives the panel
// progress bar and campaigns_stats tool.
func dbRecipientStats(db *sql.DB, pid string, campaignID int64) (map[string]int64, error) {
	rows, err := db.Query(
		`SELECT status, COUNT(*) FROM campaign_recipients
		 WHERE campaign_id = ? AND project_id = ?
		 GROUP BY status`,
		campaignID, pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var s string
		var n int64
		if err := rows.Scan(&s, &n); err == nil {
			out[s] = n
		}
	}
	return out, nil
}

// dbRecipientCountPending returns just the pending count — used by
// the tick handler to decide when to wind down.
func dbRecipientCountPending(db *sql.DB, pid string, campaignID int64) (int64, error) {
	var n int64
	row := db.QueryRow(
		`SELECT COUNT(*) FROM campaign_recipients
		 WHERE campaign_id = ? AND project_id = ? AND status IN ('pending', 'sending')`,
		campaignID, pid,
	)
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ─── Unsubscribe tokens ───────────────────────────────────────────

func dbUnsubscribeTokenCreate(db *sql.DB, pid string, recipID, campaignID int64, token string) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO campaign_unsubscribe_tokens
			(token, campaign_id, recipient_id, project_id)
		 VALUES (?, ?, ?, ?)`,
		token, campaignID, recipID, pid,
	)
	return err
}

type unsubLookup struct {
	RecipientID int64
	CampaignID  int64
	UsedAt      string
}

func dbUnsubscribeTokenLookup(db *sql.DB, token string) (*unsubLookup, string, error) {
	row := db.QueryRow(
		`SELECT recipient_id, campaign_id, project_id, COALESCE(used_at,'')
		 FROM campaign_unsubscribe_tokens WHERE token = ?`,
		token,
	)
	u := &unsubLookup{}
	var pid string
	if err := row.Scan(&u.RecipientID, &u.CampaignID, &pid, &u.UsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", nil
		}
		return nil, "", err
	}
	return u, pid, nil
}

func dbUnsubscribeTokenMarkUsed(db *sql.DB, token string) error {
	_, err := db.Exec(
		`UPDATE campaign_unsubscribe_tokens SET used_at = CURRENT_TIMESTAMP
		 WHERE token = ? AND used_at IS NULL`,
		token,
	)
	return err
}

// ─── Runtime config (auto-secret) ─────────────────────────────────

func dbRuntimeGet(db *sql.DB, key string) string {
	var v sql.NullString
	row := db.QueryRow(`SELECT v FROM campaign_runtime_config WHERE k = ?`, key)
	_ = row.Scan(&v)
	return v.String
}

func dbRuntimeSet(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		`INSERT INTO campaign_runtime_config (k, v) VALUES (?, ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		key, value,
	)
	return err
}
