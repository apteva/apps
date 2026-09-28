package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A published node pins policy and function ID. Functions intentionally executes
// that function's active version, so business rules can evolve independently.
type decisionConfig struct {
	FunctionID      int64          `json:"function_id"`
	TimeoutMS       int            `json:"timeout_ms"`
	RingTimeout     int            `json:"ring_timeout_seconds"`
	Destinations    []string       `json:"destination_ids"`
	Variables       map[string]any `json:"variables,omitempty"`
	MaxAttempts     int            `json:"max_attempts,omitempty"`
	TotalWaitSec    int            `json:"total_wait_seconds,omitempty"`
	FunctionRetries int            `json:"function_retry_limit,omitempty"`
	RetryDelaySec   int            `json:"retry_delay_seconds,omitempty"`
	AllowRepeat     bool           `json:"allow_repeat,omitempty"`
	CallbackOnAI    bool           `json:"callback_on_ai,omitempty"`
}
type decisionResponse struct {
	DecisionID    string `json:"decision_id"`
	Action        string `json:"action"`
	DestinationID string `json:"destination_id,omitempty"`
	ReservationID string `json:"reservation_id,omitempty"`
	RingTimeout   int    `json:"ring_timeout_seconds,omitempty"`
	RetryAfterSec int    `json:"retry_after_seconds,omitempty"`
}
type decisionRecord struct {
	ID              string          `json:"decision_id"`
	CallID          string          `json:"call_id"`
	ProjectID       string          `json:"project_id"`
	VersionID       string          `json:"flow_version_id"`
	NodeID          string          `json:"node_id"`
	Status          string          `json:"status"`
	Request         json.RawMessage `json:"request"`
	Result          json.RawMessage `json:"result"`
	Reason          string          `json:"reason"`
	CreatedAt       string          `json:"created_at"`
	DeadlineAt      string          `json:"deadline_at"`
	CompletedAt     string          `json:"completed_at"`
	Applied         bool            `json:"applied"`
	PlanJSON        string          `json:"-"`
	StartedAt       string          `json:"started_at"`
	NotBefore       string          `json:"not_before,omitempty"`
	DispatchDelayMS int64           `json:"dispatch_delay_ms"`
	DurationMS      int64           `json:"duration_ms"`
}

const decisionColumns = `id,call_id,project_id,flow_version_id,node_id,status,request_json,result_json,reason,created_at,deadline_at,completed_at,applied,plan_json,started_at,not_before`

func scanDecision(s interface{ Scan(...any) error }) (decisionRecord, error) {
	var d decisionRecord
	var req, res string
	err := s.Scan(&d.ID, &d.CallID, &d.ProjectID, &d.VersionID, &d.NodeID, &d.Status, &req, &res, &d.Reason, &d.CreatedAt, &d.DeadlineAt, &d.CompletedAt, &d.Applied, &d.PlanJSON, &d.StartedAt, &d.NotBefore)
	d.Request = json.RawMessage(req)
	d.Result = json.RawMessage(res)
	if start, e := time.Parse(time.RFC3339Nano, d.CreatedAt); e == nil {
		if end, e := time.Parse(time.RFC3339Nano, d.CompletedAt); e == nil {
			d.DurationMS = max(0, end.Sub(start).Milliseconds())
		}
	}
	if start, e := time.Parse(time.RFC3339Nano, d.CreatedAt); e == nil {
		if dispatched, e := time.Parse(time.RFC3339Nano, d.StartedAt); e == nil {
			d.DispatchDelayMS = max(0, dispatched.Sub(start).Milliseconds())
		}
	}
	return d, err
}
func parseDecisionConfig(n routingNode) (decisionConfig, error) {
	c := decisionConfig{TimeoutMS: 2000, RingTimeout: 20, MaxAttempts: 1, TotalWaitSec: 300, RetryDelaySec: 2}
	raw, e := json.Marshal(n.Config)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(raw, &c); e != nil {
		return c, e
	}
	if c.FunctionID <= 0 || c.TimeoutMS < 100 || c.TimeoutMS > 5000 || c.RingTimeout < 5 || c.RingTimeout > 60 || len(c.Destinations) == 0 || len(c.Destinations) > 100 || n.Branches["fallback"] == "" {
		return c, errors.New("requires function_id, 1–100 destination_ids, fallback branch, timeout_ms 100–5000 and ring_timeout_seconds 5–60")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 100 || c.TotalWaitSec < 5 || c.TotalWaitSec > 1800 || c.FunctionRetries < 0 || c.FunctionRetries > 5 || c.RetryDelaySec < 1 || c.RetryDelaySec > 30 {
		return c, errors.New("max_attempts must be 1–100, total_wait_seconds 5–1800, function_retry_limit 0–5 and retry_delay_seconds 1–30")
	}
	seen := map[string]bool{}
	for _, id := range c.Destinations {
		if !routingIDPattern.MatchString(id) || seen[id] {
			return c, errors.New("destination_ids must be valid and unique")
		}
		seen[id] = true
	}
	if len(raw) > 16384 {
		return c, errors.New("decision configuration exceeds 16 KiB")
	}
	return c, nil
}
func decisionTargetAllowed(n routingNode, id string) bool {
	c, e := parseDecisionConfig(n)
	if e != nil {
		return false
	}
	for _, v := range c.Destinations {
		if v == id {
			return true
		}
	}
	return false
}
func decisionNode(ex routingExecutionContext, id string) (routingNode, error) {
	id, _ = decisionRuntimeNode(id)
	for _, n := range ex.Definition.Nodes {
		if n.ID == id && n.Type == "decision" {
			return n, nil
		}
	}
	return routingNode{}, errors.New("decision node unavailable")
}

// Repeated attempts use distinct durable node identities. Published node IDs
// cannot contain '#', so the suffix cannot collide with a real flow node.
func decisionRuntimeNode(id string) (string, int) {
	base, suffix, ok := strings.Cut(id, "#")
	if !ok {
		return id, 1
	}
	attempt, err := strconv.Atoi(suffix)
	if err != nil || attempt < 2 {
		return id, 1
	}
	return base, attempt
}
func decisionAttemptNode(base string, attempt int) string {
	if attempt <= 1 {
		return base
	}
	return base + "#" + strconv.Itoa(attempt)
}

// Keep the caller leg alive for the configured policy budget, including a
// small margin for the final durable fallback transition. Replays reuse the
// first attempt's timestamp rather than extending the budget.
func decisionCallExpiryTx(tx *sql.Tx, callID string, plan *inboundRoutingPlan, now time.Time) (time.Time, error) {
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(plan.ContextJSON), &ex); err != nil {
		return time.Time{}, err
	}
	node, err := decisionNode(ex, plan.NodeID)
	if err != nil {
		return time.Time{}, err
	}
	cfg, err := parseDecisionConfig(node)
	if err != nil {
		return time.Time{}, err
	}
	base, _ := decisionRuntimeNode(plan.NodeID)
	var firstAt string
	if err := tx.QueryRow(`SELECT created_at FROM routing_decisions WHERE call_id=? AND node_id=?`, callID, base).Scan(&firstAt); err != nil && err != sql.ErrNoRows {
		return time.Time{}, err
	}
	started := now
	if firstAt != "" {
		started, err = time.Parse(time.RFC3339Nano, firstAt)
		if err != nil {
			return time.Time{}, err
		}
	}
	expires := started.Add(time.Duration(cfg.TotalWaitSec)*time.Second + 10*time.Second)
	if cfg.MaxAttempts == 1 {
		// Preserve the existing short safety expiry for one-shot flows.
		expires = started.Add(max(time.Duration(plan.TimeoutSec)*time.Second, time.Duration(cfg.TimeoutMS)*time.Millisecond+5*time.Second))
	}
	if expires.Before(now.Add(5 * time.Second)) {
		expires = now.Add(5 * time.Second)
	}
	return expires, nil
}

func (a *App) nextDecisionPlan(row *callRow, d decisionRecord, delay time.Duration) (*inboundRoutingPlan, bool, error) {
	var raw string
	if err := a.db().db.QueryRow(`SELECT context_json FROM call_route_executions WHERE call_id=?`, row.ID).Scan(&raw); err != nil {
		return nil, false, err
	}
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(raw), &ex); err != nil {
		return nil, false, err
	}
	n, err := decisionNode(ex, d.NodeID)
	if err != nil {
		return nil, false, err
	}
	cfg, err := parseDecisionConfig(n)
	if err != nil {
		return nil, false, err
	}
	base, attempt := decisionRuntimeNode(d.NodeID)
	var firstAt string
	if err := a.db().db.QueryRow(`SELECT created_at FROM routing_decisions WHERE call_id=? AND node_id=?`, row.ID, base).Scan(&firstAt); err != nil {
		return nil, false, err
	}
	started, err := time.Parse(time.RFC3339Nano, firstAt)
	if err != nil {
		return nil, false, err
	}
	remaining := started.Add(time.Duration(cfg.TotalWaitSec) * time.Second).Sub(time.Now())
	if attempt >= cfg.MaxAttempts || remaining <= delay || remaining <= 0 {
		plan, err := a.resolveRoutingDefinition(&ex.Route, row.FromNumber, ex.Digits, &routingFlowVersionRow{ID: d.VersionID, FlowID: row.RoutingFlowID}, ex.Definition, n.Branches["fallback"])
		if err == nil {
			plan.RoutingResolution = "routing_exhausted"
			plan.CallbackOnAI = cfg.CallbackOnAI
		}
		return plan, false, err
	}
	plan, err := a.resolveRoutingDefinition(&ex.Route, row.FromNumber, ex.Digits, &routingFlowVersionRow{ID: d.VersionID, FlowID: row.RoutingFlowID}, ex.Definition, base)
	if err != nil {
		return nil, false, err
	}
	plan.NodeID = decisionAttemptNode(base, attempt+1)
	if delay > 0 {
		plan.DecisionNotBefore = ringTime(time.Now().Add(delay))
	}
	return plan, true, nil
}

func (a *App) decisionSettings(d decisionRecord) (decisionConfig, error) {
	var raw string
	if err := a.db().db.QueryRow(`SELECT context_json FROM call_route_executions WHERE call_id=?`, d.CallID).Scan(&raw); err != nil {
		return decisionConfig{}, err
	}
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(raw), &ex); err != nil {
		return decisionConfig{}, err
	}
	n, err := decisionNode(ex, d.NodeID)
	if err != nil {
		return decisionConfig{}, err
	}
	return parseDecisionConfig(n)
}

func (a *App) finalDecisionPlan(row *callRow, d decisionRecord, resolution string, callbackOnAI bool) (*inboundRoutingPlan, error) {
	plan, _, err := a.decisionPlan(row, d, decisionResponse{Action: "fallback"})
	if err != nil {
		return nil, err
	}
	plan.RoutingResolution = resolution
	plan.CallbackOnAI = callbackOnAI
	return plan, nil
}

func (a *App) priorFunctionFailures(callID, base, currentID string) (int, error) {
	rows, err := a.db().db.Query(`SELECT node_id,reason FROM routing_decisions WHERE call_id=? AND id<>? ORDER BY created_at DESC,id DESC`, callID, currentID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var node, reason string
		if err := rows.Scan(&node, &reason); err != nil {
			return 0, err
		}
		if candidate, _ := decisionRuntimeNode(node); candidate != base {
			continue
		}
		if reason != "function_failed" && reason != "timed_out" {
			break
		}
		count++
	}
	return count, rows.Err()
}

func enqueueDecisionTx(tx *sql.Tx, callID, project string, plan *inboundRoutingPlan) error {
	if plan == nil || plan.TerminalType != "decision" {
		return nil
	}
	var ex routingExecutionContext
	if e := json.Unmarshal([]byte(plan.ContextJSON), &ex); e != nil {
		return e
	}
	n, e := decisionNode(ex, plan.NodeID)
	if e != nil {
		return e
	}
	cfg, e := parseDecisionConfig(n)
	if e != nil {
		return e
	}
	var from, to string
	if e = tx.QueryRow(`SELECT from_number,to_number FROM calls WHERE id=? AND project_id=?`, callID, project).Scan(&from, &to); e != nil {
		return e
	}
	previous := []map[string]any{}
	rows, e := tx.Query(`SELECT id,status,result_json,reason FROM routing_decisions WHERE call_id=? ORDER BY created_at,id`, callID)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id, status, result, reason string
		if e = rows.Scan(&id, &status, &result, &reason); e != nil {
			rows.Close()
			return e
		}
		previous = append(previous, map[string]any{"decision_id": id, "status": status, "result": json.RawMessage(result), "reason": reason})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	outcomes := []map[string]any{}
	rows, e = tx.Query(`SELECT id,destination_id,status,offered_at,expires_at,acknowledged_at FROM call_offers WHERE call_id=? AND status<>'queued' ORDER BY offered_at,id`, callID)
	if e != nil {
		return e
	}
	for rows.Next() {
		var offerID, dest, status, offered, expires, acknowledged string
		if e = rows.Scan(&offerID, &dest, &status, &offered, &expires, &acknowledged); e != nil {
			rows.Close()
			return e
		}
		outcomes = append(outcomes, map[string]any{"offer_id": offerID, "destination_id": dest, "outcome": status, "offered_at": offered, "expires_at": expires, "acknowledged_at": acknowledged})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	now := time.Now()
	readyAt := now
	if plan.DecisionNotBefore != "" {
		if delayed, parseErr := time.Parse(time.RFC3339Nano, plan.DecisionNotBefore); parseErr != nil {
			return parseErr
		} else if delayed.After(readyAt) {
			readyAt = delayed
		}
	}
	deadline := ringTime(readyAt.Add(time.Duration(cfg.TimeoutMS) * time.Millisecond))
	base, nodeAttempt := decisionRuntimeNode(plan.NodeID)
	var firstAt string
	if e = tx.QueryRow(`SELECT created_at FROM routing_decisions WHERE call_id=? AND node_id=?`, callID, base).Scan(&firstAt); e != nil && e != sql.ErrNoRows {
		return e
	}
	if firstAt == "" {
		firstAt = ringTime(now)
	}
	started, _ := time.Parse(time.RFC3339Nano, firstAt)
	totalDeadline := ringTime(started.Add(time.Duration(cfg.TotalWaitSec) * time.Second))
	if totalDeadline < deadline {
		deadline = totalDeadline
	}
	id := "decision_" + callID + "_" + plan.NodeID
	request, _ := json.Marshal(map[string]any{"schema_version": 2, "decision_id": id, "attempt": len(previous) + 1, "node_attempt": nodeAttempt, "project_id": project, "call_id": callID, "flow_version_id": plan.VersionID, "node_id": base, "caller": from, "called": to, "digits": ex.Digits, "variables": cfg.Variables, "previous_decisions": previous, "previous_offers": outcomes, "deadline_at": deadline, "total_deadline_at": totalDeadline})
	res, e := tx.Exec(`INSERT OR IGNORE INTO routing_decisions(id,call_id,project_id,flow_version_id,node_id,request_json,created_at,deadline_at,not_before) VALUES(?,?,?,?,?,?,?,?,?)`, id, callID, project, plan.VersionID, plan.NodeID, string(request), ringTime(now), deadline, plan.DecisionNotBefore)
	if e != nil {
		return e
	}
	count, e := res.RowsAffected()
	if e != nil || count == 0 {
		return e
	}
	return decisionEventTx(tx, id, "requested", id, "", ringTime(now), nil)
}

func (a *App) acceptedDecisionPlan(row *callRow, node string) (*inboundRoutingPlan, error) {
	var raw string
	e := a.db().db.QueryRow(`SELECT plan_json FROM routing_decisions WHERE call_id=? AND node_id=? AND status='accepted'`, row.ID, node).Scan(&raw)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var plan inboundRoutingPlan
	e = json.Unmarshal([]byte(raw), &plan)
	return &plan, e
}
func (a *App) decisionPlan(row *callRow, d decisionRecord, response decisionResponse) (*inboundRoutingPlan, destinationCapacity, error) {
	var raw string
	if e := a.db().db.QueryRow(`SELECT context_json FROM call_route_executions WHERE call_id=?`, row.ID).Scan(&raw); e != nil {
		return nil, destinationCapacity{}, e
	}
	var ex routingExecutionContext
	if e := json.Unmarshal([]byte(raw), &ex); e != nil {
		return nil, destinationCapacity{}, e
	}
	n, e := decisionNode(ex, d.NodeID)
	if e != nil {
		return nil, destinationCapacity{}, e
	}
	cfg, e := parseDecisionConfig(n)
	if e != nil {
		return nil, destinationCapacity{}, e
	}
	if response.Action != "offer" {
		p, e := a.resolveRoutingDefinition(&ex.Route, row.FromNumber, ex.Digits, &routingFlowVersionRow{ID: d.VersionID, FlowID: row.RoutingFlowID}, ex.Definition, n.Branches["fallback"])
		return p, destinationCapacity{}, e
	}
	if !decisionTargetAllowed(n, response.DestinationID) {
		return nil, destinationCapacity{}, errors.New("destination_not_allowed")
	}
	dest, ok := ex.Definition.Destinations[response.DestinationID]
	if !ok {
		return nil, destinationCapacity{}, errors.New("destination_not_pinned")
	}
	live, e := a.findRoutingDestination(row.ProjectID, dest.ID)
	if e != nil {
		return nil, destinationCapacity{}, e
	}
	if e = a.validateDecisionDestination(row.ProjectID, live); e != nil {
		return nil, destinationCapacity{}, errors.New("destination_unavailable")
	}
	cap, e := readDestinationCapacity(dest.ConfigJSON)
	if e != nil {
		return nil, cap, e
	}
	liveCap, e := readDestinationCapacity(live.ConfigJSON)
	if e != nil || liveCap.Identity != cap.Identity {
		return nil, cap, errors.New("destination_identity_changed")
	}
	current, e := a.capacityForIdentity(row.ProjectID, cap.Identity)
	if e != nil {
		return nil, cap, e
	}
	cap.Limit = min(cap.Limit, current.Limit)
	if cfg.MaxAttempts > 1 && !cfg.AllowRepeat {
		var offered int
		if e = a.db().db.QueryRow(`SELECT COUNT(*) FROM call_offers WHERE call_id=? AND status NOT IN ('queued','failed') AND (destination_id=? OR (capacity_principal<>'' AND capacity_principal=?))`, row.ID, dest.ID, cap.Identity.key()).Scan(&offered); e != nil {
			return nil, cap, e
		}
		if offered > 0 {
			return nil, cap, errors.New("adviser_already_offered")
		}
	}
	timeout := response.RingTimeout
	if timeout == 0 {
		timeout = cfg.RingTimeout
	}
	if timeout < 5 || timeout > cfg.RingTimeout {
		return nil, cap, errors.New("invalid_ring_timeout")
	}
	overflow := n.Branches["fallback"]
	if cfg.MaxAttempts > 1 {
		overflow = d.NodeID
	}
	p := &inboundRoutingPlan{FlowID: row.RoutingFlowID, VersionID: d.VersionID, NodeID: d.NodeID, TerminalType: "ring_group", RingGroupID: d.ID, AnswerMode: answerModeHumanBrowser, TimeoutSec: timeout, HoldPrompt: ex.Route.HoldPrompt, ContextJSON: raw, OverflowNodeID: overflow, GroupDestinations: map[string]routingDestinationRow{dest.ID: dest}}
	p.Group = &ringGroupRow{ID: d.ID, ProjectID: row.ProjectID, Name: "Decision offer", Strategy: "simultaneous", TimeoutSec: timeout, Enabled: true, Members: []ringGroupMemberRow{{DestinationID: dest.ID, TimeoutSec: timeout, Enabled: true}}}
	base, _ := decisionRuntimeNode(d.NodeID)
	p.Trace = []routingTraceStep{{NodeID: base, NodeType: "decision", Outcome: "accepted"}}
	return p, cap, nil
}

// Invocation admission is bounded even when a platform request outlives its
// decision deadline. The worker expires durable work; a late result cannot apply.
var decisionSlots = make(chan struct{}, 32)

func (a *App) runDecisionTick(_ context.Context, ctx *sdk.AppCtx) error {
	project := ctx.CurrentProject()
	if project == "" {
		return nil
	}
	now := ringTime(time.Now())
	rows, e := ctx.AppDB().Query(`SELECT `+decisionColumns+` FROM routing_decisions WHERE project_id=? AND (status IN ('pending','running') OR applied=0) AND (status<>'pending' OR not_before='' OR not_before<=? OR deadline_at<=?) ORDER BY created_at LIMIT 100`, project, now, now)
	if e != nil {
		return e
	}
	ds := []decisionRecord{}
	for rows.Next() {
		d, e := scanDecision(rows)
		if e != nil {
			rows.Close()
			return e
		}
		ds = append(ds, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, d := range ds {
		if d.Status != "pending" && d.Status != "running" {
			if e = a.deliverDecision(ctx, d); e != nil {
				ctx.Logger().Warn("routing decision delivery", "decision", d.ID, "err", e)
			}
			continue
		}
		row, err := a.db().findCall(d.CallID)
		if err != nil {
			return err
		}
		deadline, _ := time.Parse(time.RFC3339Nano, d.DeadlineAt)
		if row == nil || row.Status != "pending" || !time.Now().Before(deadline) {
			if err = a.completeDecision(d, decisionResponse{}, "timed_out"); err != nil {
				return err
			}
			continue
		}
		if d.Status == "running" {
			continue
		}
		if d.NotBefore != "" && d.NotBefore > ringTime(time.Now()) {
			continue
		}
		if err := a.dispatchDecision(ctx, d); err != nil {
			return err
		}
	}
	return a.flushDecisionMarks(project)
}

// Serialize admission with shutdown so the final Wait cannot race a new Add.
func (a *App) dispatchDecision(ctx *sdk.AppCtx, d decisionRecord) error {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.decisionStopping {
		return nil
	}
	select {
	case decisionSlots <- struct{}{}:
	default:
		return nil
	}
	res, err := ctx.AppDB().Exec(`UPDATE routing_decisions SET status='running',started_at=? WHERE id=? AND status='pending'`, ringTime(time.Now()), d.ID)
	if err != nil {
		<-decisionSlots
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		<-decisionSlots
		return nil
	}
	a.decisionWG.Add(1)
	go func(d decisionRecord) {
		defer a.decisionWG.Done()
		defer func() { <-decisionSlots; a.wakeRouting(d.ProjectID); a.routingCapacityReleased() }()
		response, reason := a.invokeDecision(ctx, d)
		if err := a.completeDecision(d, response, reason); err != nil {
			ctx.Logger().Warn("routing decision completion", "decision", d.ID, "err", err)
		}
	}(d)
	return nil
}

func (a *App) invokeDecision(ctx *sdk.AppCtx, d decisionRecord) (decisionResponse, string) {
	var raw string
	if e := ctx.AppDB().QueryRow(`SELECT context_json FROM call_route_executions WHERE call_id=?`, d.CallID).Scan(&raw); e != nil {
		return decisionResponse{}, "context_unavailable"
	}
	var ex routingExecutionContext
	if json.Unmarshal([]byte(raw), &ex) != nil {
		return decisionResponse{}, "context_unavailable"
	}
	n, e := decisionNode(ex, d.NodeID)
	if e != nil {
		return decisionResponse{}, "context_unavailable"
	}
	cfg, e := parseDecisionConfig(n)
	if e != nil {
		return decisionResponse{}, "invalid_config"
	}
	var out struct {
		Status   string `json:"status"`
		Response string `json:"response"`
	}
	if e = ctx.PlatformAPI().CallAppResult("functions", "functions_invoke", map[string]any{"id": cfg.FunctionID, "event": json.RawMessage(d.Request)}, &out); e != nil || out.Status != "ok" {
		return decisionResponse{}, "function_failed"
	}
	return decodeDecisionResponse(d.ID, out.Response)
}
func decodeDecisionResponse(id, raw string) (decisionResponse, string) {
	var r decisionResponse
	if len(raw) > 16384 {
		return r, "response_too_large"
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil {
		return r, "invalid_response"
	}
	if decoder.Decode(new(any)) != io.EOF {
		return r, "invalid_response"
	}
	if r.DecisionID != id || (r.Action != "offer" && r.Action != "fallback" && r.Action != "wait_retry" && r.Action != "exhausted") || len(r.ReservationID) > 256 || strings.IndexFunc(r.ReservationID, func(c rune) bool { return c < 32 }) >= 0 {
		return r, "invalid_response"
	}
	if r.Action != "offer" && (r.DestinationID != "" || r.RingTimeout != 0) || r.Action == "offer" && r.RetryAfterSec != 0 || r.Action != "wait_retry" && r.RetryAfterSec != 0 || r.Action == "wait_retry" && (r.RetryAfterSec < 1 || r.RetryAfterSec > 30) {
		return r, "invalid_response"
	}
	return r, ""
}
func (a *App) completeDecision(d decisionRecord, response decisionResponse, reason string) error {
	defer lockRoutingCall(d.CallID)()
	row, e := a.db().findCall(d.CallID)
	if e != nil {
		return e
	}
	if row == nil {
		return nil
	}
	deadline, _ := time.Parse(time.RFC3339Nano, d.DeadlineAt)
	if !time.Now().Before(deadline) {
		reason = "timed_out"
	}
	var current string
	if e = a.db().db.QueryRow(`SELECT current_node_id FROM call_route_executions WHERE call_id=?`, d.CallID).Scan(&current); e != nil {
		return e
	}
	status := "accepted"
	if reason != "" {
		status = "rejected"
	}
	if reason == "timed_out" {
		status = "timed_out"
	}
	if response.Action == "fallback" && reason == "" {
		status = "fallback"
	}
	if response.Action == "exhausted" && reason == "" {
		status = "exhausted"
	}
	if response.Action == "wait_retry" && reason == "" {
		status = "waiting"
	}
	if row.Status != "pending" || current != d.NodeID {
		status = "canceled"
		reason = "call_progressed"
	}
	var plan *inboundRoutingPlan
	var cap destinationCapacity
	if status != "canceled" {
		cfg, err := a.decisionSettings(d)
		if err != nil {
			return err
		}
		switch status {
		case "accepted":
			plan, cap, e = a.decisionPlan(row, d, response)
			if e != nil {
				reason, status = e.Error(), "rejected"
				var continued bool
				plan, continued, e = a.nextDecisionPlan(row, d, 0)
				if e == nil && !continued {
					plan.RoutingResolution = "routing_error"
				}
			}
		case "waiting":
			var continued bool
			plan, continued, e = a.nextDecisionPlan(row, d, time.Duration(response.RetryAfterSec)*time.Second)
			if !continued {
				status, reason = "exhausted", "routing_budget_exhausted"
			}
		case "exhausted":
			plan, e = a.finalDecisionPlan(row, d, "routing_exhausted", cfg.CallbackOnAI)
		case "fallback":
			plan, e = a.finalDecisionPlan(row, d, "", cfg.CallbackOnAI)
		default:
			base, _ := decisionRuntimeNode(d.NodeID)
			prior, priorErr := a.priorFunctionFailures(d.CallID, base, d.ID)
			if priorErr != nil {
				return priorErr
			}
			if (reason == "function_failed" || reason == "timed_out") && prior < cfg.FunctionRetries {
				var continued bool
				plan, continued, e = a.nextDecisionPlan(row, d, time.Duration(cfg.RetryDelaySec)*time.Second)
				if e == nil && !continued {
					plan.RoutingResolution = "routing_error"
				}
			} else {
				plan, e = a.finalDecisionPlan(row, d, "routing_error", cfg.CallbackOnAI)
			}
		}
		if e != nil {
			return e
		}
	}
	tx, e := a.db().db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec(`UPDATE routing_decisions SET status=status WHERE id=? AND status IN ('pending','running')`, d.ID)
	if e != nil {
		return e
	}
	count, _ := res.RowsAffected()
	if count == 0 {
		return nil
	}
	// Recheck after acquiring the write lock: hangup/answer may race preparation.
	var callStatus, node string
	if e = tx.QueryRow(`SELECT c.status,x.current_node_id FROM calls c JOIN call_route_executions x ON x.call_id=c.id WHERE c.id=?`, d.CallID).Scan(&callStatus, &node); e != nil {
		return e
	}
	if callStatus != "pending" || node != d.NodeID {
		status = "canceled"
		reason = "call_progressed"
		plan = nil
	}
	if status == "accepted" {
		if !time.Now().Before(deadline) {
			tx.Rollback()
			return a.rejectDecision(d, response, "timed_out")
		}
		if e = validateDecisionTargetTx(tx, d.ProjectID, response.DestinationID, cap.Identity); e != nil {
			tx.Rollback()
			return a.rejectDecision(d, response, "destination_unavailable")
		}
		if e = reserveCapacityTx(tx, d.CallID, d.ProjectID, response.DestinationID, cap, ringTime(time.Now().Add(time.Duration(plan.TimeoutSec)*time.Second))); e != nil {
			// Resolve fallback outside the transaction; retrying completion remains safe.
			tx.Rollback()
			if !errors.Is(e, errPhoneCapacity) {
				return e
			}
			return a.rejectDecision(d, response, "capacity_unavailable")
		}
	}
	return a.finishDecisionTx(tx, d, response, status, reason, plan)
}

// Capacity failures take the same durable fallback path. No recursive call-lock.
func (a *App) rejectDecision(d decisionRecord, response decisionResponse, reason string) error {
	row, e := a.db().findCall(d.CallID)
	if e != nil {
		return e
	}
	var plan *inboundRoutingPlan
	cfg, e := a.decisionSettings(d)
	if e != nil {
		return e
	}
	base, _ := decisionRuntimeNode(d.NodeID)
	prior, e := a.priorFunctionFailures(d.CallID, base, d.ID)
	if e != nil {
		return e
	}
	if reason == "timed_out" && prior < cfg.FunctionRetries {
		var continued bool
		plan, continued, e = a.nextDecisionPlan(row, d, time.Duration(cfg.RetryDelaySec)*time.Second)
		if e == nil && !continued {
			plan.RoutingResolution = "routing_error"
		}
	} else if cfg.MaxAttempts > 1 && reason != "timed_out" {
		var continued bool
		plan, continued, e = a.nextDecisionPlan(row, d, 0)
		if e == nil && !continued {
			plan.RoutingResolution = "routing_error"
		}
	} else {
		plan, e = a.finalDecisionPlan(row, d, "routing_error", cfg.CallbackOnAI)
	}
	if e != nil {
		return e
	}
	tx, e := a.db().db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec(`UPDATE routing_decisions SET status=status WHERE id=? AND status IN ('pending','running')`, d.ID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	var status, current string
	if e = tx.QueryRow(`SELECT c.status,x.current_node_id FROM calls c JOIN call_route_executions x ON x.call_id=c.id WHERE c.id=?`, d.CallID).Scan(&status, &current); e != nil {
		return e
	}
	if status != "pending" || current != d.NodeID {
		return a.finishDecisionTx(tx, d, response, "canceled", "call_progressed", nil)
	}
	status = "rejected"
	if reason == "timed_out" {
		status = "timed_out"
	}
	return a.finishDecisionTx(tx, d, response, status, reason, plan)
}
func (a *App) finishDecisionTx(tx *sql.Tx, d decisionRecord, response decisionResponse, status, reason string, plan *inboundRoutingPlan) error {
	result, _ := json.Marshal(response)
	raw, _ := json.Marshal(plan)
	now := ringTime(time.Now())
	// Result precedes offers in the same transaction, including their journal triggers.
	if _, e := tx.Exec(`UPDATE routing_decisions SET status=?,result_json=?,reason=?,completed_at=?,plan_json=?,applied=? WHERE id=?`, status, string(result), reason, now, string(raw), status == "canceled", d.ID); e != nil {
		return e
	}
	if e := decisionEventTx(tx, d.ID, status, d.ID, "", now, nil); e != nil {
		return e
	}
	if plan != nil {
		if e := writeRoutingProgressTx(tx, d.CallID, d.ProjectID, plan); e != nil {
			return e
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.routingCommitted(d.ProjectID)
	return nil
}
func (a *App) deliverDecision(ctx *sdk.AppCtx, d decisionRecord) error {
	defer lockRoutingCall(d.CallID)()
	row, e := a.db().findCall(d.CallID)
	if e != nil {
		return e
	}
	var p inboundRoutingPlan
	if e = json.Unmarshal([]byte(d.PlanJSON), &p); e != nil {
		return e
	}
	var node string
	if e = ctx.AppDB().QueryRow(`SELECT current_node_id FROM call_route_executions WHERE call_id=?`, d.CallID).Scan(&node); e != nil {
		return e
	}
	if row != nil && row.Status == "pending" && node == p.NodeID {
		if p.TerminalType == "hangup" || p.TerminalType == "reject" {
			var ex routingExecutionContext
			if e = json.Unmarshal([]byte(p.ContextJSON), &ex); e == nil {
				e = a.finishTerminalRoutingPlan(ctx, row, &ex.Route, &p)
			}
		} else if (a.callUsesDirectSIP(row) || row.CarrierSlug == "bandwidth") && (p.TerminalType == "destination" || p.TerminalType == "ring_group") && p.AnswerMode == answerModeRealtimeImmediate {
			// The decision and offer journal are carrier-independent. Direct SIP
			// keeps the INVITE ringing; Bandwidth keeps a BXML wait loop. Either
			// transport starts AI media only after the decision commits.
			_, e = a.answerCall(ctx, row, p.Directive, p.Voice, p.Greeting, true)
		} else if row.CarrierSlug == "telnyx" && !((p.TerminalType == "destination" || p.TerminalType == "ring_group") && p.AnswerMode == answerModeHumanBrowser) {
			var ex routingExecutionContext
			if e = json.Unmarshal([]byte(p.ContextJSON), &ex); e == nil {
				route := ex.Route
				applyRoutingPlanToRoute(&route, &p)
				e = a.executeTelnyxRoutingPlan(ctx, row, &route, &p)
			}
		}
		if e != nil {
			return e
		}
	}
	_, e = ctx.AppDB().Exec(`UPDATE routing_decisions SET applied=1 WHERE id=?`, d.ID)
	return e
}
func (a *App) listDecisions(project, call string) ([]decisionRecord, error) {
	if call == "" {
		return nil, errors.New("call_id is required")
	}
	rows, e := a.db().db.Query(`SELECT `+decisionColumns+` FROM routing_decisions WHERE project_id=? AND call_id=? ORDER BY created_at,id LIMIT 100`, project, call)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []decisionRecord{}
	for rows.Next() {
		d, e := scanDecision(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (a *App) handleDecisionList(w http.ResponseWriter, r *http.Request, project string) {
	ds, e := a.listDecisions(project, r.URL.Query().Get("call_id"))
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"decisions": ds})
}
func (a *App) toolDecisionsList(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ds, e := a.listDecisions(currentProject(ctx), strArg(args, "call_id", ""))
	return map[string]any{"decisions": ds}, e
}

// Unique by milestone and offer, rather than call/topic: repeat decisions must
// produce repeat outcomes. Reuse the acknowledged, retryable lifecycle outbox.
func decisionEventTx(tx *sql.Tx, id, kind, key, dest, at string, identity json.RawMessage) error {
	eventID := id + ":" + key + ":" + kind
	var exists int
	e := tx.QueryRow(`SELECT 1 FROM call_events WHERE event_id=?`, eventID).Scan(&exists)
	if e == nil {
		return nil
	}
	if e != sql.ErrNoRows {
		return e
	}
	var call, project, result, reason, request string
	if e = tx.QueryRow(`SELECT call_id,project_id,result_json,reason,request_json FROM routing_decisions WHERE id=?`, id).Scan(&call, &project, &result, &reason, &request); e != nil {
		return e
	}
	var response decisionResponse
	_ = json.Unmarshal([]byte(result), &response)
	var requestInfo struct {
		Attempt     int `json:"attempt"`
		NodeAttempt int `json:"node_attempt"`
	}
	_ = json.Unmarshal([]byte(request), &requestInfo)
	if _, e = tx.Exec(`UPDATE calls SET lifecycle_revision=lifecycle_revision+1 WHERE id=?`, call); e != nil {
		return e
	}
	var revision int64
	if e = tx.QueryRow(`SELECT lifecycle_revision FROM calls WHERE id=?`, call).Scan(&revision); e != nil {
		return e
	}
	topic := "telephony.routing." + kind
	payload := map[string]any{"schema_version": 2, "event_id": eventID, "revision": revision, "topic": topic, "occurred_at": at, "project_id": project, "call_id": call, "decision_id": id, "attempt": requestInfo.Attempt, "node_attempt": requestInfo.NodeAttempt, "reason": reason, "reservation_id": response.ReservationID, "destination_id": firstNonEmpty(dest, response.DestinationID)}
	if strings.HasPrefix(kind, "offer.") {
		payload["offer_id"] = key
	}
	if len(identity) > 0 && string(identity) != "" {
		payload["answering_identity"] = identity
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return fmt.Errorf("encode routing outcome: %w", e)
	}
	_, e = tx.Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, eventID, call, project, topic, revision, at, string(raw), at)
	return e
}
func (a *App) flushDecisionMarks(project string) error {
	tx, e := a.db().db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rows, e := tx.Query(`SELECT m.id,m.decision_id,m.kind,m.item_key,m.destination_id,m.occurred_at,m.identity_json FROM routing_outcome_marks m JOIN routing_decisions d ON d.id=m.decision_id WHERE d.project_id=? AND m.published=0 ORDER BY m.id LIMIT 200`, project)
	if e != nil {
		return e
	}
	type mark struct {
		id                                      int64
		decision, kind, key, dest, at, identity string
	}
	marks := []mark{}
	for rows.Next() {
		var m mark
		if e = rows.Scan(&m.id, &m.decision, &m.kind, &m.key, &m.dest, &m.at, &m.identity); e != nil {
			rows.Close()
			return e
		}
		marks = append(marks, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, m := range marks {
		var identity json.RawMessage
		if m.identity != "" {
			identity = json.RawMessage(m.identity)
		}
		if e = decisionEventTx(tx, m.decision, m.kind, m.key, m.dest, m.at, identity); e != nil {
			return e
		}
		if _, e = tx.Exec(`UPDATE routing_outcome_marks SET published=1 WHERE id=?`, m.id); e != nil {
			return e
		}
	}
	if e = cleanupCapacityTx(tx, time.Now()); e != nil {
		return e
	}
	return tx.Commit()
}
