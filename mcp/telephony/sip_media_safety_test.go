package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/pion/rtp"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLocalSIPVoicemailCompletedAudioRequired(t *testing.T) {
	for _, audio := range []bool{true, false} {
		t.Run(map[bool]string{true: "audio", false: "empty"}[audio], func(t *testing.T) {
			h := localMediaHarness(t, routingDefinition{Entry: "message", Nodes: []routingNode{{ID: "message", Type: "voicemail", Config: map[string]any{"prompt": "Leave a message"}}}})
			h.readAudio(t)
			s := h.gateway.sessionByCall(h.call.ID)
			waitSIPParity(t, func() bool { return s.recorder.Load() != nil })
			if audio {
				for i := 0; i < 3; i++ {
					packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 1}, Payload: make([]byte, 160)}
					for j := range packet.Payload {
						packet.Payload[j] = 0x80
					}
					raw, _ := packet.Marshal()
					if _, err := h.media.WriteToUDP(raw, h.remote); err != nil {
						t.Fatal(err)
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			select {
			case <-h.bye:
			case <-time.After(3 * time.Second):
				t.Fatal("voicemail not bounded")
			}
			waitSIPParity(t, func() bool {
				row, _ := h.app.db().findCall(h.call.ID)
				return row != nil && isTerminalStatus(row.Status)
			})
			row, _ := h.app.db().findCall(h.call.ID)
			if (row.HandlingReason == "voicemail") != audio {
				t.Fatalf("handling=%s audio=%v", row.HandlingReason, audio)
			}
		})
	}
}
func TestLocalSIPPromptPartsNoDuplicatedAnnouncement(t *testing.T) {
	for _, kind := range []string{"dtmf_menu", "voicemail"} {
		a, _, _ := localSpeechFixture(t)
		node := routingNode{ID: "next", Type: kind, Config: map[string]any{"prompt": "Follow up"}}
		if kind == "dtmf_menu" {
			node.Branches = map[string]string{"1": "end", "timeout": "end"}
		}
		nodes := []routingNode{{ID: "notice", Type: "announcement", Config: map[string]any{"text": "Welcome"}, Next: "next"}, node}
		if kind == "dtmf_menu" {
			nodes = append(nodes, routingNode{ID: "end", Type: "hangup"})
		}
		def := routingDefinition{Entry: "notice", Nodes: nodes}
		plan, err := a.resolveRoutingDefinition(&routeRow{ProjectID: "project-a"}, "", nil, &routingFlowVersionRow{}, def)
		if err != nil {
			t.Fatal(err)
		}
		parts := localRoutingSpeechParts(plan)
		if len(parts) != 2 || parts[0] != "Welcome" || parts[1] != "Follow up" {
			t.Fatalf("%s parts=%v prompt=%s", kind, parts, plan.Prompt)
		}
	}
}
func TestSIPTransportSaveFailureRestoresCarrierAssignment(t *testing.T) {
	p := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "telnyx", Fields: map[string]string{"public_key": base64.StdEncoding.EncodeToString(make([]byte, 32))}}, integrationResponse: map[string]json.RawMessage{"list_phone_numbers": json.RawMessage(`{"data":[{"id":"phone","phone_number":"+12025550100","connection_id":"program"}]}`)}}
	a, ctx := withTelephonyTestContext(t, p)
	state := directSIPProviderConfig{Provider: "telnyx", PreviousConnectionID: "program", FQDNConnectionID: "sip", FQDNID: "fqdn"}
	raw, _ := json.Marshal(state)
	route := routeRow{ID: "rollback-route", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10, PhoneNumber: "+12025550100", PhoneNumberSID: "phone", Enabled: true, AgentID: 7, InboundTransport: inboundTransportSIPDirect, TransportConfig: string(raw), PreviousVoiceURL: `{"application_id":"program"}`}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db().db.Exec(`CREATE TRIGGER fail_transport_save BEFORE UPDATE OF inbound_transport ON inbound_routes WHEN NEW.inbound_transport='programmable_websocket' BEGIN SELECT RAISE(ABORT,'test save failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.applyRouteTransport(ctx, route.ID, inboundTransportProgrammable, 0); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("rollback=%v", err)
	}
	fresh, _ := a.db().findRoute(route.ID)
	if fresh.InboundTransport != inboundTransportSIPDirect || fresh.TransportConfig != string(raw) {
		t.Fatal("committed route changed")
	}
	commands := p.integrationCalls
	last := commands[len(commands)-1]
	if last.Tool != "update_phone_number" || last.Input["connection_id"] != "sip" {
		t.Fatalf("not restored: %+v", last)
	}
}
func TestSIPRetirementResourceIdentityAndCancellation(t *testing.T) {
	a := directSIPProviderConfig{Provider: "telnyx", FQDNConnectionID: "connection", FQDNID: "endpoint"}
	if !sipResourcesOverlap(a, directSIPProviderConfig{Provider: "telnyx", FQDNConnectionID: "connection", FQDNID: "other"}) {
		t.Fatal("overlap missed")
	}
	if sipResourcesOverlap(a, directSIPProviderConfig{Provider: "twilio", FQDNConnectionID: "connection"}) {
		t.Fatal("different providers overlap")
	}
	p := &cleanup404Platform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	request, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cleanupSIPResources(ctx, 10, a, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if p.count != 0 {
		t.Fatal("canceled cleanup issued commands")
	}
}

type sipRoundTrip func(*http.Request) (*http.Response, error)

func (f sipRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSIPTwilioDetachOnlyAssociation(t *testing.T) {
	p := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "twilio", Fields: map[string]string{"account_sid": "ACtest", "auth_token": "secret"}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	for _, status := range []int{204, 404, 500} {
		http.DefaultTransport = sipRoundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Method != "DELETE" || r.URL.Host != "trunking.twilio.com" || r.URL.Path != "/v1/Trunks/TKtest/PhoneNumbers/PNtest" {
				t.Fatalf("wrong operation %s %s", r.Method, r.URL)
			}
			user, pass, ok := r.BasicAuth()
			if !ok || user != "ACtest" || pass != "secret" {
				t.Fatal("credentials missing")
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})
		err := detachTwilioSIPNumber(ctx, 10, "TKtest", "PNtest")
		if (err == nil) != (status != 500) {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
}
func TestSIPAIActivationFailureRetiresReadyAttempt(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := callRow{ID: "sip-ai-failure", ProjectID: "project-a", Direction: "inbound", IngressPath: inboundTransportSIPDirect, CarrierSlug: "twilio", CarrierConnectionID: 10, AgentID: 7, Status: "pending", PeerKind: peerKindRealtime, ThreadID: "pending-ai", AudioBridgeURL: "pending"}
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	// Core preparation succeeds, but no SIP dialog exists to activate.
	if _, err := a.answerCall(ctx, &row, "Help the caller.", "", "", false); err == nil {
		t.Fatal("missing dialog accepted")
	}
	h, err := a.aiHandoff(row.ID)
	if err != nil || h == nil || h.Status != "failed" || h.Code != "sip_activation_failed" {
		t.Fatalf("handoff=%+v err=%v", h, err)
	}
	if err = a.driveAIHandoff(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	h, _ = a.aiHandoff(row.ID)
	if h.FallbackApplied != 1 {
		t.Fatal("no defined fallback")
	}
}
func TestSIPRouteAccountScopeAndOutboundApplication(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	if _, _, err := a.createInboundRoute(ctx, 7, map[string]any{"phone_number": "+12025550100", "carrier_connection_id": 999}); err == nil {
		t.Fatal("foreign account accepted")
	}
	state, _ := json.Marshal(directSIPProviderConfig{Provider: "telnyx", PreviousConnectionID: "program"})
	r := &routeRow{InboundTransport: inboundTransportSIPDirect, TransportConfig: string(state), PreviousVoiceURL: `{"application_id":"old"}`}
	if got := telnyxRouteApplicationID(r, "fallback"); got != "program" {
		t.Fatalf("outbound app=%s", got)
	}
}

func TestLocalSIPCallerCancellationDuringSpeech(t *testing.T) {
	var p *localSpeechPlatform
	localMediaHarnessWithHooks(t, routingDefinition{Entry: "notice", Nodes: []routingNode{{ID: "notice", Type: "announcement", Config: map[string]any{"text": "Wait"}, Next: "end"}, {ID: "end", Type: "hangup"}}}, func(platform *localSpeechPlatform) {
		p = platform
		p.wait = true
		p.started = make(chan struct{}, 1)
	}, func(h *sipMediaHarness) {
		select {
		case <-p.started:
		case <-time.After(time.Second):
			t.Fatal("speech not started")
		}
		call, err := h.app.db().findInboundCallByProviderID("media-test@127.0.0.1")
		if err != nil || call == nil {
			t.Fatal(err)
		}
		s := h.gateway.sessionByCall(call.ID)
		// CANCEL must match the original INVITE transaction's Via branch and CSeq.
		header := strings.Split(h.invite, "Content-Type:")[0]
		cancel := strings.Replace(strings.Replace(header, "INVITE ", "CANCEL ", 1), "CSeq: 1 INVITE", "CSeq: 1 CANCEL", 1) + "Content-Length: 0\r\n\r\n"
		if _, err = h.signal.Write([]byte(cancel)); err != nil {
			t.Fatal(err)
		}
		readSIPResponseContaining(t, h.signal, "487")
		waitSIPParity(t, func() bool { return s.ended.Load() })
		waitSIPParity(t, func() bool {
			s.local.mu.Lock()
			defer s.local.mu.Unlock()
			select {
			case <-s.local.done:
				return true
			default:
				return false
			}
		})
		waitSIPParity(t, func() bool { row, _ := h.app.db().findCall(call.ID); return row != nil && isTerminalStatus(row.Status) })
		row, _ := h.app.db().findCall(call.ID)
		if row.CarrierAnsweredAt != "" || !isTerminalStatus(row.Status) {
			t.Fatalf("canceled call revived: %+v", row)
		}
	})
}
func TestLocalSIPRecordingFailureNeverPlayable(t *testing.T) {
	a, _, _ := localSpeechFixture(t)
	call := callRow{ID: "broken-recorder", ProjectID: "project-a", Status: "pending"}
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	r, err := a.newLocalRecording(&call, true)
	if err != nil {
		t.Fatal(err)
	}
	r.file.Close()
	r.Record(0, make([]int16, 480))
	r.Close()
	row, err := a.db().findRecording(r.row.ID)
	if err != nil || row.ProviderStatus != "failed" || row.StorageStatus != "failed" {
		t.Fatalf("failed recording=%+v err=%v", row, err)
	}
}

func TestSIPExternalOfferRetainsProgrammableAccountAfterSwitch(t *testing.T) {
	a, db, ctx, p := externalRingFixture(t, "simultaneous")
	route := routeRow{ID: "sip-external-route", ProjectID: "p1", CarrierSlug: "telnyx", CarrierConnectionID: 10, PhoneNumber: "+12025550100", Enabled: true, InboundTransport: inboundTransportProgrammable, PreviousVoiceURL: `{"application_id":"new-program"}`}
	if err := db.insertRoute(route); err != nil {
		t.Fatal(err)
	}
	pinned := route
	pinned.InboundTransport = inboundTransportSIPDirect
	pinned.PreviousVoiceURL = `{"application_id":"original-program"}`
	raw, _ := json.Marshal(routingExecutionContext{Route: pinned})
	if _, err := db.db.Exec(`UPDATE call_route_executions SET context_json=? WHERE call_id='external'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE calls SET route_id=?,carrier_slug='telnyx',carrier_connection_id=10,ingress_path='sip_direct' WHERE id='external'`, route.ID); err != nil {
		t.Fatal(err)
	}
	parent, _ := db.findCall("external")
	offers, _ := db.activeRingOffers(parent.ID, "p1")
	if err := a.startRingLeg(ctx, parent, offers[0]); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.integrationCalls {
		if c.Tool == "dial_call" {
			if c.Input["connection_id"] != "original-program" {
				t.Fatalf("wrong pinned application %+v", c.Input)
			}
			return
		}
	}
	t.Fatal("no external call placed")
}
