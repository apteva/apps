package main

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Events are notifications, not records: the durable record is Mobile's own
// tables (mobile_sessions + mobile_session_meta). The engine keeps a bounded
// in-memory journal; the sidecar drains it and publishes on the app bus. The
// same path serves a local engine and a remote runner.

const journalSize = 4000

type journalEvent struct {
	Seq       int64          `json:"seq"`
	ProjectID string         `json:"-"` // "" = every project (device events)
	Topic     string         `json:"topic"`
	Data      map[string]any `json:"data"`
}

type eventJournal struct {
	mu     sync.Mutex
	boot   string
	seq    int64
	events []journalEvent
}

func newEventJournal() *eventJournal { return &eventJournal{boot: id("boot_")} }

func (j *eventJournal) add(pid, topic string, data map[string]any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	data["event_id"] = j.boot + "-" + itoa(j.seq)
	data["occurred_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	j.events = append(j.events, journalEvent{Seq: j.seq, ProjectID: pid, Topic: topic, Data: data})
	if len(j.events) > journalSize {
		j.events = append([]journalEvent(nil), j.events[len(j.events)-journalSize:]...)
	}
}

// fetch returns this project's events after since. A different boot means
// the runner restarted: the caller's cursor no longer applies. since<0
// starts from now.
func (j *eventJournal) fetch(pid, boot string, since int64, limit int) map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	if since < 0 {
		return map[string]any{"boot": j.boot, "last_seq": j.seq, "events": []journalEvent{}}
	}
	if boot != j.boot {
		since = 0
	}
	out := []journalEvent{}
	last := since
	for _, ev := range j.events {
		if ev.Seq <= since {
			continue
		}
		if len(out) >= limit {
			break
		}
		last = ev.Seq
		if ev.ProjectID == "" || ev.ProjectID == pid {
			out = append(out, ev)
		}
	}
	if len(out) < limit {
		last = j.seq
	}
	return map[string]any{"boot": j.boot, "last_seq": last, "events": out}
}

func itoa(n int64) string {
	b := [20]byte{}
	i := len(b)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// sessionTools are the calls reported as mobile.action.performed.
var sessionTools = map[string]bool{"mobile_action": true, "mobile_app": true, "mobile_observe": true, "mobile_power": true, "mobile_device": true}

// recordAction journals a session tool call with a redacted argument summary.
func (e *engine) recordAction(pid, tool string, args map[string]any, started time.Time, err error) {
	if !sessionTools[tool] {
		return
	}
	sid := str(args, "session_id")
	if sid == "" {
		return
	}
	s, getErr := e.session(pid, sid)
	if getErr != nil {
		return
	}
	result := "ok"
	if err != nil {
		result = errorCode(err)
	}
	e.events.add(pid, "mobile.action.performed", map[string]any{
		"session_id": s.ID, "device_id": s.DeviceID, "caller_ref": s.CallerRef,
		"tool": tool, "action": str(args, "action"), "args": summarizeArgs(args),
		"result": result, "duration_ms": time.Since(started).Milliseconds(),
	})
}

// summarizeArgs keeps what an audit needs and nothing a screen or a person
// typed: text becomes its length, secrets become [secret], screens and
// elements are never included. Coordinates are kept.
func summarizeArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"x", "y", "to_x", "to_y", "key", "keycode", "duration_ms", "timeout_ms", "using", "app_id", "source", "quality", "audio", "enabled", "image", "replace", "mode", "clear_downloads"} {
		if v, ok := args[k]; ok {
			out[k] = v
		}
	}
	if str(args, "action") == "secure_text" {
		out["input"] = "[secret]"
	} else if t, ok := args["text"].(string); ok {
		out["text_length"] = len([]rune(t))
	}
	if _, ok := args["password"]; ok {
		out["input"] = "[secret]"
	}
	if v := str(args, "value"); v != "" {
		out["value_length"] = len([]rune(v))
	}
	if raw := str(args, "url"); raw != "" {
		if u, err := url.Parse(raw); err == nil {
			out["url_host"] = u.Host
		}
	}
	if p, ok := args["path"].([]any); ok {
		out["path_points"] = len(p)
	}
	return out
}

// --- sidecar side ------------------------------------------------------------

type eventCursor struct {
	mu      sync.Mutex
	boot    string
	seq     int64
	started bool
}

// eventsWorker drains the engine journal onto the app bus. Action events are
// sent only when action_events=all.
func (a *App) eventsWorker(ctx context.Context, app *sdk.AppCtx) error {
	if a.executor == nil {
		return nil
	}
	a.cursor.mu.Lock()
	defer a.cursor.mu.Unlock()
	since := a.cursor.seq
	if !a.cursor.started && a.local == nil {
		since = -1 // a remote runner's backlog predates this sidecar
	}
	v, err := a.executor.Execute(ctx, a.project, "mobile_events_fetch", map[string]any{"boot": a.cursor.boot, "since": float64(since), "limit": 500})
	if err != nil {
		return nil // runner unreachable; try again next tick
	}
	var out struct {
		Boot    string `json:"boot"`
		LastSeq int64  `json:"last_seq"`
		Events  []struct {
			Topic string         `json:"topic"`
			Data  map[string]any `json:"data"`
		} `json:"events"`
	}
	if err = decodeMap(v, &out); err != nil {
		return err
	}
	actions := strings.EqualFold(app.Config().Get("action_events"), "all")
	for _, ev := range out.Events {
		if ev.Topic == "mobile.action.performed" && !actions {
			continue
		}
		app.EmitWithProject(ev.Topic, a.project, ev.Data)
	}
	a.cursor.boot, a.cursor.seq, a.cursor.started = out.Boot, out.LastSeq, true
	return nil
}
