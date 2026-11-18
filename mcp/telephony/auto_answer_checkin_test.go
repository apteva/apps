package main

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

type checkInLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *checkInLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *checkInLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// captureCheckInLog collects log output for the rest of the test.
func captureCheckInLog(t *testing.T) *checkInLog {
	t.Helper()
	out := &checkInLog{}
	previous := log.Writer()
	log.SetOutput(out)
	t.Cleanup(func() { log.SetOutput(previous) })
	return out
}

func checkIn(t *testing.T, a *App, id *phoneIdentity, session string, takeover bool) bool {
	t.Helper()
	w := phoneTestRequest(a, id, "POST", "/softphone/auto-answer-session", map[string]any{"session_id": session, "available": true, "takeover": takeover})
	if w.Code != 200 {
		t.Fatalf("check-in: %d %s", w.Code, w.Body)
	}
	return strings.Contains(w.Body.String(), `"designated":true`)
}

// A check-in never waits on the install-wide auto-answer lock, which offers,
// policy reads and automatic pick-ups hold. The browser gives it 5 s.
func TestAutoAnswerCheckInDoesNotWaitForLock(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	a.autoAnswerMu.Lock()
	done := make(chan bool, 1)
	go func() { done <- checkIn(t, a, &alice, autoAnswerTestSession, false) }()
	select {
	case designated := <-done:
		a.autoAnswerMu.Unlock()
		if !designated {
			t.Fatal("check-in was not designated")
		}
	case <-time.After(3 * time.Second):
		a.autoAnswerMu.Unlock()
		<-done
		t.Fatal("check-in waited for the auto-answer lock")
	}
	// Releasing does not wait either.
	a.autoAnswerMu.Lock()
	defer a.autoAnswerMu.Unlock()
	released := make(chan int, 1)
	go func() {
		released <- phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-session", map[string]any{"session_id": autoAnswerTestSession, "available": false}).Code
	}()
	select {
	case code := <-released:
		if code != 200 {
			t.Fatalf("release: %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("release waited for the auto-answer lock")
	}
}

// One statement renews, replaces a lapsed claim, or reports another browser.
// The check-in no longer deletes other advisers' lapsed claims; housekeeping does.
func TestAutoAnswerCheckInReplacesLapsedClaimAndLogsRefusals(t *testing.T) {
	ctx := softphoneTestCtx(t)
	a := &App{}
	phoneTestPolicy(t, a)
	logs := captureCheckInLog(t)
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	const second = "second-browser-session-alice"
	if !checkIn(t, a, &alice, autoAnswerTestSession, false) {
		t.Fatal("first browser not designated")
	}
	if checkIn(t, a, &alice, second, false) {
		t.Fatal("second browser took a live claim without takeover")
	}
	if line := logs.String(); !strings.Contains(line, "auto_answer checkin not_designated") || !strings.Contains(line, "elapsed_ms=") || strings.Contains(line, second) || strings.Contains(line, "alice") {
		t.Fatalf("refusal log line missing or not hashed: %q", line)
	}
	if !checkIn(t, a, &alice, autoAnswerTestSession, false) {
		t.Fatal("first browser lost its own claim")
	}
	// Bob's claim lapses; nobody renews it.
	if !checkIn(t, a, &bob, "automatic-browser-session-bob", false) {
		t.Fatal("bob not designated")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE telephony_auto_answer_sessions SET expires_at=1`); err != nil {
		t.Fatal(err)
	}
	if !checkIn(t, a, &alice, second, false) {
		t.Fatal("a lapsed claim blocked another browser")
	}
	var rows int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM telephony_auto_answer_sessions WHERE expires_at<=?`, time.Now().UnixMilli()).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("check-in deleted another adviser's lapsed claim: lapsed=%d err=%v", rows, err)
	}
	if err := a.runHousekeepingAt(context.Background(), ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var session string
	if err := ctx.AppDB().QueryRow(`SELECT group_concat(session_id) FROM telephony_auto_answer_sessions`).Scan(&session); err != nil || session != second {
		t.Fatalf("housekeeping left %q err=%v", session, err)
	}
}

// A check-in the database cannot serve answers 503 and says which statement failed.
func TestAutoAnswerCheckInFailureIsLogged(t *testing.T) {
	ctx := softphoneTestCtx(t)
	a := &App{}
	phoneTestPolicy(t, a)
	logs := captureCheckInLog(t)
	alice := phoneTestIdentity("alice")
	if _, err := ctx.AppDB().Exec(`DROP TABLE telephony_auto_answer_sessions`); err != nil {
		t.Fatal(err)
	}
	w := phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-session", map[string]any{"session_id": autoAnswerTestSession, "available": true})
	if w.Code != 503 {
		t.Fatalf("storage failure hidden: %d %s", w.Code, w.Body)
	}
	if line := logs.String(); !strings.Contains(line, "auto_answer checkin failed") || !strings.Contains(line, "statement=claim") || !strings.Contains(line, "err=") {
		t.Fatalf("failure not logged: %q", line)
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-session", map[string]any{"session_id": autoAnswerTestSession, "available": false})
	if w.Code != 503 || !strings.Contains(logs.String(), "statement=release_session") {
		t.Fatalf("release failure: %d %q", w.Code, logs.String())
	}
}
