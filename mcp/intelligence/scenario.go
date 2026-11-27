package main

// Scenarios: propagate explicit, operator-stated shocks through typed
// relationships. Output is conditional by construction ("if this shock
// happened and these links transmitted it as assumed"), never a forecast:
// impacts are relative exposure scores, every exposure carries the chain
// of links that produced it with what each link's sources support, and
// links without a rule or without causal support are reported, not used.
//
// Reproducibility: a run sees only relationships recorded by known_at and
// valid at as_of. The exact graph is hashed; scenario_replay re-runs the
// stored spec and checks the hash and results.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// propagationRule: how a shock crosses a relationship. Direction "forward"
// means a shock on the relationship's from-entity exposes its to-entity
// (A supplies B: A disrupted → B exposed); "reverse" means a shock on the
// to-entity exposes the from-entity (A depends_on B: B disrupted → A).
type propagationRule struct {
	Factor    float64 `json:"factor"`
	Direction string  `json:"direction"` // forward | reverse | both
}

// defaultPropagationRules are heuristics, not estimates: no data fixed
// these factors. Reports say so and test how much results depend on them.
func defaultPropagationRules() map[string]propagationRule {
	return map[string]propagationRule{
		"supplies":   {0.7, "forward"},
		"produces":   {0.7, "forward"},
		"regulates":  {0.5, "forward"},
		"affects":    {0.6, "forward"},
		"owns":       {0.5, "forward"},
		"depends_on": {0.8, "reverse"},
		"uses":       {0.6, "reverse"},
		"located_in": {0.6, "reverse"},
		"part_of":    {0.7, "reverse"},
		"operates":   {0.6, "reverse"},
	}
}

// Status weight: how reliable the link's source is.
func defaultStatusWeights() map[string]float64 {
	return map[string]float64{"observed": 1, "reported": 0.8, "inferred": 0.6, "assumed": 0.5}
}

// Causal-basis weight: how much the sources say about transmission.
func defaultBasisWeights() map[string]float64 {
	return map[string]float64{"mechanism": 1, "asserted": 0.7, "assumed": 1, "co_occurrence": 0.3, "": 0.3}
}

const (
	cbMechanism    = "mechanism"
	cbAsserted     = "asserted"
	cbCooccurrence = "co_occurrence"
	cbUnsupported  = "unsupported"
	cbAssumed      = "assumed"
)

var causalBases = map[string]bool{cbMechanism: true, cbAsserted: true, cbCooccurrence: true, cbUnsupported: true, cbAssumed: true}

// strongBasis: propagates by default. Weak bases (co_occurrence and
// ungraded) need include_weak; unsupported never propagates.
func strongBasis(b string) bool { return b == cbMechanism || b == cbAsserted || b == cbAssumed }

var causalRank = map[string]int{cbMechanism: 5, cbAsserted: 4, cbAssumed: 3, cbCooccurrence: 2, "": 1, cbUnsupported: 0}
var statusRank = map[string]int{"observed": 4, "reported": 3, "inferred": 2, "assumed": 1}

type Shock struct {
	EntityID    int64   `json:"entity_id"`
	Entity      string  `json:"entity,omitempty"`
	Description string  `json:"description"`
	Severity    float64 `json:"severity"` // 0..1
}

// ─── Graph ─────────────────────────────────────────────────────────

// gEdge merges every recorded relationship with the same (from, type, to):
// repeated extractions of one headline pattern are one link with several
// sources, not several links.
type gEdge struct {
	Key        string   `json:"key"`
	IDs        []int64  `json:"relationship_ids"`
	From       int64    `json:"from_entity_id"`
	To         int64    `json:"to_entity_id"`
	FromName   string   `json:"from"`
	ToName     string   `json:"to"`
	Type       string   `json:"type"`
	Status     string   `json:"status"`
	Causal     string   `json:"causal_basis"`
	Confidence *float64 `json:"confidence,omitempty"`
	Sources    []int64  `json:"evidence_ids"`
}

type worldGraph struct {
	AsOf, KnownAt time.Time
	Edges         []*gEdge
	out, in       map[int64][]*gEdge
	names, kinds  map[int64]string
	Hash          string
}

func loadWorldGraph(db *sql.DB, pid string, asOf, knownAt time.Time) (*worldGraph, error) {
	rows, err := db.Query(`SELECT r.id, r.from_entity_id, f.canonical, f.kind, r.rel_type, r.to_entity_id, t.canonical, t.kind, r.status, r.confidence,
		r.valid_from, r.valid_to, r.evidence_ids, r.basis, r.created_at, r.retracted_at, r.causal_basis
		FROM intel_relationships r JOIN entities f ON f.id = r.from_entity_id JOIN entities t ON t.id = r.to_entity_id
		WHERE r.project_id = ? ORDER BY r.id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	g := &worldGraph{AsOf: asOf, KnownAt: knownAt, out: map[int64][]*gEdge{}, in: map[int64][]*gEdge{}, names: map[int64]string{}, kinds: map[int64]string{}}
	byKey := map[string]*gEdge{}
	for rows.Next() {
		var id, from, to int64
		var fname, fkind, typ, tname, tkind, status, ev, basis, causal string
		var conf sql.NullFloat64
		var vf, vt, created, retracted flexibleTime
		if err := rows.Scan(&id, &from, &fname, &fkind, &typ, &to, &tname, &tkind, &status, &conf, &vf, &vt, &ev, &basis, &created, &retracted, &causal); err != nil {
			return nil, err
		}
		if basis == basisSynthetic {
			continue // sample data never feeds a scenario
		}
		if c := created.Time(); c != nil && c.After(knownAt) {
			continue // not yet recorded at known_at
		}
		if r := retracted.Time(); r != nil && !r.After(knownAt) {
			continue
		}
		if f := vf.Time(); f != nil && f.After(asOf) {
			continue
		}
		if t := vt.Time(); t != nil && !t.After(asOf) {
			continue
		}
		g.names[from], g.kinds[from], g.names[to], g.kinds[to] = fname, fkind, tname, tkind
		key := fmt.Sprintf("%d|%s|%d", from, typ, to)
		e := byKey[key]
		if e == nil {
			e = &gEdge{Key: key, From: from, To: to, FromName: fname, ToName: tname, Type: typ, Status: status, Causal: causal, Sources: []int64{}}
			byKey[key] = e
		}
		e.IDs = append(e.IDs, id)
		if statusRank[status] > statusRank[e.Status] {
			e.Status = status
		}
		if causalRank[causal] > causalRank[e.Causal] {
			e.Causal = causal
		}
		if conf.Valid && (e.Confidence == nil || conf.Float64 > *e.Confidence) {
			v := conf.Float64
			e.Confidence = &v
		}
		var ids []int64
		_ = json.Unmarshal([]byte(ev), &ids)
		for _, s := range ids {
			if !containsID(e.Sources, s) {
				e.Sources = append(e.Sources, s)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, e := range byKey {
		sort.Slice(e.Sources, func(i, j int) bool { return e.Sources[i] < e.Sources[j] })
		g.Edges = append(g.Edges, e)
	}
	sort.Slice(g.Edges, func(i, j int) bool { return g.Edges[i].Key < g.Edges[j].Key })
	for _, e := range g.Edges {
		g.out[e.From] = append(g.out[e.From], e)
		g.in[e.To] = append(g.in[e.To], e)
	}
	b, _ := json.Marshal(g.Edges)
	sum := sha256.Sum256(b)
	g.Hash = hex.EncodeToString(sum[:])
	return g, nil
}

// ─── Propagation ───────────────────────────────────────────────────

type propParams struct {
	Rules            map[string]propagationRule `json:"rules"`
	StatusWeights    map[string]float64         `json:"status_weights"`
	BasisWeights     map[string]float64         `json:"basis_weights"`
	MaxDepth         int                        `json:"max_depth"`
	MinImpact        float64                    `json:"min_impact"`
	SkipAssumed      bool                       `json:"skip_assumed"`
	IncludeWeak      bool                       `json:"include_weak"`
	IgnoreConfidence bool                       `json:"ignore_confidence"`
}

func defaultPropParams() propParams {
	return propParams{Rules: defaultPropagationRules(), StatusWeights: defaultStatusWeights(), BasisWeights: defaultBasisWeights(), MaxDepth: 3, MinImpact: 0.05}
}

func (p propParams) clone() propParams {
	c := p
	c.Rules, c.StatusWeights, c.BasisWeights = map[string]propagationRule{}, map[string]float64{}, map[string]float64{}
	for k, v := range p.Rules {
		c.Rules[k] = v
	}
	for k, v := range p.StatusWeights {
		c.StatusWeights[k] = v
	}
	for k, v := range p.BasisWeights {
		c.BasisWeights[k] = v
	}
	return c
}

type chainLink struct {
	Shock       string  `json:"shock,omitempty"`
	Entity      string  `json:"entity,omitempty"`
	Key         string  `json:"key,omitempty"`
	From        string  `json:"from,omitempty"`
	Type        string  `json:"type,omitempty"`
	To          string  `json:"to,omitempty"`
	Status      string  `json:"status,omitempty"`
	CausalBasis string  `json:"causal_basis,omitempty"`
	NSources    int     `json:"n_sources,omitempty"`
	Factor      float64 `json:"factor,omitempty"`
	Weight      float64 `json:"weight,omitempty"`
	EvidenceIDs []int64 `json:"evidence_ids,omitempty"`
	RelIDs      []int64 `json:"relationship_ids,omitempty"`
}

type exposure struct {
	EntityID     int64       `json:"entity_id"`
	Entity       string      `json:"entity"`
	Kind         string      `json:"kind"`
	Impact       float64     `json:"impact"`
	Depth        int         `json:"depth"`
	Chain        []chainLink `json:"chain"`
	Assumed      bool        `json:"relies_on_assumed_links"`
	WeakLinks    bool        `json:"relies_on_weak_links"`
	SingleSource bool        `json:"relies_on_single_source_links"`
}

type propResult struct {
	best         map[int64]*exposure
	unruled      map[string]int
	weakSkipped  map[string]bool
	unsupported  map[string]bool
	deadEndShock []int64
}

// edgeWeight is how much of a shock crosses e, or ok=false when e may not
// carry it. reason says why not.
func (p propParams) edgeWeight(e *gEdge) (w float64, ok bool, reason string) {
	if e.Causal == cbUnsupported {
		return 0, false, "unsupported"
	}
	if p.SkipAssumed && e.Status == "assumed" {
		return 0, false, "assumed"
	}
	if !strongBasis(e.Causal) && !p.IncludeWeak {
		return 0, false, "weak"
	}
	w = p.StatusWeights[e.Status] * p.BasisWeights[e.Causal]
	if e.Confidence != nil && !p.IgnoreConfidence {
		w *= *e.Confidence
	}
	return w, true, ""
}

// propagate is a deterministic breadth-first spread from the shocked
// entities. removed lists edge keys to leave out (fragility tests).
func propagate(g *worldGraph, shocks []Shock, p propParams, removed map[string]bool) propResult {
	res := propResult{best: map[int64]*exposure{}, unruled: map[string]int{}, weakSkipped: map[string]bool{}, unsupported: map[string]bool{}}
	type node struct {
		id     int64
		impact float64
		depth  int
		chain  []chainLink
		flags  [3]bool // assumed, weak, single source
	}
	queue := []node{}
	for _, s := range shocks {
		sev := s.Severity
		res.best[s.EntityID] = &exposure{EntityID: s.EntityID, Entity: s.Entity, Kind: g.kinds[s.EntityID], Impact: sev,
			Chain: []chainLink{{Shock: s.Description, Entity: s.Entity}}}
		queue = append(queue, node{id: s.EntityID, impact: sev, chain: []chainLink{{Shock: s.Description, Entity: s.Entity}}})
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n.depth >= p.MaxDepth {
			continue
		}
		crossed := 0
		visit := func(e *gEdge, next int64, nextName string) {
			if removed[e.Key] {
				return
			}
			w, ok, why := p.edgeWeight(e)
			if !ok {
				switch why {
				case "weak":
					res.weakSkipped[e.Key] = true
				case "unsupported":
					res.unsupported[e.Key] = true
				}
				return
			}
			rule := p.Rules[e.Type]
			impact := n.impact * rule.Factor * w
			if impact < p.MinImpact {
				return
			}
			crossed++
			link := chainLink{Key: e.Key, From: e.FromName, Type: e.Type, To: e.ToName, Status: e.Status, CausalBasis: e.Causal, NSources: len(e.Sources),
				Factor: rule.Factor, Weight: round3(w), EvidenceIDs: e.Sources, RelIDs: e.IDs}
			chain := append(append([]chainLink{}, n.chain...), link)
			flags := n.flags
			flags[0] = flags[0] || e.Status == "assumed"
			flags[1] = flags[1] || !strongBasis(e.Causal)
			flags[2] = flags[2] || (len(e.Sources) <= 1 && e.Status != "assumed")
			if prev, seen := res.best[next]; seen && prev.Impact >= impact {
				return
			}
			res.best[next] = &exposure{EntityID: next, Entity: nextName, Kind: g.kinds[next], Impact: round3(impact), Depth: n.depth + 1, Chain: chain,
				Assumed: flags[0], WeakLinks: flags[1], SingleSource: flags[2]}
			queue = append(queue, node{id: next, impact: impact, depth: n.depth + 1, chain: chain, flags: flags})
		}
		for _, e := range g.out[n.id] {
			rule, ok := p.Rules[e.Type]
			if !ok {
				res.unruled[e.Type]++
				continue
			}
			if rule.Direction == "forward" || rule.Direction == "both" {
				visit(e, e.To, e.ToName)
			}
		}
		for _, e := range g.in[n.id] {
			rule, ok := p.Rules[e.Type]
			if !ok {
				continue // counted on the outgoing side
			}
			if rule.Direction == "reverse" || rule.Direction == "both" {
				visit(e, e.From, e.FromName)
			}
		}
		if n.depth == 0 && crossed == 0 {
			res.deadEndShock = append(res.deadEndShock, n.id)
		}
	}
	return res
}

// exposedList: exposures other than the shocked entities, highest first
// (ties by entity id, so output order is stable).
func exposedList(res propResult, shocks []Shock) []exposure {
	shocked := map[int64]bool{}
	for _, s := range shocks {
		shocked[s.EntityID] = true
	}
	out := []exposure{}
	for id, e := range res.best {
		if !shocked[id] {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Impact != out[j].Impact {
			return out[i].Impact > out[j].Impact
		}
		return out[i].EntityID < out[j].EntityID
	})
	return out
}

// ─── Runs ──────────────────────────────────────────────────────────

// scenarioSpec is everything needed to reproduce a run.
type scenarioSpec struct {
	Name       string                     `json:"name"`
	Shocks     []Shock                    `json:"shocks"`
	RuleEdits  map[string]propagationRule `json:"rule_overrides,omitempty"`
	AsOf       time.Time                  `json:"as_of"`
	KnownAt    time.Time                  `json:"known_at"`
	Params     propParams                 `json:"params"`
	CodeMethod string                     `json:"method"`
}

const scenarioMethodVersion = "scenario-v2"

type scenarioInput struct {
	Name             string
	Shocks           []Shock
	Rules            map[string]propagationRule
	AsOf             time.Time
	KnownAt          time.Time
	MaxDepth         int
	MinImpact        float64
	SkipAssumed      bool
	IncludeWeak      bool
	IgnoreConfidence bool
}

func buildScenarioSpec(db *sql.DB, pid string, in scenarioInput, now time.Time) (scenarioSpec, error) {
	if len(in.Shocks) == 0 {
		return scenarioSpec{}, errors.New("at least one shock is required")
	}
	p := defaultPropParams()
	for k, v := range in.Rules {
		if v.Direction != "forward" && v.Direction != "reverse" && v.Direction != "both" {
			return scenarioSpec{}, fmt.Errorf("rule %q: direction must be forward, reverse or both", k)
		}
		if v.Factor <= 0 || v.Factor > 1 {
			return scenarioSpec{}, fmt.Errorf("rule %q: factor must be in (0,1]", k)
		}
		p.Rules[strings.ToLower(k)] = v
	}
	if in.MaxDepth > 0 && in.MaxDepth <= 6 {
		p.MaxDepth = in.MaxDepth
	}
	if in.MinImpact > 0 {
		p.MinImpact = in.MinImpact
	}
	p.SkipAssumed, p.IncludeWeak, p.IgnoreConfidence = in.SkipAssumed, in.IncludeWeak, in.IgnoreConfidence
	spec := scenarioSpec{Name: in.Name, RuleEdits: in.Rules, AsOf: in.AsOf, KnownAt: in.KnownAt, Params: p, CodeMethod: scenarioMethodVersion}
	if spec.AsOf.IsZero() {
		spec.AsOf = now
	}
	if spec.KnownAt.IsZero() {
		// A past as_of sees only what was recorded by then: no hindsight.
		spec.KnownAt = now
		if spec.AsOf.Before(now) {
			spec.KnownAt = spec.AsOf
		}
	}
	if spec.KnownAt.After(now) {
		spec.KnownAt = now
	}
	spec.AsOf, spec.KnownAt = spec.AsOf.UTC().Truncate(time.Second), spec.KnownAt.UTC().Truncate(time.Second)
	for _, s := range in.Shocks {
		if s.EntityID == 0 && s.Entity != "" {
			e, err := findWorldEntity(db, pid, s.Entity, nil)
			if err != nil {
				return spec, err
			}
			if e == nil {
				return spec, fmt.Errorf("shock entity %q not found", s.Entity)
			}
			s.EntityID = e.ID
		}
		e, err := getWorldEntity(db, pid, s.EntityID)
		if err != nil || e == nil {
			return spec, fmt.Errorf("shock entity %d not found", s.EntityID)
		}
		s.Entity = e.Name
		if s.Severity <= 0 || s.Severity > 1 {
			s.Severity = 1
		}
		spec.Shocks = append(spec.Shocks, s)
	}
	return spec, nil
}

// computeScenario runs a spec against the graph known at its cutoff.
func computeScenario(db *sql.DB, pid string, spec scenarioSpec) (map[string]any, *worldGraph, error) {
	g, err := loadWorldGraph(db, pid, spec.AsOf, spec.KnownAt)
	if err != nil {
		return nil, nil, err
	}
	res := propagate(g, spec.Shocks, spec.Params, nil)
	exposed := exposedList(res, spec.Shocks)

	// What the links behind the exposures rest on.
	used := map[string]chainLink{}
	for _, e := range exposed {
		for _, l := range e.Chain[1:] {
			used[l.Key] = l
		}
	}
	byBasis, byStatus := map[string]int{}, map[string]int{}
	single := 0
	for _, l := range used {
		byBasis[orStr(l.CausalBasis, "ungraded")]++
		byStatus[l.Status]++
		if l.NSources <= 1 && l.Status != "assumed" {
			single++
		}
	}
	ungradedInGraph := 0
	for _, e := range g.Edges {
		if e.Causal == "" {
			ungradedInGraph++
		}
	}
	unsupportedShocks := []map[string]any{}
	for _, id := range res.deadEndShock {
		unsupportedShocks = append(unsupportedShocks, map[string]any{"entity_id": id, "entity": g.names[id],
			"reason": "no link that may carry a shock leads away from this entity at as_of; weak, unsupported or unruled links were not used"})
	}
	unruled := []map[string]any{}
	for t, n := range res.unruled {
		unruled = append(unruled, map[string]any{"type": t, "links": n, "note": "no propagation rule; pass rules to include"})
	}
	sort.Slice(unruled, func(i, j int) bool { return unruled[i]["type"].(string) < unruled[j]["type"].(string) })

	// Open questions touching exposed or shocked entities. Forecasts are
	// shown unchanged: a scenario computes no conditional probability.
	open, err := listQuestions(db, pid, "open", 5000)
	if err != nil {
		return nil, nil, err
	}
	questions := []map[string]any{}
	for _, q := range open {
		maxImpact, via := 0.0, ""
		for _, id := range q.EntityIDs {
			if e, ok := res.best[id]; ok && e.Impact > maxImpact {
				maxImpact, via = e.Impact, e.Entity
			}
		}
		if maxImpact > 0 {
			row := map[string]any{"question_id": q.ID, "question": q.Text, "deadline": ts(q.Deadline), "exposure": round3(maxImpact), "via_entity": via}
			if f, _ := latestForecast(db, q.ID); f != nil && f.Probability != nil {
				row["current_forecast_unchanged"] = *f.Probability
			}
			questions = append(questions, row)
		}
	}
	sort.Slice(questions, func(i, j int) bool {
		if questions[i]["exposure"].(float64) != questions[j]["exposure"].(float64) {
			return questions[i]["exposure"].(float64) > questions[j]["exposure"].(float64)
		}
		return questions[i]["question_id"].(int64) < questions[j]["question_id"].(int64)
	})
	result := map[string]any{
		"hypothetical":       true,
		"kind":               "conditional scenario",
		"note":               "Conditional on the stated shocks and on the assumption that each link transmits them as its rule says. Impacts are relative exposure scores (0-1), not probabilities, forecasts or measured effects.",
		"causal_note":        "Links come from what sources report, not from causal identification. mechanism links have a source describing how the effect passes; asserted links have a source stating it without how; co_occurrence links only show entities mentioned together and are not used unless include_weak is set. No link here has been validated as causal.",
		"as_of":              ts(spec.AsOf),
		"known_at":           ts(spec.KnownAt),
		"snapshot_hash":      g.Hash,
		"shocks":             spec.Shocks,
		"exposed_entities":   exposed,
		"exposed_questions":  questions,
		"questions_note":     "Scenarios never change forecasts and compute no conditional probabilities; exposure is not a change in probability.",
		"unsupported_shocks": unsupportedShocks,
		"unruled_links":      unruled,
		"evidence_profile": map[string]any{
			"links_used": len(used), "by_causal_basis": byBasis, "by_status": byStatus, "single_source_links_used": single,
			"links_in_graph": len(g.Edges), "ungraded_links_in_graph": ungradedInGraph,
			"weak_links_not_used": len(res.weakSkipped), "unsupported_links_not_used": len(res.unsupported),
		},
		"parameters":  spec.Params,
		"limitations": scenarioLimitations(),
	}
	return result, g, nil
}

func scenarioLimitations() []string {
	return []string{
		"Propagation factors and weights are heuristics chosen by hand, not estimated from data; see the sensitivity report for how much results depend on them.",
		"Links are extracted from headlines by a language model; a headline can misstate or oversimplify a relation.",
		"Causal basis grades what a source says, not whether the relation is causal. Correlation and co-mention are not causation.",
		"Exposure accumulates multiplicatively along one best path; it ignores substitution, buffers, feedback and timing.",
		"Entities and links the sources did not mention are missing; absence of exposure is not evidence of no effect.",
	}
}

func runScenario(db *sql.DB, pid string, in scenarioInput) (map[string]any, error) {
	now := nowUTC()
	spec, err := buildScenarioSpec(db, pid, in, now)
	if err != nil {
		return nil, err
	}
	result, g, err := computeScenario(db, pid, spec)
	if err != nil {
		return nil, err
	}
	spec.Name = fallback(spec.Name, "scenario "+ts(now))
	var id int64
	if err := db.QueryRow(`INSERT INTO intel_scenarios (project_id, name, base_as_of, shocks, rules, result, created_at, known_at, snapshot_hash, spec) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		pid, spec.Name, ts(spec.AsOf), mustJSON(spec.Shocks), mustJSON(spec.Params.Rules), mustJSON(result), ts(now), ts(spec.KnownAt), g.Hash, mustJSON(spec)).Scan(&id); err != nil {
		return nil, err
	}
	result["id"], result["name"] = id, spec.Name
	return result, nil
}

func loadScenarioSpec(db *sql.DB, pid string, id int64) (scenarioSpec, string, map[string]any, error) {
	var specRaw, hash, resRaw string
	err := db.QueryRow(`SELECT spec, snapshot_hash, result FROM intel_scenarios WHERE project_id = ? AND id = ?`, pid, id).Scan(&specRaw, &hash, &resRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return scenarioSpec{}, "", nil, fmt.Errorf("scenario %d not found", id)
	}
	if err != nil {
		return scenarioSpec{}, "", nil, err
	}
	var spec scenarioSpec
	if err := json.Unmarshal([]byte(specRaw), &spec); err != nil || len(spec.Shocks) == 0 {
		return spec, "", nil, fmt.Errorf("scenario %d predates reproducible specs (0.7.0); run it again", id)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(resRaw), &res)
	return spec, hash, res, nil
}

// replayScenario re-runs a stored spec and checks it reproduces.
func replayScenario(db *sql.DB, pid string, id int64) (map[string]any, error) {
	spec, hash, stored, err := loadScenarioSpec(db, pid, id)
	if err != nil {
		return nil, err
	}
	result, g, err := computeScenario(db, pid, spec)
	if err != nil {
		return nil, err
	}
	sig := func(v any) string {
		b, _ := json.Marshal(v)
		var list []struct {
			EntityID int64   `json:"entity_id"`
			Impact   float64 `json:"impact"`
		}
		_ = json.Unmarshal(b, &list)
		parts := []string{}
		for _, e := range list {
			parts = append(parts, fmt.Sprintf("%d:%.3f", e.EntityID, e.Impact))
		}
		return strings.Join(parts, ",")
	}
	sameGraph := g.Hash == hash
	sameResult := sig(stored["exposed_entities"]) == sig(result["exposed_entities"])
	out := map[string]any{"scenario_id": id, "known_at": ts(spec.KnownAt), "as_of": ts(spec.AsOf),
		"stored_snapshot_hash": hash, "replayed_snapshot_hash": g.Hash, "same_graph": sameGraph, "same_exposures": sameResult,
		"reproduced": sameGraph && sameResult}
	if !sameGraph {
		out["note"] = "The graph known at known_at changed since the run: links recorded at that time were regraded, or relationship rows were edited. The replay uses the graph as it reads now."
	}
	return out, nil
}

func listScenarios(db *sql.DB, pid string, limit int) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT id, name, base_as_of, shocks, result, created_at, COALESCE(known_at,''), snapshot_hash, report IS NOT NULL FROM intel_scenarios
		WHERE project_id = ? ORDER BY id DESC LIMIT ?`, pid, clampLimit(limit, 20, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, shocks, result, knownAt, hash string
		var asOf, created flexibleTime
		var hasReport bool
		if err := rows.Scan(&id, &name, &asOf, &shocks, &result, &created, &knownAt, &hash, &hasReport); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "as_of": timeString(asOf.Time()), "known_at": knownAt, "snapshot_hash": hash, "has_report": hasReport,
			"created_at": timeString(created.Time()), "shocks": rawJSON(shocks), "result": rawJSON(result)})
	}
	return out, rows.Err()
}

func rawJSON(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}
