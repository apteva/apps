package main

// Entity geography. Every location is attributed: which source placed the
// entity, at what precision, and (for looked-up coordinates) the evidence
// row that holds the lookup. Nothing is placed from a publisher's country
// or guessed from a name fragment.
//
//   gazetteer — an entity whose name or alias is a country name gets that
//               country's Natural Earth centroid (precision "country").
//   wikipedia — the "geocode" connector looks up places, regions,
//               facilities, infrastructure and organizations on Wikipedia
//               and keeps the page's own coordinates (precision "point").
//   operator  — entity_locate records coordinates with a stated provenance.
//
// public_role entities are never placed finer than a country.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type gazetteerCountry struct {
	ID, Name string
	Lat, Lon float64
}

// countryAliases maps common names onto Natural Earth 110m names.
var countryAliases = map[string]string{
	"united states": "United States of America", "usa": "United States of America", "u.s.": "United States of America",
	"us": "United States of America", "u.s.a.": "United States of America",
	"uk": "United Kingdom", "u.k.": "United Kingdom", "great britain": "United Kingdom", "britain": "United Kingdom",
	"russian federation": "Russia", "republic of korea": "South Korea", "korea, south": "South Korea",
	"dprk": "North Korea", "czech republic": "Czechia", "ivory coast": "Côte d'Ivoire", "cote d'ivoire": "Côte d'Ivoire",
	"democratic republic of the congo": "Dem. Rep. Congo", "dr congo": "Dem. Rep. Congo", "drc": "Dem. Rep. Congo",
	"republic of the congo": "Congo", "uae": "United Arab Emirates", "bosnia and herzegovina": "Bosnia and Herz.",
	"bosnia": "Bosnia and Herz.", "dominican republic": "Dominican Rep.", "central african republic": "Central African Rep.",
	"south sudan": "S. Sudan", "eswatini": "eSwatini", "swaziland": "eSwatini", "north macedonia": "Macedonia",
	"equatorial guinea": "Eq. Guinea", "western sahara": "W. Sahara", "solomon islands": "Solomon Is.",
	"falkland islands": "Falkland Is.", "east timor": "Timor-Leste", "burma": "Myanmar", "türkiye": "Turkey",
	"turkiye": "Turkey", "holland": "Netherlands", "the netherlands": "Netherlands", "persia": "Iran",
	"viet nam": "Vietnam", "lao pdr": "Laos", "palestinian territories": "Palestine", "gaza": "Palestine",
	"west bank": "Palestine",
}

var gazetteerIndex = func() map[string]gazetteerCountry {
	m := map[string]gazetteerCountry{}
	for _, c := range naturalEarthCountries {
		m[strings.ToLower(c.Name)] = c
	}
	for alias, name := range countryAliases {
		if c, ok := m[strings.ToLower(name)]; ok {
			m[alias] = c
		}
	}
	return m
}()

// gazetteerLookup matches a whole name (not a fragment) to a country.
func gazetteerLookup(name string) (gazetteerCountry, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, "the ")
	c, ok := gazetteerIndex[n]
	return c, ok
}

func gazetteerByID(id string) (gazetteerCountry, bool) {
	for _, c := range naturalEarthCountries {
		if c.ID == id {
			return c, true
		}
	}
	return gazetteerCountry{}, false
}

// geocodableKinds are entity kinds that have a place in the world.
var geocodableKinds = map[string]bool{"place": true, "region": true, "facility": true, "infrastructure": true, "organization": true, "institution": true}

var precisionRank = map[string]int{"point": 4, "city": 3, "region": 2, "country": 1}

type EntityLocation struct {
	ID         int64   `json:"id"`
	EntityID   int64   `json:"entity_id"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Label      string  `json:"label,omitempty"`
	CountryID  string  `json:"country_id,omitempty"`
	Precision  string  `json:"precision"`
	Provenance string  `json:"provenance"`
	Source     string  `json:"source"`
	EvidenceID *int64  `json:"evidence_id,omitempty"`
	Basis      string  `json:"basis"`
}

// putEntityLocation records (or replaces) one source's location for an
// entity. One row per (entity, source): a source re-placing an entity
// overwrites its own earlier answer, never another source's.
func putEntityLocation(db *sql.DB, pid string, l EntityLocation) error {
	if math.IsNaN(l.Lat) || math.IsNaN(l.Lon) || math.Abs(l.Lat) > 90 || math.Abs(l.Lon) > 180 {
		return errors.New("lat must be within ±90 and lon within ±180")
	}
	if precisionRank[l.Precision] == 0 {
		return errors.New("precision must be point, city, region or country")
	}
	if strings.TrimSpace(l.Provenance) == "" || strings.TrimSpace(l.Source) == "" {
		return errors.New("location needs a source and a provenance")
	}
	e, err := getWorldEntity(db, pid, l.EntityID)
	if err != nil {
		return err
	}
	if e == nil {
		return fmt.Errorf("entity %d not found", l.EntityID)
	}
	if e.Kind == "public_role" && l.Precision != "country" {
		return errors.New("public_role entities are placed at country precision at most")
	}
	l.Basis = fallback(l.Basis, basisObserved)
	if !validBasis(l.Basis) {
		return fmt.Errorf("invalid basis %q", l.Basis)
	}
	if l.CountryID == "" && l.Precision == "country" {
		if c, ok := gazetteerLookup(l.Label); ok {
			l.CountryID = c.ID
		}
	}
	_, err = db.Exec(`INSERT INTO intel_entity_locations (project_id, entity_id, lat, lon, label, country_id, precision, provenance, source, evidence_id, basis)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id, entity_id, source) DO UPDATE SET lat = excluded.lat, lon = excluded.lon, label = excluded.label,
		  country_id = excluded.country_id, precision = excluded.precision, provenance = excluded.provenance,
		  evidence_id = excluded.evidence_id, basis = excluded.basis, retracted_at = NULL, created_at = CURRENT_TIMESTAMP`,
		pid, l.EntityID, l.Lat, l.Lon, l.Label, l.CountryID, l.Precision, l.Provenance, l.Source, ptrInt64(l.EvidenceID), l.Basis)
	return err
}

// entityLocations returns every active location per entity.
func entityLocations(db *sql.DB, pid string) (map[int64][]EntityLocation, error) {
	rows, err := db.Query(`SELECT id, entity_id, lat, lon, label, country_id, precision, provenance, source, evidence_id, basis
		FROM intel_entity_locations WHERE project_id = ? AND retracted_at IS NULL ORDER BY entity_id, id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]EntityLocation{}
	for rows.Next() {
		var l EntityLocation
		var ev sql.NullInt64
		if err := rows.Scan(&l.ID, &l.EntityID, &l.Lat, &l.Lon, &l.Label, &l.CountryID, &l.Precision, &l.Provenance, &l.Source, &ev, &l.Basis); err != nil {
			return nil, err
		}
		if ev.Valid {
			v := ev.Int64
			l.EvidenceID = &v
		}
		out[l.EntityID] = append(out[l.EntityID], l)
	}
	return out, rows.Err()
}

// bestLocation picks the most precise location; ties go to observed over
// inferred over synthetic, then the earliest row.
func bestLocation(locs []EntityLocation) *EntityLocation {
	if len(locs) == 0 {
		return nil
	}
	sorted := append([]EntityLocation(nil), locs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if precisionRank[sorted[i].Precision] != precisionRank[sorted[j].Precision] {
			return precisionRank[sorted[i].Precision] > precisionRank[sorted[j].Precision]
		}
		return basisRank(sorted[i].Basis) > basisRank(sorted[j].Basis)
	})
	return &sorted[0]
}

// haversineKM is the great-circle distance between two points.
func haversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

// nearestCountry returns the Natural Earth country whose centroid is
// closest, used only to group point locations by country on the map.
func nearestCountry(lat, lon float64) string {
	best, bestD := "", math.MaxFloat64
	for _, c := range naturalEarthCountries {
		if d := haversineKM(lat, lon, c.Lat, c.Lon); d < bestD {
			best, bestD = c.ID, d
		}
	}
	return best
}

// placeFromGazetteer gives country-named entities their country location.
func placeFromGazetteer(db *sql.DB, pid string) (int, error) {
	rows, err := db.Query(`SELECT e.id, e.canonical, e.aliases, e.kind, e.basis FROM entities e
		WHERE e.project_id = ? AND e.kind IN ('place','region')
		  AND NOT EXISTS (SELECT 1 FROM intel_entity_locations l WHERE l.project_id = e.project_id AND l.entity_id = e.id AND l.source = 'gazetteer')`, pid)
	if err != nil {
		return 0, err
	}
	type cand struct {
		id          int64
		names       []string
		kind, basis string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		var name, aliases string
		if err := rows.Scan(&c.id, &name, &aliases, &c.kind, &c.basis); err != nil {
			rows.Close()
			return 0, err
		}
		var al []string
		_ = json.Unmarshal([]byte(aliases), &al)
		c.names = append([]string{name}, al...)
		cands = append(cands, c)
	}
	rows.Close()
	placed := 0
	for _, c := range cands {
		for _, n := range c.names {
			g, ok := gazetteerLookup(n)
			if !ok {
				continue
			}
			// The gazetteer is a real source, so the placement is observed
			// even for a synthetic entity (clearing that entity removes it).
			basis := basisObserved
			if err := putEntityLocation(db, pid, EntityLocation{EntityID: c.id, Lat: g.Lat, Lon: g.Lon, Label: g.Name, CountryID: g.ID,
				Precision: "country", Source: "gazetteer", Basis: basis,
				Provenance: fmt.Sprintf("Natural Earth country centroid; the entity name %q is the country %q", n, g.Name)}); err != nil {
				return placed, err
			}
			placed++
			break
		}
	}
	return placed, nil
}

// runGeocode is the "geocode" connector: country names from the bundled
// gazetteer, then Wikipedia page coordinates for a bounded batch of
// entities not tried in the last 30 days.
func runGeocode(env *ingestEnv, src IntelSource) (ingestStats, error) {
	st := ingestStats{}
	placed, err := placeFromGazetteer(env.db, env.pid)
	if err != nil {
		return st, err
	}
	st.Inserted += placed
	batch := int(cfgFloat(src.Config, "max_lookups_per_run", 10))
	retry := time.Duration(cfgFloat(src.Config, "retry_days", 30)) * 24 * time.Hour
	kinds := []string{}
	for k := range geocodableKinds {
		kinds = append(kinds, "'"+k+"'")
	}
	sort.Strings(kinds)
	rows, err := env.db.Query(`SELECT e.id, e.canonical FROM entities e
		WHERE e.project_id = ? AND e.kind IN (`+strings.Join(kinds, ",")+`) AND e.basis <> 'synthetic'
		  AND NOT EXISTS (SELECT 1 FROM intel_entity_locations l WHERE l.project_id = e.project_id AND l.entity_id = e.id AND l.retracted_at IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM intel_geocode_attempts a WHERE a.project_id = e.project_id AND a.entity_id = e.id AND a.source = 'wikipedia' AND a.attempted_at > ?)
		ORDER BY e.id LIMIT ?`, env.pid, ts(env.now.Add(-retry)), batch)
	if err != nil {
		return st, err
	}
	type cand struct {
		id   int64
		name string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.name); err != nil {
			rows.Close()
			return st, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	failed := 0
	for _, c := range cands {
		raw, ok := env.sc.call("wikipedia", "page_summary", map[string]any{"title": c.name})
		st.Fetched++
		if !ok {
			failed++
			noteGeocodeAttempt(env, c.id, "failed", orStr(lastSourceError("wikipedia"), "wikipedia unavailable"))
			continue
		}
		var page struct {
			Type        string `json:"type"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Coordinates *struct {
				Lat float64 `json:"lat"`
				Lon float64 `json:"lon"`
			} `json:"coordinates"`
			ContentURLs struct {
				Desktop struct {
					Page string `json:"page"`
				} `json:"desktop"`
			} `json:"content_urls"`
		}
		if json.Unmarshal(raw, &page) != nil || page.Coordinates == nil || page.Type == "disambiguation" {
			reason := "no coordinates on the Wikipedia page"
			if page.Type == "disambiguation" {
				reason = "Wikipedia title is a disambiguation page"
			}
			noteGeocodeAttempt(env, c.id, "not_found", reason)
			continue
		}
		payload, _ := json.Marshal(map[string]any{"connector": "geocode", "lookup": c.name, "page_title": page.Title,
			"locations": []map[string]any{{"lat": page.Coordinates.Lat, "lon": page.Coordinates.Lon, "label": page.Title,
				"relationship": "subject_location", "precision": "point", "provenance": "Wikipedia page coordinates"}}})
		row, inserted, err := recordEvidenceDetailed(env.db, Evidence{Kind: "reference", Title: "Wikipedia: " + page.Title, Body: page.Description,
			Source: "wikipedia", SourceRef: page.ContentURLs.Desktop.Page, Payload: payload, EntityRefs: []string{c.name}, Access: accessOpen}, env.pid)
		if err != nil {
			return st, err
		}
		if inserted {
			st.Inserted++
		} else {
			st.Duplicates++
		}
		evID, _ := row["id"].(int64)
		if err := putEntityLocation(env.db, env.pid, EntityLocation{EntityID: c.id, Lat: page.Coordinates.Lat, Lon: page.Coordinates.Lon,
			Label: page.Title, CountryID: nearestCountry(page.Coordinates.Lat, page.Coordinates.Lon), Precision: "point", Source: "wikipedia",
			EvidenceID: &evID, Provenance: "Coordinates of the Wikipedia page " + strings.TrimSpace(page.Title+" "+page.ContentURLs.Desktop.Page)}); err != nil {
			// public_role and similar refusals are recorded, not fatal.
			noteGeocodeAttempt(env, c.id, "not_found", err.Error())
			continue
		}
		noteGeocodeAttempt(env, c.id, "found", page.Title)
	}
	if failed > 0 && failed == len(cands) {
		return st, fmt.Errorf("wikipedia: all %d lookups failed (%s)", failed, orStr(lastSourceError("wikipedia"), "unavailable"))
	}
	return st, nil
}

func noteGeocodeAttempt(env *ingestEnv, entityID int64, outcome, detail string) {
	_, _ = env.db.Exec(`INSERT INTO intel_geocode_attempts (project_id, entity_id, source, attempted_at, outcome, detail) VALUES (?,?,?,?,?,?)
		ON CONFLICT(project_id, entity_id, source) DO UPDATE SET attempted_at = excluded.attempted_at, outcome = excluded.outcome, detail = excluded.detail`,
		env.pid, entityID, "wikipedia", ts(env.now), outcome, truncate(detail, 200))
}

// ensureRelationship asserts a relationship unless an identical active one
// already exists, so connectors can restate structure every run.
func ensureRelationship(db *sql.DB, pid string, r Relationship) (*Relationship, bool, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM intel_relationships WHERE project_id = ? AND from_entity_id = ? AND rel_type = ? AND to_entity_id = ? AND retracted_at IS NULL LIMIT 1`,
		pid, r.From, strings.ToLower(strings.TrimSpace(r.Type)), r.To).Scan(&id)
	if err == nil {
		r.ID = id
		return &r, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	out, err := assertRelationship(db, pid, r)
	return out, err == nil, err
}
