package main

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
)

func reviewH2StartCoach(t *testing.T) (*App, callRow, *softphoneHub, <-chan coachingWireFrame, net.Conn, *callListener) {
	t.Helper()
	app, row, h, tap, _, browser, _ := coachingFixture(t)
	session := issueCoach(t, app, row)
	c := listenerDial(t, listenerServer(t, app), session)
	t.Cleanup(func() { c.Close() })
	coachCommand(t, c, "coach.start", 1)
	coachEvent(t, c, "coach.started")
	tap.mu.Lock()
	l := tap.listeners[phoneHash(session.SessionToken)]
	tap.mu.Unlock()
	if l == nil {
		t.Fatal("coach listener missing")
	}
	return app, row, h, browser, c, l
}

func reviewH2AwaitWhisper(frames <-chan coachingWireFrame, wait time.Duration) bool {
	deadline := time.After(wait)
	for {
		select {
		case f := <-frames:
			if len(f.data) >= 4 && binary.LittleEndian.Uint32(f.data) == coachPlaybackMagic {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// Coach media must not serialize on the call's claim lock (answer/attach/takeover).
func TestReviewH2CoachFrameDoesNotWaitForClaimLock(t *testing.T) {
	app, row, _, browser, c, _ := reviewH2StartCoach(t)
	unlock := app.softphones.lockClaim(row.ID)
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()
	if e := wsutil.WriteClientBinary(c, coachCapture(1, 0, mediaClockMS())); e != nil {
		t.Fatal(e)
	}
	if !reviewH2AwaitWhisper(browser, 500*time.Millisecond) {
		unlock()
		released = true
		t.Fatal("coach frame blocked behind the call claim lock")
	}
}

// A transient DB failure on the frame path is not a target change.
func TestReviewH2CoachFrameLookupErrorIsTemporary(t *testing.T) {
	app, _, _, browser, c, l := reviewH2StartCoach(t)
	if _, e := app.db().db.Exec(`ALTER TABLE telephony_call_owners RENAME TO telephony_call_owners_h2`); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		app.db().db.Exec(`ALTER TABLE telephony_call_owners_h2 RENAME TO telephony_call_owners`)
	})
	if e := wsutil.WriteClientBinary(c, coachCapture(1, 0, mediaClockMS())); e != nil {
		t.Fatal(e)
	}
	reviewH2AwaitWhisper(browser, 300*time.Millisecond)
	// Cover at least one supervisor tick with the outage in place.
	time.Sleep(1200 * time.Millisecond)
	if reason := l.closeReason(); reason != "" {
		t.Fatalf("coaching session ended on a lookup error: %s", reason)
	}
}

// The cached authorization still expires: a DB-only target change (no
// in-memory epoch bump) stops coach audio within about one supervisor tick.
func TestReviewH2CoachStopsSoonAfterDBTargetChange(t *testing.T) {
	app, row, h, browser, c, l := reviewH2StartCoach(t)
	p, _ := app.phonePrincipal(row.ProjectID, phoneTestIdentity("bob"))
	if _, e := app.db().db.Exec(`UPDATE telephony_call_owners SET principal=? WHERE call_id=?`, p.Identity.key(), row.ID); e != nil {
		t.Fatal(e)
	}
	changed := time.Now()
	for seq := uint32(0); time.Since(changed) < 2500*time.Millisecond; seq++ {
		_ = wsutil.WriteClientBinary(c, coachCapture(1, seq, mediaClockMS()))
		time.Sleep(20 * time.Millisecond)
		if l.closeReason() != "" {
			break
		}
	}
	if l.closeReason() != "coach_target_changed" {
		t.Fatalf("coaching not stopped after target change: %q", l.closeReason())
	}
	if time.Since(changed) > 2*time.Second {
		t.Fatalf("coach audio kept flowing %v after the target changed", time.Since(changed))
	}
	for len(browser) > 0 {
		<-browser
	}
	h.mu.Lock()
	active := h.coach == l
	h.mu.Unlock()
	if active || app.forwardCoachFrame(row.ID, l, 1, make([]byte, 160), mediaClockMS()) {
		t.Fatal("closed coach can still reach the adviser")
	}
}

func TestReviewH2CoachingBrowserEpochMissingHub(t *testing.T) {
	app := &App{}
	if _, ok := app.coachingBrowserEpoch("no-such-call"); ok {
		t.Fatal("missing adviser hub reported an epoch")
	}
}
