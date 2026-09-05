package main

// Regressions reproduced against telephony/v0.3.7 and fixed on this branch.
import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
)

func TestAuditPlivoAnswerMustOpenMicrophoneGate(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "plivo", Fields: map[string]string{"password": "audit-token"}}})
	row := testCall("audit-plivo", "ringing")
	row.CarrierSlug = "plivo"
	row.PeerKind = peerKindHuman
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"CallUUID": {"answered-uuid"}, "CallStatus": {"in-progress"}}
	req := httptest.NewRequest(http.MethodPost, "/xml/plivo/"+row.ID+"?token="+row.CallbackSecret, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signPlivoTestRequest(app, req, form, "audit-token")
	response := httptest.NewRecorder()
	app.handlePlivoXML(response, req)
	if response.Code != 200 {
		t.Fatalf("answer callback: %d %s", response.Code, response.Body.String())
	}
	stored, err := app.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	hub := &softphoneHub{}
	hub.setCallState(stored.Direction, stored.Status)
	if !hub.microphoneReady() {
		t.Fatalf("valid Plivo answer callback leaves status=%s and microphone gate closed", stored.Status)
	}
}

func TestAuditFragmentedMessageMustRespectTotalLimit(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_ = server.SetDeadline(time.Now().Add(2 * time.Second))
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	writer := newWebSocketWriterPump(server, ws.StateServerSide)
	defer writer.Stop()
	go func() {
		payload := make([]byte, maxCarrierFrameBytes/2+1)
		_ = ws.WriteFrame(client, ws.MaskFrameInPlace(ws.NewFrame(ws.OpBinary, false, payload)))
		_ = ws.WriteFrame(client, ws.MaskFrameInPlace(ws.NewFrame(ws.OpContinuation, true, payload)))
	}()
	data, _, err := readWebSocketData(server, ws.StateServerSide, writer)
	if err == nil && len(data) > maxCarrierFrameBytes {
		t.Fatalf("accepted fragmented message of %d bytes above %d-byte limit", len(data), maxCarrierFrameBytes)
	}
}

func TestAuditRecordingRetryMustNotStealImportClaim(t *testing.T) {
	app, db := auditRoutingSetup(t)
	_ = app
	call := testCall("audit-recording", "completed")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	if _, err := db.upsertTwilioRecording(&call, "RE-audit", "completed", 1000, 1); err != nil {
		t.Fatal(err)
	}
	first, err := db.claimRecordingImport(call.ProjectID)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v %v", first, err)
	}
	if _, err := db.upsertTwilioRecording(&call, "RE-audit", "completed", 1000, 1); err != nil {
		t.Fatal(err)
	}
	second, err := db.claimRecordingImport(call.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatalf("duplicate callback allowed a second worker to claim active import %s", second.ID)
	}
}

func BenchmarkAuditResamplerDuplex20ms(b *testing.B) {
	in := newPCMResampler(8000, 24000)
	out := newPCMResampler(24000, 8000)
	input := make([]int16, 160)
	output := make([]int16, 480)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in.Process(input)
		out.Process(output)
	}
}

func auditRoutingSetup(t *testing.T) (*App, *callsDB) {
	t.Helper()
	db := testCallsDB(t)
	app := &App{}
	withRoutingTestDB(t, app, db)
	return app, db
}

func TestAuditRingGroupListingMustNotDeadlock(t *testing.T) {
	app, db := auditRoutingSetup(t)
	_, err := db.db.Exec(`INSERT INTO ring_groups(id,project_id,name,created_at,updated_at) VALUES('group','p1','Group','','')`)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := app.listRingGroups("p1"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		// Release the blocked query so this audit does not leak a goroutine.
		db.db.SetMaxOpenConns(2)
		if err := db.db.Ping(); err != nil {
			t.Fatal(err)
		}
		<-done
		t.Fatal("listing one ring group deadlocks with the production single-connection pool")
	}
}

func TestAuditRingGroupCannotModifyAnotherProject(t *testing.T) {
	app, db := auditRoutingSetup(t)
	// Bypass the independently tested listing deadlock to inspect ownership.
	db.db.SetMaxOpenConns(2)
	for _, project := range []string{"p1", "p2"} {
		if _, err := app.saveRoutingDestination(project, "dest-"+project, project, "browser", map[string]any{}, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.saveRingGroup("p1", "victim-group", "Victim", "simultaneous", 20, []ringGroupMemberRow{{DestinationID: "dest-p1", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	_, _ = app.saveRingGroup("p2", "victim-group", "Other", "sequential", 20, []ringGroupMemberRow{{DestinationID: "dest-p2", Enabled: true}})
	members, err := app.listRingGroupMembers("victim-group")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].DestinationID != "dest-p1" {
		t.Fatalf("other project replaced victim members: %+v", members)
	}
}

func auditPublishedRoute(t *testing.T, app *App, db *callsDB, def routingDefinition) *routeRow {
	t.Helper()
	raw, _ := json.Marshal(def)
	flow, err := app.saveRoutingFlow("p1", "audit-flow", "Audit", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	version, validation, err := app.publishRoutingFlow("p1", flow.ID)
	if err != nil || len(validation) > 0 {
		t.Fatalf("publish: %v %v", err, validation)
	}
	route := &routeRow{ID: "audit-route", ProjectID: "p1", CarrierSlug: "twilio", CarrierConnectionID: 9, PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, AnswerMode: answerModeHumanBrowser, TimeoutSec: 60, FlowID: flow.ID, PublishedFlowVersionID: version.ID, Secret: "secret"}
	if err := db.insertRoute(*route); err != nil {
		t.Fatal(err)
	}
	return route
}

func TestAuditNestedIVRMustRetainPriorDigits(t *testing.T) {
	app, db := auditRoutingSetup(t)
	def := routingDefinition{Entry: "first", Nodes: []routingNode{
		{ID: "first", Type: "dtmf_menu", Branches: map[string]string{"1": "second", "default": "end"}},
		{ID: "second", Type: "dtmf_menu", Branches: map[string]string{"2": "end", "default": "end"}},
		{ID: "end", Type: "hangup"},
	}}
	route := auditPublishedRoute(t, app, db, def)
	row := testCall("nested", "pending")
	row.ProjectID = "p1"
	row.RouteID = route.ID
	row.RoutingFlowVersionID = route.PublishedFlowVersionID
	if err := db.insertCall(row); err != nil {
		t.Fatal(err)
	}
	_, plan, err := app.routingPlanForCall(&row, map[string]string{"first": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.NodeID != "second" {
		t.Fatalf("first selection: %+v", plan)
	}
	if err := app.updateCallRoutingPlan(&row, plan); err != nil {
		t.Fatal(err)
	}
	_, next, err := app.routingPlanForCall(&row, map[string]string{"second": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if next.TerminalType != "hangup" {
		t.Fatalf("second selection went back to %q instead of completing", next.NodeID)
	}
}

func TestAuditPinnedVersionMustFreezeDestination(t *testing.T) {
	app, db := auditRoutingSetup(t)
	if _, err := app.saveRoutingDestination("p1", "dest", "Desk", "browser", map[string]any{}, true); err != nil {
		t.Fatal(err)
	}
	route := auditPublishedRoute(t, app, db, routingDefinition{Entry: "answer", Nodes: []routingNode{{ID: "answer", Type: "destination", Config: map[string]any{"destination_id": "dest"}}}})
	before, err := app.resolveInboundRoutingPlan(route, "+12025550101", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.saveRoutingDestination("p1", "dest", "Desk", "ai", map[string]any{"agent_id": 8, "directive": "Changed"}, true); err != nil {
		t.Fatal(err)
	}
	after, err := app.resolveInboundRoutingPlan(route, "+12025550101", nil)
	if err != nil {
		t.Fatal(err)
	}
	if after.AnswerMode != before.AnswerMode || after.AgentID != before.AgentID {
		t.Fatalf("same published version changed from %s/agent %d to %s/agent %d", before.AnswerMode, before.AgentID, after.AnswerMode, after.AgentID)
	}
}

func TestAuditRetryIngressMustNotRepinCall(t *testing.T) {
	app, db := auditRoutingSetup(t)
	if _, err := app.saveRoutingDestination("p1", "dest", "Desk", "browser", map[string]any{}, true); err != nil {
		t.Fatal(err)
	}
	route := auditPublishedRoute(t, app, db, routingDefinition{Entry: "answer", Nodes: []routingNode{{ID: "answer", Type: "destination", Config: map[string]any{"destination_id": "dest"}}}})
	first, _, err := app.recordInboundCall(route, "CA-retry", "+12025550101", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	first, _ = db.findCall(first.ID)
	v2, _, err := app.publishRoutingFlow("p1", route.FlowID)
	if err != nil {
		t.Fatal(err)
	}
	route, _ = db.findRoute(route.ID)
	if _, _, err := app.recordInboundCall(route, "CA-retry", "+12025550101", route.PhoneNumber); err != nil {
		t.Fatal(err)
	}
	after, _ := db.findCall(first.ID)
	if after.RoutingFlowVersionID != first.RoutingFlowVersionID {
		t.Fatalf("duplicate callback repinned %s to %s (%s)", first.RoutingFlowVersionID, after.RoutingFlowVersionID, v2.ID)
	}
}

func TestAuditLegacyAnswerModeChangeMustTakeEffect(t *testing.T) {
	app, db := auditRoutingSetup(t)
	route := routeRow{ID: "legacy", ProjectID: "p1", CarrierSlug: "twilio", CarrierConnectionID: 9, PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, AnswerMode: answerModeHumanBrowser, TimeoutSec: 60}
	if err := db.insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if err := app.ensureLegacyRoutingFlows(nil); err != nil {
		t.Fatal(err)
	}
	if err := db.updateRouteAnswerMode(route.ID, answerModeRealtimeImmediate, "Answer calls", "", "Hello"); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.findRoute(route.ID)
	plan, err := app.resolveInboundRoutingPlan(stored, "+12025550101", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AnswerMode != answerModeRealtimeImmediate {
		t.Fatalf("saved route mode=%s but actual mode=%s", stored.AnswerMode, plan.AnswerMode)
	}
}
