package main

import (
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const unclaimedStepAfter = 2 * time.Minute
const unclaimedStepCooldown = 2 * time.Minute
const maxClaimRecoveryAttempts = 3

// settled acknowledges transport delivery only. A step is claimed only when
// the assigned persistent worker calls step_claim and the state becomes running.
func (a *App) recoverUnclaimedStep(p *Process, r Run, s StepRun, all []StepRun, now time.Time) error {
	if terminal(r.State) || persistentWorkerAgent(r, s, all) == 0 || s.State != "ready" || s.ClaimedAt != "" || s.DeliveredAt == "" || s.ThreadID == "" || s.DeliverySuspended || !stepReleased(r, s) || !stepTimeReady(s, now) || !dependenciesReady(s, all) {
		return nil
	}
	delivered, err := time.Parse(time.RFC3339Nano, s.DeliveredAt)
	if err != nil {
		return err
	}
	due := delivered.Add(unclaimedStepAfter)
	if s.ClaimNextAt != "" {
		due, err = time.Parse(time.RFC3339Nano, s.ClaimNextAt)
		if err != nil {
			return err
		}
	}
	if now.Before(due) {
		return nil
	}
	worker, err := a.runWorker(r.ID, s.Executor.AgentID)
	if err != nil || worker != s.ThreadID {
		return err
	}
	if aiParallel(r) {
		var ready int
		if err = a.db.QueryRow(`SELECT count(*) FROM process_step_runs WHERE run_id=? AND target_thread_id=? AND state='ready' AND delivered_at<>'' AND claimed_at='' AND id<>?`, r.ID, worker, s.ID).Scan(&ready); err != nil {
			return err
		}
		// In auto-parallel mode a worker may deliberately choose one of several
		// delivered branches. Do not call that a missed handoff while choices remain.
		if ready > 0 {
			return nil
		}
	}
	var busy int
	if err = a.db.QueryRow(`SELECT count(*) FROM process_step_runs WHERE run_id=? AND target_thread_id=? AND state IN ('running','waiting','blocked')`, r.ID, worker).Scan(&busy); err != nil || busy > 0 {
		return err
	}
	if s.ClaimAttempts >= maxClaimRecoveryAttempts {
		_, err = a.db.Exec(`UPDATE process_step_runs SET delivery_warning=?,delivery_suspended=1,claim_next_at='' WHERE id=?`, fmt.Sprintf("Run stalled: delivered step %s remains unclaimed after %d recovery attempts. Inspect worker %s and call processes_step_claim; no domain work was retried by Processes.", s.ID, s.ClaimAttempts, worker), s.ID)
		return err
	}
	eventID := s.ClaimEventID
	if eventID == "" {
		eventID = fmt.Sprintf("process-step:%s:claim-recovery:%d", s.ID, s.ClaimAttempts+1)
	}
	candidate := sdk.AgentEventRequest{AgentID: s.Executor.AgentID, ThreadID: worker, SourceEventID: eventID, Message: fmt.Sprintf("Processes recovery: a delivered ready step remains unclaimed. This ready-step event takes precedence over any earlier completion response telling you to wait. Call processes_step_claim with process_id=%s, run_id=%s, step_id=%s to recover frozen instructions, exact dependency evidence and saved checkpoint. Keep this same worker. Inspect evidence before repeating domain actions. Wait only when no delivered ready work can be claimed.", p.ID, r.ID, s.ID)}
	frozen, err := a.immutableDelivery(s.ProjectID, candidate)
	if err != nil {
		return err
	}
	if _, err = a.db.Exec(`UPDATE process_step_runs SET claim_event_id=?,claim_attempts=claim_attempts+1,claim_next_at=? WHERE id=?`, eventID, now.Add(unclaimedStepCooldown).UTC().Format(time.RFC3339Nano), s.ID); err != nil {
		return err
	}
	api := a.ctx.WithProject(s.ProjectID).AgentEventsAPI()
	var receipt *sdk.AgentEventReceipt
	if api == nil {
		err = errors.New("tracked delivery unavailable")
	} else {
		receipt, err = api.SendTrackedAgentEvent(frozen)
	}
	if err == nil && (receipt == nil || (!receipt.Accepted && !receipt.Duplicate)) {
		err = errors.New("unclaimed-step recovery not accepted")
	}
	if err != nil {
		_, saveErr := a.db.Exec(`UPDATE process_step_runs SET delivery_warning=? WHERE id=?`, "Unclaimed-step recovery pending: "+err.Error(), s.ID)
		return errors.Join(err, saveErr)
	}
	_, err = a.db.Exec(`UPDATE process_step_runs SET claim_event_id='',delivery_warning='' WHERE id=?`, s.ID)
	return err
}
