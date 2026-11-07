package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/emiago/sipgo/sip"
)

const reviewGToken = "0123456789abcdef0123456789abcdef"

func reviewGInvite(t *testing.T, uri, source string, extra ...string) *sip.Request {
	t.Helper()
	raw := "INVITE " + uri + " SIP/2.0\r\n" +
		"Via: SIP/2.0/TLS 54.172.60.1;branch=z9hG4bK-1\r\n" +
		"From: <sip:+34648257793@carrier.test>;tag=from\r\n" +
		"To: <sip:+12025550100@sip.example.test>\r\n" +
		"Call-ID: review-g@carrier.test\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Contact: <sip:carrier@54.172.60.1>\r\n" +
		strings.Join(extra, "") +
		"Content-Length: 0\r\n\r\n"
	message, err := sip.ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	request := message.(*sip.Request)
	request.SetSource(source)
	return request
}

func reviewGRouteGateway(t *testing.T, carrier string, state *directSIPProviderConfig) (*sipGateway, *routeRow) {
	t.Helper()
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	route := routeRow{ID: "review-g-route", ProjectID: "project-a", CarrierSlug: carrier, CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret", InboundTransport: inboundTransportSIPDirect}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if state != nil {
		encoded, _ := json.Marshal(state)
		if err := app.db().updateRouteTransport(route.ID, inboundTransportSIPDirect, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := directSIPTestConfig()
	cfg.AllowedCIDRs, cfg.CarrierCIDRs, _ = parseSIPCarrierCIDRs(strings.Join(defaultSIPCarrierCIDRs, ",") + ",203.0.113.0/24")
	stored, _ := app.db().findRoute(route.ID)
	return &sipGateway{app: app, appCtx: ctx, cfg: cfg}, stored
}

func TestReviewGSIPSourceCarrierAttribution(t *testing.T) {
	_, labelled, err := parseSIPCarrierCIDRs("twilio=198.51.100.0/24, 203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	cfg := sipGatewayConfig{CarrierCIDRs: labelled}
	for source, want := range map[string]string{
		"54.172.60.1:5061":   "twilio",
		"192.76.120.10:5061": "telnyx",
		"46.19.209.1:5061":   "didww",
		"198.51.100.7:5061":  "twilio",
		"203.0.113.9:5061":   "",
	} {
		if got := cfg.sourceCarrier(source); got != want {
			t.Errorf("sourceCarrier(%s)=%q want %q", source, got, want)
		}
	}
	if _, _, err := parseSIPCarrierCIDRs("Bad Label=198.51.100.0/24"); err == nil {
		t.Fatal("invalid carrier label accepted")
	}
}

func TestReviewGSIPRouteRejectsOtherCarrierNetwork(t *testing.T) {
	gateway, _ := reviewGRouteGateway(t, "twilio", nil)
	// A Telnyx customer reaching a Twilio route's number is rejected.
	_, _, err := gateway.findRouteForInvite(reviewGInvite(t, "sip:+12025550100@sip.example.test", "192.76.120.10:5061"))
	if !errors.Is(err, errSIPRouteForbidden) {
		t.Fatalf("cross-carrier INVITE err=%v", err)
	}
	// The route's own carrier keeps working without a secret (legacy route).
	route, _, err := gateway.findRouteForInvite(reviewGInvite(t, "sip:+12025550100@sip.example.test", "54.172.60.1:5061"))
	if err != nil || route == nil {
		t.Fatalf("own-carrier legacy INVITE route=%v err=%v", route, err)
	}
	if status := directSIPRouteAuthStatus(*route); status["route_secret"] != "missing" {
		t.Fatalf("legacy route diagnostics=%v", status)
	}
}

func TestReviewGSIPRouteForwardingHeadersAreCarrierBoundFallback(t *testing.T) {
	gateway, _ := reviewGRouteGateway(t, "twilio", nil)
	diversion := "Diversion: <sip:+12025550100@forwarder.test>\r\n"
	// The R-URI names no route; only the forwarded number does.
	for source, want := range map[string]bool{"192.76.120.10:5061": false, "203.0.113.9:5061": false, "54.172.60.1:5061": true} {
		request := reviewGInvite(t, "sip:+19995550000@sip.example.test", source, diversion)
		request.To().Address.User = "+19995550000"
		route, _, err := gateway.findRouteForInvite(request)
		if err != nil || (route != nil) != want {
			t.Errorf("source %s: forwarded route=%v err=%v want found=%v", source, route, err, want)
		}
	}
}

func TestReviewGSIPRouteSecretEnforcedAfterFirstDelivery(t *testing.T) {
	gateway, _ := reviewGRouteGateway(t, "twilio", &directSIPProviderConfig{Provider: "twilio", TrunkID: "TK1", InboundToken: reviewGToken})
	source := "54.172.60.1:5061"
	plain := "sip:+12025550100@sip.example.test"
	// Pending: a carrier that never delivers the secret is not broken.
	if route, _, err := gateway.findRouteForInvite(reviewGInvite(t, plain, source)); err != nil || route == nil {
		t.Fatalf("pending route rejected: %v", err)
	}
	// A wrong secret is always rejected.
	if _, _, err := gateway.findRouteForInvite(reviewGInvite(t, plain+";x-apteva-token=ffffffffffffffffffffffffffffffff", source)); !errors.Is(err, errSIPRouteForbidden) {
		t.Fatalf("wrong secret err=%v", err)
	}
	route, _, err := gateway.findRouteForInvite(reviewGInvite(t, plain+";x-apteva-token="+reviewGToken, source))
	if err != nil || route == nil {
		t.Fatalf("secret INVITE rejected: %v", err)
	}
	stored, _ := gateway.app.db().findRoute(route.ID)
	if status := directSIPRouteAuthStatus(*stored); status["route_secret"] != "enforced" {
		t.Fatalf("route secret not enforced after delivery: %v", status)
	}
	// Enforced: same carrier network without the secret is now rejected.
	if _, _, err := gateway.findRouteForInvite(reviewGInvite(t, plain, source)); !errors.Is(err, errSIPRouteForbidden) {
		t.Fatalf("missing secret after enforcement err=%v", err)
	}
	// User-part and header carriers of the secret are accepted too.
	if route, _, err := gateway.findRouteForInvite(reviewGInvite(t, "sip:+12025550100-"+reviewGToken+"@sip.example.test", source)); err != nil || route == nil {
		t.Fatalf("user-part secret rejected: %v", err)
	}
	if route, _, err := gateway.findRouteForInvite(reviewGInvite(t, plain, source, "X-Apteva-Route-Token: "+reviewGToken+"\r\n")); err != nil || route == nil {
		t.Fatalf("header secret rejected: %v", err)
	}
}

func TestReviewGDIDWWProvisioningCarriesRouteSecret(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"list_dids":            json.RawMessage(`{"data":[{"id":"did-1","attributes":{"number":"33123456789"},"relationships":{}}]}`),
		"create_inbound_trunk": json.RawMessage(`{"data":{"type":"voice_in_trunks","id":"new-trunk"}}`),
	}}
	app, ctx := withTelephonyTestContext(t, platform)
	route := routeRow{ID: "didww-route", ProjectID: "project-a", CarrierSlug: "didww", CarrierConnectionID: 19,
		PhoneNumber: "+33123456789", AgentID: 7, Enabled: true, Secret: "secret", InboundTransport: inboundTransportSIPDirect}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.db().findRoute(route.ID)
	if err := app.configureDIDWWDirectSIP(ctx, stored, directSIPTestConfig()); err != nil {
		t.Fatal(err)
	}
	stored, _ = app.db().findRoute(route.ID)
	state, ok := directSIPRouteSecret(stored)
	username := platform.integrationCalls[1].Input["configuration"].(map[string]any)["attributes"].(map[string]any)["username"]
	if !ok || username != "+33123456789-"+state.InboundToken {
		t.Fatalf("DIDWW trunk username=%v state=%+v", username, state)
	}
}

func TestReviewGLegacyDIDWWRouteSecretUpgrade(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"update_inbound_trunk": json.RawMessage(`{"data":{"type":"voice_in_trunks","id":"trunk-1"}}`),
	}}
	app, ctx := withTelephonyTestContext(t, platform)
	cfg := directSIPTestConfig()
	app.sip.mu.Lock()
	app.sip.gateway = &sipGateway{cfg: cfg}
	app.sip.mu.Unlock()
	defer func() { app.sip.mu.Lock(); app.sip.gateway = nil; app.sip.mu.Unlock() }()
	route := routeRow{ID: "didww-legacy", ProjectID: "project-a", CarrierSlug: "didww", CarrierConnectionID: 19,
		PhoneNumber: "+33123456789", AgentID: 7, Enabled: true, Secret: "secret", InboundTransport: inboundTransportSIPDirect}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if err := app.db().updateRouteTransport(route.ID, inboundTransportSIPDirect, `{"provider":"didww","trunk_id":"trunk-1"}`); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.db().findRoute(route.ID)
	if err := app.upgradeDirectSIPRouteSecret(ctx, stored); err != nil {
		t.Fatal(err)
	}
	stored, _ = app.db().findRoute(route.ID)
	state, ok := directSIPRouteSecret(stored)
	if !ok || state.TrunkID != "trunk-1" || len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "update_inbound_trunk" ||
		directSIPRouteAuthStatus(*stored)["route_secret"] != "pending_first_call" {
		t.Fatalf("legacy upgrade state=%+v calls=%#v", state, platform.integrationCalls)
	}
}

func TestReviewGDIDWWHasTLSPeerName(t *testing.T) {
	t.Setenv("TELEPHONY_SIP_ALLOWED_CIDRS", "")
	cfg, err := loadSIPGatewayConfig(sdk.Config{
		"sip_enabled": "true", "sip_transport": "udp", "sip_allow_insecure_signaling": "true",
		"sip_public_host": "sip.example.test", "sip_public_ip": "198.51.100.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSPeerNames["didww"] != defaultDIDWWTLSPeerName {
		t.Fatalf("TLS peer names=%v", cfg.TLSPeerNames)
	}
	if cfg.sourceCarrier("46.19.209.1:5061") != "didww" || !cfg.sourceAllowed("46.19.209.1:5061") {
		t.Fatal("default DIDWW range not attributed")
	}
}
