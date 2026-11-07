package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"net/netip"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestTemporaryCarrierVerificationErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 408, 409, 429, 500, 502, 503, 504} {
		want := status == 408 || status == 429 || status == 500 || status == 502 || status == 503 || status == 504
		for _, err := range []error{&carrierHTTPError{status, "carrier error"}, &sipSetupHTTPError{status: status}} {
			if temporaryCarrierVerificationError(err) != want {
				t.Fatalf("status %d: %v", status, err)
			}
		}
	}
	if temporaryCarrierVerificationError(context.Canceled) || temporaryCarrierVerificationError(errors.New("unexpected error")) || !temporaryCarrierVerificationError(context.DeadlineExceeded) {
		t.Fatal("incorrect transport failure classification")
	}
}

func outageFixture(t *testing.T) (*App, *sdk.AppCtx, *accountFaultPlatform) {
	t.Helper()
	a, ctx, p := accountIsolationFixture(t, false)
	p.integrationResponsesByConn[10]["list_phone_numbers"] = json.RawMessage(`{"data":[{"id":"telnyx-number","phone_number":"+33189313431","features":["voice"],"status":"active","connection_id":"application-fr"}]}`)
	if _, err := a.connectedNumbers(ctx); err != nil {
		t.Fatal(err)
	}
	p.faults[10] = map[string]string{"list_phone_numbers": "503", "list_outbound_voice_profiles": "503", "get_call_control_application": "503"}
	a.inventoryReads.invalidate()
	return a, ctx, p
}

func TestCarrierInventoryOutagePreviouslyVerifiedPlacement(t *testing.T) {
	a, ctx, p := outageFixture(t)
	p.bindings["carrier"] = int64(10)
	result, err := a.connectedNumbers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found connectedNumberView
	for _, n := range result["numbers"].([]connectedNumberView) {
		if n.CarrierConnectionID == 10 {
			found = n
		}
	}
	if found.CarrierStatus != "active" || !found.OutboundFallbackAllowed || found.Outbound.Status != outboundReady || found.OutboundVerification == nil || !found.OutboundEnabled {
		t.Fatalf("missing honest fallback: %+v", found)
	}
	// Existing consumers can still use the established readiness contract,
	// while operators can distinguish stale verification from a healthy API.
	if result["inventory_status"] != "partial" || found.OutboundVerification.Status != previouslyVerified {
		t.Fatalf("unusable or misleading outage response: %+v", result)
	}
	statuses := result["provider_statuses"].([]map[string]any)
	if len(statuses) != 1 || statuses[0]["inventory_status"] != "unavailable" || len(result["warnings"].([]numberInventoryWarning)) != 1 {
		t.Fatal("live carrier API failure was hidden")
	}
	before := p.requests[10]
	session, err := a.placeHumanCall(ctx, "project-a", "+33189999901", "+33189313431", 30, nil)
	if err != nil {
		t.Fatal("503 blocked verified caller ID:", err)
	}
	row, err := a.db().findCall(session.CallID)
	if err != nil || row == nil || row.CarrierConnectionID != 10 || row.FromNumber != "+33189313431" {
		t.Fatalf("wrong placement: %+v %v", row, err)
	}
	// One ownership check, one readiness check and actual mocked carrier dial.
	if p.requests[10]-before != 3 {
		t.Fatalf("duplicated carrier lookups: %d", p.requests[10]-before)
	}
}

func TestCarrierInventoryOutageTwilioProgrammablePlacement(t *testing.T) {
	a, ctx, p := accountIsolationFixture(t, true)
	if _, err := a.connectedNumbers(ctx); err != nil {
		t.Fatal(err)
	}
	p.faults[10] = map[string]string{"list_phone_numbers": "503"}
	p.faults[11] = map[string]string{"list_phone_numbers": "503"}
	a.inventoryReads.invalidate()
	result, err := a.connectedNumbers(ctx)
	if err != nil || result["inventory_status"] != "partial" {
		t.Fatalf("verified Twilio inventory unavailable: %+v %v", result, err)
	}
	for _, n := range result["numbers"].([]connectedNumberView) {
		if !n.OutboundFallbackAllowed || n.Outbound.Status != outboundReady || n.OutboundVerification == nil {
			t.Fatalf("Twilio lost generic readiness: %+v", n)
		}
	}
	assertHealthyAccountDial(t, a, ctx, "+33189313431", 10)
}

func TestCarrierInventoryOutageFailClosed(t *testing.T) {
	for _, scenario := range []string{"never_verified", "expired", "credentials_changed", "wrong_project", "wrong_binding", "unconfigured", "disabled_number", "disabled_binding", "disabled_provider", "binding_draining", "excluded_by_fresh_inventory", "inactive", "forbidden_then_503", "disabled_profile_then_503", "fresh_disabled_profile_with_application_503"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, p := outageFixture(t)
			project := "project-a"
			switch scenario {
			case "never_verified":
				_, _ = a.db().db.Exec(`DELETE FROM carrier_verification_evidence`)
			case "expired":
				_, _ = a.db().db.Exec(`UPDATE carrier_verification_evidence SET verified_at_ms=?`, time.Now().Add(-25*time.Hour).UnixMilli())
			case "credentials_changed":
				p.credentialsByID[10].Fields["token"] = "replacement-credential"
			case "wrong_project":
				project = "project-b"
			case "wrong_binding":
				p.credentialsByID[10] = p.credentialsByID[11]
			case "unconfigured":
				delete(p.credentialsByID[10].Fields, "phone_number")
			case "disabled_number", "disabled_binding", "disabled_provider":
				scope, value := "number", "+33189313431"
				if scenario == "disabled_binding" {
					scope, value = "connection", "10"
				}
				if scenario == "disabled_provider" {
					scope, value = "provider", "telnyx"
				}
				if _, err := a.outboundPolicy(ctx, map[string]any{"scope": scope, "value": value, "enabled": false}); err != nil {
					t.Fatal(err)
				}
			case "binding_draining":
				if _, err := a.db().db.Exec(`INSERT INTO carrier_binding_drains(connection_id,change_id) VALUES(10,'removed')`); err != nil {
					t.Fatal(err)
				}
			case "excluded_by_fresh_inventory", "inactive":
				p.faults[10] = nil
				raw := `{"data":[]}`
				if scenario == "inactive" {
					raw = `{"data":[{"phone_number":"+33189313431","status":"inactive","features":["voice"],"connection_id":"application-fr"}]}`
				}
				p.integrationResponsesByConn[10]["list_phone_numbers"] = json.RawMessage(raw)
				if _, _, _, err := a.resolveCarrierBinding(ctx, project, "+33189313431"); err == nil {
					t.Fatal("fresh negative inventory authorized caller ID")
				}
				p.faults[10] = map[string]string{"list_phone_numbers": "503"}
			case "forbidden_then_503":
				p.faults[10]["list_phone_numbers"] = "reject"
				if _, err := a.connectedNumbers(ctx); err != nil {
					t.Fatal(err)
				}
				p.faults[10]["list_phone_numbers"] = "503"
			case "disabled_profile_then_503", "fresh_disabled_profile_with_application_503":
				delete(p.faults[10], "list_outbound_voice_profiles")
				if scenario == "disabled_profile_then_503" {
					delete(p.faults[10], "get_call_control_application")
				}
				p.integrationResponse["list_outbound_voice_profiles"] = json.RawMessage(`{"data":[{"id":"profile-default","enabled":false}]}`)
				if _, _, _, err := a.resolveCarrierBinding(ctx, project, "+33189313431"); err == nil {
					t.Fatal("disabled profile allowed fallback")
				}
				p.faults[10]["list_outbound_voice_profiles"] = "503"
			}
			if _, err := a.placeHumanCall(ctx, project, "+33189999901", "+33189313431", 30, nil); err == nil {
				t.Fatal("unsafe outage placement accepted")
			}
			for _, call := range p.integrationCalls {
				if call.Tool == "dial_call" {
					t.Fatal("rejected request reached carrier dial")
				}
			}
			result, err := a.connectedNumbers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if project == "project-a" {
				for _, n := range result["numbers"].([]connectedNumberView) {
					if n.CarrierConnectionID == 10 && n.OutboundFallbackAllowed {
						t.Fatalf("unsafe fallback offered: %+v", n)
					}
				}
			}
		})
	}
}

func TestCarrierVerificationTombstoneAndNoRenewal(t *testing.T) {
	a, ctx, p := outageFixture(t)
	creds := p.credentialsByID[10]
	var numbers []ownedNumber
	before, err := a.carrierVerification("project-a", 10, creds, "numbers", "", &numbers)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = a.resolveCarrierBinding(ctx, "project-a", "+33189313431"); err != nil {
		t.Fatal(err)
	}
	after, err := a.carrierVerification("project-a", 10, creds, "numbers", "", &numbers)
	if err != nil || !before.Equal(after) {
		t.Fatal("failed read renewed evidence")
	}
	if err = a.forgetCarrierVerification("project-a", 10, creds, "numbers", ""); err != nil {
		t.Fatal(err)
	}
	if err = a.saveCarrierVerification("project-a", 10, creds, "numbers", "", numbers, before); err != nil {
		t.Fatal(err)
	}
	cached, _, err := a.cachedOwnedNumbers("project-a", 10, creds)
	if err != nil || len(cached) != 0 {
		t.Fatal("older in-flight read resurrected revoked evidence")
	}
	raw, _ := json.Marshal(numbers)
	if strings.Contains(string(raw), "token") {
		t.Fatal("credentials persisted")
	}
}

func TestCarrierInventoryOutageHTTPPlacementKeepsUserPermissions(t *testing.T) {
	a, _, p := outageFixture(t)
	identity := phoneTestIdentity("alice")
	policy := phonePolicy{Users: []phoneUser{{Identity: identity, Enabled: true, phoneGrant: phoneGrant{Role: "user", OutboundNumbers: []string{"+33189313431"}}}}, Groups: []phoneGroup{}}
	w := phoneTestRequest(a, nil, "PUT", "/access/policy", policy)
	if w.Code != 200 {
		t.Fatalf("policy: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"to": "+33189999901", "from": "+33189313431", "idempotency_key": "outage-test"}
	if w := phoneTestRequest(a, &identity, "POST", "/softphone/place", body); w.Code != 200 {
		t.Fatalf("verified user cannot place through outage: %d %s", w.Code, w.Body)
	}
	before := len(p.integrationCalls)
	policy.Users[0].OutboundNumbers = nil
	if w := phoneTestRequest(a, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	body["idempotency_key"] = "after-revocation"
	if w := phoneTestRequest(a, &identity, "POST", "/softphone/place", body); w.Code != 403 {
		t.Fatalf("revoked number dialed: %d %s", w.Code, w.Body)
	}
	identity.OrganizationID = "other-organization"
	if w := phoneTestRequest(a, &identity, "POST", "/softphone/place", body); w.Code != 403 {
		t.Fatalf("cross-organization access: %d %s", w.Code, w.Body)
	}
	if len(p.integrationCalls) != before {
		t.Fatal("denied caller reached carrier")
	}
}

func TestCarrierInventoryOutageReachesSIPInvite(t *testing.T) {
	for _, provider := range []string{"telnyx", "twilio"} {
		t.Run(provider, func(t *testing.T) {
			failed := false
			stubSIPVerification(t, provider, &failed)
			a, ctx, base, cfg := outboundSIPFixture(t, provider)
			raw := `{"data":[{"phone_number":"+33123456780","features":["voice"],"status":"active"}]}`
			if provider == "twilio" {
				raw = `{"incoming_phone_numbers":[{"phone_number":"+33123456780","capabilities":{"voice":true},"status":"in-use"}]}`
			}
			base.integrationResponse = map[string]json.RawMessage{"list_phone_numbers": json.RawMessage(raw)}
			if _, err := a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "transport": inboundTransportSIPDirect, "config": cfg, "username": "sip-user", "password": "sip-secret"}); err != nil {
				t.Fatal(err)
			}
			bound := ctx.IntegrationFor("carrier")
			if _, err := a.resolveOutboundFrom(ctx, "project-a", bound, base.credentials, "+33123456780"); err != nil {
				t.Fatal(err)
			}

			// Replace the platform with the same credentials/binding and an actual
			// unsuccessful SDK result carrying Telnyx's 503/10007 response.
			p := &accountFaultPlatform{answerPlatform: base, faults: map[int64]map[string]string{10: {"list_phone_numbers": "503"}}, requests: map[int64]int{}}
			outageCtx := sdk.NewAppCtxForTest(ctx.Manifest(), ctx.AppDB(), ctx.Config(), p, nil)
			oldGlobal := globalCtx
			globalCtx = outageCtx
			t.Cleanup(func() { globalCtx = oldGlobal })
			failed = true
			ua, err := sipgo.NewUA()
			if err != nil {
				t.Fatal(err)
			}
			defer ua.Close()
			client, err := sipgo.NewClient(ua)
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			client.TxRequester = blockedSIPInvite{entered}
			life, stop := context.WithCancel(t.Context())
			defer stop()
			gatewayCfg := directSIPTestConfig()
			gatewayCfg.RTPPortMin = 0
			gatewayCfg.RTPPortMax = 0
			gatewayCfg.RTPBindIP = netip.MustParseAddr("127.0.0.1")
			g := &sipGateway{app: a, appCtx: outageCtx, cfg: gatewayCfg, client: client, ctx: life, cancel: stop, outboundByCall: map[string]*outboundSIPSession{}, outboundByProviderCall: map[string]*outboundSIPSession{}}
			g.outboundDialogs = sipgo.NewDialogClientCache(client, sip.ContactHeader{Address: sip.Uri{Host: "127.0.0.1", Port: 5061}})
			a.sip.gateway = g
			inventory, err := a.connectedNumbers(outageCtx)
			if err != nil {
				t.Fatal(err)
			}
			sipState := inventory["direct_sip"].(map[string]any)
			if sipState["enabled"] != true || sipState["ready"] != true || inventory["inventory_status"] != "partial" {
				t.Fatalf("outage lost live SIP state: %+v", inventory)
			}
			numbers := inventory["numbers"].([]connectedNumberView)
			if len(numbers) != 1 || numbers[0].Outbound.Status != outboundReady || !numbers[0].OutboundFallbackAllowed {
				t.Fatalf("verified SIP number unavailable: %+v", numbers)
			}
			done := make(chan error, 1)
			go func() {
				_, err := a.placeHumanCall(outageCtx, "project-a", "+33123456781", "+33123456780", 30, nil)
				done <- err
			}()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("REST outage blocked SIP before INVITE: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("SIP INVITE never attempted")
			}
			stop() // The mock intentionally never answers. No carrier is contacted.
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "SIP INVITE") {
					t.Fatalf("unexpected mocked SIP result: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("mock SIP did not cancel")
			}
			if _, selected, err := a.resolveOutboundSIPCredentials(outageCtx, "project-a", bound, base.credentials, "+33123456780"); err != nil || !selected {
				t.Fatal("prior SIP readiness not retained", err)
			}
			// A changed resource/config must not inherit an old trunk attestation.
			cfg.ResourceID += "-changed"
			if err := a.verifySIPWithEvidence(t.Context(), outageCtx, "project-a", 10, base.credentials, cfg); err == nil {
				t.Fatal("changed SIP resource inherited proof")
			}
		})
	}
}

func TestCarrierVerificationRepeatedNegativeCannotResurrect(t *testing.T) {
	a, _, p := outageFixture(t)
	creds := p.credentialsByID[10]
	start := time.Now().Add(-time.Second)
	numbers := []ownedNumber{{PhoneNumber: "+33189313431", CarrierStatus: "active", Capabilities: []string{"voice"}}}
	// All three observations share a millisecond; order still matters when an
	// older successful request completes after repeated authoritative negatives.
	for _, key := range []string{"negative-empty", "negative-error"} {
		var negative any = []ownedNumber{}
		if key == "negative-error" {
			negative = nil
		}
		for _, v := range []struct {
			at   time.Time
			data any
		}{
			{start, negative}, {start.Add(2 * time.Nanosecond), negative}, {start.Add(time.Nanosecond), numbers},
		} {
			if err := a.saveCarrierVerification("project-a", 10, creds, "numbers", key, v.data, v.at); err != nil {
				t.Fatal(err)
			}
		}
		var restored []ownedNumber
		if _, err := a.carrierVerification("project-a", 10, creds, "numbers", key, &restored); err != nil || len(restored) != 0 {
			t.Fatal("older successful request restored revoked evidence", err)
		}
	}
}

func TestCarrierInventoryOutageUsesVerifiedNumberApplication(t *testing.T) {
	a, ctx, p := accountIsolationFixture(t, false)
	delete(p.credentialsByID[10].Fields, "connection_id")
	if _, err := a.connectedNumbers(ctx); err != nil {
		t.Fatal(err)
	}
	p.faults[10] = map[string]string{"list_phone_numbers": "503", "list_outbound_voice_profiles": "503", "get_call_control_application": "503"}
	before := p.requests[10]
	if _, err := a.placeHumanCall(ctx, "project-a", "+33189999901", "+33189313431", 30, nil); err != nil {
		t.Fatal("number's verified application was not reused", err)
	}
	if p.requests[10]-before != 3 {
		t.Fatalf("application resolution repeated number inventory: %d reads", p.requests[10]-before)
	}
}

func TestCarrierVerificationExcludesWebhookCredentials(t *testing.T) {
	a, _, p := outageFixture(t)
	creds := p.credentialsByID[10]
	proof := []ownedNumber{{PhoneNumber: "+33189313431", CarrierStatus: "active", ConnectionID: "application-fr", Capabilities: []string{"voice"}, VoiceURL: "https://example.test?api_key=must-not-persist", StatusCallback: "https://example.test?token=must-not-persist"}}
	if err := a.saveCarrierVerification("project-a", 10, creds, "numbers", "", proof, time.Now()); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := a.db().db.QueryRow(`SELECT value_json FROM carrier_verification_evidence WHERE connection_id=10 AND kind='numbers'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "must-not-persist") {
		t.Fatal("credential-bearing URLs persisted in verification evidence")
	}
	if proof[0].VoiceURL == "" || proof[0].StatusCallback == "" {
		t.Fatal("verification mutated caller-owned inventory")
	}
	cached, _, err := a.cachedOwnedNumbers("project-a", 10, creds)
	if err != nil || len(cached) != 1 || cached[0].ConnectionID != "application-fr" {
		t.Fatal("safe identity metadata lost", err)
	}
}

func TestCarrierInventoryOutageExplicitProgrammableNumber(t *testing.T) {
	a, ctx, p := accountIsolationFixture(t, false)
	delete(p.credentialsByID[10].Fields, "phone_number")
	if _, err := a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "phone_number": "+33189313431", "transport": inboundTransportProgrammable}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.connectedNumbers(ctx); err != nil {
		t.Fatal(err)
	}
	p.faults[10] = map[string]string{"list_phone_numbers": "503", "list_outbound_voice_profiles": "503"}
	a.inventoryReads.invalidate()
	result, err := a.connectedNumbers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	for _, n := range result["numbers"].([]connectedNumberView) {
		if n.CarrierConnectionID == 10 {
			ready = n.OutboundFallbackAllowed && n.Outbound.Status == outboundReady
		}
	}
	if !ready {
		t.Fatal("explicit configured number without inbound route lost verified readiness")
	}
	if _, err = a.placeHumanCall(ctx, "project-a", "+33189999901", "+33189313431", 30, nil); err != nil {
		t.Fatal("explicit configured number blocked by inventory outage", err)
	}
}

func TestCarrierInventoryOutagePreservesEmptyCapabilityArray(t *testing.T) {
	a, ctx, p := outageFixture(t)
	numbers, _, err := a.cachedOwnedNumbers("project-a", 10, p.credentialsByID[10])
	if err != nil {
		t.Fatal(err)
	}
	numbers[0].Capabilities = nil
	if err = a.saveCarrierVerification("project-a", 10, p.credentialsByID[10], "numbers", "", numbers, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := a.connectedNumbers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Numbers []struct {
			Connection   int64           `json:"carrier_connection_id"`
			Capabilities json.RawMessage `json:"capabilities"`
		}
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, n := range payload.Numbers {
		if n.Connection == 10 && string(n.Capabilities) != "[]" {
			t.Fatalf("cached inventory broke capability collection: %s", n.Capabilities)
		}
	}
}
