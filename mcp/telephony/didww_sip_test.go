package main

import (
	"encoding/json"
	"testing"
)

func TestDIDWWOnlyOffersImplementedInboundTransport(t *testing.T) {
	if !supportsInboundTransport("didww", inboundTransportSIPDirect) {
		t.Fatal("DIDWW direct SIP routes are implemented but cannot be created")
	}
	if supportsInboundTransport("didww", inboundTransportProgrammable) {
		t.Fatal("DIDWW programmable routing was advertised without an adapter")
	}
	for _, slug := range []string{"bandwidth", "sinch"} {
		if supportsInboundTransport(slug, inboundTransportSIPDirect) || supportsInboundTransport(slug, inboundTransportProgrammable) {
			t.Fatalf("unfinished %s inbound route was advertised", slug)
		}
	}
}

func TestDIDWWDirectSIPConfigurationRestoresOriginalTrunk(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"list_dids":            json.RawMessage(`{"data":[{"id":"did-1","attributes":{"number":"33123456789"},"relationships":{"voice_in_trunk":{"data":{"id":"old-trunk"}},"voice_in_trunk_group":{"data":null}}}]}`),
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
	if len(platform.integrationCalls) != 3 || platform.integrationCalls[0].Tool != "list_dids" ||
		platform.integrationCalls[1].Tool != "create_inbound_trunk" || platform.integrationCalls[2].Tool != "set_did_voice_trunk" {
		t.Fatalf("DIDWW route setup: %#v", platform.integrationCalls)
	}
	configuration := platform.integrationCalls[1].Input["configuration"].(map[string]any)
	attributes := configuration["attributes"].(map[string]any)
	if attributes["transport_protocol_id"] != 3 || attributes["media_encryption_mode"] != "srtp_sdes" || attributes["host"] != "sip.example.test" {
		t.Fatalf("DIDWW SIP configuration: %#v", configuration)
	}
	stored, _ = app.db().findRoute(route.ID)
	var state directSIPProviderConfig
	if err := json.Unmarshal([]byte(stored.TransportConfig), &state); err != nil || state.TrunkID != "new-trunk" || state.PreviousTrunkID != "old-trunk" {
		t.Fatalf("saved DIDWW state: %+v err=%v", state, err)
	}
	platform.integrationCalls = nil
	if err := app.deconfigureDirectSIPCarrierRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[0].Tool != "set_did_voice_trunk" ||
		platform.integrationCalls[1].Tool != "delete_inbound_trunk" {
		t.Fatalf("DIDWW route restore: %#v", platform.integrationCalls)
	}
	assigned := platform.integrationCalls[0].Input["body"].(map[string]any)["data"].(map[string]any)["relationships"].(map[string]any)["voice_in_trunk"].(map[string]any)["data"].(map[string]any)
	if assigned["id"] != "old-trunk" {
		t.Fatalf("restored DIDWW trunk: %#v", assigned)
	}
}

func TestDIDWWTrunkDocumentCanClearExistingAssignment(t *testing.T) {
	document := didwwDIDTrunkDocument("did-1", "", "")
	relationships := document["data"].(map[string]any)["relationships"].(map[string]any)
	if relationships["voice_in_trunk"].(map[string]any)["data"] != nil ||
		relationships["voice_in_trunk_group"].(map[string]any)["data"] != nil {
		t.Fatalf("DIDWW clear document: %#v", document)
	}
}
