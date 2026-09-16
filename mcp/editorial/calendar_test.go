package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func calendar(t *testing.T, a *App, c *sdk.AppCtx, args map[string]any) []CalendarEvent {
	t.Helper()
	v, e := a.dispatch(c, "calendar", args)
	if e != nil {
		t.Fatal(e)
	}
	out, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("unexpected calendar payload %T", v)
	}
	events, ok := out["events"].([]CalendarEvent)
	if !ok {
		t.Fatalf("unexpected events payload %T", out["events"])
	}
	return events
}

func kinds(events []CalendarEvent) map[string]int {
	out := map[string]int{}
	for _, e := range events {
		out[e.Kind]++
	}
	return out
}

// The window is the whole point of the endpoint: a widget must not page the
// project's entire history to draw one month.
func TestCalendarWindowsItemsAndReleases(t *testing.T) {
	a, c := testApp(t, nil)
	inside := create(t, a, c, map[string]any{"title": "Inside", "planned_at": "2026-10-10"})
	create(t, a, c, map[string]any{"title": "Before", "planned_at": "2026-09-30"})
	create(t, a, c, map[string]any{"title": "After", "planned_at": "2026-11-02"})
	create(t, a, c, map[string]any{"title": "Undated"})

	events := calendar(t, a, c, map[string]any{"from": "2026-10-01", "to": "2026-10-31"})
	if len(events) != 1 || events[0].Title != "Inside" || events[0].ItemID != inside.ID {
		t.Fatalf("expected only the in-window item, got %+v", events)
	}
	if events[0].Kind != "item" || events[0].Date != "2026-10-10" {
		t.Fatalf("unexpected event shape %+v", events[0])
	}

	// An RFC3339 timestamp windows on its date prefix like a plain date does.
	create(t, a, c, map[string]any{"title": "Timed", "planned_at": "2026-10-15T09:30:00Z"})
	events = calendar(t, a, c, map[string]any{"from": "2026-10-15", "to": "2026-10-15"})
	if len(events) != 1 || events[0].Date != "2026-10-15" {
		t.Fatalf("expected the timestamped item on its date, got %+v", events)
	}
}

// The reason this is not a filter on /items: a release inside the window whose
// parent item is dated outside it (or not dated at all) still has to show up.
func TestCalendarIncludesReleasesWhoseParentIsOutsideTheWindow(t *testing.T) {
	a, c := testApp(t, nil)
	parent := create(t, a, c, map[string]any{"title": "Evergreen guide", "planned_at": "2026-01-05"})
	undated := create(t, a, c, map[string]any{"title": "Rolling series"})
	for _, args := range []map[string]any{
		{"item_id": parent.ID, "channel": "Newsletter", "planned_at": "2026-10-08"},
		{"item_id": undated.ID, "channel": "LinkedIn", "planned_at": "2026-10-09"},
	} {
		if _, e := a.dispatch(c, "releases_create", args); e != nil {
			t.Fatal(e)
		}
	}

	events := calendar(t, a, c, map[string]any{"from": "2026-10-01", "to": "2026-10-31"})
	if len(events) != 2 || kinds(events)["release"] != 2 {
		t.Fatalf("expected both releases regardless of their parents, got %+v", events)
	}
	// Releases carry their parent's identity so the widget can label and open them.
	if events[0].Title != "Evergreen guide" || events[0].Channel != "Newsletter" || events[0].ItemID != parent.ID {
		t.Fatalf("release did not inherit its parent, got %+v", events[0])
	}
	if events[0].ReleaseID == 0 {
		t.Fatal("release events must carry release_id")
	}
	// Sorted by date across both kinds.
	if events[0].Date > events[1].Date {
		t.Fatalf("events are not date-ordered: %+v", events)
	}
}

// Releases hold publication dates, so a deadline calendar must not mix them in.
func TestCalendarDeadlineFieldExcludesReleases(t *testing.T) {
	a, c := testApp(t, nil)
	i := create(t, a, c, map[string]any{"title": "Q4 feature", "deadline": "2026-10-12", "planned_at": "2026-11-20"})
	if _, e := a.dispatch(c, "releases_create", map[string]any{"item_id": i.ID, "channel": "Website", "planned_at": "2026-10-14"}); e != nil {
		t.Fatal(e)
	}

	events := calendar(t, a, c, map[string]any{"from": "2026-10-01", "to": "2026-10-31", "date_field": "deadline"})
	if len(events) != 1 || events[0].Kind != "item" || events[0].Date != "2026-10-12" {
		t.Fatalf("deadline view should show the item on its deadline only, got %+v", events)
	}
	// And the publication view shows the release but not the deadline.
	events = calendar(t, a, c, map[string]any{"from": "2026-10-01", "to": "2026-10-31"})
	if len(events) != 1 || events[0].Kind != "release" {
		t.Fatalf("publication view should show only the release, got %+v", events)
	}
	events = calendar(t, a, c, map[string]any{"from": "2026-10-01", "to": "2026-10-31", "include_releases": false})
	if len(events) != 0 {
		t.Fatalf("include_releases=false should drop release events, got %+v", events)
	}
}

func TestCalendarBrandFilterAndArchiveExclusion(t *testing.T) {
	a, c := testApp(t, nil)
	if _, e := a.dispatch(c, "settings_update", map[string]any{"revision": int64(0),
		"brands":   []any{map[string]any{"id": "acme", "name": "Acme"}},
		"statuses": defaultSettings().Statuses, "formats": defaultSettings().Formats, "channels": defaultSettings().Channels}); e != nil {
		t.Fatal(e)
	}
	branded := create(t, a, c, map[string]any{"title": "Acme launch", "planned_at": "2026-10-10", "brand_id": "acme"})
	create(t, a, c, map[string]any{"title": "House post", "planned_at": "2026-10-11"})
	archived := create(t, a, c, map[string]any{"title": "Dropped", "planned_at": "2026-10-12"})
	if _, e := a.dispatch(c, "items_update", map[string]any{"id": archived.ID, "revision": archived.Revision,
		"patch": map[string]any{"archived": true}}); e != nil {
		t.Fatal(e)
	}

	window := map[string]any{"from": "2026-10-01", "to": "2026-10-31"}
	all := calendar(t, a, c, window)
	if len(all) != 2 {
		t.Fatalf("archived content must stay off the calendar, got %+v", all)
	}
	only := calendar(t, a, c, map[string]any{"from": "2026-10-01", "to": "2026-10-31", "brand_id": "acme"})
	if len(only) != 1 || only[0].ItemID != branded.ID || only[0].BrandID != "acme" {
		t.Fatalf("brand filter failed, got %+v", only)
	}
	unassigned := calendar(t, a, c, map[string]any{"from": "2026-10-01", "to": "2026-10-31", "brand_id": "unassigned"})
	if len(unassigned) != 1 || unassigned[0].Title != "House post" {
		t.Fatalf("unassigned filter failed, got %+v", unassigned)
	}

	// An archived release disappears even though its parent is still live.
	live := create(t, a, c, map[string]any{"title": "Still live", "planned_at": "2026-12-01"})
	v, e := a.dispatch(c, "releases_create", map[string]any{"item_id": live.ID, "channel": "Website", "planned_at": "2026-12-02"})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Release)
	if _, e = a.dispatch(c, "releases_update", map[string]any{"id": r.ID, "revision": r.Revision,
		"patch": map[string]any{"archived": true}}); e != nil {
		t.Fatal(e)
	}
	december := calendar(t, a, c, map[string]any{"from": "2026-12-01", "to": "2026-12-31"})
	if len(december) != 1 || december[0].Kind != "item" {
		t.Fatalf("archived release must not appear, got %+v", december)
	}
}

func TestCalendarProjectIsolationAndDefaults(t *testing.T) {
	a, c := testApp(t, nil)
	other := c.WithProject("beta")
	create(t, a, c, map[string]any{"title": "Alpha only", "planned_at": "2027-10-10"})
	if events := calendar(t, a, other, map[string]any{"from": "2027-10-01", "to": "2027-10-31"}); len(events) != 0 {
		t.Fatalf("another project's calendar leaked: %+v", events)
	}

	// Omitting the window defaults to the next thirty days from today.
	soon := time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	far := time.Now().UTC().AddDate(0, 0, 60).Format("2006-01-02")
	create(t, a, c, map[string]any{"title": "Soon", "planned_at": soon})
	create(t, a, c, map[string]any{"title": "Far off", "planned_at": far})
	v, e := a.dispatch(c, "calendar", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	out := v.(map[string]any)
	if out["from"] != time.Now().UTC().Format("2006-01-02") || out["date_field"] != "planned_at" {
		t.Fatalf("unexpected defaults %+v", out)
	}
	events := out["events"].([]CalendarEvent)
	if len(events) != 1 || events[0].Title != "Soon" {
		t.Fatalf("default window should cover thirty days, got %+v", events)
	}
}

func TestCalendarRejectsUnusableWindows(t *testing.T) {
	a, c := testApp(t, nil)
	for _, args := range []map[string]any{
		{"from": "not-a-date"},
		{"from": "2026-10-01", "to": "2026-09-01"},
		{"from": "2026-01-01", "to": "2028-01-01"},
		{"from": "2026-10-01", "to": "2026-10-31", "date_field": "created_at"},
	} {
		if _, e := a.dispatch(c, "calendar", args); e == nil {
			t.Fatalf("expected %v to be rejected", args)
		} else {
			var v validationError
			if !errors.As(e, &v) {
				t.Fatalf("expected a validation error for %v, got %v", args, e)
			}
		}
	}
}

func TestCalendarTruncatesAtLimit(t *testing.T) {
	a, c := testApp(t, nil)
	for n := 0; n < 5; n++ {
		create(t, a, c, map[string]any{"title": "Daily", "planned_at": fmt.Sprintf("2026-10-%02d", n+1)})
	}
	v, e := a.dispatch(c, "calendar", map[string]any{"from": "2026-10-01", "to": "2026-10-31", "limit": int64(3)})
	if e != nil {
		t.Fatal(e)
	}
	out := v.(map[string]any)
	if out["truncated"] != true || len(out["events"].([]CalendarEvent)) != 3 {
		t.Fatalf("expected a truncated page of three, got %+v", out)
	}
}

func TestCalendarOverHTTP(t *testing.T) {
	a, _ := testApp(t, nil)
	create(t, a, a.ctx.WithProject("alpha"), map[string]any{"title": "Webbed", "planned_at": "2026-10-10"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/calendar?from=2026-10-01&to=2026-10-31", nil)
	r.Header.Set("X-Apteva-Project-Id", "alpha")
	a.http(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Events []CalendarEvent `json:"events"`
		From   string          `json:"from"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Events) != 1 || out.Events[0].Title != "Webbed" || out.From != "2026-10-01" {
		t.Fatalf("unexpected body %s", w.Body.String())
	}

	// include_releases arrives as a query string, not a JSON boolean.
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/calendar?from=2026-10-01&to=2026-10-31&include_releases=false", nil)
	r.Header.Set("X-Apteva-Project-Id", "alpha")
	a.http(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

// Widgets refresh off the app bus, so the writes a calendar cares about have to
// announce themselves under the topics the manifest declares.
func TestPlanningWritesEmitDeclaredTopics(t *testing.T) {
	rec := tk.NewEmitRecorder()
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec)).WithProject("alpha")
	a := &App{}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}

	i := create(t, a, ctx, map[string]any{"title": "Announced", "planned_at": "2026-10-10"})
	updated, e := a.dispatch(ctx, "items_update", map[string]any{"id": i.ID, "revision": i.Revision,
		"patch": map[string]any{"planned_at": "2026-10-11"}})
	if e != nil {
		t.Fatal(e)
	}
	item := updated.(Item)
	if _, e = a.dispatch(ctx, "releases_create", map[string]any{"item_id": i.ID, "channel": "Website", "planned_at": "2026-10-12"}); e != nil {
		t.Fatal(e)
	}
	// Archiving last: the app refuses to plan releases on archived content.
	if _, e = a.dispatch(ctx, "items_update", map[string]any{"id": item.ID, "revision": item.Revision,
		"patch": map[string]any{"archived": true}}); e != nil {
		t.Fatal(e)
	}

	for _, topic := range []string{"content.created", "content.updated", "content.archived", "release.created"} {
		if len(rec.EventsByTopic(topic)) == 0 {
			t.Fatalf("no %s event was emitted", topic)
		}
	}
	// Every declared refresh topic must be a topic the manifest also publishes,
	// or the widget waits on an event that never arrives.
	published := map[string]bool{}
	for _, d := range a.Manifest().Provides.Publishes {
		published[d.Name] = true
	}
	for _, comp := range a.Manifest().Provides.UIComponents {
		for _, topic := range comp.RefreshTopics {
			if !published[topic] {
				t.Fatalf("%s refreshes on undeclared topic %s", comp.Name, topic)
			}
		}
	}
}
