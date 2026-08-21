package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestParseOwnedCarrierNumbers(t *testing.T) {
	tests := []struct {
		provider string
		raw      string
		id       string
		phone    string
		feature  string
	}{
		{
			provider: "twilio",
			raw:      `{"incoming_phone_numbers":[{"sid":"PN1","phone_number":"+13502231050","friendly_name":"Main","capabilities":{"voice":true,"sms":true}}]}`,
			id:       "PN1", phone: "+13502231050", feature: "voice",
		},
		{
			provider: "signalwire",
			raw:      `{"incoming_phone_numbers":[{"sid":"PN2","phone_number":"+14155550101","capabilities":{"voice":true}}]}`,
			id:       "PN2", phone: "+14155550101", feature: "voice",
		},
		{
			provider: "telnyx",
			raw:      `{"data":[{"id":"number-1","phone_number":"+3725550100","features":[{"name":"voice"},{"name":"sms"}],"connection_id":"app-1"}]}`,
			id:       "number-1", phone: "+3725550100", feature: "sms",
		},
		{
			provider: "plivo",
			raw:      `{"objects":[{"number":"34648257793","voice_enabled":true,"application":"/v1/Account/test/Application/app-1/"}]}`,
			id:       "34648257793", phone: "+34648257793", feature: "voice",
		},
		{
			provider: "vonage",
			raw:      `{"numbers":[{"msisdn":"33123456789","features":["VOICE"]}]}`,
			id:       "33123456789", phone: "+33123456789", feature: "voice",
		},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			numbers, err := parseOwnedCarrierNumbers(test.provider, json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(numbers) != 1 {
				t.Fatalf("numbers=%#v", numbers)
			}
			got := numbers[0]
			if got.ProviderNumberID != test.id || got.PhoneNumber != test.phone || !containsString(got.Capabilities, test.feature) {
				t.Fatalf("unexpected normalized number: %#v", got)
			}
		})
	}
}

func TestConnectedNumbersEndpointJoinsProjectRouteAndHidesSecrets(t *testing.T) {
	platform := &answerPlatform{
		bindings: map[string]any{"carrier": float64(10)},
		connection: &sdk.PlatformConnection{
			ID: 10, AppSlug: "twilio", Status: "connected", ProjectID: "project-a",
		},
		credentials: &sdk.ConnectionCredentials{
			ConnectionID: 10, Slug: "twilio", Fields: map[string]string{"auth_token": "test-auth-token"},
		},
		agents: map[int64]*sdk.PlatformAgent{
			7: {ID: 7, Name: "Standardiste Test", Status: "running", ProjectID: "project-a"},
		},
		integrationResponse: map[string]json.RawMessage{},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	app := &App{installID: 42}

	route := routeRow{
		ID: "route-staging", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
		PhoneNumber: "+13502231050", PhoneNumberSID: "PN1", AgentID: 7, Enabled: true,
		AnswerMode: answerModeRealtimeImmediate, AutoVoice: "Kore", Secret: "route-secret",
		CreatedAt: "2026-07-24T10:00:00Z", UpdatedAt: "2026-07-24T10:00:00Z",
		RecordingMode: recordingModeInherit,
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	otherProject := route
	otherProject.ID = "route-other-project"
	otherProject.ProjectID = "project-b"
	otherProject.PhoneNumber = "+14155550199"
	otherProject.PhoneNumberSID = "PN-other"
	if err := app.db().insertRoute(otherProject); err != nil {
		t.Fatal(err)
	}
	platform.integrationResponse["list_phone_numbers"] = json.RawMessage(`{
		"incoming_phone_numbers": [{
			"sid": "PN1",
			"phone_number": "+13502231050",
			"friendly_name": "US reception",
			"capabilities": {"voice": true, "sms": true},
			"voice_url": ` + quoteJSON(app.inboundRouteURL(route)) + `,
			"voice_method": "POST",
			"status_callback": ` + quoteJSON(app.twilioRouteStatusURL(route)) + `,
			"status_callback_method": "POST"
		}]
	}`)

	request := httptest.NewRequest(http.MethodPost, "/numbers/connected?project_id=project-a", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	app.handleNumbers(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Provider string                `json:"provider"`
		Numbers  []connectedNumberView `json:"numbers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Provider != "twilio" || len(response.Numbers) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	number := response.Numbers[0]
	if number.PhoneNumber != "+13502231050" || number.Route == nil {
		t.Fatalf("number was not joined to its route: %#v", number)
	}
	if number.Route.AgentName != "Standardiste Test" || number.Route.AnswerMode != answerModeRealtimeImmediate || number.Route.Voice != "Kore" {
		t.Fatalf("route details missing: %#v", number.Route)
	}
	if number.VoiceWebhookStatus != webhookConfigured || number.StatusCallbackState != webhookConfigured || number.RoutingHealth != "healthy" {
		t.Fatalf("unexpected webhook health: %#v", number)
	}
	body := recorder.Body.String()
	for _, secret := range []string{"route-secret", "/inbound/twilio/", "project-b", "+14155550199"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
}

func TestConnectedNumbersKeepsRouteWhenCarrierNumberIsMissing(t *testing.T) {
	route := routeRow{
		ID: "route-missing", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
		PhoneNumber: "+13502231050", PhoneNumberSID: "PN-missing", AgentID: 7, Enabled: true,
		AnswerMode: answerModeAgent, Secret: "secret",
	}
	app := &App{installID: 42}
	view := app.connectedNumberView(nil, "twilio", ownedNumber{
		PhoneNumber: route.PhoneNumber, ProviderNumberID: route.PhoneNumberSID, CarrierStatus: "not_found",
	}, &route, func(int64) string { return "Reception" })
	if view.Route == nil || view.CarrierStatus != "not_found" || view.RoutingHealth != "degraded" {
		t.Fatalf("missing carrier number route was hidden: %#v", view)
	}
	if view.Capabilities == nil {
		t.Fatal("connected number capabilities must encode as an empty array, not null")
	}
}

func TestOutboundFromUsesProjectRouteWithoutCredentialPhoneNumber(t *testing.T) {
	platform := &answerPlatform{
		bindings: map[string]any{"carrier": int64(10)},
		credentials: &sdk.ConnectionCredentials{
			Slug: "telnyx", Fields: map[string]string{"api_key": "test-key"},
		},
		integrationResponse: map[string]json.RawMessage{
			"list_phone_numbers": json.RawMessage(`{"data":[{"id":"number-1","phone_number":"+33123456789","features":["voice"]}]}`),
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	route := routeRow{
		ID: "route-fr", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10,
		PhoneNumber: "+33123456789", PhoneNumberSID: "number-1", AgentID: 7, Enabled: true,
		Secret: "route-secret", CreatedAt: "2026-08-21T10:00:00Z", UpdatedAt: "2026-08-21T10:00:00Z",
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	bound := ctx.IntegrationFor("carrier")
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	for name, requested := range map[string]string{"automatic": "", "selected": route.PhoneNumber} {
		t.Run(name, func(t *testing.T) {
			got, err := app.resolveOutboundFrom(ctx, "project-a", bound, creds, requested)
			if err != nil || got != route.PhoneNumber {
				t.Fatalf("from=%q err=%v, want %q", got, err, route.PhoneNumber)
			}
		})
	}
	if _, err := app.resolveOutboundFrom(ctx, "project-a", bound, creds, "+33999999999"); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("unowned caller ID accepted: %v", err)
	}
}

func TestOutboundFromRequiresSelectionWhenProjectHasMultipleRoutes(t *testing.T) {
	platform := &answerPlatform{
		bindings:    map[string]any{"carrier": int64(10)},
		credentials: &sdk.ConnectionCredentials{Slug: "telnyx", Fields: map[string]string{}},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	for index, phone := range []string{"+33123456789", "+33123456780"} {
		route := routeRow{
			ID: fmt.Sprintf("route-%d", index), ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10,
			PhoneNumber: phone, AgentID: 7, Enabled: true, Secret: fmt.Sprintf("secret-%d", index),
			CreatedAt: "2026-08-21T10:00:00Z", UpdatedAt: "2026-08-21T10:00:00Z",
		}
		if err := app.db().insertRoute(route); err != nil {
			t.Fatal(err)
		}
	}
	bound := ctx.IntegrationFor("carrier")
	creds, _ := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if _, err := app.resolveOutboundFrom(ctx, "project-a", bound, creds, ""); err == nil || !strings.Contains(err.Error(), "choose a from number") {
		t.Fatalf("ambiguous caller ID was selected: %v", err)
	}
}

func TestTelnyxOutboundUsesSelectedNumbersRouteApplication(t *testing.T) {
	platform := &answerPlatform{
		bindings: map[string]any{"carrier": int64(10)},
		credentials: &sdk.ConnectionCredentials{
			Slug: "telnyx", Fields: map[string]string{"api_key": "test-key"},
		},
		integrationResponse: map[string]json.RawMessage{
			"list_phone_numbers": json.RawMessage(`{"data":[{"id":"number-fr","phone_number":"+33189313431","connection_id":"application-live"}]}`),
			"dial_call":          json.RawMessage(`{"data":{"call_control_id":"call-control-1"}}`),
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	config, _ := json.Marshal(telnyxRouteConfig{ApplicationID: "application-route"})
	route := routeRow{
		ID: "route-fr-outbound", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10,
		PhoneNumber: "+33189313431", PhoneNumberSID: "number-fr", AgentID: 7, Enabled: true,
		Secret: "route-secret", PreviousVoiceURL: string(config),
		CreatedAt: "2026-08-21T10:00:00Z", UpdatedAt: "2026-08-21T10:00:00Z",
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	session, err := app.placeHumanCall(ctx, "project-a", "+33612345678", route.PhoneNumber, nil)
	if err != nil {
		t.Fatalf("place Telnyx browser call: %v", err)
	}
	stored, err := app.db().findCall(session.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Status != "initiated" || stored.CarrierSID != "call-control-1" ||
		stored.ThreadID != "human-"+session.CallID || session.MediaURL == "" {
		t.Fatalf("Telnyx browser placement was not persisted completely: session=%+v call=%+v", session, stored)
	}
	var dialCall *integrationCall
	for index := range platform.integrationCalls {
		if platform.integrationCalls[index].Tool == "dial_call" {
			dialCall = &platform.integrationCalls[index]
			break
		}
	}
	if dialCall == nil || dialCall.Input["connection_id"] != "application-route" || dialCall.Input["from"] != route.PhoneNumber {
		t.Fatalf("Telnyx placement did not use selected number application: %#v", platform.integrationCalls)
	}
	if _, mutated := platform.credentials.Fields["connection_id"]; mutated {
		t.Fatal("number-specific connection_id was written into shared credentials")
	}
	if msg := app.hangupCall(ctx, session.CallID, 0, "project-a"); msg != "" {
		t.Fatalf("hang up Telnyx browser call: %s", msg)
	}
	last := platform.integrationCalls[len(platform.integrationCalls)-1]
	if last.Tool != "hangup_call" || last.Input["call_control_id"] != "call-control-1" {
		t.Fatalf("Telnyx hangup contract mismatch: %#v", last)
	}
	stored, err = app.db().findCall(session.CallID)
	if err != nil || stored.Status != "completed" {
		t.Fatalf("Telnyx browser hangup was not persisted: call=%+v err=%v", stored, err)
	}
}

func TestTelnyxOutboundFallsBackToOwnedNumberConnection(t *testing.T) {
	platform := &answerPlatform{
		bindings:    map[string]any{"carrier": int64(10)},
		credentials: &sdk.ConnectionCredentials{Slug: "telnyx", Fields: map[string]string{}},
		integrationResponse: map[string]json.RawMessage{
			"list_phone_numbers": json.RawMessage(`{"data":[{"id":"number-fr","phone_number":"+33189313431","connection_id":"application-live"}]}`),
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	bound := ctx.IntegrationFor("carrier")
	creds, _ := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	resolved, err := app.resolveOutboundCarrierCredentials(ctx, "project-a", bound, creds, "+33189313431")
	if err != nil || resolved.Fields["connection_id"] != "application-live" {
		t.Fatalf("resolved credentials=%#v err=%v", resolved, err)
	}
}

func TestListOwnedCarrierNumbersPaginatesTelnyx(t *testing.T) {
	firstPage := make([]map[string]any, 2)
	for i := range firstPage {
		firstPage[i] = map[string]any{
			"id":           fmt.Sprintf("id-%d", i),
			"phone_number": fmt.Sprintf("+1202555%04d", i),
		}
	}
	firstRaw, err := json.Marshal(map[string]any{
		"data": firstPage,
		"meta": map[string]any{"total_pages": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	platform := &answerPlatform{
		integrationResponse: map[string]json.RawMessage{},
		integrationResponses: map[string][]json.RawMessage{
			"list_phone_numbers": {
				firstRaw,
				json.RawMessage(`{"data":[{"id":"id-2","phone_number":"+12025550002"}],"meta":{"total_pages":2}}`),
			},
		},
	}
	_, ctx := withTelephonyTestContext(t, platform)
	numbers, err := listOwnedCarrierNumbers(ctx, &numberProvider{Slug: "telnyx", ConnID: 9})
	if err != nil {
		t.Fatal(err)
	}
	if len(numbers) != 3 || len(platform.integrationCalls) != 2 {
		t.Fatalf("numbers=%d calls=%#v", len(numbers), platform.integrationCalls)
	}
	if got := platform.integrationCalls[1].Input["page[number]"]; got != 2 {
		t.Fatalf("second Telnyx page=%#v", got)
	}
}

func TestListOwnedCarrierNumbersPaginatesPlivo(t *testing.T) {
	firstPage := make([]map[string]any, 20)
	for i := range firstPage {
		firstPage[i] = map[string]any{"number": fmt.Sprintf("1202555%04d", i)}
	}
	firstRaw, err := json.Marshal(map[string]any{
		"objects": firstPage,
		"meta":    map[string]any{"limit": 20, "offset": 0, "total_count": 21},
	})
	if err != nil {
		t.Fatal(err)
	}
	platform := &answerPlatform{
		integrationResponse: map[string]json.RawMessage{},
		integrationResponses: map[string][]json.RawMessage{
			"list_owned_phone_numbers": {
				firstRaw,
				json.RawMessage(`{"objects":[{"number":"12025550200"}],"meta":{"limit":20,"offset":20,"total_count":21}}`),
			},
		},
	}
	_, ctx := withTelephonyTestContext(t, platform)
	numbers, err := listOwnedCarrierNumbers(ctx, &numberProvider{Slug: "plivo", ConnID: 9})
	if err != nil {
		t.Fatal(err)
	}
	if len(numbers) != 21 || len(platform.integrationCalls) != 2 {
		t.Fatalf("numbers=%d calls=%#v", len(numbers), platform.integrationCalls)
	}
	if got := platform.integrationCalls[1].Input["offset"]; got != 20 {
		t.Fatalf("second Plivo offset=%#v", got)
	}
}

func TestConnectedNumbersPanelContract(t *testing.T) {
	source := readTestFile(t, "ui/CallsPanel.tsx")
	for _, required := range []string{
		`endpoint("/numbers/connected")`,
		"Connected numbers",
		"Find a number",
		"Assigned agent",
		"Routing health",
		"(number.capabilities ?? []).length",
		"setOffers([])",
		"void loadConnected()",
		"Call from",
		`withProject("/numbers/connected")`,
		`{ to, from: fromNumber }`,
		"chooseFromNumber",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("panel missing %q", required)
		}
	}
	manifest := (&App{}).Manifest()
	foundPermission := false
	for _, permission := range manifest.Requires.Permissions {
		if permission == sdk.PermInstancesRead {
			foundPermission = true
		}
	}
	if !foundPermission {
		t.Fatal("platform.instances.read is required to display assigned agent names")
	}
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
