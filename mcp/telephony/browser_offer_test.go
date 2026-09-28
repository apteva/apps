package main

import (
	"net/http"
	"testing"
)

func TestBrowserOfferAcknowledgementAndDeclineAreScoped(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "browser-offer")
	alice, eve := phoneTestIdentity("alice"), phoneTestIdentity("eve")
	request := map[string]any{"offer_id": "sales-browser-offer"}
	if w := phoneTestRequest(a, &eve, "POST", "/softphone/offer/ack/browser-offer", request); w.Code != http.StatusNotFound {
		t.Fatalf("other adviser acknowledged offer: %d %s", w.Code, w.Body)
	}
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/offer/ack/browser-offer", request); w.Code != http.StatusOK {
		t.Fatalf("receipt: %d %s", w.Code, w.Body)
	}
	var at string
	if err := a.db().db.QueryRow(`SELECT acknowledged_at FROM call_offers WHERE id='sales-browser-offer'`).Scan(&at); err != nil || at == "" {
		t.Fatalf("receipt missing: %q %v", at, err)
	}
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/offer/decline/browser-offer", request); w.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", w.Code, w.Body)
	}
	var status string
	if err := a.db().db.QueryRow(`SELECT status FROM call_offers WHERE id='sales-browser-offer'`).Scan(&status); err != nil || status != "declined" {
		t.Fatalf("offer status: %q %v", status, err)
	}
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/offer/ack/browser-offer", request); w.Code != http.StatusNotFound {
		t.Fatalf("stale receipt: %d %s", w.Code, w.Body)
	}
	if answerable, code := phoneTestAnswerable(t, a, &eve, "browser-offer"); code != http.StatusOK || !answerable {
		t.Fatalf("next adviser not offered: %d %t", code, answerable)
	}
}

func TestCallClassificationAndOneCallbackKey(t *testing.T) {
	base := callRow{ID: "inbound-one", Direction: "inbound", Status: "completed", PeerKind: peerKindHuman}
	if got := callbackOpportunityID(base); got != "callback:inbound-one" {
		t.Fatal(got)
	}
	if got := callClassification(base); got != "unhandled" {
		t.Fatal(got)
	}
	base.RoutingResolution = "routing_exhausted"
	if got := callClassification(base); got != "routing_exhausted" {
		t.Fatal(got)
	}
	base.TerminationInitiator = "caller"
	if got := callClassification(base); got != "caller_abandoned" {
		t.Fatal(got)
	}
	base.TerminationInitiator = ""
	base.MediaConnectedAt = "connected"
	if callbackEligible(base) || callClassification(base) != "human_connected" {
		t.Fatalf("human handled call misclassified: %s", callClassification(base))
	}
	base.PeerKind = peerKindRealtime
	if callbackEligible(base) || callClassification(base) != "ai_handled" {
		t.Fatal("AI handling misclassified")
	}
	base.CallbackOnAI = true
	if callbackOpportunityID(base) != "callback:inbound-one" {
		t.Fatal("AI callback policy ignored")
	}
	base.HandlingReason = handlingClosedHours
	if callbackEligible(base) || callClassification(base) != handlingClosedHours {
		t.Fatal("closed-hours call projected as missed")
	}
	base.HandlingReason = handlingBurstSuppressed
	if callbackEligible(base) || callClassification(base) != handlingBurstSuppressed {
		t.Fatal("suppressed call projected as missed")
	}
}
