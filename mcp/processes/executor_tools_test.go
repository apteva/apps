package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestExecutorAttachmentFailurePreventsDispatchAndPersistsWarning(t *testing.T) {
	for _, unapplied := range []bool{false, true} {
		a, f, _ := directSetup(t)
		f.attachUnapplied = unapplied
		if !unapplied {
			f.attachError = errors.New("current install MCP unavailable")
		}
		p := status(t, a, create(t, a, workflowDefinition()).ID, "active")
		_, err := a.start(p.ProjectID, p.ID, "attachment-failure", "")
		if err == nil || len(f.threads) != 0 || len(f.events) != 0 {
			t.Fatalf("dispatched without applied attachment: %v", err)
		}
		var warning string
		if err = a.db.QueryRow(`SELECT delivery_warning FROM process_step_runs WHERE state='ready' LIMIT 1`).Scan(&warning); err != nil || !strings.Contains(warning, "Processes tools") {
			t.Fatalf("missing durable provisioning error: %q %v", warning, err)
		}
	}
}

func TestUnclaimedRecoveryRepairsLegacyWorkerBeforeWake(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	var raw string
	if err := a.db.QueryRow(`SELECT spawn_json FROM process_delivery_envelopes WHERE event_id=?`, s.DeliveryEventID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original sdk.ThreadSpawnRequest
	if err := json.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	original.MCP = []string{"processes"}
	frozen, _ := json.Marshal(original)
	if _, err := a.db.Exec(`UPDATE process_delivery_envelopes SET spawn_json=? WHERE event_id=?`, string(frozen), s.DeliveryEventID); err != nil {
		t.Fatal(err)
	}
	all, _ := a.steps(r.ID)
	delivered, _ := time.Parse(time.RFC3339Nano, s.DeliveredAt)
	now := delivered.Add(unclaimedStepAfter + time.Second)
	count := len(f.events)
	f.attachError = errors.New("stale endpoint")
	if err := a.recoverUnclaimedStep(p, r, s, all, now); err == nil {
		t.Fatal("expected provisioning failure")
	}
	if len(f.events) != count || len(f.profiles) != 0 || stepBy(t, a, r, s.Key).ClaimAttempts != 0 {
		t.Fatal("woke unusable worker or consumed claim attempt")
	}
	f.attachError = nil
	s = stepBy(t, a, r, s.Key)
	if err := a.recoverUnclaimedStep(p, r, s, all, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.profiles) != 1 || f.profiles[0].MCP != nil || f.profiles[0].ThreadID != s.ThreadID || f.profiles[0].AgentID != s.Executor.AgentID || f.profiles[0].DirectiveSuffix != original.DirectiveSuffix || len(f.profiles[0].Events) != 0 {
		t.Fatalf("profile lost checkpoint or domain scopes: %+v", f.profiles)
	}
	if len(f.events) != count+1 || len(f.threads) != 1 {
		t.Fatal("recovery did not reuse existing worker")
	}
	if err := a.db.QueryRow(`SELECT spawn_json FROM process_delivery_envelopes WHERE event_id=?`, s.DeliveryEventID).Scan(&raw); err != nil || raw != string(frozen) {
		t.Fatal("immutable provisioning envelope changed", err)
	}
}

func TestDirectAttachmentFailurePreventsDelivery(t *testing.T) {
	a, f, p := directSetup(t)
	f.attachError = errors.New("attachment unavailable")
	if _, err := a.start(p.ProjectID, p.ID, "direct-attachment", ""); err == nil || len(f.events) != 0 {
		t.Fatal("direct work dispatched without tools", err)
	}
}

func TestUnclaimedRecoveryProfileFailureDoesNotWakeWorker(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	all, _ := a.steps(r.ID)
	delivered, _ := time.Parse(time.RFC3339Nano, s.DeliveredAt)
	f.profileError = errors.New("runtime profile update unavailable")
	events := len(f.events)
	if err := a.recoverUnclaimedStep(p, r, s, all, delivered.Add(unclaimedStepAfter+time.Second)); err == nil {
		t.Fatal("expected profile failure")
	}
	current := stepBy(t, a, r, s.Key)
	if len(f.events) != events || current.ClaimAttempts != 0 || !strings.Contains(current.DeliveryWarning, "reconcile worker tools") {
		t.Fatalf("failed profile repair woke worker or lost blocker: %+v", current)
	}
}

func TestLegacyProvisionedWorkerRepairsScopesBeforeNextStep(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	var raw string
	if err := a.db.QueryRow(`SELECT spawn_json FROM process_delivery_envelopes WHERE event_id=?`, s.DeliveryEventID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var request sdk.ThreadSpawnRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	request.MCP = []string{"processes"}
	frozen, _ := json.Marshal(request)
	if _, err := a.db.Exec(`UPDATE process_delivery_envelopes SET spawn_json=? WHERE event_id=?`, string(frozen), s.DeliveryEventID); err != nil {
		t.Fatal(err)
	}
	// A retry of an acknowledged spawn repairs its old capability ceiling.
	if err := a.ensureProcessThread(p.ProjectID, s.DeliveryEventID, request); err != nil {
		t.Fatal(err)
	}
	if len(f.profiles) != 1 || f.profiles[0].MCP != nil || len(f.threads) != 1 {
		t.Fatal("legacy spawn retry did not repair the same worker")
	}
	actor := "agent:7:" + s.ThreadID
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_claim", nil); err != nil {
		t.Fatal(err)
	}
	finishStep(t, a, p, r, s.Key, actor, "research evidence", "")
	if len(f.profiles) != 2 || len(f.threads) != 1 || stepBy(t, a, r, "write").ThreadID != s.ThreadID {
		t.Fatal("legacy handoff lost worker or excluded domain tools")
	}
	if err := a.db.QueryRow(`SELECT spawn_json FROM process_delivery_envelopes WHERE event_id=?`, s.DeliveryEventID).Scan(&raw); err != nil || raw != string(frozen) {
		t.Fatal("immutable spawn request changed", err)
	}
}
