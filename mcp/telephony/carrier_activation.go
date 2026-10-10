package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) prepareAndActivateTelnyxAI(ctx *sdk.AppCtx, row *callRow, directive, voice, greeting string) (string, error) {
	if row == nil || isTerminalStatus(row.Status) {
		return "", errAnswerCallEnded
	}
	current, err := a.db().findCall(row.ID)
	if err != nil {
		return "", err
	}
	if current == nil || isTerminalStatus(current.Status) {
		return "", errAnswerCallEnded
	}
	if current.ProjectID != ctx.CurrentProject() || current.AgentID != row.AgentID || current.ThreadID != row.ThreadID {
		return "", errAnswerPreparationInProgress
	}
	*row = *current
	if row.PeerKind != peerKindRealtime {
		return "", errors.New("call is not assigned to AI")
	}
	if row.RoutingFlowVersionID != "" {
		_, plan, err := a.routingPlanForCall(row, nil)
		if err != nil {
			return "", err
		}
		if plan != nil && plan.TerminalType != "destination" && plan.TerminalType != "ring_group" {
			return "", errors.New("call is still executing its routing flow")
		}
	}
	if !realtimePreparationReady(row) {
		// An IVR may already have answered without creating an AI thread. Preserve
		// carrier answer evidence while acquiring the ordinary startup claim.
		if row.Status == "answered" && carrierAnswerObserved(row) {
			_, err := a.db().db.Exec(`UPDATE calls SET status='pending' WHERE id=? AND status='answered' AND media_connected_at='' AND (thread_id='' OR thread_id LIKE 'pending-%')`, row.ID)
			if err != nil {
				return "", err
			}
		}
		if _, err := a.prepareInboundRealtime(ctx, row, directive, voice, greeting); err != nil {
			return "", err
		}
	}
	if err := a.ensureCarrierActivation(row); err != nil {
		return "", err
	}
	if err := a.driveCarrierActivation(ctx, row.ID); err != nil {
		return "", err
	}
	return row.ThreadID, nil
}

func (a *App) ensureCarrierActivation(row *callRow) error {
	deadline := time.Now().Add(30 * time.Second)
	if d, err := time.Parse(time.RFC3339Nano, row.DeadlineAt); err == nil && d.Before(deadline) {
		deadline = d
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO carrier_activations(call_id,project_id,thread_id,deadline_at,next_attempt_at) VALUES(?,?,?,?,?)`, row.ID, row.ProjectID, row.ThreadID, ringTime(deadline), ringTime(time.Now()))
	if err != nil {
		return err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		// The previous offer's timeout no longer owns this phase.
		_, err = tx.Exec(`UPDATE calls SET state_expires_at=? WHERE id=? AND thread_id=? AND status NOT IN ('completed','failed','busy','no-answer','canceled')`, ringTime(deadline), row.ID, row.ThreadID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) runCarrierActivations(_ context.Context, ctx *sdk.AppCtx) error {
	// Recover the gap if a request stopped waiting just before Core became ready.
	rows, err := a.db().listWhere(`project_id=? AND direction='inbound' AND carrier_slug='telnyx' AND peer_kind='realtime' AND status IN ('answering','answered','pending') AND thread_id NOT LIKE 'pending-%' AND audio_bridge_url<>'pending' AND audio_bridge_url<>'' AND EXISTS(SELECT 1 FROM ai_handoffs h WHERE h.call_id=calls.id AND h.status='ready') AND NOT EXISTS(SELECT 1 FROM inbound_routes r WHERE r.id=calls.route_id AND r.inbound_transport='sip_direct') AND NOT EXISTS(SELECT 1 FROM carrier_activations x WHERE x.call_id=calls.id) LIMIT 100`, ctx.CurrentProject())
	if err != nil {
		return err
	}
	for i := range rows {
		if err = a.ensureCarrierActivation(&rows[i]); err != nil {
			return err
		}
	}
	due, err := a.db().db.Query(`SELECT x.call_id FROM carrier_activations x JOIN calls c ON c.id=x.call_id WHERE x.project_id=? AND x.status IN ('pending','waiting','failed') AND (x.next_attempt_at<=? OR x.deadline_at<=? OR c.status IN ('completed','failed','busy','no-answer','canceled') OR (x.status<>'failed' AND (c.media_connected_at<>'' OR (x.phase='answer' AND c.carrier_answered_at<>'')))) ORDER BY x.next_attempt_at LIMIT 100`, ctx.CurrentProject(), ringTime(time.Now()), ringTime(time.Now()))
	if err != nil {
		return err
	}
	var ids []string
	for due.Next() {
		var id string
		if err = due.Scan(&id); err != nil {
			due.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = due.Err()
	due.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		a.launchAIRecovery("activation/"+id, func() {
			if err := a.driveCarrierActivation(ctx, id); err != nil && !errors.Is(err, errAnswerPreparationInProgress) && !errors.Is(err, errAnswerCallEnded) {
				ctx.Logger().Warn("AI carrier activation", "call", id, "err", err)
			}
		})
	}
	return nil
}

// One durable command per phase, with a stable carrier command_id. HTTP success
// means accepted; answer confirmation and media connection are separate facts.
func (a *App) driveCarrierActivation(ctx *sdk.AppCtx, id string) error {
	unlock := a.softphones.lockClaim(id)
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	var status, phase, thread, deadline, next, code string
	var attempts int
	err := a.db().db.QueryRow(`SELECT status,phase,thread_id,deadline_at,next_attempt_at,attempts,error_code FROM carrier_activations WHERE call_id=? AND project_id=?`, id, ctx.CurrentProject()).Scan(&status, &phase, &thread, &deadline, &next, &attempts, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "connected" {
		return nil
	}
	if status == "ended" || status == "canceled" {
		return errAnswerCallEnded
	}
	row, err := a.db().findCall(id)
	if err != nil {
		return err
	}
	if row == nil || isTerminalStatus(row.Status) || row.ThreadID != thread || row.PeerKind != peerKindRealtime {
		_, err = a.db().db.Exec(`UPDATE carrier_activations SET status='canceled' WHERE call_id=? AND thread_id=?`, id, thread)
		if err != nil {
			return err
		}
		return errAnswerCallEnded
	}
	if row.MediaConnectedAt != "" && row.MediaStatus == "connected" && status != "failed" {
		_, err = a.db().db.Exec(`UPDATE carrier_activations SET status='connected',error_code='' WHERE call_id=? AND thread_id=?`, id, thread)
		if err != nil {
			return err
		}
		return a.db().clearStateExpiry(id)
	}
	// Socket claims and carrier callbacks use the ordinary call lock; never wait
	// here for a network dispatch already in progress.
	pending, err := a.carrierCommandPending(id)
	if err != nil {
		return err
	}
	if pending {
		return errAnswerPreparationInProgress
	}
	now := ringTime(time.Now())
	desired := "answer"
	if carrierAnswerObserved(row) {
		desired = "stream"
	}
	if status != "failed" && (deadline <= now || (phase == desired && attempts >= 3 && next <= now)) {
		code = "carrier_activation_failed"
		_, err = a.db().db.Exec(`UPDATE carrier_activations SET status='failed',error_code=?,next_attempt_at=? WHERE call_id=? AND thread_id=?`, code, now, id, thread)
		if err != nil {
			return err
		}
		status = "failed"
		next = now
	}
	// A failed stream or missed confirmation is an unsuccessful activation attempt,
	// not an instruction to hang up after the first carrier command.
	if status == "waiting" && row.MediaStatus == "error" {
		status = "pending"
		next = ringTime(time.Now().Add(2 * time.Second))
		_, err = a.db().db.Exec(`UPDATE carrier_activations SET status='pending',error_code='media_confirmation_failed',next_attempt_at=? WHERE call_id=? AND thread_id=?`, next, id, thread)
		if err != nil {
			return err
		}
	}
	if status == "failed" {
		if next > now {
			return errAnswerPreparationInProgress
		}
		token, err := a.reserveCarrierCommand(row, "ai-terminate", thread)
		if err != nil {
			return err
		}
		if _, err = a.db().db.Exec(`UPDATE calls SET routing_resolution='ai_activation_failed',error_message='AI carrier activation failed',state_expires_at=? WHERE id=? AND thread_id=?`, now, id, thread); err != nil {
			a.releaseCarrierCommand(id, token)
			return err
		}
		_, err = a.db().db.Exec(`UPDATE carrier_activations SET next_attempt_at=? WHERE call_id=? AND thread_id=?`, ringTime(time.Now().Add(5*time.Second)), id, thread)
		if err != nil {
			a.releaseCarrierCommand(id, token)
			return err
		}
		unlock()
		unlock = nil
		// Release ownership serialization before carrier I/O, including termination.
		err = a.terminateCarrierCall(ctx, row)
		if err != nil && phase == "answer" && attempts > 0 {
			carrier, carrierErr := a.carrierForRow(ctx, nil, row)
			if carrierErr == nil {
				err = carrier.Hangup(ctx, row)
			} else {
				err = carrierErr
			}
		}
		unlock = a.softphones.lockClaim(id)
		owns, ownErr := a.ownsCarrierCommand(id, token)
		a.releaseCarrierCommand(id, token)
		if ownErr != nil {
			return ownErr
		}
		if !owns {
			return errAnswerPreparationInProgress
		}
		if err != nil {
			return err
		}
		current, err := a.db().findCall(id)
		if err != nil {
			return err
		}
		if current == nil || current.ThreadID != thread || current.PeerKind != peerKindRealtime {
			return errAnswerCallEnded
		}
		if !isTerminalStatus(current.Status) {
			_, err = a.db().updateStatusWithFacts(id, "failed", "AI carrier activation failed", lifecycleFacts{Source: "telephony", ExpectedThreadID: thread, TerminationInitiator: "telephony"})
			if err != nil {
				return err
			}
		}
		_, err = a.db().db.Exec(`UPDATE carrier_activations SET status='ended' WHERE call_id=? AND thread_id=?`, id, thread)
		a.launchAIRecovery("activation-cleanup/"+id, func() { _ = a.killCallThread(ctx, row) })
		return err
	}
	if phase == desired && next > now {
		return errAnswerPreparationInProgress
	}
	if phase != desired {
		attempts = 0
	}
	token, err := a.reserveCarrierCommand(row, "ai-"+desired, thread)
	if err != nil {
		return err
	}
	_, err = a.db().db.Exec(`UPDATE carrier_activations SET phase=?,attempts=?,status='pending',next_attempt_at=? WHERE call_id=? AND thread_id=?`, desired, attempts+1, ringTime(time.Now().Add(2*time.Second)), id, thread)
	if err != nil {
		a.releaseCarrierCommand(id, token)
		return err
	}
	// Retain failed media evidence until the replacement handler confirms media.
	// Its bridge URL resolver also needs that state to renew the Core lease.
	limit := carrierActivationCommandTimeout
	if end, e := time.Parse(time.RFC3339Nano, deadline); e == nil && time.Until(end) < limit {
		limit = time.Until(end)
	}
	request, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	unlock()
	unlock = nil
	if desired == "answer" {
		input := map[string]any{"call_control_id": row.CarrierSID, "command_id": telnyxCommandID(id, "ai-answer"), "webhook_url": a.statusCallbackURL(row.ID, row.CallbackSecret, row.ProjectID), "webhook_url_method": "POST"}
		if row.RecordingMode == recordingModeAlways {
			input["record"] = "record-from-answer"
			input["record_channels"] = telnyxRecordingChannels(row.RecordingChannels)
			input["record_format"] = "wav"
			input["record_track"] = "both"
		}
		_, err = executeCarrierTool(ctx, row.CarrierConnectionID, "answer_call", input, request)
	} else {
		err = a.startTelnyxStream(ctx, row, request)
	}
	commandErr := err
	unlock = a.softphones.lockClaim(id)
	owns, err := a.ownsCarrierCommand(id, token)
	if err != nil {
		return err
	}
	defer a.releaseCarrierCommand(id, token)
	current, err := a.db().findCall(id)
	if err != nil {
		return err
	}
	if current == nil || isTerminalStatus(current.Status) || current.ThreadID != thread || current.PeerKind != peerKindRealtime {
		return errAnswerCallEnded
	}
	if !owns {
		return errAnswerPreparationInProgress
	}
	if current.MediaConnectedAt != "" && current.MediaStatus == "connected" {
		_, err = a.db().db.Exec(`UPDATE carrier_activations SET status='connected',error_code='' WHERE call_id=? AND thread_id=?`, id, thread)
		if err != nil {
			return err
		}
		return a.db().clearStateExpiry(id)
	}
	if commandErr != nil || current.MediaStatus == "error" {
		code = desired + "_command_failed"
		if commandErr == nil {
			code = "media_confirmation_failed"
		}
		_, err = a.db().db.Exec(`UPDATE carrier_activations SET status='pending',error_code=?,next_attempt_at=? WHERE call_id=? AND thread_id=? AND phase=? AND status NOT IN ('connected','canceled','ended','failed')`, code, ringTime(time.Now().Add(time.Duration(1<<min(attempts+1, 3))*time.Second)), id, thread, desired)
		if err != nil {
			return err
		}
		return errAnswerPreparationInProgress
	}
	_, err = a.db().db.Exec(`UPDATE carrier_activations SET status='waiting',error_code='',next_attempt_at=? WHERE call_id=? AND thread_id=? AND phase=? AND status NOT IN ('connected','canceled','ended','failed')`, ringTime(time.Now().Add(10*time.Second)), id, thread, desired)
	if err != nil {
		return err
	}
	return errAnswerPreparationInProgress
}

func (a *App) carrierActivationPublic(id string) (map[string]any, error) {
	var status, phase, code, deadline string
	var attempts int
	err := a.db().db.QueryRow(`SELECT status,phase,attempts,error_code,deadline_at FROM carrier_activations WHERE call_id=?`, id).Scan(&status, &phase, &attempts, &code, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": status, "phase": phase, "attempts": attempts, "error_code": code, "deadline_at": deadline}, nil
}

// The periodic worker is the recovery path; callbacks only accelerate it.
func (a *App) advanceCarrierActivation(ctx *sdk.AppCtx, id string) {
	if err := a.driveCarrierActivation(ctx, id); err != nil && !errors.Is(err, errAnswerPreparationInProgress) && !errors.Is(err, errAnswerCallEnded) {
		ctx.Logger().Warn("carrier activation callback pending", "call", id)
	}
}
