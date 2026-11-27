package main

// Historical shock test. Past news events (an entity named in a claim at
// time t) are replayed as shocks through the graph as it was known at t.
// For prediction-market questions linked to entities at t, compare how far
// their prices moved in the window after t for questions the model calls
// exposed versus questions it does not. A placebo window before t checks
// whether "exposed" questions simply move more anyway (busy topics).
//
// This measures association between modelled exposure and later price
// moves. It cannot establish that the links transmitted anything: events
// cluster, news reports moves that already happened, and busy entities
// carry busy questions.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

type historyParams struct {
	WindowHours   int     `json:"window_hours"`
	SinceDays     int     `json:"since_days"`
	MaxEvents     int     `json:"max_events"`
	IncludeWeak   bool    `json:"include_weak"`
	MoveThreshold float64 `json:"move_threshold"`
	Seed          int64   `json:"seed"`
}

type histObs struct {
	Event    int
	Group    string // direct | exposed | other
	Post     float64
	Pre      float64
	Question int64
}

type pricePoint struct {
	t time.Time
	p float64
}

func priceAtMem(series []pricePoint, t time.Time) (float64, bool) {
	i := sort.Search(len(series), func(i int) bool { return series[i].t.After(t) })
	if i == 0 {
		return 0, false
	}
	return series[i-1].p, true
}

func runHistoryTest(db *sql.DB, pid string, hp historyParams, now time.Time) (map[string]any, error) {
	if hp.WindowHours <= 0 {
		hp.WindowHours = 24
	}
	if hp.WindowHours > 24*14 {
		hp.WindowHours = 24 * 14
	}
	if hp.SinceDays <= 0 {
		hp.SinceDays = 14
	}
	if hp.MaxEvents <= 0 {
		hp.MaxEvents = 40
	}
	if hp.MaxEvents > 200 {
		hp.MaxEvents = 200
	}
	if hp.MoveThreshold <= 0 {
		hp.MoveThreshold = 0.05
	}
	if hp.Seed == 0 {
		hp.Seed = 1
	}
	window := time.Duration(hp.WindowHours) * time.Hour
	since := now.AddDate(0, 0, -hp.SinceDays)
	lastEvent := now.Add(-window)

	// Market questions and their price series.
	series := map[int64][]pricePoint{}
	deadlines := map[int64]time.Time{}
	{
		rows, err := db.Query(`SELECT q.id, q.deadline, p.observed_at, p.price FROM intel_questions q JOIN intel_question_prices p ON p.question_id = q.id
			WHERE q.project_id = ? AND q.origin = 'market' ORDER BY q.id, p.observed_at, p.id`, pid)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var qid int64
			var dl, at flexibleTime
			var price float64
			if err := rows.Scan(&qid, &dl, &at, &price); err != nil {
				rows.Close()
				return nil, err
			}
			if t := at.Time(); t != nil {
				series[qid] = append(series[qid], pricePoint{*t, price})
			}
			if d := dl.Time(); d != nil {
				deadlines[qid] = *d
			}
		}
		rows.Close()
	}
	for _, s := range series {
		sort.SliceStable(s, func(i, j int) bool { return s[i].t.Before(s[j].t) })
	}

	// Point-in-time question ↔ entity links: entities of claims on evidence
	// linked to the question, known once both the claim and the link exist.
	type qlink struct {
		q, e int64
		at   time.Time
	}
	links := []qlink{}
	{
		rows, err := db.Query(`SELECT qe.question_id, c.subject_entity_id, c.object_entity_id, c.created_at, qe.linked_at
			FROM intel_question_evidence qe JOIN intel_claims c ON c.evidence_id = qe.evidence_id AND c.project_id = ?
			JOIN intel_questions q ON q.id = qe.question_id AND q.project_id = ? AND q.origin = 'market'
			WHERE qe.relevance >= 0.5`, pid, pid)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var qid int64
			var subj, obj sql.NullInt64
			var cAt, lAt flexibleTime
			if err := rows.Scan(&qid, &subj, &obj, &cAt, &lAt); err != nil {
				rows.Close()
				return nil, err
			}
			at := time.Time{}
			for _, t := range []*time.Time{cAt.Time(), lAt.Time()} {
				if t != nil && t.After(at) {
					at = *t
				}
			}
			for _, e := range []sql.NullInt64{subj, obj} {
				if e.Valid {
					links = append(links, qlink{qid, e.Int64, at})
				}
			}
		}
		rows.Close()
	}

	// Events: first claim naming an entity on each day, from non-synthetic
	// evidence, early enough that the post window has passed.
	type event struct {
		Entity int64     `json:"entity_id"`
		Name   string    `json:"entity"`
		At     time.Time `json:"at"`
	}
	events := []event{}
	{
		rows, err := db.Query(`SELECT c.subject_entity_id, e.canonical, c.created_at FROM intel_claims c JOIN entities e ON e.id = c.subject_entity_id
			JOIN intelligence_evidence ev ON ev.id = c.evidence_id WHERE c.project_id = ? AND ev.basis <> ? ORDER BY c.id`, pid, basisSynthetic)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for rows.Next() {
			var id int64
			var name string
			var at flexibleTime
			if err := rows.Scan(&id, &name, &at); err != nil {
				rows.Close()
				return nil, err
			}
			t := at.Time()
			if t == nil || t.Before(since) || t.After(lastEvent) {
				continue
			}
			k := fmt.Sprintf("%d|%s", id, t.UTC().Format("2006-01-02"))
			if seen[k] {
				continue
			}
			seen[k] = true
			events = append(events, event{id, name, t.UTC()})
		}
		rows.Close()
	}
	candidates := len(events)
	rng := rand.New(rand.NewSource(hp.Seed))
	rng.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
	if len(events) > hp.MaxEvents {
		events = events[:hp.MaxEvents]
	}
	sort.Slice(events, func(i, j int) bool {
		if !events[i].At.Equal(events[j].At) {
			return events[i].At.Before(events[j].At)
		}
		return events[i].Entity < events[j].Entity
	})

	params := defaultPropParams()
	params.IncludeWeak = hp.IncludeWeak
	obs := []histObs{}
	eventRows := []map[string]any{}
	for i, ev := range events {
		g, err := loadWorldGraph(db, pid, ev.At, ev.At)
		if err != nil {
			return nil, err
		}
		res := propagate(g, []Shock{{EntityID: ev.Entity, Entity: ev.Name, Severity: 1}}, params, nil)
		exposed := map[int64]bool{}
		for id := range res.best {
			if id != ev.Entity {
				exposed[id] = true
			}
		}
		qEnts := map[int64]map[int64]bool{}
		for _, l := range links {
			if !l.at.After(ev.At) {
				if qEnts[l.q] == nil {
					qEnts[l.q] = map[int64]bool{}
				}
				qEnts[l.q][l.e] = true
			}
		}
		n := map[string]int{}
		for qid, ents := range qEnts {
			if d, ok := deadlines[qid]; ok && !d.After(ev.At.Add(window)) {
				continue // closed before the window ends
			}
			s := series[qid]
			pBefore, ok1 := priceAtMem(s, ev.At.Add(-window))
			pAt, ok2 := priceAtMem(s, ev.At)
			pAfter, ok3 := priceAtMem(s, ev.At.Add(window))
			if !ok1 || !ok2 || !ok3 {
				continue
			}
			group := "other"
			if ents[ev.Entity] {
				group = "direct"
			} else {
				for e := range ents {
					if exposed[e] {
						group = "exposed"
						break
					}
				}
			}
			obs = append(obs, histObs{Event: i, Group: group, Post: math.Abs(pAfter - pAt), Pre: math.Abs(pAt - pBefore), Question: qid})
			n[group]++
		}
		eventRows = append(eventRows, map[string]any{"entity_id": ev.Entity, "entity": ev.Name, "at": ts(ev.At), "links_known": len(g.Edges),
			"entities_exposed": len(exposed), "questions": n})
	}

	summarize := func(group string) map[string]any {
		var post, pre, hits float64
		n := 0
		evs := map[int]bool{}
		for _, o := range obs {
			if o.Group != group {
				continue
			}
			n++
			post += o.Post
			pre += o.Pre
			if o.Post >= hp.MoveThreshold {
				hits++
			}
			evs[o.Event] = true
		}
		if n == 0 {
			return map[string]any{"n": 0, "events": 0}
		}
		return map[string]any{"n": n, "events": len(evs), "mean_abs_move_after": round4(post / float64(n)), "mean_abs_move_before": round4(pre / float64(n)),
			"share_moved_after": round3(hits / float64(n))}
	}
	diff := func(a, b string, pick func(histObs) float64) func([]histObs) float64 {
		return func(sample []histObs) float64 {
			var sa, sb float64
			var na, nb int
			for _, o := range sample {
				switch o.Group {
				case a:
					sa += pick(o)
					na++
				case b:
					sb += pick(o)
					nb++
				}
			}
			if na == 0 || nb == 0 {
				return math.NaN()
			}
			return sa/float64(na) - sb/float64(nb)
		}
	}
	post := func(o histObs) float64 { return o.Post }
	pre := func(o histObs) float64 { return o.Pre }
	compare := func(a string) map[string]any {
		return map[string]any{
			"after_minus_other":  eventBootstrap(obs, diff(a, "other", post), 1000, hp.Seed),
			"before_minus_other": eventBootstrap(obs, diff(a, "other", pre), 1000, hp.Seed+1),
		}
	}
	groups := map[string]any{"direct": summarize("direct"), "exposed": summarize("exposed"), "other": summarize("other")}
	exp := groups["exposed"].(map[string]any)
	expN, _ := exp["n"].(int)
	expEvents, _ := exp["events"].(int)
	cmpExposed, cmpDirect := compare("exposed"), compare("direct")
	verdict := map[string]any{}
	switch {
	case expN < 30 || expEvents < 10:
		verdict["status"] = "insufficient"
		verdict["detail"] = fmt.Sprintf("%d exposed question observations over %d events; need at least 30 over 10 events before reading anything into the difference", expN, expEvents)
	default:
		after, before := cmpExposed["after_minus_other"].(ci), cmpExposed["before_minus_other"].(ci)
		switch {
		case after.Lo > 0 && before.Lo > 0:
			verdict["status"] = "confounded"
			verdict["detail"] = "Exposed questions moved more than others both after and before events: consistent with busier topics, not with the links transmitting the shock."
		case after.Lo > 0:
			verdict["status"] = "associated"
			verdict["detail"] = "Exposed questions moved more after events than other questions, and not before. This is an association; it does not show the links caused the moves."
		case after.Hi < 0:
			verdict["status"] = "negative"
			verdict["detail"] = "Exposed questions moved less than others after events."
		default:
			verdict["status"] = "no detectable difference"
			verdict["detail"] = "The interval for the difference includes zero: no evidence that modelled exposure tracks later price moves at this sample size."
		}
	}
	report := map[string]any{
		"kind":   "historical shock test",
		"params": hp,
		"note":   "Association between modelled exposure and later market moves, not causal validation.",
		"method": "Each event is an entity named in a claim at time t. It is shocked (severity 1) through only the links recorded by t and valid at t. Market questions linked to entities by t are grouped as direct (linked to the shocked entity), exposed (linked to an entity the model reaches) or other, and their absolute price change over the window after t is compared; the window before t is a placebo. Intervals are 95% event-cluster bootstraps.",
		"candidate_events": candidates, "events_used": len(events), "observations": len(obs),
		"groups":     groups,
		"exposed_vs_other": cmpExposed,
		"direct_vs_other":  cmpDirect,
		"verdict":    verdict,
		"caveats": []string{
			"News often reports price moves that already happened; the placebo window shows when that drives the result.",
			"Events overlap in time, so windows from different events share market moves.",
			"Question-entity links come from the same headline extraction as the graph, so errors are correlated.",
			"Prices come from snapshots taken every few hours; short windows can miss moves.",
		},
		"events": eventRows,
	}
	var id int64
	if err := db.QueryRow(`INSERT INTO intel_scenario_tests (project_id, params, report, created_at) VALUES (?,?,?,?) RETURNING id`,
		pid, mustJSON(hp), mustJSON(report), ts(now)).Scan(&id); err != nil {
		return nil, err
	}
	report["id"] = id
	return report, nil
}

// eventBootstrap resamples events (not observations) and returns the mean
// statistic with a 95% interval; undefined with fewer than 5 events.
func eventBootstrap(obs []histObs, stat func([]histObs) float64, B int, seed int64) ci {
	byEvent := map[int][]histObs{}
	keys := []int{}
	for _, o := range obs {
		if _, ok := byEvent[o.Event]; !ok {
			keys = append(keys, o.Event)
		}
		byEvent[o.Event] = append(byEvent[o.Event], o)
	}
	point := stat(obs)
	out := ci{Mean: point, Lo: math.NaN(), Hi: math.NaN()}
	if math.IsNaN(point) || len(keys) < 5 {
		return out
	}
	sort.Ints(keys)
	rng := rand.New(rand.NewSource(seed))
	vals := []float64{}
	sample := []histObs{}
	for b := 0; b < B; b++ {
		sample = sample[:0]
		for range keys {
			sample = append(sample, byEvent[keys[rng.Intn(len(keys))]]...)
		}
		if v := stat(sample); !math.IsNaN(v) {
			vals = append(vals, v)
		}
	}
	if len(vals) < 50 {
		return out
	}
	sort.Float64s(vals)
	out.Mean, out.Lo, out.Hi = round4(point), round4(quantile(vals, 0.025)), round4(quantile(vals, 0.975))
	return out
}

func latestHistoryTest(db *sql.DB, pid string) (map[string]any, error) {
	var id int64
	var raw, created string
	err := db.QueryRow(`SELECT id, report, created_at FROM intel_scenario_tests WHERE project_id = ? ORDER BY id DESC LIMIT 1`, pid).Scan(&id, &raw, &created)
	if err != nil {
		return nil, err
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "created_at": created, "params": rep["params"], "groups": rep["groups"], "exposed_vs_other": rep["exposed_vs_other"],
		"verdict": rep["verdict"], "events_used": rep["events_used"], "observations": rep["observations"], "note": rep["note"]}, nil
}

func listHistoryTests(db *sql.DB, pid string, limit int) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT id, report, created_at FROM intel_scenario_tests WHERE project_id = ? ORDER BY id DESC LIMIT ?`, pid, clampLimit(limit, 10, 50))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var raw, created string
		if err := rows.Scan(&id, &raw, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "created_at": created, "report": rawJSON(raw)})
	}
	return out, rows.Err()
}
