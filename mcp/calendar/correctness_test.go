package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProjectOwnershipAllOperations(t *testing.T) {
	ctx := newCtx(t)
	t.Setenv("APTEVA_PROJECT_ID", "")
	a := &App{}
	owner := ctx.WithProject("A")
	other := ctx.WithProject("B")
	e := auditEvent(t, owner, nil)
	for name, call := range map[string]func() error{
		"get event": func() error { _, err := a.toolEventsGet(other, map[string]any{"event_id": e.ID}); return err },
		"update event": func() error {
			_, err := a.toolEventsUpdate(other, map[string]any{"event_id": e.ID, "title": "Wrong"})
			return err
		},
		"delete event": func() error { _, err := a.toolEventsDelete(other, map[string]any{"event_id": e.ID}); return err },
		"get calendar": func() error { _, err := getCalendar(other, e.CalendarID); return err },
		"update calendar": func() error {
			_, err := a.toolCalendarsUpdate(other, map[string]any{"id": e.CalendarID, "name": "Wrong"})
			return err
		},
		"delete calendar": func() error { _, err := a.toolCalendarsDelete(other, map[string]any{"id": e.CalendarID}); return err },
		"missing project": func() error { _, err := a.toolCalendarsList(ctx, nil); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("unauthorized operation accepted")
			}
		})
	}
}
func TestHTTPProjectAndJSONValidation(t *testing.T) {
	newCtx(t)
	a := &App{}
	for _, body := range []string{"null", "[1]", "{", "{} {}"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("PATCH", "/items/1", strings.NewReader(body))
		a.handleEventsItem(w, r)
		if w.Code != 400 {
			t.Fatalf("body %q: %d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/calendars?project_id=other", nil)
	a.handleCalendars(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
}
func TestSplitPreservesExceptionsAndRebasesToNewTime(t *testing.T) {
	ctx := newCtx(t)
	a := &App{}
	e := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=6"})
	if _, err := a.toolEventsDelete(ctx, map[string]any{"event_id": e.ID, "scope": "this", "occurrence_start_at": "2026-05-07T09:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolEventsUpdate(ctx, map[string]any{"event_id": e.ID, "scope": "this", "occurrence_start_at": "2026-05-08T09:00:00Z", "title": "Custom"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolEventsUpdate(ctx, map[string]any{"event_id": e.ID, "scope": "this_and_following", "occurrence_start_at": "2026-05-06T09:00:00Z", "start_at": "2026-05-06T11:00:00Z", "end_at": "2026-05-06T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got := auditList(t, ctx)
	if len(got) != 5 {
		t.Fatal(got)
	}
	for _, o := range got {
		if strings.HasPrefix(o.StartAt, "2026-05-07") {
			t.Fatal("deleted occurrence resurrected")
		}
	}
	custom := 0
	for _, o := range got {
		if o.Title == "Custom" {
			custom++
		}
	}
	if custom != 1 {
		t.Fatal(got)
	}
}
func TestSplitFailureRollsBack(t *testing.T) {
	ctx := newCtx(t)
	e := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=5"})
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_split BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": e.ID, "scope": "this_and_following", "occurrence_start_at": "2026-05-06T09:00:00Z", "title": "new"}); err == nil {
		t.Fatal("expected failure")
	}
	if len(auditList(t, ctx)) != 5 {
		t.Fatal("split partially committed")
	}
}
func TestConcurrentOccurrenceEdits(t *testing.T) {
	ctx := newCtx(t)
	e := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=5"})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, at := range []string{"2026-05-05T09:00:00Z", "2026-05-06T09:00:00Z"} {
		wg.Add(1)
		go func(at string) {
			defer wg.Done()
			_, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": e.ID, "scope": "this", "occurrence_start_at": at, "title": "Changed"})
			errs <- err
		}(at)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := auditList(t, ctx)
	if len(got) != 5 {
		t.Fatal(got)
	}
	n := 0
	for _, o := range got {
		if o.Title == "Changed" {
			n++
		}
	}
	if n != 2 {
		t.Fatal(got)
	}
}
func TestMonthlyFirstMondayAndLeapYear(t *testing.T) {
	e := Event{StartAt: "2026-01-05T09:00:00Z", EndAt: "2026-01-05T10:00:00Z", RRule: "FREQ=MONTHLY;BYDAY=MO;BYSETPOS=1;COUNT=3"}
	got := expandOccurrences(e, auditTime("2026-01-01T00:00:00Z"), auditTime("2026-04-01T00:00:00Z"))
	if len(got) != 3 || got[1].StartAt != "2026-02-02T09:00:00Z" || got[2].StartAt != "2026-03-02T09:00:00Z" {
		t.Fatal(got)
	}
	e.StartAt = "2024-02-29T09:00:00Z"
	e.EndAt = "2024-02-29T10:00:00Z"
	e.RRule = "FREQ=YEARLY;COUNT=2"
	got = expandOccurrences(e, auditTime("2024-01-01T00:00:00Z"), auditTime("2029-01-01T00:00:00Z"))
	if len(got) != 2 || got[1].StartAt != "2028-02-29T09:00:00Z" {
		t.Fatal(got)
	}
}
func TestOldUncountedRulesSeekWithoutChangingAnchor(t *testing.T) {
	for _, tc := range []struct{ rule, start, want string }{
		{"FREQ=MONTHLY;INTERVAL=2", "2000-01-31T09:00:00Z", "2026-05-31T09:00:00Z"},
		{"FREQ=YEARLY;INTERVAL=2", "2000-05-04T09:00:00Z", "2026-05-04T09:00:00Z"},
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO", "2000-01-03T09:00:00Z", "2026-05-04T09:00:00Z"},
	} {
		e := Event{StartAt: tc.start, EndAt: auditTime(tc.start).Add(time.Hour).Format(time.RFC3339), RRule: tc.rule}
		got := expandOccurrences(e, auditTime("2026-05-01T00:00:00Z"), auditTime("2026-06-01T00:00:00Z"))
		if len(got) == 0 || got[0].StartAt != tc.want {
			t.Fatalf("%s: %+v", tc.rule, got)
		}
	}
}
func TestFindSlotTimezoneCancelledAndAllDay(t *testing.T) {
	ctx := newCtx(t)
	auditEvent(t, ctx, map[string]any{"status": "cancelled"})
	args := map[string]any{"duration_minutes": 30, "window_start": "2026-05-04T07:00:00Z", "window_end": "2026-05-04T10:00:00Z", "timezone": "Europe/Madrid", "limit": 1}
	out, err := (&App{}).toolEventsFindSlot(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["slots"].([]map[string]string)[0]["start"] != "2026-05-04T07:00:00Z" {
		t.Fatal(out)
	}
	auditEvent(t, ctx, map[string]any{"all_day": true, "start_at": "2026-05-04", "end_at": "2026-05-05"})
	args["timezone"] = "America/Los_Angeles"
	args["window_start"] = "2026-05-05T00:00:00Z"
	args["window_end"] = "2026-05-05T01:00:00Z"
	out, err = (&App{}).toolEventsFindSlot(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.(map[string]any)["slots"].([]map[string]string)) != 0 {
		t.Fatal("all-day date did not block evening", out)
	}
}
func TestHolidayCountriesRemainSeparate(t *testing.T) {
	ctx := newCtx(t)
	a := &App{}
	fr, err := a.toolHolidaysSet(ctx, map[string]any{"year": 2026, "country": "FR"})
	if err != nil {
		t.Fatal(err)
	}
	us, err := a.toolHolidaysSet(ctx, map[string]any{"year": 2026, "country": "US"})
	if err != nil {
		t.Fatal(err)
	}
	if fr.(map[string]any)["calendar_id"] == us.(map[string]any)["calendar_id"] {
		t.Fatal("countries merged")
	}
}
func TestMigrationDeduplicatesOverrides(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, file := range []string{"migrations/001_init.sql"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO calendars(id,project_id,name) VALUES(1,'A','Work'); INSERT INTO events(id,calendar_id,title,start_at,end_at,rrule,exdate) VALUES(1,1,'Master','2026-05-04T09:00:00Z','2026-05-04T10:00:00Z','FREQ=DAILY','["2026-05-05T09:00:00Z"]'); INSERT INTO events(calendar_id,title,start_at,end_at,parent_event_id,occurrence_start_at) VALUES(1,'Old','2026-05-05T09:00:00Z','2026-05-05T10:00:00Z',1,'2026-05-05T09:00:00Z'),(1,'Latest','2026-05-05T09:00:00.000Z','2026-05-05T10:00:00.000Z',1,'2026-05-05T09:00:00.000Z');`)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("migrations/002_calendar_correctness.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(data)); err != nil {
		t.Fatal(err)
	}
	var n int
	var title string
	if err = db.QueryRow(`SELECT COUNT(*),MAX(title) FROM events WHERE parent_event_id=1`).Scan(&n, &title); err != nil {
		t.Fatal(err)
	}
	if n != 1 || title != "Latest" {
		t.Fatal(n, title)
	}
}
func TestCancelledQueriesStop(t *testing.T) {
	ctx := newCtx(t)
	call, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&App{}).listEvents(call, ctx, map[string]any{"from": "2026-05-01", "to": "2026-06-01"}); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := (&App{}).findSlot(call, ctx, map[string]any{}); err != context.Canceled {
		t.Fatal(err)
	}
}
func TestLegacySplitRuleKeepsItsActualEnd(t *testing.T) {
	ctx := newCtx(t)
	e := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=5"})
	if _, err := ctx.AppDB().Exec(`UPDATE events SET rrule='FREQ=DAILY;COUNT=5;UNTIL=20260506T085959Z' WHERE id=?`, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := repairLegacyRules(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	if got := auditList(t, ctx); len(got) != 2 {
		t.Fatal(got)
	}
}
