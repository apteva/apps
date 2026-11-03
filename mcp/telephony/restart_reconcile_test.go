package main

import (
	"testing"
	"time"
)

// A restart drops every direct SIP dialog with the process that held it. The
// calls those dialogs belonged to must end as restart failures, publish once,
// and leave nothing for the expiry worker to chase.
func TestReconcileAbandonedDirectSIPCallsAtStartup(t *testing.T) {
	platform := &answerPlatform{}
	app, ctx := withTelephonyTestContext(t, platform)
	now := time.Now().UTC()
	placed := ringTime(now.Add(-time.Minute))
	past := now.Add(-time.Second).Format(time.RFC3339Nano)
	future := now.Add(time.Hour).Format(time.RFC3339Nano)
	calls := []callRow{
		// Ringing inbound call on a direct SIP route.
		{ID: "sip-ringing", ThreadID: "thread-ringing", AgentID: 7, Direction: "inbound", Status: "answering", IngressPath: inboundTransportSIPDirect, CarrierSlug: "telnyx"},
		// Connected outbound call through the SIP gateway, with the AI talking.
		{ID: "sip-connected", ThreadID: "thread-connected", AgentID: 7, Direction: "outbound", Status: "in-progress", IngressPath: inboundTransportSIPDirect, CarrierSlug: "telnyx", AnsweredAt: placed, MediaConnectedAt: placed},
		// Already finished before the restart.
		{ID: "sip-finished", ThreadID: "thread-finished", AgentID: 7, Direction: "inbound", Status: "completed", IngressPath: inboundTransportSIPDirect, CarrierSlug: "telnyx", AnsweredAt: placed, EndedAt: placed},
		// Held by the carrier's call control: it survives our restart.
		{ID: "carrier-held", ThreadID: "thread-carrier", AgentID: 7, Direction: "inbound", Status: "in-progress", IngressPath: inboundTransportProgrammable, CarrierSlug: "twilio", CarrierSID: "CA1", AnsweredAt: placed},
	}
	for _, call := range calls {
		call.ProjectID, call.PlacedAt = "project-a", placed
		if err := app.db().insertCall(call, false); err != nil {
			t.Fatal(err)
		}
	}
	// Deadlines the previous execution armed; one is already overdue.
	for _, id := range []string{"sip-ringing", "sip-connected", "carrier-held"} {
		if _, err := app.db().db.Exec(`UPDATE calls SET state_expires_at=?,deadline_at=?,connected_deadline_at=?,media_deadline_at=? WHERE id=?`, past, future, future, future, id); err != nil {
			t.Fatal(err)
		}
	}

	reconciled, err := app.reconcileAbandonedDirectSIPCalls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 2 {
		t.Fatalf("reconciled %d calls, want 2", reconciled)
	}
	for _, id := range []string{"sip-ringing", "sip-connected"} {
		row, err := app.db().findCall(id)
		if err != nil {
			t.Fatal(err)
		}
		// Never a successful conversation, even for the connected call.
		if row.Status != "failed" || row.TerminationCause != terminationTelephonyRestart || row.TerminationInitiator != "telephony" || row.ErrorMessage != telephonyRestartReason {
			t.Fatalf("%s: status=%q cause=%q initiator=%q error=%q", id, row.Status, row.TerminationCause, row.TerminationInitiator, row.ErrorMessage)
		}
		if row.EndedAt == "" {
			t.Fatalf("%s: no end time", id)
		}
		if row.StateExpiresAt != "" || row.DeadlineAt != "" || row.ConnectedDeadlineAt != "" || row.MediaDeadlineAt != "" {
			t.Fatalf("%s kept deadlines: state=%q setup=%q connected=%q media=%q", id, row.StateExpiresAt, row.DeadlineAt, row.ConnectedDeadlineAt, row.MediaDeadlineAt)
		}
		if topics := callEventTopics(t, app, id); len(topics) != 1 || topics[0] != "call.failed" {
			t.Fatalf("%s events = %v, want one call.failed", id, topics)
		}
		// The expiry worker finds nothing left to terminate.
		if err := app.expireCallAt(ctx, row, now.Add(2*time.Hour)); err != nil {
			t.Fatalf("%s expiry: %v", id, err)
		}
	}
	killed := map[string]bool{}
	for _, thread := range platform.killed {
		killed[thread] = true
	}
	if len(platform.killed) != 2 || !killed["thread-ringing"] || !killed["thread-connected"] {
		t.Fatalf("killed threads = %v", platform.killed)
	}

	finished, _ := app.db().findCall("sip-finished")
	if finished.Status != "completed" || finished.TerminationCause != "" {
		t.Fatalf("finished call changed: status=%q cause=%q", finished.Status, finished.TerminationCause)
	}
	held, _ := app.db().findCall("carrier-held")
	if held.Status != "in-progress" || held.TerminationCause != "" || held.ConnectedDeadlineAt != future {
		t.Fatalf("carrier-held call changed: status=%q cause=%q deadline=%q", held.Status, held.TerminationCause, held.ConnectedDeadlineAt)
	}
	if topics := callEventTopics(t, app, "carrier-held"); len(topics) != 0 {
		t.Fatalf("carrier-held call published %v", topics)
	}

	// A second initialization (a retried one) is a no-op.
	again, err := app.reconcileAbandonedDirectSIPCalls(ctx)
	if err != nil || again != 0 {
		t.Fatalf("second pass reconciled %d, err %v", again, err)
	}
	if len(platform.killed) != 2 {
		t.Fatalf("second pass killed threads again: %v", platform.killed)
	}
}

// A managed runtime starts its gateway before initialization, and a failed
// initialization can be retried in the same process: a dialog the gateway
// still holds is live and must not be ended.
func TestReconcileSkipsLiveDirectSIPDialog(t *testing.T) {
	f := telnyxSIPDialogFixture(t)
	reconciled, err := f.app.reconcileAbandonedDirectSIPCalls(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 0 {
		t.Fatalf("reconciled %d live calls", reconciled)
	}
	row, err := f.app.db().findCall(f.call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if isTerminalStatus(row.Status) || row.TerminationCause != "" {
		t.Fatalf("live call ended: status=%q cause=%q", row.Status, row.TerminationCause)
	}
}

func callEventTopics(t *testing.T, app *App, callID string) []string {
	t.Helper()
	rows, err := app.db().db.Query(`SELECT topic FROM call_events WHERE call_id=? ORDER BY revision`, callID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var topics []string
	for rows.Next() {
		var topic string
		if err := rows.Scan(&topic); err != nil {
			t.Fatal(err)
		}
		topics = append(topics, topic)
	}
	return topics
}
