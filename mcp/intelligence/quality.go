package main

// Data quality: what is stale, what is missing, and where sources
// disagree. Computed on demand from the stored data; nothing is hidden or
// auto-resolved. A conflict lists both sides with their evidence so an
// operator (or the claim review tools) can settle it.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type QualityIssue struct {
	Type      string         `json:"type"`     // stale | missing | conflict
	Severity  string         `json:"severity"` // info | warn | error
	Subject   string         `json:"subject"`  // source | series | entity | question | location | claim | evidence
	Code      string         `json:"code"`
	Title     string         `json:"title"`
	Detail    string         `json:"detail"`
	EntityIDs []int64        `json:"entity_ids,omitempty"`
	Refs      map[string]any `json:"refs,omitempty"`
	Since     string         `json:"since,omitempty"`
}

type QualityReport struct {
	GeneratedAt string                     `json:"generated_at"`
	Summary     map[string]int             `json:"summary"`
	Issues      []QualityIssue             `json:"issues"`
	ByEntity    map[int64][]string         `json:"by_entity"`
	Series      []SeriesFreshness          `json:"series"`
	Sources     []map[string]any           `json:"sources"`
	Basis       map[string]map[string]int  `json:"basis"`
	Access      map[string]int             `json:"access"`
	entityFlags map[int64]map[string]bool
}

type SeriesFreshness struct {
	EntityID    int64   `json:"entity_id"`
	Entity      string  `json:"entity"`
	Variable    string  `json:"variable"`
	Source      string  `json:"source"`
	Points      int     `json:"points"`
	LatestAt    string  `json:"latest_at"`
	LatestValue float64 `json:"latest_value"`
	Unit        string  `json:"unit"`
	CadenceDays float64 `json:"cadence_days"` // median gap; 0 = unknown
	AgeDays     float64 `json:"age_days"`
	Status      string  `json:"status"` // fresh | stale | unknown
	Basis       string  `json:"basis"`
}

func (r *QualityReport) add(i QualityIssue) {
	r.Issues = append(r.Issues, i)
	r.Summary[i.Type]++
	for _, id := range i.EntityIDs {
		if r.entityFlags[id] == nil {
			r.entityFlags[id] = map[string]bool{}
		}
		r.entityFlags[id][i.Type] = true
	}
}

// sourceStaleAfter: a source is stale when its last success is older than
// three intervals (at least an hour), so one missed run is not an alarm.
func sourceStaleAfter(intervalSec int) time.Duration {
	d := 3 * time.Duration(intervalSec) * time.Second
	if d < time.Hour {
		d = time.Hour
	}
	return d
}

func dataQuality(db *sql.DB, pid string, now time.Time) (*QualityReport, error) {
	r := &QualityReport{GeneratedAt: ts(now), Summary: map[string]int{"stale": 0, "missing": 0, "conflict": 0},
		Issues: []QualityIssue{}, ByEntity: map[int64][]string{}, entityFlags: map[int64]map[string]bool{},
		Series: []SeriesFreshness{}, Sources: []map[string]any{}}
	steps := []func(*sql.DB, string, time.Time, *QualityReport) error{
		qualitySources, qualitySeries, qualityStateConflicts, qualityClaimConflicts, qualityLocations, qualityQuestions, qualityEvidence, qualityBasis,
	}
	for _, step := range steps {
		if err := step(db, pid, now, r); err != nil {
			return nil, err
		}
	}
	for id, flags := range r.entityFlags {
		for f := range flags {
			r.ByEntity[id] = append(r.ByEntity[id], f)
		}
		sort.Strings(r.ByEntity[id])
	}
	sev := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(r.Issues, func(i, j int) bool { return sev[r.Issues[i].Severity] < sev[r.Issues[j].Severity] })
	return r, nil
}

func qualitySources(db *sql.DB, pid string, now time.Time, r *QualityReport) error {
	sources, err := listSources(db, pid)
	if err != nil {
		return err
	}
	for _, s := range sources {
		status := "fresh"
		switch {
		case !s.Enabled:
			status = "disabled"
		case s.LastSuccessAt == nil && s.LastRunAt == nil:
			status = "never_run"
		case s.LastSuccessAt == nil:
			status = "failing"
			r.add(QualityIssue{Type: "missing", Severity: "error", Subject: "source", Code: "source_never_succeeded",
				Title: s.Slug + " has never delivered data", Detail: orStr(s.LastError, "no successful run yet"), Refs: map[string]any{"source": s.Slug}})
		case now.Sub(*s.LastSuccessAt) > sourceStaleAfter(s.IntervalSec):
			status = "stale"
			r.add(QualityIssue{Type: "stale", Severity: "warn", Subject: "source", Code: "source_stale",
				Title:  s.Slug + " last delivered " + humanAge(now.Sub(*s.LastSuccessAt)) + " ago",
				Detail: fmt.Sprintf("Expected every %s. %s", humanAge(time.Duration(s.IntervalSec)*time.Second), s.LastError),
				Refs:   map[string]any{"source": s.Slug}, Since: ts(*s.LastSuccessAt)})
		case s.LastError != "":
			status = "degraded"
		}
		r.Sources = append(r.Sources, map[string]any{"slug": s.Slug, "status": status, "enabled": s.Enabled, "access": s.Access,
			"last_success_at": timeString(s.LastSuccessAt), "interval_sec": s.IntervalSec, "last_error": s.LastError, "retention_days": s.RetentionDays})
		// Configured series that never produced a value.
		if s.Enabled && (s.Connector == "bls" || s.Connector == "fred") {
			for _, spec := range cfgSeries(s.Config) {
				var n int
				_ = db.QueryRow(`SELECT COUNT(*) FROM intel_state_values v JOIN entities e ON e.id = v.entity_id
					WHERE v.project_id = ? AND v.source = ? AND json_extract(e.external_ids, '$.' || ?) = ?`, pid, s.Slug, s.Slug, spec.ID).Scan(&n)
				if n == 0 {
					r.add(QualityIssue{Type: "missing", Severity: "warn", Subject: "series", Code: "series_never_loaded",
						Title: spec.Name + " has no values", Detail: "Configured on " + s.Slug + " (" + spec.ID + ") but nothing was loaded.",
						Refs: map[string]any{"source": s.Slug, "series": spec.ID}})
				}
			}
		}
	}
	return nil
}

// qualitySeries measures each state series against its own cadence: the
// median gap between its latest points. Stale = older than 2.5 cadences.
func qualitySeries(db *sql.DB, pid string, now time.Time, r *QualityReport) error {
	rows, err := db.Query(`SELECT v.entity_id, e.canonical, v.variable, v.source, v.valid_at, v.value, v.unit, v.basis
		FROM intel_state_values v JOIN entities e ON e.id = v.entity_id WHERE v.project_id = ?
		ORDER BY v.entity_id, v.variable, v.source, v.valid_at DESC`, pid)
	if err != nil {
		return err
	}
	type key struct {
		id       int64
		variable string
		source   string
	}
	type series struct {
		name, unit, basis string
		at                []time.Time
		latest            float64
	}
	order := []key{}
	all := map[key]*series{}
	for rows.Next() {
		var k key
		var name, unit, basis string
		var at flexibleTime
		var value float64
		if err := rows.Scan(&k.id, &name, &k.variable, &k.source, &at, &value, &unit, &basis); err != nil {
			rows.Close()
			return err
		}
		s := all[k]
		if s == nil {
			s = &series{name: name, unit: unit, basis: basis, latest: value}
			all[k] = s
			order = append(order, k)
		}
		if t := at.Time(); t != nil && len(s.at) < 13 {
			s.at = append(s.at, *t)
		}
	}
	rows.Close()
	for _, k := range order {
		s := all[k]
		if len(s.at) == 0 {
			continue
		}
		f := SeriesFreshness{EntityID: k.id, Entity: s.name, Variable: k.variable, Source: k.source, Points: len(s.at),
			LatestAt: ts(s.at[0]), LatestValue: s.latest, Unit: s.unit, AgeDays: round1(now.Sub(s.at[0]).Hours() / 24), Status: "unknown", Basis: s.basis}
		if len(s.at) >= 2 {
			gaps := []float64{}
			for i := 0; i+1 < len(s.at); i++ {
				gaps = append(gaps, s.at[i].Sub(s.at[i+1]).Hours()/24)
			}
			sort.Float64s(gaps)
			f.CadenceDays = round1(gaps[len(gaps)/2])
			// Statistical releases lag their reference period; allow 2.5
			// cadences (a monthly series is stale after ~75 days).
			if f.CadenceDays > 0 && f.AgeDays > 2.5*math.Max(f.CadenceDays, 1) {
				f.Status = "stale"
				r.add(QualityIssue{Type: "stale", Severity: "warn", Subject: "series", Code: "series_stale", EntityIDs: []int64{k.id},
					Title:  fmt.Sprintf("%s (%s) is %.0f days old", s.name, k.source, f.AgeDays),
					Detail: fmt.Sprintf("Latest value is for %s; the series usually updates every %.0f days.", s.at[0].Format("2006-01-02"), f.CadenceDays),
					Refs:   map[string]any{"source": k.source, "variable": k.variable}, Since: ts(s.at[0])})
			} else if f.CadenceDays > 0 {
				f.Status = "fresh"
			}
		}
		r.Series = append(r.Series, f)
	}
	return nil
}

// qualityStateConflicts: two sources giving the same entity and variable
// a different value for the same day.
func qualityStateConflicts(db *sql.DB, pid string, now time.Time, r *QualityReport) error {
	rows, err := db.Query(`SELECT a.entity_id, e.canonical, a.variable, substr(a.valid_at, 1, 10), a.source, a.value, a.evidence_id, b.source, b.value, b.evidence_id, a.unit
		FROM intel_state_values a JOIN intel_state_values b
		  ON b.project_id = a.project_id AND b.entity_id = a.entity_id AND b.variable = a.variable
		 AND substr(b.valid_at, 1, 10) = substr(a.valid_at, 1, 10) AND b.source > a.source
		JOIN entities e ON e.id = a.entity_id
		WHERE a.project_id = ? LIMIT 200`, pid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, variable, day, sa, sb, unit string
		var va, vb float64
		var ea, eb sql.NullInt64
		if err := rows.Scan(&id, &name, &variable, &day, &sa, &va, &ea, &sb, &vb, &eb, &unit); err != nil {
			return err
		}
		if relDiff(va, vb) <= 0.005 {
			continue
		}
		r.add(QualityIssue{Type: "conflict", Severity: "error", Subject: "series", Code: "value_conflict", EntityIDs: []int64{id},
			Title:  fmt.Sprintf("%s %s on %s: %s says %g, %s says %g", name, variable, day, sa, va, sb, vb),
			Detail: fmt.Sprintf("Values differ by %.1f%%. Neither is preferred automatically.", relDiff(va, vb)*100),
			Refs: map[string]any{"sides": []map[string]any{{"source": sa, "value": va, "unit": unit, "evidence_id": nullID(ea)},
				{"source": sb, "value": vb, "unit": unit, "evidence_id": nullID(eb)}}}})
	}
	return rows.Err()
}

// qualityClaimConflicts: claims about the same subject and predicate that
// disagree. Numeric values more than 1% apart are conflicts; different
// objects are reported as possible conflicts (many predicates allow more
// than one object). Rejected claims are ignored.
func qualityClaimConflicts(db *sql.DB, pid string, now time.Time, r *QualityReport) error {
	rows, err := db.Query(`SELECT a.id, b.id, a.subject_entity_id, a.subject, a.predicate, a.object, b.object, a.value, b.value, a.unit, a.evidence_id, b.evidence_id, a.basis, b.basis
		FROM intel_claims a JOIN intel_claims b ON b.project_id = a.project_id AND b.id > a.id
		 AND lower(b.predicate) = lower(a.predicate)
		 AND (b.subject_entity_id = a.subject_entity_id OR (a.subject_entity_id IS NULL AND lower(b.subject) = lower(a.subject)))
		 AND COALESCE(substr(b.claim_time, 1, 10), '') = COALESCE(substr(a.claim_time, 1, 10), '')
		 AND b.evidence_id <> a.evidence_id
		WHERE a.project_id = ? AND a.status <> 'rejected' AND b.status <> 'rejected'
		ORDER BY a.id DESC LIMIT 500`, pid)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var ida, idb, eva, evb int64
		var subj sql.NullInt64
		var subject, predicate, oa, ob, unit, ba, bb string
		var va, vb sql.NullFloat64
		if err := rows.Scan(&ida, &idb, &subj, &subject, &predicate, &oa, &ob, &va, &vb, &unit, &eva, &evb, &ba, &bb); err != nil {
			return err
		}
		ids := []int64{}
		if subj.Valid {
			ids = append(ids, subj.Int64)
		}
		refs := map[string]any{"claim_ids": []int64{ida, idb}, "evidence_ids": []int64{eva, evb}, "bases": []string{ba, bb}}
		switch {
		case va.Valid && vb.Valid && relDiff(va.Float64, vb.Float64) > 0.01:
			r.add(QualityIssue{Type: "conflict", Severity: "error", Subject: "claim", Code: "claim_value_conflict", EntityIDs: ids,
				Title:  fmt.Sprintf("%s %s: %g vs %g %s", subject, predicate, va.Float64, vb.Float64, unit),
				Detail: fmt.Sprintf("Claims #%d and #%d cite different evidence (#%d, #%d).", ida, idb, eva, evb), Refs: refs})
		case !va.Valid && !vb.Valid && normText(oa) != "" && normText(ob) != "" && normText(oa) != normText(ob):
			k := fmt.Sprint(subj.Int64, subject, predicate, normText(oa), normText(ob))
			if seen[k] {
				continue
			}
			seen[k] = true
			r.add(QualityIssue{Type: "conflict", Severity: "info", Subject: "claim", Code: "claim_object_differs", EntityIDs: ids,
				Title:  fmt.Sprintf("%s %s: %q vs %q", subject, predicate, oa, ob),
				Detail: fmt.Sprintf("Possible conflict: claims #%d and #%d name different objects. Accept or reject one if they cannot both hold.", ida, idb), Refs: refs})
		}
	}
	return rows.Err()
}

// qualityLocations: entities that should have a place and do not, and
// entities whose sources put them in different places.
func qualityLocations(db *sql.DB, pid string, now time.Time, r *QualityReport) error {
	locs, err := entityLocations(db, pid)
	if err != nil {
		return err
	}
	for id, ls := range locs {
		for i := 0; i < len(ls); i++ {
			for j := i + 1; j < len(ls); j++ {
				a, b := ls[i], ls[j]
				d := haversineKM(a.Lat, a.Lon, b.Lat, b.Lon)
				differentCountry := a.CountryID != "" && b.CountryID != "" && a.CountryID != b.CountryID
				bothPrecise := precisionRank[a.Precision] >= precisionRank["city"] && precisionRank[b.Precision] >= precisionRank["city"]
				if differentCountry || (bothPrecise && d > 100) {
					r.add(QualityIssue{Type: "conflict", Severity: "warn", Subject: "location", Code: "location_conflict", EntityIDs: []int64{id},
						Title:  fmt.Sprintf("%s and %s place this entity %.0f km apart", a.Source, b.Source, d),
						Detail: fmt.Sprintf("%s: %s (%s). %s: %s (%s).", a.Source, orStr(a.Label, "coordinates"), a.Precision, b.Source, orStr(b.Label, "coordinates"), b.Precision),
						Refs:   map[string]any{"location_ids": []int64{a.ID, b.ID}}})
				}
			}
		}
	}
	rows, err := db.Query(`SELECT e.id, e.canonical, e.kind, a.outcome, a.detail FROM entities e
		LEFT JOIN intel_geocode_attempts a ON a.project_id = e.project_id AND a.entity_id = e.id AND a.source = 'wikipedia'
		WHERE e.project_id = ? AND e.kind IN ('place','region','facility','infrastructure')
		  AND NOT EXISTS (SELECT 1 FROM intel_entity_locations l WHERE l.project_id = e.project_id AND l.entity_id = e.id AND l.retracted_at IS NULL)
		ORDER BY e.id DESC LIMIT 200`, pid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, kind string
		var outcome, detail sql.NullString
		if err := rows.Scan(&id, &name, &kind, &outcome, &detail); err != nil {
			return err
		}
		why := "Not looked up yet; the geocode connector will try it."
		if outcome.Valid {
			why = "Lookup " + outcome.String + ": " + detail.String + ". Use entity_locate to place it."
		}
		r.add(QualityIssue{Type: "missing", Severity: "info", Subject: "location", Code: "location_missing", EntityIDs: []int64{id},
			Title: name + " (" + kind + ") has no location", Detail: why})
	}
	return rows.Err()
}

// qualityQuestions: open questions with no market price for a while, or no
// linked evidence at all.
func qualityQuestions(db *sql.DB, pid string, now time.Time, r *QualityReport) error {
	rows, err := db.Query(`SELECT q.id, q.text, q.origin, q.venue,
		 (SELECT MAX(observed_at) FROM intel_question_prices p WHERE p.question_id = q.id),
		 (SELECT COUNT(*) FROM intel_question_evidence x WHERE x.question_id = q.id), q.entity_ids, q.created_at
		FROM intel_questions q WHERE q.project_id = ? AND q.status = 'open' AND q.deadline > ? ORDER BY q.deadline LIMIT 500`, pid, ts(now))
	if err != nil {
		return err
	}
	defer rows.Close()
	noEvidence := 0
	var sample []int64
	for rows.Next() {
		var id int64
		var text, origin, venue, entityIDs string
		var lastPrice, created flexibleTime
		var nEvidence int
		if err := rows.Scan(&id, &text, &origin, &venue, &lastPrice, &nEvidence, &entityIDs, &created); err != nil {
			return err
		}
		var ids []int64
		_ = json.Unmarshal([]byte(entityIDs), &ids)
		if origin == "market" {
			if lp := lastPrice.Time(); lp == nil || now.Sub(*lp) > 6*time.Hour {
				age := "never priced"
				if lp != nil {
					age = "last priced " + humanAge(now.Sub(*lp)) + " ago"
				}
				r.add(QualityIssue{Type: "stale", Severity: "warn", Subject: "question", Code: "question_price_stale", EntityIDs: ids,
					Title: truncate(text, 90), Detail: venue + " price " + age + ".", Refs: map[string]any{"question_id": id}})
			}
		}
		if nEvidence == 0 {
			if c := created.Time(); c != nil && now.Sub(*c) > 2*time.Hour {
				noEvidence++
				if len(sample) < 20 {
					sample = append(sample, id)
				}
			}
		}
	}
	if noEvidence > 0 {
		r.add(QualityIssue{Type: "missing", Severity: "info", Subject: "question", Code: "question_no_evidence",
			Title:  fmt.Sprintf("%d open questions have no linked evidence", noEvidence),
			Detail: "Their forecasts equal the market prior. The news connector rotates through open questions.",
			Refs:   map[string]any{"question_ids": sample}})
	}
	return rows.Err()
}

func qualityEvidence(db *sql.DB, pid string, now time.Time, r *QualityReport) error {
	var undated, unsourced, expired int
	_ = db.QueryRow(`SELECT COUNT(*) FROM intelligence_evidence WHERE project_id = ? AND event_time IS NULL AND published_time IS NULL AND valid_from IS NULL`, pid).Scan(&undated)
	_ = db.QueryRow(`SELECT COUNT(*) FROM intelligence_evidence WHERE project_id = ? AND source_ref = '' AND basis <> 'synthetic'`, pid).Scan(&unsourced)
	_ = db.QueryRow(`SELECT COUNT(*) FROM intelligence_evidence WHERE project_id = ? AND content_expired_at IS NOT NULL`, pid).Scan(&expired)
	if undated > 0 {
		r.add(QualityIssue{Type: "missing", Severity: "info", Subject: "evidence", Code: "evidence_undated",
			Title: fmt.Sprintf("%d evidence records have no event or publication time", undated), Detail: "They are placed by observation time only."})
	}
	if unsourced > 0 {
		r.add(QualityIssue{Type: "missing", Severity: "warn", Subject: "evidence", Code: "evidence_no_reference",
			Title: fmt.Sprintf("%d evidence records have no source reference", unsourced), Detail: "No link or identifier back to the original."})
	}
	if expired > 0 {
		r.add(QualityIssue{Type: "missing", Severity: "info", Subject: "evidence", Code: "evidence_content_expired",
			Title:  fmt.Sprintf("%d evidence records had their content removed under retention", expired),
			Detail: "Metadata and hashes remain, so forecast replays still verify."})
	}
	var weak int
	_ = db.QueryRow(`SELECT COUNT(*) FROM intel_relationships WHERE project_id = ? AND retracted_at IS NULL AND status IN ('observed','reported')
		AND evidence_ids = '[]' AND claim_ids = '[]'`, pid).Scan(&weak)
	if weak > 0 {
		r.add(QualityIssue{Type: "missing", Severity: "error", Subject: "evidence", Code: "relationship_unsupported",
			Title: fmt.Sprintf("%d observed or reported relationships cite no evidence", weak), Detail: "Retract them or downgrade them to assumed."})
	}
	return nil
}

// qualityBasis counts rows by basis and evidence by access class.
func qualityBasis(db *sql.DB, pid string, now time.Time, r *QualityReport) error {
	r.Basis = map[string]map[string]int{}
	for table, label := range map[string]string{"intelligence_evidence": "evidence", "entities": "entities", "intel_relationships": "relationships",
		"intel_claims": "claims", "intel_state_values": "state_values", "intel_entity_locations": "locations"} {
		rows, err := db.Query(`SELECT basis, COUNT(*) FROM `+table+` WHERE project_id = ? GROUP BY basis`, pid)
		if err != nil {
			return err
		}
		m := map[string]int{basisObserved: 0, basisInferred: 0, basisSynthetic: 0}
		for rows.Next() {
			var b string
			var n int
			if err := rows.Scan(&b, &n); err != nil {
				rows.Close()
				return err
			}
			m[b] = n
		}
		rows.Close()
		r.Basis[label] = m
	}
	r.Access = map[string]int{accessOpen: 0, accessInternal: 0, accessRestricted: 0}
	rows, err := db.Query(`SELECT access, COUNT(*) FROM intelligence_evidence WHERE project_id = ? GROUP BY access`, pid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var n int
		if err := rows.Scan(&a, &n); err != nil {
			return err
		}
		r.Access[a] = n
	}
	return rows.Err()
}

func relDiff(a, b float64) float64 {
	den := math.Max(math.Abs(a), math.Abs(b))
	if den == 0 {
		return 0
	}
	return math.Abs(a-b) / den
}

func normText(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func nullID(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}
