package main

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func listenerReady(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, _, err := wsutil.ReadServerData(conn)
	if err != nil || !strings.Contains(string(data), "listener.ready") {
		t.Fatalf("listener handshake: %s %v", data, err)
	}
	_ = conn.SetReadDeadline(time.Time{})
}

func tapListener(t *testing.T, tap *callAudioTap, token string) *callListener {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		tap.mu.Lock()
		l := tap.listeners[phoneHash(token)]
		tap.mu.Unlock()
		if l != nil {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatal("listener not attached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Production 2026-11-16 (0.17.0): a listening session lost 1,921 and 1,891
// frames in 4 min 48 s. The once-a-second access check read the database in
// the same goroutine that drains the 120 ms listener queue.
func TestListenerSlowAccessCheckDoesNotStallAudio(t *testing.T) {
	app, row, tap, _ := listenerFixture(t)
	boss := phoneTestIdentity("boss")
	var checks atomic.Int32
	listenerCheckHook = func() { checks.Add(1); time.Sleep(600 * time.Millisecond) }
	t.Cleanup(func() { listenerCheckHook = nil })
	server := listenerServer(t, app)
	session := listenerIssue(t, app, row.ID, &boss)
	conn := listenerDial(t, server, session)
	listenerReady(t, conn)
	l := tapListener(t, tap, session.SessionToken)
	var received atomic.Int64
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, op, err := wsutil.ReadServerData(conn)
			if err != nil {
				return
			}
			if op == ws.OpBinary {
				received.Add(1)
			}
		}
	}()
	pcm := pcm16ToBytes(sinePCM(24000, 440, 480))
	published := 0
	ticker := time.NewTicker(20 * time.Millisecond)
	for start := time.Now(); time.Since(start) < 3200*time.Millisecond; {
		<-ticker.C
		tap.publish(0, pcm)
		tap.publish(1, pcm)
		published += 2
	}
	ticker.Stop()
	time.Sleep(100 * time.Millisecond)
	overflow := l.dropped[0].Load() + l.dropped[1].Load()
	sent := l.sent[0].Load() + l.sent[1].Load()
	unchecked := l.unchecked[0].Load() + l.unchecked[1].Load()
	t.Logf("checks=%d published=%d sent=%d received=%d overflow=%d stale=%d unchecked=%d max_check_ms=%d slow_checks=%d max_queue_ms=%d", checks.Load(), published, sent, received.Load(), overflow, l.stale[0].Load()+l.stale[1].Load(), unchecked, l.maxCheckMS.Load(), l.slowChecks.Load(), l.maxQueueMS.Load())
	if checks.Load() < 2 {
		t.Fatalf("only %d access checks overlapped the audio", checks.Load())
	}
	// Before the fix each 600 ms check lost about 48 frames.
	if overflow > listenerQueueFrames {
		t.Fatalf("slow access checks overflowed the listener queue: %d frames", overflow)
	}
	if unchecked != 0 {
		t.Fatalf("audio paused while checks were completing: %d frames", unchecked)
	}
	if sent < int64(published)*8/10 {
		t.Fatalf("sent %d of %d frames", sent, published)
	}
	if l.maxCheckMS.Load() < 600 || l.slowChecks.Load() < 2 {
		t.Fatalf("slow checks not recorded: max %d ms, %d slow", l.maxCheckMS.Load(), l.slowChecks.Load())
	}
	conn.Close()
	<-readerDone
}

// Moving the check off the audio path must not let a stuck check keep a
// listener hearing the call: audio pauses until a check starts again.
func TestListenerStuckAccessCheckPausesAudio(t *testing.T) {
	app, row, tap, _ := listenerFixture(t)
	boss := phoneTestIdentity("boss")
	release := make(chan struct{})
	var once sync.Once
	var entered atomic.Int32
	listenerCheckHook = func() {
		if entered.Add(1) == 1 {
			<-release
		}
	}
	t.Cleanup(func() { listenerCheckHook = nil })
	server := listenerServer(t, app)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	session := listenerIssue(t, app, row.ID, &boss)
	conn := listenerDial(t, server, session)
	listenerReady(t, conn)
	l := tapListener(t, tap, session.SessionToken)
	pcm := pcm16ToBytes(sinePCM(24000, 440, 480))
	tap.publish(0, pcm)
	readBinaryWithin(t, conn, time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for entered.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("access check never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Stand in for a check that has been stuck past the limit.
	l.checkStartedAt.Store(time.Now().Add(-listenerUncheckedLimit - time.Second).UnixMilli())
	for i := 0; i < 5; i++ {
		tap.publish(0, pcm)
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(time.Second)
	for l.unchecked[0].Load() < 5 {
		if time.Now().After(deadline) {
			t.Fatalf("unchecked frames %d, sent %d", l.unchecked[0].Load(), l.sent[0].Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, op, err := wsutil.ReadServerData(conn); err == nil && op == ws.OpBinary {
		t.Fatal("audio reached the listener while no access check had started for over the limit")
	}
	once.Do(func() { close(release) })
	// The next completed check restarts audio.
	resumed := make(chan struct{})
	go func() {
		defer close(resumed)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, op, err := wsutil.ReadServerData(conn)
			if err != nil || op == ws.OpBinary {
				return
			}
		}
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case <-resumed:
			// The server counts a frame only after its write returns, so the
			// browser can hold the frame a moment before the count moves.
			for counted := time.Now().Add(time.Second); l.sent[0].Load() < 2; time.Sleep(5 * time.Millisecond) {
				if time.Now().After(counted) {
					t.Fatal("audio did not resume after the check completed")
				}
			}
			return
		case <-timeout:
			t.Fatal("audio did not resume after the check completed")
		case <-ticker.C:
			tap.publish(0, pcm)
		}
	}
}

// 0.19.0 resource grants give numbers to a principal. The listen request saw
// them (cached policy), the per-second check and the policy-write sweep did
// not, so a supervisor admitted through a granted number was cut off.
func TestListenerKeepsAccessFromResourceGrantNumber(t *testing.T) {
	app, _, _, _ := listenerFixture(t)
	boss := phoneTestIdentity("boss")
	number := "+13502239999"
	principal, _ := json.Marshal(boss)
	if _, err := app.db().db.Exec(`INSERT INTO telephony_resource_grants(project_id,id,principal_json,resource_kind,resource_id,actions_json,enabled,revision,administrator_install_id,created_at,updated_at) VALUES('project-a','boss-number',?,'outbound_number',?,'["call.dial"]',1,'r1',7,'now','now')`, string(principal), number); err != nil {
		t.Fatal(err)
	}
	row := callRow{ID: "granted-number", ThreadID: "test-granted-number", Direction: "outbound", CarrierSlug: "twilio", Status: "in-progress", ProjectID: "project-a", PeerKind: peerKindHuman, PeerToken: "peer-granted-number", CallbackSecret: "cb-granted-number", ToNumber: "+13334445555", FromNumber: number, PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	if err := app.db().updateMediaStatus(row.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	row.MediaStatus = "connected"
	tap := app.listeners.openBridge(row.ID)
	t.Cleanup(func() { app.listeners.closeBridge(row.ID, tap) })
	session := listenerIssue(t, app, row.ID, &boss)
	if reason, _ := app.listenerSessionCheck(&row, session.SessionToken); reason != "" {
		t.Fatalf("periodic check refused a listener the request admitted: %s", reason)
	}
	app.revokeCallListeners("project-a")
	if reason, _ := app.listenerSessionCheck(&row, session.SessionToken); reason != "" {
		t.Fatalf("policy-write sweep removed a listener the request admitted: %s", reason)
	}
	if _, err := app.db().db.Exec(`UPDATE telephony_resource_grants SET enabled=0 WHERE id='boss-number'`); err != nil {
		t.Fatal(err)
	}
	if reason, _ := app.listenerSessionCheck(&row, session.SessionToken); reason != "access_revoked" {
		t.Fatalf("removed grant still allowed listening: %q", reason)
	}
}
