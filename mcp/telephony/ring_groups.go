package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type ringOffer struct {
	ID            string `json:"id"`
	DestinationID string `json:"destination_id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	AgentID       int64  `json:"agent_id,omitempty"`
	ExpiresAt     string `json:"expires_at"`
	RunID         string `json:"-"`
	ConfigJSON    string `json:"-"`
	TimeoutSec    int    `json:"-"`
	Position      int    `json:"-"`
	Priority      int    `json:"-"`
}

// initRingRunTx is part of the ingress/IVR transaction. Replayed callbacks must
// neither advance the shared round-robin cursor nor create another set of offers.
func initRingRunTx(tx *sql.Tx, callID, project string, plan *inboundRoutingPlan, now time.Time) error {
	if plan == nil || plan.Group == nil {
		return nil
	}
	group := *plan.Group
	runID := "ring_" + callID + "_" + plan.NodeID
	res, err := tx.Exec(`INSERT OR IGNORE INTO call_ring_runs(id,call_id,project_id,node_id,ring_group_id,strategy,overflow_node_id,started_at,deadline_at) VALUES(?,?,?,?,?,?,?,?,?)`, runID, callID, project, plan.NodeID, group.ID, group.Strategy, plan.OverflowNodeID, ringTime(now), ringTime(now.Add(10*time.Minute)))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	members := []ringGroupMemberRow{}
	for _, m := range group.Members {
		if m.Enabled {
			members = append(members, m)
		}
	}
	sort.SliceStable(members, func(i, j int) bool {
		if group.Strategy == "priority" && members[i].Priority != members[j].Priority {
			return members[i].Priority < members[j].Priority
		}
		return members[i].Position < members[j].Position
	})
	if len(members) == 0 {
		return errors.New("ring group has no enabled members")
	}
	if group.Strategy == "round_robin" {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO ring_group_cursors(project_id,ring_group_id) VALUES(?,?)`, project, group.ID); err != nil {
			return err
		}
		var cursor int
		if err = tx.QueryRow(`SELECT next_position FROM ring_group_cursors WHERE project_id=? AND ring_group_id=?`, project, group.ID).Scan(&cursor); err != nil {
			return err
		}
		start := cursor % len(members)
		members = append(append([]ringGroupMemberRow{}, members[start:]...), members[:start]...)
		if _, err = tx.Exec(`UPDATE ring_group_cursors SET next_position=? WHERE project_id=? AND ring_group_id=?`, (cursor+1)%len(members), project, group.ID); err != nil {
			return err
		}
	}
	for i, m := range members {
		dest, ok := plan.GroupDestinations[m.DestinationID]
		if !ok || !dest.Enabled {
			return fmt.Errorf("ring destination %s unavailable", m.DestinationID)
		}
		var config map[string]any
		_ = json.Unmarshal([]byte(dest.ConfigJSON), &config)
		timeout := m.TimeoutSec
		if timeout <= 0 {
			timeout = group.TimeoutSec
		}
		timeout = min(timeout, 300)
		if _, err = tx.Exec(`INSERT INTO call_offers(id,call_id,project_id,ring_group_id,destination_id,status,offered_at,expires_at,run_id,position,priority,timeout_sec,kind,agent_id,config_json,destination_name) VALUES(?,?,?,?,?,'queued','','',?,?,?,?,?,?,?,?)`, runID+"_"+fmt.Sprint(i), callID, project, group.ID, dest.ID, runID, i, m.Priority, timeout, dest.Kind, routingConfigInt(config, "agent_id", 0), dest.ConfigJSON, dest.Name); err != nil {
			return err
		}
	}
	// The parent deadline covers all attempts. Each offer has its own timeout.
	if _, err = tx.Exec(`UPDATE calls SET state_expires_at=? WHERE id=? AND status='pending'`, now.Add(10*time.Minute).Format(time.RFC3339), callID); err != nil {
		return err
	}
	return advanceRingRunTx(tx, runID, now)
}

func ringTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func advanceRingRunTx(tx *sql.Tx, runID string, now time.Time) error {
	var callID, strategy, status, deadline string
	if err := tx.QueryRow(`SELECT call_id,strategy,status,deadline_at FROM call_ring_runs WHERE id=?`, runID).Scan(&callID, &strategy, &status, &deadline); err != nil {
		return err
	}
	if status != "ringing" {
		return nil
	}
	var callStatus string
	if err := tx.QueryRow(`SELECT status FROM calls WHERE id=?`, callID).Scan(&callStatus); err != nil {
		return err
	}
	if callStatus != "pending" {
		if isTerminalStatus(callStatus) {
			if _, err := tx.Exec(`UPDATE call_ring_runs SET status='canceled' WHERE id=?`, runID); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE call_offers SET status='canceled' WHERE run_id=? AND status IN ('queued','offered')`, runID)
			return err
		}
		return nil // An answer claim owns the transition while it prepares media.
	}
	expired, _ := time.Parse(time.RFC3339Nano, deadline)
	if !now.Before(expired) {
		if _, err := tx.Exec(`UPDATE call_offers SET status='expired' WHERE run_id=? AND status IN ('queued','offered')`, runID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`UPDATE call_offers SET status='expired' WHERE run_id=? AND status='offered' AND expires_at<=?`, runID, ringTime(now)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM phone_capacity WHERE call_id=? AND expires_at<>'' AND NOT EXISTS(SELECT 1 FROM call_offers o WHERE o.call_id=phone_capacity.call_id AND o.capacity_principal=phone_capacity.principal AND o.status='offered' AND o.expires_at>?)`, callID, ringTime(now)); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM call_offers WHERE run_id=? AND status='offered'`, runID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return nil
	}
	rows, err := tx.Query(`SELECT id,timeout_sec,priority,destination_id,config_json FROM call_offers WHERE run_id=? AND status='queued' ORDER BY position`, runID)
	if err != nil {
		return err
	}
	var offers []ringOffer
	for rows.Next() {
		var o ringOffer
		if err := rows.Scan(&o.ID, &o.TimeoutSec, &o.Priority, &o.DestinationID, &o.ConfigJSON); err != nil {
			rows.Close()
			return err
		}
		offers = append(offers, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(offers) == 0 {
		_, err = tx.Exec(`UPDATE call_ring_runs SET status='exhausted' WHERE id=?`, runID)
		return err
	}
	activated := 0
	for _, o := range offers {
		if (strategy == "sequential" || strategy == "round_robin") && activated > 0 {
			break
		}
		if strategy == "priority" && o.Priority != offers[0].Priority {
			break
		}
		until := now.Add(time.Duration(o.TimeoutSec) * time.Second)
		if until.After(expired) {
			until = expired
		}
		capacity, e := readDestinationCapacity(o.ConfigJSON)
		if e != nil {
			return e
		}
		if capacity.Limit > 0 {
			var project string
			if e = tx.QueryRow(`SELECT project_id FROM calls WHERE id=?`, callID).Scan(&project); e != nil {
				return e
			}
			if e = reserveCapacityTx(tx, callID, project, o.DestinationID, capacity, ringTime(until)); e != nil {
				if !errors.Is(e, errPhoneCapacity) {
					return e
				}
				if _, e = tx.Exec(`UPDATE call_offers SET status='failed',last_error='capacity_unavailable' WHERE id=?`, o.ID); e != nil {
					return e
				}
				continue
			}
		}
		if capacity.Limit > 0 {
			if _, err = tx.Exec(`UPDATE call_offers SET capacity_principal=? WHERE id=?`, capacity.Identity.key(), o.ID); err != nil {
				return err
			}
		}
		activated++
		if _, err = tx.Exec(`UPDATE call_offers SET status='offered',offered_at=?,expires_at=?,next_attempt_at=? WHERE id=? AND status='queued'`, ringTime(now), ringTime(until), ringTime(now), o.ID); err != nil {
			return err
		}
	}
	if activated == 0 {
		return advanceRingRunTx(tx, runID, now)
	}
	return nil
}

func (c *callsDB) claimRingOffer(callID, project, destinationID, kind string, agentID int64) (bool, bool, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()
	var runID string
	err = tx.QueryRow(`SELECT r.id FROM call_ring_runs r JOIN call_route_executions e ON e.call_id=r.call_id AND e.current_node_id=r.node_id WHERE r.call_id=? AND r.project_id=? AND r.status='ringing'`, callID, project).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	now := time.Now().UTC()
	var o ringOffer
	err = tx.QueryRow(`SELECT id,destination_id,kind,agent_id,config_json FROM call_offers WHERE run_id=? AND status='offered' AND expires_at>? AND (?='' OR destination_id=?) AND ((?='browser' AND kind='browser') OR (?='agent' AND kind IN ('agent','ai') AND agent_id=?) OR (?='external' AND kind IN ('pstn','sip'))) ORDER BY position LIMIT 1`, runID, ringTime(now), destinationID, destinationID, kind, kind, agentID, kind).Scan(&o.ID, &o.DestinationID, &o.Kind, &o.AgentID, &o.ConfigJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return true, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if o.Kind == "browser" {
		capacity, e := readDestinationCapacity(o.ConfigJSON)
		if e != nil {
			return true, false, e
		}
		if e = reserveCapacityTx(tx, callID, project, o.DestinationID, capacity, ""); e != nil {
			return true, false, e
		}
	}
	peer := peerKindRealtime
	if o.Kind == "browser" {
		peer = peerKindHuman
	} else if o.Kind == "pstn" || o.Kind == "sip" {
		peer = peerKindExternal
	}
	var config map[string]any
	_ = json.Unmarshal([]byte(o.ConfigJSON), &config)
	res, err := tx.Exec(`UPDATE calls SET status='answering',agent_id=?,peer_kind=?,routing_destination_id=?,directive=?,voice=?,state_expires_at=? WHERE id=? AND project_id=? AND status='pending' AND (deadline_at='' OR deadline_at>?)`, o.AgentID, peer, o.DestinationID, routingConfigString(config, "directive"), routingConfigString(config, "voice"), now.Add(30*time.Second).Format(time.RFC3339), callID, project, now.Format(time.RFC3339))
	if err != nil {
		return true, false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return true, false, err
	}
	if _, err = tx.Exec(`UPDATE call_offers SET status=CASE WHEN id=? THEN 'claimed' ELSE 'canceled' END,claimed_at=CASE WHEN id=? THEN ? ELSE '' END WHERE run_id=? AND status IN ('offered','queued')`, o.ID, o.ID, ringTime(now), runID); err != nil {
		return true, false, err
	}
	if _, err = tx.Exec(`DELETE FROM phone_capacity WHERE call_id=? AND destination_id<>? AND expires_at<>''`, callID, o.DestinationID); err != nil {
		return true, false, err
	}
	if _, err = tx.Exec(`UPDATE call_ring_runs SET status='claimed' WHERE id=?`, runID); err != nil {
		return true, false, err
	}
	if _, err = tx.Exec(`UPDATE call_route_executions SET selected_destination_id=?,status='claimed' WHERE call_id=?`, o.DestinationID, callID); err != nil {
		return true, false, err
	}
	return true, true, c.commitCall(tx, callID)
}

func (c *callsDB) activeRingOffers(callID, project string) ([]ringOffer, error) {
	rows, err := c.db.Query(`SELECT o.id,o.destination_id,o.destination_name,o.kind,o.agent_id,o.expires_at,o.run_id,o.config_json FROM call_offers o JOIN call_ring_runs r ON r.id=o.run_id WHERE o.call_id=? AND o.project_id=? AND o.status='offered' AND r.status='ringing' AND o.expires_at>? ORDER BY o.position`, callID, project, ringTime(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ringOffer{}
	for rows.Next() {
		var o ringOffer
		if err := rows.Scan(&o.ID, &o.DestinationID, &o.Name, &o.Kind, &o.AgentID, &o.ExpiresAt, &o.RunID, &o.ConfigJSON); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (c *callsDB) hasAgentRingOffer(callID, project string, agentID int64) bool {
	var count int
	_ = c.db.QueryRow(`SELECT COUNT(*) FROM call_offers o JOIN call_ring_runs r ON r.id=o.run_id WHERE o.call_id=? AND o.project_id=? AND o.agent_id=? AND o.kind IN ('agent','ai') AND o.status='offered' AND r.status='ringing' AND o.expires_at>?`, callID, project, agentID, ringTime(time.Now())).Scan(&count)
	return count > 0
}

func (c *callsDB) declineRingOffers(callID, project string, agentID int64) (bool, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var runID string
	err = tx.QueryRow(`SELECT r.id FROM call_ring_runs r JOIN calls c ON c.id=r.call_id WHERE r.call_id=? AND r.project_id=? AND r.status='ringing' AND c.status='pending'`, callID, project).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	res, err := tx.Exec(`UPDATE call_offers SET status='declined',declined_at=? WHERE run_id=? AND agent_id=? AND kind IN ('agent','ai') AND status='offered' AND expires_at>?`, ringTime(time.Now()), runID, agentID, ringTime(time.Now()))
	if err != nil {
		return true, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return true, err
	}
	if n == 0 {
		return true, errors.New("no current ring offer for this agent")
	}
	if err = advanceRingRunTx(tx, runID, time.Now()); err != nil {
		return true, err
	}
	return true, c.commitCall(tx, callID)
}

// Failed setup releases only the winning offer. The other destinations remain
// eligible; a recovered worker advances to them instead of abandoning the caller.
func (c *callsDB) releaseRingClaim(callID string) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = releaseRingClaimTx(tx, callID); err != nil {
		return err
	}
	return c.commitCall(tx, callID)
}

// releaseRingClaimTx reopens a claimed ring run inside the caller's
// transaction, so returning the call to 'pending' and re-ringing the remaining
// members commit together. A separate transaction could leave the call pending
// with its run stuck in 'claimed', and nobody else would ever ring.
func releaseRingClaimTx(tx *sql.Tx, callID string) error {
	var runID string
	err := tx.QueryRow(`SELECT id FROM call_ring_runs WHERE call_id=? AND status='claimed'`, callID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var status string
	if err = tx.QueryRow(`SELECT status FROM calls WHERE id=?`, callID).Scan(&status); err != nil {
		return err
	}
	if status != "pending" {
		return nil
	}
	if _, err = tx.Exec(`UPDATE call_offers SET status=CASE WHEN status='claimed' THEN 'failed' ELSE 'queued' END WHERE run_id=? AND status IN ('claimed','canceled')`, runID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE call_ring_runs SET status='ringing' WHERE id=?`, runID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE calls SET routing_destination_id='',state_expires_at=(SELECT deadline_at FROM call_ring_runs WHERE id=?) WHERE id=?`, runID, callID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE call_route_executions SET status='ringing',selected_destination_id='' WHERE call_id=?`, callID); err != nil {
		return err
	}
	return advanceRingRunTx(tx, runID, time.Now())
}

func (a *App) runRingGroupTick(_ context.Context, ctx *sdk.AppCtx) error {
	if ctx.CurrentProject() == "" {
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT id,call_id FROM call_ring_runs WHERE project_id=? AND status IN ('ringing','exhausted') ORDER BY started_at LIMIT 100`, ctx.CurrentProject())
	if err != nil {
		return err
	}
	type item struct{ run, call string }
	items := []item{}
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.run, &it.call); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := a.tickRingRun(ctx, it.run, it.call); err != nil {
			ctx.Logger().Warn("ring group progress", "call", it.call, "err", err)
		}
	}
	return nil
}

func (a *App) tickRingRun(ctx *sdk.AppCtx, runID, callID string) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	var before int64
	if err = tx.QueryRow(`SELECT COALESCE((SELECT revision FROM telephony_read_versions WHERE project_id=?),0)`, ctx.CurrentProject()).Scan(&before); err != nil {
		tx.Rollback()
		return err
	}
	if err = advanceRingRunTx(tx, runID, time.Now()); err != nil {
		tx.Rollback()
		return err
	}
	var after int64
	if err = tx.QueryRow(`SELECT COALESCE((SELECT revision FROM telephony_read_versions WHERE project_id=?),0)`, ctx.CurrentProject()).Scan(&after); err != nil {
		tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if before != after {
		a.callChanges.notify(ctx.CurrentProject())
	}
	var status, overflow string
	if err = ctx.AppDB().QueryRow(`SELECT status,overflow_node_id FROM call_ring_runs WHERE id=?`, runID).Scan(&status, &overflow); err != nil {
		return err
	}
	if status == "exhausted" {
		return a.finishRingRun(ctx, runID, callID, overflow)
	}
	if status != "ringing" {
		return nil
	}
	offers, err := a.db().activeRingOffers(callID, ctx.CurrentProject())
	if err != nil {
		return err
	}
	// A decision offer can lose its destination or verified access while it is
	// ringing. Release it now so the policy can select another adviser.
	var decisionRun int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM call_ring_runs r JOIN routing_decisions d ON d.id=r.ring_group_id AND d.call_id=r.call_id WHERE r.id=? AND r.call_id=? AND d.status='accepted'`, runID, callID).Scan(&decisionRun); err != nil {
		return err
	}
	if decisionRun != 0 {
		for _, offer := range offers {
			dest, lookupErr := a.findRoutingDestination(ctx.CurrentProject(), offer.DestinationID)
			if lookupErr != nil {
				return lookupErr
			}
			if a.validateDecisionDestination(ctx.CurrentProject(), dest) == nil {
				continue
			}
			tx, beginErr := ctx.AppDB().Begin()
			if beginErr != nil {
				return beginErr
			}
			result, updateErr := tx.Exec(`UPDATE call_offers SET status='failed' WHERE id=? AND status='offered' AND EXISTS(SELECT 1 FROM calls WHERE id=? AND status='pending')`, offer.ID, callID)
			if updateErr != nil {
				tx.Rollback()
				return updateErr
			}
			changed, updateErr := result.RowsAffected()
			if updateErr != nil {
				tx.Rollback()
				return updateErr
			}
			if changed != 0 {
				if updateErr = advanceRingRunTx(tx, runID, time.Now()); updateErr != nil {
					tx.Rollback()
					return updateErr
				}
			}
			if updateErr = tx.Commit(); updateErr != nil {
				return updateErr
			}
			if changed != 0 {
				a.routingCommitted(ctx.CurrentProject())
				return a.tickRingRun(ctx, runID, callID)
			}
			return nil
		}
	}
	parent, err := a.db().findCall(callID)
	if err != nil || parent == nil {
		return err
	}
	a.startOfferedRingLegs(ctx, parent, offers)
	for _, offer := range offers {
		if offer.Kind == "ai" {
			row, err := a.db().findCall(callID)
			if err != nil {
				return err
			}
			if row == nil || row.Status != "pending" {
				continue
			}
			row.AgentID = offer.AgentID
			row.PeerKind = peerKindRealtime
			var config map[string]any
			_ = json.Unmarshal([]byte(offer.ConfigJSON), &config)
			if _, err = a.answerCall(ctx, row, routingConfigString(config, "directive"), routingConfigString(config, "voice"), routingConfigString(config, "greeting"), false); err != nil {
				ctx.Logger().Warn("ring group AI answer", "call", callID, "destination", offer.DestinationID, "err", err)
			}
		} else if offer.Kind == "agent" {
			var delivered, next string
			var attempts int
			if err := ctx.AppDB().QueryRow(`SELECT delivered_at,next_attempt_at,delivery_attempts FROM call_offers WHERE id=? AND status='offered'`, offer.ID).Scan(&delivered, &next, &attempts); err != nil || delivered != "" || next > ringTime(time.Now()) {
				continue
			}
			message := fmt.Sprintf("Incoming phone call offered to you. call_id=%s destination_id=%s offer_id=%s expires_at=%s. Answer with telephony_answer_call or decline your offer with telephony_reject_call. The first answer wins.", callID, offer.DestinationID, offer.ID, offer.ExpiresAt)
			err := ctx.PlatformAPI().SendEvent(offer.AgentID, message)
			if err == nil {
				_, err = ctx.AppDB().Exec(`UPDATE call_offers SET delivered_at=?,last_error='' WHERE id=?`, ringTime(time.Now()), offer.ID)
			} else {
				_, _ = ctx.AppDB().Exec(`UPDATE call_offers SET delivery_attempts=delivery_attempts+1,next_attempt_at=?,last_error=? WHERE id=?`, ringTime(time.Now().Add(time.Duration(min(1<<min(attempts, 5), 30))*time.Second)), err.Error(), offer.ID)
			}
		}
	}
	return nil
}

func (a *App) finishRingRun(ctx *sdk.AppCtx, runID, callID, overflow string) error {
	defer lockRoutingCall(callID)()
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM call_ring_runs WHERE id=? AND call_id=?`, runID, callID).Scan(&status); err != nil {
		return err
	}
	if status != "exhausted" {
		return nil
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		return err
	}
	if row.Status != "pending" {
		_, err = ctx.AppDB().Exec(`UPDATE call_ring_runs SET status='canceled' WHERE id=? AND status='exhausted'`, runID)
		return err
	}
	// failThrough ends the run on the flow's failure path. Used when there is
	// no overflow, and when the overflow cannot make progress (see below), so
	// the caller is not left holding until the run deadline.
	failThrough := func() error {
		if err := a.expireCall(ctx, row); err != nil {
			return err
		}
		_, err := ctx.AppDB().Exec(`UPDATE call_ring_runs SET status='finished' WHERE id=?`, runID)
		return err
	}
	if overflow == "" {
		return failThrough()
	}
	// failLoop: the overflow can never offer anyone (it loops back into a ring
	// node that already ran). End the call now rather than at its state expiry.
	failLoop := func() error {
		if _, err := ctx.AppDB().Exec(`UPDATE calls SET state_expires_at=? WHERE id=? AND status='pending'`, time.Now().UTC().Format(time.RFC3339Nano), callID); err != nil {
			return err
		}
		return failThrough()
	}
	var raw string
	if err = ctx.AppDB().QueryRow(`SELECT context_json FROM call_route_executions WHERE call_id=?`, callID).Scan(&raw); err != nil {
		return err
	}
	var execution routingExecutionContext
	if err = json.Unmarshal([]byte(raw), &execution); err != nil {
		return err
	}
	var plan *inboundRoutingPlan
	var current string
	if err = ctx.AppDB().QueryRow(`SELECT current_node_id FROM call_route_executions WHERE call_id=?`, callID).Scan(&current); err != nil {
		return err
	}
	if overflow == current {
		var d decisionRecord
		d, err = scanDecision(ctx.AppDB().QueryRow(`SELECT `+decisionColumns+` FROM routing_decisions WHERE call_id=? AND node_id=? AND status='accepted'`, callID, current))
		if errors.Is(err, sql.ErrNoRows) {
			// A flow ring node whose overflow points at itself: there is no
			// decision to retry, and retrying the lookup each tick never succeeds.
			return failLoop()
		}
		if err != nil {
			return err
		}
		plan, _, err = a.nextDecisionPlan(row, d, 0)
	} else {
		plan, err = a.resolveRoutingDefinition(&execution.Route, row.FromNumber, execution.Digits, &routingFlowVersionRow{ID: row.RoutingFlowVersionID, FlowID: row.RoutingFlowID}, execution.Definition, overflow)
	}
	if err != nil {
		return err
	}
	if plan != nil && plan.Group != nil {
		// Overflow back into a ring node that already ran: initRingRunTx keys
		// runs by call+node, so it would insert nothing and offer nobody.
		var existing int
		if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM call_ring_runs WHERE id=?`, "ring_"+callID+"_"+plan.NodeID).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return failLoop()
		}
	}
	if err = a.persistRoutingProgress(callID, row.ProjectID, plan, runID); err != nil {
		return err
	}
	row, err = a.db().findCall(callID)
	if err != nil || row == nil || row.Status != "pending" {
		return err
	}
	// SIP executes the same pinned plan locally.
	if a.callUsesDirectSIP(row) {
		return a.executeSIPRoutingPlan(ctx, row, plan)
	}
	// XML providers pick up the new pinned node on their next wait callback.
	if plan.TerminalType == "hangup" || plan.TerminalType == "reject" {
		return a.finishTerminalRoutingPlan(ctx, row, &execution.Route, plan)
	}
	if row.CarrierSlug == "telnyx" {
		route := execution.Route
		applyRoutingPlanToRoute(&route, plan)
		return a.executeTelnyxRoutingPlan(ctx, row, &route, plan)
	}
	return nil
}

func ringHasBrowser(offers []ringOffer) bool {
	for _, o := range offers {
		if o.Kind == "browser" {
			return true
		}
	}
	return false
}
func ringOfferIDs(offers []ringOffer) string {
	ids := []string{}
	for _, o := range offers {
		ids = append(ids, o.DestinationID)
	}
	return strings.Join(ids, ",")
}

// Attach all visible offers with one query rather than one lookup per call row.
func (c *callsDB) attachRingOffers(project string, calls []callRow) error {
	indices := map[string]int{}
	for i := range calls {
		if calls[i].Status == "pending" {
			indices[calls[i].ID] = i
		}
	}
	if len(indices) == 0 {
		return nil
	}
	ids, args := callReadIDs(project, calls)
	args = append(args, ringTime(time.Now()))
	rows, err := c.db.Query(`SELECT o.call_id,o.id,o.destination_id,o.destination_name,o.kind,o.agent_id,o.expires_at FROM call_offers o JOIN call_ring_runs r ON r.id=o.run_id WHERE o.project_id=? AND o.call_id IN (`+phonePlaceholders(len(ids))+`) AND o.status='offered' AND r.status='ringing' AND o.expires_at>? ORDER BY o.position`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var o ringOffer
		if err := rows.Scan(&id, &o.ID, &o.DestinationID, &o.Name, &o.Kind, &o.AgentID, &o.ExpiresAt); err != nil {
			return err
		}
		if i, ok := indices[id]; ok {
			calls[i].RingOffers = append(calls[i].RingOffers, o)
		}
	}
	return rows.Err()
}

func panelPeerKind(row callRow) string {
	if row.Status == "pending" && ringHasBrowser(row.RingOffers) {
		return peerKindHuman
	}
	return firstNonEmpty(row.PeerKind, peerKindRealtime)
}
