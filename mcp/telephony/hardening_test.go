package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

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
	for _, pattern := range []string{"/media/twilio/", "/media/telnyx/", "/xml/plivo/", "/webhook/status/", "/inbound/twilio/"} {
		if !public[pattern] {
			t.Fatalf("carrier route %s must be public at the SDK layer", pattern)
		}
	}
	if public["/calls"] || public["/calls/"] || public["/numbers/"] {
		t.Fatal("panel routes must retain app-token authentication")
	}
	manifest := (&App{}).Manifest()
	for _, route := range manifest.Provides.HTTPRoutes {
		if route.Prefix == "/" {
			t.Fatal("root route must not be anonymous")
		}
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

func TestPlivoSixteenKilohertzCodecRoundTripShape(t *testing.T) {
	pcm24 := make([]byte, 960)
	payload, err := encodeCarrierPayload(pcm24, carrierCodecL16_16)
	if err != nil {
		t.Fatal(err)
	}
	out := buildCarrierOutbound("plivo16", "", payload).(map[string]any)
	media := out["media"].(map[string]string)
	if media["sampleRate"] != "16000" || media["contentType"] != "audio/x-l16" {
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
	plivo := buildCarrierClear("plivo16", "").(map[string]string)
	if plivo["event"] != "clearAudio" {
		t.Fatalf("plivo clear = %#v", plivo)
	}
	if got := buildCarrierClear("unknown", ""); got != nil {
		t.Fatalf("unsupported carrier clear = %#v", got)
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
