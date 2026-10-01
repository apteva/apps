package main

import (
	"context"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// dueApp builds an app whose clock the test drives, so the window is exercised
// by placing the scanner in time rather than sleeping.
func dueApp(t *testing.T, at *time.Time) (*App, *sdk.AppCtx, *tk.EmitRecorder) {
	t.Helper()
	rec := tk.NewEmitRecorder()
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec)).WithProject("alpha")
	a := &App{now: func() time.Time { return *at }}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}
	return a, ctx, rec
}

func scan(t *testing.T, a *App, ctx *sdk.AppCtx) {
	t.Helper()
	if e := a.runDueScanner(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
}

func topics(rec *tk.EmitRecorder, topic string) []map[string]any {
	out := []map[string]any{}
	for _, e := range rec.EventsByTopic(topic) {
		if data, ok := e.Data.(map[string]any); ok {
			out = append(out, data)
		}
	}
	return out
}

func settingsFor(t *testing.T, a *App, ctx *sdk.AppCtx, patch map[string]any) {
	t.Helper()
	current, e := getSettings(ctx.AppDB(), "alpha")
	if e != nil {
		t.Fatal(e)
	}
	args := map[string]any{"revision": current.Revision, "statuses": current.Statuses,
		"formats": current.Formats, "channels": current.Channels}
	for k, v := range patch {
		args[k] = v
	}
	if _, e := a.dispatch(ctx, "settings_update", args); e != nil {
		t.Fatal(e)
	}
}

// The first scan of a project starts the clock. Installing into a project that
// already holds a year of back-dated content must not stampede the bus.
func TestDueScannerDoesNotReplayThePast(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	a, ctx, rec := dueApp(t, &at)
	create(t, a, ctx, map[string]any{"title": "Long gone", "planned_at": "2026-01-05"})
	create(t, a, ctx, map[string]any{"title": "Yesterday", "planned_at": "2026-10-14"})

	scan(t, a, ctx)
	if n := len(topics(rec, "content.due")); n != 0 {
		t.Fatalf("first scan replayed %d past events", n)
	}

	// The watermark is the first scan itself, so anything whose instant already
	// passed that moment stays quiet — including this morning's 09:00 items.
	create(t, a, ctx, map[string]any{"title": "This morning", "planned_at": "2026-10-15"})
	at = at.Add(time.Hour)
	scan(t, a, ctx)
	if n := len(topics(rec, "content.due")); n != 0 {
		t.Fatalf("an instant older than the watermark must stay quiet, got %d", n)
	}

	// Something falling due after the watermark does fire.
	create(t, a, ctx, map[string]any{"title": "Tomorrow", "planned_at": "2026-10-16"})
	at = time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
	scan(t, a, ctx)
	due := topics(rec, "content.due")
	if len(due) != 1 || due[0]["title"] != "Tomorrow" {
		t.Fatalf("expected only the newly due item, got %v", due)
	}
}

func TestDueScannerFiresOnceAndReArmsOnReschedule(t *testing.T) {
	at := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	a, ctx, rec := dueApp(t, &at)
	scan(t, a, ctx) // establish the watermark before anything is due
	item := create(t, a, ctx, map[string]any{"title": "Launch", "planned_at": "2026-10-15"})

	// 09:00 UTC by default, so nothing yet at 08:00.
	scan(t, a, ctx)
	if n := len(topics(rec, "content.due")); n != 0 {
		t.Fatalf("fired before the due time, got %d", n)
	}

	at = time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	scan(t, a, ctx)
	scan(t, a, ctx)
	scan(t, a, ctx)
	if n := len(topics(rec, "content.due")); n != 1 {
		t.Fatalf("expected exactly one event across three scans, got %d", n)
	}

	// Rescheduling is a different due instant, so the notice re-arms.
	current, e := readItem(ctx.AppDB(), "alpha", item.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := a.dispatch(ctx, "items_update", map[string]any{"id": item.ID, "revision": current.Revision,
		"patch": map[string]any{"planned_at": "2026-10-16"}}); e != nil {
		t.Fatal(e)
	}
	at = time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
	scan(t, a, ctx)
	if n := len(topics(rec, "content.due")); n != 2 {
		t.Fatalf("a rescheduled date should fire again, got %d", n)
	}
}

// Brand is what subscribers route on, so it travels with every due event.
func TestDueEventsCarryBrandAndIdentity(t *testing.T) {
	at := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	a, ctx, rec := dueApp(t, &at)
	scan(t, a, ctx)
	settingsFor(t, a, ctx, map[string]any{
		"formats": []string{"idea", "preview"},
		"brands":  []any{map[string]any{"id": "acme", "name": "Acme", "color": "#e0533f"}}})

	branded := create(t, a, ctx, map[string]any{"title": "Acme launch", "brand_id": "acme",
		"format": "preview", "approval": "approved", "reviewer": "Sam",
		"planned_at": "2026-10-15", "deadline": "2026-10-15", "owner": "Dana"})
	create(t, a, ctx, map[string]any{"title": "House post", "planned_at": "2026-10-15"})
	if _, e := a.dispatch(ctx, "releases_create", map[string]any{"item_id": branded.ID,
		"channel": "Newsletter", "planned_at": "2026-10-15"}); e != nil {
		t.Fatal(e)
	}

	at = time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	scan(t, a, ctx)

	due := topics(rec, "content.due")
	if len(due) != 2 {
		t.Fatalf("expected both items due, got %v", due)
	}
	var brandedEvent, houseEvent map[string]any
	for _, d := range due {
		if d["title"] == "Acme launch" {
			brandedEvent = d
		} else {
			houseEvent = d
		}
	}
	if brandedEvent["brand_id"] != "acme" || brandedEvent["brand"] != "Acme" {
		t.Fatalf("branded event lost its brand: %v", brandedEvent)
	}
	if brandedEvent["owner"] != "Dana" || brandedEvent["date_field"] != "planned_at" {
		t.Fatalf("unexpected payload: %v", brandedEvent)
	}
	if brandedEvent["due_at"] != "2026-10-15T09:00:00Z" {
		t.Fatalf("due_at should be the resolved instant: %v", brandedEvent["due_at"])
	}
	// Unassigned content keeps the same shape with empty values, never absent keys.
	id, okID := houseEvent["brand_id"]
	name, okName := houseEvent["brand"]
	if !okID || !okName || id != "" || name != "" {
		t.Fatalf("unassigned event should carry empty brand fields: %v", houseEvent)
	}

	deadline := topics(rec, "content.deadline")
	if len(deadline) != 1 || deadline[0]["brand"] != "Acme" || deadline[0]["date_field"] != "deadline" {
		t.Fatalf("deadline event wrong: %v", deadline)
	}
	release := topics(rec, "release.due")
	if len(release) != 1 {
		t.Fatalf("expected one release.due, got %v", release)
	}
	// A release carries its parent's routing fields, including custom formats.
	if release[0]["brand"] != "Acme" || release[0]["channel"] != "Newsletter" ||
		release[0]["format"] != "preview" || release[0]["approval"] != "approved" ||
		release[0]["title"] != "Acme launch" || release[0]["item_id"] != branded.ID {
		t.Fatalf("release event wrong: %v", release[0])
	}
}

// A bare date names a day, not an instant; the project's timezone decides when
// that day starts and the due time decides when it fires.
func TestDueTimezoneAndDueTimeDecideTheInstant(t *testing.T) {
	at := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	a, ctx, rec := dueApp(t, &at)
	scan(t, a, ctx)
	settingsFor(t, a, ctx, map[string]any{"timezone": "Australia/Sydney", "due_time": "08:00"})
	create(t, a, ctx, map[string]any{"title": "Sydney morning", "planned_at": "2026-10-15"})

	// 08:00 in Sydney on the 15th is 21:00 UTC on the 14th — the day before.
	at = time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC)
	scan(t, a, ctx)
	if n := len(topics(rec, "content.due")); n != 0 {
		t.Fatalf("fired an hour early, got %d", n)
	}
	at = time.Date(2026, 10, 14, 21, 30, 0, 0, time.UTC)
	scan(t, a, ctx)
	due := topics(rec, "content.due")
	if len(due) != 1 || due[0]["due_at"] != "2026-10-14T21:00:00Z" {
		t.Fatalf("expected the Sydney instant, got %v", due)
	}

	// A stored timestamp carries its own offset and ignores both settings.
	create(t, a, ctx, map[string]any{"title": "Precise", "planned_at": "2026-10-14T23:15:00Z"})
	at = time.Date(2026, 10, 14, 23, 30, 0, 0, time.UTC)
	scan(t, a, ctx)
	for _, d := range topics(rec, "content.due") {
		if d["title"] == "Precise" && d["due_at"] != "2026-10-14T23:15:00Z" {
			t.Fatalf("a timestamp must be used as written: %v", d)
		}
	}
}

func TestDueScannerSkipsArchivedAndEmptyProjects(t *testing.T) {
	at := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	a, ctx, rec := dueApp(t, &at)
	scan(t, a, ctx)
	archived := create(t, a, ctx, map[string]any{"title": "Dropped", "planned_at": "2026-10-15"})
	if _, e := a.dispatch(ctx, "items_update", map[string]any{"id": archived.ID,
		"revision": archived.Revision, "patch": map[string]any{"archived": true}}); e != nil {
		t.Fatal(e)
	}
	at = time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	scan(t, a, ctx)
	if n := len(topics(rec, "content.due")); n != 0 {
		t.Fatalf("archived content must not come due, got %d", n)
	}

	// The SDK runs one empty-project tick when ListProjects fails; it must be a
	// no-op rather than an error that retries forever.
	if e := a.runDueScanner(context.Background(), ctx.WithProject("")); e != nil {
		t.Fatalf("empty-project tick should be a silent no-op: %v", e)
	}

	// Another project's content stays invisible.
	other := ctx.WithProject("beta")
	create(t, a, other, map[string]any{"title": "Beta item", "planned_at": "2026-10-15"})
	scan(t, a, ctx)
	for _, d := range topics(rec, "content.due") {
		if d["title"] == "Beta item" {
			t.Fatal("project isolation broken in the due scanner")
		}
	}
}

func TestDueSettingsValidationAndManifest(t *testing.T) {
	at := time.Now()
	a, ctx, _ := dueApp(t, &at)
	for _, bad := range []map[string]any{
		{"timezone": "Not/AZone"},
		{"due_time": "9am"},
		{"due_time": "24:00"},
		{"due_time": "09:60"},
	} {
		current, _ := getSettings(ctx.AppDB(), "alpha")
		args := map[string]any{"revision": current.Revision, "statuses": current.Statuses,
			"formats": current.Formats, "channels": current.Channels}
		for k, v := range bad {
			args[k] = v
		}
		if _, e := a.dispatch(ctx, "settings_update", args); e == nil {
			t.Fatalf("expected %v to be rejected", bad)
		}
	}
	settingsFor(t, a, ctx, map[string]any{"timezone": "Europe/Paris", "due_time": "07:30"})
	got, e := getSettings(ctx.AppDB(), "alpha")
	if e != nil || got.Timezone != "Europe/Paris" || got.DueTime != "07:30" {
		t.Fatalf("settings not stored: %+v (%v)", got, e)
	}

	// The worker is declared, and every topic it emits is one the manifest
	// publishes — otherwise a subscriber waits on an event that never arrives.
	m := a.Manifest()
	if len(m.Provides.Workers) != 1 || m.Provides.Workers[0].Name != "due_scanner" {
		t.Fatalf("manifest does not declare the worker: %+v", m.Provides.Workers)
	}
	workers := a.Workers()
	if len(workers) != 1 || workers[0].Schedule != m.Provides.Workers[0].Schedule {
		t.Fatalf("worker schedule disagrees with the manifest: %+v", workers)
	}
	published := map[string]bool{}
	for _, d := range m.Provides.Publishes {
		published[d.Name] = true
	}
	for _, topic := range []string{"content.due", "content.deadline", "release.due"} {
		if !published[topic] {
			t.Fatalf("%s is emitted but not declared", topic)
		}
	}
}
