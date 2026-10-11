package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTelnyxDirectSIPProvisioningCodecNames(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		project, route, want []string
	}{
		{name: "defaults", want: []string{"G722", "G711U", "G711A"}},
		{name: "project preference", project: []string{"PCMA", "G722", "PCMU"}, want: []string{"G711A", "G722", "G711U"}},
		{name: "route override", project: []string{"G722"}, route: []string{"PCMU", "PCMA"}, want: []string{"G711U", "G711A"}},
		{name: "wideband only", route: []string{"G722"}, want: []string{"G722"}},
		{name: "ulaw only", route: []string{"PCMU"}, want: []string{"G711U"}},
		{name: "alaw only", route: []string{"PCMA"}, want: []string{"G711A"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
				"list_phone_numbers":     json.RawMessage(`{"data":[{"id":"number-1","phone_number":"+12025550100","connection_id":"old-connection"}]}`),
				"create_fqdn_connection": json.RawMessage(`{"data":{"id":"connection-1"}}`),
				"create_fqdn":            json.RawMessage(`{"data":{"id":"fqdn-1"}}`),
			}}
			app, ctx := withTelephonyTestContext(t, platform)
			route := routeRow{ID: "codec-route", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10,
				PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, InboundTransport: inboundTransportSIPDirect}
			if err := app.db().insertRoute(route); err != nil {
				t.Fatal(err)
			}
			for _, scope := range []struct {
				id     string
				codecs []string
			}{{"", tc.project}, {route.ID, tc.route}} {
				if scope.codecs != nil {
					if err := app.saveLocalMediaSettings(ctx, scope.id, localMediaSettings{Codecs: scope.codecs}); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := app.localMediaSettings(route.ProjectID, route.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.configureTelnyxDirectSIP(ctx, &route, directSIPTestConfig()); err != nil {
				t.Fatal(err)
			}
			// Inspect the actual serialized request rather than only the translator.
			raw, err := json.Marshal(platform.integrationCalls[1].Input)
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Inbound struct {
					Codecs []string `json:"codecs"`
				} `json:"inbound"`
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(request.Inbound.Codecs, tc.want) {
				t.Fatalf("provider codecs = %v, want %v", request.Inbound.Codecs, tc.want)
			}
			after, err := app.localMediaSettings(route.ProjectID, route.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("shared SIP settings changed: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestTelnyxProvisioningCodecsRejectInvalidSettings(t *testing.T) {
	for _, codecs := range [][]string{{"G711U"}, {"opus"}, {"PCMU", "PCMU"}} {
		if _, err := telnyxProvisioningCodecs(codecs); err == nil {
			t.Fatalf("accepted invalid shared codec settings %v", codecs)
		}
	}
	codecs := []string{"PCMA", "PCMU", "G722"}
	want := append([]string(nil), codecs...)
	if _, err := telnyxProvisioningCodecs(codecs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(codecs, want) {
		t.Fatal("translation mutated input")
	}
}
