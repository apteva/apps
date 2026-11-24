package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// transferTestRequest is phoneTestRequest with the call.transfer action.
func transferTestRequest(app *App, identity *phoneIdentity, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("X-Apteva-Project-ID", "project-a")
	if identity != nil {
		r.Header.Set("X-Apteva-Issuer-App", identity.IssuerApp)
		r.Header.Set("X-Apteva-Issuer-Install-ID", identity.IssuerInstallID)
		r.Header.Set("X-Apteva-Subject-Type", identity.SubjectType)
		r.Header.Set("X-Apteva-Subject-ID", identity.SubjectID)
		r.Header.Set("X-Apteva-Organization-ID", identity.OrganizationID)
		r.Header.Set("X-Apteva-Scopes", `[{"type":"app_user","app":"telephony","actions":["call.read","call.answer","call.attach","call.hangup","call.hold","call.transfer"]}]`)
	}
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// transferTestPolicy is phoneTestPolicy where the sales team may transfer to
// support and to the "team" ring group (sales + support).
func transferTestPolicy(t *testing.T, app *App) {
	t.Helper()
	p := phoneTestPolicy(t, app)
	if _, err := app.saveRingGroup("project-a", "team", "Team", "sequential", 20, []ringGroupMemberRow{
		{DestinationID: "sales", Position: 0, Enabled: true, TimeoutSec: 20},
		{DestinationID: "support", Position: 1, Enabled: true, TimeoutSec: 20},
	}); err != nil {
		t.Fatal(err)
	}
	for i := range p.Groups {
		if p.Groups[i].ID == "sales-team" {
			p.Groups[i].TransferTargets = []string{"support", "team"}
		}
	}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", p); w.Code != 200 {
		t.Fatalf("policy: %d %s", w.Code, w.Body)
	}
}

// transferTestOwnedCall is an in-progress call that alice answered.
func transferTestOwnedCall(t *testing.T, app *App, id string) callRow {
	t.Helper()
	row := phoneTestCall(t, app, id, "pending")
	alice := phoneTestIdentity("alice")
	if w := transferTestRequest(app, &alice, "POST", "/softphone/answer/"+id, map[string]any{}); w.Code != 200 {
		t.Fatalf("alice answer: %d %s", w.Code, w.Body)
	}
	if _, err := app.db().db.Exec(`UPDATE calls SET status='in-progress' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	row.Status = "in-progress"
	return row
}

func transferTestCallRead(t *testing.T, app *App, identity *phoneIdentity, id string) (map[string]any, int) {
	t.Helper()
	w := transferTestRequest(app, identity, "GET", "/calls?call_id="+id, nil)
	if w.Code != 200 {
		return nil, w.Code
	}
	var response struct {
		Calls []map[string]any `json:"calls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Calls) != 1 {
		t.Fatalf("call read: %s %v", w.Body, err)
	}
	return response.Calls[0], 200
}

func transferTestTopics(t *testing.T, app *App, id string) []string {
	t.Helper()
	rows, err := app.db().db.Query(`SELECT topic FROM call_events WHERE call_id=? AND topic LIKE 'call.transfer_%' ORDER BY revision`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var topics []string
	for rows.Next() {
		var topic string
		_ = rows.Scan(&topic)
		topics = append(topics, topic)
	}
	return topics
}

func transferTestStatus(t *testing.T, app *App, id string) (status, reason string) {
	t.Helper()
	if err := app.db().db.QueryRow(`SELECT status,reason FROM call_transfers WHERE call_id=? ORDER BY started_at DESC LIMIT 1`, id).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	return status, reason
}

func TestCallTransferAdviserToColleague(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	transferTestPolicy(t, app)
	alice, bob, eve := phoneTestIdentity("alice"), phoneTestIdentity("bob"), phoneTestIdentity("eve")
	row := transferTestOwnedCall(t, app, "transfer-call")
	answered, _ := app.db().findCall(row.ID)
	aliceGrant := ""
	_ = app.db().db.QueryRow(`SELECT token_hash FROM telephony_media_sessions WHERE call_id=?`, row.ID).Scan(&aliceGrant)

	w := transferTestRequest(app, &alice, "GET", "/softphone/transfer-targets", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"support"`) || !strings.Contains(w.Body.String(), `"team"`) || strings.Contains(w.Body.String(), `"id":"sales"`) {
		t.Fatalf("alice targets: %d %s", w.Code, w.Body)
	}
	if w := transferTestRequest(app, &eve, "GET", "/softphone/transfer-targets", nil); w.Code != 200 || strings.Contains(w.Body.String(), `"support"`) {
		t.Fatalf("eve has no transfer grant: %d %s", w.Code, w.Body)
	}
	// Without the call.transfer scope the routes are refused.
	if w := phoneTestRequest(app, &alice, "POST", "/softphone/transfer/"+row.ID, map[string]any{"target_id": "support"}); w.Code != 403 {
		t.Fatalf("transfer without scope: %d", w.Code)
	}
	if w := transferTestRequest(app, &alice, "POST", "/softphone/transfer/"+row.ID, map[string]any{"target_id": "sales"}); w.Code != 403 || !strings.Contains(w.Body.String(), "target_not_allowed") {
		t.Fatalf("ungranted target: %d %s", w.Code, w.Body)
	}
	if w := transferTestRequest(app, &bob, "POST", "/softphone/transfer/"+row.ID, map[string]any{"target_id": "support"}); w.Code != 403 || !strings.Contains(w.Body.String(), "not_call_owner") {
		t.Fatalf("non-owner transfer: %d %s", w.Code, w.Body)
	}
	w = transferTestRequest(app, &alice, "POST", "/softphone/transfer/"+row.ID, map[string]any{"target_id": "support", "note": "Wants a refund"})
	if w.Code != 200 {
		t.Fatalf("transfer: %d %s", w.Code, w.Body)
	}
	if w := transferTestRequest(app, &alice, "POST", "/softphone/transfer/"+row.ID, map[string]any{"target_id": "support"}); w.Code != 409 || !strings.Contains(w.Body.String(), "transfer_in_progress") {
		t.Fatalf("second transfer: %d %s", w.Code, w.Body)
	}
	if held, _ := app.db().findCall(row.ID); held.HoldState != "held" {
		t.Fatalf("caller not held: %q", held.HoldState)
	}

	// Eve sees the connected call offered to her; alice sees it ringing but
	// cannot answer it; bob does not see it.
	call, code := transferTestCallRead(t, app, &eve, row.ID)
	if code != 200 || call["answerable"] != true || call["transfer"] == nil || !strings.Contains(transferJSON(call["ring_offers"]), `"support"`) {
		t.Fatalf("eve read: %d %v", code, call)
	}
	if transfer := call["transfer"].(map[string]any); transfer["note"] != "Wants a refund" || transfer["source"] != "adviser" {
		t.Fatalf("transfer detail: %v", transfer)
	}
	if w := transferTestRequest(app, &eve, "GET", "/calls", nil); w.Code != 200 || !strings.Contains(w.Body.String(), row.ID) {
		t.Fatalf("eve list: %d %s", w.Code, w.Body)
	}
	if call, code := transferTestCallRead(t, app, &alice, row.ID); code != 200 || call["answerable"] != false || call["transfer"] == nil {
		t.Fatalf("alice read: %d %v", code, call)
	}
	if _, code := transferTestCallRead(t, app, &bob, row.ID); code != 404 {
		t.Fatalf("bob sees the call: %d", code)
	}
	if w := transferTestRequest(app, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{}); w.Code < 400 {
		t.Fatalf("alice answered her own transfer: %d", w.Code)
	}

	w = transferTestRequest(app, &eve, "POST", "/softphone/answer/"+row.ID, map[string]any{})
	if w.Code != 200 {
		t.Fatalf("eve answer: %d %s", w.Code, w.Body)
	}
	var session softphoneSession
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	owner, dest, _ := app.phoneOwner(row.ID)
	if owner != eve.key() || dest != "support" {
		t.Fatalf("owner = %q %q", owner, dest)
	}
	fresh, _ := app.db().findCall(row.ID)
	if fresh.HoldState != "active" || fresh.RoutingDestinationID != "support" || fresh.PeerToken != answered.PeerToken {
		t.Fatalf("call after answer: hold=%q dest=%q", fresh.HoldState, fresh.RoutingDestinationID)
	}
	if !app.validPhoneMedia(fresh, session.SessionToken) {
		t.Fatal("eve's media grant rejected")
	}
	var grant string
	_ = app.db().db.QueryRow(`SELECT token_hash FROM telephony_media_sessions WHERE call_id=?`, row.ID).Scan(&grant)
	if grant == "" || grant == aliceGrant {
		t.Fatal("alice's media grant was not replaced")
	}
	if w := transferTestRequest(app, &alice, "POST", "/softphone/attach/"+row.ID, nil); w.Code < 400 {
		t.Fatalf("alice reattached after transfer: %d", w.Code)
	}
	if status, _ := transferTestStatus(t, app, row.ID); status != transferAnswered {
		t.Fatalf("transfer status %q", status)
	}
	if got := strings.Join(transferTestTopics(t, app, row.ID), ","); got != "call.transfer_started,call.transfer_answered" {
		t.Fatalf("events: %s", got)
	}
	if app.transfers.get(row.ID) != nil {
		t.Fatal("answered transfer still registered")
	}
	if call, _ := transferTestCallRead(t, app, &eve, row.ID); call["transfer"] != nil {
		t.Fatalf("answered call still shows a transfer: %v", call["transfer"])
	}
}

func TestCallTransferNoAnswerCancelAndAbandon(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	transferTestPolicy(t, app)
	alice, eve := phoneTestIdentity("alice"), phoneTestIdentity("eve")
	row := transferTestOwnedCall(t, app, "transfer-return")
	expire := func() {
		t.Helper()
		tr := app.transfers.get(row.ID)
		if tr == nil {
			t.Fatal("transfer not registered")
		}
		tr.ExpiresAt = time.Now().Add(-time.Second)
		if err := app.runCallTransfers(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
	}

	if w := transferTestRequest(app, &alice, "POST", "/softphone/transfer/"+row.ID, map[string]any{"target_id": "support"}); w.Code != 200 {
		t.Fatalf("transfer: %d %s", w.Code, w.Body)
	}
	expire()
	if status, _ := transferTestStatus(t, app, row.ID); status != transferNoAnswer {
		t.Fatalf("status %q", status)
	}
	fresh, _ := app.db().findCall(row.ID)
	owner, _, _ := app.phoneOwner(row.ID)
	if fresh.HoldState != "active" || owner != alice.key() {
		t.Fatalf("call did not return to alice: hold=%q owner=%q", fresh.HoldState, owner)
	}
	if w := transferTestRequest(app, &eve, "POST", "/softphone/answer/"+row.ID, map[string]any{}); w.Code < 400 {
		t.Fatalf("expired transfer answered: %d", w.Code)
	}

	if w := transferTestRequest(app, &alice, "POST", "/softphone/transfer/"+row.ID, map[string]any{"target_id": "support"}); w.Code != 200 {
		t.Fatalf("second transfer: %d %s", w.Code, w.Body)
	}
	if w := transferTestRequest(app, &eve, "POST", "/softphone/transfer-cancel/"+row.ID, nil); w.Code != 403 {
		t.Fatalf("colleague canceled: %d %s", w.Code, w.Body)
	}
	if w := transferTestRequest(app, &alice, "POST", "/softphone/transfer-cancel/"+row.ID, nil); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if w := transferTestRequest(app, &alice, "POST", "/softphone/transfer-cancel/"+row.ID, nil); w.Code != 404 {
		t.Fatalf("second cancel: %d", w.Code)
	}
	if status, _ := transferTestStatus(t, app, row.ID); status != transferCanceled {
		t.Fatalf("status %q", status)
	}

	if w := transferTestRequest(app, &alice, "POST", "/softphone/transfer/"+row.ID, map[string]any{"target_id": "team"}); w.Code != 200 {
		t.Fatalf("ring group transfer: %d %s", w.Code, w.Body)
	}
	// The ring group rings every member except the adviser who transferred.
	bob := phoneTestIdentity("bob")
	if call, code := transferTestCallRead(t, app, &eve, row.ID); code != 200 || call["answerable"] != true {
		t.Fatalf("eve not offered the group transfer: %d %v", code, call)
	}
	if _, err := app.db().db.Exec(`UPDATE calls SET status='completed' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runCallTransfers(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if status, _ := transferTestStatus(t, app, row.ID); status != transferAbandoned {
		t.Fatalf("status %q", status)
	}
	_ = bob
	want := "call.transfer_started,call.transfer_no_answer,call.transfer_started,call.transfer_canceled,call.transfer_started,call.transfer_abandoned"
	if got := strings.Join(transferTestTopics(t, app, row.ID), ","); got != want {
		t.Fatalf("events: %s", got)
	}
}

func TestCallTransferFromAIHandsCallToAdviser(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	transferTestPolicy(t, app)
	eve := phoneTestIdentity("eve")
	ai := callRow{ID: "ai-call", ThreadID: "thread-7", Direction: "inbound", CarrierSlug: "telnyx", Status: "in-progress", MediaStatus: "connected",
		ProjectID: "project-a", PeerKind: peerKindRealtime, AgentID: 7, PeerToken: "core-token", CallbackSecret: "cb-ai",
		AudioBridgeURL: "ws://core.invalid/audio", ToNumber: "+13502231050", FromNumber: "+13334445555", PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := app.db().insertCall(ai); err != nil {
		t.Fatal(err)
	}
	request := transferRequest{CallID: ai.ID, ProjectID: "project-a", Source: transferSourceAI, TargetID: "support", AgentID: 8}
	var refused *transferError
	if _, err := app.startTransfer(ctx, request); !errors.As(err, &refused) || refused.Code != "not_call_agent" {
		t.Fatalf("other agent transferred: %v", err)
	}
	twilio := ai
	twilio.ID, twilio.ThreadID, twilio.CarrierSlug = "ai-twilio", "thread-twilio", "twilio"
	if err := app.db().insertCall(twilio); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db().db.Exec(`UPDATE calls SET media_status='connected' WHERE id IN (?,?)`, ai.ID, twilio.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.startTransfer(ctx, transferRequest{CallID: twilio.ID, ProjectID: "project-a", Source: transferSourceAI, TargetID: "support", AgentID: 7}); !errors.As(err, &refused) || refused.Code != "transfer_unsupported" {
		t.Fatalf("unsupported carrier: %v", err)
	}

	request.AgentID = 7
	tr, err := app.startTransfer(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, parked := app.parkedCalls.Load(ai.ID); !parked {
		t.Fatal("AI not parked")
	}
	if call, code := transferTestCallRead(t, app, &eve, ai.ID); code != 200 || call["answerable"] != true {
		t.Fatalf("eve not offered the AI call: %d %v", code, call)
	}
	w := transferTestRequest(app, &eve, "POST", "/softphone/answer/"+ai.ID, map[string]any{})
	if w.Code != 200 {
		t.Fatalf("eve answer: %d %s", w.Code, w.Body)
	}
	select {
	case <-tr.done:
	default:
		t.Fatal("waiting tool not woken")
	}
	fresh, _ := app.db().findCall(ai.ID)
	if fresh.PeerKind != peerKindHuman || fresh.PeerToken == ai.PeerToken || fresh.AudioBridgeURL != app.peerLoopbackURL(&ai) || fresh.ThreadID != "human-"+ai.ID || fresh.HoldState != "active" {
		t.Fatalf("call not handed to the softphone: %+v", fresh)
	}
	if owner, dest, _ := app.phoneOwner(ai.ID); owner != eve.key() || dest != "support" {
		t.Fatalf("owner %q %q", owner, dest)
	}
	if _, message := app.hangupCall(ctx, ai.ID, 7, "project-a", hangupActor{Initiator: terminationByAgent}); !strings.Contains(message, "transferred") {
		t.Fatalf("AI hung up the adviser's call: %q", message)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, parked := app.parkedCalls.Load(ai.ID); !parked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("AI still parked after the handover")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCallTransferToolReturnsWhenNobodyAnswers(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	transferTestPolicy(t, app)
	ai := callRow{ID: "ai-return", ThreadID: "thread-7", Direction: "inbound", CarrierSlug: "telnyx", Status: "in-progress", MediaStatus: "connected",
		ProjectID: "project-a", PeerKind: peerKindRealtime, AgentID: 7, PeerToken: "core-token", CallbackSecret: "cb-ai",
		ToNumber: "+13502231050", FromNumber: "+13334445555", PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := app.db().insertCall(ai); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db().db.Exec(`UPDATE calls SET media_status='connected' WHERE id=?`, ai.ID); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result any
		err    error
	}
	done := make(chan outcome, 1)
	caller := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7})
	go func() {
		result, err := app.toolTransferCall(caller, ctx, map[string]any{"call_id": ai.ID, "target_id": "team", "note": "Billing question"})
		done <- outcome{result, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	var tr *callTransfer
	for tr == nil {
		select {
		case got := <-done:
			t.Fatalf("tool returned before the transfer rang: %v %v", got.result, got.err)
		default:
		}
		if tr = app.transfers.get(ai.ID); tr == nil && time.Now().After(deadline) {
			t.Fatal("transfer never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	tr.ExpiresAt = time.Now().Add(-time.Second)
	if err := app.runCallTransfers(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || !strings.Contains(transferJSON(got.result), "Nobody answered") {
		t.Fatalf("tool result: %v %v", got.result, got.err)
	}
	if _, parked := app.parkedCalls.Load(ai.ID); parked {
		t.Fatal("AI still parked after no answer")
	}
	if fresh, _ := app.db().findCall(ai.ID); fresh.PeerKind != peerKindRealtime || fresh.HoldState != "active" {
		t.Fatalf("call did not return to the AI: %+v", fresh)
	}
	if _, err := app.toolTransferCall(caller, ctx, map[string]any{"call_id": ai.ID, "target_id": "team", "if_no_answer": "voicemail"}); err == nil {
		t.Fatal("invalid if_no_answer accepted")
	}
}

func TestCallTransferAITargetsFollowConfig(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	transferTestPolicy(t, app)
	caller := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7})
	result, err := app.toolTransferTargets(caller, ctx, nil)
	if err != nil || !strings.Contains(transferJSON(result), `"support"`) || !strings.Contains(transferJSON(result), `"team"`) {
		t.Fatalf("default targets: %v %v", result, err)
	}
	allowed := aiTransferAllowed(nil, "project-a")
	if !allowed("anything") {
		t.Fatal("empty config should allow every target")
	}
}

func TestCallTransferRestartEndsRingingTransfers(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	transferTestPolicy(t, app)
	row := transferTestOwnedCall(t, app, "transfer-restart")
	now := time.Now().UTC()
	if _, err := app.db().db.Exec(`UPDATE calls SET hold_state='held' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db().db.Exec(`INSERT INTO call_transfers(id,call_id,project_id,source,status,target_kind,target_id,target_name,destinations_json,from_principal,started_at,expires_at) VALUES('tr-1',?,?,'adviser','ringing','destination','support','support','["support"]',?,?,?)`,
		row.ID, row.ProjectID, phoneTestIdentity("alice").key(), ringTime(now), ringTime(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := app.runCallTransfers(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	status, reason := transferTestStatus(t, app, row.ID)
	if status != transferFailed || !strings.Contains(reason, "restarted") {
		t.Fatalf("restored transfer: %q %q", status, reason)
	}
	if fresh, _ := app.db().findCall(row.ID); fresh.HoldState != "active" {
		t.Fatalf("caller left on hold: %q", fresh.HoldState)
	}
	if app.transfers.mayRing() {
		t.Fatal("registry should know nothing rings")
	}
}

func TestCallTransferGrantValidationAndActions(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	p := phoneTestPolicy(t, app)
	p.Groups[0].TransferTargets = []string{"missing"}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", p); w.Code < 400 {
		t.Fatalf("unknown transfer target accepted: %d", w.Code)
	}
	for path, method := range map[string]string{"/softphone/transfer/x": "POST", "/softphone/transfer-cancel/x": "POST", "/softphone/transfer-targets": "GET"} {
		r := httptest.NewRequest(method, path, nil)
		if got := phoneAction(r); got != "call.transfer" {
			t.Fatalf("%s %s = %q", method, path, got)
		}
	}
}

func transferJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
