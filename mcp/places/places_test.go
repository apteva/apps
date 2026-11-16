package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const pid = "proj-1"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	schema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

type fakeGeo struct {
	cands []Candidate
	calls []lookupQuery
	err   error
}

func (f *fakeGeo) Search(q lookupQuery) ([]Candidate, error) {
	f.calls = append(f.calls, q)
	return f.cands, f.err
}

func fp(f float64) *float64 { return &f }
func ip(i int) *int         { return &i }

func mustAdd(t *testing.T, db *sql.DB, geo geocoder, args map[string]any) *addResult {
	t.Helper()
	r, err := addPlace(db, pid, geo, Settings{}, args, nil)
	if err != nil {
		t.Fatalf("addPlace(%v): %v", args, err)
	}
	return r
}

func TestAddWithLookupFillsDetailsAndDedupes(t *testing.T) {
	db := testDB(t)
	geo := &fakeGeo{cands: []Candidate{{
		Source: "google", Name: "Bar Mut", Kind: "bar", Lat: fp(41.3953), Lng: fp(2.1620),
		Address: "Carrer de Pau Claris, 192, 08037 Barcelona, Spain", City: "Barcelona", Country: "Spain",
		GooglePlaceID: "ChIJmut", PriceLevel: ip(3), GoogleRating: fp(4.5),
	}}}
	r := mustAdd(t, db, geo, map[string]any{"name": "Bar Mut", "city": "Barcelona", "tags": []any{"tapas", "date night"}})
	if r.Duplicate || r.LookupSource != "google" {
		t.Fatalf("unexpected result %+v", r)
	}
	p := r.Place
	if p.GooglePlaceID != "ChIJmut" || p.Lat == nil || p.Kind != "bar" || p.Status != "to_try" || *p.PriceLevel != 3 {
		t.Fatalf("lookup fields not merged: %+v", p)
	}
	if got := geo.calls[0].Text; got != "Bar Mut, Barcelona" {
		t.Fatalf("lookup query = %q", got)
	}
	// Same Google id → duplicate, no second row.
	r2 := mustAdd(t, db, geo, map[string]any{"name": "bar mut"})
	if !r2.Duplicate || r2.Place.ID != p.ID {
		t.Fatalf("expected duplicate of %d, got %+v", p.ID, r2)
	}
	// allow_duplicate is respected.
	geo.cands[0].GooglePlaceID = ""
	r3 := mustAdd(t, db, geo, map[string]any{"name": "Bar Mut", "allow_duplicate": true})
	if r3.Duplicate || r3.Place.ID == p.ID {
		t.Fatalf("allow_duplicate ignored: %+v", r3)
	}
}

func TestAddNearDuplicateByNameAndDistance(t *testing.T) {
	db := testDB(t)
	a := mustAdd(t, db, nil, map[string]any{"name": "Cal Pep", "lat": 41.3846, "lng": 2.1830})
	near := mustAdd(t, db, nil, map[string]any{"name": "cal pep", "lat": 41.3850, "lng": 2.1832}) // ~50 m
	if !near.Duplicate || near.Place.ID != a.Place.ID {
		t.Fatalf("expected near duplicate, got %+v", near)
	}
	far := mustAdd(t, db, nil, map[string]any{"name": "Cal Pep", "lat": 41.40, "lng": 2.20}) // other branch
	if far.Duplicate {
		t.Fatal("place 2 km away should not be a duplicate")
	}
}

func TestAddFromGoogleMapsURL(t *testing.T) {
	db := testDB(t)
	u := "https://www.google.com/maps/place/Hotel+Brummell/@41.3729,2.1620,17z/data=!3m1!4b1!4m9!3m8!1s0x12a4a2!8m2!3d41.3731!4d2.1633"
	r := mustAdd(t, db, nil, map[string]any{"url": u, "kind": "hotel", "lookup": false})
	p := r.Place
	if p.Name != "Hotel Brummell" || p.Kind != "hotel" {
		t.Fatalf("name/kind from URL: %+v", p)
	}
	if p.Lat == nil || *p.Lat != 41.3731 || *p.Lng != 2.1633 {
		t.Fatalf("pin coords should win over viewport: %v,%v", *p.Lat, *p.Lng)
	}
	if p.GoogleMapsURL != u {
		t.Fatalf("maps url not kept: %q", p.GoogleMapsURL)
	}
	// A non-Maps link becomes source_url, and the name is still required.
	if _, err := addPlace(db, pid, nil, Settings{}, map[string]any{"url": "https://instagram.com/p/xyz"}, nil); err == nil {
		t.Fatal("expected name required error")
	}
}

func TestLookupFailureStillSaves(t *testing.T) {
	db := testDB(t)
	geo := &fakeGeo{err: fmt.Errorf("boom")}
	r := mustAdd(t, db, geo, map[string]any{"name": "Secret spot"})
	if r.Place == nil || r.LookupError == "" {
		t.Fatalf("expected saved place with lookup_error, got %+v", r)
	}
}

func TestPickCandidateUsesKnownCoords(t *testing.T) {
	cands := []Candidate{
		{Name: "Far", Lat: fp(41.5), Lng: fp(2.5)},
		{Name: "Near", Lat: fp(41.3801), Lng: fp(2.1701)},
	}
	if c := pickCandidate(cands, fp(41.38), fp(2.17)); c == nil || c.Name != "Near" {
		t.Fatalf("got %+v", c)
	}
	if c := pickCandidate(cands[:1], fp(41.38), fp(2.17)); c != nil {
		t.Fatalf("far candidate should be rejected, got %+v", c)
	}
}

func TestSearchFilters(t *testing.T) {
	db := testDB(t)
	tapas := mustAdd(t, db, nil, map[string]any{"name": "Quimet", "kind": "bar", "city": "Barcelona", "lat": 41.3745, "lng": 2.1640, "tags": "tapas, cheap", "price_level": 1})
	mustAdd(t, db, nil, map[string]any{"name": "Disfrutar", "kind": "restaurant", "city": "Barcelona", "lat": 41.3876, "lng": 2.1530, "tags": []any{"tasting menu"}, "price_level": 4, "status": "visited", "my_rating": 10})
	mustAdd(t, db, nil, map[string]any{"name": "Time Out Market", "kind": "market", "city": "Lisbon", "lat": 38.7071, "lng": -9.1458})
	mustAdd(t, db, nil, map[string]any{"name": "Old place", "status": "closed", "city": "Barcelona"})

	count := func(f placeFilter) int {
		ps, err := listPlaces(db, pid, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(ps)
	}
	if n := count(placeFilter{}); n != 3 {
		t.Fatalf("default hides closed: got %d", n)
	}
	if n := count(placeFilter{Status: "all"}); n != 4 {
		t.Fatalf("all: got %d", n)
	}
	if n := count(placeFilter{Tag: "TAPAS"}); n != 1 {
		t.Fatalf("tag (case-insensitive): got %d", n)
	}
	if n := count(placeFilter{City: "barcelona"}); n != 2 {
		t.Fatalf("city: got %d", n)
	}
	if n := count(placeFilter{MaxPrice: ip(2)}); n != 2 { // Quimet + Lisbon (no price)
		t.Fatalf("max price: got %d", n)
	}
	if n := count(placeFilter{MinMyRating: ip(8)}); n != 1 {
		t.Fatalf("min rating: got %d", n)
	}
	if n := count(placeFilter{Q: "market"}); n != 1 {
		t.Fatalf("q: got %d", n)
	}
	near, err := listPlaces(db, pid, placeFilter{NearLat: fp(41.3740), NearLng: fp(2.1650), RadiusM: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(near) != 2 || near[0].ID != tapas.Place.ID || near[0].DistanceM == nil || *near[0].DistanceM > 200 {
		t.Fatalf("radius search sorted by distance: %+v", near)
	}
	if n := count(placeFilter{Status: "to_try", NotVisitedDays: 30}); n != 2 {
		t.Fatalf("not visited: got %d", n)
	}
}

func TestLogVisitPromotesStatusAndRating(t *testing.T) {
	db := testDB(t)
	p := mustAdd(t, db, nil, map[string]any{"name": "Hotel Casa Fuster", "kind": "hotel"}).Place
	v, after, err := logVisit(db, pid, p.ID, visitInput{
		VisitedOn: "2026-10-03", WithWhom: "Ana", Spend: fp(280), Currency: "eur", Rating: ip(8),
		DetailsJSON: `{"room":"terrace suite","nightly_price":140}`, DishesJSON: `["breakfast"]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "visited" || after.VisitCount != 1 || after.LastVisitedAt != "2026-10-03" || *after.MyRating != 8 {
		t.Fatalf("place not updated: %+v", after)
	}
	if v.Currency != "EUR" || !strings.Contains(string(v.Details), "terrace suite") {
		t.Fatalf("visit: %+v", v)
	}
	// A favorite stays favorite.
	if _, err := updatePlace(db, pid, p.ID, map[string]any{"status": "favorite"}); err != nil {
		t.Fatal(err)
	}
	_, after, _ = logVisit(db, pid, p.ID, visitInput{VisitedOn: "2026-11-01"})
	if after.Status != "favorite" || after.VisitCount != 2 || after.LastVisitedAt != "2026-11-01" || *after.MyRating != 8 {
		t.Fatalf("second visit: %+v", after)
	}
	if _, _, err := logVisit(db, pid, p.ID, visitInput{Rating: ip(11)}); err == nil {
		t.Fatal("rating 11 should fail")
	}
	if _, _, err := logVisit(db, pid, 999, visitInput{}); err == nil {
		t.Fatal("unknown place should fail")
	}
	after, err = deleteVisit(db, pid, v.ID)
	if err != nil || after.VisitCount != 1 || after.LastVisitedAt != "2026-11-01" {
		t.Fatalf("delete visit: %+v %v", after, err)
	}
}

func TestListsAndDeleteCascade(t *testing.T) {
	db := testDB(t)
	a := mustAdd(t, db, nil, map[string]any{"name": "A", "list": "Barcelona to try"})
	if len(a.Place.ListIDs) != 1 || a.AddedToList != "Barcelona to try" {
		t.Fatalf("list on add: %+v", a)
	}
	b := mustAdd(t, db, nil, map[string]any{"name": "B"}).Place
	l, created, err := ensureList(db, pid, "barcelona TO TRY", "", "")
	if err != nil || created {
		t.Fatalf("ensureList should reuse case-insensitively: %v %v", created, err)
	}
	if n, err := addToList(db, pid, l.ID, []int64{a.Place.ID, b.ID}); err != nil || n != 1 {
		t.Fatalf("addToList: %d %v", n, err)
	}
	if ps, _ := listPlaces(db, pid, placeFilter{ListID: l.ID}); len(ps) != 2 {
		t.Fatalf("list filter: %d", len(ps))
	}
	if err := deletePlace(db, pid, b.ID); err != nil {
		t.Fatal(err)
	}
	l, _ = getList(db, pid, l.ID)
	if l.PlaceCount != 1 {
		t.Fatalf("delete should drop list membership, count=%d", l.PlaceCount)
	}
	if err := deleteList(db, pid, l.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := getPlace(db, pid, a.Place.ID); p == nil || len(p.ListIDs) != 0 {
		t.Fatalf("place should survive list delete: %+v", p)
	}
}

func TestProjectIsolation(t *testing.T) {
	db := testDB(t)
	p := mustAdd(t, db, nil, map[string]any{"name": "Mine"}).Place
	if _, err := getPlace(db, "other", p.ID); err == nil {
		t.Fatal("other project must not read the place")
	}
	if ps, _ := listPlaces(db, "other", placeFilter{}); len(ps) != 0 {
		t.Fatal("other project must not list the place")
	}
	if _, _, err := logVisit(db, "other", p.ID, visitInput{}); err == nil {
		t.Fatal("other project must not log visits")
	}
}

func TestBuildPatchTags(t *testing.T) {
	db := testDB(t)
	p := mustAdd(t, db, nil, map[string]any{"name": "X", "tags": []any{"tapas", "terrace"}}).Place
	patch, err := buildPatch(p, map[string]any{"add_tags": []any{"Terrace", "late"}, "remove_tags": []any{"TAPAS"}, "my_rating": nil})
	if err != nil {
		t.Fatal(err)
	}
	if patch["tags_json"] != `["terrace","late"]` {
		t.Fatalf("tags = %v", patch["tags_json"])
	}
	if _, err := updatePlace(db, pid, p.ID, patch); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPatch(p, map[string]any{"status": "maybe"}); err == nil {
		t.Fatal("invalid status should fail")
	}
	if _, err := buildPatch(p, map[string]any{"lat": 1.0}); err == nil {
		t.Fatal("lat without lng should fail")
	}
}

func TestImportTakeoutGeoJSON(t *testing.T) {
	db := testDB(t)
	content := `{"type":"FeatureCollection","features":[
	 {"type":"Feature","geometry":{"type":"Point","coordinates":[2.1744,41.4036]},
	  "properties":{"date":"2024-05-01T10:00:00Z","google_maps_url":"http://maps.google.com/?cid=123",
	   "location":{"address":"Carrer de Mallorca, 401, Barcelona","country_code":"ES","name":"Sagrada Família"}}},
	 {"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},
	  "properties":{"Title":"Old format bar","Location":{"Address":"Lisbon","Geo Coordinates":{"Latitude":"38.71","Longitude":"-9.14"}}}},
	 {"type":"Feature","geometry":{"type":"Point","coordinates":[2.1744,41.4036]},
	  "properties":{"location":{"name":"Sagrada Família"}}}
	]}`
	res, err := importPlaces(db, pid, nil, Settings{}, map[string]any{"content": content, "list_name": "Takeout", "tags": "imported"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "geojson" || res.Rows != 3 || res.Added != 2 || res.Duplicates != 1 || res.Failed != 0 {
		t.Fatalf("result %+v", res)
	}
	ps, _ := listPlaces(db, pid, placeFilter{Tag: "imported", Sort: "name"})
	if len(ps) != 2 || ps[0].Name != "Old format bar" || ps[0].Lat == nil || *ps[0].Lat != 38.71 {
		t.Fatalf("imported places: %+v", ps)
	}
	if ps[1].Country != "ES" || len(ps[1].ListIDs) != 1 {
		t.Fatalf("country/list: %+v", ps[1])
	}
}

func TestImportTakeoutCSVAndExportRoundTrip(t *testing.T) {
	db := testDB(t)
	content := "Title,Note,URL,Tags,Comment\n" +
		"El Xampanyet,best anchovies,https://www.google.com/maps/place/El+Xampanyet/data=!4m2!3m1!1s0x0:0x1!3d41.3842!4d2.1815,,\n" +
		"Somewhere,,https://maps.app.goo.gl/abc,,\n" +
		",,,,\n"
	res, err := importPlaces(db, pid, nil, Settings{}, map[string]any{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "csv" || res.Added != 2 || res.WithoutCoords != 1 {
		t.Fatalf("csv import %+v", res)
	}
	format, out, err := exportPlaces(db, pid, map[string]any{"format": "geojson"})
	if err != nil || format != "geojson" {
		t.Fatal(err)
	}
	// Re-import our own export into a fresh project: no duplicates, same count.
	res2, err := importPlaces(db, "proj-2", nil, Settings{}, map[string]any{"content": out})
	if err != nil || res2.Added != 2 {
		t.Fatalf("round trip: %+v %v", res2, err)
	}
	_, csvOut, err := exportPlaces(db, pid, map[string]any{"format": "csv"})
	if err != nil || !strings.Contains(csvOut, "El Xampanyet") || !strings.Contains(csvOut, "best anchovies") {
		t.Fatalf("csv export: %v\n%s", err, csvOut)
	}
}

func TestParseMapsURLVariants(t *testing.T) {
	cases := []struct {
		in       string
		name     string
		lat, lng float64
	}{
		{"https://www.google.com/maps/place/Caf%C3%A9+de+Flore/@48.854,2.3325,17z", "Café de Flore", 48.854, 2.3325},
		{"https://maps.google.com/?q=41.38,2.17", "", 41.38, 2.17},
		{"https://www.google.com/maps/search/?api=1&query=x&q=Bar+Brutal", "Bar Brutal", 0, 0},
	}
	for _, c := range cases {
		got := parseMapsURL(c.in)
		if got.Name != c.name {
			t.Errorf("%s: name %q want %q", c.in, got.Name, c.name)
		}
		if c.lat != 0 && (got.Lat == nil || *got.Lat != c.lat || *got.Lng != c.lng) {
			t.Errorf("%s: coords %v,%v", c.in, got.Lat, got.Lng)
		}
	}
	if !isMapsURL("https://maps.app.goo.gl/xyz") || isMapsURL("https://instagram.com/p/1") {
		t.Fatal("isMapsURL")
	}
}

func TestExpandShortMapsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.google.com/maps/place/Foo/@1.5,2.5,17z", http.StatusFound)
	}))
	defer srv.Close()
	// Non-short links pass through untouched.
	if got, _ := expandShortMapsURL(srv.URL+"/x", srv.Client()); got != srv.URL+"/x" {
		t.Fatalf("got %q", got)
	}
}

func TestParseGoogleAndNominatim(t *testing.T) {
	g := parseGooglePlaces(json.RawMessage(`{"places":[{"id":"abc","displayName":{"text":"Hotel Neri"},
		"formattedAddress":"Carrer de Sant Sever, 5, Ciutat Vella, 08002 Barcelona, Spain",
		"location":{"latitude":41.383,"longitude":2.175},"rating":4.6,"userRatingCount":900,
		"priceLevel":"PRICE_LEVEL_EXPENSIVE","primaryType":"hotel","googleMapsUri":"https://maps.google.com/?cid=1"}]}`))
	if len(g) != 1 || g[0].Kind != "hotel" || g[0].City != "Barcelona" || g[0].Country != "Spain" || *g[0].PriceLevel != 3 {
		t.Fatalf("google: %+v", g)
	}
	n := parseNominatim([]byte(`[{"osm_type":"node","osm_id":42,"lat":"38.6916","lon":"-9.2160","name":"Miradouro","display_name":"Miradouro, Lisboa, Portugal",
		"category":"tourism","type":"viewpoint","address":{"city":"Lisboa","country":"Portugal"},"extratags":{"website":"https://x.pt"}}]`))
	if len(n) != 1 || n[0].Kind != "viewpoint" || n[0].City != "Lisboa" || n[0].OSMID != "node/42" || n[0].Website != "https://x.pt" {
		t.Fatalf("osm: %+v", n)
	}
}

func TestNominatimGeocoderHTTP(t *testing.T) {
	var gotUA, gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotQ = r.UserAgent(), r.URL.Query().Get("q")
		_, _ = w.Write([]byte(`[{"osm_type":"way","osm_id":7,"lat":"41.1","lon":"2.1","name":"Platja","display_name":"Platja, Sitges","category":"natural","type":"beach","address":{"town":"Sitges","country":"Spain"}}]`))
	}))
	defer srv.Close()
	c, err := nominatimGeocoder{baseURL: srv.URL, client: srv.Client()}.Search(lookupQuery{Text: "Platja Sitges"})
	if err != nil || len(c) != 1 || c[0].Kind != "beach" || c[0].City != "Sitges" {
		t.Fatalf("%+v %v", c, err)
	}
	if !strings.HasPrefix(gotUA, "apteva-places/") || gotQ != "Platja Sitges" {
		t.Fatalf("ua=%q q=%q", gotUA, gotQ)
	}
}

func TestManifestParses(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "places" {
		t.Fatalf("name %q", m.Name)
	}
	tools := map[string]bool{}
	for _, tl := range (&App{}).MCPTools() {
		tools[tl.Name] = true
	}
	for _, mt := range m.Provides.MCPTools {
		if !tools[mt.Name] {
			t.Errorf("manifest tool %s has no handler", mt.Name)
		}
	}
	if len(tools) != len(m.Provides.MCPTools) {
		t.Errorf("handlers %d vs manifest %d", len(tools), len(m.Provides.MCPTools))
	}
}

func TestLookupRequiresGivenCity(t *testing.T) {
	db := testDB(t)
	wrongTown := Candidate{Source: "osm", Name: "Can Solé", Kind: "restaurant", Lat: fp(41.6801), Lng: fp(2.1558),
		Address: "Can Solé, la Quintana, Sant Feliu de Codines, Vallès Oriental, Barcelona, Catalonia, Spain",
		City:    "Sant Feliu de Codines", Country: "Spain", OSMID: "node/1"}
	inCity := Candidate{Source: "osm", Name: "Can Solé", Kind: "restaurant", Lat: fp(41.3790), Lng: fp(2.1884),
		Address: "Can Solé, Carrer de Sant Carles, la Barceloneta, Barcelona, Spain", City: "Barcelona", Country: "Spain", OSMID: "node/2"}

	// The wrong town ranks first; the in-city match must win.
	geo := &fakeGeo{cands: []Candidate{wrongTown, inCity}}
	r := mustAdd(t, db, geo, map[string]any{"name": "Can Solé", "city": "Barcelona"})
	if r.Place.OSMID != "node/2" || *r.Place.Lat != 41.3790 {
		t.Fatalf("picked the wrong town: %+v", r.Place)
	}
	if geo.calls[0].Limit != 10 {
		t.Fatalf("city lookups should ask for more candidates, got %d", geo.calls[0].Limit)
	}

	// Only the wrong town: save without a location rather than a bad pin.
	geo = &fakeGeo{cands: []Candidate{wrongTown}}
	r = mustAdd(t, db, geo, map[string]any{"name": "Can Solé", "city": "Barcelona", "allow_duplicate": true})
	if r.Place.Lat != nil || !strings.Contains(r.LookupError, "no match in Barcelona") {
		t.Fatalf("expected no location + lookup_error, got %+v / %q", r.Place, r.LookupError)
	}
	if r.Place.City != "Barcelona" {
		t.Fatalf("city should stay as given, got %q", r.Place.City)
	}

	// No city given: first result is still taken.
	geo = &fakeGeo{cands: []Candidate{wrongTown, inCity}}
	r = mustAdd(t, db, geo, map[string]any{"name": "Can Solé", "allow_duplicate": true})
	if r.Place.OSMID != "node/1" {
		t.Fatalf("without a city the first match is used, got %+v", r.Place)
	}
}

func TestFilterInCityFallsBackToAddress(t *testing.T) {
	cands := []Candidate{
		{Name: "A", Address: "Carrer de Sant Sever, 5, 08002 Barcelona, Spain"},                  // Google, no components
		{Name: "B", Address: "Plaça Major, 08001 Lisboa, Portugal"},                              // other city
		{Name: "C", City: "Barcelóna"},                                                           // accent folding
		{Name: "D", City: "Sant Feliu de Codines", Address: "Vallès Oriental, Barcelona, Spain"}, // province only
	}
	got := filterInCity(cands, "barcelona")
	names := []string{}
	for _, c := range got {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "A,C" {
		t.Fatalf("got %v", names)
	}
	ranked := rankInCity(cands, "Barcelona")
	if ranked[0].Name != "A" || ranked[1].Name != "C" || len(ranked) != 4 {
		t.Fatalf("rank: %+v", ranked)
	}
}

func TestLookupPrefersRequestedKind(t *testing.T) {
	db := testDB(t)
	manor := Candidate{Source: "osm", Name: "Can Solé", Kind: "other", Lat: fp(41.4180), Lng: fp(2.1080),
		Address: "Can Solé, Passeig de Solé i Pla, Vallvidrera, Barcelona, Spain", City: "Barcelona", OSMID: "way/1"}
	resto := Candidate{Source: "osm", Name: "Can Solé", Kind: "restaurant", Lat: fp(41.3790), Lng: fp(2.1884),
		Address: "Can Solé, Carrer de Sant Carles, la Barceloneta, Barcelona, Spain", City: "Barcelona", OSMID: "node/2"}

	geo := &fakeGeo{cands: []Candidate{manor, resto}}
	r := mustAdd(t, db, geo, map[string]any{"name": "Can Solé", "city": "Barcelona", "kind": "restaurant"})
	if r.Place.OSMID != "node/2" {
		t.Fatalf("expected the restaurant, got %+v", r.Place)
	}

	// A typed place beats "other" even when the kind differs (bar vs restaurant).
	bar := resto
	bar.Kind, bar.OSMID = "bar", "node/3"
	got := preferKind([]Candidate{manor, bar}, "restaurant")
	if got[0].OSMID != "node/3" {
		t.Fatalf("typed place should rank above other, got %+v", got)
	}

	// No kind given: order is kept.
	if got := preferKind([]Candidate{manor, resto}, ""); got[0].OSMID != "way/1" {
		t.Fatalf("order should be kept without a kind")
	}
}
