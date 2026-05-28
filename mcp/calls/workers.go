package main

import (
	"context"
	"strconv"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) runPresenceReaper(ctx context.Context, app *sdk.AppCtx) error {
	d, cancel := withDeadline(ctx, 5_000_000_000)
	defer cancel()
	_ = d
	_, err := app.AppDB().Exec(
		`UPDATE participants
		    SET status='left', left_at = CURRENT_TIMESTAMP
		  WHERE status IN ('joining','active')
		    AND last_seen_at IS NOT NULL
		    AND last_seen_at < datetime('now', '-60 seconds')`)
	return err
}

func (a *App) runRoomIdleCloser(ctx context.Context, app *sdk.AppCtx) error {
	timeout := 300
	if v := app.Config().Get("idle_room_timeout_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = n
		}
	}
	rows, err := app.AppDB().Query(
		`SELECT r.project_id, r.id
		   FROM rooms r
		  WHERE r.status = 'open'
		    AND r.created_at < datetime('now', ?)
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
	for _, it := range items {
		if _, err := a.toolEndRoom(app, map[string]any{"_project_id": it.projectID, "id": it.roomID}); err != nil {
			app.Logger().Warn("idle close room", "room_id", it.roomID, "err", err)
		}
	}
	return nil
}

func (a *App) runSessionCleaner(ctx context.Context, app *sdk.AppCtx) error {
	_, err := app.AppDB().Exec(
		`UPDATE peer_sessions
		    SET status='closed', closed_at = CURRENT_TIMESTAMP, error='negotiation timeout'
		  WHERE status='negotiating'
		    AND created_at < datetime('now', '-5 minutes')`)
	return err
}
