package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func issueCodes(r *QualityReport) map[string]int {
	out := map[string]int{}
	for _, i := range r.Issues {
		out[i.Code]++
	}
	return out
}

func TestSyntheticSeedShowsStaleMissingAndConflicts(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	counts, err := seedSynthetic(db, "p", now)
	if err != nil {
		t.Fatal(err)
	}
	if counts["entities"] < 8 || counts["evidence"] != 4 || counts["claims"] != 2 {
		t.Fatalf("seed counts = %v", counts)
	}
	// Seeding twice adds nothing new.
	again, err := seedSynthetic(db, "p", now)
	if err != nil {
		t.Fatal(err)
	}
	if again["entities"] != 0 || again["evidence"] != 0 || again["relationships"] != 0 {
		t.Fatalf("second seed added rows: %v", again)
	}
	q, err := dataQuality(db, "p", now)
	if err != nil {
		t.Fatal(err)
	}
	codes := issueCodes(q)
	for _, want := range []string{"series_stale", "value_conflict", "claim_value_conflict", "location_missing"} {
		if codes[want] == 0 {
			t.Errorf("missing %s issue; got %v", want, codes)
		}
	}
	if q.Summary["stale"] == 0 || q.Summary["missing"] == 0 || q.Summary["conflict"] == 0 {
		t.Fatalf("summary = %v", q.Summary)
	}
	if q.Basis["evidence"][basisSynthetic] != 4 || q.Access[accessRestricted] != 1 {
		t.Fatalf("basis %v access %v", q.Basis, q.Access)
	}
	// The depot has no location; it is flagged by entity.
	depot, _ := findWorldEntity(db, "p", "Synthetic Inland Depot", nil)
	if depot == nil || !containsString(q.ByEntity[depot.ID], "missing") {
		t.Fatalf("depot flags = %v", q.ByEntity[depot.ID])
	}
}

func TestSyntheticRowsNeverFeedForecastsAndClearCleanly(t *testing.T) {
	db := openTestDB(t)
	// Evidence is stamped with the real clock, so the cutoff must follow it.
	now := nowUTC().Truncate(time.Second)
	if _, err := seedSynthetic(db, "p", now); err != nil {
		t.Fatal(err)
	}
	// A real question linked to a synthetic and a restricted record sees
	// neither of them.
	qq, _, err := insertQuestion(db, "p", Question{Origin: "manual", Text: "Will transits recover?", ResolutionRule: "YES if transits exceed 50 on any day", ResolutionSource: "operator", Deadline: now.Add(72 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	synthetic, _ := searchEvidence(db, EvidenceQuery{ProjectID: "p", Basis: basisSynthetic, Limit: 10})
	real, _, err := recordEvidenceDetailed(db, Evidence{Kind: "news", Title: "Real headline", Source: "example.com", SourceRef: "https://example.com/x"}, "p")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := recordEvidenceDetailed(db, Evidence{Kind: "news", Title: "Restricted note", Source: "desk", Access: accessRestricted}, "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{synthetic[0]["id"].(int64), real["id"].(int64), secret["id"].(int64)} {
		if _, err := linkQuestionEvidence(db, qq.ID, id, 1); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := questionEvidenceIDs(db, "p", qq.ID, now.Add(time.Hour), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != real["id"].(int64) {
		t.Fatalf("forecast evidence = %v, want only the real open record", ids)
	}

	// Real data confirms one synthetic country; it survives the clear.
	if _, _, err := upsertWorldEntity(db, "p", WorldEntity{Name: "Egypt", Kind: "region"}); err != nil {
		t.Fatal(err)
	}
	removed, err := clearSynthetic(db, "p")
	if err != nil {
		t.Fatal(err)
	}
	if removed["evidence"] != 4 || removed["entities"] == 0 {
		t.Fatalf("removed = %v", removed)
	}
	q, _ := dataQuality(db, "p", now)
	for table, m := range q.Basis {
		if m[basisSynthetic] != 0 {
			t.Errorf("%s still has %d synthetic rows", table, m[basisSynthetic])
		}
	}
	egypt, _ := findWorldEntity(db, "p", "Egypt", nil)
	if egypt == nil || egypt.Basis != basisObserved {
		t.Fatalf("confirmed entity = %+v", egypt)
	}
}

func TestReaderAccessControl(t *testing.T) {
	db := openTestDB(t)
	for _, a := range []string{accessOpen, accessInternal, accessRestricted} {
		if _, _, err := recordEvidenceDetailed(db, Evidence{Kind: "note", Title: a + " title", Body: "body", Source: "s-" + a, SourceRef: "ref", Access: a}, "p"); err != nil {
			t.Fatal(err)
		}
	}
	resources := []sdk.ResourceDecl{{Name: "access", Matcher: "id_set"}}
	visible := func(rd reader) []string {
		rows, err := searchEvidence(db, EvidenceQuery{ProjectID: "p", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		rd.redactRows(rows)
		out := []string{}
		for _, r := range rows {
			if r["redacted"] == nil {
				out = append(out, r["access"].(string))
			} else if r["body"] != "" || r["source_ref"] != "" {
				t.Fatalf("redacted row kept content: %v", r)
			}
		}
		return out
	}
	cases := []struct {
		name string
		rd   reader
		want string
	}{
		{"operator", operatorReader, "restricted,internal,open"},
		{"no caller info", reader{}, "internal,open"},
		{"default allow", reader{caller: &sdk.Caller{DefaultEffect: "allow", Resources: resources}}, "internal,open"},
		{"deny internal", reader{caller: &sdk.Caller{DefaultEffect: "allow", Resources: resources,
			Grants: []sdk.Grant{{Effect: "deny", Permission: readPermission, Resource: "access/internal"}}}}, "open"},
		{"explicit restricted", reader{caller: &sdk.Caller{DefaultEffect: "allow", Resources: resources,
			Grants: []sdk.Grant{{Effect: "allow", Permission: readPermission, Resource: "internal,restricted"}}}}, "restricted,internal,open"},
		{"allow then deny", reader{caller: &sdk.Caller{DefaultEffect: "allow", Resources: resources,
			Grants: []sdk.Grant{{Effect: "allow", Permission: readPermission, Resource: "*"}, {Effect: "deny", Permission: readPermission, Resource: "restricted"}}}}, "internal,open"},
	}
	for _, c := range cases {
		if got := strings.Join(visible(c.rd), ","); got != c.want {
			t.Errorf("%s: visible %s, want %s", c.name, got, c.want)
		}
	}
	// A forged reader key from tool arguments is not a reader value.
	if readerFrom(map[string]any{readerArg: map[string]any{"operator": true}}).operator {
		t.Fatal("forged reader accepted")
	}
}

func TestEvidenceAccessFloorAndRetention(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	prev := nowUTC
	nowUTC = func() time.Time { return now.Add(-40 * 24 * time.Hour) }
	defer func() { nowUTC = prev }()
	for _, s := range defaultSources() {
		if err := ensureSource(db, "p", s); err != nil {
			t.Fatal(err)
		}
	}
	days := 30
	if err := updateSourceSettings(db, "p", "news", nil, nil, nil, accessInternal, &days); err != nil {
		t.Fatal(err)
	}
	env := &ingestEnv{db: db, pid: "p", now: now}
	id, _, err := recordNewsItem(env, NewsItem{Title: "Old headline", URL: "https://example.com/old", Source: "example.com"}, "q", nil, "gdelt")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := searchEvidence(db, EvidenceQuery{ProjectID: "p", IDs: []int64{id}})
	// GDELT is open, but the source was set to internal: the stricter wins.
	if rows[0]["access"] != accessInternal {
		t.Fatalf("access = %v", rows[0]["access"])
	}
	hash := rows[0]["content_hash"]
	n, err := applyRetention(db, "p", now)
	if err != nil || n != 1 {
		t.Fatalf("retention removed %d (%v)", n, err)
	}
	rows, _ = searchEvidence(db, EvidenceQuery{ProjectID: "p", IDs: []int64{id}})
	if rows[0]["content_hash"] != hash || rows[0]["content_expired_at"] == nil || strings.Contains(rows[0]["title"].(string), "Old headline") {
		t.Fatalf("after retention: %v", rows[0])
	}
	if n, _ := applyRetention(db, "p", now); n != 0 {
		t.Fatalf("retention ran twice on the same row")
	}
}

func TestGeographyGazetteerWikipediaAndConflicts(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	fs := &fakeSources{responses: map[string]string{
		"wikipedia/page_summary": `{"type":"standard","title":"Port of Example","description":"A port","coordinates":{"lat":51.9,"lon":4.1},"content_urls":{"desktop":{"page":"https://en.wikipedia.org/wiki/Port_of_Example"}}}`,
	}}
	env := &ingestEnv{db: db, pid: "p", sc: fs, now: now}
	for _, e := range []WorldEntity{{Name: "France", Kind: "region"}, {Name: "USA", Kind: "place"}, {Name: "Port of Example", Kind: "facility"},
		{Name: "Interest rates", Kind: "concept"}, {Name: "Central bank governor", Kind: "public_role"}} {
		if _, _, err := upsertWorldEntity(db, "p", e); err != nil {
			t.Fatal(err)
		}
	}
	st, err := runGeocode(env, IntelSource{Slug: "geocode", Config: map[string]any{"max_lookups_per_run": 10}})
	if err != nil {
		t.Fatal(err)
	}
	locs, _ := entityLocations(db, "p")
	france, _ := findWorldEntity(db, "p", "France", nil)
	usa, _ := findWorldEntity(db, "p", "USA", nil)
	port, _ := findWorldEntity(db, "p", "Port of Example", nil)
	if l := bestLocation(locs[france.ID]); l == nil || l.Precision != "country" || l.CountryID != "250" || l.Source != "gazetteer" {
		t.Fatalf("France = %+v", l)
	}
	if l := bestLocation(locs[usa.ID]); l == nil || l.CountryID != "840" {
		t.Fatalf("USA = %+v", l)
	}
	l := bestLocation(locs[port.ID])
	if l == nil || l.Source != "wikipedia" || l.Precision != "point" || l.EvidenceID == nil || l.CountryID != "528" {
		t.Fatalf("port = %+v (stats %+v)", l, st)
	}
	// Countries are not looked up on Wikipedia (they already have a place).
	for _, c := range fs.calls {
		if c != "wikipedia/page_summary" {
			t.Fatalf("unexpected call %s", c)
		}
	}
	if len(fs.calls) != 1 {
		t.Fatalf("wikipedia calls = %v", fs.calls)
	}
	// public_role is never placed below country precision.
	role, _ := findWorldEntity(db, "p", "Central bank governor", nil)
	if err := putEntityLocation(db, "p", EntityLocation{EntityID: role.ID, Lat: 1, Lon: 1, Precision: "point", Source: "operator", Provenance: "x"}); err == nil {
		t.Fatal("public_role placed at point precision")
	}
	// An operator placing the port on another continent is a conflict.
	if err := putEntityLocation(db, "p", EntityLocation{EntityID: port.ID, Lat: -33.9, Lon: 18.4, Precision: "city", Source: "operator",
		Provenance: "operator note", CountryID: nearestCountry(-33.9, 18.4)}); err != nil {
		t.Fatal(err)
	}
	q, _ := dataQuality(db, "p", now)
	if issueCodes(q)["location_conflict"] != 1 {
		t.Fatalf("issues = %v", issueCodes(q))
	}

	m, err := buildWorldMap(db, "p", reader{}, mapFilter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	ents := m["entities"].([]map[string]any)
	if len(ents) != 3 {
		t.Fatalf("located entities = %d", len(ents))
	}
	reasons := map[string]string{}
	for _, u := range m["unmapped"].([]map[string]any) {
		reasons[u["name"].(string)] = u["reason"].(string)
	}
	if !strings.Contains(reasons["Interest rates"], "no place of its own") || !strings.Contains(reasons["Central bank governor"], "public roles") {
		t.Fatalf("unmapped reasons = %v", reasons)
	}
	b, _ := json.Marshal(m)
	if !json.Valid(b) {
		t.Fatal("world map does not serialise")
	}
}

func TestWorldMapHidesRestrictedEvidencePointsFromAgents(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if _, err := seedSynthetic(db, "p", now); err != nil {
		t.Fatal(err)
	}
	agent, err := buildWorldMap(db, "p", reader{caller: &sdk.Caller{DefaultEffect: "allow"}}, mapFilter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildWorldMap(db, "p", operatorReader, mapFilter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(op["evidence_points"].([]map[string]any)) != 3 || len(agent["evidence_points"].([]map[string]any)) != 2 || agent["hidden_evidence"] != 1 {
		t.Fatalf("operator points %d, agent points %d hidden %v", len(op["evidence_points"].([]map[string]any)), len(agent["evidence_points"].([]map[string]any)), agent["hidden_evidence"])
	}
	// Filtering to observed data drops every synthetic row.
	f := mapFilter{Bases: map[string]bool{basisObserved: true}}
	obs, err := buildWorldMap(db, "p", operatorReader, f, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range obs["entities"].([]map[string]any) {
		if e["basis"] != basisObserved {
			t.Fatalf("basis filter leaked %v", e)
		}
	}
	if len(obs["evidence_points"].([]map[string]any)) != 0 || len(obs["relationships"].([]map[string]any)) != 0 {
		t.Fatal("synthetic links or points shown under observed filter")
	}
}
