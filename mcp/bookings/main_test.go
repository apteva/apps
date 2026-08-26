package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type bookingPlatform struct {
	tk.BasePlatformClient
	mu          sync.Mutex
	calls       []string
	events      []map[string]any
	nextEventID int64
	nextRoomID  int64
}

func newBookingPlatform() *bookingPlatform {
	return &bookingPlatform{nextEventID: 100, nextRoomID: 200}
}

func (p *bookingPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, appName+":"+tool)
	var response any = map[string]any{}
	switch appName + ":" + tool {
	case "calendar:events_list":
		response = map[string]any{"events": append([]map[string]any(nil), p.events...)}
	case "calendar:events_create":
		p.nextEventID++
		id := p.nextEventID
		p.events = append(p.events, map[string]any{
			"id": id, "event_id": id, "start_at": input["start_at"], "end_at": input["end_at"],
		})
		response = map[string]any{"id": id}
	case "calendar:events_update":
		id := int64FromAny(input["event_id"])
		for _, event := range p.events {
			if int64FromAny(event["id"]) == id {
				event["start_at"], event["end_at"] = input["start_at"], input["end_at"]
			}
		}
	case "calendar:events_delete":
		id := int64FromAny(input["event_id"])
		kept := p.events[:0]
		for _, event := range p.events {
			if int64FromAny(event["id"]) != id {
				kept = append(kept, event)
			}
		}
		p.events = kept
	case "calendar:calendars_list":
		response = map[string]any{"calendars": []map[string]any{{"id": 5, "name": "Work", "enabled": true}}}
	case "calendar:calendars_create":
		response = map[string]any{"id": 6}
	case "calendar:calendars_update":
		response = map[string]any{"updated": true}
	case "calls:calls_create_room":
		p.nextRoomID++
		response = map[string]any{"room": map[string]any{"id": p.nextRoomID}, "host_join_url": "https://calls.test/host-secret"}
	case "calls:calls_create_join_token":
		response = map[string]any{"join_url": "https://calls.test/guest"}
	case "calls:calls_end_room":
		response = map[string]any{"ended": true}
	case "crm:contacts_upsert_by_channel":
		response = map[string]any{"contact": map[string]any{"id": 300}}
	}
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func int64FromAny(value any) int64 {
	switch value := value.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	}
	return 0
}

func newBookingsTest(t *testing.T, platform sdk.PlatformClient) (*App, *sdk.AppCtx) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	app := &App{}
	globalCtx = ctx
	publicRateLimit = newRequestLimiter()
	return app, ctx
}

func TestManifestDeclaresPublicGatewayRoutes(t *testing.T) {
	manifest := (&App{}).Manifest()
	public := map[string]bool{}
	for _, route := range manifest.Provides.HTTPRoutes {
		if route.NoAuth {
			public[route.Prefix] = true
		}
	}
	for _, prefix := range []string{"/public/", "/b/"} {
		if !public[prefix] {
			t.Fatalf("manifest route %q must declare no_auth for the server proxy", prefix)
		}
	}
}

func TestPublicManagementURLsRetainProjectScope(t *testing.T) {
	_, ctx := newBookingsTest(t, newBookingPlatform())
	manageURL := publicManageURL(ctx, "project with spaces", "manage-token")
	if !strings.Contains(manageURL, "/b/manage-token?project_id=project+with+spaces") {
		t.Fatalf("manage URL lost project scope: %s", manageURL)
	}

	booking := &Booking{
		ProjectID:         "test-proj",
		Status:            "confirmed",
		StartAt:           futureSlot().Format(time.RFC3339),
		CancellationToken: "cancel-token",
		RescheduleToken:   "reschedule-token",
	}
	bookingType := &BookingType{Title: "Client call", Slug: "client-call"}
	page := managePageHTML(booking, bookingType)
	for _, action := range []string{
		"/b/reschedule-token/reschedule?project_id=test-proj",
		"/b/cancel-token/cancel?project_id=test-proj",
	} {
		if !strings.Contains(page, action) {
			t.Fatalf("manage page action lost project scope %q", action)
		}
	}
	reschedulePage := reschedulePageHTML("test-proj", "reschedule-token", bookingType)
	if !strings.Contains(reschedulePage, "/reschedule?project_id='+encodeURIComponent(PID)") {
		t.Fatal("reschedule submission lost project scope")
	}
}

func createTestType(t *testing.T, app *App, ctx *sdk.AppCtx, calls bool) *BookingType {
	t.Helper()
	location := "external_url"
	if calls {
		location = "calls"
	}
	out, err := app.toolBookingTypesCreate(ctx, map[string]any{
		"_project_id":             "test-proj",
		"title":                   "Client call",
		"slug":                    "client-call",
		"duration_minutes":        30,
		"timezone":                "UTC",
		"location_kind":           location,
		"location_value":          "https://meet.test/client",
		"calls_enabled":           calls,
		"crm_enabled":             false,
		"destination_calendar_id": 5,
		"calendar_ids":            []any{float64(5)},
		"availability_rules": map[string]any{
			"minimum_notice_minutes": 0,
			"booking_horizon_days":   30,
			"working_hours": map[string]any{
				"mon": map[string]any{"start": "00:00", "end": "23:59"},
				"tue": map[string]any{"start": "00:00", "end": "23:59"},
				"wed": map[string]any{"start": "00:00", "end": "23:59"},
				"thu": map[string]any{"start": "00:00", "end": "23:59"},
				"fri": map[string]any{"start": "00:00", "end": "23:59"},
				"sat": map[string]any{"start": "00:00", "end": "23:59"},
				"sun": map[string]any{"start": "00:00", "end": "23:59"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create booking type: %v", err)
	}
	return out.(map[string]any)["booking_type"].(*BookingType)
}

func futureSlot() time.Time {
	now := time.Now().UTC().Add(24 * time.Hour)
	return time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
}

func bookingArgs(bt *BookingType, start time.Time, key string) map[string]any {
	return map[string]any{
		"_project_id": "test-proj", "booking_type_id": bt.ID,
		"start_at": start.Format(time.RFC3339), "invitee_name": "Alice",
		"invitee_email": "alice@example.com", "idempotency_key": key,
	}
}

func TestBookingCreateRevalidatesAndIsIdempotent(t *testing.T) {
	platform := newBookingPlatform()
	app, ctx := newBookingsTest(t, platform)
	bt := createTestType(t, app, ctx, false)
	start := futureSlot()
	first, err := app.toolBookingsCreate(ctx, bookingArgs(bt, start, "request-1"))
	if err != nil {
		t.Fatalf("create booking: %v", err)
	}
	booking := first.(map[string]any)["booking"].(*Booking)
	if booking.CalendarEventID == 0 {
		t.Fatal("calendar event was not persisted")
	}
	second, err := app.toolBookingsCreate(ctx, bookingArgs(bt, start, "request-1"))
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if second.(map[string]any)["booking"].(*Booking).ID != booking.ID {
		t.Fatal("idempotent replay created another booking")
	}
	if _, err := app.toolBookingsCreate(ctx, bookingArgs(bt, start, "request-2")); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestPublicBookingResponseDoesNotLeakHostURLOrTokens(t *testing.T) {
	platform := newBookingPlatform()
	app, ctx := newBookingsTest(t, platform)
	_ = createTestType(t, app, ctx, true)
	body := fmt.Sprintf(`{"start_at":%q,"invitee_name":"Alice","invitee_email":"alice@example.com","idempotency_key":"public-1"}`, futureSlot().Format(time.RFC3339))
	req := httptest.NewRequest(http.MethodPost, "/public/client-call/book?project_id=test-proj", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	app.handlePublic(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	response := rec.Body.String()
	for _, secret := range []string{"host_join_url", "host-secret", "cancellation_token", "reschedule_token"} {
		if strings.Contains(response, secret) {
			t.Fatalf("public response leaked %q: %s", secret, response)
		}
	}
	if !strings.Contains(response, "calls_guest_join_url") || !strings.Contains(response, "public_manage_url") {
		t.Fatalf("public response missing guest fields: %s", response)
	}
}

func TestBookingActionsRejectGET(t *testing.T) {
	app, _ := newBookingsTest(t, newBookingPlatform())
	req := httptest.NewRequest(http.MethodGet, "/bookings/1/cancel?project_id=test-proj", nil)
	rec := httptest.NewRecorder()
	app.handleBookingItem(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405", rec.Code)
	}
}

func TestCancellationEndsCallsAndDeletesCalendarEvent(t *testing.T) {
	platform := newBookingPlatform()
	app, ctx := newBookingsTest(t, platform)
	bt := createTestType(t, app, ctx, true)
	out, err := app.toolBookingsCreate(ctx, bookingArgs(bt, futureSlot(), "cancel-1"))
	if err != nil {
		t.Fatal(err)
	}
	b := out.(map[string]any)["booking"].(*Booking)
	if _, err := app.toolBookingsCancel(ctx, map[string]any{"_project_id": "test-proj", "id": b.ID}); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadBookingByID(ctx, "test-proj", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "cancelled" || loaded.CalendarEventID != 0 || loaded.CallsRoomID != 0 || loaded.CallsHostJoinURL != "" {
		t.Fatalf("resources were not cleared: %+v", loaded)
	}
	joined := strings.Join(platform.calls, ",")
	if !strings.Contains(joined, "calendar:events_delete") || !strings.Contains(joined, "calls:calls_end_room") {
		t.Fatalf("cleanup calls missing: %s", joined)
	}
}

func TestCallsRoomIsEndedWhenPersistenceFails(t *testing.T) {
	platform := newBookingPlatform()
	app, ctx := newBookingsTest(t, platform)
	bt := createTestType(t, app, ctx, true)
	if _, err := ctx.AppDB().Exec(`
		CREATE TRIGGER fail_calls_persistence
		BEFORE UPDATE OF calls_room_id ON bookings
		BEGIN SELECT RAISE(FAIL, 'forced calls persistence failure'); END;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolBookingsCreate(ctx, bookingArgs(bt, futureSlot(), "calls-failure")); err == nil {
		t.Fatal("expected the forced persistence failure")
	}
	if !strings.Contains(strings.Join(platform.calls, ","), "calls:calls_end_room") {
		t.Fatalf("created Calls room was not ended: %v", platform.calls)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM bookings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed booking was retained: count=%d", count)
	}
}

func TestDefaultDestinationCalendarIsIncludedInConflictChecks(t *testing.T) {
	platform := newBookingPlatform()
	app, ctx := newBookingsTest(t, platform)
	bt := createTestType(t, app, ctx, false)
	bt.DestinationCalendarID = 0
	if _, err := ctx.AppDB().Exec(`UPDATE booking_types SET destination_calendar_id=0 WHERE id=?`, bt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolBookingsFindSlots(ctx, map[string]any{
		"_project_id": "test-proj", "booking_type_id": bt.ID, "limit": 1,
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadBookingTypeByID(ctx, "test-proj", bt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DestinationCalendarID != 6 {
		t.Fatalf("default destination was not resolved and persisted: %d", reloaded.DestinationCalendarID)
	}
	joined := strings.Join(platform.calls, ",")
	if !strings.Contains(joined, "calendar:calendars_create") || !strings.Contains(joined, "calendar:events_list") {
		t.Fatalf("expected destination setup and conflict check: %s", joined)
	}
}

func TestTimezoneAndZeroNoticeRules(t *testing.T) {
	rules := parseRules(json.RawMessage(`{"minimum_notice_minutes":0,"booking_horizon_days":7,"working_hours":{"mon":{"start":"09:00","end":"17:00"}}}`))
	if rules.MinimumNoticeMins != 0 {
		t.Fatalf("zero notice became %d", rules.MinimumNoticeMins)
	}
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	// 08:00 UTC is 09:00 in Madrid during winter.
	start := time.Date(2026, time.January, 5, 8, 0, 0, 0, time.UTC)
	if !withinWorkingHours(start, start.Add(30*time.Minute), loc, rules.WorkingHours) {
		t.Fatal("timezone-aware working hours rejected a valid Madrid slot")
	}
}
