package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"math"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

type coachingWireFrame struct {
	op   ws.OpCode
	data []byte
}

func coachingSink(t *testing.T) (*websocketWriterPump, <-chan coachingWireFrame) {
	t.Helper()
	server, client := net.Pipe()
	pump := newWebSocketWriterPump(server, ws.StateServerSide)
	frames := make(chan coachingWireFrame, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			data, op, e := wsutil.ReadServerData(client)
			if e != nil {
				return
			}
			select {
			case frames <- coachingWireFrame{op, data}:
			default:
			}
		}
	}()
	t.Cleanup(func() { client.Close(); server.Close(); pump.Stop(); <-done })
	return pump, frames
}
func coachTestRequest(app *App, identity phoneIdentity, action, id string, body any, scope bool) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/softphone/"+action+"/"+id, bytes.NewReader(b))
	r.Header.Set("X-Apteva-Project-ID", "project-a")
	r.Header.Set("X-Apteva-Issuer-App", identity.IssuerApp)
	r.Header.Set("X-Apteva-Issuer-Install-ID", identity.IssuerInstallID)
	r.Header.Set("X-Apteva-Subject-Type", identity.SubjectType)
	r.Header.Set("X-Apteva-Subject-ID", identity.SubjectID)
	r.Header.Set("X-Apteva-Organization-ID", identity.OrganizationID)
	actions := []string{"call.listen"}
	if scope {
		actions = append(actions, "call.coach")
	}
	raw, _ := json.Marshal([]map[string]any{{"type": "app_user", "app": "telephony", "actions": actions}})
	r.Header.Set("X-Apteva-Scopes", string(raw))
	w := httptest.NewRecorder()
	app.applicationUserHTTP(app.handleSoftphoneAction)(w, r)
	return w
}
func coachingFixture(t *testing.T) (*App, callRow, *softphoneHub, *callAudioTap, phonePolicy, <-chan coachingWireFrame, <-chan coachingWireFrame) {
	t.Helper()
	app, row, tap, policy := listenerFixture(t)
	policy.Users[3].Coach = true
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	} else {
		if e := json.Unmarshal(w.Body.Bytes(), &policy); e != nil {
			t.Fatal(e)
		}
	}
	p, _ := app.phonePrincipal(row.ProjectID, phoneTestIdentity("alice"))
	if e := app.setPhoneOwner(&row, p, "sales"); e != nil {
		t.Fatal(e)
	}
	h := app.softphones.hubFor(row.ID)
	browser, bframes := coachingSink(t)
	peer, pframes := coachingSink(t)
	h.setBrowser(browser)
	h.markReady(browser)
	h.setPeer(peer)
	h.setCallState("inbound", "in-progress")
	h.mu.Lock()
	h.whisperBrowser = browser
	h.mu.Unlock()
	return app, row, h, tap, policy, bframes, pframes
}
func issueCoach(t *testing.T, app *App, row callRow) softphoneSession {
	t.Helper()
	w := coachTestRequest(app, phoneTestIdentity("boss"), "coach", row.ID, nil, true)
	if w.Code != 200 {
		t.Fatalf("coach %d %s", w.Code, w.Body)
	}
	var s softphoneSession
	if json.Unmarshal(w.Body.Bytes(), &s) != nil || s.SessionToken == "" {
		t.Fatal("invalid coach session")
	}
	return s
}
func coachCommand(t *testing.T, c net.Conn, kind string, generation uint32) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"type": kind, "generation": generation})
	if e := wsutil.WriteClientText(c, b); e != nil {
		t.Fatal(e)
	}
}
func coachEvent(t *testing.T, c net.Conn, kind string) map[string]any {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(4 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	for {
		data, op, e := wsutil.ReadServerData(c)
		if e != nil {
			t.Fatalf("waiting for %s: %v", kind, e)
		}
		if op != ws.OpText {
			continue
		}
		var v map[string]any
		if json.Unmarshal(data, &v) == nil && v["type"] == kind {
			return v
		}
	}
}
func coachCapture(generation, sequence uint32, clock float64) []byte {
	b := make([]byte, 984)
	binary.LittleEndian.PutUint32(b, coachCaptureMagic)
	binary.LittleEndian.PutUint32(b[4:], generation)
	binary.LittleEndian.PutUint32(b[8:], sequence)
	binary.LittleEndian.PutUint64(b[16:], math.Float64bits(clock))
	copy(b[24:], pcm16ToBytes(sinePCM(24000, 1700, 480)))
	return b
}
func wireAudio(t *testing.T, frames <-chan coachingWireFrame, magic uint32) []byte {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case f := <-frames:
			if f.op == ws.OpBinary && (magic == 0 || (len(f.data) >= 4 && binary.LittleEndian.Uint32(f.data) == magic)) {
				return f.data
			}
		case <-timer.C:
			t.Fatal("missing wire audio")
			return nil
		}
	}
}
func TestCoachingAuthorityTargetAndCredentialIsolation(t *testing.T) {
	app, row, h, _, policy, _, _ := coachingFixture(t)
	boss := phoneTestIdentity("boss")
	if w := coachTestRequest(app, boss, "coach", row.ID, nil, false); w.Code != 403 {
		t.Fatalf("scope bypass: %d", w.Code)
	}
	for _, id := range []string{"alice", "eve"} {
		if w := coachTestRequest(app, phoneTestIdentity(id), "coach", row.ID, nil, true); w.Code != 403 && w.Code != 404 {
			t.Fatalf("role bypass: %d", w.Code)
		}
	}
	session := issueCoach(t, app, row)
	if app.validPhoneMedia(&row, session.SessionToken) {
		t.Fatal("coach became adviser")
	}
	if w := phoneTestRequest(app, &boss, "POST", "/softphone/listen-renew/"+row.ID, map[string]any{"session_token": session.SessionToken}); w.Code != 403 {
		t.Fatal("passive scope renewed coach")
	}
	passive := listenerIssue(t, app, row.ID, &boss)
	if w := coachTestRequest(app, boss, "coach-renew", row.ID, map[string]any{"session_token": passive.SessionToken}, true); w.Code != 403 {
		t.Fatal("passive token upgraded")
	}
	different := row
	different.ProjectID = "other"
	if app.listenerSessionValid(&different, session.SessionToken) {
		t.Fatal("cross project credential")
	}
	h.mu.Lock()
	h.whisperBrowser = nil
	h.mu.Unlock()
	if w := coachTestRequest(app, boss, "coach", row.ID, nil, true); w.Code != 409 {
		t.Fatal("unsupported adviser accepted")
	}
	h.mu.Lock()
	h.whisperBrowser = h.browser
	h.mu.Unlock()
	policy.Users[3].Coach = false
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if app.listenerSessionValid(&row, session.SessionToken) {
		t.Fatal("revoked coaching valid")
	}
}
func TestCoachingWebSocketPrivateAudioAndPrimaryContinuity(t *testing.T) {
	app, row, h, tap, _, browser, carrier := coachingFixture(t)
	observer, _ := tap.add("observer", 4)
	before, _ := app.db().findCall(row.ID)
	session := issueCoach(t, app, row)
	server := listenerServer(t, app)
	c := listenerDial(t, server, session)
	defer c.Close()
	coachCommand(t, c, "coach.start", 1)
	coachEvent(t, c, "coach.started")
	if e := wsutil.WriteClientBinary(c, coachCapture(1, 0, 1000)); e != nil {
		t.Fatal(e)
	}
	whisper := wireAudio(t, browser, coachPlaybackMagic)
	if len(whisper) < 17 || len(whisper) > 176 || rmsPCM(ulawToPCM16(whisper[16:])) < 100 {
		t.Fatal("coaching inaudible")
	}
	select {
	case f := <-carrier:
		t.Fatalf("coaching leaked to carrier: %x", f.data)
	default:
	}
	if len(observer.audio) != 0 {
		t.Fatal("coaching leaked into call tap")
	}
	pcm := pcm16ToBytes(sinePCM(24000, 440, 480))
	h.toBrowser(ws.OpBinary, pcm)
	if got := wireAudio(t, browser, 0); !bytes.Equal(got, pcm) {
		t.Fatal("caller PCM changed")
	}
	h.forwardMicrophone(h.browserWriter(), pcm)
	if got := wireAudio(t, carrier, 0); !bytes.Equal(got, pcm) {
		t.Fatal("adviser PCM changed")
	}
	coachCommand(t, c, "coach.stop", 2)
	coachEvent(t, c, "coach.stopped")
	if e := wsutil.WriteClientBinary(c, coachCapture(1, 1, 1020)); e != nil {
		t.Fatal(e)
	}
	coachCommand(t, c, "coach.start", 1)
	time.Sleep(30 * time.Millisecond)
	h.mu.Lock()
	active := h.coach != nil
	h.mu.Unlock()
	if active {
		t.Fatal("late start revived coaching")
	}
	var ended int
	if e := app.db().db.QueryRow(`SELECT COUNT(*) FROM telephony_coaching_audit WHERE call_id=? AND ended_at<>''`, row.ID).Scan(&ended); e != nil || ended != 1 {
		t.Fatalf("audit missing %d %v", ended, e)
	}
	after, _ := app.db().findCall(row.ID)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if !bytes.Equal(b, a) {
		t.Fatal("coaching changed call status/media")
	}
}
func TestCoachingStopsForHoldTakeoverRevocationAndDeadman(t *testing.T) {
	for _, mode := range []string{"hold", "owner", "session", "browser", "peer", "revocation", "expiry", "hangup", "deadman"} {
		t.Run(mode, func(t *testing.T) {
			app, row, h, _, policy, _, _ := coachingFixture(t)
			session := issueCoach(t, app, row)
			server := listenerServer(t, app)
			c := listenerDial(t, server, session)
			defer c.Close()
			coachCommand(t, c, "coach.start", 1)
			coachEvent(t, c, "coach.started")
			switch mode {
			case "hold":
				h.setHeld(true)
			case "owner":
				p, _ := app.phonePrincipal(row.ProjectID, phoneTestIdentity("bob"))
				if e := app.setPhoneOwner(&row, p, "sales"); e != nil {
					t.Fatal(e)
				}
			case "session":
				p, _ := app.phonePrincipal(row.ProjectID, phoneTestIdentity("alice"))
				if _, e := app.issuePhoneSession(&row, p); e != nil {
					t.Fatal(e)
				}
			case "browser":
				w, _ := coachingSink(t)
				h.setBrowser(w)
			case "peer":
				h.clearPeer(h.peerWriter())
			case "revocation":
				policy.Users[3].Coach = false
				if w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
					t.Fatal(w.Body)
				}
			case "expiry":
				app.db().db.Exec(`UPDATE telephony_listener_sessions SET expires_at=0 WHERE token_hash=?`, phoneHash(session.SessionToken))
			case "hangup":
				app.db().updateStatus(row.ID, "completed", "")
				h.setCallState("inbound", "completed")
			}
			deadline := time.Now().Add(4 * time.Second)
			for {
				h.mu.Lock()
				active := h.coach != nil && h.coach.coaching.talking.Load()
				h.mu.Unlock()
				if !active {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("coaching did not stop")
				}
				time.Sleep(10 * time.Millisecond)
			}
			var ended int
			for {
				app.db().db.QueryRow(`SELECT COUNT(*) FROM telephony_coaching_audit WHERE call_id=? AND ended_at<>''`, row.ID).Scan(&ended)
				if ended == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("audit still open")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
func TestCoachingConcurrentTalkersAndQueuedInvalidation(t *testing.T) {
	app, row, h, tap, _, _, _ := coachingFixture(t)
	s := issueCoach(t, app, row)
	g, e := app.coachingGrant(&row, s.SessionToken)
	if e != nil {
		t.Fatal(e)
	}
	one, _ := tap.addWithCoaching("one", 4, g)
	two, _ := tap.addWithCoaching("two", 4, g)
	if !h.startCoach(one, 1) || h.startCoach(two, 1) {
		t.Fatal("concurrent talkers")
	}
	h.stopCoach(one)
	if !h.startCoach(two, 1) {
		t.Fatal("coach slot not released")
	}
	w := &websocketWriterPump{whisper: make(chan websocketWriteRequest, 3), requests: make(chan websocketWriteRequest, 64), done: make(chan struct{}), stop: make(chan struct{})}
	h.mu.Lock()
	h.browser = w
	h.whisperBrowser = w
	h.mu.Unlock()
	if !h.forwardCoach(two, 1, make([]byte, 160), mediaClockMS()) {
		t.Fatal("queue failed")
	}
	queued := <-w.whisper
	if !queued.valid() {
		t.Fatal("live queue invalid")
	}
	h.invalidateCoaching()
	if queued.valid() {
		t.Fatal("queued audio survived target change")
	}
}

func TestCoachingFloodCannotEvictCallerAndCallerWritesFirst(t *testing.T) {
	server, client := net.Pipe()
	p := &websocketWriterPump{conn: server, state: ws.StateServerSide, whisper: make(chan websocketWriteRequest, 3), requests: make(chan websocketWriteRequest, 64), audio: make(chan websocketWriteRequest, 128), done: make(chan struct{}), stop: make(chan struct{})}
	pcm := pcm16ToBytes(sinePCM(24000, 440, 480))
	p.QueueAudio(pcm)
	for n := 0; n < 1000; n++ {
		p.queueWhisper(make([]byte, 176), func() bool { return true })
	}
	if len(p.whisper) != 3 || p.whisperDropped.Load() != 997 || len(p.audio) != 1 || p.audioSnapshot().OverflowBytes != 0 {
		t.Fatal("coaching flood affected primary queue")
	}
	go p.run()
	defer func() { client.Close(); server.Close(); p.Stop() }()
	client.SetReadDeadline(time.Now().Add(time.Second))
	frame, op, e := wsutil.ReadServerData(client)
	if e != nil || op != ws.OpBinary || !bytes.Equal(frame, pcm) {
		t.Fatalf("caller lost priority: %v", e)
	}
}
func TestCoachingDiagnosticsPreserveSeparatePlaybackCounters(t *testing.T) {
	var d browserAudioDiagnostics
	if e := json.Unmarshal([]byte(`{"timing":{"playback":{"played_ms":500,"coaching":{"played_ms":60,"dropped_ms":20,"max_queue_ms":80}}}}`), &d); e != nil {
		t.Fatal(e)
	}
	if d.Timing.Playback.Coaching == nil || d.Timing.Playback.Coaching.PlayedMS != 60 || d.Timing.Playback.PlayedMS != 500 {
		t.Fatal("coaching counters lost or mixed")
	}
}
