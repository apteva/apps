package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// addInventoryNumber adds a Call Control number with its route and
// application, like inventoryFixture's routed numbers.
func addInventoryNumber(t *testing.T, a *App, p *inventoryTestPlatform, i int) {
	t.Helper()
	id, phone := fmt.Sprintf("application-%02d", i), fmt.Sprintf("+33189313%03d", i)
	config, _ := json.Marshal(telnyxRouteConfig{ApplicationID: id})
	route := routeRow{ID: fmt.Sprintf("route-%d", i), ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10, PhoneNumber: phone, PhoneNumberSID: fmt.Sprintf("number-%d", i), Enabled: true, PreviousVoiceURL: string(config), Secret: "test-secret", AnswerMode: answerModeHumanBrowser}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.numbers = append(p.numbers, map[string]any{"id": route.PhoneNumberSID, "phone_number": phone, "connection_id": id, "features": []string{"voice"}})
	p.apps[id] = map[string]any{"id": id, "active": true, "outbound": map[string]any{"outbound_voice_profile_id": "profile-default"}, "webhook_event_url": a.inboundRouteURL(route)}
}

func (p *inventoryTestPlatform) toolCalls(tool string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for key, v := range p.counts {
		if strings.Contains(key, ":"+tool+":") {
			n += v
		}
	}
	return n
}

func TestTelnyxApplicationsOneListForSixteenNumbers(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	for i := 11; i < 16; i++ {
		addInventoryNumber(t, a, p, i)
	}
	views := inventoryViews(t, a, ctx, context.Background())
	if len(views) != 16 {
		t.Fatalf("numbers=%d want 16", len(views))
	}
	if got := p.applicationCalls(); got > 2 {
		t.Fatalf("Telnyx application requests=%d want at most 2", got)
	}
	if got := p.toolCalls("get_call_control_application"); got != 0 {
		t.Fatalf("separate application reads=%d want 0", got)
	}
	for _, v := range views {
		if v.Outbound.Status != outboundReady {
			t.Fatalf("outbound not ready from the list: %+v", v.Outbound)
		}
		if v.Route != nil && v.RoutingHealth != "healthy" {
			t.Fatalf("route not verified from the list: %s %s", v.RoutingHealth, v.HealthMessage)
		}
	}
}

func TestTelnyxApplicationMissingFromTruncatedListReadsOnce(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	// Applications sort before "application-": ten full pages hold every
	// number's application except application-10.
	for i := 0; i < maxApplicationListPages*inventoryApplicationPageSize-10; i++ {
		id := fmt.Sprintf("aaa-other-%04d", i)
		p.apps[id] = map[string]any{"id": id, "active": true}
	}
	views := inventoryViews(t, a, ctx, context.Background())
	if got := p.toolCalls("list_call_control_applications"); got != maxApplicationListPages {
		t.Fatalf("list pages=%d want %d", got, maxApplicationListPages)
	}
	p.mu.Lock()
	gets, missed := p.counts["10:get_call_control_application:application-10"], p.toolCallsUnsafe("get_call_control_application")
	p.mu.Unlock()
	if gets != 1 || missed != 1 {
		t.Fatalf("fallback reads: application-10=%d total=%d, want one read of application-10", gets, missed)
	}
	for _, v := range views {
		if v.Outbound.Status != outboundReady {
			t.Fatalf("outbound not ready: %+v", v.Outbound)
		}
	}
}

func (p *inventoryTestPlatform) toolCallsUnsafe(tool string) int {
	n := 0
	for key, v := range p.counts {
		if strings.Contains(key, ":"+tool+":") {
			n += v
		}
	}
	return n
}

func TestTelnyxApplicationListRateLimitKeepsVerifiedOutbound(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	for _, v := range inventoryViews(t, a, ctx, context.Background()) {
		if v.Outbound.Status != outboundReady {
			t.Fatalf("setup not verified: %+v", v.Outbound)
		}
	}
	a.inventoryReads.invalidate()
	p.mu.Lock()
	p.counts = map[string]int{}
	p.listStatus = 429
	p.listHeaders = map[string]string{"Retry-After": "30"}
	p.mu.Unlock()
	fresh := inventoryFreshContext(context.Background(), true)
	views := inventoryViews(t, a, ctx, fresh)
	for _, v := range views {
		if v.Route == nil {
			// Only configured outbound numbers may use a verified fallback.
			continue
		}
		if v.Outbound.Status != outboundReady || v.OutboundVerification == nil || v.OutboundVerification.Status != previouslyVerified {
			t.Fatalf("429 replaced a verified result: %+v verification=%+v", v.Outbound, v.OutboundVerification)
		}
		if !strings.Contains(v.HealthMessage, "rate-limited") {
			t.Fatalf("route health hides the rate limit: %q", v.HealthMessage)
		}
	}
	if got, gets := p.toolCalls("list_call_control_applications"), p.toolCalls("get_call_control_application"); got != 1 || gets != 0 {
		t.Fatalf("rate-limited account got list=%d get=%d, want one list and no reads", got, gets)
	}
	// Retry-After pauses the list for later views, fresh ones included.
	inventoryViews(t, a, ctx, inventoryFreshContext(context.Background(), true))
	inventoryViews(t, a, ctx, context.Background())
	if got := p.toolCalls("list_call_control_applications"); got != 1 {
		t.Fatalf("list requested %d times during Retry-After", got)
	}
}

func TestTelnyxApplicationRateLimitWithoutVerification(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	p.listStatus = 429
	for _, v := range inventoryViews(t, a, ctx, context.Background()) {
		if v.Outbound.Status != outboundConfigError {
			t.Fatalf("unverified number allowed outbound: %+v", v.Outbound)
		}
		if !strings.Contains(v.Outbound.Message, "rate-limited") || strings.Contains(v.Outbound.Message, "status=429") {
			t.Fatalf("unclear rate-limit message: %q", v.Outbound.Message)
		}
	}
	if gets := p.toolCalls("get_call_control_application"); gets != 0 {
		t.Fatalf("separate reads after a rate-limited list: %d", gets)
	}
}

func TestTelnyxApplicationRateLimitStopsFallbackReads(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	p.listStatus = 403 // an unusable list: read applications separately
	p.getStatus = 429
	for _, v := range inventoryViews(t, a, ctx, context.Background()) {
		if !strings.Contains(v.Outbound.Message, "rate-limited") {
			t.Fatalf("unclear rate-limit message: %q", v.Outbound.Message)
		}
	}
	if got := p.toolCalls("get_call_control_application"); got > inventoryApplicationConcurrency {
		t.Fatalf("reads=%d after a 429, want at most the %d already in flight", got, inventoryApplicationConcurrency)
	}
}

func TestTelnyxApplicationNotFoundMessage(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	p.missing = map[string]bool{"application-03": true}
	profiles := []outboundProfileOption{{ID: "profile-default", Name: "Default", Enabled: true}}
	view, err := a.telnyxOutboundReadiness(ctx, 10, "application-03", profiles)
	if err == nil || view.Status != outboundConfigError {
		t.Fatalf("missing application accepted: %+v", view)
	}
	if !strings.Contains(err.Error(), "application-03 no longer exists") || strings.Contains(err.Error(), "status=404") {
		t.Fatalf("unclear not-found message: %q", err)
	}
	if carrierErrorStatus(err) != 404 || temporaryCarrierVerificationError(err) {
		t.Fatal("not-found lost its status or became temporary")
	}
}

func TestTelnyxSIPDirectNumbersReadNoApplications(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	sipConfig, _ := json.Marshal(directSIPProviderConfig{Provider: "telnyx", FQDNConnectionID: "fqdn-1", PreviousConnectionID: "application-old"})
	if _, err := a.db().db.Exec(`UPDATE inbound_routes SET inbound_transport='sip_direct', transport_config=?`, string(sipConfig)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db().db.Exec(`INSERT INTO outbound_transports(project_id,connection_id,phone_number,provider,transport) VALUES('project-a',10,'','telnyx','sip_direct')`); err != nil {
		t.Fatal(err)
	}
	inventoryViews(t, a, ctx, context.Background())
	if got := p.applicationCalls(); got != 0 {
		t.Fatalf("direct SIP numbers made %d Call Control application requests", got)
	}
}
