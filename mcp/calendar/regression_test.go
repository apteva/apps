package main

import (
	sdk "github.com/apteva/app-sdk"
	"testing"
	"time"
)

func auditTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
func auditEvent(t *testing.T, ctx *sdk.AppCtx, extra map[string]any) Event {
	t.Helper()
	a := &App{}
	c, err := a.toolCalendarsCreate(ctx, map[string]any{"name": "Audit"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"calendar_id": c.(Calendar).ID, "title": "Original", "start_at": "2026-05-04T09:00:00Z", "end_at": "2026-05-04T10:00:00Z"}
	for k, v := range extra {
		args[k] = v
	}
	out, err := a.toolEventsCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(Event)
}
func auditList(t *testing.T, ctx *sdk.AppCtx) []Occurrence {
	t.Helper()
	out, err := (&App{}).toolEventsList(ctx, map[string]any{"from": "2026-05-01T00:00:00Z", "to": "2026-06-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["events"].([]Occurrence)
}

func TestAudit_OldDailySeriesStillAppears(t *testing.T) {
	ev := Event{StartAt: "2020-01-01T09:00:00Z", EndAt: "2020-01-01T10:00:00Z", RRule: "FREQ=DAILY"}
	got := expandOccurrences(ev, auditTime("2026-05-04T00:00:00Z"), auditTime("2026-05-05T00:00:00Z"))
	if len(got) != 1 {
		t.Fatalf("want 1 occurrence of ongoing daily series; got %d", len(got))
	}
}
func TestAudit_Monthly31DoesNotDrift(t *testing.T) {
	ev := Event{StartAt: "2026-01-31T09:00:00Z", EndAt: "2026-01-31T10:00:00Z", RRule: "FREQ=MONTHLY;COUNT=3"}
	got := expandOccurrences(ev, auditTime("2026-01-01T00:00:00Z"), auditTime("2026-07-01T00:00:00Z"))
	if len(got) != 3 || got[1].StartAt != "2026-03-31T09:00:00Z" {
		t.Fatalf("RFC5545 skips invalid Feb 31; got %+v", got)
	}
}
func TestAudit_WeeklyByDayOrderDoesNotChangeCount(t *testing.T) {
	ev := Event{StartAt: "2026-05-04T09:00:00Z", EndAt: "2026-05-04T10:00:00Z", RRule: "FREQ=WEEKLY;BYDAY=FR,MO;COUNT=1"}
	got := expandOccurrences(ev, auditTime("2026-05-04T00:00:00Z"), auditTime("2026-05-11T00:00:00Z"))
	if len(got) != 1 || got[0].StartAt != "2026-05-04T09:00:00Z" {
		t.Fatalf("want Monday before Friday regardless of BYDAY order; got %+v", got)
	}
}
func TestAudit_UnknownRulePartRejected(t *testing.T) {
	if _, err := parseRRule("FREQ=MONTHLY;UNSUPPORTED=1"); err == nil {
		t.Fatal("unknown recurrence field accepted")
	}
}
func TestAudit_CreateRejectsNegativeDuration(t *testing.T) {
	ctx := newCtx(t)
	c, err := (&App{}).toolCalendarsCreate(ctx, map[string]any{"name": "Audit"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).toolEventsCreate(ctx, map[string]any{"calendar_id": c.(Calendar).ID, "title": "Backwards", "start_at": "2026-05-04T10:00:00Z", "end_at": "2026-05-04T09:00:00Z"})
	if err == nil {
		t.Fatal("negative-duration event accepted")
	}
}
func TestAudit_UpdateRejectsInvalidTimes(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, nil)
	_, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "start_at": "garbage"})
	if err == nil {
		t.Fatalf("invalid date accepted; event now lists as %+v", auditList(t, ctx))
	}
}
func TestAudit_ClearOptionalFieldsAndRecurrence(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, map[string]any{"description": "Private notes", "location": "Office", "rrule": "FREQ=DAILY;COUNT=3"})
	out, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "scope": "all", "title": "Updated", "description": "", "location": "", "rrule": ""})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(Event)
	if got.Description != "" || got.Location != "" || got.RRule != "" {
		t.Fatalf("cleared fields retained: description=%q location=%q rrule=%q", got.Description, got.Location, got.RRule)
	}
}
func TestAudit_DialogEditPreservesEarlierOccurrences(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=5"})
	// Exact payload emitted by EventDialog when editing the third occurrence's title.
	_, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "scope": "all", "title": "Renamed", "description": "", "location": ""})
	if err != nil {
		t.Fatal(err)
	}
	got := auditList(t, ctx)
	if len(got) == 0 || got[0].StartAt != "2026-05-04T09:00:00Z" {
		t.Fatalf("title edit moved series origin: %+v", got)
	}
}
func TestAudit_SplitPreservesCount(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=5"})
	_, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "scope": "this_and_following", "occurrence_start_at": "2026-05-06T09:00:00Z", "title": "Later"})
	if err != nil {
		t.Fatal(err)
	}
	got := auditList(t, ctx)
	if len(got) != 5 {
		t.Fatalf("5-occurrence series became %d occurrences after splitting at third", len(got))
	}
}
func TestAudit_DeleteFollowingRemovesOverrides(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=5"})
	_, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "scope": "this", "occurrence_start_at": "2026-05-08T09:00:00Z", "title": "Override"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).toolEventsDelete(ctx, map[string]any{"event_id": ev.ID, "scope": "this_and_following", "occurrence_start_at": "2026-05-06T09:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	got := auditList(t, ctx)
	if len(got) != 2 {
		t.Fatalf("expected only May 4/5, got %+v", got)
	}
}
func TestAudit_FailedOverrideDoesNotEraseOccurrence(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=3"})
	_, err := ctx.AppDB().Exec(`CREATE TRIGGER audit_fail_child BEFORE INSERT ON events WHEN NEW.parent_event_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "scope": "this", "occurrence_start_at": "2026-05-05T09:00:00Z", "title": "Edit"})
	if err == nil {
		t.Fatal("injection failed")
	}
	if got := auditList(t, ctx); len(got) != 3 {
		t.Fatalf("failed edit erased occurrence: got %d instead of 3", len(got))
	}
}
func TestAudit_OverrideRetryDoesNotDuplicate(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=3"})
	for i := 0; i < 2; i++ {
		_, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "scope": "this", "occurrence_start_at": "2026-05-05T09:00:00Z", "title": "Edit"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := auditList(t, ctx); len(got) != 3 {
		t.Fatalf("same occurrence updated twice produces %d occurrences instead of 3", len(got))
	}
}
func TestAudit_OverrideHonorsAllDay(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=3"})
	out, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "scope": "this", "occurrence_start_at": "2026-05-05T09:00:00Z", "all_day": true, "start_at": "2026-05-05", "end_at": "2026-05-06"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.(Event).AllDay {
		t.Fatal("all_day=true ignored for single-occurrence update")
	}
}
func TestAudit_FindSlotIncludesOutsideWindowBuffers(t *testing.T) {
	ctx := newCtx(t)
	auditEvent(t, ctx, map[string]any{"start_at": "2026-05-04T08:00:00Z", "end_at": "2026-05-04T09:00:00Z"})
	out, err := (&App{}).toolEventsFindSlot(ctx, map[string]any{"duration_minutes": 30, "window_start": "2026-05-04T09:00:00Z", "window_end": "2026-05-04T12:00:00Z", "buffer_after_minutes": 30, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["slots"].([]map[string]string)
	if len(got) != 1 || got[0]["start"] != "2026-05-04T09:30:00Z" {
		t.Fatalf("want first slot 09:30 after prior event buffer, got %+v", got)
	}
}
func TestAudit_DefaultScopeMatchesToolDescription(t *testing.T) {
	ctx := newCtx(t)
	ev := auditEvent(t, ctx, map[string]any{"rrule": "FREQ=DAILY;COUNT=3"})
	_, err := (&App{}).toolEventsUpdate(ctx, map[string]any{"event_id": ev.ID, "occurrence_start_at": "2026-05-05T09:00:00Z", "title": "Only this"})
	if err != nil {
		t.Fatal(err)
	}
	got := auditList(t, ctx)
	changed := 0
	for _, o := range got {
		if o.Title == "Only this" {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("tool documents default scope=this, but changed %d occurrences", changed)
	}
}

func TestAudit_GlobalInstallSeparatesProjects(t *testing.T) {
	ctx := newCtx(t)
	t.Setenv("APTEVA_PROJECT_ID", "")
	a := &App{}
	_, err := a.toolCalendarsCreate(ctx.WithProject("project-A"), map[string]any{"name": "Private A"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.toolCalendarsList(ctx.WithProject("project-B"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["calendars"].([]Calendar); len(got) != 0 {
		t.Fatalf("project B can list A calendar: %+v", got)
	}
}

func TestAudit_WeeklyLocalTimeSurvivesDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	ev := Event{Timezone: "Europe/Madrid", StartAt: "2026-03-23T08:00:00Z", EndAt: "2026-03-23T09:00:00Z", RRule: "FREQ=WEEKLY;COUNT=2"}
	got := expandOccurrences(ev, auditTime("2026-03-23T00:00:00Z"), auditTime("2026-04-01T00:00:00Z"))
	if len(got) != 2 {
		t.Fatal(got)
	}
	if hour := auditTime(got[1].StartAt).In(loc).Hour(); hour != 9 {
		t.Fatalf("09:00 Madrid weekly meeting becomes %02d:00 after DST", hour)
	}
}

func TestAuditPerf_HistoricalRows(t *testing.T) {
	for _, n := range []int{1000, 10000, 50000} {
		t.Run(itoa(int64(n)), func(t *testing.T) {
			ctx := newCtx(t)
			ev := auditEvent(t, ctx, nil)
			_, err := ctx.AppDB().Exec(`WITH RECURSIVE nums(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM nums WHERE n<?) INSERT INTO events(calendar_id,title,start_at,end_at) SELECT ?, 'Historical', '2020-01-01T09:00:00Z','2020-01-01T10:00:00Z' FROM nums`, n, ev.CalendarID)
			if err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			for i := 0; i < 3; i++ {
				if got := auditList(t, ctx); len(got) != 1 {
					t.Fatal(len(got))
				}
			}
			t.Logf("historical rows=%d average list duration=%s (one returned event)", n, time.Since(before)/3)
		})
	}
}
