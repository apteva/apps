package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func multiCarrierPlatform() *answerPlatform {
	return &answerPlatform{
		bindings: map[string]any{"carrier": map[string]any{"ids": []any{10, 11}, "default_id": int64(11)}},
		connectionsByID: map[int64]*sdk.PlatformConnection{
			10: {AppSlug: "telnyx", Status: "connected"},
			11: {AppSlug: "twilio", Status: "connected"},
		},
		credentialsByID: map[int64]*sdk.ConnectionCredentials{
			10: {Slug: "telnyx", Fields: map[string]string{"phone_number": "+33189313431", "connection_id": "application-fr"}},
			11: {Slug: "twilio", Fields: map[string]string{"phone_number": "+33189313432", "auth_token": "test-auth-token"}},
		},
		integrationResponse: map[string]json.RawMessage{
			"list_outbound_voice_profiles": json.RawMessage(`{"data":[{"id":"profile-default","name":"Default","enabled":true}]}`),
			"get_call_control_application": json.RawMessage(`{"data":{"id":"application-fr","active":true,"outbound":{"outbound_voice_profile_id":"profile-default"}}}`),
			"make_call":                    json.RawMessage(`{"sid":"CA-multicarrier"}`),
		},
		integrationResponsesByConn: map[int64]map[string]json.RawMessage{
			10: {"list_phone_numbers": json.RawMessage(`{"data":[{"id":"telnyx-number","phone_number":"+33189313431","features":["voice"],"connection_id":"application-fr"}]}`)},
			11: {"list_phone_numbers": json.RawMessage(`{"incoming_phone_numbers":[{"sid":"PN-twilio","phone_number":"+33189313432","capabilities":{"voice":true},"status":"active"}]}`)},
		},
	}
}

func TestExplicitFromSelectsAuthorizedNonDefaultCarrier(t *testing.T) {
	platform := multiCarrierPlatform()
	app, ctx := withTelephonyTestContext(t, platform)
	bound, creds, from, err := app.resolveCarrierBinding(ctx, "project-a", "+33189313431")
	if err != nil {
		t.Fatal(err)
	}
	if bound.ConnectionID != 10 || creds.Slug != "telnyx" || from != "+33189313431" {
		t.Fatalf("selected wrong carrier: bound=%+v creds=%+v from=%q", bound, creds, from)
	}
	if creds.Fields["connection_id"] != "application-fr" {
		t.Fatalf("selected Telnyx application was not retained: %#v", creds.Fields)
	}
}

func TestOutboundCallPersistsSelectedCarrierAndConnection(t *testing.T) {
	platform := multiCarrierPlatform()
	app, ctx := withTelephonyTestContext(t, platform)
	session, err := app.placeHumanCall(ctx, "project-a", "+33189999901", "+33189313432", 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.db().findCall(session.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.CarrierSlug != "twilio" || row.CarrierConnectionID != 11 || row.FromNumber != "+33189313432" {
		t.Fatalf("call did not persist the selected carrier: %+v", row)
	}
}

func TestNoFromKeepsConfiguredDefaultCarrier(t *testing.T) {
	platform := multiCarrierPlatform()
	app, ctx := withTelephonyTestContext(t, platform)
	bound, creds, from, err := app.resolveCarrierBinding(ctx, "project-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if bound.ConnectionID != 11 || creds.Slug != "twilio" || from != "+33189313432" {
		t.Fatalf("default carrier was not preserved: bound=%+v creds=%+v from=%q", bound, creds, from)
	}
}

func TestExplicitFromRejectsNumberOutsideAuthorizedBindings(t *testing.T) {
	platform := multiCarrierPlatform()
	app, ctx := withTelephonyTestContext(t, platform)
	_, _, _, err := app.resolveCarrierBinding(ctx, "project-a", "+33189999999")
	if err == nil || !strings.Contains(err.Error(), "not owned by any authorized carrier") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConnectedNumbersIncludesEveryAuthorizedCarrier(t *testing.T) {
	platform := multiCarrierPlatform()
	app, ctx := withTelephonyTestContext(t, platform)
	result, err := app.connectedNumbers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	numbers, ok := result["numbers"].([]connectedNumberView)
	if !ok || len(numbers) != 2 {
		t.Fatalf("unexpected multi-carrier number inventory: %#v", result)
	}
	providers := map[string]bool{}
	for _, number := range numbers {
		providers[number.Provider] = true
	}
	if !providers["telnyx"] || !providers["twilio"] || result["provider"] != "multiple" {
		t.Fatalf("inventory did not preserve provider identity: %#v", result)
	}
}
