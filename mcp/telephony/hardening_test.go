package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

type answerPlatform struct {
	tk.BasePlatformClient
	failCarrier         bool
	spawned             []sdk.RealtimeSpawnRequest
	killed              []string
	integrationCalls    []integrationCall
	integrationResponse map[string]json.RawMessage
	credentials         *sdk.ConnectionCredentials
}

type integrationCall struct {
	Tool  string
	Input map[string]any
}

func (p *answerPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{InstallID: 42, PublicURL: "https://example.test"}, nil
}

func (p *answerPlatform) SpawnRealtimeThread(req sdk.RealtimeSpawnRequest) (*sdk.RealtimeSpawnResult, error) {
	p.spawned = append(p.spawned, req)
	return &sdk.RealtimeSpawnResult{
		Status: "created", ThreadID: req.ThreadID, AudioBridgeURL: "wss://bridge.test/audio?token=test",
	}, nil
}

func (p *answerPlatform) KillThread(_ int64, threadID string) error {
	p.killed = append(p.killed, threadID)
	return nil
}

func (p *answerPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	if p.credentials != nil {
		copy := *p.credentials
		copy.ConnectionID = id
		return &copy, nil
	}
	return &sdk.ConnectionCredentials{ConnectionID: id, Slug: "twilio", Fields: map[string]string{"auth_token": "test-auth-token"}}, nil
}

func (p *answerPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.integrationCalls = append(p.integrationCalls, integrationCall{Tool: tool, Input: input})
	if p.failCarrier {
		return &sdk.ExecuteResult{Success: false, Status: 409, Data: json.RawMessage(`{"message":"call ended"}`)}, nil
	}
	data := json.RawMessage(`{}`)
	if response := p.integrationResponse[tool]; response != nil {
		data = response
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
}

func signTwilioTestRequest(t *testing.T, a *App, req *http.Request, form url.Values) {
	t.Helper()
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", twilioTestSignature(a.publicRequestURL(req), form, "test-auth-token"))
}

func twilioTestSignature(fullURL string, form url.Values, token string) string {
	keys := make([]string, 0, len(form))
	for key := range form {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var signed strings.Builder
	signed.WriteString(fullURL)
	for _, key := range keys {
		signed.WriteString(key)
		signed.WriteString(form.Get(key))
	}
	mac := hmac.New(sha1.New, []byte(token))
	_, _ = mac.Write([]byte(signed.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func testCallsDB(t *testing.T) *callsDB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+url.QueryEscape(t.Name())+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		migration, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
	}
	return &callsDB{db: db}
}

func applyMigrationFile(t *testing.T, db *sql.DB, file string) {
	t.Helper()
	migration, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply %s: %v", file, err)
	}
}

func testCall(id, status string) callRow {
	now := time.Now().UTC()
	return callRow{
		ID: id, ThreadID: "thread-" + id, Direction: "outbound", AgentID: 7,
		CarrierSID: "CA" + id, CarrierSlug: "twilio", CarrierConnectionID: 9,
		CallbackSecret: "callback-secret", ToNumber: "+14155550100", FromNumber: "+14155550101",
		Directive: "test", Voice: "alloy", AudioBridgeURL: "wss://core.test/audio?token=secret",
		Status: status, PlacedAt: now.Format(time.RFC3339), ProjectID: "project-a",
		StateExpiresAt: now.Add(time.Minute).Format(time.RFC3339), DeadlineAt: now.Add(time.Hour).Format(time.RFC3339),
	}
}

func TestManifestSeparatesPublicCarrierRoutes(t *testing.T) {
	routes := (&App{}).HTTPRoutes()
	public := map[string]bool{}
	for _, route := range routes {
		public[route.Pattern] = route.NoAuth
	}
	for _, pattern := range []string{"/media/twilio/", "/media/telnyx/", "/media/plivo/", "/xml/plivo/", "/webhook/status/", "/webhook/stream/twilio/", "/webhook/recording/twilio/", "/webhook/recording/plivo/", "/inbound/twilio/", "/inbound/telnyx/", "/inbound/plivo/"} {
		if !public[pattern] {
			t.Fatalf("carrier route %s must be public at the SDK layer", pattern)
		}
	}
	if public["/calls"] || public["/calls/"] || public["/recordings/"] || public["/recording-settings"] || public["/numbers/"] {
		t.Fatal("panel routes must retain app-token authentication")
	}
	manifest := (&App{}).Manifest()
	for _, route := range manifest.Provides.HTTPRoutes {
		if route.Prefix == "/" {
			t.Fatal("root route must not be anonymous")
		}
	}
}

func TestVerifyTelnyxSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"data":{"event_type":"call.initiated"}}`)
	signature := ed25519.Sign(privateKey, append([]byte(timestamp+"|"), body...))
	publicKeyText := base64.StdEncoding.EncodeToString(publicKey)
	signatureText := base64.StdEncoding.EncodeToString(signature)
	if err := verifyTelnyxSignature(publicKeyText, timestamp, signatureText, body, now); err != nil {
		t.Fatalf("valid Telnyx signature rejected: %v", err)
	}
	if err := verifyTelnyxSignature(publicKeyText, timestamp, signatureText, []byte(`{"tampered":true}`), now); err == nil {
		t.Fatal("tampered Telnyx webhook accepted")
	}
	if err := verifyTelnyxSignature(publicKeyText, timestamp, signatureText, body, now.Add(6*time.Minute)); err == nil {
		t.Fatal("stale Telnyx webhook accepted")
	}
}

func TestHardeningMigrationUpgradesPopulatedDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", "file:upgrade?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, file := range []string{"migrations/001_init.sql", "migrations/002_carrier_metadata.sql", "migrations/003_inbound_routes.sql"} {
		applyMigrationFile(t, db, file)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range []string{"old-a", "old-b"} {
		_, err := db.Exec(`INSERT INTO calls
            (id, thread_id, direction, agent_id, route_id, carrier_sid, carrier_slug,
             carrier_connection_id, to_number, from_number, directive, voice,
             audio_bridge_url, status, placed_at, project_id)
            VALUES (?, ?, 'inbound', 7, 'route', 'CA-duplicate', 'twilio', 9,
                    '+14155550101', '+14155550100', 'pending', '', 'pending', 'pending', ?, 'project-a')`,
			id, "thread-"+id, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"route-old-a", "route-old-b"} {
		_, err := db.Exec(`INSERT INTO inbound_routes
            (id, project_id, carrier_slug, carrier_connection_id, phone_number,
             agent_id, enabled, timeout_sec, secret, created_at, updated_at)
            VALUES (?, 'project-a', 'twilio', 9, '+14155550101', 7, 1, 60, ?, ?, ?)`,
			id, "secret-"+id, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	applyMigrationFile(t, db, "migrations/004_hardening.sql")
	var carrierIDs, enabledRoutes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM calls WHERE carrier_sid = 'CA-duplicate'`).Scan(&carrierIDs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbound_routes WHERE enabled = 1`).Scan(&enabledRoutes); err != nil {
		t.Fatal(err)
	}
	if carrierIDs != 1 || enabledRoutes != 1 {
		t.Fatalf("migration did not deduplicate: carrier IDs=%d enabled routes=%d", carrierIDs, enabledRoutes)
	}
}

func TestTwilioSignatureVerification(t *testing.T) {
	fullURL := "https://example.test/api/apps/telephony/inbound/twilio/route?secret=s"
	form := url.Values{"CallSid": {"CA123"}, "From": {"+14155550100"}, "To": {"+14155550101"}}
	keys := []string{"CallSid", "From", "To"}
	var signed strings.Builder
	signed.WriteString(fullURL)
	for _, key := range keys {
		signed.WriteString(key)
		signed.WriteString(form.Get(key))
	}
	mac := hmac.New(sha1.New, []byte("auth-token"))
	_, _ = mac.Write([]byte(signed.String()))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !verifyTwilioSignature(fullURL, form, "auth-token", signature) {
		t.Fatal("valid signature rejected")
	}
	if verifyTwilioSignature(fullURL, form, "wrong", signature) {
		t.Fatal("signature accepted with wrong token")
	}
}

func TestStateTransitionsAreAtomicAndTerminal(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("state", "initiated")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	if err := db.updateStatus(call.ID, "in-progress", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.updateStatus(call.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.updateStatus(call.ID, "ringing", "late callback"); err != nil {
		t.Fatal(err)
	}
	got, err := db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.EndedAt == "" {
		t.Fatalf("terminal call reopened: status=%s ended=%q", got.Status, got.EndedAt)
	}
}

func TestPendingCallCanOnlyBeClaimedOnce(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("pending", "pending")
	call.Direction = "inbound"
	call.RouteID = "route"
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.claimPendingCall(call.ID, call.AgentID, call.ProjectID)
	if err != nil || !claimed {
		t.Fatalf("first claim failed: claimed=%t err=%v", claimed, err)
	}
	claimed, err = db.claimPendingCall(call.ID, call.AgentID, call.ProjectID)
	if err != nil || claimed {
		t.Fatalf("second claim succeeded: claimed=%t err=%v", claimed, err)
	}
}

func TestImmediateAnswerSpawnsRealtimeThreadAndAnswersCarrier(t *testing.T) {
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	a := &App{installID: 42}

	route := routeRow{
		ID: "route-immediate", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", AgentID: 7, Enabled: true, TimeoutSec: 60,
		AnswerMode: answerModeRealtimeImmediate, AutoDirective: "Help the caller.", AutoVoice: "marin",
		AutoGreeting: "Hello. How can I help?",
	}
	call := testCall("immediate", "pending")
	call.Direction = "inbound"
	call.RouteID = route.ID
	call.ThreadID = "pending-" + call.ID
	call.AudioBridgeURL = "pending"
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	if err := a.answerImmediateCall(ctx, &route, call.ID); err != nil {
		t.Fatalf("answer immediate call: %v", err)
	}
	if len(platform.spawned) != 1 {
		t.Fatalf("spawn count=%d, want 1", len(platform.spawned))
	}
	spawn := platform.spawned[0]
	if spawn.AgentID != route.AgentID || spawn.Directive != route.AutoDirective || spawn.Voice != route.AutoVoice || spawn.InitialMessage != route.AutoGreeting {
		t.Fatalf("unexpected realtime spawn: %+v", spawn)
	}
	stored, err := a.db().findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "answered" || stored.ThreadID != "tel-"+call.ID || stored.AudioBridgeURL == "pending" {
		t.Fatalf("call was not attached and answered: %+v", stored)
	}
}

func TestAnswerCallIsIdempotentAfterAnswer(t *testing.T) {
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	a := &App{installID: 42}
	call := testCall("already-answered", "answered")
	call.Direction = "inbound"
	call.ThreadID = "tel-" + call.ID
	threadID, err := a.answerCall(ctx, &call, "Help.", "marin", "Hello.", false)
	if err != nil || threadID != call.ThreadID {
		t.Fatalf("thread=%q err=%v", threadID, err)
	}
	if len(platform.integrationCalls) != 0 || len(platform.spawned) != 0 {
		t.Fatalf("idempotent answer performed work: calls=%v spawned=%v", platform.integrationCalls, platform.spawned)
	}
}

func TestTwilioImmediateInboundReturnsStreamInInitialResponse(t *testing.T) {
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	a := &App{installID: 42}
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{
		ID: "route-direct", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", PhoneNumberSID: "PN1", AgentID: 7, Enabled: true,
		TimeoutSec: 60, AnswerMode: answerModeRealtimeImmediate, AutoDirective: "Help the caller.",
		AutoVoice: "marin", AutoGreeting: "Hello. How can I help?", Secret: "route-secret",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"CallSid": {"CAdirect"}, "From": {"+34648257793"}, "To": {route.PhoneNumber},
	}
	endpoint := strings.TrimPrefix(a.inboundRouteURL(route), a.publicAppURL())
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	signTwilioTestRequest(t, a, req, form)
	rec := httptest.NewRecorder()
	a.handleTwilioInbound(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<Connect><Stream`) || !strings.Contains(body, `statusCallback=`) ||
		strings.Contains(body, `<Pause`) || strings.Contains(body, `<Say`) {
		t.Fatalf("unexpected immediate TwiML: %s", body)
	}
	if len(platform.spawned) != 1 || len(platform.integrationCalls) != 0 {
		t.Fatalf("spawned=%d carrier API calls=%d", len(platform.spawned), len(platform.integrationCalls))
	}
	stored, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, "CAdirect")
	if err != nil || stored == nil || stored.Status != "answered" || stored.ThreadID != "tel-"+stored.ID {
		t.Fatalf("stored call=%+v err=%v", stored, err)
	}
}

func TestTwilioStreamErrorFailsCallAndKillsRealtimeThread(t *testing.T) {
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	a := &App{installID: 42}
	call := testCall("stream-error", "answered")
	call.Direction = "inbound"
	call.ThreadID = "tel-" + call.ID
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"CallSid": {call.CarrierSID}, "StreamEvent": {"stream-error"}, "StreamError": {"handshake rejected"},
	}
	fullURL := a.twilioStreamStatusURL(call.ID, call.CallbackSecret, call.ProjectID)
	endpoint := strings.TrimPrefix(fullURL, a.publicAppURL())
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	signTwilioTestRequest(t, a, req, form)
	rec := httptest.NewRecorder()
	a.handleTwilioStreamStatus(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := a.db().findCall(call.ID)
	if err != nil || stored.Status != "failed" || stored.ErrorMessage != "handshake rejected" {
		t.Fatalf("stored call=%+v err=%v", stored, err)
	}
	if len(platform.killed) != 1 || platform.killed[0] != call.ThreadID {
		t.Fatalf("killed threads=%v", platform.killed)
	}
}

func TestTwilioInboundStatusCallbackCompletesCall(t *testing.T) {
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	a := &App{installID: 42}
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{
		ID: "route-status", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", PhoneNumberSID: "PN1", AgentID: 7, Enabled: true,
		TimeoutSec: 60, Secret: "route-secret", CreatedAt: now, UpdatedAt: now,
	}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	call := testCall("route-status", "in-progress")
	call.Direction = "inbound"
	call.RouteID = route.ID
	call.ThreadID = "tel-" + call.ID
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"CallSid": {call.CarrierSID}, "CallStatus": {"completed"}, "To": {route.PhoneNumber}}
	fullURL := a.twilioRouteStatusURL(route)
	endpoint := strings.TrimPrefix(fullURL, a.publicAppURL())
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	signTwilioTestRequest(t, a, req, form)
	rec := httptest.NewRecorder()
	a.handleTwilioInbound(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := a.db().findCall(call.ID)
	if err != nil || stored.Status != "completed" || stored.EndedAt == "" {
		t.Fatalf("stored call=%+v err=%v", stored, err)
	}
	if len(platform.killed) != 1 || platform.killed[0] != call.ThreadID {
		t.Fatalf("killed threads=%v", platform.killed)
	}
}

func TestTwilioRouteConfigurationPreservesAndRestoresCallbacks(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"list_phone_numbers": json.RawMessage(`{"incoming_phone_numbers":[{"sid":"PN1","phone_number":"+14155550101","voice_url":"https://old.test/voice","status_callback":"https://old.test/status"}]}`),
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	a := &App{installID: 42}
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{
		ID: "route-callbacks", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", AgentID: 7, Enabled: true, TimeoutSec: 60,
		Secret: "route-secret", CreatedAt: now, UpdatedAt: now,
	}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if err := a.configureTwilioRoute(ctx, &route); err != nil {
		t.Fatal(err)
	}
	stored, err := a.db().findRoute(route.ID)
	if err != nil || stored.PreviousVoiceURL != "https://old.test/voice" || stored.PreviousStatusCallback != "https://old.test/status" {
		t.Fatalf("stored route=%+v err=%v", stored, err)
	}
	configured := platform.integrationCalls[len(platform.integrationCalls)-1]
	if configured.Tool != "update_phone_number" || configured.Input["VoiceUrl"] != a.inboundRouteURL(route) ||
		configured.Input["StatusCallback"] != a.twilioRouteStatusURL(route) {
		t.Fatalf("configured callbacks=%+v", configured)
	}
	if err := a.disableTwilioRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	restored := platform.integrationCalls[len(platform.integrationCalls)-1]
	if restored.Input["VoiceUrl"] != "https://old.test/voice" || restored.Input["StatusCallback"] != "https://old.test/status" {
		t.Fatalf("restored callbacks=%+v", restored.Input)
	}
}

func TestImmediateAnswerCleansUpThreadWhenCarrierAnswerFails(t *testing.T) {
	platform := &answerPlatform{failCarrier: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	a := &App{installID: 42}

	route := routeRow{
		ID: "route-failure", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", AgentID: 7, Enabled: true, TimeoutSec: 60,
		AnswerMode: answerModeRealtimeImmediate, AutoDirective: "Help the caller.", AutoGreeting: defaultInboundGreeting,
	}
	call := testCall("immediate-failure", "pending")
	call.Direction = "inbound"
	call.RouteID = route.ID
	call.ThreadID = "pending-" + call.ID
	call.AudioBridgeURL = "pending"
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	if err := a.answerImmediateCall(ctx, &route, call.ID); err == nil {
		t.Fatal("carrier answer failure was ignored")
	}
	if len(platform.killed) != 1 || platform.killed[0] != "tel-"+call.ID {
		t.Fatalf("spawned thread was not cleaned up: %v", platform.killed)
	}
	stored, err := a.db().findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || stored.EndedAt == "" {
		t.Fatalf("failed automatic answer remained retryable: %+v", stored)
	}
}

func TestImmediateAnswerConfigurationRequiresDirective(t *testing.T) {
	if _, _, _, _, err := normalizeRouteAnswerConfig(answerModeRealtimeImmediate, "", "", ""); err == nil {
		t.Fatal("immediate answer accepted an empty directive")
	}
	mode, directive, voice, greeting, err := normalizeRouteAnswerConfig(
		answerModeRealtimeImmediate, " Help the caller. ", " marin ", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if mode != answerModeRealtimeImmediate || directive != "Help the caller." || voice != "marin" || greeting != defaultInboundGreeting {
		t.Fatalf("unexpected normalized answer config: %q %q %q %q", mode, directive, voice, greeting)
	}
}

func TestInboundCarrierCallbackIsIdempotent(t *testing.T) {
	db := testCallsDB(t)
	first := testCall("inbound-a", "pending")
	first.Direction = "inbound"
	first.RouteID = "route"
	stored, created, err := db.insertInboundCallWithEvent(first, "incoming")
	if err != nil || !created || stored.ID != first.ID {
		t.Fatalf("insert first inbound: stored=%v created=%t err=%v", stored, created, err)
	}
	duplicate := first
	duplicate.ID = "inbound-b"
	duplicate.ThreadID = "thread-inbound-b"
	stored, created, err = db.insertInboundCallWithEvent(duplicate, "duplicate")
	if err != nil || created || stored.ID != first.ID {
		t.Fatalf("dedupe inbound: stored=%v created=%t err=%v", stored, created, err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM inbound_event_outbox`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("outbox count=%d err=%v", count, err)
	}
}

func TestPanelResponseDoesNotExposeBridgeCredentials(t *testing.T) {
	call := testCall("redact", "in-progress")
	encoded, err := json.Marshal(callsPanelPublic([]callRow{call}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"AudioBridgeURL", "callback_secret", "core.test", "token=secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("panel response leaked %q: %s", secret, text)
		}
	}
}

func TestMediaClaimIsExclusive(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("media", "in-progress")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.claimMedia(call.ID)
	if err != nil || !claimed {
		t.Fatalf("first media claim failed: %t %v", claimed, err)
	}
	claimed, err = db.claimMedia(call.ID)
	if err != nil || claimed {
		t.Fatalf("duplicate media claim accepted: %t %v", claimed, err)
	}
	if err := db.releaseMedia(call.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err = db.claimMedia(call.ID)
	if err != nil || !claimed {
		t.Fatalf("reconnect media claim failed: %t %v", claimed, err)
	}
}

func TestCallCallbackTokenAndURLRedaction(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(&answerPlatform{}))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })
	call := testCall("token", "in-progress")
	call.CarrierSlug = "telnyx"
	req := httptest.NewRequest(http.MethodPost, "/webhook/status/token?token=callback-secret", nil)
	if err := (&App{}).authorizeCallRequest(req, &call); err != nil {
		t.Fatalf("valid callback token rejected: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/webhook/status/token?token=wrong", nil)
	if err := (&App{}).authorizeCallRequest(req, &call); err == nil {
		t.Fatal("invalid callback token accepted")
	}
	redacted := redactURL("wss://core.test/audio?token=secret&thread=t")
	if strings.Contains(redacted, "secret") || !strings.Contains(redacted, "REDACTED") {
		t.Fatalf("URL was not redacted: %s", redacted)
	}
	redacted = redactURL("wss://agents.test/api/apps/telephony/_install/42/media/twilio/call/callback-secret")
	if strings.Contains(redacted, "callback-secret") || !strings.HasSuffix(redacted, "/REDACTED") {
		t.Fatalf("path credential was not redacted: %s", redacted)
	}
}

func TestMediaCallbackUsesPathCredential(t *testing.T) {
	call := testCall("media-token", "in-progress")
	call.CarrierSlug = "telnyx"
	req := httptest.NewRequest(http.MethodGet, "/media/telnyx/media-token/callback-secret", nil)
	if err := (&App{}).authorizeCallRequest(req, &call); err != nil {
		t.Fatalf("valid media path credential rejected: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/media/telnyx/media-token/wrong", nil)
	if err := (&App{}).authorizeCallRequest(req, &call); err == nil {
		t.Fatal("invalid media path credential accepted")
	}
}

func TestVoiceValidationSupportsRealtimeProviders(t *testing.T) {
	for _, voice := range []string{"", "marin", "Kore", "eve", "provider_voice-1"} {
		if !validVoice(voice) {
			t.Fatalf("valid provider voice %q rejected", voice)
		}
	}
	for _, voice := range []string{"voice name", "../../secret", strings.Repeat("a", 65)} {
		if validVoice(voice) {
			t.Fatalf("unsafe provider voice %q accepted", voice)
		}
	}
}

func TestTwilioMediaSignatureUsesInstalledPublicPath(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://agents.example.test")
	a := &App{installID: 42}
	req := httptest.NewRequest(http.MethodGet, "/media/twilio/call/callback-secret", nil)
	fullURL := a.publicRequestURL(req)
	if fullURL != "wss://agents.example.test/api/apps/telephony/_install/42/media/twilio/call/callback-secret" {
		t.Fatalf("Twilio media signature URL = %q", fullURL)
	}
}

func TestCarrierURLsCarrySafeDispatchContext(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://agents.example.test")
	a := &App{installID: 42}
	media := a.publicWSStreamURL("twilio", "call", "secret")
	status := a.statusCallbackURL("call", "secret", "project-a")
	route := a.inboundRouteURL(routeRow{ID: "route", CarrierSlug: "twilio", Secret: "route-secret", ProjectID: "project-a"})
	mediaURL, err := url.Parse(media)
	if err != nil {
		t.Fatal(err)
	}
	if mediaURL.RawQuery != "" || mediaURL.Path != "/api/apps/telephony/_install/42/media/twilio/call/secret" {
		t.Fatalf("media URL must use path dispatch without a query: %s", media)
	}
	for _, raw := range []string{status, route} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Get("project_id") != "project-a" {
			t.Fatalf("carrier URL lacks project dispatch context: %s", raw)
		}
	}
}

func TestPanelProjectPrefersProxyContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/calls?project_id=project-a", nil)
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	project, err := (&App{}).panelProject(req)
	if err != nil || project != "project-a" {
		t.Fatalf("trusted project context rejected: project=%q err=%v", project, err)
	}
	req = httptest.NewRequest(http.MethodGet, "/calls?project_id=project-b", nil)
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	if _, err := (&App{}).panelProject(req); err == nil {
		t.Fatal("mismatched project query and proxy context accepted")
	}
}

func TestActiveRouteIsUniqueAndCanBeDisabled(t *testing.T) {
	db := testCallsDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{
		ID: "route-a", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", PhoneNumberSID: "PN1", AgentID: 7, Enabled: true,
		TimeoutSec: 60, Secret: "route-secret", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.insertRoute(route); err != nil {
		t.Fatal(err)
	}
	duplicate := route
	duplicate.ID = "route-b"
	if err := db.insertRoute(duplicate); err == nil {
		t.Fatal("duplicate active route accepted")
	}
	if err := db.updateRoutePreviousVoiceURL(route.ID, "https://previous.test/voice"); err != nil {
		t.Fatal(err)
	}
	if err := db.disableRoute(route.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.insertRoute(duplicate); err != nil {
		t.Fatalf("replacement route rejected after disable: %v", err)
	}
	stored, err := db.findRoute(route.ID)
	if err != nil || stored.Enabled || stored.PreviousVoiceURL != "https://previous.test/voice" {
		t.Fatalf("disabled route not persisted correctly: route=%+v err=%v", stored, err)
	}
}

func TestPlivoNativeTelephonyCodecRoundTripShape(t *testing.T) {
	pcm24 := make([]byte, 960)
	payload, err := encodeCarrierPayload(pcm24, carrierCodecPCMU8)
	if err != nil {
		t.Fatal(err)
	}
	out := buildCarrierOutbound("plivo", "", payload).(map[string]any)
	media := out["media"].(map[string]string)
	if media["sampleRate"] != "8000" || media["contentType"] != "audio/x-mulaw" {
		t.Fatalf("unexpected Plivo media shape: %#v", media)
	}
}

func TestRealtimeInterruptControlAndCarrierClearShapes(t *testing.T) {
	if !realtimeInterruptControl([]byte(`{"type":"interrupt"}`)) {
		t.Fatal("valid realtime interrupt control was not recognized")
	}
	if realtimeInterruptControl([]byte(`{"type":"other"}`)) || realtimeInterruptControl([]byte(`not-json`)) {
		t.Fatal("invalid controls must be ignored")
	}
	signalwire := buildCarrierClear("signalwire", "stream-1").(map[string]string)
	if signalwire["event"] != "clear" || signalwire["streamSid"] != "stream-1" {
		t.Fatalf("signalwire clear = %#v", signalwire)
	}
	plivo := buildCarrierClear("plivo", "").(map[string]string)
	if plivo["event"] != "clearAudio" {
		t.Fatalf("plivo clear = %#v", plivo)
	}
	if got := buildCarrierClear("unknown", ""); got != nil {
		t.Fatalf("unsupported carrier clear = %#v", got)
	}
}

func TestRealtimeAudioFrameControl(t *testing.T) {
	control, ok := parseRealtimeBridgeControl([]byte(`{"type":"audio.frame","response_id":"response-1","item_id":"item-1","audio_end_ms":320}`))
	if !ok || control.Type != "audio.frame" || control.ResponseID != "response-1" || control.ItemID != "item-1" || control.AudioEndMS != 320 {
		t.Fatalf("unexpected audio frame control: %#v, ok=%v", control, ok)
	}
}

func TestTwilioPlaybackTrackerDoesNotAcknowledgeClearedAudio(t *testing.T) {
	tracker := newTwilioPlaybackTracker()
	first := tracker.add("item-1", 120)
	if first != "apt-1" {
		t.Fatalf("first mark=%q, want apt-1", first)
	}
	progress, ok := tracker.acknowledge(first)
	if !ok || progress.ItemID != "item-1" || progress.AudioEndMS != 120 {
		t.Fatalf("unexpected progress: %#v, ok=%v", progress, ok)
	}
	second := tracker.add("item-1", 240)
	tracker.clear()
	if progress, ok := tracker.acknowledge(second); ok {
		t.Fatalf("cleared audio acknowledged as played: %#v", progress)
	}
}

func TestTwilioAudioPacketsAreAtMostTwentyMillisecondsWithPreciseMarks(t *testing.T) {
	pcm8 := make([]int16, 401)
	packets := twilioAudioPackets(pcm8, 1200, realtimeBridgeControl{
		Type: "audio.frame", ItemID: "item-1", AudioEndMS: 1000,
	})
	if len(packets) != 3 {
		t.Fatalf("packet count=%d, want 3", len(packets))
	}
	wantLengths := []int{160, 160, 81}
	previousEnd := 0
	for i, packet := range packets {
		if len(packet.PCM) != wantLengths[i] {
			t.Fatalf("packet %d samples=%d, want %d", i, len(packet.PCM), wantLengths[i])
		}
		if len(packet.PCM) > twilioMediaPacketSamples {
			t.Fatalf("packet %d exceeds 20ms: %d samples", i, len(packet.PCM))
		}
		if packet.AudioEndMS <= previousEnd || packet.AudioEndMS > 1000 {
			t.Fatalf("packet %d has invalid end timestamp %d after %d", i, packet.AudioEndMS, previousEnd)
		}
		previousEnd = packet.AudioEndMS
	}
	if packets[len(packets)-1].AudioEndMS != 1000 {
		t.Fatalf("final packet end=%d, want frame end 1000", packets[len(packets)-1].AudioEndMS)
	}
}

func TestTwilioAudioPacketMarksStayWithinTwentyMilliseconds(t *testing.T) {
	packets := twilioAudioPackets(make([]int16, 480), 1440, realtimeBridgeControl{
		Type: "audio.frame", ItemID: "item-2", AudioEndMS: 2060,
	})
	previous := 2000
	for i, packet := range packets {
		if delta := packet.AudioEndMS - previous; delta <= 0 || delta > 20 {
			t.Fatalf("packet %d timestamp delta=%dms, want 1..20ms", i, delta)
		}
		previous = packet.AudioEndMS
	}
}

func TestTelnyxCallbackParsing(t *testing.T) {
	body := `{"data":{"event_type":"call.hangup","payload":{"call_control_id":"v2:test","hangup_cause":"normal_clearing"}}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/status/call?token=x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	status, reason, sid := callbackStatusFor("telnyx", req)
	if status != "completed" || reason != "normal_clearing" || sid != "v2:test" {
		t.Fatalf("unexpected Telnyx callback: status=%q reason=%q sid=%q", status, reason, sid)
	}
}

func TestIdentifiersAndE164Validation(t *testing.T) {
	first, second := newCallID(), newCallID()
	if len(first) != 32 || first == second {
		t.Fatalf("call IDs are not 128-bit unique values: %q %q", first, second)
	}
	for _, number := range []string{"+14155550100", "+34648257793"} {
		if !validE164(number) {
			t.Fatalf("valid E.164 rejected: %s", number)
		}
	}
	for _, number := range []string{"648257793", "+", "+123abc", "+123"} {
		if validE164(number) {
			t.Fatalf("invalid E.164 accepted: %s", number)
		}
	}
}
