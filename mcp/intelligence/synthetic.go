package main

// Synthetic sample data. A small, clearly labelled shipping scenario that
// exercises every part of the map and the quality checks: located
// facilities and countries, observed/inferred/assumed links, a series that
// has gone stale, two sources disagreeing, conflicting claims, an entity
// with no location and one restricted record.
//
// Every row is basis "synthetic" and source "synthetic". Forecasts, the
// ledger, extraction and scenarios never read synthetic rows, and
// synthetic_clear removes them all (an entity that real data later
// confirmed stays, without its synthetic rows).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const syntheticSource = "synthetic"

func seedSynthetic(db *sql.DB, pid string, now time.Time) (map[string]int, error) {
	counts := map[string]int{}
	day := func(d int) time.Time { return now.Add(time.Duration(d) * 24 * time.Hour).Truncate(24 * time.Hour) }
	ent := func(name, kind, desc string) (int64, error) {
		e, created, err := upsertWorldEntity(db, pid, WorldEntity{Name: name, Kind: kind, Description: desc, Basis: basisSynthetic})
		if err != nil {
			return 0, err
		}
		if created {
			counts["entities"]++
		}
		return e.ID, nil
	}
	evidence := func(title, body string, at time.Time, access string, locs []map[string]any) (int64, error) {
		payload := map[string]any{"connector": syntheticSource, "note": "synthetic sample data"}
		if locs != nil {
			payload["locations"] = locs
		}
		raw, _ := json.Marshal(payload)
		row, inserted, err := recordEvidenceDetailed(db, Evidence{Kind: "document", Title: "[Synthetic] " + title, Body: body, Source: syntheticSource,
			SourceRef: "synthetic://" + fmt.Sprint(at.Unix()), Payload: raw, EventTime: &at, ObservedAt: &at, Basis: basisSynthetic, Access: access}, pid)
		if err != nil {
			return 0, err
		}
		if inserted {
			counts["evidence"]++
		}
		id, _ := row["id"].(int64)
		return id, nil
	}
	loc := func(id int64, lat, lon float64, label, precision string) error {
		counts["locations"]++
		return putEntityLocation(db, pid, EntityLocation{EntityID: id, Lat: lat, Lon: lon, Label: label, Precision: precision,
			CountryID: nearestCountry(lat, lon), Source: syntheticSource, Basis: basisSynthetic, Provenance: "Synthetic sample coordinates"})
	}
	link := func(from, to int64, typ, status string, ev []int64, note string) error {
		_, created, err := ensureRelationship(db, pid, Relationship{From: from, To: to, Type: typ, Status: status, EvidenceIDs: ev, Note: note, Basis: basisSynthetic})
		if created {
			counts["relationships"]++
		}
		return err
	}

	rotterdam, err := ent("Synthetic Port North", "facility", "Synthetic container port for the sample scenario.")
	if err != nil {
		return nil, err
	}
	shanghai, err := ent("Synthetic Port East", "facility", "Synthetic container port for the sample scenario.")
	if err != nil {
		return nil, err
	}
	canal, err := ent("Synthetic Canal Passage", "infrastructure", "Synthetic shipping canal.")
	if err != nil {
		return nil, err
	}
	depot, err := ent("Synthetic Inland Depot", "facility", "Synthetic depot with no known location (shows a missing location).")
	if err != nil {
		return nil, err
	}
	index, err := ent("Synthetic Freight Rate Index", "indicator", "Synthetic weekly freight rate index.")
	if err != nil {
		return nil, err
	}
	throughput, err := ent("Synthetic Port North throughput", "indicator", "Synthetic weekly container throughput.")
	if err != nil {
		return nil, err
	}
	operator, err := ent("Synthetic Shipping Line", "organization", "Synthetic carrier.")
	if err != nil {
		return nil, err
	}
	for _, e := range []struct {
		id                    int64
		lat, lon              float64
		label, precision      string
	}{
		{rotterdam, 51.95, 4.14, "North Sea coast", "city"},
		{shanghai, 30.63, 122.06, "East China Sea coast", "city"},
		{canal, 30.58, 32.27, "Canal passage", "point"},
		{operator, 55.68, 12.57, "Head office", "city"},
	} {
		if err := loc(e.id, e.lat, e.lon, e.label, e.precision); err != nil {
			return nil, err
		}
	}
	// Countries are real places; they are placed from the gazetteer.
	countriesIDs := map[string]int64{}
	for _, c := range []string{"Netherlands", "China", "Egypt", "Denmark"} {
		id, err := ent(c, "region", "")
		if err != nil {
			return nil, err
		}
		countriesIDs[c] = id
	}

	e1, err := evidence("Port North reports record weekly volume", "Synthetic report of throughput at Port North.", day(-3), accessOpen,
		[]map[string]any{{"lat": 51.95, "lon": 4.14, "label": "Synthetic Port North", "relationship": "event_location", "precision": "city", "provenance": "Synthetic sample coordinates"}})
	if err != nil {
		return nil, err
	}
	e2, err := evidence("Canal transits fall after a grounding", "Synthetic account of reduced transits.", day(-2), accessOpen,
		[]map[string]any{{"lat": 30.58, "lon": 32.27, "label": "Synthetic Canal Passage", "relationship": "event_location", "precision": "point", "provenance": "Synthetic sample coordinates"}})
	if err != nil {
		return nil, err
	}
	e3, err := evidence("Second account gives a different transit count", "Synthetic conflicting report.", day(-2), accessOpen, nil)
	if err != nil {
		return nil, err
	}
	e4, err := evidence("Carrier contract terms (restricted)", "Synthetic restricted record: only operators and explicitly granted agents see this text.", day(-1), accessRestricted,
		[]map[string]any{{"lat": 55.68, "lon": 12.57, "label": "Synthetic Shipping Line", "relationship": "subject_location", "precision": "city", "provenance": "Synthetic sample coordinates"}})
	if err != nil {
		return nil, err
	}

	for _, l := range []struct {
		from, to     int64
		typ, status  string
		ev           []int64
		note         string
	}{
		{rotterdam, countriesIDs["Netherlands"], "located_in", "observed", []int64{e1}, "synthetic"},
		{shanghai, countriesIDs["China"], "located_in", "observed", []int64{e1}, "synthetic"},
		{canal, countriesIDs["Egypt"], "located_in", "observed", []int64{e2}, "synthetic"},
		{operator, countriesIDs["Denmark"], "headquartered_in", "reported", []int64{e4}, "synthetic"},
		{shanghai, rotterdam, "ships_to", "reported", []int64{e1}, "synthetic route"},
		{shanghai, canal, "routes_via", "inferred", nil, "synthetic: inferred from the route geography"},
		{operator, shanghai, "operates_at", "assumed", nil, "synthetic operator assumption"},
		{index, canal, "measures", "observed", []int64{e2}, "synthetic"},
		{throughput, rotterdam, "measures", "observed", []int64{e1}, "synthetic"},
		{depot, rotterdam, "supplies", "assumed", nil, "synthetic"},
	} {
		if err := link(l.from, l.to, l.typ, l.status, l.ev, l.note); err != nil {
			return nil, err
		}
	}

	// A weekly index that stopped six weeks ago: stale.
	for w := 0; w < 10; w++ {
		at := day(-42 - 7*(9-w))
		if ok, err := putStateValue(db, pid, StateValue{EntityID: index, Variable: "value", Value: 1800 + float64(w*35), Unit: "points", ValidAt: at,
			Source: syntheticSource, Basis: basisSynthetic}); err != nil {
			return nil, err
		} else if ok {
			counts["state_values"]++
		}
	}
	// Weekly throughput from two sources that disagree on the latest week.
	for w := 0; w < 8; w++ {
		at := day(-7 * (7 - w))
		v := 210000 + float64(w*1500)
		for src, val := range map[string]float64{"synthetic": v, "synthetic-port-authority": v} {
			if w == 7 && src == "synthetic-port-authority" {
				val = v * 0.9 // the conflicting figure
			}
			if ok, err := putStateValue(db, pid, StateValue{EntityID: throughput, Variable: "teu", Value: val, Unit: "TEU", ValidAt: at,
				Source: src, Basis: basisSynthetic}); err != nil {
				return nil, err
			} else if ok {
				counts["state_values"]++
			}
		}
	}
	// Two claims that disagree on the same day's canal transits.
	t := day(-2)
	for _, c := range []struct {
		ev  int64
		val float64
	}{{e2, 52}, {e3, 38}} {
		v := c.val
		if _, err := insertClaim(db, pid, Claim{EvidenceID: c.ev, SubjectID: &canal, Subject: "Synthetic Canal Passage", Predicate: "daily_transits",
			Value: &v, Unit: "vessels", ClaimTime: &t, Extractor: syntheticSource, Basis: basisSynthetic}); err != nil {
			return nil, err
		}
		counts["claims"]++
	}
	if _, err := placeFromGazetteer(db, pid); err != nil {
		return nil, err
	}
	return counts, nil
}

// clearSynthetic removes every synthetic row. Entities that are still
// synthetic go with their links and locations; entities real data has
// since confirmed stay.
func clearSynthetic(db *sql.DB, pid string) (map[string]int64, error) {
	out := map[string]int64{}
	stmts := []struct{ key, q string }{
		{"relationships", `DELETE FROM intel_relationships WHERE project_id = ?1 AND (basis = 'synthetic'
			OR from_entity_id IN (SELECT id FROM entities WHERE project_id = ?1 AND basis = 'synthetic')
			OR to_entity_id IN (SELECT id FROM entities WHERE project_id = ?1 AND basis = 'synthetic'))`},
		{"locations", `DELETE FROM intel_entity_locations WHERE project_id = ?1 AND (basis = 'synthetic'
			OR entity_id IN (SELECT id FROM entities WHERE project_id = ?1 AND basis = 'synthetic'))`},
		{"state_values", `DELETE FROM intel_state_values WHERE project_id = ?1 AND (basis = 'synthetic'
			OR entity_id IN (SELECT id FROM entities WHERE project_id = ?1 AND basis = 'synthetic'))`},
		{"claims", `DELETE FROM intel_claims WHERE project_id = ?1 AND (basis = 'synthetic'
			OR evidence_id IN (SELECT id FROM intelligence_evidence WHERE project_id = ?1 AND basis = 'synthetic'))`},
		{"question_links", `DELETE FROM intel_question_evidence WHERE evidence_id IN (SELECT id FROM intelligence_evidence WHERE project_id = ?1 AND basis = 'synthetic')`},
		{"evidence", `DELETE FROM intelligence_evidence WHERE project_id = ?1 AND basis = 'synthetic'`},
		{"geocode_attempts", `DELETE FROM intel_geocode_attempts WHERE project_id = ?1 AND entity_id IN (SELECT id FROM entities WHERE project_id = ?1 AND basis = 'synthetic')`},
		{"entities", `DELETE FROM entities WHERE project_id = ?1 AND basis = 'synthetic'`},
	}
	for _, s := range stmts {
		res, err := db.Exec(s.q, pid)
		if err != nil {
			return out, fmt.Errorf("%s: %w", s.key, err)
		}
		out[s.key], _ = res.RowsAffected()
	}
	return out, nil
}
