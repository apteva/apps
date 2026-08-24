package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func runLifecycleSweep(ctx context.Context, app *sdk.AppCtx) error {
	if err := markOverdueGigs(ctx, app); err != nil {
		return err
	}
	if err := expireAssignmentAccess(ctx, app); err != nil {
		return err
	}
	return remindOfferedWorkers(ctx, app)
}

func markOverdueGigs(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().QueryContext(ctx, `
		SELECT id, project_id
		  FROM gigs
		 WHERE status IN ('open','offered','accepted')
		   AND due_at IS NOT NULL
		   AND overdue_at IS NULL
		   AND datetime(due_at) <= datetime('now')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type overdueGig struct {
		id  int64
		pid string
	}
	var due []overdueGig
	for rows.Next() {
		var g overdueGig
		if err := rows.Scan(&g.id, &g.pid); err != nil {
			return err
		}
		due = append(due, g)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, g := range due {
		tx, err := app.AppDB().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE gigs
			SET overdue_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND project_id=? AND overdue_at IS NULL
			  AND status IN ('open','offered','accepted')`, g.id, g.pid); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO gig_events (project_id, gig_id, kind, actor, body)
				VALUES (?, ?, 'overdue', 'system', 'soft due date elapsed')`, g.pid, g.id)
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		app.EmitWithProject("gig.overdue", g.pid, map[string]any{"gig_id": g.id})
	}
	return nil
}

func expireAssignmentAccess(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().QueryContext(ctx, `SELECT a.id,a.gig_id,g.project_id
		FROM gig_assignments a JOIN gigs g ON g.id=a.gig_id
		WHERE a.token_revoked_at IS NULL AND a.token_expires_at IS NOT NULL
		  AND a.status IN ('offered','accepted','submitted')
		  AND datetime(a.token_expires_at)<=datetime('now')`)
	if err != nil {
		return err
	}
	type expiredAccess struct {
		assignmentID, gigID int64
		pid                 string
	}
	var items []expiredAccess
	for rows.Next() {
		var item expiredAccess
		if err := rows.Scan(&item.assignmentID, &item.gigID, &item.pid); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		tx, err := app.AppDB().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE gig_assignments
			SET status=CASE WHEN status IN ('offered','accepted') THEN 'withdrawn' ELSE status END,
			    token_revoked_at=CURRENT_TIMESTAMP
			WHERE id=? AND token_revoked_at IS NULL`, item.assignmentID)
		if err == nil {
			if changed, _ := res.RowsAffected(); changed == 0 {
				_ = tx.Rollback()
				continue
			}
			_, err = tx.ExecContext(ctx, `UPDATE gigs SET status=CASE
				WHEN status IN ('submitted','reviewed','cancelled','expired') THEN status
				WHEN EXISTS (SELECT 1 FROM gig_assignments WHERE gig_id=? AND status='accepted' AND token_revoked_at IS NULL) THEN 'accepted'
				WHEN EXISTS (SELECT 1 FROM gig_assignments WHERE gig_id=? AND status='offered' AND token_revoked_at IS NULL) THEN 'offered'
				ELSE 'open' END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, item.gigID, item.gigID, item.gigID)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO gig_events(project_id,gig_id,kind,actor,body)
				VALUES (?,?,'access_expired','system',?)`, item.pid, item.gigID, fmt.Sprintf("assignment:%d", item.assignmentID))
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		app.EmitWithProject("gig.access_expired", item.pid, map[string]any{"gig_id": item.gigID, "assignment_id": item.assignmentID})
	}
	return nil
}

func remindOfferedWorkers(ctx context.Context, app *sdk.AppCtx) error {
	hours := atoi(app.Config().Get("auto_remind_after_hours"))
	if hours <= 0 {
		return nil
	}
	modifier := "-" + strconv.Itoa(hours) + " hours"
	rows, err := app.AppDB().QueryContext(ctx, `
		SELECT a.id, a.gig_id, a.worker_id, g.project_id, g.title
		  FROM gig_assignments a
		  JOIN gigs g ON g.id=a.gig_id
		 WHERE a.status='offered' AND a.notify_worker=1
		   AND datetime(a.offered_at) <= datetime('now', ?)
		   AND a.token_revoked_at IS NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM gig_events e
		        WHERE e.gig_id=a.gig_id AND e.kind='reminded'
		          AND e.actor=('worker:' || a.worker_id)
		          AND datetime(e.at) >= datetime(a.offered_at)
		   )`, modifier)
	if err != nil {
		return err
	}
	defer rows.Close()
	type reminder struct {
		assignmentID int64
		gigID        int64
		workerID     int64
		pid          string
		title        string
	}
	var pending []reminder
	for rows.Next() {
		var item reminder
		if err := rows.Scan(&item.assignmentID, &item.gigID, &item.workerID, &item.pid, &item.title); err != nil {
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		wk, err := getWorker(app.AppDB(), item.pid, item.workerID)
		if err != nil || wk == nil {
			continue
		}
		var token, publicBaseURL string
		if err := app.AppDB().QueryRowContext(ctx, `SELECT magic_token, COALESCE(public_base_url,'') FROM gig_assignments WHERE id=?`, item.assignmentID).Scan(&token, &publicBaseURL); err != nil {
			continue
		}
		workerURL, err := buildWorkerURL(app, token, publicBaseURL)
		if err != nil {
			app.Logger().Warn("gig reminder URL failed", "gig_id", item.gigID, "err", err.Error())
			continue
		}
		body := fmt.Sprintf("Reminder: %s\n\nOpen: %s", item.title, workerURL)
		if _, err := crmSendMessage(app, item.pid, wk.ContactID, body, wk.DefaultChannel, item.title); err != nil {
			app.Logger().Warn("gig reminder failed", "gig_id", item.gigID, "err", err.Error())
			continue
		}
		_, _ = app.AppDB().ExecContext(ctx, `
			INSERT INTO gig_events (project_id, gig_id, kind, actor, body)
			VALUES (?, ?, 'reminded', ?, ?)`, item.pid, item.gigID, "worker:"+strconv.FormatInt(item.workerID, 10), time.Now().UTC().Format(time.RFC3339))
	}
	return nil
}

func loadAssignmentState(db *sql.DB, token string) (assignmentID, gigID int64, projectID, assignmentStatus, gigStatus, mode string, revoked, accessExpired bool, err error) {
	var revokedAt sql.NullString
	var expired int
	err = db.QueryRow(`
		SELECT a.id, a.gig_id, g.project_id, a.status, g.status,
		       COALESCE(a.mode, 'direct'), a.token_revoked_at,
		       CASE WHEN a.token_expires_at IS NOT NULL AND datetime(a.token_expires_at)<=datetime('now') THEN 1 ELSE 0 END
		  FROM gig_assignments a
		  JOIN gigs g ON g.id=a.gig_id
		 WHERE a.magic_token=?`, token,
	).Scan(&assignmentID, &gigID, &projectID, &assignmentStatus, &gigStatus, &mode, &revokedAt, &expired)
	revoked = revokedAt.Valid
	accessExpired = expired != 0
	return
}

func assignmentAcceptsWork(assignmentStatus, gigStatus string, revoked, accessExpired bool) bool {
	if revoked || accessExpired {
		return false
	}
	if assignmentStatus != "accepted" && assignmentStatus != "submitted" {
		return false
	}
	return gigStatus == "accepted" || gigStatus == "submitted"
}
