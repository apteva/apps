package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDefaultTerminationDistinguishesUnansweredAndAnswered(t *testing.T) {
	for _, tc := range []struct{ name, direction, answered, media, status, want string }{
		{"first inbound", "inbound", "", "", "pending", "reject_call"},
		{"carrier answered", "inbound", "2026-09-28T10:00:00Z", "", "pending", "hangup_call"},
		{"media connected", "inbound", "", "2026-09-28T10:00:00Z", "answering", "hangup_call"},
		{"unconfirmed answered status", "inbound", "", "", "answered", "reject_call"},
		{"outbound ringing", "outbound", "", "", "ringing", "hangup_call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, platform, _, row, _ := reliabilityFixture(t)
			row.Direction, row.CarrierAnsweredAt, row.MediaConnectedAt, row.Status = tc.direction, tc.answered, tc.media, tc.status
			if err := a.terminateCarrierCall(ctx, row); err != nil {
				t.Fatal(err)
			}
			if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != tc.want {
				t.Fatalf("wrong termination: %+v", platform.integrationCalls)
			}
			if tc.want == "reject_call" && platform.integrationCalls[0].Input["cause"] != "CALL_REJECTED" {
				t.Fatal("missing final rejection cause")
			}
		})
	}
}

func TestDefaultBurstSuppressionIsGenericAndNeverPlaysMedia(t *testing.T) {
	for _, carrier := range []string{"telnyx", "twilio", "plivo", "bandwidth"} {
		t.Run(carrier, func(t *testing.T) {
			a, ctx, platform, route, _, _ := reliabilityFixture(t)
			route.CarrierSlug = carrier
			if _, err := a.db().db.Exec(`UPDATE inbound_routes SET carrier_slug=? WHERE id=?`, carrier, route.ID); err != nil {
				t.Fatal(err)
			}
			var suppressed *callRow
			for i := 0; i < 13; i++ {
				row, _, err := a.recordInboundCall(route, fmt.Sprintf("%s-%d", carrier, i), "+33611111111", route.PhoneNumber)
				if err != nil {
					t.Fatal(err)
				}
				if i < 12 && row.HandlingReason != "" {
					t.Fatalf("suppressed before threshold: %d", i)
				}
				suppressed = row
			}
			if suppressed.HandlingReason != handlingBurstSuppressed || suppressed.AnnouncementText != "" {
				t.Fatal("unexpected burst policy")
			}
			if err := a.suppressInboundCall(ctx, suppressed); err != nil {
				t.Fatal(err)
			}
			if len(platform.integrationCalls) != 1 {
				t.Fatalf("commands: %+v", platform.integrationCalls)
			}
			for _, cmd := range platform.integrationCalls {
				if cmd.Tool == "answer_call" || cmd.Tool == "speak_text" {
					t.Fatal("burst answered or spoke")
				}
			}
			var count int
			if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM inbound_event_outbox WHERE call_id=?`, suppressed.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("suppressed call offered")
			}
			latest, err := a.db().findCall(suppressed.ID)
			if err != nil || callbackEligible(*latest) {
				t.Fatal("suppressed call entered callback pool")
			}
			other, _, err := a.recordInboundCall(route, carrier+"-other", "+33622222222", route.PhoneNumber)
			if err != nil || other.HandlingReason != "" {
				t.Fatal("unrelated caller blocked")
			}
			stored, _ := a.db().findRoute(route.ID)
			if !stored.Enabled {
				t.Fatal("IVR disabled")
			}
		})
	}
}

func TestDefaultInitialTwilioSuppressionRejectsWithoutAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSuppressedTwilioCall(rec)
	if !strings.Contains(rec.Body.String(), `<Reject reason="rejected"/>`) || strings.Contains(rec.Body.String(), "Say") {
		t.Fatal(rec.Body)
	}
}

func TestDefaultExpiryRechecksFreshClaimAndUsesRejection(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	past := ringTime(time.Now().Add(-time.Minute))
	future := ringTime(time.Now().Add(time.Minute))
	if _, err := a.db().db.Exec(`UPDATE calls SET state_expires_at=?,deadline_at=? WHERE id=?`, past, future, row.ID); err != nil {
		t.Fatal(err)
	}
	stale, _ := a.db().findCall(row.ID)
	if _, err := a.db().db.Exec(`UPDATE calls SET status='answering',peer_token='new-claim',state_expires_at=? WHERE id=?`, future, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.expireCall(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 0 {
		t.Fatal("stale expiry interrupted a fresh claim")
	}
	if _, err := a.db().db.Exec(`UPDATE calls SET status='pending',peer_token='',state_expires_at=? WHERE id=?`, past, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.expireCall(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "reject_call" {
		t.Fatalf("unanswered deadline termination: %+v", platform.integrationCalls)
	}
}

func TestDefaultAnnouncementDuplicateIngressKeepsAnswerBudget(t *testing.T) {
	a, _, platform, route, row, key := reliabilityFixture(t)
	reliabilityPublishTerminal(t, a, route)
	row.CarrierSID = "normal-terminal-no-burst"
	for i := 0; i < 10; i++ {
		if rec := reliabilityEvent(t, a, route, row, key, "call.initiated"); rec.Code != 204 {
			t.Fatal(rec.Code, rec.Body)
		}
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "answer_call" {
		t.Fatal("duplicate callbacks exhausted answer budget")
	}
	stored, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, row.CarrierSID)
	if err != nil || stored.HandlingReason != "" || stored.RoutingResolution == "routing_error" {
		t.Fatal("normal call became burst or error")
	}
	if rec := reliabilityEvent(t, a, route, row, key, "call.answered"); rec.Code != 204 {
		t.Fatal(rec.Code, rec.Body)
	}
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[1].Tool != "speak_text" {
		t.Fatal("configured announcement lost")
	}
}
