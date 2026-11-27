package main

// Assumption and sensitivity report for one stored scenario. Every factor
// and weight in the propagation is a hand-set heuristic, so the report asks
// how much the result moves when they move:
//   - one at a time: each assumption halved and raised by half, depth ±1,
//     each switch flipped; how far exposures and the top 5 change
//   - Monte Carlo: all factors and weights drawn together (seeded), giving
//     a range per entity instead of one number
//   - fragility: drop each link on the path to a top entity; does the
//     exposure survive, and how many sources hold the path up
// Results are labelled robust / sensitive / fragile *to these assumptions*.
// None of this validates the model; the historical test is the only check
// against data, and it measures association.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

type oatRow struct {
	Assumption string  `json:"assumption"`
	Kind       string  `json:"kind"`
	Base       any     `json:"base"`
	Low        any     `json:"low"`
	High       any     `json:"high"`
	ExposedLow int     `json:"exposed_low"`
	ExposedHi  int     `json:"exposed_high"`
	Top5Low    int     `json:"top5_overlap_low"`
	Top5High   int     `json:"top5_overlap_high"`
	MaxChange  float64 `json:"max_abs_impact_change"`
	Source     string  `json:"source"`
}

type rangeRow struct {
	EntityID int64   `json:"entity_id"`
	Entity   string  `json:"entity"`
	Base     float64 `json:"base_impact"`
	P5       float64 `json:"p5"`
	P50      float64 `json:"p50"`
	P95      float64 `json:"p95"`
	PExposed float64 `json:"p_exposed"`
	PTop5    float64 `json:"p_top5"`
}

type fragRow struct {
	EntityID       int64    `json:"entity_id"`
	Entity         string   `json:"entity"`
	Base           float64  `json:"base_impact"`
	PathLinks      int      `json:"path_links"`
	CriticalLinks  []string `json:"critical_links"` // removal drops the entity below min_impact
	SingleSourceOn int      `json:"single_source_links_on_path"`
	WeakOnPath     bool     `json:"weak_links_on_path"`
	AssumedOnPath  bool     `json:"assumed_links_on_path"`
	Classification string   `json:"classification"`
	ClassifiedWhy  string   `json:"why"`
}

func impactMap(res propResult, shocks []Shock) map[int64]float64 {
	out := map[int64]float64{}
	for _, e := range exposedList(res, shocks) {
		out[e.EntityID] = e.Impact
	}
	return out
}

func topIDs(m map[int64]float64, k int) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if m[ids[i]] != m[ids[j]] {
			return m[ids[i]] > m[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > k {
		ids = ids[:k]
	}
	return ids
}

func overlap(a, b []int64) int {
	n := 0
	for _, x := range a {
		if containsID(b, x) {
			n++
		}
	}
	return n
}

func maxAbsChange(a, b map[int64]float64) float64 {
	m := 0.0
	for id, v := range a {
		m = math.Max(m, math.Abs(v-b[id]))
	}
	for id, v := range b {
		m = math.Max(m, math.Abs(v-a[id]))
	}
	return round3(m)
}

func clip(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Min(float64(len(sorted)-1), math.Floor(q*float64(len(sorted)))))
	return sorted[i]
}

// sensitivityReport builds the report for a spec against its graph.
func sensitivityReport(g *worldGraph, spec scenarioSpec, draws int, seed int64) map[string]any {
	if draws <= 0 {
		draws = 200
	}
	if draws > 2000 {
		draws = 2000
	}
	base := spec.Params
	baseRes := propagate(g, spec.Shocks, base, nil)
	baseMap := impactMap(baseRes, spec.Shocks)
	baseTop := topIDs(baseMap, 5)

	// Which assumptions matter at all: rule types and statuses present in
	// the graph near the shocks.
	typesInGraph, statusesInGraph, basesInGraph := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range g.Edges {
		typesInGraph[e.Type] = true
		statusesInGraph[e.Status] = true
		basesInGraph[e.Causal] = true
	}

	rows := []oatRow{}
	try := func(name, kind string, baseV, lowV, highV any, low, high propParams) {
		lr, hr := impactMap(propagate(g, spec.Shocks, low, nil), spec.Shocks), impactMap(propagate(g, spec.Shocks, high, nil), spec.Shocks)
		rows = append(rows, oatRow{Assumption: name, Kind: kind, Base: baseV, Low: lowV, High: highV, ExposedLow: len(lr), ExposedHi: len(hr),
			Top5Low: overlap(baseTop, topIDs(lr, 5)), Top5High: overlap(baseTop, topIDs(hr, 5)),
			MaxChange: math.Max(maxAbsChange(baseMap, lr), maxAbsChange(baseMap, hr)), Source: "heuristic, not estimated from data"})
	}
	ruleNames := sortedKeys(base.Rules)
	for _, t := range ruleNames {
		if !typesInGraph[t] {
			continue
		}
		r := base.Rules[t]
		lo, hi := base.clone(), base.clone()
		lo.Rules[t] = propagationRule{clip(r.Factor*0.5, 0.05, 1), r.Direction}
		hi.Rules[t] = propagationRule{clip(r.Factor*1.5, 0.05, 1), r.Direction}
		try("factor: "+t, "propagation factor", r.Factor, round3(lo.Rules[t].Factor), round3(hi.Rules[t].Factor), lo, hi)
	}
	for _, s := range sortedKeys(base.StatusWeights) {
		if !statusesInGraph[s] {
			continue
		}
		v := base.StatusWeights[s]
		lo, hi := base.clone(), base.clone()
		lo.StatusWeights[s], hi.StatusWeights[s] = clip(v*0.5, 0.05, 1), clip(v*1.5, 0.05, 1)
		try("status weight: "+s, "evidence status weight", v, round3(lo.StatusWeights[s]), round3(hi.StatusWeights[s]), lo, hi)
	}
	for _, b := range sortedKeys(base.BasisWeights) {
		if !basesInGraph[b] || (!strongBasis(b) && !base.IncludeWeak) {
			continue
		}
		v := base.BasisWeights[b]
		lo, hi := base.clone(), base.clone()
		lo.BasisWeights[b], hi.BasisWeights[b] = clip(v*0.5, 0.05, 1), clip(v*1.5, 0.05, 1)
		try("causal-basis weight: "+orStr(b, "ungraded"), "causal-basis weight", v, round3(lo.BasisWeights[b]), round3(hi.BasisWeights[b]), lo, hi)
	}
	{
		lo, hi := base.clone(), base.clone()
		lo.MaxDepth, hi.MaxDepth = max(1, base.MaxDepth-1), min(6, base.MaxDepth+1)
		try("max depth", "structure", base.MaxDepth, lo.MaxDepth, hi.MaxDepth, lo, hi)
		lo, hi = base.clone(), base.clone()
		lo.MinImpact, hi.MinImpact = base.MinImpact*0.5, base.MinImpact*2
		try("min impact cutoff", "structure", base.MinImpact, lo.MinImpact, hi.MinImpact, lo, hi)
	}
	for _, sw := range []struct {
		name string
		set  func(*propParams, bool)
		get  bool
	}{
		{"include weak links", func(p *propParams, v bool) { p.IncludeWeak = v }, base.IncludeWeak},
		{"skip assumed links", func(p *propParams, v bool) { p.SkipAssumed = v }, base.SkipAssumed},
		{"ignore link confidence", func(p *propParams, v bool) { p.IgnoreConfidence = v }, base.IgnoreConfidence},
	} {
		flipped := base.clone()
		sw.set(&flipped, !sw.get)
		try(sw.name, "switch", sw.get, !sw.get, !sw.get, flipped, flipped)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].MaxChange > rows[j].MaxChange })

	// Monte Carlo: every factor and weight scaled by an independent U(0.5,1.5).
	rng := rand.New(rand.NewSource(seed))
	samples := map[int64][]float64{}
	top5Count := map[int64]int{}
	for d := 0; d < draws; d++ {
		p := base.clone()
		for _, t := range ruleNames {
			r := p.Rules[t]
			p.Rules[t] = propagationRule{clip(r.Factor*(0.5+rng.Float64()), 0.05, 1), r.Direction}
		}
		for _, s := range sortedKeys(p.StatusWeights) {
			p.StatusWeights[s] = clip(p.StatusWeights[s]*(0.5+rng.Float64()), 0.05, 1)
		}
		for _, b := range sortedKeys(p.BasisWeights) {
			p.BasisWeights[b] = clip(p.BasisWeights[b]*(0.5+rng.Float64()), 0.05, 1)
		}
		m := impactMap(propagate(g, spec.Shocks, p, nil), spec.Shocks)
		for id, v := range m {
			samples[id] = append(samples[id], v)
		}
		for _, id := range topIDs(m, 5) {
			top5Count[id]++
		}
	}
	ranges := []rangeRow{}
	for id, vals := range samples {
		full := append([]float64{}, vals...)
		for len(full) < draws {
			full = append(full, 0) // not exposed in that draw
		}
		sort.Float64s(full)
		ranges = append(ranges, rangeRow{EntityID: id, Entity: g.names[id], Base: baseMap[id], P5: round3(quantile(full, 0.05)), P50: round3(quantile(full, 0.5)),
			P95: round3(quantile(full, 0.95)), PExposed: round3(float64(len(vals)) / float64(draws)), PTop5: round3(float64(top5Count[id]) / float64(draws))})
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].P50 != ranges[j].P50 {
			return ranges[i].P50 > ranges[j].P50
		}
		return ranges[i].EntityID < ranges[j].EntityID
	})
	rangeByID := map[int64]rangeRow{}
	for _, r := range ranges {
		rangeByID[r.EntityID] = r
	}
	if len(ranges) > 25 {
		ranges = ranges[:25]
	}

	// Fragility of the top 10.
	frag := []fragRow{}
	counts := map[string]int{}
	exposed := exposedList(baseRes, spec.Shocks)
	for i, e := range exposed {
		if i == 10 {
			break
		}
		fr := fragRow{EntityID: e.EntityID, Entity: e.Entity, Base: e.Impact, WeakOnPath: e.WeakLinks, AssumedOnPath: e.Assumed, CriticalLinks: []string{}}
		for _, l := range e.Chain[1:] {
			fr.PathLinks++
			if l.NSources <= 1 && l.Status != "assumed" {
				fr.SingleSourceOn++
			}
			without := impactMap(propagate(g, spec.Shocks, base, map[string]bool{l.Key: true}), spec.Shocks)
			if without[e.EntityID] == 0 {
				fr.CriticalLinks = append(fr.CriticalLinks, fmt.Sprintf("%s —%s→ %s", l.From, l.Type, l.To))
			}
		}
		rr := rangeByID[e.EntityID]
		switch {
		case len(fr.CriticalLinks) > 0 && fr.SingleSourceOn > 0:
			fr.Classification, fr.ClassifiedWhy = "fragile", "exposure disappears if one link is removed, and that path rests on single-source links"
		case fr.AssumedOnPath && len(fr.CriticalLinks) > 0:
			fr.Classification, fr.ClassifiedWhy = "fragile", "exposure depends on an operator assumption with no alternative path"
		case rr.PExposed < 0.9 || (rr.P5 > 0 && rr.P95/rr.P5 > 3) || rr.P5 == 0 || rr.PTop5 < 0.5:
			fr.Classification, fr.ClassifiedWhy = "sensitive", fmt.Sprintf("under varied assumptions exposed in %.0f%% of draws, range %.2f–%.2f, in the top 5 in %.0f%%", rr.PExposed*100, rr.P5, rr.P95, rr.PTop5*100)
		default:
			fr.Classification, fr.ClassifiedWhy = "robust to these assumptions", "survives single-link removal and stays exposed across assumption draws; this is not validation against data"
		}
		counts[fr.Classification]++
		frag = append(frag, fr)
	}

	assumptions := []map[string]any{}
	for _, t := range ruleNames {
		r := base.Rules[t]
		assumptions = append(assumptions, map[string]any{"assumption": fmt.Sprintf("a shock crosses a %s link (%s) at factor %.2f", t, r.Direction, r.Factor),
			"source": "heuristic, not estimated from data", "used": typesInGraph[t]})
	}
	for _, s := range sortedKeys(base.StatusWeights) {
		assumptions = append(assumptions, map[string]any{"assumption": fmt.Sprintf("a %s link carries %.2f of its weight", s, base.StatusWeights[s]), "source": "heuristic, not estimated from data", "used": statusesInGraph[s]})
	}
	for _, b := range sortedKeys(base.BasisWeights) {
		assumptions = append(assumptions, map[string]any{"assumption": fmt.Sprintf("a %s link carries %.2f of its weight", orStr(b, "ungraded"), base.BasisWeights[b]), "source": "heuristic, not estimated from data", "used": basesInGraph[b] && (strongBasis(b) || base.IncludeWeak)})
	}
	assumptions = append(assumptions,
		map[string]any{"assumption": "a shock travels at most " + fmt.Sprint(base.MaxDepth) + " links", "source": "heuristic", "used": true},
		map[string]any{"assumption": "effects multiply along one best path and do not add across paths", "source": "modelling choice", "used": true},
		map[string]any{"assumption": "links transmit immediately and in full proportion; no buffers, substitution or timing", "source": "modelling choice", "used": true},
		map[string]any{"assumption": "co_occurrence and ungraded links do not transmit unless include_weak", "source": "evidence rule", "used": true},
	)
	return map[string]any{
		"kind":          "assumption and sensitivity report",
		"snapshot_hash": g.Hash,
		"draws":         draws, "seed": seed,
		"assumptions":           assumptions,
		"one_at_a_time":         rows,
		"ranges":                ranges,
		"ranges_note":           "Each factor and weight scaled independently by U(0.5, 1.5) per draw. Ranges show sensitivity to the assumptions, not statistical confidence intervals.",
		"fragility":             frag,
		"classification_counts": counts,
		"limitations":           scenarioLimitations(),
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runSensitivity computes and stores the report on the scenario.
func runSensitivity(db *sql.DB, pid string, id int64, draws int, seed int64) (map[string]any, error) {
	spec, hash, _, err := loadScenarioSpec(db, pid, id)
	if err != nil {
		return nil, err
	}
	g, err := loadWorldGraph(db, pid, spec.AsOf, spec.KnownAt)
	if err != nil {
		return nil, err
	}
	if seed == 0 {
		seed = 1
	}
	rep := sensitivityReport(g, spec, draws, seed)
	rep["scenario_id"] = id
	rep["graph_matches_run"] = g.Hash == hash
	rep["evidence_profile"] = edgeEvidenceProfile(g)
	if ht, err := latestHistoryTest(db, pid); err == nil && ht != nil {
		rep["historical_test"] = ht
	} else {
		rep["historical_test"] = map[string]any{"status": "not run", "note": "run scenario_history_test to compare modelled exposure with later market moves"}
	}
	rep["generated_at"] = ts(nowUTC())
	if _, err := db.Exec(`UPDATE intel_scenarios SET report = ? WHERE project_id = ? AND id = ?`, mustJSON(rep), pid, id); err != nil {
		return nil, err
	}
	return rep, nil
}

// edgeEvidenceProfile: how well-sourced the whole graph is.
func edgeEvidenceProfile(g *worldGraph) map[string]any {
	byBasis := map[string]int{}
	sourceHist := map[string]int{}
	for _, e := range g.Edges {
		byBasis[orStr(e.Causal, "ungraded")]++
		switch n := len(e.Sources); {
		case n == 0:
			sourceHist["0"]++
		case n == 1:
			sourceHist["1"]++
		case n <= 3:
			sourceHist["2-3"]++
		default:
			sourceHist["4+"]++
		}
	}
	return map[string]any{"links": len(g.Edges), "by_causal_basis": byBasis, "links_by_source_count": sourceHist}
}

func loadScenarioReport(db *sql.DB, pid string, id int64) (any, error) {
	var raw sql.NullString
	if err := db.QueryRow(`SELECT report FROM intel_scenarios WHERE project_id = ? AND id = ?`, pid, id).Scan(&raw); err != nil {
		return nil, fmt.Errorf("scenario %d not found", id)
	}
	if !raw.Valid {
		return nil, nil
	}
	var v any
	_ = json.Unmarshal([]byte(raw.String), &v)
	return v, nil
}
