package main

// Scenario v1: propagate explicit, operator-stated shocks through typed
// relationships valid at a chosen time. Output is hypothetical by
// construction: every exposure carries the chain of relationships (and
// their evidence status) that produced it. Links without a rule are
// reported as unsupported rather than guessed.

import (
	"database/sql"
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

// Evidence status discounts how far a shock travels over a link.
var statusWeight = map[string]float64{"observed": 1, "reported": 0.8, "inferred": 0.6, "assumed": 0.5}

type Shock struct {
	EntityID    int64   `json:"entity_id"`
	Entity      string  `json:"entity,omitempty"`
	Description string  `json:"description"`
	Severity    float64 `json:"severity"` // 0..1
}

type exposure struct {
	EntityID int64            `json:"entity_id"`
	Entity   string           `json:"entity"`
	Kind     string           `json:"kind"`
	Impact   float64          `json:"impact"`
	Depth    int              `json:"depth"`
	Chain    []map[string]any `json:"chain"`
	Assumed  bool             `json:"relies_on_assumed_links"`
}

type scenarioInput struct {
	Name        string
	Shocks      []Shock
	Rules       map[string]propagationRule
	AsOf        time.Time
	MaxDepth    int
	MinImpact   float64
	SkipAssumed bool
}

func runScenario(db *sql.DB, pid string, in scenarioInput) (map[string]any, error) {
	if len(in.Shocks) == 0 {
		return nil, errors.New("at least one shock is required")
	}
	rules := defaultPropagationRules()
	for k, v := range in.Rules {
		if v.Direction != "forward" && v.Direction != "reverse" && v.Direction != "both" {
			return nil, fmt.Errorf("rule %q: direction must be forward, reverse or both", k)
		}
		if v.Factor <= 0 || v.Factor > 1 {
			return nil, fmt.Errorf("rule %q: factor must be in (0,1]", k)
		}
		rules[strings.ToLower(k)] = v
	}
	if in.MaxDepth <= 0 || in.MaxDepth > 6 {
		in.MaxDepth = 3
	}
	if in.MinImpact <= 0 {
		in.MinImpact = 0.05
	}
	if in.AsOf.IsZero() {
		in.AsOf = nowUTC()
	}
	best := map[int64]*exposure{}
	unsupported := []map[string]any{}
	unruled := map[string]int{}
	type node struct {
		id      int64
		impact  float64
		depth   int
		chain   []map[string]any
		assumed bool
	}
	queue := []node{}
	for i := range in.Shocks {
		s := &in.Shocks[i]
		if s.EntityID == 0 && s.Entity != "" {
			e, err := findWorldEntity(db, pid, s.Entity, nil)
			if err != nil {
				return nil, err
			}
			if e == nil {
				return nil, fmt.Errorf("shock entity %q not found", s.Entity)
			}
			s.EntityID = e.ID
		}
		e, err := getWorldEntity(db, pid, s.EntityID)
		if err != nil || e == nil {
			return nil, fmt.Errorf("shock entity %d not found", s.EntityID)
		}
		s.Entity = e.Name
		if s.Severity <= 0 || s.Severity > 1 {
			s.Severity = 1
		}
		best[e.ID] = &exposure{EntityID: e.ID, Entity: e.Name, Kind: e.Kind, Impact: s.Severity, Chain: []map[string]any{{"shock": s.Description}}}
		queue = append(queue, node{id: e.ID, impact: s.Severity, chain: []map[string]any{{"shock": s.Description, "entity": e.Name}}})
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n.depth >= in.MaxDepth {
			continue
		}
		rels, err := relationshipsAt(db, pid, []int64{n.id}, &in.AsOf)
		if err != nil {
			return nil, err
		}
		crossed := 0
		for _, r := range rels {
			if in.SkipAssumed && r.Status == "assumed" {
				continue
			}
			rule, ok := rules[r.Type]
			if !ok {
				unruled[r.Type]++
				continue
			}
			var next int64
			var nextName string
			switch {
			case r.From == n.id && (rule.Direction == "forward" || rule.Direction == "both"):
				next, nextName = r.To, r.ToName
			case r.To == n.id && (rule.Direction == "reverse" || rule.Direction == "both"):
				next, nextName = r.From, r.FromName
			default:
				continue
			}
			w := statusWeight[r.Status]
			if r.Confidence != nil {
				w *= *r.Confidence
			}
			impact := n.impact * rule.Factor * w
			if impact < in.MinImpact {
				continue
			}
			crossed++
			link := map[string]any{"relationship_id": r.ID, "from": r.FromName, "type": r.Type, "to": r.ToName, "status": r.Status,
				"factor": rule.Factor, "evidence_ids": r.EvidenceIDs}
			chain := append(append([]map[string]any{}, n.chain...), link)
			assumed := n.assumed || r.Status == "assumed"
			if prev, seen := best[next]; seen && prev.Impact >= impact {
				continue
			}
			ent, _ := getWorldEntity(db, pid, next)
			kind := ""
			if ent != nil {
				kind = ent.Kind
			}
			best[next] = &exposure{EntityID: next, Entity: nextName, Kind: kind, Impact: round3(impact), Depth: n.depth + 1, Chain: chain, Assumed: assumed}
			queue = append(queue, node{id: next, impact: impact, depth: n.depth + 1, chain: chain, assumed: assumed})
		}
		if n.depth == 0 && crossed == 0 {
			unsupported = append(unsupported, map[string]any{"entity_id": n.id, "reason": "no relationship with a propagation rule leads away from this entity at as_of; add evidence-backed or assumed relationships to model its effects"})
		}
	}
	shocked := map[int64]bool{}
	for _, s := range in.Shocks {
		shocked[s.EntityID] = true
	}
	exposed := []exposure{}
	for id, e := range best {
		if !shocked[id] {
			exposed = append(exposed, *e)
		}
	}
	sort.Slice(exposed, func(i, j int) bool { return exposed[i].Impact > exposed[j].Impact })

	// Open questions touching exposed or shocked entities.
	open, err := listQuestions(db, pid, "open", 5000)
	if err != nil {
		return nil, err
	}
	questions := []map[string]any{}
	for _, q := range open {
		maxImpact, via := 0.0, ""
		for _, id := range q.EntityIDs {
			if e, ok := best[id]; ok && e.Impact > maxImpact {
				maxImpact, via = e.Impact, e.Entity
			}
		}
		if maxImpact > 0 {
			row := map[string]any{"question_id": q.ID, "question": q.Text, "deadline": ts(q.Deadline), "impact": round3(maxImpact), "via_entity": via}
			if f, _ := latestForecast(db, q.ID); f != nil && f.Probability != nil {
				row["current_forecast"] = *f.Probability
			}
			questions = append(questions, row)
		}
	}
	sort.Slice(questions, func(i, j int) bool { return questions[i]["impact"].(float64) > questions[j]["impact"].(float64) })
	unruledList := []map[string]any{}
	for t, n := range unruled {
		unruledList = append(unruledList, map[string]any{"type": t, "links": n, "note": "no propagation rule; pass rules to include"})
	}
	result := map[string]any{
		"hypothetical":       true,
		"note":               "Conditional on the stated shocks and propagation rules. Impacts are relative exposure scores (0-1), not forecasts or probabilities.",
		"as_of":              ts(in.AsOf),
		"shocks":             in.Shocks,
		"exposed_entities":   exposed,
		"exposed_questions":  questions,
		"unsupported_shocks": unsupported,
		"unruled_links":      unruledList,
		"parameters":         map[string]any{"max_depth": in.MaxDepth, "min_impact": in.MinImpact, "skip_assumed": in.SkipAssumed},
	}
	name := fallback(in.Name, "scenario "+ts(nowUTC()))
	var id int64
	if err := db.QueryRow(`INSERT INTO intel_scenarios (project_id, name, base_as_of, shocks, rules, result, created_at) VALUES (?,?,?,?,?,?,?) RETURNING id`,
		pid, name, ts(in.AsOf), mustJSON(in.Shocks), mustJSON(rules), mustJSON(result), ts(nowUTC())).Scan(&id); err != nil {
		return nil, err
	}
	result["id"], result["name"] = id, name
	return result, nil
}

func listScenarios(db *sql.DB, pid string, limit int) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT id, name, base_as_of, shocks, result, created_at FROM intel_scenarios WHERE project_id = ? ORDER BY id DESC LIMIT ?`, pid, clampLimit(limit, 20, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, shocks, result string
		var asOf, created flexibleTime
		if err := rows.Scan(&id, &name, &asOf, &shocks, &result, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "as_of": timeString(asOf.Time()), "created_at": timeString(created.Time()),
			"shocks": rawJSON(shocks), "result": rawJSON(result)})
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
