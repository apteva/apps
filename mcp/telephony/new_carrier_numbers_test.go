package main

import (
	"encoding/json"
	"testing"
)

func TestSinchOwnedNumberPaginationAndVoiceCapability(t *testing.T) {
	platform := &answerPlatform{integrationResponses: map[string][]json.RawMessage{
		"list_active_numbers": {
			json.RawMessage(`{"activeNumbers":[{"phoneNumber":"+33123456789","displayName":"Paris","capability":["VOICE","SMS"]}],"nextPageToken":"page-2"}`),
			json.RawMessage(`{"activeNumbers":[{"phoneNumber":"+33123456788","capability":["VOICE"]}]}`),
		},
	}}
	_, ctx := withTelephonyTestContext(t, platform)
	owned, err := listOwnedCarrierNumbers(ctx, &numberProvider{Slug: "sinch", ConnID: 18})
	if err != nil || len(owned) != 2 || owned[0].PhoneNumber != "+33123456789" || !containsString(owned[0].Capabilities, "voice") {
		t.Fatalf("Sinch owned numbers: %+v err=%v", owned, err)
	}
	if got := platform.integrationCalls[1].Input["pageToken"]; got != "page-2" {
		t.Fatalf("second page token = %v", got)
	}
}

func TestDIDWWOwnedNumbersDoNotIncludeTerminatedDIDs(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"list_dids": json.RawMessage(`{"data":[{"id":"did-1","type":"dids","attributes":{"number":"33123456789","description":"Paris","blocked":false},"relationships":{"voice_in_trunk":{"data":{"type":"voice_in_trunks","id":"trunk-1"}}}},{"id":"did-2","type":"dids","attributes":{"number":"33123456788","terminated":true}}]}`),
	}}
	_, ctx := withTelephonyTestContext(t, platform)
	owned, err := listOwnedCarrierNumbers(ctx, &numberProvider{Slug: "didww", ConnID: 19})
	if err != nil || len(owned) != 1 || owned[0].ProviderNumberID != "did-1" || owned[0].ConnectionID != "trunk-1" {
		t.Fatalf("DIDWW owned numbers: %+v err=%v", owned, err)
	}
}
