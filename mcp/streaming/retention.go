package main

import (
	"context"
	"os"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Worker: retention-sweeper ────────────────────────────────────
//
// v0.1 wrote retention_days on every stream, echoed it back from
// streams_get, and then never read it again — nothing pruned
// segments, recordings, or stream_events. Combined with an unbounded
// HLS window that keeps every segment on disk, a box running weekly
// webinars grew monotonically until an operator ran streams_delete by
// hand.
//
// The sweeper prunes MEDIA and EVENTS for terminal streams past their
// retention window; the streams row itself survives (it carries the
// session's aggregate stats, which consumers still report on) and is
// stamped pruned_at so the hourly sweep doesn't re-walk finished
// streams forever. retention_days = 0 means "keep indefinitely".

type prunable struct {
	id            int64
	projectID     string
	storagePrefix string
	retentionDays int
	endedAt       string
}

func (a *App) runRetention(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}

	rows, err := app.AppDB().Query(
		`SELECT id, project_id, storage_prefix, retention_days, COALESCE(ended_at,'')
		 FROM streams
		 WHERE status IN ('ended','errored')
		   AND retention_days > 0
		   AND pruned_at IS NULL
		   AND ended_at IS NOT NULL`)
	if err != nil {
		return err
	}
	pending := []prunable{}
	for rows.Next() {
		var p prunable
		if err := rows.Scan(&p.id, &p.projectID, &p.storagePrefix, &p.retentionDays, &p.endedAt); err == nil {
			pending = append(pending, p)
		}
	}
	rows.Close()

	pruned := 0
	for _, p := range pending {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		endedAt, ok := parseTimestamp(p.endedAt)
		if !ok {
			// Unparseable timestamp — leave the row alone rather than
			// deleting media on a guess.
			app.Logger().Warn("retention: unparseable ended_at", "id", p.id, "ended_at", p.endedAt)
			continue
		}
		if time.Since(endedAt) < time.Duration(p.retentionDays)*24*time.Hour {
			continue
		}

		if err := os.RemoveAll(streamDataDir(app, p.storagePrefix)); err != nil {
			app.Logger().Warn("retention: rmdir", "id", p.id, "err", err)
			continue
		}
		if _, err := app.AppDB().Exec(`DELETE FROM stream_events WHERE stream_id = ?`, p.id); err != nil {
			app.Logger().Warn("retention: delete events", "id", p.id, "err", err)
		}
		if _, err := app.AppDB().Exec(
			`UPDATE streams SET pruned_at = ?, recording_path = NULL WHERE id = ?`,
			nowStamp(), p.id); err != nil {
			app.Logger().Warn("retention: mark pruned", "id", p.id, "err", err)
			continue
		}
		a.invalidatePlayback(p.projectID, p.id)
		app.Emit("stream.pruned", map[string]any{
			"id":             p.id,
			"retention_days": p.retentionDays,
		})
		pruned++
	}
	if pruned > 0 {
		app.Logger().Info("retention sweep", "pruned", pruned, "candidates", len(pending))
	}
	return nil
}
