package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func eventsOf(e *engine, pid, topic string) []map[string]any {
	res := e.events.fetch(pid, e.events.boot, 0, 1000)
	out := []map[string]any{}
	for _, ev := range res["events"].([]journalEvent) {
		if ev.Topic == topic {
			out = append(out, ev.Data)
		}
	}
	return out
}

func TestStopCancelsRunningActionWithoutWaitingForLock(t *testing.T) {
	e, b := fixture(t)
	s := openFixture(t, e, "p")
	entered := make(chan struct{})
	var once sync.Once
	b.action = func(ctx context.Context) error {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return ctx.Err()
	}
	actionDone := make(chan error, 1)
	go func() {
		_, err := e.Execute(context.Background(), "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "swipe", "x": 0.5, "y": 0.8, "to_x": 0.5, "to_y": 0.2})
		actionDone <- err
	}()
	<-entered
	started := time.Now()
	v := execute(t, e, "p", "mobile_session", map[string]any{"action": "stop", "session_id": s.ID, "reason": "operator pressed stop"})
	if d := time.Since(started); d > 500*time.Millisecond {
		t.Fatalf("stop took %v", d)
	}
	if v.(map[string]any)["count"] != 1 {
		t.Fatalf("stop result: %v", v)
	}
	select {
	case err := <-actionDone:
		if err == nil {
			t.Fatal("cancelled action reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("running action was not cancelled")
	}
	got, _ := e.session("p", s.ID)
	if got.Status != "stopped" || got.EndReason != "stopped" || got.StopReason != "operator pressed stop" || got.EndedAt == nil || got.StoppedBy == "" {
		t.Fatalf("stopped session: %+v", got)
	}
	before := b.actions
	if _, err := e.Execute(context.Background(), "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "tap", "x": 0.5, "y": 0.5}); err == nil {
		t.Fatal("action ran on a stopped session")
	}
	if b.actions != before {
		t.Fatal("backend reached after stop")
	}
	// Stopping again is harmless and the phone can be leased again.
	execute(t, e, "p", "mobile_session", map[string]any{"action": "stop", "session_id": s.ID})
	openFixture(t, e, "p")
	ended := eventsOf(e, "p", "mobile.session.ended")
	if len(ended) != 1 || ended[0]["end_reason"] != "stopped" || ended[0]["stop_reason"] != "operator pressed stop" {
		t.Fatalf("ended events: %v", ended)
	}
}

func TestStopByDeviceAndAllStaysInProject(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	if v := execute(t, e, "other", "mobile_session", map[string]any{"action": "stop", "all": true}); v.(map[string]any)["count"] != 0 {
		t.Fatalf("another project stopped this session: %v", v)
	}
	if v := execute(t, e, "other", "mobile_session", map[string]any{"action": "stop", "device_id": "android:phone"}); v.(map[string]any)["count"] != 0 {
		t.Fatalf("another project stopped by device: %v", v)
	}
	if v := execute(t, e, "p", "mobile_session", map[string]any{"action": "stop", "device_id": "android:phone"}); v.(map[string]any)["count"] != 1 {
		t.Fatalf("stop by device: %v", v)
	}
	got, _ := e.session("p", s.ID)
	if got.Status != "stopped" {
		t.Fatalf("status %s", got.Status)
	}
	if _, err := e.Execute(context.Background(), "p", "mobile_session", map[string]any{"action": "stop"}); errorCode(err) != "invalid_argument" {
		t.Fatalf("stop without target: %v", err)
	}
}

func TestSessionEventsCarryCallerRefAndEndReason(t *testing.T) {
	e, _ := fixture(t)
	v := execute(t, e, "p", "mobile_session", map[string]any{"action": "open", "device_id": "android:phone", "caller_ref": "task-42"})
	s := v.(map[string]any)["session"].(Session)
	if s.CallerRef != "task-42" {
		t.Fatalf("caller_ref: %+v", s)
	}
	execute(t, e, "p", "mobile_session", map[string]any{"action": "close", "session_id": s.ID})
	started := eventsOf(e, "p", "mobile.session.started")
	ended := eventsOf(e, "p", "mobile.session.ended")
	if len(started) != 1 || started[0]["caller_ref"] != "task-42" {
		t.Fatalf("started: %v", started)
	}
	if len(ended) != 1 || ended[0]["end_reason"] != "closed" || ended[0]["duration_ms"] == nil {
		t.Fatalf("ended: %v", ended)
	}
	if len(eventsOf(e, "other", "mobile.session.started")) != 0 {
		t.Fatal("events leaked to another project")
	}
	if _, err := e.Execute(context.Background(), "p", "mobile_session", map[string]any{"action": "open", "device_id": "android:phone", "caller_ref": strings.Repeat("x", 201)}); errorCode(err) != "invalid_argument" {
		t.Fatalf("long caller_ref: %v", err)
	}
}

func TestActionEventsRedactTypedText(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	execute(t, e, "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "text", "text": "hello private words"})
	_, _ = e.Execute(context.Background(), "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "secure_text", "password": "hunter2"})
	execute(t, e, "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "navigate", "url": "https://example.com/reset?token=abc"})
	acts := eventsOf(e, "p", "mobile.action.performed")
	if len(acts) != 3 {
		t.Fatalf("action events: %v", acts)
	}
	all := jsonText(acts)
	for _, leak := range []string{"hello private words", "hunter2", "token=abc", "/reset"} {
		if strings.Contains(all, leak) {
			t.Fatalf("event leaked %q: %s", leak, all)
		}
	}
	if acts[0]["args"].(map[string]any)["text_length"] != 19 || acts[2]["args"].(map[string]any)["url_host"] != "example.com" {
		t.Fatalf("summaries: %s", all)
	}
}

func TestEventJournalCursorAndRestart(t *testing.T) {
	j := newEventJournal()
	now := j.fetch("p", "", -1, 10)
	if len(now["events"].([]journalEvent)) != 0 {
		t.Fatal("since<0 returned history")
	}
	for i := 0; i < 5; i++ {
		j.add("p", "t", map[string]any{})
	}
	j.add("q", "t", map[string]any{})
	j.add("", "mobile.device.online", map[string]any{})
	first := j.fetch("p", j.boot, now["last_seq"].(int64), 3)
	if n := len(first["events"].([]journalEvent)); n != 3 {
		t.Fatalf("page: %d", n)
	}
	rest := j.fetch("p", j.boot, first["last_seq"].(int64), 10)
	evs := rest["events"].([]journalEvent)
	if len(evs) != 3 || evs[2].Topic != "mobile.device.online" || rest["last_seq"].(int64) != 7 {
		t.Fatalf("rest: %v", rest)
	}
	// A cursor from an earlier boot starts over rather than skipping events.
	if n := len(j.fetch("p", "boot_old", 99, 100)["events"].([]journalEvent)); n != 6 {
		t.Fatalf("restart replay: %d", n)
	}
}

// The events worker passes its cursor back through Execute; a cursor that
// does not round-trip replays the whole journal on every tick.
func TestEventsFetchCursorAdvancesThroughExecute(t *testing.T) {
	e, _ := fixture(t)
	openFixture(t, e, "p")
	fetch := func(boot string, since any) (string, int64, int) {
		v := execute(t, e, "p", "mobile_events_fetch", map[string]any{"boot": boot, "since": since, "limit": 500})
		m := v.(map[string]any)
		return m["boot"].(string), m["last_seq"].(int64), len(m["events"].([]journalEvent))
	}
	boot, last, n := fetch("", float64(0))
	if n == 0 {
		t.Fatal("no events on first fetch")
	}
	for _, since := range []any{last, float64(last), int(last)} {
		if _, _, again := fetch(boot, since); again != 0 {
			t.Fatalf("cursor %T replayed %d events", since, again)
		}
	}
}
