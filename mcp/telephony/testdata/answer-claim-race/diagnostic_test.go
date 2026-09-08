package main

import (
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type diagnosticSpawn struct {
	*answerPlatform
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (p *diagnosticSpawn) SpawnRealtimeThread(req sdk.RealtimeSpawnRequest) (*sdk.RealtimeSpawnResult, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.proceed
	return p.answerPlatform.SpawnRealtimeThread(req)
}
func TestDiagnosticConcurrentAnswerWebhook(t *testing.T) {
	p := &diagnosticSpawn{answerPlatform: &answerPlatform{}, entered: make(chan struct{}), proceed: make(chan struct{})}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	previous := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = previous }()
	a := &App{installID: 42}
	route := routeRow{ID: "diagnostic", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 9, PhoneNumber: "+14155550101", AgentID: 7, Enabled: true, TimeoutSec: 60, AnswerMode: answerModeRealtimeImmediate, AutoDirective: "Help.", AutoGreeting: "Hello.", Secret: "route-secret"}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureLegacyRoutingFlows(ctx); err != nil {
		t.Fatal(err)
	}
	routePtr, _ := a.db().findRoute(route.ID)
	route = *routePtr
	webhook := func() *httptest.ResponseRecorder {
		form := url.Values{"CallSid": {"CA-diagnostic"}, "From": {"+14155550102"}, "To": {route.PhoneNumber}}
		req := httptest.NewRequest("POST", strings.TrimPrefix(a.inboundRouteURL(route), a.publicAppURL()), strings.NewReader(form.Encode()))
		signTwilioTestRequest(t, a, req, form)
		rec := httptest.NewRecorder()
		a.handleTwilioInbound(rec, req)
		return rec
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- webhook() }()
	<-p.entered
	go func() { second <- webhook() }()
	var early *httptest.ResponseRecorder
	select {
	case early = <-second:
	case <-time.After(100 * time.Millisecond):
	}
	close(p.proceed)
	one := <-first
	var two *httptest.ResponseRecorder
	if early != nil {
		two = early
	} else {
		two = <-second
	}
	stored, _ := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, "CA-diagnostic")
	if stored.Status == "failed" || strings.Contains(two.Body.String(), "<Hangup") || strings.Contains(one.Body.String(), "<Hangup") {
		t.Fatalf("concurrent preparation caused terminal failure: status=%s error=%s first=%s second=%s", stored.Status, stored.ErrorMessage, one.Body.String(), two.Body.String())
	}
}
