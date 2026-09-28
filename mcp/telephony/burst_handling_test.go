package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func insertBurstTestCall(t *testing.T, a *App, template *callRow, id string, policy burstHandlingPolicy) *callRow {
	t.Helper()
	row := *template
	row.ID = id
	row.ThreadID = "pending-" + id
	row.CarrierSID = "carrier-" + id
	row.HandlingReason = handlingBurstSuppressed
	row.ErrorMessage = burstPerCaller
	stored, _, err := a.db().insertInboundCallWithPolicy(row, "must not offer", policy)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}
func burstTestPolicy() burstHandlingPolicy {
	return loadBurstHandlingPolicy(sdk.Config{"inbound_burst_action": burstAnswerAnnouncement, "inbound_burst_message": "Nous ne pouvons pas prendre votre appel. Au revoir.", "inbound_burst_language": "fr-FR"})
}

func TestBurstCompletionAnswersWithoutAdviserOrMissedCall(t *testing.T) {
	a, ctx, platform, route, template, key := reliabilityFixture(t)
	row := insertBurstTestCall(t, a, template, "burst-message", burstTestPolicy())
	if err := a.suppressInboundCall(ctx, row); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "answer_call" {
		t.Fatalf("initial action: %+v", platform.integrationCalls)
	}
	// No published routing flow is needed for suppression callbacks.
	if row.RoutingFlowVersionID != "" {
		t.Fatal("unexpected flow")
	}
	for _, event := range []string{"call.answered", "call.answered"} {
		if r := reliabilityEvent(t, a, route, row, key, event); r.Code != 204 {
			t.Fatal(r.Code, r.Body)
		}
	}
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[1].Tool != "speak_text" || platform.integrationCalls[1].Input["language"] != "fr-FR" {
		t.Fatalf("speech: %+v", platform.integrationCalls)
	}
	if r := reliabilityEvent(t, a, route, row, key, "call.speak.ended", map[string]any{"client_state": terminalAnnouncementClientState(row.ID), "status": "completed"}); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	if len(platform.integrationCalls) != 3 || platform.integrationCalls[2].Tool != "hangup_call" {
		t.Fatalf("completion: %+v", platform.integrationCalls)
	}
	// Duplicate completion and restart cannot issue more carrier commands.
	reliabilityEvent(t, a, route, row, key, "call.speak.ended", map[string]any{"client_state": terminalAnnouncementClientState(row.ID), "status": "completed"})
	if err := (&App{}).runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 3 {
		t.Fatal("duplicate carrier commands")
	}
	current, err := a.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !isTerminalStatus(current.Status) || callClassification(*current) != handlingBurstSuppressed || callbackEligible(*current) {
		t.Fatalf("bad classification: %+v", current)
	}
	for _, table := range []string{"call_offers", "telephony_call_owners", "phone_capacity", "inbound_event_outbox"} {
		var count int
		if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE call_id=?`, row.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s: %d %v", table, count, err)
		}
	}
	if route, err := a.db().findRoute(route.ID); err != nil || !route.Enabled {
		t.Fatal("IVR disabled")
	}
	result, err := a.toolCallGet(context.Background(), ctx, map[string]any{"call_id": row.ID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "configured_announcement") {
		t.Fatal("missing disposition evidence")
	}
}

func TestBurstCompletionMissingCallbacksAreBounded(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(fmt.Sprint(answered), func(t *testing.T) {
			a, ctx, platform, route, template, key := reliabilityFixture(t)
			row := insertBurstTestCall(t, a, template, "missing-callback", burstTestPolicy())
			if err := a.suppressInboundCall(ctx, row); err != nil {
				t.Fatal(err)
			}
			if answered {
				if r := reliabilityEvent(t, a, route, row, key, "call.answered"); r.Code != 204 {
					t.Fatal(r.Code, r.Body)
				}
			}
			if _, err := a.db().db.Exec(`UPDATE calls SET deadline_at=? WHERE id=?`, ringTime(time.Now().Add(-time.Second)), row.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := a.db().db.Exec(`UPDATE routing_effects SET next_attempt_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID); err != nil {
				t.Fatal(err)
			}
			if err := (&App{}).runRoutingEffects(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			if platform.integrationCalls[len(platform.integrationCalls)-1].Tool != "hangup_call" {
				t.Fatalf("missing bounded cleanup: %+v", platform.integrationCalls)
			}
			current, _ := a.db().findCall(row.ID)
			if !isTerminalStatus(current.Status) {
				t.Fatal("capacity not released")
			}
		})
	}
}

func TestBurstCompletionCapacityAndExplicitBlocks(t *testing.T) {
	a, ctx, platform, _, template, _ := reliabilityFixture(t)
	policy := burstTestPolicy()
	policy.MaxConcurrent = 1
	first := insertBurstTestCall(t, a, template, "slot-one", policy)
	second := insertBurstTestCall(t, a, template, "slot-two", policy)
	if first.AnnouncementText == "" || second.AnnouncementText != "" {
		t.Fatal("capacity limit failed")
	}
	if err := a.suppressInboundCall(ctx, second); err != nil {
		t.Fatal(err)
	}
	if platform.integrationCalls[0].Tool != "reject_call" || platform.integrationCalls[0].Input["cause"] != "CALL_REJECTED" {
		t.Fatal("capacity fallback not rejection")
	}
	// Replayed ingress retains its original disposition even after policy changes.
	duplicate, created, err := a.db().insertInboundCallWithPolicy(*first, "", loadBurstHandlingPolicy(nil))
	if err != nil || created || duplicate.AnnouncementText != first.AnnouncementText {
		t.Fatal("replay changed disposition")
	}
	if err := a.db().updateStatus(first.ID, "canceled", ""); err != nil {
		t.Fatal(err)
	}
	third := insertBurstTestCall(t, a, template, "slot-three", policy)
	if third.AnnouncementText == "" {
		t.Fatal("terminal call retained capacity")
	}
	blocked := *template
	blocked.ID = "blocked"
	blocked.ThreadID = "pending-blocked"
	blocked.CarrierSID = "blocked"
	blocked.HandlingReason = handlingSpamSuppressed
	blocked.ErrorMessage = blockedCaller
	saved, _, err := a.db().insertInboundCallWithPolicy(blocked, "", policy)
	if err != nil || saved.AnnouncementText != "" {
		t.Fatal("explicit block answered")
	}
}

func TestBurstCompletionCancellationAndFailureRecovery(t *testing.T) {
	a, ctx, platform, route, template, key := reliabilityFixture(t)
	row := insertBurstTestCall(t, a, template, "retry-speak", burstTestPolicy())
	if err := a.suppressInboundCall(ctx, row); err != nil {
		t.Fatal(err)
	}
	platform.failTool = "speak_text"
	if r := reliabilityEvent(t, a, route, row, key, "call.answered"); r.Code != 503 {
		t.Fatal(r.Code, r.Body)
	}
	platform.failTool = ""
	if _, err := a.db().db.Exec(`UPDATE routing_effects SET next_attempt_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 3 || platform.integrationCalls[1].Input["command_id"] != platform.integrationCalls[2].Input["command_id"] {
		t.Fatal("lost speech retry")
	}
	if r := reliabilityEvent(t, a, route, row, key, "call.hangup"); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	reliabilityEvent(t, a, route, row, key, "call.speak.ended", map[string]any{"client_state": terminalAnnouncementClientState(row.ID), "status": "completed"})
	if err := a.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 3 {
		t.Fatal("canceled caller controlled again")
	}
}

func TestBurstCompletionConcurrentAdmissionIsBounded(t *testing.T) {
	a, _, _, _, template, _ := reliabilityFixture(t)
	policy := burstTestPolicy()
	policy.MaxConcurrent = 2
	var wg sync.WaitGroup
	results := make(chan *callRow, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row := *template
			row.ID = fmt.Sprintf("parallel-%d", i)
			row.ThreadID = "pending-" + row.ID
			row.CarrierSID = row.ID
			row.HandlingReason = handlingBurstSuppressed
			row.ErrorMessage = burstPerCaller
			stored, _, err := a.db().insertInboundCallWithPolicy(row, "", policy)
			if err != nil {
				errs <- err
			} else {
				results <- stored
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	count := 0
	for row := range results {
		if row.AnnouncementText != "" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("capacity exceeded: %d", count)
	}
}

func TestBurstCompletionDefaultAndUnsupportedCarriersReject(t *testing.T) {
	a, _, _, _, template, _ := reliabilityFixture(t)
	if row := insertBurstTestCall(t, a, template, "default", loadBurstHandlingPolicy(nil)); row.AnnouncementText != "" {
		t.Fatal("answer enabled by default")
	}
	for _, carrier := range []string{"twilio", "bandwidth", "plivo", "didww"} {
		row := *template
		row.CarrierSlug = carrier
		if stored := insertBurstTestCall(t, a, &row, carrier, burstTestPolicy()); stored.AnnouncementText != "" {
			t.Fatalf("unsupported %s answered", carrier)
		}
	}
	if _, err := a.db().db.Exec(`UPDATE inbound_routes SET inbound_transport=? WHERE id=?`, inboundTransportSIPDirect, template.RouteID); err != nil {
		t.Fatal(err)
	}
	if row := insertBurstTestCall(t, a, template, "direct-sip", burstTestPolicy()); row.AnnouncementText != "" {
		t.Fatal("SIP entered programmable answering")
	}
	p := loadBurstHandlingPolicy(sdk.Config{"inbound_burst_max_seconds": "0", "inbound_burst_max_concurrent": "999", "inbound_burst_message": strings.Repeat("x", 241)})
	if p.MaxSeconds != 20 || p.MaxConcurrent != 3 || len(p.Message) > 240 {
		t.Fatal("invalid limits accepted")
	}
}

func TestBurstCompletionSignedIngressPinsPolicyAndDeduplicatesAnswer(t *testing.T) {
	a, _, platform, route, template, key := reliabilityFixture(t)
	db := a.db().db
	globalCtx = sdk.NewAppCtxForTest(&sdk.Manifest{}, db, sdk.Config{"inbound_burst_action": burstAnswerAnnouncement, "inbound_burst_per_caller": "1", "inbound_burst_per_number": "1", "inbound_burst_message": "Please end this call.", "inbound_burst_language": "en-US"}, platform, nil).WithProject(route.ProjectID)
	original := *template
	original.CarrierSID = "first-real-attempt"
	if r := reliabilityEvent(t, a, route, &original, key, "call.initiated"); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	original.CarrierSID = "second-real-attempt"
	for i := 0; i < 10; i++ {
		if r := reliabilityEvent(t, a, route, &original, key, "call.initiated"); r.Code != 204 {
			t.Fatal(r.Code, r.Body)
		}
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "answer_call" {
		t.Fatalf("duplicate ingress repeated accepted answer: %+v", platform.integrationCalls)
	}
	stored, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, original.CarrierSID)
	if err != nil || stored.HandlingReason != handlingBurstSuppressed || stored.AnnouncementText != "Please end this call." {
		t.Fatalf("ingress disposition: %+v %v", stored, err)
	}
	// A different caller survives the destination-wide alert, even while this
	// announcement uses capacity. The public route stays assigned and enabled.
	original.CarrierSID = "other-caller"
	original.FromNumber = "+33622222222"
	if r := reliabilityEvent(t, a, route, &original, key, "call.initiated"); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	other, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, original.CarrierSID)
	if err != nil || other.HandlingReason != "" {
		t.Fatal("destination alert blocked unrelated caller")
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbound_burst_attempts WHERE project_id=?`, route.ProjectID).Scan(&attempts); err != nil || attempts != 3 {
		t.Fatalf("webhook retries counted: %d %v", attempts, err)
	}
}
