package main

import (
	"bytes"
	"context"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func carrierMediaFixture(t *testing.T, direction string) (*App, *sdk.AppCtx, map[string]any) {
	t.Helper()
	a, ctx, _, cfg := outboundSIPFixture(t, "telnyx")
	if direction == "inbound" {
		r := routeRow{ID: "carrier-buffer-route", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10, PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "test", InboundTransport: inboundTransportSIPDirect, TransportConfig: `{"provider":"telnyx","fqdn_connection_id":"sip-1"}`}
		if err := a.db().insertRoute(r); err != nil {
			t.Fatal(err)
		}
		return a, ctx, map[string]any{"route_id": r.ID}
	}
	cfg.ResourceType = "credential_connection"
	raw, _ := json.Marshal(cfg)
	_, err := a.db().db.Exec(`INSERT INTO outbound_transports(project_id,connection_id,phone_number,provider,transport,config_json,credentials_cipher,verified_at) VALUES('project-a',10,'','telnyx','sip_direct',?,?,'')`, string(raw), []byte{})
	if err != nil {
		t.Fatal(err)
	}
	return a, ctx, map[string]any{"carrier_connection_id": 10}
}

func TestCarrierMediaDefaultsApplyVerifyAndIsolation(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound"} {
		t.Run(direction, func(t *testing.T) {
			a, ctx, args := carrierMediaFixture(t, direction)
			effective := (carrierJitterBufferPolicy{}).resolved()
			reads, writes := 0, 0
			fail := false
			old := http.DefaultTransport
			http.DefaultTransport = sipVerificationRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.telnyx.com" || r.Header.Get("Authorization") != "Bearer api-secret" {
					t.Fatal("wrong authorized carrier boundary")
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("missing shared deadline")
				}
				if fail {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"token":"do-not-copy"}`)), Header: http.Header{}}, nil
				}
				if r.Method == http.MethodPatch {
					writes++
					if err := a.checkOutboundAdmission("project-a", "telnyx", 10, "+12025550100"); err == nil {
						t.Fatal("new call admitted while connection is changing")
					}
					var body map[string]map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if len(body) != 1 || body["jitter_buffer"] == nil {
						t.Fatal("unrelated settings included in provider patch")
					}
					var err error
					effective, err = func() (carrierJitterBufferPolicy, error) {
						p, e := parseTelnyxJitterBuffer(map[string]any{"jitter_buffer": body["jitter_buffer"]})
						if e != nil {
							return carrierJitterBufferPolicy{}, e
						}
						return *p, nil
					}()
					if err != nil {
						t.Fatal(err)
					}
				} else if r.Method == http.MethodGet {
					reads++
				} else {
					t.Fatal("unexpected provider mutation", r.Method)
				}
				raw, _ := json.Marshal(map[string]any{"data": map[string]any{"id": "sip-1", "jitter_buffer": effective.telnyx(), "password": "never-return-this"}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: http.Header{}}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = old })
			result, err := a.configureCarrierMedia(t.Context(), ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			view := result["carrier_jitter_buffer"].(carrierJitterBufferView)
			if view.Requested.Enabled || view.Requested.MinMS != 60 || view.Requested.MaxMS != 200 || reads != 0 || writes != 0 {
				t.Fatal("default enabled buffer or called provider")
			}
			args["carrier_jitter_buffer"] = map[string]any{"enabled": true, "min_ms": 60, "max_ms": 200}
			result, err = a.configureCarrierMedia(t.Context(), ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if result["carrier_jitter_buffer"].(carrierJitterBufferView).State != "pending" || writes != 0 {
				t.Fatal("saving policy applied an unrequested carrier change")
			}
			args["apply"] = true
			delete(args, "carrier_jitter_buffer")
			result, err = a.configureCarrierMedia(t.Context(), ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			view = result["carrier_jitter_buffer"].(carrierJitterBufferView)
			if view.State != "verified" || view.Effective == nil || !view.Effective.Enabled || view.VerifiedAt == "" || writes != 1 || reads != 2 {
				t.Fatal("carrier result not verified", view, reads, writes)
			}
			if _, busy := a.carrierMediaDrains.Load(int64(10)); busy {
				t.Fatal("configuration gate leaked")
			}
			foreign, err := a.carrierBufferView("other-project", carrierMediaTarget{10, "telnyx", view.ResourceType, view.ResourceID})
			if err != nil || foreign.Requested.Enabled || foreign.Effective != nil {
				t.Fatal("carrier policy crossed project")
			}
			fail = true
			args["fresh"] = true
			delete(args, "apply")
			result, err = a.configureCarrierMedia(t.Context(), ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			view = result["carrier_jitter_buffer"].(carrierJitterBufferView)
			if view.State != "verification_unavailable" || view.Effective == nil || !view.Effective.Enabled || view.VerifiedAt == "" {
				t.Fatal("temporary failure changed verified evidence", view)
			}
			raw, _ := json.Marshal(result)
			for _, secret := range []string{"api-secret", "never-return-this", "do-not-copy"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("carrier credentials escaped into diagnostics")
				}
			}
		})
	}
}

func TestCarrierMediaActiveCallsDeferAndNewAdmissionGate(t *testing.T) {
	a, ctx, args := carrierMediaFixture(t, "inbound")
	row := callRow{ID: "carrier-buffer-live", ThreadID: "carrier-buffer-live", Direction: "inbound", ProjectID: "project-a", CarrierConnectionID: 10, CarrierSlug: "telnyx", Status: "in-progress", PlacedAt: ringTime(time.Now())}
	if err := a.db().insertCall(row, false); err != nil {
		t.Fatal(err)
	}
	args["carrier_jitter_buffer"] = map[string]any{"enabled": true}
	args["apply"] = true
	old := http.DefaultTransport
	http.DefaultTransport = sipVerificationRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Error("active connection contacted provider")
		return nil, context.Canceled
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	result, err := a.configureCarrierMedia(t.Context(), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if result["carrier_jitter_buffer"].(carrierJitterBufferView).State != "pending" {
		t.Fatal("live calls not protected")
	}
	var status string
	if err = a.db().db.QueryRow(`SELECT status FROM calls WHERE id=?`, row.ID).Scan(&status); err != nil || status != "in-progress" {
		t.Fatal("live call changed")
	}
	if _, busy := a.carrierMediaDrains.Load(int64(10)); busy {
		t.Fatal("busy connection admission was disabled")
	}
}

func TestCarrierMediaValidationAndAuthorization(t *testing.T) {
	a, ctx, args := carrierMediaFixture(t, "inbound")
	for _, policy := range []any{map[string]any{"enabled": "yes"}, map[string]any{"enabled": nil}, map[string]any{"min_ms": 0}, map[string]any{"max_ms": nil}, map[string]any{"min_ms": 39}, map[string]any{"max_ms": 401}, map[string]any{"min_ms": 200, "max_ms": 60}, map[string]any{"min_ms": 1.5}, map[string]any{"enabled": true, "extra": true}, "bad"} {
		args["carrier_jitter_buffer"] = policy
		if _, err := a.configureCarrierMedia(t.Context(), ctx, args); err == nil {
			t.Fatal("malformed/out-of-range policy accepted", policy)
		}
	}
	if _, err := a.configureCarrierMedia(t.Context(), ctx.WithProject("other-project"), map[string]any{"route_id": "carrier-buffer-route"}); err == nil {
		t.Fatal("foreign route accepted")
	}
}

func TestCarrierMediaSharedResourceDefersAcrossBindings(t *testing.T) {
	a, _, _ := carrierMediaFixture(t, "inbound")
	alias := routeRow{ID: "alias-route", ProjectID: "project-b", CarrierSlug: "telnyx", CarrierConnectionID: 11, PhoneNumber: "+12025550101", AgentID: 7, Enabled: true, Secret: "test", InboundTransport: inboundTransportSIPDirect, TransportConfig: `{"fqdn_connection_id":"sip-1"}`}
	if err := a.db().insertRoute(alias); err != nil {
		t.Fatal(err)
	}
	target := carrierMediaTarget{10, "telnyx", "fqdn_connection", "sip-1"}
	release, idle, err := a.beginCarrierMediaChange(target)
	if err != nil || !idle {
		t.Fatal("idle aliases unavailable", err)
	}
	if _, busy := a.carrierMediaDrains.Load(int64(11)); !busy {
		t.Fatal("shared alias was not gated")
	}
	release()
	if _, busy := a.carrierMediaDrains.Load(int64(11)); busy {
		t.Fatal("alias gate leaked")
	}
	if err := a.db().insertCall(callRow{ID: "alias-live", ThreadID: "alias-live", ProjectID: "project-b", CarrierSlug: "telnyx", CarrierConnectionID: 11, Direction: "inbound", Status: "in-progress", PlacedAt: ringTime(time.Now())}, false); err != nil {
		t.Fatal(err)
	}
	_, idle, err = a.beginCarrierMediaChange(target)
	if err != nil || idle {
		t.Fatal("active alias did not prevent provider mutation", err)
	}
}

func TestCarrierMediaFailedReadbackAndCancellationPreserveVerified(t *testing.T) {
	a, ctx, args := carrierMediaFixture(t, "inbound")
	target, err := a.carrierMediaTarget("project-a", "carrier-buffer-route", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	effective := (carrierJitterBufferPolicy{}).resolved()
	saved := carrierJitterBufferView{Requested: effective, Effective: &effective, State: "verified", VerifiedAt: ringTime(time.Now())}
	if err = a.saveCarrierBufferView("project-a", target, saved); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	http.DefaultTransport = sipVerificationRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPatch {
			cancel()
			return nil, context.Canceled
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"id":"sip-1","jitter_buffer":{"enable_jitter_buffer":true,"jitterbuffer_msec_min":60.5,"jitterbuffer_msec_max":200}}}`)), Header: http.Header{}}, nil
	})
	args["fresh"] = true
	result, err := a.configureCarrierMedia(t.Context(), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	v := result["carrier_jitter_buffer"].(carrierJitterBufferView)
	if v.State != "verification_unavailable" || v.Effective == nil || *v.Effective != effective || v.VerifiedAt != saved.VerifiedAt {
		t.Fatal("invalid readback destroyed verified value", v)
	}
	args["apply"] = true
	_, _ = a.configureCarrierMedia(parent, ctx, args)
	if _, busy := a.carrierMediaDrains.Load(int64(10)); busy {
		t.Fatal("canceled provider operation leaked admission gate")
	}
}

func TestCarrierMediaUnsupportedProviderDoesNotMutate(t *testing.T) {
	a, ctx, args := carrierMediaFixture(t, "inbound")
	if _, err := a.db().db.Exec(`UPDATE inbound_routes SET carrier_slug='twilio' WHERE id='carrier-buffer-route'`); err != nil {
		t.Fatal(err)
	}
	result, err := a.configureCarrierMedia(t.Context(), ctx, args)
	if err != nil || result["carrier_jitter_buffer"].(carrierJitterBufferView).State != "unsupported" {
		t.Fatal("unsupported carrier status missing", err)
	}
	args["carrier_jitter_buffer"] = map[string]any{"enabled": true}
	args["apply"] = true
	if _, err = a.configureCarrierMedia(t.Context(), ctx, args); err == nil {
		t.Fatal("unsupported carrier buffer enabled")
	}
}
