package main

// Tier 1 — every MCP tool handler exercised against an in-memory
// SQLite. Three groups: UNIT (handler logic), HTTP (in-process
// route dispatch), MANIFEST (contract checks).
//
// Tier 2 (real binary via tk.SpawnSidecar) lives in
// integration_test.go behind //go:build integration.
// Tier 3 (live agent + LLM) lives in scenarios/*.yaml.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── UNIT ────────────────────────────────────────────────────────

func TestUnit_CalendarsCreateAndList(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	c1, err := app.toolCalendarsCreate(ctx, map[string]any{"name": "Personal", "color": "#22c55e", "kind": "personal"})
	if err != nil {
		t.Fatal(err)
	}
	cal := c1.(Calendar)
	if cal.Name != "Personal" || cal.Color != "#22c55e" || cal.Kind != "personal" {
		t.Errorf("created malformed: %+v", cal)
	}
	out, _ := app.toolCalendarsList(ctx, map[string]any{})
	cs := out.(map[string]any)["calendars"].([]Calendar)
	if len(cs) != 1 || cs[0].Name != "Personal" {
		t.Errorf("list returned %+v", cs)
	}
}

func TestUnit_CalendarsCreate_RequiresName(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	if _, err := app.toolCalendarsCreate(ctx, map[string]any{}); err == nil {
		t.Error("expected error for missing name")
	}
}

func TestUnit_CalendarsUpdateAndDisable(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	c, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "Work"})
	id := c.(Calendar).ID
	_, err := app.toolCalendarsUpdate(ctx, map[string]any{"id": id, "enabled": false})
	if err != nil {
		t.Fatal(err)
	}
	// calendars_list always returns all rows; the row carries
	// `enabled=false` so the caller can filter client-side.
	out, _ := app.toolCalendarsList(ctx, map[string]any{})
	cs := out.(map[string]any)["calendars"].([]Calendar)
	if len(cs) != 1 || cs[0].Enabled {
		t.Errorf("expected 1 disabled calendar, got %+v", cs)
	}
}

func TestUnit_CalendarsUpdate_RefusesNoOp(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	c, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "Work"})
	id := c.(Calendar).ID
	// Empty update — should error so the agent doesn't loop on
	// "I tried to set a field but it didn't take" when the field
	// isn't a real one.
	if _, err := app.toolCalendarsUpdate(ctx, map[string]any{"id": id}); err == nil {
		t.Error("expected error on empty calendars_update")
	}
	if _, err := app.toolCalendarsUpdate(ctx, map[string]any{
		"id":          id,
		"description": "ignored — not a supported field",
	}); err == nil {
		t.Error("expected error when only unsupported fields are passed")
	}
}

func TestUnit_EventsUpdate_RefusesNoOp(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "P"})
	calID := cal.(Calendar).ID
	ev, _ := app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "x",
		"start_at": "2026-05-04T10:00:00Z",
		"end_at":   "2026-05-04T11:00:00Z",
	})
	if _, err := app.toolEventsUpdate(ctx, map[string]any{
		"event_id": ev.(Event).ID,
		"scope":    "all",
	}); err == nil {
		t.Error("expected error on empty events_update")
	}
}

func TestUnit_EventCreateAndList_OneOff(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "P"})
	calID := cal.(Calendar).ID
	if _, err := app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "Coffee with Anna",
		"start_at": "2026-05-04T10:00:00Z",
		"end_at":   "2026-05-04T10:30:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolEventsList(ctx, map[string]any{
		"from": "2026-05-04T00:00:00Z", "to": "2026-05-05T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	occ := out.(map[string]any)["events"].([]Occurrence)
	if len(occ) != 1 || occ[0].Title != "Coffee with Anna" {
		t.Errorf("list returned %+v", occ)
	}
	if occ[0].IsRecurring {
		t.Error("one-off shouldn't be marked recurring")
	}
}

func TestUnit_EventCreate_ValidatesTimes(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "P"})
	calID := cal.(Calendar).ID
	_, err := app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "x",
		"start_at": "not-a-date", "end_at": "2026-05-04T10:30:00Z",
	})
	if err == nil {
		t.Error("expected error for invalid start_at")
	}
}

func TestUnit_RecurrenceWeeklyByDay_Expands(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "P"})
	calID := cal.(Calendar).ID
	// Event starts on a Monday morning 2026-05-04, recurs every
	// week on Mon + Wed for 6 occurrences.
	if _, err := app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "Standup",
		"start_at": "2026-05-04T09:00:00Z", // Monday
		"end_at":   "2026-05-04T09:15:00Z",
		"rrule":    "FREQ=WEEKLY;BYDAY=MO,WE;COUNT=6",
	}); err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolEventsList(ctx, map[string]any{
		"from": "2026-05-04T00:00:00Z",
		"to":   "2026-06-01T00:00:00Z",
	})
	occ := out.(map[string]any)["events"].([]Occurrence)
	if len(occ) != 6 {
		t.Fatalf("expected 6 occurrences, got %d:\n%+v", len(occ), occ)
	}
	// Spot-check weekday alternation.
	for _, o := range occ {
		s, _ := time.Parse(time.RFC3339, o.StartAt)
		if s.Weekday() != time.Monday && s.Weekday() != time.Wednesday {
			t.Errorf("unexpected weekday in %s", o.StartAt)
		}
		if !o.IsRecurring {
			t.Errorf("recurring occurrence should be marked: %+v", o)
		}
	}
}

func TestUnit_RecurrenceDailyUntil_Expands(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "P"})
	calID := cal.(Calendar).ID
	_, err := app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "Morning meditation",
		"start_at": "2026-05-04T07:00:00Z",
		"end_at":   "2026-05-04T07:15:00Z",
		"rrule":    "FREQ=DAILY;UNTIL=20260510T070000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolEventsList(ctx, map[string]any{
		"from": "2026-05-04T00:00:00Z", "to": "2026-05-15T00:00:00Z",
	})
	occ := out.(map[string]any)["events"].([]Occurrence)
	// May 4-10 inclusive = 7 days.
	if len(occ) != 7 {
		t.Errorf("expected 7 daily occurrences, got %d", len(occ))
	}
}

func TestUnit_EventUpdate_ScopeThis_CreatesChild(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "P"})
	calID := cal.(Calendar).ID
	master, _ := app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "Standup",
		"start_at": "2026-05-04T09:00:00Z",
		"end_at":   "2026-05-04T09:15:00Z",
		"rrule":    "FREQ=DAILY;COUNT=5",
	})
	masterID := master.(Event).ID
	// Override day 2 (May 5) with a different title + later time.
	out, err := app.toolEventsUpdate(ctx, map[string]any{
		"event_id":            masterID,
		"scope":               "this",
		"occurrence_start_at": "2026-05-05T09:00:00Z",
		"title":               "Standup (skipped morning, did 11am)",
		"start_at":            "2026-05-05T11:00:00Z",
		"end_at":              "2026-05-05T11:15:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	child := out.(Event)
	if child.ParentEventID != masterID {
		t.Errorf("child parent_event_id=%d, want %d", child.ParentEventID, masterID)
	}
	// List occurrences — should see 4 from master + 1 child = 5 total,
	// with the May-5 morning occurrence skipped (in master's exdate)
	// and the child's 11am occurrence in its place.
	occs, _ := app.toolEventsList(ctx, map[string]any{
		"from": "2026-05-04T00:00:00Z", "to": "2026-05-10T00:00:00Z",
	})
	es := occs.(map[string]any)["events"].([]Occurrence)
	if len(es) != 5 {
		t.Errorf("expected 5 occurrences after override, got %d:\n%+v", len(es), es)
	}
	var sawOverride bool
	for _, e := range es {
		if e.Title == "Standup (skipped morning, did 11am)" {
			sawOverride = true
		}
	}
	if !sawOverride {
		t.Error("expected the overridden occurrence to appear")
	}
}

func TestUnit_EventDelete_ScopeThis_AddsToExdate(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "P"})
	calID := cal.(Calendar).ID
	master, _ := app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "Daily",
		"start_at": "2026-05-04T09:00:00Z",
		"end_at":   "2026-05-04T09:15:00Z",
		"rrule":    "FREQ=DAILY;COUNT=5",
	})
	if _, err := app.toolEventsDelete(ctx, map[string]any{
		"event_id":            master.(Event).ID,
		"scope":               "this",
		"occurrence_start_at": "2026-05-05T09:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	occs, _ := app.toolEventsList(ctx, map[string]any{
		"from": "2026-05-04T00:00:00Z", "to": "2026-05-10T00:00:00Z",
	})
	es := occs.(map[string]any)["events"].([]Occurrence)
	if len(es) != 4 {
		t.Errorf("expected 4 (5 - 1 skipped), got %d", len(es))
	}
}

func TestUnit_FindSlot_HonoursWorkingHoursAndBusy(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "Work"})
	calID := cal.(Calendar).ID
	// Block 10:00-11:00 on Monday, May 4 2026.
	app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "Existing meeting",
		"start_at": "2026-05-04T10:00:00Z",
		"end_at":   "2026-05-04T11:00:00Z",
	})
	out, err := app.toolEventsFindSlot(ctx, map[string]any{
		"duration_minutes": 30,
		"window_start":     "2026-05-04T09:00:00Z",
		"window_end":       "2026-05-04T18:00:00Z",
		"working_hours": map[string]any{
			"mon": map[string]any{"start": "09:00", "end": "18:00"},
		},
		"limit": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	slots := out.(map[string]any)["slots"].([]map[string]string)
	if len(slots) == 0 {
		t.Fatal("no slots returned")
	}
	// First slot should be 09:00 (before the busy block).
	if slots[0]["start"] != "2026-05-04T09:00:00Z" {
		t.Errorf("first slot = %v, want 09:00", slots[0])
	}
	// None of the returned slots should overlap 10:00-11:00.
	for _, s := range slots {
		st, _ := time.Parse(time.RFC3339, s["start"])
		en, _ := time.Parse(time.RFC3339, s["end"])
		busyStart, _ := time.Parse(time.RFC3339, "2026-05-04T10:00:00Z")
		busyEnd, _ := time.Parse(time.RFC3339, "2026-05-04T11:00:00Z")
		if st.Before(busyEnd) && busyStart.Before(en) {
			t.Errorf("slot %v overlaps busy range", s)
		}
	}
}

func TestUnit_FindSlot_RespectsBuffers(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cal, _ := app.toolCalendarsCreate(ctx, map[string]any{"name": "Work"})
	calID := cal.(Calendar).ID
	app.toolEventsCreate(ctx, map[string]any{
		"calendar_id": calID, "title": "M",
		"start_at": "2026-05-04T10:00:00Z",
		"end_at":   "2026-05-04T10:30:00Z",
	})
	// 30-min slot search with 30-min buffer before+after means a meeting
	// from 10:00-10:30 effectively blocks 09:30-11:00 (exclusive on
	// edges). 09:00-09:30 ends exactly at the buffer's start so it's
	// allowed; 09:30-10:00 is NOT (eats into the before-buffer);
	// 11:00-11:30 IS allowed (after-buffer ends at 11:00).
	out, _ := app.toolEventsFindSlot(ctx, map[string]any{
		"duration_minutes":      30,
		"window_start":          "2026-05-04T09:00:00Z",
		"window_end":            "2026-05-04T13:00:00Z",
		"buffer_before_minutes": 30,
		"buffer_after_minutes":  30,
		"working_hours": map[string]any{
			"mon": map[string]any{"start": "09:00", "end": "18:00"},
		},
		"limit": 20,
	})
	slots := out.(map[string]any)["slots"].([]map[string]string)
	starts := map[string]bool{}
	for _, s := range slots {
		starts[s["start"]] = true
	}
	if starts["2026-05-04T09:30:00Z"] {
		t.Errorf("09:30 slot returned but should be blocked by before-buffer; slots=%+v", slots)
	}
	if starts["2026-05-04T10:00:00Z"] {
		t.Errorf("10:00 slot returned but should be blocked by the meeting itself")
	}
	if starts["2026-05-04T10:30:00Z"] {
		t.Errorf("10:30 slot returned but should be blocked by after-buffer")
	}
	if !starts["2026-05-04T11:00:00Z"] {
		t.Errorf("11:00 slot should be available (after-buffer ends at 11:00); slots=%+v", slots)
	}
}

func TestUnit_HolidaysSet_IsIdempotent(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	r1, err := app.toolHolidaysSet(ctx, map[string]any{"year": 2026, "country": "FR"})
	if err != nil {
		t.Fatal(err)
	}
	first := r1.(map[string]any)["created"].(int)
	if first == 0 {
		t.Errorf("expected nonzero holidays for FR 2026")
	}
	// Re-run — should create zero new rows.
	r2, _ := app.toolHolidaysSet(ctx, map[string]any{"year": 2026, "country": "FR"})
	if r2.(map[string]any)["created"].(int) != 0 {
		t.Errorf("re-running should be idempotent, got %v", r2)
	}
	// And land on a kind=holidays calendar.
	out, _ := app.toolCalendarsList(ctx, map[string]any{})
	cs := out.(map[string]any)["calendars"].([]Calendar)
	if len(cs) != 1 || cs[0].Kind != "holidays" {
		t.Errorf("expected one holidays calendar, got %+v", cs)
	}
}

// ─── HTTP (in-process) ───────────────────────────────────────────

func TestHTTP_CalendarsAndEvents(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	// Create a calendar.
	resp, err := http.Post(srv.URL+"/calendars", "application/json",
		strings.NewReader(`{"name":"Personal","color":"#22c55e"}`))
	must200(t, resp, err)
	var cal Calendar
	json.NewDecoder(resp.Body).Decode(&cal)
	if cal.ID == 0 || cal.Name != "Personal" {
		t.Errorf("calendar create returned %+v", cal)
	}
	// Create an event.
	body := `{"calendar_id":` + itoa(cal.ID) + `,"title":"Lunch","start_at":"2026-05-04T12:00:00Z","end_at":"2026-05-04T13:00:00Z"}`
	resp2, err := http.Post(srv.URL+"/items", "application/json", strings.NewReader(body))
	must200(t, resp2, err)
	var ev Event
	json.NewDecoder(resp2.Body).Decode(&ev)
	// List events in window.
	listResp, err := http.Get(srv.URL + "/items?from=2026-05-04T00:00:00Z&to=2026-05-05T00:00:00Z")
	must200(t, listResp, err)
	var listOut struct {
		Events []Occurrence `json:"events"`
	}
	json.NewDecoder(listResp.Body).Decode(&listOut)
	if len(listOut.Events) != 1 || listOut.Events[0].Title != "Lunch" {
		t.Errorf("HTTP list mismatch: %+v", listOut)
	}
}

func TestHTTP_FindSlot(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	// Setup: calendar with one busy hour.
	app := &App{}
	c, _ := app.toolCalendarsCreate(globalCtx, map[string]any{"name": "Work"})
	calID := c.(Calendar).ID
	app.toolEventsCreate(globalCtx, map[string]any{
		"calendar_id": calID, "title": "Existing",
		"start_at": "2026-05-04T10:00:00Z",
		"end_at":   "2026-05-04T11:00:00Z",
	})
	resp, err := http.Post(srv.URL+"/find_slot", "application/json", strings.NewReader(`{
		"duration_minutes": 30,
		"window_start": "2026-05-04T09:00:00Z",
		"window_end": "2026-05-04T18:00:00Z",
		"working_hours": {"mon": {"start": "09:00", "end": "18:00"}},
		"limit": 3
	}`))
	must200(t, resp, err)
	var out struct {
		Slots []map[string]string `json:"slots"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Slots) == 0 {
		t.Errorf("no slots returned via HTTP")
	}
}

// ─── MANIFEST (contract checks) ──────────────────────────────────

func TestMCP_AllToolsHaveValidShape(t *testing.T) {
	app := &App{}
	tools := app.MCPTools()
	want := map[string]bool{
		"calendars_list": false, "calendars_create": false,
		"calendars_update": false, "calendars_delete": false,
		"events_list": false, "events_get": false,
		"events_create": false, "events_update": false,
		"events_delete": false, "events_find_slot": false,
		"holidays_set": false,
	}
	for _, tool := range tools {
		if _, expected := want[tool.Name]; !expected {
			t.Errorf("unexpected tool: %q", tool.Name)
			continue
		}
		want[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("%s: description empty", tool.Name)
		}
		if tool.Handler == nil {
			t.Errorf("%s: handler nil", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s: schema.type != object", tool.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing tool: %q", name)
		}
	}
}

// ─── helpers ─────────────────────────────────────────────────────

func newCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEmitter(rec),
	)
	globalCtx = ctx
	return ctx
}

func newHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	newCtx(t)
	app := &App{}
	mux := http.NewServeMux()
	for _, r := range app.HTTPRoutes() {
		method, pattern, handler := r.Method, r.Pattern, r.Handler
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if method != "" && req.Method != method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handler(w, req)
		})
	}
	return httptest.NewServer(mux)
}

func must200(t *testing.T, resp *http.Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("HTTP error: %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf bytes.Buffer
	for n > 0 {
		buf.WriteByte(byte('0' + n%10))
		n /= 10
	}
	b := buf.Bytes()
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
