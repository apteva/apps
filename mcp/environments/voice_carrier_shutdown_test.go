package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
)

type carrierShutdownRuntime struct {
	*cancelledVoiceRuntime
	url                    string
	callbackMu             sync.Mutex
	closedCallbacks        int
	deletedBeforeCallbacks bool
}

func (p *carrierShutdownRuntime) GetRuntimeAppEndpoint(string, string) (*sdk.RuntimeAppEndpoint, error) {
	return &sdk.RuntimeAppEndpoint{PlatformURL: p.url, GatewayURL: p.url}, nil
}
func (p *carrierShutdownRuntime) CallRuntimeAppAsAgentResult(_, _, _, tool string, _ map[string]any, out any) error {
	var v any
	switch tool {
	case "telephony_routes_create":
		v = map[string]any{"inbound_url": p.url + "/inbound", "route": map[string]any{"id": "route", "phone_number": "+15550000001"}}
	case "telephony_active_calls":
		v = map[string]any{"calls": []any{map[string]any{"call_id": "call", "to": "+15550000001", "thread_id": "reception-target", "status": "active"}}}
	default:
		return fmt.Errorf("unexpected tool %s", tool)
	}
	raw, _ := json.Marshal(v)
	return json.Unmarshal(raw, out)
}
func (p *carrierShutdownRuntime) DestroyRuntime(id string) error {
	p.callbackMu.Lock()
	p.deletedBeforeCallbacks = p.closedCallbacks != 2
	p.callbackMu.Unlock()
	return p.cancelledVoiceRuntime.DestroyRuntime(id)
}

func TestCarrierMicrophoneRunsDuringCallbackAndFinalizesBeforeDeletion(t *testing.T) {
	s, base := auditService(t)
	p := &carrierShutdownRuntime{cancelledVoiceRuntime: &cancelledVoiceRuntime{auditPlatform: base, db: s.db, stopped: map[string]bool{}}}
	callbackStarted := make(chan struct{})
	mediaInput := make(chan struct{})
	var mediaOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/caller":
			conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		case "/inbound":
			fmt.Fprintf(w, `<Response><Connect><Stream url="%s/media" statusCallback="%s/callback"/></Connect></Response>`, "wss"+strings.TrimPrefix(p.url, "http"), p.url)
		case "/media":
			conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var frame struct {
					Event string `json:"event"`
				}
				_ = json.Unmarshal(raw, &frame)
				if frame.Event == "media" {
					mediaOnce.Do(func() { close(mediaInput) })
				}
			}
		case "/callback":
			_ = r.ParseForm()
			if r.Form.Get("StreamEvent") == "stream-started" {
				close(callbackStarted)
				<-r.Context().Done()
				return
			}
			p.callbackMu.Lock()
			p.closedCallbacks++
			p.callbackMu.Unlock()
			w.WriteHeader(204)
		case "/inbound/status":
			p.callbackMu.Lock()
			p.closedCallbacks++
			p.callbackMu.Unlock()
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p.url = server.URL
	p.bridge = "ws" + strings.TrimPrefix(server.URL, "http") + "/caller"
	s.ctx = sdk.NewAppCtxForTest(nil, s.db.db, nil, p, nil).WithProject("audit")
	run := &Run{ID: "run", RuntimeID: "rt", Status: "running", StartedAt: time.Now()}
	if err := s.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	defer s.stopRun(run)
	fixture := &ProtocolFixtureInstance{RunID: run.ID, ID: "carrier", Pack: protocolPackTelephonyCarrier, Version: "1", Protocol: protocolTwilio, TargetApp: "telephony", Status: "running", Config: map[string]any{"auth_token": "synthetic"}}
	if err := s.db.createProtocolFixture(fixture); err != nil {
		t.Fatal(err)
	}
	done := make(chan *VoiceCall, 1)
	go func() {
		call, _ := s.runVoiceCall(context.Background(), run, VoiceFixtureSpec{TargetAgent: "main", CallerGoal: "Wait for greeting", Transport: "carrier", ProtocolFixture: "carrier", TimeoutSeconds: 30})
		done <- call
	}()
	select {
	case <-callbackStarted:
	case early := <-done:
		t.Fatalf("carrier failed before callback: %+v", early)
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	select {
	case <-mediaInput:
	case <-time.After(time.Second):
		t.Fatal("backend callback blocked microphone startup")
	}
	if err := s.stopRun(run); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-done:
		if call == nil || call.Status != "cancelled" || call.Cleanup.Status != "complete" {
			t.Fatalf("call=%+v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled carrier call leaked")
	}
	p.callbackMu.Lock()
	early, n := p.deletedBeforeCallbacks, p.closedCallbacks
	p.callbackMu.Unlock()
	if early || n != 2 || p.destroyBeforeResult {
		t.Fatalf("deleted before callbacks/result: early=%v callbacks=%d result=%v", early, n, p.destroyBeforeResult)
	}
}
