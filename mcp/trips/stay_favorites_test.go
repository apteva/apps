package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func favoriteFixture(t *testing.T) (*App, *sdk.AppCtx, *fakeCalendar, Trip, Destination) {
	t.Helper()
	ctx, fake := newCtx(t)
	app := &App{}
	out, err := app.toolTripsCreate(ctx, map[string]any{"name": "Japan", "start_at": "2027-03-20", "end_at": "2027-03-30"})
	if err != nil {
		t.Fatal(err)
	}
	trip := out.(Trip)
	out, err = app.toolDestinationsAdd(ctx, map[string]any{"trip_id": trip.ID, "place_name": "Kyoto", "arrive_at": "2027-03-24", "depart_at": "2027-03-28"})
	if err != nil {
		t.Fatal(err)
	}
	return app, ctx, fake, trip, out.(Destination)
}

func addFavorite(t *testing.T, app *App, ctx *sdk.AppCtx, dest Destination, name string, top bool) StayFavorite {
	t.Helper()
	out, err := app.toolStayFavoritesAdd(ctx, map[string]any{"destination_id": dest.ID, "name": name, "kind": "airbnb", "price_amount": float64(12500), "price_basis": "per_night", "listing_url": "https://www.airbnb.com/rooms/123", "notes": "Near station", "top_choice": top})
	if err != nil {
		t.Fatal(err)
	}
	return out.(StayFavorite)
}

func TestFavoritesPlanningAndSelection(t *testing.T) {
	app, ctx, fake, trip, dest := favoriteFixture(t)
	eventsBefore := fake.countCalls("events_create")
	first := addFavorite(t, app, ctx, dest, "Kyoto house", true)
	second := addFavorite(t, app, ctx, dest, "Hotel alternative", true)
	f, _ := readFavorite(ctx, first.ID)
	if f.TopChoice {
		t.Fatal("previous top choice not cleared")
	}
	dashOut, err := app.toolDashboard(ctx, map[string]any{"trip_id": trip.ID})
	if err != nil {
		t.Fatal(err)
	}
	dash := dashOut.(TripDashboard)
	if len(dash.StayFavorites) != 2 || dash.Budget.TotalPlanned != 0 || len(dash.Accommodations) != 0 {
		t.Fatalf("shortlist changed itinerary or budget: %+v", dash)
	}
	if fake.countCalls("events_create") != eventsBefore {
		t.Fatal("shortlist created calendar events")
	}
	listOut, err := app.toolStayFavoritesList(ctx, map[string]any{"destination_id": dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if listOut.(map[string]any)["favorites"].([]StayFavorite)[0].ID != second.ID {
		t.Fatal("top choice must sort first")
	}
	out, err := app.toolStayFavoritesSelect(ctx, map[string]any{"id": first.ID})
	if err != nil {
		t.Fatal(err)
	}
	stay := out.(Accommodation)
	if stay.Booked || stay.DestinationID != dest.ID || stay.CheckInAt != "2027-03-24T15:00:00Z" || stay.CheckOutAt != "2027-03-28T11:00:00Z" || stay.CostEstimated == nil || *stay.CostEstimated != 50000 || !strings.Contains(stay.Notes, first.ListingURL) {
		t.Fatalf("incorrect selected stay: %+v", stay)
	}
	out, err = app.toolStayFavoritesSelect(ctx, map[string]any{"id": first.ID, "cost_estimated": int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if out.(Accommodation).ID != stay.ID || *out.(Accommodation).CostEstimated != 50000 {
		t.Fatal("selection retry duplicated or changed stay")
	}
	if fake.countCalls("events_create") != eventsBefore+1 {
		t.Fatal("selection must mirror exactly once")
	}
	dashOut, err = app.toolDashboard(ctx, map[string]any{"trip_id": trip.ID})
	if err != nil {
		t.Fatal(err)
	}
	dash = dashOut.(TripDashboard)
	if dash.Budget.TotalPlanned != 50000 || len(dash.StayFavorites) != 2 || len(dash.Accommodations) != 1 {
		t.Fatalf("incorrect selected budget: %+v", dash)
	}
	f, err = readFavorite(ctx, first.ID)
	if err != nil || f.AccommodationID == nil || *f.AccommodationID != stay.ID {
		t.Fatal("favorite missing selected stay link")
	}
	if _, err = app.toolStayFavoritesUpdate(ctx, map[string]any{"id": first.ID, "name": "New option name", "price_amount": nil}); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := readAccommodation(ctx, stay.ID)
	if unchanged.Name != stay.Name {
		t.Fatal("editing favorite changed chosen accommodation")
	}
	if _, err = app.toolStayFavoritesDelete(ctx, map[string]any{"id": first.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = readAccommodation(ctx, stay.ID); err != nil {
		t.Fatal("deleting favorite deleted itinerary stay")
	}
}

func TestFavoritesDateAndPriceOverrides(t *testing.T) {
	app, ctx, fake, _, dest := favoriteFixture(t)
	f := addFavorite(t, app, ctx, dest, "Nightly", false)
	if _, err := app.toolStayFavoritesSelect(ctx, map[string]any{"id": f.ID, "check_in_at": nil, "check_out_at": nil}); err == nil {
		t.Fatal("nightly amount without dates treated as total")
	}
	events := fake.countCalls("events_create")
	out, err := app.toolStayFavoritesSelect(ctx, map[string]any{"id": f.ID, "check_in_at": nil, "check_out_at": nil, "cost_estimated": nil})
	if err != nil {
		t.Fatal(err)
	}
	stay := out.(Accommodation)
	if stay.CheckInAt != "" || stay.CheckOutAt != "" || stay.CostEstimated != nil || fake.countCalls("events_create") != events {
		t.Fatal("unknown dates/price should produce an unscheduled idea")
	}
	other := addFavorite(t, app, ctx, dest, "Override", false)
	out, err = app.toolStayFavoritesSelect(ctx, map[string]any{"id": other.ID, "check_in_at": "2027-03-25T15:00:00Z", "check_out_at": "2027-03-27T11:00:00Z", "cost_estimated": float64(30000), "currency": "USD"})
	if err != nil {
		t.Fatal(err)
	}
	stay = out.(Accommodation)
	if stay.Currency != "USD" || *stay.CostEstimated != 30000 {
		t.Fatal("overrides ignored")
	}
	another := addFavorite(t, app, ctx, dest, "Currency guard", false)
	if _, err := app.toolStayFavoritesSelect(ctx, map[string]any{"id": another.ID, "currency": "USD"}); err == nil {
		t.Fatal("changed currency silently without a new price")
	}
	if _, err := app.toolStayFavoritesSelect(ctx, map[string]any{"id": another.ID, "check_in_at": "bad"}); err == nil {
		t.Fatal("invalid date accepted")
	}
}

func TestFavoritesIsolationAndValidation(t *testing.T) {
	app, ctx, _, trip, dest := favoriteFixture(t)
	cases := []map[string]any{
		{"name": ""}, {"kind": "wrong"}, {"price_amount": float64(-1)}, {"price_amount": 1.5}, {"price_amount": "123"},
		{"listing_url": "javascript:alert(1)"}, {"photo_url": "file:///tmp/photo"}, {"price_basis": "weekly"}, {"currency": "bad!"}, {"top_choice": "yes"},
	}
	for _, args := range cases {
		input := map[string]any{"destination_id": dest.ID, "name": "Test"}
		for k, v := range args {
			input[k] = v
		}
		if _, err := app.toolStayFavoritesAdd(ctx, input); err == nil {
			t.Errorf("accepted invalid fields: %+v", args)
		}
	}
	f := addFavorite(t, app, ctx, dest, "Private", true)
	if _, err := app.toolStayFavoritesUpdate(ctx, map[string]any{"id": f.ID, "destination_id": dest.ID}); err == nil {
		t.Fatal("allowed moving shortlist item")
	}
	if _, err := app.toolStayFavoritesList(ctx, map[string]any{}); err == nil {
		t.Fatal("allowed unscoped list")
	}
	if _, err := app.toolStayFavoritesList(ctx, map[string]any{"trip_id": trip.ID + 1, "destination_id": dest.ID}); err == nil {
		t.Fatal("allowed mismatched trip")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE trips SET project_id='other-project' WHERE id=?`, trip.ID); err != nil {
		t.Fatal(err)
	}
	for name, fn := range map[string]func(*sdk.AppCtx, map[string]any) (any, error){"read": app.toolStayFavoritesList, "update": app.toolStayFavoritesUpdate, "delete": app.toolStayFavoritesDelete, "select": app.toolStayFavoritesSelect, "add": app.toolStayFavoritesAdd} {
		if _, err := fn(ctx, map[string]any{"id": f.ID, "trip_id": trip.ID, "destination_id": dest.ID, "name": "Try"}); err == nil {
			t.Errorf("%s accessed another project", name)
		}
	}
}

func TestFavoritesConcurrentSelectionAndCascades(t *testing.T) {
	app, ctx, _, trip, dest := favoriteFixture(t)
	f := addFavorite(t, app, ctx, dest, "One stay", false)
	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := app.toolStayFavoritesSelect(ctx, map[string]any{"id": f.ID})
			if err != nil {
				errs <- err
				return
			}
			ids <- out.(Accommodation).ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var stayID int64
	for id := range ids {
		if stayID != 0 && id != stayID {
			t.Fatal("concurrent selections duplicated stay")
		}
		stayID = id
	}
	if _, err := app.toolDestinationsDelete(ctx, map[string]any{"id": dest.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := readFavorite(ctx, f.ID); err == nil {
		t.Fatal("destination did not cascade to favorites")
	}
	if _, err := readAccommodation(ctx, stayID); err != nil {
		t.Fatal("destination delete lost selected stay")
	}
	out, err := app.toolDestinationsAdd(ctx, map[string]any{"trip_id": trip.ID, "place_name": "Tokyo"})
	if err != nil {
		t.Fatal(err)
	}
	next := addFavorite(t, app, ctx, out.(Destination), "Tokyo option", true)
	if _, err := app.toolTripsDelete(ctx, map[string]any{"id": trip.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := readFavorite(ctx, next.ID); err == nil {
		t.Fatal("trip did not cascade to favorites")
	}
}

func TestFavoritesMCPAndHTTP(t *testing.T) {
	app, ctx, _, _, dest := favoriteFixture(t)
	var add sdk.Tool
	manifest := app.Manifest()
	for _, tool := range app.favoriteTools() {
		found := false
		for _, declared := range manifest.Provides.MCPTools {
			if declared.Name == tool.Name {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s missing from manifest", tool.Name)
		}
		if tool.Name == "stay_favorites_add" {
			add = tool
		}
	}
	out, err := add.Handler(ctx, map[string]any{"destination_id": dest.ID, "name": "MCP hotel", "price_amount": int64(0)})
	if err != nil {
		t.Fatal(err)
	}
	f := out.(StayFavorite)
	if f.PriceAmount == nil || *f.PriceAmount != 0 {
		t.Fatal("zero price not preserved")
	}
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
		return r
	}
	r := request("GET", "/stay-favorites?destination_id="+itoa(dest.ID), "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "MCP hotel") {
		t.Fatalf("list: %d %s", r.Code, r.Body.String())
	}
	r = request("PATCH", "/stay-favorites/"+itoa(f.ID), `{"name":"Updated"}`)
	if r.Code != 200 {
		t.Fatalf("update: %s", r.Body.String())
	}
	r = request("GET", "/stay-favorites/"+itoa(f.ID)+"/select", "")
	if r.Code != 405 {
		t.Fatal("GET selection mutated state")
	}
	r = request("POST", "/stay-favorites/"+itoa(f.ID)+"/select", `{}`)
	if r.Code != 200 {
		t.Fatalf("select: %s", r.Body.String())
	}
	var stay Accommodation
	if err := json.Unmarshal(r.Body.Bytes(), &stay); err != nil {
		t.Fatal(err)
	}
	if stay.ID == 0 || stay.Name != "Updated" {
		t.Fatal("HTTP select returned wrong stay")
	}
	r = request("DELETE", "/stay-favorites/"+itoa(f.ID), "")
	if r.Code != 204 {
		t.Fatalf("delete: %s", r.Body.String())
	}
	for _, body := range []string{`not json`, `null`, `[]`} {
		if r := request("POST", "/stay-favorites", body); r.Code != 400 {
			t.Errorf("invalid JSON accepted: %s", body)
		}
	}
}

func TestStayNightsCivilDates(t *testing.T) {
	for _, tt := range []struct {
		start, end string
		want       int64
	}{
		{"2027-03-27T15:00:00+01:00", "2027-03-29T11:00:00+02:00", 2},
		{"2027-03-01T15:00:00Z", "2027-03-02T11:00:00Z", 1},
		{"", "", 0},
	} {
		if got := stayNights(tt.start, tt.end); got != tt.want {
			t.Errorf("nights %s %s=%d, want %d", tt.start, tt.end, got, tt.want)
		}
	}
}

// Applying the new migration must preserve real stays from existing installs.
func TestFavoritesUpgradePreservesExistingStays(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "007" {
			continue
		}
		data, err := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(data)); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
	}
	if _, err := db.Exec(`INSERT INTO trips(id,project_id,name,home_currency) VALUES(1,'test-proj','Existing','EUR'); INSERT INTO destinations(id,trip_id,place_name) VALUES(1,1,'Kyoto'); INSERT INTO accommodations(id,trip_id,destination_id,name,kind,currency,booked,cost_actual) VALUES(1,1,1,'Booked hotel','hotel','EUR',1,45000)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/007_stay_favorites.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var name string
	var booked bool
	var amount int64
	var source sql.NullInt64
	if err := db.QueryRow(`SELECT name,booked,cost_actual,stay_favorite_id FROM accommodations WHERE id=1`).Scan(&name, &booked, &amount, &source); err != nil {
		t.Fatal(err)
	}
	if name != "Booked hotel" || !booked || amount != 45000 || source.Valid {
		t.Fatal("upgrade changed existing booking")
	}
}
