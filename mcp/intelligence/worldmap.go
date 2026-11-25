package main

// world_map: everything the app knows, laid out for a map. Located
// entities become points, relationships between located entities become
// links, evidence with its own attributed locations becomes evidence
// points, and every other entity is listed as unmapped with the reason.
// Each item carries its basis, its quality flags and where it came from.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type mapFilter struct {
	Bases map[string]bool // empty = all
	Kind  string
	AsOf  *time.Time
}

func (f mapFilter) basisOK(b string) bool { return len(f.Bases) == 0 || f.Bases[b] }

func parseMapFilter(args map[string]any) (mapFilter, error) {
	f := mapFilter{Bases: map[string]bool{}, Kind: strings.TrimSpace(strArg(args, "kind"))}
	for _, b := range strings.Split(strArg(args, "basis"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			if !validBasis(b) {
				return f, errors.New("basis must be observed, inferred or synthetic")
			}
			f.Bases[b] = true
		}
	}
	at, err := timeArg(args, "as_of")
	f.AsOf = at
	return f, err
}

func buildWorldMap(db *sql.DB, pid string, rd reader, f mapFilter, now time.Time) (map[string]any, error) {
	quality, err := dataQuality(db, pid, now)
	if err != nil {
		return nil, err
	}
	locs, err := entityLocations(db, pid)
	if err != nil {
		return nil, err
	}
	// Counts per entity, each in one pass.
	count := func(q string) map[int64]int {
		out := map[int64]int{}
		rows, err := db.Query(q, pid)
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var n int
			if rows.Scan(&id, &n) == nil {
				out[id] = n
			}
		}
		return out
	}
	when := ts(now)
	if f.AsOf != nil {
		when = ts(*f.AsOf)
	}
	claims := count(`SELECT id, n FROM (SELECT subject_entity_id AS id, COUNT(*) AS n FROM intel_claims WHERE project_id = ?1 AND subject_entity_id IS NOT NULL GROUP BY 1
		UNION ALL SELECT object_entity_id, COUNT(*) FROM intel_claims WHERE project_id = ?1 AND object_entity_id IS NOT NULL GROUP BY 1)`)
	questions := count(`SELECT CAST(j.value AS INTEGER), COUNT(*) FROM intel_questions q, json_each(q.entity_ids) j WHERE q.project_id = ? AND q.status = 'open' GROUP BY 1`)
	evidenceByName := map[string]int{}
	if rows, err := db.Query(`SELECT lower(j.value), COUNT(*) FROM intelligence_evidence e, json_each(e.entity_refs) j WHERE e.project_id = ? GROUP BY 1`, pid); err == nil {
		for rows.Next() {
			var name string
			var n int
			if rows.Scan(&name, &n) == nil {
				evidenceByName[name] = n
			}
		}
		rows.Close()
	}
	latest := map[int64][]map[string]any{}
	stateCount := map[int64]int{}
	for _, s := range quality.Series {
		stateCount[s.EntityID] += s.Points
		latest[s.EntityID] = append(latest[s.EntityID], map[string]any{"variable": s.Variable, "value": s.LatestValue, "unit": s.Unit,
			"valid_at": s.LatestAt, "source": s.Source, "status": s.Status, "cadence_days": s.CadenceDays, "age_days": s.AgeDays, "basis": s.Basis})
	}

	// Relationships valid at the cut-off.
	rels := []map[string]any{}
	relCount := map[int64]int{}
	rows, err := db.Query(`SELECT r.id, r.from_entity_id, r.to_entity_id, r.rel_type, r.status, r.basis, r.confidence, r.evidence_ids, r.note
		FROM intel_relationships r WHERE r.project_id = ? AND (r.valid_from IS NULL OR r.valid_from <= ?) AND (r.valid_to IS NULL OR r.valid_to > ?)
		AND (r.retracted_at IS NULL OR r.retracted_at > ?) ORDER BY r.id LIMIT 5000`, pid, when, when, when)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, from, to int64
		var typ, status, basis, ev, note string
		var conf sql.NullFloat64
		if err := rows.Scan(&id, &from, &to, &typ, &status, &basis, &conf, &ev, &note); err != nil {
			rows.Close()
			return nil, err
		}
		if !f.basisOK(basis) {
			continue
		}
		var evIDs []int64
		_ = json.Unmarshal([]byte(ev), &evIDs)
		row := map[string]any{"id": id, "from": from, "to": to, "type": typ, "status": status, "basis": basis, "evidence_ids": evIDs, "note": note}
		if conf.Valid {
			row["confidence"] = conf.Float64
		}
		rels = append(rels, row)
		relCount[from]++
		relCount[to]++
	}
	rows.Close()

	geoKinds := []string{}
	for k := range geocodableKinds {
		geoKinds = append(geoKinds, k)
	}
	attempts := map[int64]string{}
	if rows, err := db.Query(`SELECT entity_id, outcome || ': ' || detail FROM intel_geocode_attempts WHERE project_id = ?`, pid); err == nil {
		for rows.Next() {
			var id int64
			var d string
			if rows.Scan(&id, &d) == nil {
				attempts[id] = d
			}
		}
		rows.Close()
	}

	entities, unmapped := []map[string]any{}, []map[string]any{}
	countries := map[string]map[string]int{}
	bumpCountry := func(id, key string) {
		if id == "" {
			return
		}
		if countries[id] == nil {
			countries[id] = map[string]int{}
		}
		countries[id][key]++
	}
	erows, err := db.Query(`SELECT id, canonical, kind, basis, description FROM entities WHERE project_id = ? ORDER BY id LIMIT 5000`, pid)
	if err != nil {
		return nil, err
	}
	for erows.Next() {
		var id int64
		var name, kind, basis, desc string
		if err := erows.Scan(&id, &name, &kind, &basis, &desc); err != nil {
			erows.Close()
			return nil, err
		}
		if !f.basisOK(basis) || (f.Kind != "" && f.Kind != kind) {
			continue
		}
		flags := quality.ByEntity[id]
		if flags == nil {
			flags = []string{}
		}
		row := map[string]any{"id": id, "name": name, "kind": kind, "basis": basis, "description": truncate(desc, 200), "flags": flags,
			"counts": map[string]int{"relationships": relCount[id], "claims": claims[id], "state": stateCount[id],
				"evidence": evidenceByName[strings.ToLower(name)], "questions": questions[id]},
			"latest": latest[id]}
		if best := bestLocation(locs[id]); best != nil {
			row["location"] = best
			row["locations"] = locs[id]
			cid := best.CountryID
			if cid == "" {
				cid = nearestCountry(best.Lat, best.Lon)
			}
			row["country_id"] = cid
			bumpCountry(cid, "entities")
			for _, fl := range flags {
				bumpCountry(cid, fl)
			}
			entities = append(entities, row)
			continue
		}
		reason := "not looked up yet"
		switch {
		case kind == "public_role":
			reason = "public roles are not placed on the map"
		case !geocodableKinds[kind]:
			k := strings.ReplaceAll(kind, "_", " ")
			article := "a "
			if strings.ContainsRune("aeiou", rune(k[0])) {
				article = "an "
			}
			reason = article + k + " has no place of its own; see its links"
		case basis == basisSynthetic:
			reason = "synthetic entity without sample coordinates"
		case attempts[id] != "":
			reason = "lookup " + attempts[id]
		}
		row["reason"] = reason
		unmapped = append(unmapped, row)
	}
	erows.Close()

	// Evidence that carries its own attributed locations.
	points := []map[string]any{}
	hidden := 0
	ev, err := searchEvidenceWhere(db, pid, `json_type(payload, '$.locations') = 'array'`, 1000)
	if err != nil {
		return nil, err
	}
	for _, row := range ev {
		if !f.basisOK(row["basis"].(string)) {
			continue
		}
		if rd.redactEvidence(row) {
			hidden++
			continue // a redacted row has no locations to show
		}
		locations, _ := row["locations"].([]EvidenceLocation)
		for _, l := range locations {
			if l.Latitude == nil || l.Longitude == nil {
				continue
			}
			cid := nearestCountry(*l.Latitude, *l.Longitude)
			bumpCountry(cid, "evidence")
			points = append(points, map[string]any{"evidence_id": row["id"], "title": row["title"], "source": row["source"], "source_ref": row["source_ref"],
				"kind": row["kind"], "basis": row["basis"], "access": row["access"], "event_time": row["event_time"], "observed_at": row["observed_at"],
				"lat": *l.Latitude, "lon": *l.Longitude, "label": orStr(l.Label, l.Country), "relationship": l.Relationship,
				"precision": l.Precision, "confidence": l.Confidence, "provenance": l.Provenance, "country_id": cid})
		}
	}
	sort.SliceStable(unmapped, func(i, j int) bool { return unmapped[i]["kind"].(string) < unmapped[j]["kind"].(string) })
	issues := quality.Issues
	if len(issues) > 200 {
		issues = issues[:200]
	}
	return map[string]any{
		"generated_at": ts(now), "as_of": timeString(f.AsOf), "entities": entities, "relationships": rels,
		"evidence_points": points, "unmapped": unmapped, "countries": countries,
		"quality": map[string]any{"summary": quality.Summary, "issues": issues, "by_entity": quality.ByEntity, "sources": quality.Sources},
		"basis": quality.Basis, "access": quality.Access, "hidden_evidence": hidden, "geocodable_kinds": geoKinds,
	}, nil
}

// searchEvidenceWhere runs searchEvidence for ids matching a raw condition.
func searchEvidenceWhere(db *sql.DB, pid, cond string, limit int) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT id FROM intelligence_evidence WHERE project_id = ? AND `+cond+` ORDER BY id DESC LIMIT ?`, pid, limit)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []map[string]any{}
	for i := 0; i < len(ids); i += 200 {
		chunk, err := searchEvidence(db, EvidenceQuery{ProjectID: pid, IDs: ids[i:min(i+200, len(ids))], Limit: 200})
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
	}
	return out, nil
}
