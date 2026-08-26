package main

import (
	"context"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) runPresenceReaper(ctx context.Context, app *sdk.AppCtx) error {
	d, cancel := withDeadline(ctx, 5*time.Second)
	defer cancel()
	rows, err := app.AppDB().QueryContext(d,
		`SELECT project_id, room_id, id FROM participants
		  WHERE status IN ('joining','active')
		    AND (last_seen_at IS NULL OR last_seen_at < datetime('now', '-45 seconds'))`)
	if err != nil {
		return err
	}
	type staleParticipant struct {
		projectID             string
		roomID, participantID int64
	}
	items := []staleParticipant{}
	for rows.Next() {
		var item staleParticipant
		if err := rows.Scan(&item.projectID, &item.roomID, &item.participantID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		p, changed, err := a.transitionParticipant(app, item.projectID, item.roomID, item.participantID, "left", "presence timeout")
		if err != nil {
			app.Logger().Warn("reap participant", "participant_id", item.participantID, "err", err)
			continue
		}
		if changed {
			a.emit(app, item.projectID, "participant.left", map[string]any{"room_id": item.roomID, "participant_id": item.participantID, "participant_kind": p.Kind, "reason": "presence timeout"})
			a.emit(app, item.projectID, "peer.closed", map[string]any{"room_id": item.roomID, "participant_id": item.participantID})
		}
	}
	return d.Err()
}

func (a *App) runRoomIdleCloser(ctx context.Context, app *sdk.AppCtx) error {
	timeout := 300
	if v := app.Config().Get("idle_room_timeout_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = n
		}
	}
	d, cancel := withDeadline(ctx, 10*time.Second)
	defer cancel()
	rows, err := app.AppDB().QueryContext(d,
		`SELECT r.project_id, r.id
		   FROM rooms r
		  WHERE r.status = 'open'
		    AND COALESCE(r.last_activity_at, r.created_at) < datetime('now', ?)
		    AND NOT EXISTS (
		      SELECT 1 FROM participants p
		       WHERE p.room_id = r.id AND p.project_id = r.project_id
		         AND p.status IN ('joining','active')
		    )`,
		"-"+strconv.Itoa(timeout)+" seconds")
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		projectID string
		roomID    int64
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.projectID, &it.roomID); err != nil {
			return err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, it := range items {
		if _, err := a.toolEndRoom(app, map[string]any{"_project_id": it.projectID, "id": it.roomID}); err != nil {
			app.Logger().Warn("idle close room", "room_id", it.roomID, "err", err)
		}
	}
	return nil
}

func (a *App) runSessionCleaner(ctx context.Context, app *sdk.AppCtx) error {
	d, cancel := withDeadline(ctx, 10*time.Second)
	defer cancel()
	tx, err := app.AppDB().BeginTx(d, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(d,
		`UPDATE peer_sessions SET status='closed', connection_state='closed', closed_at=CURRENT_TIMESTAMP, error='negotiation timeout'
		  WHERE status='negotiating' AND created_at < datetime('now', '-5 minutes')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(d, `DELETE FROM signaling_messages WHERE created_at < datetime('now','-1 hour')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(d, `DELETE FROM join_tokens WHERE COALESCE(revoked_at,expires_at) < datetime('now','-30 days')`); err != nil {
		return err
	}
	retentionDays := configInt(app, "retention_days", 90)
	if _, err := tx.ExecContext(d, `DELETE FROM rooms WHERE status='ended' AND ended_at < datetime('now', ?)`, "-"+strconv.Itoa(retentionDays)+" days"); err != nil {
		return err
	}
	return tx.Commit()
}
