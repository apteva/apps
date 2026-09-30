package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"sync/atomic"
)

func listenerFixture(t *testing.T) (*App, callRow, *callAudioTap, phonePolicy) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	policy := phoneTestPolicy(t, app)
	policy.Users[3].Listen = true
	rec := phoneTestRequest(app, nil, "PUT", "/access/policy", policy)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &policy) != nil {
		t.Fatalf("policy %d %s", rec.Code, rec.Body)
	}
	row := phoneTestCall(t, app, "listened", "in-progress")
	if err := app.db().updateMediaStatus(row.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	row.MediaStatus = "connected"
	tap := app.listeners.openBridge(row.ID)
	t.Cleanup(func() { app.listeners.closeBridge(row.ID, tap) })
	return app, row, tap, policy
}
func listenerIssue(t *testing.T, app *App, id string, identity *phoneIdentity) softphoneSession {
	t.Helper()
	rec := phoneTestRequest(app, identity, "POST", "/softphone/listen/"+id, nil)
	if rec.Code != 200 {
		t.Fatalf("listen: %d %s", rec.Code, rec.Body)
	}
	var session softphoneSession
	if json.Unmarshal(rec.Body.Bytes(), &session) != nil || session.SessionToken == "" {
		t.Fatal("invalid listener grant")
	}
	return session
}
func listenerServer(t *testing.T, app *App) *httptest.Server {
	t.Helper()
	var live sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		live.Add(1)
		defer live.Done()
		app.handleListenMedia(w, r)
	}))
	t.Cleanup(func() {
		server.Close()
		done := make(chan struct{})
		go func() { live.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("listener handler did not drain")
		}
	})
	return server
}
func listenerDial(t *testing.T, server *httptest.Server, session softphoneSession) net.Conn {
	t.Helper()
	return dialWS(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42"))
}
func assertListenerClosed(t *testing.T, conn net.Conn, reason string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, _, err := wsutil.ReadServerData(conn)
		if err != nil {
			if !strings.Contains(err.Error(), reason) {
				t.Fatalf("close reason %q: %v", reason, err)
			}
			return
		}
	}
}
func TestListenerAuthorizationAndIndependentCredentials(t *testing.T) {
	app, row, tap, policy := listenerFixture(t)
	alice, boss, eve := phoneTestIdentity("alice"), phoneTestIdentity("boss"), phoneTestIdentity("eve")
	owner, _ := app.phonePrincipal(row.ProjectID, alice)
	if err := app.setPhoneOwner(&row, owner, "sales"); err != nil {
		t.Fatal(err)
	}
	operator, err := app.issuePhoneSession(&row, owner)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := app.db().findCall(row.ID)
	for _, identity := range []*phoneIdentity{&alice, &eve} {
		if rec := phoneTestRequest(app, identity, "POST", "/softphone/listen/"+row.ID, nil); rec.Code != 403 && rec.Code != 404 {
			t.Fatalf("unauthorized listen %d", rec.Code)
		}
	}
	session := listenerIssue(t, app, row.ID, &boss)
	if !app.validPhoneMedia(&row, operator.SessionToken) {
		t.Fatal("listen replaced operator media grant")
	}
	if app.validPhoneMedia(&row, session.SessionToken) {
		t.Fatal("listen token accepted as operator token")
	}
	if app.listenerSessionValid(&row, operator.SessionToken) || app.listenerSessionValid(&row, "forged") {
		t.Fatal("foreign token accepted as listener")
	}
	another := row
	another.ID = "another"
	if app.listenerSessionValid(&another, session.SessionToken) {
		t.Fatal("token accepted on another call")
	}
	otherProject := row
	otherProject.ProjectID = "other"
	if app.listenerSessionValid(&otherProject, session.SessionToken) {
		t.Fatal("token crossed project")
	}
	after, _ := app.db().findCall(row.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("listening changed call row")
	}
	actualOwner, _, _ := app.phoneOwner(row.ID)
	if actualOwner != alice.key() {
		t.Fatal("listener changed ownership")
	}
	rec := phoneTestRequest(app, &boss, "GET", "/calls?call_id="+row.ID, nil)
	var listing struct {
		Calls []struct {
			Listenable bool `json:"listenable"`
			Supported  bool `json:"listen_supported"`
		} `json:"calls"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &listing) != nil || len(listing.Calls) != 1 || !listing.Calls[0].Listenable || !listing.Calls[0].Supported {
		t.Fatalf("capabilities: %d %s", rec.Code, rec.Body)
	}
	policy.Users[3].Listen = false
	rec = phoneTestRequest(app, nil, "PUT", "/access/policy", policy)
	if rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if app.listenerSessionValid(&row, session.SessionToken) {
		t.Fatal("revoked credential remained valid")
	}
	rec = phoneTestRequest(app, &boss, "POST", "/softphone/listen/"+row.ID, nil)
	if rec.Code != 404 {
		t.Fatalf("supervisor visibility implied listen permission: %d", rec.Code)
	}
	// Passive listeners do not create an operator hub, claims, or adviser reservations.
	if app.softphones.lookup(row.ID) != nil || len(tap.listeners) != 0 {
		t.Fatal("grant attached operator/media")
	}
}
func TestListenerMediaBothDirectionsStopAndAudit(t *testing.T) {
	app, row, tap, _ := listenerFixture(t)
	boss := phoneTestIdentity("boss")
	server := listenerServer(t, app)
	first := listenerIssue(t, app, row.ID, &boss)
	second := listenerIssue(t, app, row.ID, &boss)
	one, two := listenerDial(t, server, first), listenerDial(t, server, second)
	pcm := pcm16ToBytes(sinePCM(24000, 440, 480))
	for direction := uint32(0); direction < 2; direction++ {
		tap.publish(direction, pcm)
		for _, conn := range []net.Conn{one, two} {
			frame := readBinaryWithin(t, conn, time.Second)
			if binary.LittleEndian.Uint32(frame) != listenerFrameMagic || binary.LittleEndian.Uint32(frame[4:]) != direction || !bytes.Equal(frame[24:], pcm) {
				t.Fatal("directional audio changed")
			}
		}
	}
	rec := phoneTestRequest(app, &boss, "POST", "/softphone/listen-stop/"+row.ID, map[string]any{"session_token": first.SessionToken})
	if rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	assertListenerClosed(t, one, "listener_stopped")
	tap.publish(0, pcm)
	if got := readBinaryWithin(t, two, time.Second); !bytes.Equal(got[24:], pcm) {
		t.Fatal("stopping one listener affected another")
	}
	// Binary injection is rejected on this socket; no frame enters the call bridge.
	if err := wsutil.WriteClientBinary(two, pcm); err != nil {
		t.Fatal(err)
	}
	assertListenerClosed(t, two, "listener_protocol_violation")
	deadline := time.Now().Add(time.Second)
	for {
		var closed int
		_ = app.db().db.QueryRow(`SELECT COUNT(*) FROM telephony_listener_audit WHERE call_id=? AND left_at<>''`, row.ID).Scan(&closed)
		if closed == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("join/leave audit missing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if app.softphones.lookup(row.ID) != nil {
		t.Fatal("listener injected into operator hub")
	}
}
func TestListenerScopeExpiryLimitsAndNoBridge(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	boss := phoneTestIdentity("boss")
	r := httptest.NewRequest("POST", "/softphone/listen/"+row.ID, nil)
	r.Header.Set("X-Apteva-Project-ID", row.ProjectID)
	r.Header.Set("X-Apteva-Issuer-App", boss.IssuerApp)
	r.Header.Set("X-Apteva-Issuer-Install-ID", boss.IssuerInstallID)
	r.Header.Set("X-Apteva-Subject-Type", boss.SubjectType)
	r.Header.Set("X-Apteva-Subject-ID", boss.SubjectID)
	r.Header.Set("X-Apteva-Organization-ID", boss.OrganizationID)
	r.Header.Set("X-Apteva-Scopes", `[{"type":"app_user","app":"telephony","actions":["call.read"]}]`)
	w := httptest.NewRecorder()
	app.applicationUserHTTP(app.handleSoftphoneAction)(w, r)
	if w.Code != 403 {
		t.Fatalf("scope bypass %d", w.Code)
	}
	var sessions []softphoneSession
	for i := 0; i < 4; i++ {
		sessions = append(sessions, listenerIssue(t, app, row.ID, &boss))
	}
	if rec := phoneTestRequest(app, &boss, "POST", "/softphone/listen/"+row.ID, nil); rec.Code != 409 {
		t.Fatalf("limit bypass %d", rec.Code)
	}
	_, _ = app.db().db.Exec(`UPDATE telephony_listener_sessions SET expires_at=0 WHERE token_hash=?`, phoneHash(sessions[0].SessionToken))
	if app.listenerSessionValid(&row, sessions[0].SessionToken) {
		t.Fatal("expired listener accepted")
	}
	_ = listenerIssue(t, app, row.ID, &boss)
	other := phoneTestCall(t, app, "not-bridged", "in-progress")
	if rec := phoneTestRequest(app, &boss, "POST", "/softphone/listen/"+other.ID, nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "media_not_bridged") {
		t.Fatal("unbridged call was listenable")
	}
	if err := app.db().updateStatus(row.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if rec := phoneTestRequest(app, &boss, "POST", "/softphone/listen/"+row.ID, nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "call_ended") {
		t.Fatal("terminal outcome not distinct")
	}
}
func TestListenerRevocationAndCallerEndCloseOnlyListener(t *testing.T) {
	for _, mode := range []string{"revoke", "caller_end", "provider_revoke"} {
		t.Run(mode, func(t *testing.T) {
			app, row, _, policy := listenerFixture(t)
			boss := phoneTestIdentity("boss")
			var provider *phoneAuthProvider
			if mode == "provider_revoke" {
				p := phoneAuthProvider{ID: "auth", IssuerApp: "auth", IssuerInstallID: "11", URL: "https://auth.example/me", Format: "apteva-auth", Actions: []string{"call.listen"}}
				policy.Providers = []phoneAuthProvider{p}
				rec := phoneTestRequest(app, nil, "PUT", "/access/policy", policy)
				if json.Unmarshal(rec.Body.Bytes(), &policy) != nil {
					t.Fatal(rec.Body)
				}
				provider = &p
			}
			session := listenerIssue(t, app, row.ID, &boss)
			if provider != nil {
				raw, _ := json.Marshal(provider)
				_, _ = app.db().db.Exec(`UPDATE telephony_listener_sessions SET provider_json=? WHERE token_hash=?`, string(raw), phoneHash(session.SessionToken))
			}
			server := listenerServer(t, app)
			conn := listenerDial(t, server, session)
			// Wait for the ready handshake before changing policy or call status.
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			data, _, err := wsutil.ReadServerData(conn)
			if err != nil || !strings.Contains(string(data), "listener.ready") {
				t.Fatalf("listener handshake: %s %v", data, err)
			}
			reason := "access_revoked"
			if mode == "caller_end" {
				reason = "call_ended"
				if err := app.db().updateStatus(row.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
			} else {
				if mode == "revoke" {
					policy.Users[3].Listen = false
				} else {
					policy.Providers[0].Actions = []string{"call.read"}
				}
				rec := phoneTestRequest(app, nil, "PUT", "/access/policy", policy)
				if rec.Code != 200 {
					t.Fatal(rec.Body)
				}
			}
			assertListenerClosed(t, conn, reason)
			if mode != "caller_end" {
				current, _ := app.db().findCall(row.ID)
				if current.Status != "in-progress" {
					t.Fatal("revoking listener ended call")
				}
			}
		})
	}
}
func TestListenerTapSlowConsumersAndGenerationIsolation(t *testing.T) {
	var registry callListenerRegistry
	tap := registry.openBridge("call")
	slow, _ := tap.add("slow", 4)
	fast, _ := tap.add("fast", 4)
	pcm := make([]byte, 960)
	started := time.Now()
	for i := 0; i < 5000; i++ {
		tap.publish(uint32(i%2), pcm)
		select {
		case <-fast.audio:
		default:
		}
	}
	if time.Since(started) > time.Second {
		t.Fatal("slow listener blocked main call")
	}
	if len(slow.audio) > listenerQueueFrames || slow.dropped[0].Load()+slow.dropped[1].Load() == 0 {
		t.Fatal("listener queue not bounded")
	}
	tap.close("media_disconnected")
	next := registry.openBridge("call")
	registry.closeBridge("call", tap)
	if registry.lookup("call") != next {
		t.Fatal("old bridge teardown removed newer tap")
	}
	if _, err := tap.add("late", 4); err == nil {
		t.Fatal("old media resurrected")
	}
}
func TestListenerJSONObserverDoesNotCopyMarksOrClears(t *testing.T) {
	for _, codec := range []string{carrierCodecPCMU8, carrierCodecL16_16, carrierCodecL16_24} {
		t.Run(codec, func(t *testing.T) {
			tap := &callAudioTap{}
			listener, _ := tap.add("listen", 4)
			observe := tap.jsonOutputObserver(codec)
			observe([]byte(`{"event":"mark","mark":{"name":"not audio"}}`))
			observe([]byte(`{"event":"clear"}`))
			if len(listener.audio) != 0 {
				t.Fatal("control treated as audio")
			}
			pcm := sinePCM(carrierCodecSampleRate(codec), 440, carrierCodecSampleRate(codec)/50)
			payload := base64.StdEncoding.EncodeToString(pcm16ToBytes(pcm))
			if codec == carrierCodecPCMU8 {
				payload = base64.StdEncoding.EncodeToString(pcm16ToUlaw(pcm))
			}
			raw, _ := json.Marshal(map[string]any{"media": map[string]string{"payload": payload}})
			observe(raw)
			select {
			case frame := <-listener.audio:
				if binary.LittleEndian.Uint32(frame.data[4:]) != 1 || rmsPCM(bytesToPCM16(frame.data[24:])) < 100 {
					t.Fatal("missing audible outbound frame")
				}
			default:
				t.Fatal("no outbound audio")
			}
		})
	}
}
func BenchmarkListenerTap(b *testing.B) {
	for _, count := range []int{0, 1, 4, 16} {
		b.Run(fmt.Sprintf("%d_listeners", count), func(b *testing.B) {
			tap := &callAudioTap{}
			for i := 0; i < count; i++ {
				_, _ = tap.add(string(rune('a'+i)), 16)
			}
			pcm := make([]byte, 960)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tap.publish(uint32(i%2), pcm)
			}
		})
	}
}

// Run existing carrier continuity/cadence assertions with a healthy listener
// and a deliberately stalled listener. Both use the real bridge tap.
func startListenerProbe(t *testing.T, app *App, id string) func() {
	t.Helper()
	var tap *callAudioTap
	deadline := time.Now().Add(time.Second)
	for tap == nil && time.Now().Before(deadline) {
		tap = app.listeners.lookup(id)
		if tap == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if tap == nil {
		t.Fatal("carrier bridge did not expose a listening tap")
	}
	fast, err := tap.add("probe-fast", 4)
	if err != nil {
		t.Fatal(err)
	}
	slow, err := tap.add("probe-slow", 4)
	if err != nil {
		t.Fatal(err)
	}
	counts := &[2]atomic.Int64{}
	done := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-done:
				return
			case frame := <-fast.audio:
				counts[binary.LittleEndian.Uint32(frame.data[4:])].Add(1)
			}
		}
	}()
	t.Cleanup(func() { close(done); <-drained; tap.remove(fast); tap.remove(slow) })
	return func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && (counts[0].Load() == 0 || counts[1].Load() == 0) {
			time.Sleep(time.Millisecond)
		}
		if counts[0].Load() == 0 || counts[1].Load() == 0 {
			t.Fatalf("listener missed a direction: %d %d", counts[0].Load(), counts[1].Load())
		}
		if slow.dropped[0].Load()+slow.dropped[1].Load() == 0 {
			t.Fatal("stalled listener was not independently bounded")
		}
	}
}

func TestListenerBurstTrimIsDiagnosedWithoutAudioHistory(t *testing.T) {
	tap := &callAudioTap{}
	listener, _ := tap.add("listener", 4)
	tap.publish(0, make([]byte, 960*50))
	if len(listener.audio) != 6 || listener.trimmed[0].Load() != 44 {
		t.Fatalf("source trim unbounded/undiagnosed: queue=%d trim=%d", len(listener.audio), listener.trimmed[0].Load())
	}
	frame := <-listener.audio
	if binary.LittleEndian.Uint64(frame.data[8:]) != 44 {
		t.Fatal("source trim not reflected in directional sequence")
	}
	tap.remove(listener)
	tap.publish(0, make([]byte, 960))
	late, _ := tap.add("late", 4)
	if len(late.audio) != 0 {
		t.Fatal("listener received audio from before joining")
	}
}
func TestListenerAIDestinationGrant(t *testing.T) {
	app, row, _, policy := listenerFixture(t)
	if _, err := app.saveRoutingDestination(row.ProjectID, "ai", "AI", "ai", map[string]any{"agent_id": 1}, true); err != nil {
		t.Fatal(err)
	}
	_, err := app.db().db.Exec(`UPDATE calls SET peer_kind='realtime',routing_destination_id='ai' WHERE id=?`, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy.Users[3].Destinations = []string{"ai"}
	rec := phoneTestRequest(app, nil, "PUT", "/access/policy", policy)
	if rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	boss := phoneTestIdentity("boss")
	session := listenerIssue(t, app, row.ID, &boss)
	actual, _ := app.db().findCall(row.ID)
	if !app.listenerSessionValid(actual, session.SessionToken) {
		t.Fatal("AI listener not authorized")
	}
	if rec := phoneTestRequest(app, &boss, "POST", "/softphone/attach/"+row.ID, nil); rec.Code != 409 {
		t.Fatalf("listen enabled operator attachment to AI: %d", rec.Code)
	}
}
