package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
)

func runtimeTestActive() *telephonyRuntime {
	return &telephonyRuntime{deployment: "deployment-a", generation: 1, state: "active", armed: true, initialized: true, expires: time.Now().Add(time.Minute), http: &http.Client{Timeout: time.Second}, endpoints: map[string]string{}}
}
func runtimeLifecycleRequest(a *App, action, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", runtimeAppPrefix+action, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer runtime-secret")
	w := httptest.NewRecorder()
	a.handleRuntimeLifecycle(w, r)
	return w
}
func TestManagedRuntimeActivationAndOwnership(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	var active atomic.Bool
	var status atomic.Int32
	status.Store(200)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Error("missing process auth")
		}
		if status.Load() != 200 {
			w.WriteHeader(int(status.Load()))
			return
		}
		writeJSON(w, runtimeOwnership{Active: active.Load(), Deployment: "deployment-a", Generation: 1, Expires: time.Now().Add(15 * time.Second).Unix()})
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	var starts atomic.Int32
	a := &App{runtime: &telephonyRuntime{deployment: "deployment-a", generation: 1, state: "standby", http: gateway.Client(), initialize: func() error { starts.Add(1); return nil }}}
	status.Store(409)
	a.refreshRuntimeOwnership(t.Context())
	if a.runtime.fenced || a.runtimeOwned() {
		t.Fatal("a new installation must wait for its first owner without admission")
	}
	status.Store(200)
	if w := runtimeLifecycleRequest(a, "activate", `{"deployment_id":"deployment-a","generation":2}`); w.Code != 409 {
		t.Fatal("wrong generation armed")
	}
	for i := 0; i < 2; i++ {
		if w := runtimeLifecycleRequest(a, "activate", `{"deployment_id":"deployment-a","generation":1}`); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	a.refreshRuntimeOwnership(t.Context())
	if starts.Load() != 0 {
		t.Fatal("activation ran before ownership commit")
	}
	if _, err := a.runtimeBegin(true); err == nil {
		t.Fatal("standby admitted work")
	}
	active.Store(true)
	a.refreshRuntimeOwnership(t.Context())
	a.runtime.initWG.Wait()
	a.refreshRuntimeOwnership(t.Context())
	if starts.Load() != 1 || !a.runtimeOwned() {
		t.Fatal("activation was not idempotent")
	}
	status.Store(503)
	a.refreshRuntimeOwnership(t.Context())
	if !a.runtimeOwned() {
		t.Fatal("temporary lookup discarded verified lease")
	}
	a.runtime.expires = time.Now().Add(-time.Second)
	a.refreshRuntimeOwnership(t.Context())
	if a.runtimeOwned() || !a.runtime.fenced {
		t.Fatal("expired lease admitted work")
	}
	status.Store(200)
	a.refreshRuntimeOwnership(t.Context())
	if a.runtimeOwned() {
		t.Fatal("fenced process resurrected itself")
	}
	if w := runtimeLifecycleRequest(a, "activate", `{"deployment_id":"deployment-a","generation":1}`); w.Code != 409 {
		t.Fatal("fenced process must restart")
	}
}
func TestManagedRuntimeStandbyDoesNotRecoverOrWrite(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	t.Setenv("APTEVA_RUNTIME_MODE", "standby")
	t.Setenv("APTEVA_DEPLOYMENT_ID", "deployment-a")
	t.Setenv("APTEVA_DEPLOYMENT_GENERATION", "1")
	t.Setenv("APTEVA_RUNTIME_ENDPOINTS", "")
	row := testCall("standby-call", "in-progress")
	row.ProjectID = "project-a"
	row.MediaStatus = "connected"
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	a.db().db.Exec(`UPDATE calls SET media_active=1,media_status='connected' WHERE id=?`, row.ID)
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.OnUnmount(ctx)
	for _, worker := range a.Workers() {
		if err := worker.Run(t.Context(), ctx); err != nil {
			t.Fatal(err)
		}
	}
	var media int
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT media_active,media_status FROM calls WHERE id=?`, row.ID).Scan(&media, &status); err != nil {
		t.Fatal(err)
	}
	if media != 1 || status != "connected" || a.dispatcher != nil {
		t.Fatal("standby modified live recovery state")
	}
	for _, route := range a.HTTPRoutes() {
		if route.Pattern == "/inbound/telnyx/" {
			w := httptest.NewRecorder()
			route.Handler(w, httptest.NewRequest("POST", "/inbound/telnyx/test", nil))
			if w.Code != 503 {
				t.Fatal("standby carrier admission", w.Code)
			}
		}
	}
}
func TestManagedRuntimeDrainWaitsForCallsAndWriters(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	a.runtime = runtimeTestActive()
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	row := testCall("drain-call", "in-progress")
	row.ProjectID = "project-a"
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	done, err := a.runtimeBegin(false)
	if err != nil {
		t.Fatal(err)
	}
	w := runtimeLifecycleRequest(a, "drain", `{"deployment_id":"deployment-a","generation":1}`)
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["drained"] != false {
		t.Fatal("live call was drained")
	}
	if _, err = a.runtimeBegin(true); err == nil {
		t.Fatal("draining admitted new call")
	}
	control, err := a.runtimeBegin(false)
	if err != nil {
		t.Fatal("draining blocked existing control")
	}
	control()
	if _, err = a.db().db.Exec(`UPDATE calls SET status='completed',ended_at=? WHERE id=?`, ringTime(time.Now()), row.ID); err != nil {
		t.Fatal(err)
	}
	a.recordingWriters.Add(1)
	done()
	result, err = a.runtimeDrainStatus()
	if err != nil || result["drained"] != false {
		t.Fatal("writer was ignored", result, err)
	}
	a.recordingWriters.Add(-1)
	result, err = a.runtimeDrainStatus()
	if err != nil || result["drained"] != true {
		t.Fatal(result, err)
	}
	if _, err = a.runtimeBegin(false); err == nil {
		t.Fatal("drained writer restarted")
	}
}
func TestManagedRuntimeLegacyAdoptionDrainAndResume(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, runtimeOwnership{Deployment: "legacy", Generation: 0, Expires: time.Now().Add(15 * time.Second).Unix()})
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	w := runtimeLifecycleRequest(a, "drain", `{"deployment_id":"legacy","generation":0}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"drained":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := a.runtimeBegin(true); err == nil {
		t.Fatal("adopted legacy process admitted work")
	}
	w = runtimeLifecycleRequest(a, "activate", `{"deployment_id":"legacy","generation":0}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	done, err := a.runtimeBegin(true)
	if err != nil {
		t.Fatal("legacy rollback did not resume")
	}
	done()
}
func TestManagedRuntimeSocketOwnershipDoesNotChangePCM(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42, runtime: runtimeTestActive()}
	insertSoftphoneCall(t, a, "in-progress")
	// Exercise the real hub handler with managed ownership guarding.
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/", a.handlePeerSocket)
	mux.HandleFunc("/softphone/media/", a.runtimeHTTP(a.handleSoftphoneMedia, false))
	server := httptest.NewServer(mux)
	defer server.Close()
	peer := dialWS(t, server.URL+"/peer/call-soft-1/cb-secret")
	defer peer.Close()
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	defer browser.Close()
	readSoftphoneEventWithin(t, browser, "ready", time.Second)
	pcm := pcm16ToBytes([]int16{1000, -4000, 3000})
	for i := 0; i < 10; i++ {
		if err := wsutil.WriteClientBinary(browser, pcm); err != nil {
			t.Fatal(err)
		}
		if got := readBinaryWithin(t, peer, time.Second); string(got) != string(pcm) {
			t.Fatal("managed path changed PCM")
		}
	}
	// Own the request cancellation even when its reader is blocked.
	a.runtime.mu.Lock()
	a.runtime.expires = time.Now().Add(-time.Second)
	a.runtime.mu.Unlock()
	browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, _, err := wsutil.ReadServerData(browser)
		if err != nil {
			break
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.runtime.mu.Lock()
		work := a.runtime.work
		a.runtime.mu.Unlock()
		if work == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fenced socket retained writer")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestManagedRuntimeRecordingReferencesMoveWithState(t *testing.T) {
	a, memory := withTelephonyTestContext(t, &answerPlatform{})
	fileDB := filepath.Join(t.TempDir(), "app.db")
	if _, err := memory.AppDB().Exec(`VACUUM INTO ?`, fileDB); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", fileDB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	manifest := a.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, &answerPlatform{}, nil)
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = memory; db.Close() })

	a.runtime = runtimeTestActive()
	root, err := a.recordingRoot()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(root, 0700)
	row := testCall("portable-recording", "completed")
	row.ProjectID = "project-a"
	a.db().insertCall(row)
	rec, err := a.db().upsertProviderRecording(&row, localRecordingProvider, "portable", "completed", "wav", 20, 2, "both")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "call-portable.wav")
	os.WriteFile(file, []byte("fixture"), 0600)
	defer os.Remove(file)
	ctx.AppDB().Exec(`UPDATE recordings SET size_bytes=7 WHERE id=?`, rec.ID)
	ctx.AppDB().Exec(`INSERT INTO local_recording_files(recording_id,path) VALUES(?,?)`, rec.ID, "/previous-mount/recordings/call-portable.wav")
	if err = a.relocateRecordingPaths(); err != nil {
		t.Fatal(err)
	}
	var stored string
	ctx.AppDB().QueryRow(`SELECT path FROM local_recording_files WHERE recording_id=?`, rec.ID).Scan(&stored)
	if stored != "recordings/call-portable.wav" {
		t.Fatal(stored)
	}
	got, err := a.resolveRecordingPath(stored)
	if err != nil || got != file {
		t.Fatal(got, err)
	}
	ctx.AppDB().Exec(`UPDATE recordings SET size_bytes=8 WHERE id=?`, rec.ID)
	if err = a.relocateRecordingPaths(); err == nil {
		t.Fatal("unverified transfer passed")
	}
}
func TestManagedRuntimeEndpointValidation(t *testing.T) {
	for _, raw := range []string{`{"browser":"https://voice.test"}`, `{"carrier_media":"wss://voice.test"}`, `{}`} {
		if _, err := parseRuntimeEndpoints(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"browser":"http://voice.test"}`, `{"media":"https://user:secret@voice.test"}`, `{"signaling":"https://voice.test?token=secret"}`, `[]`} {
		if _, err := parseRuntimeEndpoints(raw); err == nil {
			t.Fatal("unsafe endpoint", raw)
		}
	}
	a := &App{runtime: runtimeTestActive()}
	a.runtime.endpoints = map[string]string{"callbacks": "https://voice.test/api", "carrier_media": "wss://voice.test"}
	if got := a.publicInstalledAppURL(); got != "https://voice.test/api" {
		t.Fatal(got)
	}
	if got := a.publicWSStreamURL("telnyx", "call", "secret"); got != "wss://voice.test/media/telnyx/call/secret" {
		t.Fatal(got)
	}
}

func TestManagedRuntimeDrainAcceptsExistingTelnyxCallbacks(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "telnyx", Fields: map[string]string{"public_key": base64.StdEncoding.EncodeToString(public)}}}
	a, _ := withTelephonyTestContext(t, platform)
	a.runtime = runtimeTestActive()
	a.runtime.state = "draining"
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{ID: "draining-route", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 9, PhoneNumber: "+33189000001", Enabled: true, Secret: "route-secret", TimeoutSec: 60, PreviousVoiceURL: `{"application_id":"app-123"}`, CreatedAt: now, UpdatedAt: now}
	if err = a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	row := testCall("existing-drain-call", "in-progress")
	row.ProjectID = route.ProjectID
	row.RouteID = route.ID
	row.CarrierSID = "carrier-existing"
	row.CarrierSlug = "telnyx"
	row.CarrierConnectionID = 9
	row.Direction = "inbound"
	if err = a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	var handler http.HandlerFunc
	for _, r := range a.HTTPRoutes() {
		if r.Pattern == "/inbound/telnyx/" {
			handler = r.Handler
		}
	}
	if handler == nil {
		t.Fatal("actual route missing")
	}
	send := func(event, sid string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"id": event + sid, "event_type": event, "occurred_at": now, "payload": map[string]any{"call_control_id": sid, "connection_id": "app-123", "from": "+33611111111", "to": route.PhoneNumber, "hangup_cause": "normal_clearing", "hangup_source": "caller"}}})
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		signature := ed25519.Sign(private, append([]byte(timestamp+"|"), body...))
		req := httptest.NewRequest("POST", "/inbound/telnyx/"+route.ID+"?secret="+route.Secret+"&project_id="+route.ProjectID, strings.NewReader(string(body)))
		req.Header.Set("Telnyx-Timestamp", timestamp)
		req.Header.Set("Telnyx-Signature-Ed25519", base64.StdEncoding.EncodeToString(signature))
		w := httptest.NewRecorder()
		handler(w, req)
		return w
	}
	for _, event := range []string{"call.answered", "call.hangup"} {
		if w := send(event, row.CarrierSID); w.Code != 204 {
			t.Fatal(event, w.Code, w.Body.String())
		}
	}
	got, err := a.db().findCall(row.ID)
	if err != nil || got == nil || !isTerminalStatus(got.Status) {
		t.Fatal("existing hangup did not complete", got, err)
	}
	if w := send("call.initiated", "carrier-new"); w.Code != 503 {
		t.Fatal("new call not gated", w.Code, w.Body.String())
	}
	got, err = a.db().findInboundCallByCarrierSID(route.ID, 9, "carrier-new")
	if err != nil || got != nil {
		t.Fatal("drain created call", got, err)
	}
	// Existing control/read work is permitted; operator mutations are not.
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"GET", "/numbers/connected", 200}, {"POST", "/numbers/outbound-transport", 503}, {"POST", "/softphone/place", 503}, {"POST", "/softphone/renew/existing", 200}} {
		w := httptest.NewRecorder()
		a.runtimeHTTP(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }, runtimeNewHTTP(tc.path))(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Fatal(tc, w.Code)
		}
	}
}

func TestManagedRuntimeSlowInitializationKeepsOwnershipAndDrain(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, runtimeOwnership{Active: true, Deployment: "deployment-a", Generation: 1, Expires: time.Now().Add(15 * time.Second).Unix()})
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	entered, release := make(chan struct{}), make(chan struct{})
	a.runtime = &telephonyRuntime{deployment: "deployment-a", generation: 1, state: "standby", armed: true, http: gateway.Client(), initialize: func() error { close(entered); <-release; return nil }}
	a.refreshRuntimeOwnership(t.Context())
	<-entered
	a.runtime.mu.Lock()
	a.runtime.expires = time.Now().Add(-time.Second)
	a.runtime.mu.Unlock()
	a.refreshRuntimeOwnership(t.Context())
	a.runtime.mu.Lock()
	renewed := time.Now().Before(a.runtime.expires)
	a.runtime.mu.Unlock()
	if !renewed {
		t.Fatal("initialization blocked renewal")
	}
	w := runtimeLifecycleRequest(a, "drain", `{"deployment_id":"deployment-a","generation":1}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"drained":true`) {
		t.Fatal("initializer omitted from drain", w.Body.String())
	}
	close(release)
	a.runtime.initWG.Wait()
	if a.runtimeOwned() && a.runtime.state != "draining" {
		t.Fatal("initialization reopened admission after drain")
	}
	if _, err := a.runtimeBegin(true); err == nil {
		t.Fatal("new call admitted after drain")
	}
}

func TestManagedRuntimeAsyncPreparationCountsUntilFinished(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	a.runtime = runtimeTestActive()
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(returned)
		_, _ = a.sharedRealtimeWork(&callRow{ID: "async", ProjectID: "project-a"}, "test", true, func(*callRow) error { close(entered); <-release; return nil })
	}()
	<-entered
	a.runtime.mu.Lock()
	a.runtime.state = "draining"
	a.runtime.mu.Unlock()
	counts, err := a.runtimeDrainStatus()
	if err != nil || counts["drained"] != false {
		t.Fatal(counts, err)
	}
	close(release)
	<-returned
	// The waiter can wake before the goroutine's deferred accounting completes.
	deadline := time.Now().Add(time.Second)
	for {
		counts, err = a.runtimeDrainStatus()
		if err != nil {
			t.Fatal(err)
		}
		if counts["drained"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(counts)
		}
		time.Sleep(time.Millisecond)
	}
}

func BenchmarkManagedRuntimeAdmission(b *testing.B) {
	for _, managed := range []bool{false, true} {
		name := "legacy"
		a := &App{}
		if managed {
			name = "managed"
			a.runtime = runtimeTestActive()
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				done, err := a.runtimeBegin(false)
				if err != nil {
					b.Fatal(err)
				}
				done()
			}
		})
	}
}

func TestManagedRuntimeLegacyAuthorizationFailsClosed(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	if w := runtimeLifecycleRequest(a, "activate", `{"deployment_id":"legacy","generation":0}`); w.Code != 409 {
		t.Fatal("unauthorized legacy activation accepted", w.Code)
	}
}

func TestManagedRuntimeSIPStandbyRejectsPromptlyWithoutCreatingCall(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	a.runtime = &telephonyRuntime{state: "standby"}
	cfg := directSIPTestConfig()
	cfg.Transport = "udp"
	cfg.ListenAddress = fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t))
	cfg.PublicHost = "127.0.0.1"
	cfg.PublicIP = netip.MustParseAddr("127.0.0.1")
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	gateway, err := newSIPGateway(a, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = gateway.Start(); err != nil {
		t.Fatal(err)
	}
	defer gateway.Stop()
	address, err := net.ResolveUDPAddr("udp4", cfg.ListenAddress)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUDP("udp4", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	message := fmt.Sprintf("INVITE sip:+12025550100@127.0.0.1 SIP/2.0\r\nVia: SIP/2.0/UDP %s;branch=z9hG4bK-runtime;rport\r\nMax-Forwards: 70\r\nFrom: <sip:caller@127.0.0.1>;tag=runtime\r\nTo: <sip:+12025550100@127.0.0.1>\r\nCall-ID: standby-runtime\r\nCSeq: 1 INVITE\r\nContact: <sip:caller@%s>\r\nContent-Length: 0\r\n\r\n", conn.LocalAddr(), conn.LocalAddr())
	started := time.Now()
	conn.SetReadDeadline(started.Add(time.Second))
	if _, err = conn.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	n, err := conn.Read(buffer)
	if err != nil || !strings.HasPrefix(string(buffer[:n]), "SIP/2.0 503") {
		t.Fatal("standby silently waited instead of rejecting", err, string(buffer[:n]))
	}
	var calls int
	if err = a.db().db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&calls); err != nil || calls != 0 {
		t.Fatal("standby SIP created state", calls, err)
	}
}
