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
	if err := expireDueGigs(ctx, app); err != nil {
		return err
	}
	return remindOfferedWorkers(ctx, app)
}

func expireDueGigs(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().QueryContext(ctx, `
		SELECT id, project_id
		  FROM gigs
		 WHERE status IN ('open','offered','accepted')
		   AND deadline_at IS NOT NULL
		   AND datetime(deadline_at) <= datetime('now')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type dueGig struct {
		id  int64
		pid string
	}
	var due []dueGig
	for rows.Next() {
		var g dueGig
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
		if _, err = tx.ExecContext(ctx, `
			UPDATE gigs
			   SET status='expired', completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND project_id=? AND status IN ('open','offered','accepted')`, g.id, g.pid); err == nil {
			_, err = tx.ExecContext(ctx, `
				UPDATE gig_assignments
				   SET status='withdrawn', token_revoked_at=CURRENT_TIMESTAMP
				 WHERE gig_id=? AND status IN ('offered','accepted')`, g.id)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO gig_events (project_id, gig_id, kind, actor, body)
				VALUES (?, ?, 'expired', 'system', 'deadline elapsed')`, g.pid, g.id)
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if err := syncContractFromGig(app.AppDB(), g.pid, g.id, "expired"); err != nil {
			app.Logger().Warn("sync contract milestone after gig expiry failed", "gig_id", g.id, "err", err.Error())
		}
		app.EmitWithProject("gig.expired", g.pid, map[string]any{"gig_id": g.id})
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

func loadAssignmentState(db *sql.DB, token string) (assignmentID, gigID int64, projectID, assignmentStatus, gigStatus, mode string, revoked bool, err error) {
	var revokedAt sql.NullString
	err = db.QueryRow(`
		SELECT a.id, a.gig_id, g.project_id, a.status, g.status,
		       COALESCE(a.mode, 'direct'), a.token_revoked_at
		  FROM gig_assignments a
		  JOIN gigs g ON g.id=a.gig_id
		 WHERE a.magic_token=?
		   AND (a.token_expires_at IS NULL OR datetime(a.token_expires_at) > datetime('now'))`, token,
	).Scan(&assignmentID, &gigID, &projectID, &assignmentStatus, &gigStatus, &mode, &revokedAt)
	revoked = revokedAt.Valid
	return
}

func assignmentAcceptsWork(assignmentStatus, gigStatus string, revoked bool) bool {
	if revoked {
		return false
	}
	if assignmentStatus != "offered" && assignmentStatus != "accepted" && assignmentStatus != "submitted" {
		return false
	}
	return gigStatus == "offered" || gigStatus == "accepted" || gigStatus == "submitted"
}
