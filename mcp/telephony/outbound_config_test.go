package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnsureOutboundReadyAutoAttachesSoleEnabledProfile(t *testing.T) {
	platform := &answerPlatform{
		integrationResponse: map[string]json.RawMessage{
			"list_outbound_voice_profiles": json.RawMessage(`{"data":[{"id":"profile-default","name":"Default","enabled":true,"whitelisted_destinations":["FR","US"]}]}`),
		},
		integrationResponses: map[string][]json.RawMessage{
			"get_call_control_application": {
				json.RawMessage(`{"data":{"id":"application-1","active":true,"outbound":null}}`),
				json.RawMessage(`{"data":{"id":"application-1","active":true,"outbound":{"outbound_voice_profile_id":"profile-default"}}}`),
			},
		},
	}
	a, ctx := withTelephonyTestContext(t, platform)
	if err := a.ensureOutboundReady(ctx, "telnyx", 10, "application-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"list_outbound_voice_profiles", "get_call_control_application", "list_outbound_voice_profiles", "update_call_control_application", "get_call_control_application"}
	if len(platform.integrationCalls) != len(want) {
		t.Fatalf("calls=%#v", platform.integrationCalls)
	}
	for i, tool := range want {
		if platform.integrationCalls[i].Tool != tool {
			t.Fatalf("call %d=%s want %s", i, platform.integrationCalls[i].Tool, tool)
		}
	}
	outbound, _ := platform.integrationCalls[3].Input["outbound"].(map[string]any)
	if outbound["outbound_voice_profile_id"] != "profile-default" {
		t.Fatalf("profile update=%#v", platform.integrationCalls[3].Input)
	}
}

func TestEnsureOutboundReadyRequiresSelectionWhenProfilesAreAmbiguous(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"list_outbound_voice_profiles": json.RawMessage(`{"data":[{"id":"profile-a","name":"France","enabled":true},{"id":"profile-b","name":"Global","enabled":true}]}`),
		"get_call_control_application": json.RawMessage(`{"data":{"id":"application-1","active":true,"outbound":null}}`),
	}}
	a, ctx := withTelephonyTestContext(t, platform)
	err := a.ensureOutboundReady(ctx, "telnyx", 10, "application-1")
	if err == nil || !strings.Contains(err.Error(), "Choose which outbound calling profile") {
		t.Fatalf("error=%v", err)
	}
	for _, call := range platform.integrationCalls {
		if call.Tool == "update_call_control_application" {
			t.Fatalf("ambiguous profile was applied: %#v", platform.integrationCalls)
		}
	}
}

func TestCarrierWithoutProfileRequirementIsReady(t *testing.T) {
	platform := &answerPlatform{}
	a, ctx := withTelephonyTestContext(t, platform)
	view, err := a.outboundReadiness(ctx, "twilio", 10, "")
	if err != nil || view.Required || view.Status != outboundReady {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	if len(platform.integrationCalls) != 0 {
		t.Fatalf("generic carrier readiness called provider tools: %#v", platform.integrationCalls)
	}
}
