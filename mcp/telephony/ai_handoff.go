package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// This means the durable coordinator owns recovery, not that the caller may
// immediately spawn again. XML transports wait for the committed fallback.
var errAIHandoffPending = errors.New("AI handoff recovery is pending")

var aiRecoverySlots = make(chan struct{}, 32)

// Slow platform requests cannot block deadline/fallback processing for every
// other call in the project. Admission is bounded and coalesced per call.
func (a *App) launchAIRecovery(key string, run func()) bool {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.decisionStopping || a.aiRecovering[key] {
		return false
	}
	select {
	case aiRecoverySlots <- struct{}{}:
	default:
		return false
	}
	if a.aiRecovering == nil {
		a.aiRecovering = map[string]bool{}
	}
	a.aiRecovering[key] = true
	a.decisionWG.Add(1)
	go func() {
		defer a.decisionWG.Done()
		defer func() { a.dispatchMu.Lock(); delete(a.aiRecovering, key); a.dispatchMu.Unlock(); <-aiRecoverySlots }()
		run()
	}()
	return true
}

type aiHandoff struct {
	Status, Deadline, Next, Owner, Code, Directive, Voice, Greeting string
	Node                                                            string
	AgentID                                                         int64
	Attempts, MaxAttempts, FallbackApplied                          int
}

func (a *App) aiHandoff(call string) (*aiHandoff, error) {
	h := &aiHandoff{}
	err := a.db().db.QueryRow(`SELECT status,deadline_at,next_attempt_at,owner,error_code,attempts,max_attempts,fallback_applied,directive,voice,greeting,node_id,agent_id FROM ai_handoffs WHERE call_id=?`, call).Scan(&h.Status, &h.Deadline, &h.Next, &h.Owner, &h.Code, &h.Attempts, &h.MaxAttempts, &h.FallbackApplied, &h.Directive, &h.Voice, &h.Greeting, &h.Node, &h.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

// Detail-only diagnostics: no per-call joins on the frequently polled list and
// no provider error prose, credentials, directives or bridge URLs in the API.
func (a *App) aiHandoffPublic(call string) (map[string]any, error) {
	h, err := a.aiHandoff(call)
	if err != nil || h == nil {
		return nil, err
	}
	rows, err := a.db().db.Query(`SELECT attempt,outcome,error_code,started_at,completed_at FROM ai_handoff_attempts WHERE call_id=? ORDER BY attempt`, call)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := []map[string]any{}
	for rows.Next() {
		var number int
		var outcome, code, start, end string
		if err = rows.Scan(&number, &outcome, &code, &start, &end); err != nil {
			return nil, err
		}
		attempts = append(attempts, map[string]any{"attempt": number, "outcome": outcome, "error_code": code, "started_at": start, "completed_at": end})
	}
	return map[string]any{"status": h.Status, "attempt_count": h.Attempts, "max_attempts": h.MaxAttempts, "deadline_at": h.Deadline, "next_attempt_at": h.Next, "error_code": h.Code, "fallback_applied": h.FallbackApplied != 0, "attempts": attempts}, rows.Err()
}

func (a *App) aiHandoffAdmission(call string) error {
	h, err := a.aiHandoff(call)
	if err != nil || h == nil {
		return err
	}
	if h.Status == "failed" || h.Status == "canceled" {
		if h.Status == "failed" && h.FallbackApplied != 0 {
			// A permitted fallback can later reach a different AI node through
			// a decision/menu. Retire that node too, without resetting the budget
			// or recursively following another AI failure branch.
			_, err = a.db().db.Exec(`UPDATE ai_handoffs SET fallback_applied=0,error_code='ai_budget_already_failed',owner='',agent_id=(SELECT agent_id FROM calls WHERE id=?),node_id=COALESCE((SELECT current_node_id FROM call_route_executions WHERE call_id=?),'') WHERE call_id=? AND status='failed' AND fallback_applied=1 AND node_id<>COALESCE((SELECT current_node_id FROM call_route_executions WHERE call_id=?),'') AND EXISTS(SELECT 1 FROM calls WHERE id=? AND status='pending' AND peer_kind='realtime')`, call, call, call, call, call)
			if err != nil {
				return err
			}
		}
		return errAIHandoffPending
	}
	if h.Status == "running" || (h.Status == "retry" && h.Next > ringTime(time.Now())) {
		return errAnswerPreparationInProgress
	}
	if h.Status != "ready" && (h.Attempts >= h.MaxAttempts || h.Deadline <= ringTime(time.Now())) {
		return errAIHandoffPending
	}
	return nil
}

// Attach and mark ready in one transaction. A deadline worker must never
// observe an attached thread whose journal still says startup is running.
func (a *App) attachAIHandoff(row *callRow, token, thread, bridge, directive, voice string) (bool, error) {
	tx, err := a.db().db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE calls SET thread_id=?,audio_bridge_url=?,directive=?,voice=? WHERE id=? AND status='answering' AND thread_id=? AND EXISTS(SELECT 1 FROM ai_handoffs WHERE call_id=? AND owner=? AND status='running' AND deadline_at>?)`, thread, bridge, strings.TrimSpace(directive), voice, row.ID, token, row.ID, token, ringTime(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE ai_handoffs SET status='ready' WHERE call_id=? AND owner=? AND status='running'`, row.ID, token); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE ai_handoff_attempts SET outcome='ready',completed_at=? WHERE call_id=? AND thread_id=?`, ringTime(time.Now()), row.ID, thread); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Called after the existing offer/identity claim. The attempt and its unique
// owner are committed before invoking Core, so a crash cannot reset the budget.
func (a *App) beginAIHandoff(ctx *sdk.AppCtx, row *callRow, token, directive, voice, greeting string) error {
	now := time.Now().UTC()
	seconds := max(1, boundedBurstSetting(ctx.Config(), "ai_startup_timeout_seconds", 15, 120))
	maximum := max(1, boundedBurstSetting(ctx.Config(), "ai_startup_max_attempts", 3, 5))
	deadline := now.Add(time.Duration(seconds) * time.Second)
	for _, raw := range []string{row.StateExpiresAt, row.DeadlineAt} {
		if d, err := time.Parse(time.RFC3339Nano, raw); err == nil && d.Before(deadline) {
			deadline = d
		}
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT OR IGNORE INTO ai_handoffs(call_id,project_id,max_attempts,deadline_at,next_attempt_at,directive,voice,greeting) VALUES(?,?,?,?,?,?,?,?)`, row.ID, row.ProjectID, maximum, ringTime(deadline), ringTime(now), directive, voice, greeting)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE ai_handoffs SET status='running',attempts=attempts+1,owner=?,agent_id=?,node_id=COALESCE((SELECT current_node_id FROM call_route_executions WHERE call_id=?),''),error_code='' WHERE call_id=? AND status IN ('retry','ready') AND attempts<max_attempts AND deadline_at>? AND next_attempt_at<=? AND EXISTS(SELECT 1 FROM calls WHERE id=? AND status='answering' AND thread_id=?)`, token, row.AgentID, row.ID, row.ID, ringTime(now), ringTime(now), row.ID, token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		if _, err = tx.Exec(`UPDATE ai_handoffs SET status='failed',error_code='startup_deadline' WHERE call_id=? AND status IN ('retry','ready') AND (deadline_at<=? OR attempts>=max_attempts)`, row.ID, ringTime(now)); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return errAIHandoffPending
	}
	_, err = tx.Exec(`INSERT INTO ai_handoff_attempts(call_id,attempt,thread_id,started_at) SELECT call_id,attempts,?,? FROM ai_handoffs WHERE call_id=?`, "tel-"+strings.TrimPrefix(token, "pending-"), ringTime(now), row.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// The current SDK preserves HTTP failures as a fixed envelope rather than a
// typed error. Decode only that envelope and the server's nested Core status;
// never infer retryability from provider-specific prose. Unknown failures are
// stopped conservatively (including ambiguous timeouts after Core accepted work).
var aiPlatformStatus = regexp.MustCompile(`^platform /api/apps/callback/threads/spawn-realtime: http ([0-9]{3}): ([\s\S]*)$`)
var aiCoreStatus = regexp.MustCompile(`^spawn realtime thread "[^"\n]+": HTTP ([0-9]{3}) `)

func aiStartupFailure(err error) (code string, retry bool) {
	var statusError interface{ HTTPStatusCode() int }
	status := 0
	if errors.As(err, &statusError) {
		status = statusError.HTTPStatusCode()
	}
	for cause := err; cause != nil && status == 0; cause = errors.Unwrap(cause) {
		if m := aiPlatformStatus.FindStringSubmatch(cause.Error()); m != nil {
			status, _ = strconv.Atoi(m[1])
			if nested := aiCoreStatus.FindStringSubmatch(m[2]); nested != nil {
				status, _ = strconv.Atoi(nested[1])
			}
		}
	}
	switch status {
	case 429, 503:
		return "temporarily_unavailable", true
	case 400, 401, 403, 404, 422:
		return "configuration_or_access", false
	default:
		return "startup_failed_or_uncertain", false
	}
}

func (a *App) failAIHandoff(row *callRow, token, code string, retry bool) error {
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var attempts, maximum int
	var deadline string
	err = tx.QueryRow(`SELECT attempts,max_attempts,deadline_at FROM ai_handoffs WHERE call_id=? AND owner=? AND status='running'`, row.ID, token).Scan(&attempts, &maximum, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now()
	next := now.Add(time.Duration(1<<min(attempts, 4)) * time.Second)
	status := "failed"
	finalCode := code
	if retry && attempts < maximum && ringTime(next) < deadline {
		status = "retry"
	} else if retry && attempts >= maximum {
		finalCode = "retry_exhausted"
	} else if retry {
		finalCode = "startup_deadline"
	}
	_, err = tx.Exec(`UPDATE ai_handoffs SET status=?,error_code=?,next_attempt_at=? WHERE call_id=? AND owner=?`, status, finalCode, ringTime(next), row.ID, token)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE ai_handoff_attempts SET outcome='failed',error_code=?,completed_at=? WHERE call_id=? AND attempt=?`, code, ringTime(now), row.ID, attempts)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Run independently from decision delivery: direct/legacy routes also need
// bounded recovery, and an in-flight spawn must not hold the caller indefinitely.
func (a *App) runAIHandoffs(_ context.Context, ctx *sdk.AppCtx) error {
	now := ringTime(time.Now())
	rows, err := a.db().db.Query(`SELECT call_id,status='retry' AND deadline_at>? FROM ai_handoffs WHERE project_id=? AND ((status='running' AND deadline_at<=?) OR (status='retry' AND (next_attempt_at<=? OR deadline_at<=?)) OR (status='failed' AND fallback_applied=0)) ORDER BY status='retry' AND deadline_at>?,next_attempt_at LIMIT 100`, now, ctx.CurrentProject(), now, now, now, now)
	if err != nil {
		return err
	}
	type work struct {
		id    string
		retry bool
	}
	var calls []work
	for rows.Next() {
		var item work
		if err = rows.Scan(&item.id, &item.retry); err != nil {
			rows.Close()
			return err
		}
		calls = append(calls, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range calls {
		if item.retry {
			a.launchAIRecovery(item.id, func() {
				if err := a.driveAIHandoff(ctx, item.id); err != nil {
					ctx.Logger().Warn("AI handoff retry", "call", item.id, "err", err)
				}
			})
		} else if err = a.driveAIHandoff(ctx, item.id); err != nil {
			ctx.Logger().Warn("AI handoff recovery", "call", item.id, "err", err)
		}
	}
	return nil
}

func (a *App) driveAIHandoff(ctx *sdk.AppCtx, id string) error {
	h, err := a.aiHandoff(id)
	if err != nil || h == nil {
		return err
	}
	row, err := a.db().findCall(id)
	if err != nil {
		return err
	}
	if row == nil || isTerminalStatus(row.Status) || row.MediaConnectedAt != "" {
		_, err = a.db().db.Exec(`UPDATE ai_handoffs SET status='canceled' WHERE call_id=? AND status<>'ready'`, id)
		return err
	}
	if h.Status == "running" || h.Status == "retry" {
		if h.Deadline <= ringTime(time.Now()) {
			// Fencing the owner before releasing its claim prevents late attach.
			_, err = a.db().db.Exec(`UPDATE ai_handoffs SET status='failed',error_code='startup_deadline' WHERE call_id=? AND status IN ('running','retry')`, id)
			if err != nil {
				return err
			}
			h, err = a.aiHandoff(id)
			if err != nil {
				return err
			}
		} else if h.Status == "running" || h.Next > ringTime(time.Now()) {
			return nil
		}
	}
	if h.Status == "failed" {
		if err = a.db().releaseRealtimePreparation(id, h.Owner); err != nil {
			return err
		}
		return a.applyAIHandoffFallback(ctx, row, h)
	}
	if h.Status != "retry" {
		return nil
	}
	// Pull transports resume on their authenticated wait callbacks. Push
	// transports need a worker after a transient failure even without a decision.
	if row.CarrierSlug == "twilio" || row.CarrierSlug == "plivo" {
		return nil
	}
	defer lockRoutingCall(id)()
	// Reload under the routing lock: a fallback or another selection may have
	// committed since the recovery worker collected this candidate.
	h, err = a.aiHandoff(id)
	if err != nil || h == nil {
		return err
	}
	if h.Status != "retry" {
		return nil
	}
	row, err = a.db().findCall(id)
	if err != nil || row == nil {
		return err
	}
	if row.Status != "pending" || row.PeerKind == peerKindHuman {
		return nil
	}
	_, plan, err := a.routingPlanForCall(row, nil)
	if err != nil {
		return err
	}
	if row.CarrierSlug == "telnyx" && plan != nil {
		var ex routingExecutionContext
		if err = json.Unmarshal([]byte(plan.ContextJSON), &ex); err != nil {
			return err
		}
		return a.executeTelnyxRoutingPlan(ctx, row, &ex.Route, plan)
	}
	_, err = a.answerCall(ctx, row, h.Directive, h.Voice, h.Greeting, true)
	return err
}

func (a *App) aiFailurePlan(row *callRow, allowBranch bool) (*inboundRoutingPlan, error) {
	route, err := a.db().findRoute(row.RouteID)
	if err != nil {
		return nil, err
	}
	if route == nil {
		route = &routeRow{ID: row.RouteID, ProjectID: row.ProjectID, CarrierSlug: row.CarrierSlug, CarrierConnectionID: row.CarrierConnectionID}
	}
	var raw, node string
	err = a.db().db.QueryRow(`SELECT context_json,current_node_id FROM call_route_executions WHERE call_id=?`, row.ID).Scan(&raw, &node)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var ex routingExecutionContext
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &ex); err != nil {
			return nil, err
		}
		route = &ex.Route
	}
	for _, n := range ex.Definition.Nodes {
		if allowBranch && n.ID == node && n.Branches["ai_startup_failed"] != "" {
			plan, e := a.resolveRoutingDefinition(route, row.FromNumber, ex.Digits, &routingFlowVersionRow{ID: row.RoutingFlowVersionID, FlowID: row.RoutingFlowID}, ex.Definition, n.Branches["ai_startup_failed"])
			// A failed call-wide AI budget cannot be reset by another graph node.
			// An invalid legacy branch also uses the safe terminal fallback.
			if e == nil && aiFallbackCanRun(plan) {
				plan.RoutingResolution = "ai_startup_failed"
				return plan, nil
			}
		}
	}
	// Existing flows need no migration or new branch to fail safely.
	ex.Route = *route
	ex.Definition = routingDefinition{Entry: "ai-startup-failed", Nodes: []routingNode{{ID: "ai-startup-failed", Type: "hangup"}}}
	encoded, err := json.Marshal(ex)
	if err != nil {
		return nil, err
	}
	return &inboundRoutingPlan{FlowID: row.RoutingFlowID, VersionID: row.RoutingFlowVersionID, NodeID: "ai-startup-failed", TerminalType: "hangup", RoutingResolution: "ai_startup_failed", ContextJSON: string(encoded)}, nil
}

func aiFallbackCanRun(plan *inboundRoutingPlan) bool {
	if plan.Group != nil {
		eligible := 0
		for i, member := range plan.Group.Members {
			dest := plan.GroupDestinations[member.DestinationID]
			if dest.Kind == "ai" || dest.Kind == "agent" {
				plan.Group.Members[i].Enabled = false
				delete(plan.GroupDestinations, member.DestinationID)
			} else if member.Enabled {
				eligible++
			}
		}
		return eligible > 0
	}
	return plan.TerminalType != "destination" || plan.AnswerMode == answerModeHumanBrowser
}

func (a *App) applyAIHandoffFallback(ctx *sdk.AppCtx, row *callRow, h *aiHandoff) error {
	defer lockRoutingCall(row.ID)()
	unlock := a.softphones.lockClaim(row.ID)
	defer unlock()
	plan, err := a.aiFailurePlan(row, h.Code != "ai_budget_already_failed")
	if err != nil {
		return err
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanCall(tx.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id=?`, row.ID))
	if err != nil {
		return err
	}
	if isTerminalStatus(current.Status) || current.MediaConnectedAt != "" || (current.Status == "answering" && current.ThreadID != h.Owner) {
		return nil
	}
	res, err := tx.Exec(`UPDATE ai_handoffs SET fallback_applied=1 WHERE call_id=? AND status='failed' AND fallback_applied=0`, row.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	_, err = tx.Exec(`UPDATE ai_handoff_attempts SET outcome='failed',error_code=?,completed_at=? WHERE call_id=? AND outcome='started'`, h.Code, ringTime(time.Now()), row.ID)
	if err != nil {
		return err
	}
	var currentNode string
	if err = tx.QueryRow(`SELECT COALESCE((SELECT current_node_id FROM call_route_executions WHERE call_id=?),'')`, row.ID).Scan(&currentNode); err != nil {
		return err
	}
	var otherOffers int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM call_offers WHERE call_id=? AND kind IN ('browser','pstn','sip') AND status IN ('offered','queued','claimed')`, row.ID).Scan(&otherOffers); err != nil {
		return err
	}
	if currentNode != h.Node || current.PeerKind == peerKindHuman || current.AgentID != h.AgentID || otherOffers > 0 {
		// The normal routing loop already has another eligible destination. A
		// failed AI must not cancel an adviser offer or rewind a newer node.
		return tx.Commit()
	}
	_, err = tx.Exec(`UPDATE calls SET status='pending',thread_id=?,audio_bridge_url='pending' WHERE id=? AND status='answering' AND thread_id=?`, "pending-ai-failed-"+row.ID, row.ID, h.Owner)
	if err != nil {
		return err
	}
	// Retire the old AI offer/reservation before committing a replacement route.
	_, err = tx.Exec(`UPDATE call_offers SET status='failed' WHERE call_id=? AND status IN ('offered','claimed','queued')`, row.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE call_ring_runs SET status='finished' WHERE call_id=? AND status IN ('ringing','claimed','exhausted')`, row.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM phone_capacity WHERE call_id=?`, row.ID); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE routing_decisions SET applied=1 WHERE call_id=?`, row.ID)
	if err != nil {
		return err
	}
	if err = writeRoutingProgressTx(tx, row.ID, row.ProjectID, plan); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.routingCommitted(row.ProjectID)
	ctx.Logger().Warn("AI startup fallback committed", "call", row.ID, "attempts", h.Attempts, "code", h.Code, "node", plan.NodeID)
	// Late spawns are also fenced and cleaned by runInboundPreparation.
	if h.Owner != "" && (h.Code == "startup_deadline" || h.Code == "cleanup_uncertain") {
		a.launchAIRecovery("cleanup/"+row.ID, func() {
			if err := ctx.PlatformAPI().KillThread(h.AgentID, "tel-"+strings.TrimPrefix(h.Owner, "pending-")); err != nil {
				ctx.Logger().Warn("AI startup cleanup failed", "call", row.ID)
			}
		})
	}
	return nil
}
