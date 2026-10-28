package main

import (
	"testing"
	"time"
)

// The durable path must not drop on lock contention or behind a full observer
// queue: recording.ready and routing traces are promised to persist.
func TestReviewH1DurableNotificationSurvivesContentionAndFullQueue(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, app, "durable-notify", "telnyx")
	q := &app.notifications
	// Fill the observer queue so observeCallNotification drops.
	for i := 0; i < callNotificationObserverLimit; i++ {
		app.observeCallNotification(row.ProjectID, "missing-call", "telephony.audio.degraded", nil)
	}
	dropped := q.dropped.Load()
	app.observeCallNotification(row.ProjectID, row.ID, "telephony.audio.degraded", nil)
	if q.dropped.Load() != dropped+1 {
		t.Fatal("observer did not drop on a full queue")
	}
	// Hold the queue lock, as a concurrent observer would.
	q.mu.Lock()
	done := make(chan struct{})
	go func() {
		app.saveCallNotification(row.ProjectID, row.ID, "recording.ready", map[string]any{"id": "rec-1"})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	q.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("durable save did not return")
	}
	var n int
	if err := app.db().db.QueryRow(`SELECT count(*) FROM call_events WHERE call_id=? AND topic='recording.ready'`, row.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("recording.ready persisted %d times, err=%v", n, err)
	}
	if q.dropped.Load() != dropped+1 {
		t.Fatal("durable event counted as dropped")
	}
}
