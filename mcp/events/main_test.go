package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func setupEvents(t *testing.T) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "events-test")
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "events.db")+"?_pragma=foreign_keys(on)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(8)
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	manifest := (&App{}).Manifest()
	globalCtx = sdk.NewAppCtxForTest(&manifest, database, sdk.Config{}, nil, nil)
	t.Cleanup(func() { database.Close(); globalCtx = nil })
}

func testEvent(t *testing.T, values map[string]any) *Event {
	t.Helper()
	if values == nil {
		values = map[string]any{}
	}
	if _, ok := values["title"]; !ok {
		values["title"] = "Friday show"
	}
	e, err := createEvent(values)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func testType(t *testing.T, eventID int64, values map[string]any) *TicketType {
	t.Helper()
	if values == nil {
		values = map[string]any{}
	}
	values["event_id"], values["name"] = eventID, "General admission"
	typeRow, err := createTicketType(values)
	if err != nil {
		t.Fatal(err)
	}
	return typeRow
}

func testBuyer(eventID, typeID int64) issueTicketsInput {
	return issueTicketsInput{EventID: eventID, TicketTypeID: typeID, BuyerName: "Visitor", BuyerEmail: "visitor@example.com", Source: "public", Quantity: 1}
}

func assertNoOrders(t *testing.T) {
	t.Helper()
	for _, table := range []string{"tickets", "orders"} {
		var count int
		if err := db().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rejected issuance left %d %s", count, table)
		}
	}
}

func TestTicketIssuanceRejectsInvalidTypesAndPublicPaymentBypasses(t *testing.T) {
	for _, scenario := range []struct {
		name                  string
		values                map[string]any
		other, omit, external bool
	}{
		{name: "another event", other: true},
		{name: "paused", values: map[string]any{"status": "paused"}},
		{name: "archived", values: map[string]any{"status": "archived"}},
		{name: "sold out", values: map[string]any{"status": "sold_out"}},
		{name: "before sales", values: map[string]any{"sales_start_at": time.Now().Add(time.Hour).Format(time.RFC3339)}},
		{name: "after sales", values: map[string]any{"sales_end_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}},
		{name: "paid", values: map[string]any{"price_cents": 2500}},
		{name: "omitted paid type", values: map[string]any{"price_cents": 2500}, omit: true},
		{name: "external free", external: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			setupEvents(t)
			e := testEvent(t, map[string]any{"status": "published", "visibility": "public"})
			typeEvent := e
			if scenario.other {
				typeEvent = testEvent(t, map[string]any{"title": "Other private event"})
			}
			tt := testType(t, typeEvent.ID, scenario.values)
			if scenario.external {
				if _, err := updateEvent(e.ID, map[string]any{"external_checkout_url": "https://example.com/checkout"}); err != nil {
					t.Fatal(err)
				}
			}
			input := testBuyer(e.ID, tt.ID)
			if scenario.omit {
				input.TicketTypeID = 0
			}
			if _, err := issueTickets(input); err == nil {
				t.Fatal("invalid registration accepted")
			}
			assertNoOrders(t)
		})
	}
}

func TestFreePublicAndManualPaidTickets(t *testing.T) {
	setupEvents(t)
	e := testEvent(t, map[string]any{"status": "published", "visibility": "public"})
	tt := testType(t, e.ID, nil)
	if tickets, err := issueTickets(testBuyer(e.ID, tt.ID)); err != nil || len(tickets) != 1 {
		t.Fatalf("free registration: %v %v", tickets, err)
	}
	paid := testType(t, e.ID, map[string]any{"price_cents": 2500})
	input := testBuyer(e.ID, paid.ID)
	input.Source = "manual"
	if _, err := issueTickets(input); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := db().QueryRow("SELECT SUM(total_cents) FROM orders").Scan(&total); err != nil || total != 2500 {
		t.Fatalf("manual amount: %d %v", total, err)
	}
}

func TestConcurrentCapacityAcrossConnections(t *testing.T) {
	for _, byType := range []bool{false, true} {
		t.Run(fmt.Sprintf("ticket-type=%v", byType), func(t *testing.T) {
			setupEvents(t)
			capacity := 3
			values := map[string]any{"status": "published", "visibility": "public"}
			if !byType {
				values["capacity"] = capacity
			}
			e := testEvent(t, values)
			var typeID int64
			if byType {
				typeID = testType(t, e.ID, map[string]any{"capacity": capacity}).ID
			}
			var wg sync.WaitGroup
			results := make(chan error, 20)
			start := make(chan struct{})
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; _, err := issueTickets(testBuyer(e.ID, typeID)); results <- err }()
			}
			close(start)
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else if !strings.Contains(err.Error(), "capacity exceeded") {
					t.Fatalf("unexpected failure: %v", err)
				}
			}
			if success != capacity {
				t.Fatalf("got %d successful reservations; want %d", success, capacity)
			}
			var orders, tickets int
			db().QueryRow("SELECT COUNT(*) FROM orders").Scan(&orders)
			db().QueryRow("SELECT COUNT(*) FROM tickets").Scan(&tickets)
			if orders != capacity || tickets != capacity {
				t.Fatalf("orders=%d tickets=%d", orders, tickets)
			}
		})
	}
}

func TestBatchIssuanceRollsBackOnInsertFailure(t *testing.T) {
	setupEvents(t)
	e := testEvent(t, map[string]any{"status": "published", "visibility": "public"})
	_, err := db().Exec(`CREATE TRIGGER fail_second_ticket BEFORE INSERT ON tickets WHEN (SELECT COUNT(*) FROM tickets) > 0 BEGIN SELECT RAISE(ABORT, 'simulated failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	input := testBuyer(e.ID, 0)
	input.Quantity = 2
	if _, err := issueTickets(input); err == nil {
		t.Fatal("expected insertion failure")
	}
	assertNoOrders(t)
}

func publicRequest(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	(&App{}).handlePublic(w, r)
	return w
}

func TestPublicPageFormsAndSafeJSON(t *testing.T) {
	setupEvents(t)
	e := testEvent(t, map[string]any{"title": "Friday <script>alert(1)</script>", "slug": "friday", "status": "published", "visibility": "public", "description": "A lovely show"})
	_, err := createSlot(map[string]any{"event_id": e.ID, "performer_name": "Alice", "notes": "PRIVATE-SCHEDULE-NOTE"})
	if err != nil {
		t.Fatal(err)
	}
	w := publicRequest("GET", "/public/friday", "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"data-action=\"apply\"", "data-action=\"register\"", "Alice", "&lt;script&gt;"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, path := range []string{"/public/friday", "/public/friday?format=json"} {
		w := publicRequest("GET", path, "")
		for _, forbidden := range []string{"PRIVATE-SCHEDULE-NOTE", "application_id", "slot_count", "application_count", "project_id", "<script>alert(1)</script>"} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatalf("public response exposes %s", forbidden)
			}
		}
	}
	r := httptest.NewRequest("GET", "/public/friday", nil)
	r.Header.Set("Accept", "application/json")
	w = httptest.NewRecorder()
	(&App{}).handlePublic(w, r)
	if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal("JSON content negotiation failed")
	}
	w = publicRequest("POST", "/public/friday/register", `{"buyer_name":"Visitor","buyer_email":"visitor@example.com","quantity":1}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ticket_codes") {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	w = publicRequest("POST", "/public/friday/apply", `{"applicant_name":"Performer","email":"performer@example.com"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Application received") || strings.Contains(w.Body.String(), "reviewer_notes") {
		t.Fatalf("apply: %s", w.Body.String())
	}
}

func TestPublicRegistrationCannotSpoofSourceOrBypassCheckout(t *testing.T) {
	setupEvents(t)
	e := testEvent(t, map[string]any{"status": "published", "visibility": "public", "external_checkout_url": "https://example.com/checkout"})
	w := publicRequest("GET", "/public/"+e.Slug, "")
	if !strings.Contains(w.Body.String(), `href="https://example.com/checkout"`) || strings.Contains(w.Body.String(), `data-action="register"`) {
		t.Fatal("external checkout page incorrect")
	}
	w = publicRequest("POST", "/public/"+e.Slug+"/register", `{"buyer_name":"Visitor","buyer_email":"visitor@example.com","source":"manual"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("checkout bypass: %d", w.Code)
	}
	assertNoOrders(t)
	if w := publicRequest("POST", "/public/"+e.Slug+"/apply", "null"); w.Code != 400 {
		t.Fatal("null JSON not rejected")
	}
	for _, values := range []map[string]any{{"status": "draft"}, {"status": "published", "visibility": "private"}} {
		if _, err := updateEvent(e.ID, values); err != nil {
			t.Fatal(err)
		}
		if w := publicRequest("GET", "/public/"+e.Slug, ""); w.Code != 404 {
			t.Fatal("non-public event exposed")
		}
	}
}

func TestEventEditorPersistsFieldsAndNormalizesTimezone(t *testing.T) {
	setupEvents(t)
	venue, err := createVenue(map[string]any{"name": "The room"})
	if err != nil {
		t.Fatal(err)
	}
	e := testEvent(t, nil)
	body := fmt.Sprintf(`{"title":"Updated show","description":"New description","timezone":"Europe/Madrid","starts_at":"2026-10-10T19:30","ends_at":"2026-10-10T21:30","capacity":40,"venue_id":%d,"external_checkout_url":"https://example.com/tickets"}`, venue.ID)
	w := httptest.NewRecorder()
	(&App{}).handleEventsItem(w, httptest.NewRequest("PATCH", fmt.Sprintf("/shows/%d", e.ID), strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var saved Event
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Title != "Updated show" || saved.Description != "New description" || saved.StartsAt != "2026-10-10T17:30:00Z" || saved.EndsAt != "2026-10-10T19:30:00Z" || saved.Capacity != 40 || saved.VenueID == nil || *saved.VenueID != venue.ID || saved.ExternalCheckoutURL != "https://example.com/tickets" {
		t.Fatalf("incorrect event: %+v", saved)
	}
	for _, bad := range []map[string]any{{"title": " "}, {"starts_at": "invalid"}, {"ends_at": "2020-01-01T00:00:00Z"}, {"capacity": -1}, {"timezone": "not/a/timezone"}, {"external_checkout_url": "javascript:alert(1)"}, {"venue_id": 999}, {"starts_at": "2026-03-29T02:30"}} {
		if _, err := updateEvent(e.ID, bad); err == nil {
			t.Fatalf("accepted invalid update: %v", bad)
		}
	}
}

func TestCheckInRejectsInactiveAndPreservesFirstTimestamp(t *testing.T) {
	setupEvents(t)
	e := testEvent(t, map[string]any{"status": "published", "visibility": "public"})
	ts, err := issueTickets(testBuyer(e.ID, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkInTicket(ts[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	db().Exec("UPDATE tickets SET checked_in_at='2020-01-01T00:00:00Z' WHERE id=?", ts[0].ID)
	again, err := checkInTicket(ts[0].ID, "")
	if err != nil || again.CheckedInAt != "2020-01-01T00:00:00Z" {
		t.Fatal("repeat check-in changed timestamp")
	}
	db().Exec("UPDATE tickets SET status='voided', checkin_status='not_checked_in' WHERE id=?", ts[0].ID)
	if _, err := checkInTicket(ts[0].ID, ""); err == nil {
		t.Fatal("voided ticket admitted")
	}
}

func TestManifestPublicRouteAndVersion(t *testing.T) {
	file, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sdk.ParseManifest(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []sdk.Manifest{*manifest, (&App{}).Manifest()} {
		if m.Version != "0.2.0" {
			t.Fatalf("version mismatch: %s", m.Version)
		}
		public := false
		for _, r := range m.Provides.HTTPRoutes {
			if r.NoAuth && r.Prefix == "/" {
				t.Fatal("administrative routes must require auth")
			}
			if r.NoAuth && r.Prefix == "/public/" {
				public = true
			}
		}
		if !public {
			t.Fatal("gateway cannot serve public attendee page anonymously")
		}
	}
}

func TestAppRoutesCoexistWithSDKEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	for _, path := range []string{"/events", "/mcp", "/health", "/ui/"} {
		mux.HandleFunc(path, func(http.ResponseWriter, *http.Request) {})
	}
	for _, route := range (&App{}).HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
}
