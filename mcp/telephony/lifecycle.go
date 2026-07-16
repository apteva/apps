package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type outboxRow struct {
	CallID   string
	AgentID  int64
	Message  string
	Attempts int
}

func (c *callsDB) insertInboundCallWithEvent(call callRow, message string) (*callRow, bool, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRow(`SELECT id FROM calls
        WHERE direction = 'inbound' AND carrier_slug = ? AND carrier_connection_id = ? AND carrier_sid = ?`,
		call.CarrierSlug, call.CarrierConnectionID, call.CarrierSID).Scan(&existingID)
	if err == nil {
		existing, scanErr := scanCall(tx.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id = ?`, existingID))
		return existing, false, scanErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	_, err = tx.Exec(`INSERT INTO calls
        (id, thread_id, direction, agent_id, route_id, carrier_sid, carrier_request_id,
         carrier_slug, carrier_connection_id, callback_secret, to_number, from_number,
         directive, voice, audio_bridge_url, status, placed_at, project_id,
         idempotency_key, state_expires_at, deadline_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		call.ID, call.ThreadID, call.Direction, call.AgentID, call.RouteID, call.CarrierSID, call.CarrierRequestID,
		call.CarrierSlug, call.CarrierConnectionID, call.CallbackSecret, call.ToNumber, call.FromNumber,
		call.Directive, call.Voice, call.AudioBridgeURL, call.Status, call.PlacedAt, call.ProjectID,
		call.IdempotencyKey, call.StateExpiresAt, call.DeadlineAt)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO inbound_event_outbox
        (call_id, project_id, agent_id, message, next_attempt_at)
        VALUES (?, ?, ?, ?, ?)`, call.ID, call.ProjectID, call.AgentID, message, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &call, true, nil
}

func (a *App) deliverOutboxCall(ctx *sdk.AppCtx, callID string) error {
	var event outboxRow
	err := ctx.AppDB().QueryRow(`SELECT call_id, agent_id, message, attempts
        FROM inbound_event_outbox WHERE call_id = ? AND delivered_at = ''`, callID).
		Scan(&event.CallID, &event.AgentID, &event.Message, &event.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.deliverOutboxEvent(ctx, event)
}

func (a *App) deliverOutboxEvent(ctx *sdk.AppCtx, event outboxRow) error {
	err := ctx.PlatformAPI().SendEvent(event.AgentID, event.Message)
	if err == nil {
		_, markErr := ctx.AppDB().Exec(`UPDATE inbound_event_outbox
            SET delivered_at = ?, last_error = '' WHERE call_id = ? AND delivered_at = ''`,
			time.Now().UTC().Format(time.RFC3339), event.CallID)
		return markErr
	}
	delaySeconds := math.Min(300, math.Pow(2, float64(min(event.Attempts, 8))))
	_, _ = ctx.AppDB().Exec(`UPDATE inbound_event_outbox
        SET attempts = attempts + 1, last_error = ?, next_attempt_at = ?
        WHERE call_id = ? AND delivered_at = ''`,
		err.Error(), time.Now().UTC().Add(time.Duration(delaySeconds)*time.Second).Format(time.RFC3339), event.CallID)
	return err
}

func (a *App) runLifecycleTick(_ context.Context, ctx *sdk.AppCtx) error {
	project := ctx.CurrentProject()
	if project == "" {
		return nil
	}
	now := time.Now().UTC()

	rows, err := ctx.AppDB().Query(`SELECT call_id, agent_id, message, attempts
        FROM inbound_event_outbox
        WHERE project_id = ? AND delivered_at = '' AND next_attempt_at <= ?
        ORDER BY next_attempt_at LIMIT 20`, project, now.Format(time.RFC3339))
	if err != nil {
		return err
	}
	var events []outboxRow
	for rows.Next() {
		var event outboxRow
		if err := rows.Scan(&event.CallID, &event.AgentID, &event.Message, &event.Attempts); err != nil {
			rows.Close()
			return err
		}
		events = append(events, event)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, event := range events {
		if err := a.deliverOutboxEvent(ctx, event); err != nil {
			ctx.Logger().Warn("deliver incoming call event", "call", event.CallID, "err", err)
		}
	}

	expired, err := a.db().listWhere(`project_id = ?
        AND status NOT IN ('completed','failed','no-answer','busy','canceled')
        AND ((state_expires_at <> '' AND state_expires_at <= ?)
          OR (deadline_at <> '' AND deadline_at <= ?))
        ORDER BY placed_at LIMIT 50`, project, now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return err
	}
	for i := range expired {
		row := &expired[i]
		if err := a.expireCall(ctx, row); err != nil {
			ctx.Logger().Warn("expire call", "call", row.ID, "status", row.Status, "err", err)
			_ = a.db().setStateExpiry(row.ID, now.Add(30*time.Second))
		}
	}

	_, _ = ctx.AppDB().Exec(`UPDATE calls SET callback_secret = '', audio_bridge_url = ''
        WHERE project_id = ? AND ended_at <> '' AND ended_at < ?`,
		project, now.Add(-24*time.Hour).Format(time.RFC3339))
	_, _ = ctx.AppDB().Exec(`DELETE FROM inbound_event_outbox WHERE call_id IN (
        SELECT id FROM calls WHERE project_id = ? AND ended_at <> '' AND ended_at < ?
    )`, project, now.Add(-30*24*time.Hour).Format(time.RFC3339))
	_, _ = ctx.AppDB().Exec(`DELETE FROM calls
        WHERE project_id = ? AND ended_at <> '' AND ended_at < ?`,
		project, now.Add(-30*24*time.Hour).Format(time.RFC3339))
	return nil
}

func (a *App) expireCall(ctx *sdk.AppCtx, row *callRow) error {
	if row.CarrierSID != "" {
		carrier, err := a.carrierForRow(ctx, nil, row)
		if err != nil {
			return err
		}
		if err := carrier.Hangup(ctx, row); err != nil {
			return fmt.Errorf("carrier hangup: %w", err)
		}
	}
	if err := a.killCallThread(ctx, row); err != nil {
		ctx.Logger().Warn("kill expired call thread", "call", row.ID, "err", err)
	}
	status := "failed"
	reason := "call lifecycle deadline exceeded"
	if row.Status == "pending" || row.Status == "answering" || row.Status == "initiated" || row.Status == "ringing" {
		status = "no-answer"
		reason = "call was not connected before its deadline"
	}
	return a.db().updateStatus(row.ID, status, reason)
}
