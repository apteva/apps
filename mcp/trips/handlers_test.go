package main

// Tier 1 — every MCP tool exercised against in-memory SQLite + a
// fake calendar platform stub that records every CallAppResult.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Fake calendar platform ──────────────────────────────────────

type callRecord struct {
	App, Tool string
	Input     map[string]any
}

type fakeCalendar struct {
	tk.BasePlatformClient
	mu                  sync.Mutex
	nextCalID, nextEvtID int64
	calls               []callRecord
	calendars           map[int64]map[string]any
	events              map[int64]map[string]any
	// integration plumbing for the search tools (v0.4+)
	integCalls     []integCall
	integResponses map[string]any                   // tool name → canned response (unwrapped JSON)
	connList       []sdk.PlatformConnection
}

func newFakeCalendar() *fakeCalendar {
	return &fakeCalendar{
		calendars:      map[int64]map[string]any{},
		events:         map[int64]map[string]any{},
		integResponses: map[string]any{},
	}
}

func (f *fakeCalendar) CallAppResult(app, tool string, input map[string]any, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, callRecord{App: app, Tool: tool, Input: input})
	var result any
	switch app + "/" + tool {
	case "calendar/calendars_create":
		f.nextCalID++
		f.calendars[f.nextCalID] = clone(input)
		result = map[string]any{"id": f.nextCalID, "name": input["name"], "color": input["color"]}
	case "calendar/calendars_list":
		cals := []map[string]any{}
		for id, c := range f.calendars {
			cals = append(cals, map[string]any{"id": id, "name": c["name"]})
		}
		result = map[string]any{"calendars": cals}
	case "calendar/calendars_update":
		result = map[string]any{"id": input["id"]}
	case "calendar/calendars_delete":
		id := int64FromAny(input["id"])
		delete(f.calendars, id)
		result = map[string]any{"deleted": id}
	case "calendar/events_create":
		f.nextEvtID++
		f.events[f.nextEvtID] = clone(input)
		result = map[string]any{"id": f.nextEvtID}
	case "calendar/events_update":
		eid := int64FromAny(input["event_id"])
		f.events[eid] = clone(input)
		result = map[string]any{"id": eid}
	case "calendar/events_delete":
		eid := int64FromAny(input["event_id"])
		delete(f.events, eid)
		result = map[string]any{"deleted": eid}
	default:
		f.mu.Unlock()
		return tk.ErrNotImplemented
	}
	f.mu.Unlock()
	b, _ := json.Marshal(result)
	return json.Unmarshal(b, out)
}

func (f *fakeCalendar) countCalls(tool string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Tool == tool {
			n++
		}
	}
	return n
}

// integExec is set by tests that exercise ExecuteIntegrationTool;
// the default returns ErrNotImplemented (matches BasePlatformClient).
func (f *fakeCalendar) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	f.mu.Lock()
	f.integCalls = append(f.integCalls, integCall{ConnID: connID, Tool: tool, Input: input})
	resp := f.integResponses[tool]
	f.mu.Unlock()
	if resp == nil {
		return nil, tk.ErrNotImplemented
	}
	b, _ := json.Marshal(resp)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: b}, nil
}

// ListConnections returns the canned set so the available_connections
// tool round-trip works in tests.
func (f *fakeCalendar) ListConnections(filter sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []sdk.PlatformConnection{}
	for _, c := range f.connList {
		if filter.AppSlug != "" && c.AppSlug != filter.AppSlug {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

type integCall struct {
	ConnID int64
	Tool   string
	Input  map[string]any
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func int64FromAny(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// ─── Trips lifecycle ─────────────────────────────────────────────

func TestUnit_TripsCreate_CreatesLinkedCalendar(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	r, err := app.toolTripsCreate(ctx, map[string]any{
		"name": "Paris weekend", "start_at": "2026-06-05T00:00:00Z", "end_at": "2026-06-08T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	trip := r.(Trip)
	if trip.CalendarID == nil || *trip.CalendarID == 0 {
		t.Errorf("expected calendar_id set, got %+v", trip.CalendarID)
	}
	if fake.countCalls("calendars_create") != 1 {
		t.Errorf("expected one calendars_create call, got %d", fake.countCalls("calendars_create"))
	}
}

func TestUnit_TripsCreate_RequiresDates(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	if _, err := app.toolTripsCreate(ctx, map[string]any{"name": "x"}); err == nil {
		t.Error("expected error for missing dates")
	}
}

func TestUnit_TripsCreate_RejectsBackwardsDates(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	if _, err := app.toolTripsCreate(ctx, map[string]any{
		"name": "x", "start_at": "2026-06-10T00:00:00Z", "end_at": "2026-06-05T00:00:00Z",
	}); err == nil {
		t.Error("expected error when end_at <= start_at")
	}
}

func TestUnit_TripsCreate_SkipsCalendarWhenSyncDisabled(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	r, _ := app.toolTripsCreate(ctx, map[string]any{
		"name": "Stealth", "start_at": "2026-06-05T00:00:00Z", "end_at": "2026-06-08T00:00:00Z",
		"sync_calendar": false,
	})
	if r.(Trip).CalendarID != nil {
		t.Errorf("expected nil calendar_id when sync=false")
	}
	if fake.countCalls("calendars_create") != 0 {
		t.Errorf("expected no calendar calls when sync=false, got %d", fake.countCalls("calendars_create"))
	}
}

// Under the shared-calendar design, trips_delete prunes the trip's
// events but never touches the shared "Trips" calendar — other trips
// may still be using it.
func TestUnit_TripsDelete_DoesNotTouchSharedCalendar(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Solo", true)
	// Add an item so the prune has something to delete.
	if _, err := app.toolTransportLegsAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "kind": "flight",
		"depart_at": "2026-06-05T08:00:00Z", "arrive_at": "2026-06-05T09:30:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolTripsDelete(ctx, map[string]any{"id": float64(trip.ID)}); err != nil {
		t.Fatal(err)
	}
	if fake.countCalls("calendars_delete") != 0 {
		t.Errorf("calendars_delete=%d, want 0 (shared calendar must survive)", fake.countCalls("calendars_delete"))
	}
	if fake.countCalls("events_delete") != 1 {
		t.Errorf("events_delete=%d, want 1 (the leg's event)", fake.countCalls("events_delete"))
	}
}

func TestUnit_TwoTrips_ShareCalendar(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	r1 := mustCreateTrip(t, app, ctx, "Paris", true)
	r2 := mustCreateTrip(t, app, ctx, "Tokyo", true)
	if r1.CalendarID == nil || r2.CalendarID == nil {
		t.Fatalf("expected both trips linked, got %v %v", r1.CalendarID, r2.CalendarID)
	}
	if *r1.CalendarID != *r2.CalendarID {
		t.Errorf("expected shared calendar, got %d and %d", *r1.CalendarID, *r2.CalendarID)
	}
	if fake.countCalls("calendars_create") != 1 {
		t.Errorf("calendars_create=%d, want 1 (shared cal created once)", fake.countCalls("calendars_create"))
	}
}

func TestUnit_LegacyTrip_MigratesOnNextWrite(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	// Simulate a v0.1.x trip by inserting directly with a fake legacy
	// per-trip calendar id (one no other trip uses).
	res, err := ctx.AppDB().Exec(
		`INSERT INTO trips (project_id, name, start_at, end_at, home_currency,
		                    color, sync_calendar, calendar_id)
		 VALUES (?, 'Old', '2026-06-05T00:00:00Z', '2026-06-08T00:00:00Z', 'EUR', '#3b82f6', 1, 99)`,
		"test-proj",
	)
	if err != nil {
		t.Fatal(err)
	}
	tripID, _ := res.LastInsertId()
	// First write — adding a transport leg — should migrate.
	if _, err := app.toolTransportLegsAdd(ctx, map[string]any{
		"trip_id": float64(tripID), "kind": "train",
		"depart_at": "2026-06-05T08:00:00Z", "arrive_at": "2026-06-05T10:30:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	t2, _ := readTrip(ctx, tripID)
	if t2.CalendarID == nil || *t2.CalendarID == 99 {
		t.Errorf("migration didn't switch calendar id: %v", t2.CalendarID)
	}
	if fake.countCalls("calendars_delete") < 1 {
		t.Errorf("expected legacy calendar to be deleted, got calendars_delete=%d", fake.countCalls("calendars_delete"))
	}
}

// ─── Transport leg → calendar event ──────────────────────────────

func TestUnit_TransportLeg_MirrorsToCalendar(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Trip", true)
	r, err := app.toolTransportLegsAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "kind": "flight",
		"depart_at": "2026-06-05T08:00:00Z", "arrive_at": "2026-06-05T09:30:00Z",
		"provider": "Air France", "reference": "AF1234",
		"depart_location": "CDG", "arrive_location": "LIN",
	})
	if err != nil {
		t.Fatal(err)
	}
	leg := r.(TransportLeg)
	if leg.CalendarEventID == nil {
		t.Fatal("expected calendar_event_id set")
	}
	if fake.countCalls("events_create") != 1 {
		t.Errorf("expected 1 events_create, got %d", fake.countCalls("events_create"))
	}
	// Update should call events_update, not events_create.
	if _, err := app.toolTransportLegsUpdate(ctx, map[string]any{
		"id": float64(leg.ID), "reference": "AF1235",
	}); err != nil {
		t.Fatal(err)
	}
	if fake.countCalls("events_update") != 1 {
		t.Errorf("expected 1 events_update, got %d", fake.countCalls("events_update"))
	}
	if _, err := app.toolTransportLegsDelete(ctx, map[string]any{"id": float64(leg.ID)}); err != nil {
		t.Fatal(err)
	}
	if fake.countCalls("events_delete") != 1 {
		t.Errorf("expected 1 events_delete, got %d", fake.countCalls("events_delete"))
	}
}

func TestUnit_TransportLeg_NoMirrorWhenSyncOff(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Stealth", false)
	r, _ := app.toolTransportLegsAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "kind": "flight",
		"depart_at": "2026-06-05T08:00:00Z", "arrive_at": "2026-06-05T09:30:00Z",
	})
	leg := r.(TransportLeg)
	if leg.CalendarEventID != nil {
		t.Error("expected no calendar event when sync off")
	}
	if fake.countCalls("events_create") != 0 {
		t.Error("no events_create should fire when sync off")
	}
}

// ─── Sync toggle ─────────────────────────────────────────────────

func TestUnit_SyncToggle_OffToOn_RehydratesEvents(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Stealth", false)
	// Add items while sync is off — no events should land.
	_, _ = app.toolTransportLegsAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "kind": "flight",
		"depart_at": "2026-06-05T08:00:00Z", "arrive_at": "2026-06-05T09:30:00Z",
	})
	_, _ = app.toolAccommodationsAdd(ctx, map[string]any{
		"trip_id":      float64(trip.ID),
		"name":         "Hotel",
		"check_in_at":  "2026-06-05T15:00:00Z",
		"check_out_at": "2026-06-08T11:00:00Z",
	})
	if fake.countCalls("events_create") != 0 {
		t.Errorf("pre-toggle events_create=%d, want 0", fake.countCalls("events_create"))
	}
	// Now toggle on.
	if _, err := app.toolTripsUpdate(ctx, map[string]any{
		"id": float64(trip.ID), "sync_calendar": true,
	}); err != nil {
		t.Fatal(err)
	}
	// Expect: one calendars_create (since the trip never had one) +
	// two events_create (one per item).
	if fake.countCalls("calendars_create") != 1 {
		t.Errorf("calendars_create=%d, want 1", fake.countCalls("calendars_create"))
	}
	if fake.countCalls("events_create") != 2 {
		t.Errorf("events_create after rehydrate=%d, want 2", fake.countCalls("events_create"))
	}
}

func TestUnit_SyncToggle_OnToOff_PrunesEvents(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Trip", true)
	_, _ = app.toolTransportLegsAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "kind": "flight",
		"depart_at": "2026-06-05T08:00:00Z", "arrive_at": "2026-06-05T09:30:00Z",
	})
	if fake.countCalls("events_create") != 1 {
		t.Fatalf("pre-toggle events_create=%d, want 1", fake.countCalls("events_create"))
	}
	if _, err := app.toolTripsUpdate(ctx, map[string]any{
		"id": float64(trip.ID), "sync_calendar": false,
	}); err != nil {
		t.Fatal(err)
	}
	if fake.countCalls("events_delete") != 1 {
		t.Errorf("events_delete after prune=%d, want 1", fake.countCalls("events_delete"))
	}
}

// ─── Destinations reorder ────────────────────────────────────────

func TestUnit_DestinationsReorder(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Trip", false)
	d1 := mustAddDest(t, app, ctx, trip.ID, "Paris")
	d2 := mustAddDest(t, app, ctx, trip.ID, "Lyon")
	d3 := mustAddDest(t, app, ctx, trip.ID, "Marseille")
	// d1=0, d2=1, d3=2 → reorder to d3, d1, d2 → indices 0,1,2.
	if _, err := app.toolDestinationsReorder(ctx, map[string]any{
		"trip_id": float64(trip.ID),
		"order":   []any{float64(d3.ID), float64(d1.ID), float64(d2.ID)},
	}); err != nil {
		t.Fatal(err)
	}
	dests, _ := listDestinationsByTrip(ctx, trip.ID)
	if len(dests) != 3 || dests[0].PlaceName != "Marseille" || dests[2].PlaceName != "Lyon" {
		t.Errorf("after reorder: %+v", dests)
	}
}

// ─── Budget summary ──────────────────────────────────────────────

func TestUnit_BudgetSummary_AggregatesByBucket(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Trip", false)

	// Transport: planned 30000, actual 32000
	_, _ = app.toolTransportLegsAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "kind": "flight",
		"depart_at": "2026-06-05T08:00:00Z", "arrive_at": "2026-06-05T09:30:00Z",
		"cost_estimated": float64(30000), "cost_actual": float64(32000),
	})
	// Lodging: planned 80000
	_, _ = app.toolAccommodationsAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "name": "Hotel",
		"check_in_at": "2026-06-05T15:00:00Z", "check_out_at": "2026-06-08T11:00:00Z",
		"cost_estimated": float64(80000),
	})
	// Activity food: planned 5000
	_, _ = app.toolActivitiesAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "name": "Dinner", "category": "food",
		"cost_estimated": float64(5000),
	})
	// Activity transport_local: planned 2000 → rolls into transport bucket
	_, _ = app.toolActivitiesAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "name": "Taxi to hotel", "category": "transport_local",
		"cost_estimated": float64(2000),
	})

	// Cap on lodging.
	_, _ = app.toolBudgetSet(ctx, map[string]any{
		"trip_id": float64(trip.ID), "category": "lodging", "amount": float64(100000),
	})

	out, err := app.toolBudgetSummary(ctx, map[string]any{"trip_id": float64(trip.ID)})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(BudgetSummary)
	byCat := map[string]BudgetCategoryRow{}
	for _, c := range s.Categories {
		byCat[c.Category] = c
	}
	if byCat["transport"].Planned != 32000 {
		t.Errorf("transport planned=%d, want 32000 (flight 30000 + taxi 2000)", byCat["transport"].Planned)
	}
	if byCat["transport"].Actual != 32000 {
		t.Errorf("transport actual=%d, want 32000", byCat["transport"].Actual)
	}
	if byCat["lodging"].Cap != 100000 || !byCat["lodging"].Capped {
		t.Errorf("lodging cap missing: %+v", byCat["lodging"])
	}
	if byCat["food"].Planned != 5000 {
		t.Errorf("food planned=%d, want 5000", byCat["food"].Planned)
	}
	if s.TotalPlanned != 117000 {
		t.Errorf("total planned=%d, want 117000", s.TotalPlanned)
	}
}

func TestUnit_BudgetSet_ZeroClears(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Trip", false)
	if _, err := app.toolBudgetSet(ctx, map[string]any{
		"trip_id": float64(trip.ID), "category": "food", "amount": float64(5000),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolBudgetSet(ctx, map[string]any{
		"trip_id": float64(trip.ID), "category": "food", "amount": float64(0),
	}); err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolBudgetSummary(ctx, map[string]any{"trip_id": float64(trip.ID)})
	for _, c := range out.(BudgetSummary).Categories {
		if c.Category == "food" && (c.Cap != 0 || c.Capped) {
			t.Errorf("expected food cap cleared, got %+v", c)
		}
	}
}

// ─── Todos ───────────────────────────────────────────────────────

func TestUnit_TodosToggle(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Trip", false)
	r, _ := app.toolTodosAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "label": "Pack passport",
	})
	id := r.(Todo).ID
	r2, _ := app.toolTodosToggle(ctx, map[string]any{"id": float64(id)})
	if !r2.(Todo).Done {
		t.Error("expected done=true after first toggle")
	}
	r3, _ := app.toolTodosToggle(ctx, map[string]any{"id": float64(id)})
	if r3.(Todo).Done {
		t.Error("expected done=false after second toggle")
	}
}

// ─── Dashboard ───────────────────────────────────────────────────

func TestUnit_Dashboard_ReturnsEverything(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	trip := mustCreateTrip(t, app, ctx, "Trip", false)
	mustAddDest(t, app, ctx, trip.ID, "Paris")
	_, _ = app.toolTransportLegsAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "kind": "flight",
		"depart_at": "2026-06-05T08:00:00Z", "arrive_at": "2026-06-05T09:30:00Z",
	})
	_, _ = app.toolTodosAdd(ctx, map[string]any{
		"trip_id": float64(trip.ID), "label": "Pack",
	})
	r, err := app.toolDashboard(ctx, map[string]any{"trip_id": float64(trip.ID)})
	if err != nil {
		t.Fatal(err)
	}
	d := r.(TripDashboard)
	if d.Trip.ID != trip.ID || len(d.Destinations) != 1 || len(d.TransportLegs) != 1 || len(d.Todos) != 1 {
		t.Errorf("dashboard incomplete: %+v", d)
	}
	if len(d.Budget.Categories) != len(budgetCategories()) {
		t.Errorf("expected one row per budget category, got %d", len(d.Budget.Categories))
	}
}

// ─── HTTP ────────────────────────────────────────────────────────

func TestHTTP_TripCreateDashboardDelete(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/trips", "application/json", bytes.NewBufferString(
		`{"name":"x","start_at":"2026-06-05T00:00:00Z","end_at":"2026-06-08T00:00:00Z"}`))
	must200(t, resp, err)
	var trip Trip
	_ = json.NewDecoder(resp.Body).Decode(&trip)
	resp.Body.Close()
	if trip.ID == 0 {
		t.Fatal("no trip id")
	}
	r2, err := http.Get(srv.URL + "/dashboard?trip_id=" + itoa(trip.ID))
	must200(t, r2, err)
	r2.Body.Close()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/trips/"+itoa(trip.ID), nil)
	r3, err := http.DefaultClient.Do(req)
	if err != nil || r3.StatusCode != 204 {
		t.Fatalf("delete failed: %v %v", err, r3)
	}
}

// ─── Settings + search (v0.4) ────────────────────────────────────

func TestUnit_Settings_UpsertAndRead(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	// First read returns defaults.
	r1, err := app.toolSettingsGet(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.(Settings).DefaultPassengers != 1 || r1.(Settings).DailySearchBudgetCents != 500 {
		t.Errorf("defaults wrong: %+v", r1)
	}
	// Set a few values, read back.
	if _, err := app.toolSettingsSet(ctx, map[string]any{
		"home_airport":         "CDG",
		"default_passengers":   float64(2),
		"duffel_connection_id": float64(42),
		"places_connection_id": float64(99),
	}); err != nil {
		t.Fatal(err)
	}
	r2, _ := app.toolSettingsGet(ctx, nil)
	s := r2.(Settings)
	if s.HomeAirport != "CDG" || s.DefaultPassengers != 2 || s.DuffelConnectionID != 42 || s.PlacesConnectionID != 99 {
		t.Errorf("after set: %+v", s)
	}
}

func TestUnit_SearchPlaces_RequiresConnection(t *testing.T) {
	ctx, _ := newCtx(t)
	app := &App{}
	if _, err := app.toolSearchPlaces(ctx, map[string]any{"query": "paris", "kind": "destination"}); err == nil {
		t.Error("expected error when no places connection configured")
	}
}

func TestUnit_SearchPlaces_AutocompleteDispatch(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	// Configure connection.
	_, _ = app.toolSettingsSet(ctx, map[string]any{"places_connection_id": float64(99)})
	// Stub the upstream response (Google Places autocomplete shape).
	fake.integResponses["autocomplete"] = map[string]any{
		"suggestions": []any{
			map[string]any{"placePrediction": map[string]any{
				"placeId": "ChIJ_paris",
				"structuredFormat": map[string]any{
					"mainText":      map[string]any{"text": "Paris"},
					"secondaryText": map[string]any{"text": "France"},
				},
			}},
		},
	}
	r, err := app.toolSearchPlaces(ctx, map[string]any{"kind": "destination", "query": "par"})
	if err != nil {
		t.Fatal(err)
	}
	out := r.(map[string]any)
	places := out["places"].([]PlaceResult)
	if len(places) != 1 || places[0].PlaceID != "ChIJ_paris" || places[0].Name != "Paris" {
		t.Errorf("autocomplete normalize: %+v", places)
	}
	// Verify it used the autocomplete tool, not search_text/nearby.
	tool := fake.integCalls[0].Tool
	if tool != "autocomplete" {
		t.Errorf("expected autocomplete, got %q", tool)
	}
}

func TestUnit_SearchPlaces_NearbyDispatch(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	_, _ = app.toolSettingsSet(ctx, map[string]any{"places_connection_id": float64(99)})
	fake.integResponses["search_nearby"] = map[string]any{
		"places": []any{
			map[string]any{
				"id":               "ChIJ_hotel",
				"displayName":      map[string]any{"text": "Hotel Foo"},
				"formattedAddress": "1 rue Foo, Paris",
				"location":         map[string]any{"latitude": 48.8, "longitude": 2.3},
				"rating":           4.5,
				"userRatingCount":  float64(123),
				"primaryType":      "lodging",
			},
		},
	}
	r, err := app.toolSearchPlaces(ctx, map[string]any{
		"kind": "lodging", "lat": 48.8, "lng": 2.3, "limit": float64(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	places := r.(map[string]any)["places"].([]PlaceResult)
	if len(places) != 1 || places[0].Name != "Hotel Foo" || places[0].PriceLevel != "" {
		t.Errorf("nearby normalize: %+v", places)
	}
	if fake.integCalls[0].Tool != "search_nearby" {
		t.Errorf("expected search_nearby dispatch, got %q", fake.integCalls[0].Tool)
	}
}

func TestUnit_SearchPlaces_CacheHit(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	_, _ = app.toolSettingsSet(ctx, map[string]any{"places_connection_id": float64(99)})
	fake.integResponses["search_text"] = map[string]any{
		"places": []any{
			map[string]any{"id": "p1", "displayName": map[string]any{"text": "Place 1"}},
		},
	}
	// First call hits upstream.
	if _, err := app.toolSearchPlaces(ctx, map[string]any{"query": "sushi paris"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.integCalls) != 1 {
		t.Fatalf("first call should hit upstream, got %d", len(fake.integCalls))
	}
	// Second call with identical args is a cache hit (no new upstream).
	r, err := app.toolSearchPlaces(ctx, map[string]any{"query": "sushi paris"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.integCalls) != 1 {
		t.Errorf("second call should be cached, got %d upstream calls", len(fake.integCalls))
	}
	if cached := r.(map[string]any)["cached"]; cached != true {
		t.Errorf("expected cached=true on second call, got %v", cached)
	}
}

func TestUnit_SearchFlights_HappyPath(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	_, _ = app.toolSettingsSet(ctx, map[string]any{
		"duffel_connection_id": float64(11),
		"home_airport":         "CDG",
	})
	// Duffel response shape (paraphrased).
	fake.integResponses["search_flights"] = map[string]any{
		"data": map[string]any{
			"offers": []any{
				map[string]any{
					"id":             "off_1",
					"total_amount":   "120.50",
					"total_currency": "EUR",
					"slices": []any{
						map[string]any{
							"duration": "PT1H30M",
							"segments": []any{
								map[string]any{
									"marketing_carrier":                map[string]any{"iata_code": "AF", "name": "Air France"},
									"marketing_carrier_flight_number": "1234",
									"departing_at":                     "2026-06-05T08:00:00",
									"arriving_at":                      "2026-06-05T09:30:00",
									"origin":                           map[string]any{"iata_code": "CDG"},
									"destination":                      map[string]any{"iata_code": "LIN"},
									"cabin_class":                      "economy",
								},
							},
						},
					},
				},
				map[string]any{
					"id": "off_2", "total_amount": "85.00", "total_currency": "EUR",
					"slices": []any{map[string]any{"segments": []any{
						map[string]any{
							"marketing_carrier":                map[string]any{"iata_code": "U2", "name": "easyJet"},
							"marketing_carrier_flight_number": "4321",
							"departing_at":                     "2026-06-05T10:00:00",
							"arriving_at":                      "2026-06-05T11:30:00",
							"origin":                           map[string]any{"iata_code": "CDG"},
							"destination":                      map[string]any{"iata_code": "LIN"},
						},
					}}},
				},
			},
		},
	}
	r, err := app.toolSearchFlights(ctx, map[string]any{
		"to":          "LIN",
		"depart_date": "2026-06-05",
	})
	if err != nil {
		t.Fatal(err)
	}
	offers := r.(map[string]any)["offers"].([]FlightOffer)
	if len(offers) != 2 {
		t.Fatalf("want 2 offers, got %d", len(offers))
	}
	// Sorted cheapest-first: easyJet 85.00 should be index 0.
	if offers[0].CarrierCode != "U2" || offers[0].TotalAmountCents != 8500 {
		t.Errorf("sort/parse wrong: %+v", offers[0])
	}
	// Verify from defaulted to settings.home_airport.
	in := fake.integCalls[0].Input
	slices := in["slices"].([]map[string]any)
	if slices[0]["origin"] != "CDG" {
		t.Errorf("default home_airport not applied: %+v", slices)
	}
}

func TestUnit_AvailableConnections_FiltersByProvider(t *testing.T) {
	ctx, fake := newCtx(t)
	app := &App{}
	fake.connList = []sdk.PlatformConnection{
		{ID: 1, AppSlug: "duffel", Name: "duffel-prod", Status: "active", ProjectID: "test-proj"},
		{ID: 2, AppSlug: "google-places", Name: "places-key", Status: "active", ProjectID: "test-proj"},
		{ID: 3, AppSlug: "duffel", Name: "duffel-sandbox", Status: "active", ProjectID: "test-proj"},
	}
	out, err := app.toolAvailableConnections(ctx, map[string]any{"provider": "duffel"})
	if err != nil {
		t.Fatal(err)
	}
	conns := out.(map[string]any)["connections"]
	// Should only include duffel (2 of 3).
	b, _ := json.Marshal(conns)
	if !strings.Contains(string(b), "duffel-prod") || strings.Contains(string(b), "places-key") {
		t.Errorf("filter wrong: %s", b)
	}
}

func TestManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "trips" {
		t.Errorf("manifest name=%q", m.Name)
	}
	if len(app.MCPTools()) < 20 {
		t.Errorf("expected ≥20 tools, got %d", len(app.MCPTools()))
	}
}

// ─── helpers ─────────────────────────────────────────────────────

func newCtx(t *testing.T) (*sdk.AppCtx, *fakeCalendar) {
	t.Helper()
	rec := tk.NewEmitRecorder()
	fake := newFakeCalendar()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEmitter(rec),
		tk.WithPlatform(fake),
	)
	globalCtx = ctx
	return ctx, fake
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

func mustCreateTrip(t *testing.T, app *App, ctx *sdk.AppCtx, name string, sync bool) Trip {
	t.Helper()
	r, err := app.toolTripsCreate(ctx, map[string]any{
		"name": name, "start_at": "2026-06-05T00:00:00Z", "end_at": "2026-06-08T00:00:00Z",
		"sync_calendar": sync,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.(Trip)
}

func mustAddDest(t *testing.T, app *App, ctx *sdk.AppCtx, tripID int64, name string) Destination {
	t.Helper()
	r, err := app.toolDestinationsAdd(ctx, map[string]any{
		"trip_id": float64(tripID), "place_name": name,
		"arrive_at": "2026-06-05T10:00:00Z", "depart_at": "2026-06-08T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.(Destination)
}
