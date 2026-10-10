package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws/wsutil"
)

func setAdmissionRule(t *testing.T, a *App, ctx *sdk.AppCtx, scope, value string, enabled bool) {
	t.Helper()
	if _, err := a.outboundPolicy(ctx, map[string]any{"scope": scope, "value": value, "enabled": enabled}); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimeOutboundDisablePreservesExistingAndOtherCalls(t *testing.T) {
	p := multiCarrierPlatform()
	p.integrationResponse["dial_call"] = json.RawMessage(`{"data":{"call_control_id":"second-telnyx"}}`)
	a, ctx := withTelephonyTestContext(t, p)
	original, err := a.placeHumanCallForUserWithOptions(ctx, nil, "project-a", "+33189999901", "+33189313432", 30, nil, outboundCallOptions{}, "original")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := a.db().findCall(original.CallID)

	setAdmissionRule(t, a, ctx, "number", row.FromNumber, false)
	current, _ := a.db().findCall(row.ID)
	if current.Status != row.Status || current.PeerToken != row.PeerToken {
		t.Fatalf("existing call changed: %+v", current)
	}

	if reason, _ := a.phoneMediaCheck(current, current.PeerToken); reason != "" {
		t.Fatal(reason)
	}
	replay, err := a.placeHumanCallForUserWithOptions(ctx, nil, "project-a", row.ToNumber, row.FromNumber, 30, nil, outboundCallOptions{}, "original")
	if err != nil || replay.CallID != original.CallID {
		t.Fatalf("idempotent replay lost: %+v %v", replay, err)
	}
	denied := phoneTestRequest(a, nil, "POST", "/softphone/place", map[string]any{"to": "+33189999902", "from": row.FromNumber, "idempotency_key": "new"})
	if denied.Code != 403 || !strings.Contains(denied.Body.String(), `"code":"outbound_disabled"`) {
		t.Fatalf("cached dial allowed: %d %s", denied.Code, denied.Body)
	}
	if _, _, _, err = a.resolveCarrierBinding(ctx, "project-a", row.FromNumber); !errors.Is(err, errOutboundDisabled) {
		t.Fatalf("AI selection: %v", err)
	}
	if err = a.checkOutboundAdmission("other-project", "twilio", 11, row.FromNumber); err != nil {
		t.Fatalf("project leaked: %v", err)
	}
	if _, err = a.placeHumanCall(ctx, "project-a", "+33189999903", "+33189313431", 30, nil); err != nil {
		t.Fatalf("other provider disabled: %v", err)
	}
	for _, call := range p.integrationCalls {
		if call.Tool == "hangup_call" || call.Tool == "update_call" {
			t.Fatalf("disable issued carrier command: %+v", call)
		}
	}
	setAdmissionRule(t, a, ctx, "number", row.FromNumber, true)
	if _, _, _, err = a.resolveCarrierBinding(ctx, "project-a", row.FromNumber); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeOutboundProviderAndConnectionScopes(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, multiCarrierPlatform())
	for _, scope := range []string{"provider", "connection"} {
		value := "twilio"
		if scope == "connection" {
			value = "11"
		}
		setAdmissionRule(t, a, ctx, scope, value, false)
		for _, number := range []string{"+33189313432", "+33189313499"} {
			if !errors.Is(a.checkOutboundAdmission("project-a", "twilio", 11, number), errOutboundDisabled) {
				t.Fatal("scope did not cover all numbers")
			}
		}
		if _, _, _, err := a.resolveCarrierBinding(ctx, "project-a", ""); !errors.Is(err, errOutboundDisabled) {
			t.Fatalf("default silently switched: %v", err)
		}
		if err := a.checkOutboundAdmission("project-a", "telnyx", 10, "+33189313431"); err != nil {
			t.Fatal(err)
		}
		if scope == "connection" {
			if err := a.checkOutboundAdmission("project-a", "twilio", 12, "+33189313499"); err != nil {
				t.Fatal("other account disabled", err)
			}
		}
		result, err := a.connectedNumbers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range result["numbers"].([]connectedNumberView) {
			if n.Provider == "twilio" && n.OutboundEnabled {
				t.Fatal("disabled number advertised")
			}
		}
		setAdmissionRule(t, a, ctx, scope, value, true)
	}
}

type inventoryFailurePlatform struct {
	*answerPlatform
	failIDs         map[int64]bool
	failCredentials bool
}

func (p *inventoryFailurePlatform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if p.failIDs[id] && tool == "list_phone_numbers" {
		return nil, errors.New("provider unavailable")
	}
	return p.answerPlatform.ExecuteIntegrationTool(id, tool, args)
}
func (p *inventoryFailurePlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	if p.failCredentials && p.failIDs[id] {
		return nil, errors.New("credential unavailable")
	}
	return p.answerPlatform.GetConnectionCredentials(id)
}
func TestRuntimeInventoryFailureIsolation(t *testing.T) {
	for _, credentials := range []bool{false, true} {
		t.Run(map[bool]string{false: "inventory", true: "credentials"}[credentials], func(t *testing.T) {
			p := &inventoryFailurePlatform{multiCarrierPlatform(), map[int64]bool{11: true}, credentials}
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
			old := globalCtx
			globalCtx = ctx
			t.Cleanup(func() { globalCtx = old })
			a := &App{}
			result, err := a.connectedNumbers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			numbers := result["numbers"].([]connectedNumberView)
			warnings := result["warnings"].([]numberInventoryWarning)
			if len(numbers) != 1 || numbers[0].Provider != "telnyx" || !numbers[0].OutboundEnabled || result["inventory_status"] != "partial" || len(warnings) != 1 || warnings[0].ConnectionID != 11 {
				t.Fatalf("partial result: %+v", result)
			}
			p.failIDs[10] = true
			// Explicitly verify the new carrier failure rather than the 25-second snapshot.
			result, err = a.connectedNumbers(ctx, inventoryFreshContext(context.Background(), true))
			if err != nil || result["inventory_status"] != "unavailable" || len(result["numbers"].([]connectedNumberView)) != 0 || len(result["warnings"].([]numberInventoryWarning)) != 2 {
				t.Fatalf("all failures hidden: %+v %v", result, err)
			}
		})
	}
}

func TestRuntimeInboundDisableKeepsCarrierAndSnapshots(t *testing.T) {
	a, ctx, p, route, row, key := reliabilityFixture(t)
	before := len(p.integrationCalls)
	if _, err := a.disableInboundRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	if len(p.integrationCalls) != before {
		t.Fatal("route disable mutated carrier resources")
	}
	if _, _, err := a.recordInboundCall(route, "new-session", row.FromNumber, row.ToNumber); !errors.Is(err, errInboundDisabled) {
		t.Fatalf("stale route snapshot admitted new call: %v", err)
	}
	duplicate, created, err := a.recordInboundCall(route, row.CarrierSID, row.FromNumber, row.ToNumber)
	if err != nil || created || duplicate.ID != row.ID {
		t.Fatalf("existing ingress lost: %+v %v", duplicate, err)
	}
	if rec := reliabilityEvent(t, a, route, row, key, "call.hangup"); rec.Code != http.StatusNoContent {
		t.Fatalf("callback lost: %d %s", rec.Code, rec.Body)
	}
	if _, err = a.setInboundRouteEnabled(route, true); err != nil {
		t.Fatal(err)
	}
	if err = a.checkInboundAdmission(route); err != nil {
		t.Fatal(err)
	}
	if len(p.integrationCalls) != before {
		t.Fatal("enable mutated carrier")
	}
}

func TestRuntimeDisableKeepsBidirectionalAudioAndReattach(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	row := insertSoftphoneCall(t, a, "in-progress")
	server := softphoneTestServer(t, a)
	peer := dialWS(t, server.URL+"/peer/"+row.ID+"/"+row.CallbackSecret)
	browser := dialWS(t, server.URL+"/softphone/media/"+row.ID+"/"+row.PeerToken)
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	if _, err := a.db().db.Exec(`INSERT INTO outbound_admission_rules VALUES('project-a','number',?,?)`, row.FromNumber, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	audio := pcm16ToBytes([]int16{1, -2, 3, -4})
	for _, sockets := range [][2]net.Conn{{peer, browser}, {browser, peer}} {
		if err := wsutil.WriteClientBinary(sockets[0], audio); err != nil {
			t.Fatal(err)
		}
		if got := readBinaryWithin(t, sockets[1], time.Second); string(got) != string(audio) {
			t.Fatal("audio changed")
		}
	}
	browser.Close()
	browser = dialWS(t, server.URL+"/softphone/media/"+row.ID+"/"+row.PeerToken)
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	if err := wsutil.WriteClientBinary(peer, audio); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, browser, time.Second); string(got) != string(audio) {
		t.Fatal("reattach failed")
	}
}

func TestRuntimeAuthorizedChoicesAndAdministrativeDenial(t *testing.T) {
	p := multiCarrierPlatform()
	a, ctx := withTelephonyTestContext(t, p)
	policy := phoneTestPolicy(t, a)
	policy.Groups[0].OutboundNumbers = []string{"+33189313432"}
	if w := phoneTestRequest(a, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	alice := phoneTestIdentity("alice")
	choices := phoneTestRequest(a, &alice, "GET", "/softphone/numbers", nil)
	if choices.Code != 200 || !strings.Contains(choices.Body.String(), "+33189313432") || strings.Contains(choices.Body.String(), "+33189313431") {
		t.Fatalf("choices leak: %d %s", choices.Code, choices.Body)
	}
	setAdmissionRule(t, a, ctx, "number", "+33189313432", false)
	choices = phoneTestRequest(a, &alice, "GET", "/softphone/numbers", nil)
	if choices.Code != 200 || strings.Contains(choices.Body.String(), "+33189313432") {
		t.Fatalf("disabled choice: %s", choices.Body)
	}
	access := phoneTestRequest(a, &alice, "GET", "/softphone/access", nil)
	if !strings.Contains(access.Body.String(), "+33189313432") {
		t.Fatal("existing permission was revoked")
	}
	for _, path := range []string{"/numbers/outbound-policy", "/numbers/routes/enable", "/_runtime/bindings"} {
		response := phoneTestRequest(a, &alice, "POST", path, map[string]any{"scope": "provider", "value": "twilio", "enabled": false})
		if response.Code != 403 && response.Code != 401 {
			t.Fatalf("admin exposed: %s %d", path, response.Code)
		}
	}
}

func TestRuntimeBindingDrainKeepsExistingCarrierUntilFinished(t *testing.T) {
	a, _, p, route, row, key := reliabilityFixture(t)
	req := runtimeBindingRequest{Phase: "prepare", ChangeID: "drain-1", Previous: map[string]any{"carrier": float64(9)}, Desired: map[string]any{"carrier": nil}}
	result, err := a.applyRuntimeBindingRequest(req)
	if err != nil || result["ready"] != false {
		t.Fatalf("active leg not retained: %+v %v", result, err)
	}
	if !errors.Is(a.checkOutboundAdmission(row.ProjectID, "telnyx", 9, row.ToNumber), errOutboundDisabled) {
		t.Fatal("draining carrier admitted new dial")
	}
	if !errors.Is(a.checkInboundAdmission(route), errInboundDisabled) {
		t.Fatal("draining carrier admitted inbound")
	}
	current, _ := a.db().findCall(row.ID)
	if current.Status != row.Status || len(p.integrationCalls) != 0 {
		t.Fatal("drain interrupted call")
	}
	req.Phase = "commit"
	if _, err = a.applyRuntimeBindingRequest(req); err == nil {
		t.Fatal("premature commit allowed")
	}
	if response := reliabilityEvent(t, a, route, row, key, "call.hangup"); response.Code != 204 {
		t.Fatal(response.Code)
	}
	if _, err = a.db().db.Exec(`UPDATE calls SET ended_at=?,media_active=0 WHERE id=?`, time.Now().UTC().Add(-time.Minute*2).Format(time.RFC3339), row.ID); err != nil {
		t.Fatal(err)
	}
	result, err = a.applyRuntimeBindingRequest(req)
	if err != nil || result["ready"] != true {
		t.Fatalf("commit failed: %+v %v", result, err)
	}
	if !errors.Is(a.checkInboundAdmission(route), errInboundDisabled) {
		t.Fatal("removed connection re-admitted stale webhook")
	}
	req = runtimeBindingRequest{Phase: "prepare", ChangeID: "rebind-2", Previous: map[string]any{"carrier": nil}, Desired: map[string]any{"carrier": float64(9)}}
	if _, err = a.applyRuntimeBindingRequest(req); err != nil {
		t.Fatal(err)
	}
	req.Phase = "commit"
	if _, err = a.applyRuntimeBindingRequest(req); err != nil {
		t.Fatal(err)
	}
	if err = a.checkInboundAdmission(route); err != nil {
		t.Fatal("rebind failed", err)
	}
}

func TestRuntimeBindingSignatureAndUnsupportedChanges(t *testing.T) {
	a, _ := withTelephonyTestContext(t, multiCarrierPlatform())
	t.Setenv("APTEVA_APP_TOKEN", "runtime-test")
	req := runtimeBindingRequest{Phase: "prepare", ChangeID: "signed", Previous: map[string]any{"carrier": float64(11)}, Desired: map[string]any{"carrier": float64(10)}}
	raw, _ := json.Marshal(req)
	request := httptest.NewRequest("POST", runtimeBindingsPath, strings.NewReader(string(raw)))
	response := httptest.NewRecorder()
	a.handleRuntimeBindings(response, request)
	if response.Code != 403 {
		t.Fatal("unsigned drain accepted")
	}
	mac := hmac.New(sha256.New, []byte("runtime-test"))
	mac.Write(raw)
	request = httptest.NewRequest("POST", runtimeBindingsPath, strings.NewReader(string(raw)))
	request.Header.Set("X-Apteva-Runtime-Signature", hex.EncodeToString(mac.Sum(nil)))
	response = httptest.NewRecorder()
	a.handleRuntimeBindings(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	req.ChangeID = "other"
	req.Previous["storage"] = float64(1)
	req.Desired["storage"] = float64(2)
	if _, err := a.applyRuntimeBindingRequest(req); err == nil {
		t.Fatal("unsafe role hot reloaded")
	}
	req.ChangeID = "signed"
	delete(req.Previous, "storage")
	delete(req.Desired, "storage")
	req.Desired["carrier"] = nil
	if _, err := a.applyRuntimeBindingRequest(req); err == nil {
		t.Fatal("change id payload mismatch accepted")
	}
}

type blockingAdmissionCarrier struct {
	started chan struct{}
	release chan struct{}
	places  atomic.Int32
}

func (c *blockingAdmissionCarrier) Slug() string                       { return "twilio" }
func (c *blockingAdmissionCarrier) Hangup(*sdk.AppCtx, *callRow) error { return nil }
func (c *blockingAdmissionCarrier) Place(*sdk.AppCtx, carrierPlaceRequest) (*carrierPlaceResult, error) {
	c.places.Add(1)
	close(c.started)
	<-c.release
	return &carrierPlaceResult{CarrierSID: "already-admitted"}, nil
}
func TestRuntimeDisableSerializesWithPlacementAdmission(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, multiCarrierPlatform())
	carrier := &blockingAdmissionCarrier{started: make(chan struct{}), release: make(chan struct{})}
	row := callRow{ID: "admission-race", ThreadID: "human-admission-race", Direction: "outbound", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 11, Status: "initiated", FromNumber: "+33189313432", ToNumber: "+33189999900", PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	placed := make(chan error, 1)
	go func() { placed <- a.placeOutboundLeg(ctx, carrier, &row, 30, 3600, nil) }()
	select {
	case <-carrier.started:
	case <-time.After(time.Second):
		t.Fatal("placement not started")
	}
	disabled := make(chan error, 1)
	go func() {
		_, err := a.outboundPolicy(ctx, map[string]any{"scope": "connection", "value": "11", "enabled": false})
		disabled <- err
	}()
	select {
	case err := <-disabled:
		t.Fatal("disable acknowledged before concurrent admission settled", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(carrier.release)
	if err := <-placed; err != nil {
		t.Fatal(err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	denied := row
	denied.ID = "after-disable"
	denied.ThreadID = "human-after-disable"
	denied.CarrierSID = ""
	unwound := false
	if err := a.placeOutboundLeg(ctx, carrier, &denied, 30, 3600, func() { unwound = true }); !errors.Is(err, errOutboundDisabled) {
		t.Fatalf("late placement accepted: %v", err)
	}
	if carrier.places.Load() != 1 || !unwound {
		t.Fatal("late placement issued a carrier command or leaked preparation")
	}
	if stored, _ := a.db().findCall(denied.ID); stored != nil {
		t.Fatal("denied placement created a call")
	}
}
func TestRuntimeDisableDoesNotBreakExternalRoutingForExistingInbound(t *testing.T) {
	a, _, _, route, row, _ := reliabilityFixture(t)
	if _, err := a.db().db.Exec(`INSERT INTO carrier_binding_drains VALUES(9,'draining',0)`); err != nil {
		t.Fatal(err)
	}
	child := callRow{ID: "external-leg", ProjectID: row.ProjectID, CarrierSlug: "telnyx", CarrierConnectionID: 9, FromNumber: route.PhoneNumber, IngressPath: "ring_group"}
	if _, err := a.db().db.Exec(`INSERT INTO call_legs(id,call_id,project_id,destination_id,provider,direction,kind,status,started_at) VALUES(?,?,?,'destination','telnyx','outbound','pstn','placing',?)`, child.ID, row.ID, row.ProjectID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := a.checkOutboundPlacement(&child); err != nil {
		t.Fatal("existing routing interrupted", err)
	}
	forged := child
	forged.ID = "unlinked-leg"
	if !errors.Is(a.checkOutboundPlacement(&forged), errOutboundDisabled) {
		t.Fatal("unlinked external call bypassed admission")
	}
}
func TestRuntimeDrainRetainsPendingEffectsAndRecordings(t *testing.T) {
	a, _, _, _, row, _ := reliabilityFixture(t)
	_, err := a.db().db.Exec(`UPDATE calls SET status='completed',ended_at=?,media_active=0 WHERE id=?`, time.Now().UTC().Add(-time.Minute*2).Format(time.RFC3339), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err = a.db().db.Exec(`INSERT INTO routing_effects(id,call_id,project_id,node_id,plan_json,status,next_attempt_at,updated_at) VALUES('effect',?,?,'node','{}','pending',?,?)`, row.ID, row.ProjectID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db().db.Exec(`INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,storage_status,created_at) VALUES('recording',?,?,'telnyx',9,'recording-provider','importing',?)`, row.ID, row.ProjectID, now); err != nil {
		t.Fatal(err)
	}
	req := runtimeBindingRequest{Phase: "prepare", ChangeID: "pending-work", Previous: map[string]any{"carrier": float64(9)}, Desired: map[string]any{"carrier": nil}}
	result, err := a.applyRuntimeBindingRequest(req)
	if err != nil || result["ready"] != false || result["retained_work"] != 2 {
		t.Fatalf("lost asynchronous carrier work: %+v %v", result, err)
	}
	// A replacement App object reads the same durable prepared state.
	replacement := &App{}
	result, err = replacement.applyRuntimeBindingRequest(req)
	if err != nil || result["ready"] != false {
		t.Fatalf("restart lost drain: %+v %v", result, err)
	}
	a.db().db.Exec(`UPDATE routing_effects SET status='done' WHERE id='effect'`)
	a.db().db.Exec(`UPDATE recordings SET storage_status='stored' WHERE id='recording'`)
	req.Phase = "commit"
	result, err = replacement.applyRuntimeBindingRequest(req)
	if err != nil || result["ready"] != true {
		t.Fatalf("drained work did not complete: %+v %v", result, err)
	}
}

func TestRuntimeDisabledTelnyxIngressRejectsOnlyNewCarrierSessions(t *testing.T) {
	a, ctx, p, route, row, key := reliabilityFixture(t)
	if _, err := a.disableInboundRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	fresh := *row
	fresh.CarrierSID = "new-disabled-session"
	response := reliabilityEvent(t, a, route, &fresh, key, "call.initiated")
	if response.Code != 204 || len(p.integrationCalls) != 1 || p.integrationCalls[0].Tool != "reject_call" || p.integrationCalls[0].Input["call_control_id"] != fresh.CarrierSID || p.integrationCalls[0].Input["cause"] != "CALL_REJECTED" {
		t.Fatalf("disabled ingress did not explicitly reject: %d %+v", response.Code, p.integrationCalls)
	}
	if call, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, fresh.CarrierSID); err != nil || call != nil {
		t.Fatal("disabled call entered history or adviser projection", err)
	}
	current, _ := a.db().findCall(row.ID)
	if current.Status != row.Status {
		t.Fatal("existing call interrupted")
	}
	if _, err := a.setInboundRouteEnabled(route, true); err != nil {
		t.Fatal(err)
	}
	if err := a.checkOutboundAdmission(row.ProjectID, row.CarrierSlug, row.CarrierConnectionID, route.PhoneNumber); err != nil {
		t.Fatal("inbound control altered outbound", err)
	}
}

func TestRuntimeInboundControlLeavesOutboundAndOtherRoutesUsable(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, multiCarrierPlatform())
	route := routeRow{ID: "pause-inbound", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 11, PhoneNumber: "+33189313432", Enabled: true, Secret: "route-secret", AgentID: 1, AnswerMode: answerModeHumanBrowser, InboundTransport: inboundTransportProgrammable}
	other := route
	other.ID = "other-route"
	other.PhoneNumber = "+33189313499"
	for _, r := range []routeRow{route, other} {
		if err := a.db().insertRoute(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.disableInboundRoute(ctx, &route); err != nil {
		t.Fatal(err)
	}
	if err := a.checkInboundAdmission(&other); err != nil {
		t.Fatal("unrelated route disabled", err)
	}
	if _, err := a.placeHumanCall(ctx, "project-a", "+33189999903", route.PhoneNumber, 30, nil); err != nil {
		t.Fatal("inbound toggle prevented outbound", err)
	}
	if _, err := a.setInboundRouteEnabled(&route, true); err != nil {
		t.Fatal(err)
	}
	stored, err := a.db().findRoute(route.ID)
	if err != nil || !stored.Enabled {
		t.Fatal("could not re-enable", err)
	}
}
