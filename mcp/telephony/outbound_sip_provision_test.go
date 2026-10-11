package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Stateful provider contract fixture: resources survive a deliberately lost
// response, just as they would after a carrier accepts a POST.
type sipSetupProvider struct {
	mu                                                          sync.Mutex
	provider                                                    string
	profiles, connections, trunks, lists, credentials, mappings []map[string]any
	writes                                                      map[string]int
	failPath                                                    string
	losePath                                                    string
	blockPath                                                   string
	entered                                                     chan struct{}
	bad                                                         bool
}

func newSIPSetupProvider(t *testing.T, provider string) *sipSetupProvider {
	t.Helper()
	s := &sipSetupProvider{provider: provider, writes: map[string]int{}, profiles: []map[string]any{{"id": "profile-1", "name": "France", "enabled": true, "whitelisted_destinations": []string{"FR"}}}}
	old := http.DefaultTransport
	http.DefaultTransport = sipVerificationRoundTripper(func(r *http.Request) (*http.Response, error) { return s.roundTrip(t, r) })
	t.Cleanup(func() { http.DefaultTransport = old })
	return s
}
func (s *sipSetupProvider) roundTrip(t *testing.T, r *http.Request) (*http.Response, error) {
	t.Helper()
	if _, ok := r.Context().Deadline(); !ok {
		t.Error("setup lookup has no caller deadline")
	}
	if s.provider == "telnyx" {
		if r.URL.Host != "api.telnyx.com" || r.Header.Get("Authorization") != "Bearer api-secret" {
			t.Error("unauthorized API boundary")
		}
	} else {
		u, p, ok := r.BasicAuth()
		if !ok || u != "ACtest" || p != "api-secret" || (r.URL.Host != "trunking.twilio.com" && r.URL.Host != "api.twilio.com") {
			t.Error("unauthorized Twilio boundary")
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	path = strings.TrimPrefix(path, "/v1")
	path = strings.TrimPrefix(path, "/2010-04-01/Accounts/ACtest")
	if path == s.blockPath {
		if s.entered != nil {
			close(s.entered)
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := 200
	response := map[string]any{}
	if path == s.failPath {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"password":"never-log-this"}`)), Header: make(http.Header)}, nil
	}
	if r.Method == "POST" {
		s.writes[path]++
		switch path {
		case "/outbound_voice_profiles":
			var m map[string]any
			if e := json.NewDecoder(r.Body).Decode(&m); e != nil {
				t.Fatal(e)
			}
			m["id"] = "profile-created"
			s.profiles = append(s.profiles, m)
			response["data"] = m
		case "/credential_connections":
			var m map[string]any
			if e := json.NewDecoder(r.Body).Decode(&m); e != nil {
				t.Fatal(e)
			}
			if m["user_name"] == "" || m["password"] == "" || m["transport_protocol"] != "TLS" {
				t.Error("missing secure connection setup")
			}
			if _, ok := m["encrypted_media"]; ok {
				t.Error("incompatible TLS encrypted_media provisioning field")
			}
			inbound, _ := m["inbound"].(map[string]any)
			codecs, _ := json.Marshal(inbound["codecs"])
			if string(codecs) != `["G722","G711U","G711A"]` {
				t.Errorf("wrong carrier codec names: %s", codecs)
			}
			m["id"] = "sip-managed"
			s.connections = append(s.connections, m)
			response["data"] = m
		case "/Trunks":
			_ = r.ParseForm()
			if r.Form.Get("Secure") != "true" {
				t.Error("insecure trunk")
			}
			m := map[string]any{"sid": "TKmanaged", "friendly_name": r.Form.Get("FriendlyName"), "domain_name": r.Form.Get("DomainName"), "secure": true, "recording": map[string]any{"mode": "do-not-record"}}
			s.trunks = append(s.trunks, m)
			response = m
		case "/SIP/CredentialLists.json":
			_ = r.ParseForm()
			m := map[string]any{"sid": "CLmanaged", "friendly_name": r.Form.Get("FriendlyName")}
			s.lists = append(s.lists, m)
			response = m
		case "/SIP/CredentialLists/CLmanaged/Credentials.json":
			_ = r.ParseForm()
			if len(r.Form.Get("Password")) < 12 {
				t.Error("weak credential")
			}
			m := map[string]any{"sid": "CRmanaged", "username": r.Form.Get("Username")}
			s.credentials = append(s.credentials, m)
			response = m
		case "/Trunks/TKmanaged/CredentialLists":
			_ = r.ParseForm()
			if r.Form.Get("CredentialListSid") != "CLmanaged" {
				t.Error("wrong list attachment")
			}
			m := map[string]any{"sid": "CLmanaged"}
			s.mappings = append(s.mappings, m)
			response = m
		default:
			t.Errorf("unexpected mutation %s; inbound routing must remain unchanged", path)
			status = 400
		}
		if path == s.losePath {
			s.losePath = ""
			return nil, errors.New("lost carrier response with secret=never-log-this")
		}
	} else if r.Method == "GET" {
		list := func(key string, items []map[string]any) {
			if items == nil {
				items = []map[string]any{}
			}
			response[key] = items
		}
		switch path {
		case "/outbound_voice_profiles":
			list("data", s.profiles)
		case "/credential_connections":
			list("data", s.connections)
		case "/credential_connections/sip-managed":
			if len(s.connections) > 0 {
				response["data"] = s.connections[0]
			} else {
				status = 404
			}
		case "/outbound_voice_profiles/profile-1", "/outbound_voice_profiles/profile-created":
			found := false
			for _, m := range s.profiles {
				if "/outbound_voice_profiles/"+stringValue(m["id"]) == path {
					response["data"] = m
					found = true
				}
			}
			if !found {
				status = 404
			}
		case "/Trunks":
			list("trunks", s.trunks)
		case "/Trunks/TKmanaged":
			if len(s.trunks) > 0 {
				response = s.trunks[0]
			} else {
				status = 404
			}
		case "/SIP/CredentialLists.json":
			list("credential_lists", s.lists)
		case "/SIP/CredentialLists/CLmanaged/Credentials.json":
			list("credentials", s.credentials)
		case "/Trunks/TKmanaged/CredentialLists":
			list("credential_lists", s.mappings)
		case "/Trunks/TKmanaged/IpAccessControlLists":
			list("ip_access_control_lists", nil)
		default:
			t.Errorf("unexpected read %s", path)
			status = 404
		}
	} else {
		t.Errorf("unexpected method %s", r.Method)
		status = 400
	}
	if s.bad && path == "/Trunks/TKmanaged" {
		response = map[string]any{"domain_name": "wrong.pstn.twilio.com", "secure": false}
	}
	raw, _ := json.Marshal(response)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
}
func outboundSetupGatewayFixture(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	writeSIPTestCertificate(t, directory, "sip.example.test")
	for k, v := range map[string]string{"TELEPHONY_SIP_PUBLIC_HOST": "sip.example.test", "TELEPHONY_SIP_PUBLIC_IP": "198.51.100.10", "TELEPHONY_SIP_TRANSPORT": "tls", "TELEPHONY_SIP_TLS_CERT_FILE": filepath.Join(directory, "fullchain.pem"), "TELEPHONY_SIP_TLS_KEY_FILE": filepath.Join(directory, "privkey.pem"), "TELEPHONY_SIP_SRTP": "required"} {
		t.Setenv(k, v)
	}
}
func automaticOutboundArgs() map[string]any {
	return map[string]any{"carrier_connection_id": 10, "transport": "sip_direct", "provision": true}
}
func TestAutomaticOutboundSIPTelnyxCredentialFormats(t *testing.T) {
	for _, tc := range []struct {
		name, token, legacy string
	}{
		{name: "integration_token", token: "api-secret"},
		{name: "legacy_api_key", legacy: "api-secret"},
		{name: "integration_token_precedes_legacy", token: "api-secret", legacy: "outdated-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, p, _ := outboundSIPFixture(t, "telnyx")
			delete(p.credentials.Fields, "token")
			if tc.token != "" {
				p.credentials.Fields["token"] = tc.token
			}
			if tc.legacy != "" {
				p.credentials.Fields["api_key"] = tc.legacy
			}
			before, _ := json.Marshal(p.credentials.Fields)
			outboundSetupGatewayFixture(t)
			s := newSIPSetupProvider(t, "telnyx")
			// Complete setup, then reuse and reverify the saved selection.
			for i := 0; i < 2; i++ {
				if _, err := a.configureOutboundTransport(ctx, automaticOutboundArgs()); err != nil {
					t.Fatal(err)
				}
			}
			// New-call admission must verify using the same integration format.
			resolved, selected, err := a.resolveOutboundSIPCredentials(ctx, "project-a", ctx.IntegrationFor("carrier"), p.credentials, "+33123456780")
			if err != nil || !selected || resolved == nil || resolved.Fields["outbound_transport"] != inboundTransportSIPDirect {
				t.Fatalf("outbound SIP admission failed: selected=%v err=%v", selected, err)
			}
			if s.writes["/credential_connections"] != 1 {
				t.Fatal("setup retry created a second SIP connection")
			}
			after, _ := json.Marshal(p.credentials.Fields)
			if string(before) != string(after) {
				t.Fatal("setup modified shared integration credentials")
			}
		})
	}
}

func TestOutboundSIPTelnyxMissingCredentials(t *testing.T) {
	a, ctx, p, _ := outboundSIPFixture(t, "telnyx")
	delete(p.credentials.Fields, "token")
	outboundSetupGatewayFixture(t)
	s := newSIPSetupProvider(t, "telnyx")
	if _, err := a.configureOutboundTransport(ctx, automaticOutboundArgs()); err == nil || err.Error() != "Telnyx API credentials unavailable" {
		t.Fatalf("expected sanitized missing-credential error, got %v", err)
	}
	if len(s.writes) != 0 {
		t.Fatal("missing credentials allowed carrier writes")
	}
	v, _, _, err := a.outboundTransport("project-a", 10, "")
	if err != nil || v.Transport != inboundTransportProgrammable {
		t.Fatal("failed setup changed outbound transport")
	}
}

func TestAutomaticOutboundSIPProvisionAndRetry(t *testing.T) {
	for _, provider := range []string{"telnyx", "twilio"} {
		t.Run(provider, func(t *testing.T) {
			a, ctx, _, _ := outboundSIPFixture(t, provider)
			outboundSetupGatewayFixture(t)
			s := newSIPSetupProvider(t, provider)
			for i := 0; i < 2; i++ {
				result, e := a.configureOutboundTransport(ctx, automaticOutboundArgs())
				if e != nil {
					t.Fatal(e)
				}
				raw, _ := json.Marshal(result)
				if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "username") {
					t.Fatal("setup exposed credentials")
				}
			}
			v, encrypted, scope, e := a.outboundTransport("project-a", 10, "")
			if e != nil || v.Transport != "sip_direct" || scope != "" || !v.CredentialsConfigured || v.VerifiedAt == "" {
				t.Fatalf("not selected: %+v %v", v, e)
			}
			secret, e := openOutboundSIPCredentials(ctx, "project-a", 10, "", encrypted)
			if e != nil || !validSIPDigestCredentials(secret) {
				t.Fatal("generated credentials unavailable")
			}
			if strings.Contains(string(encrypted), secret.Password) {
				t.Fatal("plaintext secret stored")
			}
			for path, count := range s.writes {
				if count != 1 {
					t.Errorf("duplicated %s: %d", path, count)
				}
			}
			if provider == "telnyx" && len(s.writes) != 1 {
				t.Fatal("unexpected Telnyx provisioning writes")
			}
			if provider == "twilio" && len(s.writes) != 4 {
				t.Fatal("incomplete Twilio setup")
			}
			if a.directSIPGateway() != nil {
				t.Fatal("configuration acquired listener ownership")
			}
			for _, key := range []struct {
				project string
				conn    int64
			}{{"other-project", 10}, {"project-a", 11}} {
				other, _, _, e := a.outboundTransport(key.project, key.conn, "")
				if e != nil || other.Transport != "programmable_websocket" {
					t.Fatal("configuration crossed authorization scope")
				}
			}
		})
	}
}
func TestAutomaticOutboundSIPResumesLostCreateResponses(t *testing.T) {
	for _, tc := range []struct{ provider, path string }{{"telnyx", "/credential_connections"}, {"telnyx", "/outbound_voice_profiles"}, {"twilio", "/Trunks"}, {"twilio", "/SIP/CredentialLists.json"}, {"twilio", "/SIP/CredentialLists/CLmanaged/Credentials.json"}, {"twilio", "/Trunks/TKmanaged/CredentialLists"}} {
		t.Run(tc.provider+tc.path, func(t *testing.T) {
			a, ctx, _, _ := outboundSIPFixture(t, tc.provider)
			outboundSetupGatewayFixture(t)
			s := newSIPSetupProvider(t, tc.provider)
			s.losePath = tc.path
			args := automaticOutboundArgs()
			if tc.path == "/outbound_voice_profiles" {
				s.profiles = nil
				args["profile_policy"] = map[string]any{"destinations": []string{"FR"}, "daily_spend_limit": "20.00"}
			}
			if _, e := a.configureOutboundTransport(ctx, args); e == nil || strings.Contains(e.Error(), "never-log-this") {
				t.Fatal("expected sanitized uncertain setup failure")
			}
			v, _, _, _ := a.outboundTransport("project-a", 10, "")
			if v.Transport != "programmable_websocket" {
				t.Fatal("failed setup changed admission")
			}
			if _, e := a.configureOutboundTransport(ctx, args); e != nil {
				t.Fatal(e)
			}
			if s.writes[tc.path] != 1 {
				t.Fatalf("retry duplicated resource at %s", tc.path)
			}
		})
	}
}
func TestAutomaticOutboundSIPProfileSelectionAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profiles []map[string]any
		args     map[string]any
		success  bool
	}{
		{name: "none"},
		{name: "multiple", profiles: []map[string]any{{"id": "a", "enabled": true}, {"id": "b", "enabled": true}}},
		{name: "disabled", profiles: []map[string]any{{"id": "a", "enabled": false}}, args: map[string]any{"outbound_profile_id": "a"}},
		{name: "select", profiles: []map[string]any{{"id": "profile-1", "enabled": true}, {"id": "b", "enabled": true}}, args: map[string]any{"outbound_profile_id": "profile-1"}, success: true},
		{name: "create", args: map[string]any{"profile_policy": map[string]any{"destinations": []string{"fr", "FR", "ES"}, "daily_spend_limit": "12.50"}}, success: true},
		{name: "malformed", args: map[string]any{"profile_policy": map[string]any{"destinations": []string{"France"}, "daily_spend_limit": "-1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, _, _ := outboundSIPFixture(t, "telnyx")
			outboundSetupGatewayFixture(t)
			s := newSIPSetupProvider(t, "telnyx")
			s.profiles = tc.profiles
			args := automaticOutboundArgs()
			for k, v := range tc.args {
				args[k] = v
			}
			_, e := a.configureOutboundTransport(ctx, args)
			if (e == nil) != tc.success {
				t.Fatalf("success=%v error=%v", tc.success, e)
			}
			if !tc.success && len(s.writes) > 0 {
				t.Fatal("unsafe write without profile policy")
			}
			if tc.name == "create" {
				if len(s.profiles) != 1 || s.profiles[0]["daily_spend_limit_enabled"] != true || s.profiles[0]["daily_spend_limit"] != "12.50" {
					t.Fatal("did not preserve caller budget")
				}
				raw, _ := json.Marshal(s.profiles[0]["whitelisted_destinations"])
				if string(raw) != `["FR","ES"]` {
					t.Fatalf("unexpected allowed countries %s", raw)
				}
			}
		})
	}
}
func TestAutomaticOutboundSIPFailurePreservesSelectionAndCalls(t *testing.T) {
	a, ctx, _, _ := outboundSIPFixture(t, "twilio")
	outboundSetupGatewayFixture(t)
	s := newSIPSetupProvider(t, "twilio")
	s.failPath = "/SIP/CredentialLists.json"
	row := callRow{ID: "still-live", Direction: "outbound", CarrierSlug: "twilio", CarrierConnectionID: 10, IngressPath: inboundTransportProgrammable, ProjectID: "project-a", Status: "answered", ThreadID: "test-thread", PlacedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if e := a.db().insertCall(row); e != nil {
		t.Fatal(e)
	}
	if _, e := a.configureOutboundTransport(ctx, automaticOutboundArgs()); e == nil {
		t.Fatal("provider failure accepted")
	}
	v, _, _, _ := a.outboundTransport("project-a", 10, "")
	if v.Transport != inboundTransportProgrammable {
		t.Fatal("changed prior transport")
	}
	current, e := a.db().findCall(row.ID)
	if e != nil || current.Status != "answered" || current.IngressPath != inboundTransportProgrammable {
		t.Fatal("setup altered an active call")
	}
	s.failPath = ""
	if _, e = a.configureOutboundTransport(ctx, automaticOutboundArgs()); e != nil {
		t.Fatal(e)
	}
	if s.writes["/Trunks"] != 1 {
		t.Fatal("partial setup duplicated trunk")
	}
}
func TestAutomaticOutboundSIPConcurrentSetupAndCancellation(t *testing.T) {
	a, ctx, _, _ := outboundSIPFixture(t, "telnyx")
	outboundSetupGatewayFixture(t)
	s := newSIPSetupProvider(t, "telnyx")
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := a.configureOutboundTransport(ctx, automaticOutboundArgs()); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if s.writes["/credential_connections"] != 1 {
		t.Fatal("concurrent setup duplicated connection")
	}
	// Gate acquisition is cancellable; a stuck configuration cannot hold media.
	unlock, e := lockOutboundSetup(t.Context(), "test-gate")
	if e != nil {
		t.Fatal(e)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if release, e := lockOutboundSetup(canceled, "test-gate"); e == nil {
		release()
		t.Fatal("canceled waiter acquired busy gate")
	}
	unlock()
	outboundSetupGates.Lock()
	_, leaked := outboundSetupGates.entries["test-gate"]
	outboundSetupGates.Unlock()
	if leaked {
		t.Fatal("gate leaked")
	}
}
func TestAutomaticOutboundSIPCallerDeadlineAndPreflight(t *testing.T) {
	a, ctx, _, _ := outboundSIPFixture(t, "telnyx")
	outboundSetupGatewayFixture(t)
	s := newSIPSetupProvider(t, "telnyx")
	s.blockPath = "/outbound_voice_profiles"
	s.entered = make(chan struct{})
	caller, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, e := a.configureOutboundTransport(ctx, automaticOutboundArgs(), caller); done <- e }()
	select {
	case <-s.entered:
	case <-time.After(time.Second):
		t.Fatal("did not reach provider")
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancellation accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("provider request ignored cancellation")
	}
	s.blockPath = ""
	t.Setenv("TELEPHONY_SIP_TLS_CERT_FILE", "/missing-certificate")
	if _, e := a.configureOutboundTransport(ctx, automaticOutboundArgs()); e == nil {
		t.Fatal("missing certificate accepted")
	}
	if len(s.writes) != 0 {
		t.Fatal("preflight mutated carrier")
	}
}
func TestAutomaticOutboundSIPAuthorizationAndOptions(t *testing.T) {
	a, ctx, _, _ := outboundSIPFixture(t, "telnyx")
	outboundSetupGatewayFixture(t)
	s := newSIPSetupProvider(t, "telnyx")
	for _, args := range []map[string]any{{"carrier_connection_id": 99, "transport": "sip_direct", "provision": true}, {"carrier_connection_id": 10, "phone_number": "+33123456799", "transport": "sip_direct", "provision": true}} {
		if _, e := a.configureOutboundTransport(ctx, args); e == nil {
			t.Fatal("unauthorized setup accepted")
		}
	}
	if len(s.writes) > 0 {
		t.Fatal("unauthorized writes")
	}
	result, e := a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "setup_options": true})
	if e != nil || result["setup_options"] == nil {
		t.Fatal("setup options missing")
	}
	if len(s.writes) > 0 {
		t.Fatal("reading options mutated carrier")
	}
	s.failPath = "/outbound_voice_profiles"
	result, e = a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "setup_options": true})
	if e != nil || result["outbound_transport"] == nil {
		t.Fatal("failed SIP options blocked Voice API settings")
	}
}

func TestAutomaticOutboundSIPUncertainCreateWaitsForVisibility(t *testing.T) {
	a, ctx, _, _ := outboundSIPFixture(t, "telnyx")
	outboundSetupGatewayFixture(t)
	s := newSIPSetupProvider(t, "telnyx")
	s.losePath = "/credential_connections"
	if _, e := a.configureOutboundTransport(ctx, automaticOutboundArgs()); e == nil {
		t.Fatal("lost response accepted")
	}
	saved := s.connections
	s.connections = nil // provider list temporarily lags the accepted write
	if _, e := a.configureOutboundTransport(ctx, automaticOutboundArgs()); e == nil || !strings.Contains(e.Error(), "uncertain") {
		t.Fatalf("expected visibility wait, got %v", e)
	}
	if s.writes["/credential_connections"] != 1 {
		t.Fatal("uncertain creation was repeated")
	}
	s.connections = saved
	if _, e := a.configureOutboundTransport(ctx, automaticOutboundArgs()); e != nil {
		t.Fatal(e)
	}
}
func TestAutomaticOutboundSIPRetainsManualAndInheritedSelection(t *testing.T) {
	a, ctx, _, cfg := outboundSIPFixture(t, "telnyx")
	outboundSetupGatewayFixture(t)
	stubSIPVerification(t, "telnyx", nil)
	if _, e := a.configureOutboundTransport(ctx, map[string]any{"carrier_connection_id": 10, "transport": "sip_direct", "config": cfg, "username": "existing-user", "password": "existing-secret"}); e != nil {
		t.Fatal(e)
	}
	args := automaticOutboundArgs()
	args["phone_number"] = "+33123456780"
	if _, e := a.configureOutboundTransport(ctx, args); e != nil {
		t.Fatal(e)
	}
	_, encrypted, scope, e := a.outboundTransport("project-a", 10, "+33123456780")
	if e != nil || scope != "+33123456780" {
		t.Fatal("number override missing")
	}
	secret, e := openOutboundSIPCredentials(ctx, "project-a", 10, scope, encrypted)
	if e != nil || secret.Password != "existing-secret" {
		t.Fatal("inherited credentials were lost or rotated")
	}
}
