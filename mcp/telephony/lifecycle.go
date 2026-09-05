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

func (c *callsDB) insertInboundCallWithEvent(call callRow, message string, plans ...*inboundRoutingPlan) (*callRow, bool, error) {
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
	         forwarded_from, ingress_path, directive, voice, audio_bridge_url, status, placed_at, project_id,
		         idempotency_key, state_expires_at, deadline_at, recording_mode,
		         recording_channels, recording_storage_mode, recording_retention_days,
		         peer_kind, peer_token)
		        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		call.ID, call.ThreadID, call.Direction, call.AgentID, call.RouteID, call.CarrierSID, call.CarrierRequestID,
		call.CarrierSlug, call.CarrierConnectionID, call.CallbackSecret, call.ToNumber, call.FromNumber,
		call.ForwardedFrom, call.IngressPath, call.Directive, call.Voice, call.AudioBridgeURL, call.Status, call.PlacedAt, call.ProjectID,
		call.IdempotencyKey, call.StateExpiresAt, call.DeadlineAt,
		firstNonEmpty(call.RecordingMode, recordingModeOff), firstNonEmpty(call.RecordingChannels, "dual"),
		firstNonEmpty(call.RecordingStorageMode, recordingStorageCopy), call.RecordingRetentionDays,
		firstNonEmpty(call.PeerKind, peerKindRealtime), call.PeerToken)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	call.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE calls SET updated_at = ? WHERE id = ?`, now, call.ID); err != nil {
		return nil, false, err
	}
	if _, err := enqueueLifecycleEventTx(tx, &call, "call.incoming", call.PlacedAt, lifecycleFacts{
		OccurredAt:      call.PlacedAt,
		Source:          "provider",
		ProviderEventID: firstNonEmpty(call.CarrierSID, call.ID) + ":incoming",
	}); err != nil {
		return nil, false, err
	}
	if len(plans) == 0 || plans[0] == nil || (plans[0].TerminalType == "destination" && plans[0].Group == nil) {
		if _, err := tx.Exec(`INSERT INTO inbound_event_outbox
        (call_id, project_id, agent_id, message, next_attempt_at)
        VALUES (?, ?, ?, ?, ?)`, call.ID, call.ProjectID, call.AgentID, message, now); err != nil {
			return nil, false, err
		}
	}
	if len(plans) > 0 {
		if err := persistRoutingExecutionTx(tx, call.ID, call.ProjectID, plans[0]); err != nil {
			return nil, false, err
		}
	}
	stored, err := scanCall(tx.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id=?`, call.ID))
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return stored, true, nil
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

func (a *App) enqueueImmediateAnswer(route *routeRow, callID string) {
	if route == nil || route.AnswerMode != answerModeRealtimeImmediate || globalCtx == nil {
		return
	}
	routeCopy := *route
	ctx := globalCtx.WithProject(route.ProjectID)
	go func() {
		if err := a.answerImmediateCall(ctx, &routeCopy, callID); err != nil {
			ctx.Logger().Warn("immediate inbound answer", "call", callID, "route", routeCopy.ID, "err", err)
		}
	}()
}

func (a *App) answerImmediateCall(ctx *sdk.AppCtx, route *routeRow, callID string) error {
	if route == nil || !route.Enabled || route.AnswerMode != answerModeRealtimeImmediate {
		return nil
	}
	row, err := a.db().findCall(callID)
	if err != nil {
		return fmt.Errorf("load call: %w", err)
	}
	if row == nil || row.RouteID != route.ID || row.ProjectID != route.ProjectID || row.AgentID != route.AgentID {
		return nil
	}
	if row.Status != "pending" {
		return nil
	}
	_, err = a.answerCall(ctx, row, route.AutoDirective, route.AutoVoice, route.AutoGreeting, true)
	return err
}

func (a *App) runAutoAnswerTick(_ context.Context, ctx *sdk.AppCtx) error {
	project := ctx.CurrentProject()
	if project == "" {
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT c.id, r.id
        FROM calls c JOIN inbound_routes r ON r.id = c.route_id
        WHERE c.project_id = ? AND c.direction = 'inbound' AND c.status = 'pending'
          AND r.enabled = 1
          AND (c.state_expires_at = '' OR c.state_expires_at > ?)
        ORDER BY c.placed_at LIMIT 20`, project, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	type pendingAnswer struct{ callID, routeID string }
	var pending []pendingAnswer
	for rows.Next() {
		var item pendingAnswer
		if err := rows.Scan(&item.callID, &item.routeID); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range pending {
		row, err := a.db().findCall(item.callID)
		if err != nil {
			return err
		}
		if row == nil {
			continue
		}
		route, plan, err := a.routingPlanForCall(row, nil)
		if err != nil {
			return err
		}
		if plan != nil && plan.TerminalType != "destination" && plan.TerminalType != "ring_group" {
			continue
		}
		if route == nil || route.AnswerMode != answerModeRealtimeImmediate {
			continue
		}
		if err := a.answerImmediateCall(ctx, route, item.callID); err != nil {
			ctx.Logger().Warn("recover immediate inbound answer", "call", item.callID, "route", item.routeID, "err", err)
		}
	}
	return nil
}

func (a *App) runLifecycleTick(_ context.Context, ctx *sdk.AppCtx) error {
	project := ctx.CurrentProject()
	if project == "" {
		return nil
	}
	now := time.Now().UTC()
	_, _ = ctx.AppDB().Exec(`UPDATE call_offers SET status='expired' WHERE project_id=? AND run_id='' AND status='offered' AND expires_at<=?`, project, now.Format(time.RFC3339Nano))
	if err := a.publishLifecycleEvents(ctx, ""); err != nil {
		ctx.Logger().Warn("publish pending call lifecycle events", "err", err)
	}

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
		WHERE project_id = ? AND ended_at <> '' AND ended_at < ?
		  AND NOT EXISTS (SELECT 1 FROM recordings r WHERE r.call_id = calls.id AND r.deleted_at = '')`,
		project, now.Add(-30*24*time.Hour).Format(time.RFC3339))
	return nil
}

func (a *App) expireCall(ctx *sdk.AppCtx, row *callRow) error {
	if a.callUsesDirectSIP(row) {
		if gateway := a.directSIPGateway(); gateway != nil {
			if err := gateway.Hangup(row); err != nil {
				return err
			}
		}
	} else if row.CarrierSID != "" {
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
