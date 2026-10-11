package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sipVerificationRoundTripper func(*http.Request) (*http.Response, error)

func (f sipVerificationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func stubSIPVerification(t *testing.T, provider string, fail *bool) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = sipVerificationRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Errorf("verification mutated provider: %s", r.Method)
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("verification has no shared deadline")
		}
		status := 200
		body := `{"data":{"id":"sip-1","active":true,"outbound":{"outbound_voice_profile_id":"profile-1"}}}`
		if strings.Contains(r.URL.Path, "outbound_voice_profiles") {
			body = `{"data":{"enabled":true}}`
		}
		if provider == "twilio" {
			if r.URL.Host != "trunking.twilio.com" {
				t.Errorf("unexpected API host %s", r.URL.Host)
			}
			u, p, ok := r.BasicAuth()
			if !ok || u != "ACtest" || p != "api-secret" {
				t.Error("wrong account credentials")
			}
			body = `{"sid":"TKtest","domain_name":"example.pstn.twilio.com","secure":true,"recording":{"mode":"do-not-record"}}`
		} else {
			if r.URL.Host != "api.telnyx.com" || r.Header.Get("Authorization") != "Bearer api-secret" {
				t.Error("wrong Telnyx API boundary")
			}
		}
		if fail != nil && *fail {
			status = 503
			body = `{"secret":"do-not-log-me"}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
}
func outboundSIPFixture(t *testing.T, provider string) (*App, *sdk.AppCtx, *answerPlatform, outboundSIPConfig) {
	t.Helper()
	t.Setenv("APTEVA_DATA_DIR", t.TempDir())
	p := &answerPlatform{bindings: map[string]any{"carrier": int64(10)}, credentials: &sdk.ConnectionCredentials{Slug: provider, Fields: map[string]string{"phone_number": "+33123456780", "api_key": "api-secret", "account_sid": "ACtest", "auth_token": "api-secret"}}}
	a, ctx := withTelephonyTestContext(t, p)
	cfg := outboundSIPConfig{Host: "sip.telnyx.com", ResourceID: "sip-1", AuthMode: "digest", MediaEncryption: "srtp_sdes"}
	if provider == "twilio" {
		cfg.Host = "example.pstn.twilio.com"
		cfg.ResourceID = "TKtest"
	}
	return a, ctx, p, cfg
}
func TestOutboundSIPSettingsScopeSecretsAndAdmission(t *testing.T) {
	for _, provider := range []string{"telnyx", "twilio"} {
		t.Run(provider, func(t *testing.T) {
			fail := false
			stubSIPVerification(t, provider, &fail)
			a, ctx, p, cfg := outboundSIPFixture(t, provider)
			args := map[string]any{"carrier_connection_id": 10, "transport": inboundTransportSIPDirect, "config": cfg, "username": "sip-user", "password": "sip-secret"}
			result, err := a.configureOutboundTransport(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(result)
			if bytes.Contains(encoded, []byte("sip-secret")) || bytes.Contains(encoded, []byte("sip-user")) {
				t.Fatal("readback exposed SIP credentials")
			}
			view, cipher, scope, err := a.outboundTransport("project-a", 10, "+33123456780")
			if err != nil || view.Transport != inboundTransportSIPDirect || scope != "" || bytes.Contains(cipher, []byte("sip-secret")) {
				t.Fatalf("account scope/encryption: %+v %v", view, err)
			}
			opened, err := openOutboundSIPCredentials(ctx, "project-a", 10, "", cipher)
			if err != nil || opened.Password != "sip-secret" {
				t.Fatal("credential roundtrip failed")
			}
			for _, wrong := range []struct {
				project    string
				connection int64
				number     string
			}{{"project-b", 10, ""}, {"project-a", 11, ""}, {"project-a", 10, "+33123456780"}} {
				if _, e := openOutboundSIPCredentials(ctx, wrong.project, wrong.connection, wrong.number, cipher); e == nil {
					t.Fatal("secret crossed authorization scope")
				}
			}
			info, err := os.Stat(filepath.Join(ctx.DataDir(), "outbound-sip.key"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("SIP key permissions")
			}
			for _, project := range []string{"project-b"} {
				v, _, _, e := a.outboundTransport(project, 10, "+33123456780")
				if e != nil || v.Transport != inboundTransportProgrammable {
					t.Fatal("configuration crossed project boundary")
				}
			}
			bound := ctx.IntegrationFor("carrier")
			if bound == nil {
				t.Fatal("fixture has no carrier")
			}
			resolved, selected, err := a.resolveOutboundSIPCredentials(ctx, "project-a", bound, p.credentials, "+33123456780")
			if err != nil || !selected {
				t.Fatal(err)
			}
			adapter, err := a.carrierFor(bound, provider, resolved.Fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := adapter.(*sipOutboundCarrier); !ok {
				t.Fatal("SIP selected REST adapter")
			}
			if p.credentials.Fields["sip_password"] != "" {
				t.Fatal("resolution mutated shared carrier credentials")
			}
			// A number can deliberately retain Voice API while its account uses SIP.
			_, err = a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "phone_number": "+33123456780", "transport": inboundTransportProgrammable})
			if err != nil {
				t.Fatal(err)
			}
			v, _, _, err := a.outboundTransport("project-a", 10, "+33123456780")
			if err != nil || v.Transport != inboundTransportProgrammable || v.Scope != "number" {
				t.Fatal("number override lost")
			}
			_, err = a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "phone_number": "+33123456780", "inherit": true})
			if err != nil {
				t.Fatal(err)
			}
			fail = true
			if _, _, err = a.resolveOutboundSIPCredentials(ctx, "project-a", bound, p.credentials, "+33123456780"); err == nil || strings.Contains(err.Error(), "do-not-log-me") {
				t.Fatal("provider failure must block only new SIP admission with sanitized evidence")
			}
			// Established calls retain their transport regardless of subsequent settings.
			row := &callRow{ID: "existing", Direction: "outbound", CarrierSlug: provider, CarrierConnectionID: 10, IngressPath: inboundTransportSIPDirect}
			_, err = a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "transport": inboundTransportProgrammable})
			if err != nil {
				t.Fatal(err)
			}
			adapter, err = a.carrierForRow(ctx, nil, row)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := adapter.(*sipOutboundCarrier); !ok {
				t.Fatal("live SIP call changed adapter")
			}
			if !a.callUsesDirectSIP(row) {
				t.Fatal("live call lost durable SIP transport")
			}
		})
	}
}
func TestOutboundSIPConfigurationFailClosed(t *testing.T) {
	for _, p := range []string{"telnyx", "twilio"} {
		t.Run(p, func(t *testing.T) {
			a, ctx, _, cfg := outboundSIPFixture(t, p)
			stubSIPVerification(t, p, nil)
			for _, host := range []string{"127.0.0.1", "attacker.example", "example.pstn.twilio.com.attacker.test", "sip.telnyx.com@attacker.test"} {
				invalid := cfg
				invalid.Host = host
				if validateOutboundSIPConfig(p, &invalid) == nil {
					t.Fatalf("accepted untrusted host %s", host)
				}
			}
			if _, err := a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 99, "transport": inboundTransportSIPDirect, "config": cfg}); err == nil {
				t.Fatal("unbound account accepted")
			}
			if _, err := a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "phone_number": "+33123456799", "transport": inboundTransportProgrammable}); err == nil {
				t.Fatal("unowned caller ID accepted")
			}
			if _, err := a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "transport": inboundTransportSIPDirect, "config": cfg}); err == nil {
				t.Fatal("missing SIP credentials accepted")
			}
		})
	}
}
func TestGenericOutboundSIPAdapterCapabilitiesAndDefaults(t *testing.T) {
	a := &App{}
	for _, provider := range []string{"telnyx", "twilio"} {
		legacy, err := a.carrierForSlug(provider, 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := legacy.(*sipOutboundCarrier); ok {
			t.Fatal("default changed to SIP")
		}
		fields := map[string]string{"outbound_transport": inboundTransportSIPDirect, "sip_host": "sip.telnyx.com", "sip_resource_id": "sip-1", "sip_auth_mode": "digest", "sip_username": "sip-user", "sip_password": "sip-secret"}
		if provider == "twilio" {
			fields["sip_host"] = "example.pstn.frankfurt.twilio.com"
			fields["sip_resource_id"] = "TKtest"
		}
		adapter, err := a.carrierForSlug(provider, 10, fields)
		if err != nil {
			t.Fatal(err)
		}
		placed, err := adapter.Place(nil, carrierPlaceRequest{CallID: "local", From: "+33123456780", To: "+33123456781", RecordingMode: recordingModeAlways})
		if err != nil || placed.CarrierSID != "local" {
			t.Fatalf("native preparation: %v", err)
		}
		if _, err = adapter.Place(nil, carrierPlaceRequest{CallID: "local", From: "+33123456780", To: "+33123456781", MachineDetection: machineDetectionDetect}); err == nil {
			t.Fatal("native SIP silently ignored AMD")
		}
		capabilities := callControlCapabilities(callRow{Direction: "outbound", CarrierSlug: provider, IngressPath: inboundTransportSIPDirect, CarrierSID: "sip-id", PeerKind: peerKindHuman, RecordingMode: recordingModeAlways, HoldMusicURL: "https://example.test/hold.wav"})
		if !capabilities["recording_pause"] || !capabilities["hold_music"] {
			t.Fatal("SIP controls missing")
		}
	}
}
func TestOutboundSIPNegotiatedTimerRoles(t *testing.T) {
	for _, tc := range []struct {
		header string
		local  bool
	}{{"1800;refresher=uac", true}, {"1800;refresher=uas", false}} {
		res := &sip.Response{}
		res.AppendHeader(sip.NewHeader("Session-Expires", tc.header))
		timer, err := outboundSIPTimer(res, sipSessionTimer{})
		if err != nil || timer.interval != 1800*time.Second || timer.local != tc.local {
			t.Fatalf("outbound timer role %s: %+v %v", tc.header, timer, err)
		}
	}
}

func TestOutboundSIPMissingKeyDoesNotReplaceSavedKey(t *testing.T) {
	a, ctx, _, _ := outboundSIPFixture(t, "telnyx")
	_ = a
	data, err := sealOutboundSIPCredentials(ctx, "project-a", 10, "", outboundSIPCredentials{"user", "secret"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ctx.DataDir(), "outbound-sip.key")
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = openOutboundSIPCredentials(ctx, "project-a", 10, "", data); err == nil {
		t.Fatal("lost key silently accepted")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read silently replaced encryption key")
	}
}

type blockedSIPInvite struct{ entered chan struct{} }

func (b blockedSIPInvite) Request(ctx context.Context, _ *sip.Request) (sip.ClientTransaction, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("missing dialing deadline")
	}
	close(b.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestOutboundSIPCancellationBeforeDialogRegistration(t *testing.T) {
	a, ctx, _, _ := outboundSIPFixture(t, "telnyx")
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
	lifetime, stop := context.WithCancel(t.Context())
	defer stop()
	cfg := directSIPTestConfig()
	cfg.RTPPortMin = 0
	cfg.RTPPortMax = 0
	cfg.RTPBindIP = netip.MustParseAddr("127.0.0.1")
	g := &sipGateway{app: a, appCtx: ctx, cfg: cfg, client: client, ctx: lifetime, cancel: stop, outboundByCall: map[string]*outboundSIPSession{}, outboundByProviderCall: map[string]*outboundSIPSession{}}
	g.outboundDialogs = sipgo.NewDialogClientCache(client, sip.ContactHeader{Address: sip.Uri{Host: "127.0.0.1", Port: 5061}})
	a.sip.gateway = g
	row := callRow{ID: "pending-sip", Direction: "outbound", CarrierSlug: "telnyx", IngressPath: inboundTransportSIPDirect, Status: "initiated", ProjectID: "project-a", FromNumber: "+33123456780", ToNumber: "+33123456781", ThreadID: "pending-thread", PlacedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- g.startSIPOutbound(&row, didwwSIPSettings{host: "sip.telnyx.com", port: 5061, secure: true, codecs: []string{"PCMU"}}, 30, 300)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("INVITE not started")
	}
	if err = hangupOutboundSIP(a, &row); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("canceled INVITE succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt pending INVITE")
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.outboundPending) != 0 || len(g.outboundReserved) != 0 || len(g.outboundByCall) != 0 {
		t.Fatal("canceled SIP leaked a reservation or dialog")
	}
}

func TestOutboundSIPRecordingsDoNotQueryVoiceAPI(t *testing.T) {
	for _, provider := range []string{"telnyx", "twilio"} {
		t.Run(provider, func(t *testing.T) {
			a, ctx, p, _ := outboundSIPFixture(t, provider)
			row := callRow{ID: "sip-recording", Direction: "outbound", CarrierSlug: provider, CarrierConnectionID: 10, CarrierSID: "sip-call-id", IngressPath: inboundTransportSIPDirect, ProjectID: "project-a", RecordingMode: recordingModeAlways, Status: "failed", ThreadID: "local-recording-thread", PlacedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}
			if err := a.db().insertCall(row); err != nil {
				t.Fatal(err)
			}
			if _, err := a.db().db.Exec(`UPDATE calls SET ended_at=? WHERE id=?`, time.Now().Add(-30*time.Second).UTC().Format(time.RFC3339Nano), row.ID); err != nil {
				t.Fatal(err)
			}
			if err := a.reconcileCallRecordings(ctx, &row); err != nil {
				t.Fatal(err)
			}
			if err := a.reconcileProviderRecording(ctx); err != nil {
				t.Fatal(err)
			}
			if len(p.integrationCalls) != 0 {
				t.Fatal("native SIP recording reconciliation queried Voice API")
			}
		})
	}
}
