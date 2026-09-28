package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const routingEffectAttempts = 5

// Persist the external effect in the same transaction as routing progress. A
// finished ring run does not imply that its carrier command has succeeded.
func enqueueRoutingEffectTx(tx *sql.Tx, callID, project string, plan *inboundRoutingPlan) error {
	if plan == nil {
		return nil
	}
	terminal := plan.TerminalType == "hangup" || plan.TerminalType == "reject"
	if !terminal && plan.TerminalType != "dtmf_menu" {
		return nil
	}
	var provider, status string
	if err := tx.QueryRow(`SELECT carrier_slug,status FROM calls WHERE id=? AND project_id=?`, callID, project).Scan(&provider, &status); err != nil {
		return err
	}
	if isTerminalStatus(status) || (!terminal && provider != "telnyx") {
		return nil
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := tx.Exec(`INSERT OR IGNORE INTO routing_effects(id,call_id,project_id,node_id,plan_json,next_attempt_at,updated_at) VALUES(?,?,?,?,?,?,?)`, callID+":"+plan.NodeID, callID, project, plan.NodeID, string(raw), ringTime(now), ringTime(now))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted > 0 && terminal {
		// Give the selected announcement time to finish; the absolute call deadline
		// still bounds the call if a carrier never sends its completion callback.
		seconds := 30
		if terminalAnnouncementText(plan) != "" {
			seconds = 180
		}
		_, err = tx.Exec(`UPDATE calls SET state_expires_at=? WHERE id=? AND status='pending'`, ringTime(now.Add(time.Duration(seconds)*time.Second)), callID)
	}
	return err
}

func (a *App) ensureRoutingEffect(row *callRow, plan *inboundRoutingPlan) error {
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enqueueRoutingEffectTx(tx, row.ID, row.ProjectID, plan); err != nil {
		return err
	}
	return tx.Commit()
}

type routingEffect struct {
	ID, CallID, Project, Node, Status, Stage string
	Attempts                                 int
	Plan                                     inboundRoutingPlan
}

func routingMayControlCall(row *callRow) bool {
	return row != nil && (row.Status == "pending" || row.Status == "answered") && row.MediaConnectedAt == "" && row.PeerToken == "" && (row.ThreadID == "" || strings.HasPrefix(row.ThreadID, "pending-"))
}

func (a *App) runRoutingEffects(_ context.Context, ctx *sdk.AppCtx) error {
	if err := a.recoverLegacyAnnouncementEffects(ctx); err != nil {
		return err
	}
	rows, err := a.db().db.Query(`SELECT call_id,node_id FROM routing_effects WHERE project_id=? AND status='pending' AND next_attempt_at<=? ORDER BY next_attempt_at LIMIT 20`, ctx.CurrentProject(), ringTime(time.Now()))
	if err != nil {
		return err
	}
	var items [][2]string
	for rows.Next() {
		var item [2]string
		if err = rows.Scan(&item[0], &item[1]); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if err = a.driveRoutingEffect(ctx, item[0], item[1]); err != nil {
			ctx.Logger().Warn("routing carrier effect pending", "call", item[0], "node", item[1], "err", err)
		}
	}
	return nil
}

// The browser answer path uses this same claim lock. External work can never
// reset an adviser claim, and a durable row survives process restarts.
func (a *App) driveRoutingEffect(ctx *sdk.AppCtx, callID, nodeID string) error {
	if ctx == nil {
		return errors.New("app context unavailable for routing effect")
	}
	unlock := a.softphones.lockClaim(callID)
	defer unlock()
	var e routingEffect
	var raw string
	err := a.db().db.QueryRow(`SELECT id,call_id,project_id,node_id,status,stage,attempts,plan_json FROM routing_effects WHERE id=? AND project_id=?`, callID+":"+nodeID, ctx.CurrentProject()).Scan(&e.ID, &e.CallID, &e.Project, &e.Node, &e.Status, &e.Stage, &e.Attempts, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if e.Status != "pending" {
		return nil
	}
	if err = json.Unmarshal([]byte(raw), &e.Plan); err != nil {
		return err
	}
	row, err := a.db().findCall(callID)
	if err != nil {
		return err
	}
	var current string
	err = a.db().db.QueryRow(`SELECT current_node_id FROM call_route_executions WHERE call_id=?`, callID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !routingMayControlCall(row) || (current != "" && current != e.Node) {
		_, err = a.db().db.Exec(`UPDATE routing_effects SET status='canceled',updated_at=? WHERE id=?`, ringTime(time.Now()), e.ID)
		return err
	}
	var execution routingExecutionContext
	if e.Plan.ContextJSON != "" {
		if err = json.Unmarshal([]byte(e.Plan.ContextJSON), &execution); err != nil {
			return err
		}
	}
	route := execution.Route
	if route.ID == "" && row.RouteID != "" {
		stored, findErr := a.db().findRoute(row.RouteID)
		if findErr != nil {
			return findErr
		}
		if stored != nil {
			route = *stored
		}
	}
	prompt := terminalAnnouncementText(&e.Plan)
	stage := "end"
	if e.Plan.TerminalType == "dtmf_menu" {
		stage = "gather"
	}
	if row.CarrierSlug == "telnyx" && (prompt != "" || stage == "gather") {
		if prompt != "" {
			if row.AnnouncementState == "finished" {
				return a.completeRoutingEffect(e.ID)
			}
			if row.AnnouncementState == "finishing" {
				stage = "hangup"
			} else if row.AnnouncementState == "speaking" {
				_, err = a.db().db.Exec(`UPDATE routing_effects SET next_attempt_at=? WHERE id=?`, ringTime(time.Now().Add(15*time.Second)), e.ID)
				return err
			} else {
				stage = "speak"
			}
			if row.AnnouncementState == "" {
				_, err = a.db().db.Exec(`UPDATE calls SET announcement_state='awaiting_answer',announcement_text=?,handling_reason=? WHERE id=? AND status IN ('pending','answered')`, prompt, firstNonEmpty(row.HandlingReason, routeHandlingReason(&e.Plan)), row.ID)
				if err != nil {
					return err
				}
				row.AnnouncementState = "awaiting_answer"
				row.AnnouncementText = prompt
			}
		}
		if stage != "hangup" && row.AnsweredAt == "" && row.Status != "answered" {
			stage = "answer"
		}
	}
	// Command retries have independent budgets per phase. Waiting for speech
	// completion does not consume that budget.
	if stage != e.Stage {
		e.Attempts = 0
	}
	if e.Attempts >= routingEffectAttempts {
		_, err = a.db().db.Exec(`UPDATE routing_effects SET status='failed',last_error='carrier action retry limit exceeded',updated_at=? WHERE id=?`, ringTime(time.Now()), e.ID)
		if err != nil {
			return err
		}
		_, err = a.db().db.Exec(`UPDATE calls SET routing_resolution='routing_error',state_expires_at=?,error_message='carrier action retry limit exceeded' WHERE id=? AND status='pending'`, ringTime(time.Now()), row.ID)
		ctx.Emit("telephony.routing.error", map[string]any{"call_id": row.ID, "reason": "carrier_action_retry_exhausted", "stage": stage})
		return err
	}
	_, err = a.db().db.Exec(`UPDATE routing_effects SET stage=?,attempts=?,next_attempt_at=?,updated_at=? WHERE id=?`, stage, e.Attempts+1, ringTime(time.Now().Add(15*time.Second)), ringTime(time.Now()), e.ID)
	if err != nil {
		return err
	}
	switch {
	case isSuppressedHandlingReason(row.HandlingReason):
		err = a.rejectInboundCarrierCall(ctx, row)
		if err == nil {
			err = a.db().updateStatus(row.ID, "canceled", row.ErrorMessage)
		}
	case row.CarrierSlug == "telnyx" && stage == "answer":
		err = a.answerTelnyxIVR(ctx, row)
	case row.CarrierSlug == "telnyx" && stage == "speak":
		err = a.startTelnyxTerminalAnnouncement(ctx, row)
	case row.CarrierSlug == "telnyx" && stage == "hangup":
		err = a.finishTelnyxTerminalAnnouncement(ctx, row)
	case row.CarrierSlug == "telnyx" && stage == "gather":
		err = a.startTelnyxGather(ctx, row, &e.Plan)
	case prompt != "" && row.CarrierSlug == "twilio":
		return a.completeRoutingEffect(e.ID) // next XML callback renders the pinned plan
	case prompt != "" && row.CarrierSlug == "bandwidth":
		_, err = a.db().db.Exec(`UPDATE calls SET announcement_state='awaiting_redirect',announcement_text=?,handling_reason=? WHERE id=?`, prompt, firstNonEmpty(row.HandlingReason, routeHandlingReason(&e.Plan)), row.ID)
		if err == nil {
			_, err = executeCarrierTool(ctx, row.CarrierConnectionID, "update_call", map[string]any{"callId": row.CarrierSID, "state": "active", "redirectUrl": a.bandwidthWaitURL(route, row.ID), "redirectMethod": "POST"})
		}
	default:
		if prompt != "" {
			err = errors.New("carrier cannot execute terminal announcement")
		} else {
			err = a.endRoutingCall(ctx, row, &e.Plan)
		}
	}
	if err != nil {
		// Persist only the category here. Carrier responses can contain customer data.
		_, saveErr := a.db().db.Exec(`UPDATE routing_effects SET last_error='carrier action failed',next_attempt_at=?,updated_at=? WHERE id=?`, ringTime(time.Now().Add(time.Duration(1<<min(e.Attempts, 4))*time.Second)), ringTime(time.Now()), e.ID)
		if saveErr != nil {
			return saveErr
		}
		return err
	}
	if stage == "answer" || stage == "speak" {
		return nil
	}
	return a.completeRoutingEffect(e.ID)
}

func (a *App) completeRoutingEffect(id string) error {
	_, err := a.db().db.Exec(`UPDATE routing_effects SET status='done',last_error='',updated_at=? WHERE id=?`, ringTime(time.Now()), id)
	return err
}

func (a *App) endRoutingCall(ctx *sdk.AppCtx, row *callRow, plan *inboundRoutingPlan) error {
	reason := firstNonEmpty(plan.RoutingResolution, row.RoutingResolution, "routing_completed")
	if plan.TerminalType == "reject" && plan.RoutingResolution == "" && row.RoutingResolution == "" {
		reason = "routing_rejected"
	}
	if _, err := a.db().db.Exec(`UPDATE calls SET routing_resolution=? WHERE id=? AND status IN ('pending','answered')`, reason, row.ID); err != nil {
		return err
	}
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
		if err = carrier.Hangup(ctx, row); err != nil {
			return err
		}
	}
	if err := a.killCallThread(ctx, row); err != nil {
		return err
	}
	_, err := a.db().updateStatusWithFacts(row.ID, "no-answer", "", lifecycleFacts{Source: "telephony", TerminationInitiator: "telephony"})
	return err
}

func (a *App) finishTerminalRoutingPlan(ctx *sdk.AppCtx, row *callRow, route *routeRow, plan *inboundRoutingPlan) error {
	if row == nil || plan == nil {
		return errors.New("terminal routing plan unavailable")
	}
	if a.callUsesDirectSIP(row) && terminalAnnouncementText(plan) != "" {
		return fmt.Errorf("direct SIP cannot play a terminal routing announcement")
	}
	if err := a.ensureRoutingEffect(row, plan); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("app context unavailable for terminal routing")
	}
	return a.driveRoutingEffect(ctx.WithProject(row.ProjectID), row.ID, plan.NodeID)
}

// Calls admitted by older releases may have announcement state but no durable
// effect. Their stored text is enough to resume safely without changing flows.
func (a *App) legacyAnnouncementPlan(row *callRow) (*inboundRoutingPlan, error) {
	node := "legacy-terminal"
	var current string
	err := a.db().db.QueryRow(`SELECT current_node_id FROM call_route_executions WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if current != "" {
		node = current
	}
	route := routeRow{ID: row.RouteID, ProjectID: row.ProjectID, CarrierSlug: row.CarrierSlug, CarrierConnectionID: row.CarrierConnectionID, PhoneNumber: row.ToNumber, InboundTransport: inboundTransportProgrammable}
	def := routingDefinition{Entry: "legacy-notice", Nodes: []routingNode{{ID: "legacy-notice", Type: "announcement", Config: map[string]any{"text": row.AnnouncementText}, Next: node}, {ID: node, Type: "hangup"}}}
	raw, err := json.Marshal(routingExecutionContext{Route: route, Definition: def})
	if err != nil {
		return nil, err
	}
	return &inboundRoutingPlan{FlowID: row.RoutingFlowID, VersionID: row.RoutingFlowVersionID, NodeID: node, TerminalType: "hangup", ContextJSON: string(raw), Trace: []routingTraceStep{{NodeID: "legacy-notice", NodeType: "announcement", Outcome: "play"}, {NodeID: node, NodeType: "hangup", Outcome: "hangup"}}}, nil
}

func (a *App) recoverLegacyAnnouncementEffects(ctx *sdk.AppCtx) error {
	calls, err := a.db().listWhere(`project_id=? AND carrier_slug='telnyx' AND status IN ('pending','answered') AND announcement_state IN ('awaiting_answer','speaking','finishing') AND announcement_text<>'' AND NOT EXISTS(SELECT 1 FROM routing_effects e WHERE e.call_id=calls.id) LIMIT 20`, ctx.CurrentProject())
	if err != nil {
		return err
	}
	for i := range calls {
		plan, err := a.legacyAnnouncementPlan(&calls[i])
		if err != nil {
			return err
		}
		if err = a.ensureRoutingEffect(&calls[i], plan); err != nil {
			return err
		}
	}
	return nil
}
