package main

// Backtest evaluation on a frozen dataset.
//
// For every question and horizon h the forecast time ("cutoff") is h hours
// before the question stopped being uncertain (the earlier of close and
// resolution). Every method sees only what existed before that cutoff:
//
//   uniform       0.5
//   base_rate     smoothed YES rate of questions that had RESOLVED before
//                 the cutoff (expanding window)
//   market        last traded price at or before the cutoff
//   market_recal  market price recalibrated with a logistic fit on the
//                 market prices and outcomes of questions resolved before
//                 the cutoff (expanding window, shrunk toward identity)
//   ai_shift      market price plus the live model's capped LLM shift, on
//                 news GDELT had seen before the cutoff
//
// Training data is chosen per test question by resolution time, the
// strictest time-ordered holdout: no method uses an outcome that was not
// known at its own forecast time. Reports also split results into
// chronological blocks so drift is visible.
//
// The LLM's own training data is a leak no filter removes, so ai_shift is
// only scored on questions resolving after the stated model knowledge
// cutoff; earlier ones are recorded as excluded.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"time"
)

const evalMethodVersion = "eval-v1"

var statMethods = []string{"uniform", "base_rate", "market", "market_recal"}

type evalParams struct {
	DatasetID         int64     `json:"dataset_id"`
	Horizons          []float64 `json:"horizons_hours"`
	MinTrain          int       `json:"min_train"`
	Blocks            int       `json:"blocks"`
	Bootstrap         int       `json:"bootstrap"`
	Seed              int64     `json:"seed"`
	AI                bool      `json:"ai"`
	AIHorizon         float64   `json:"ai_horizon_hours,omitempty"`
	AIMaxItems        int       `json:"ai_max_items,omitempty"`
	AIKnowledgeCutoff string    `json:"ai_knowledge_cutoff,omitempty"`
	AIModel           string    `json:"ai_model,omitempty"`
	AIEvidenceDays    int       `json:"ai_evidence_days,omitempty"`
}

func parseEvalParams(args map[string]any) (evalParams, error) {
	p := evalParams{DatasetID: intArgAny(args, "dataset_id"), Horizons: []float64{24, 168}, MinTrain: 30, Blocks: 4, Bootstrap: 1000, Seed: 1,
		AIMaxItems: 30, AIEvidenceDays: 14}
	if p.DatasetID <= 0 {
		return p, errors.New("dataset_id is required (build one with evaluation_dataset_build)")
	}
	if v := strArg(args, "horizons_hours"); v != "" {
		p.Horizons = nil
		for _, s := range strings.Split(v, ",") {
			var h float64
			if _, err := fmt.Sscan(strings.TrimSpace(s), &h); err != nil || h <= 0 || h > 24*90 {
				return p, fmt.Errorf("horizons_hours: %q is not a positive number of hours up to 2160", s)
			}
			p.Horizons = append(p.Horizons, h)
		}
	}
	if n := intArgAny(args, "min_train"); n > 0 {
		p.MinTrain = int(n)
	}
	if n := intArgAny(args, "blocks"); n > 0 {
		p.Blocks = int(min(n, 10))
	}
	if n := intArgAny(args, "bootstrap"); n > 0 {
		p.Bootstrap = int(min(n, 10000))
	}
	if n := intArgAny(args, "seed"); n != 0 {
		p.Seed = n
	}
	p.AI, _ = boolArgAny(args, "ai")
	if p.AI {
		p.AIKnowledgeCutoff = strArg(args, "ai_knowledge_cutoff")
		t := parseTS(p.AIKnowledgeCutoff)
		if t == nil {
			return p, errors.New("ai=true needs ai_knowledge_cutoff (the model's training-data cutoff, RFC3339 or YYYY-MM-DD); questions resolving before it are excluded from the AI comparison")
		}
		p.AIKnowledgeCutoff = ts(*t)
		p.AIHorizon = p.Horizons[0]
		if v, ok := floatArgAny(args, "ai_horizon_hours"); ok && v > 0 {
			p.AIHorizon = v
		}
		if n := intArgAny(args, "ai_max_items"); n > 0 {
			p.AIMaxItems = int(min(n, 300))
		}
		if n := intArgAny(args, "ai_evidence_days"); n > 0 {
			p.AIEvidenceDays = int(min(n, 30))
		}
	}
	return p, nil
}


// evalPred is one method's forecast for one question at one horizon.
type evalPred struct {
	Item    int       `json:"item"`
	Horizon float64   `json:"horizon_hours"`
	Method  string    `json:"method"`
	Cutoff  time.Time `json:"cutoff"`
	P       *float64  `json:"p,omitempty"`
	Status  string    `json:"status"` // ok | abstain | error | excluded
	Detail  string    `json:"detail,omitempty"`
}

type trainPoint struct {
	resolved time.Time
	price    float64
	outcome  int
}

// statPredictions computes the statistical methods for one horizon.
func statPredictions(items []evalItem, h float64, minTrain int) []evalPred {
	dur := time.Duration(h * float64(time.Hour))
	type row struct {
		cutoff time.Time
		price  float64
		ok     bool
		reason string
	}
	rows := make([]row, len(items))
	for i, it := range items {
		c := it.end().Add(-dur)
		rows[i].cutoff = c
		switch {
		case !c.After(it.OpenedAt):
			rows[i].reason = "question opened after the forecast time"
		default:
			if p, _, ok := it.priceAt(c); ok {
				rows[i].price, rows[i].ok = p, true
			} else {
				rows[i].reason = "no trade before the forecast time"
			}
		}
	}
	// Training points: questions whose outcome is known, with their own
	// market price at this horizon. Sorted by resolution time.
	train := []trainPoint{}
	for i, it := range items {
		if rows[i].ok {
			train = append(train, trainPoint{resolved: it.ResolvedAt, price: rows[i].price, outcome: it.Outcome})
		}
	}
	sort.Slice(train, func(i, j int) bool { return train[i].resolved.Before(train[j].resolved) })

	out := []evalPred{}
	for i := range items {
		r := rows[i]
		mk := func(m string) evalPred { return evalPred{Item: items[i].Idx, Horizon: h, Method: m, Cutoff: r.cutoff} }
		if r.reason == "question opened after the forecast time" {
			for _, m := range statMethods {
				p := mk(m)
				p.Status, p.Detail = "excluded", r.reason
				out = append(out, p)
			}
			continue
		}
		// Outcomes known strictly before this question's forecast time.
		k := sort.Search(len(train), func(j int) bool { return !train[j].resolved.Before(r.cutoff) })
		known := train[:k]

		u := mk("uniform")
		half := 0.5
		u.P, u.Status = &half, "ok"
		out = append(out, u)

		br := mk("base_rate")
		if len(known) < minTrain {
			br.Status, br.Detail = "abstain", fmt.Sprintf("only %d resolved questions before the forecast time (need %d)", len(known), minTrain)
		} else {
			yes := 0
			for _, t := range known {
				yes += t.outcome
			}
			v := (float64(yes) + 1) / (float64(len(known)) + 2)
			br.P, br.Status = &v, "ok"
		}
		out = append(out, br)

		m := mk("market")
		if !r.ok {
			m.Status, m.Detail = "abstain", r.reason
		} else {
			v := r.price
			m.P, m.Status = &v, "ok"
		}
		out = append(out, m)

		rc := mk("market_recal")
		switch {
		case !r.ok:
			rc.Status, rc.Detail = "abstain", r.reason
		case len(known) < minTrain:
			rc.Status, rc.Detail = "abstain", fmt.Sprintf("only %d resolved questions before the forecast time (need %d)", len(known), minTrain)
		default:
			a, b := fitRecalibration(known)
			v := logistic(a + b*logit(r.price))
			rc.P, rc.Status = &v, "ok"
			rc.Detail = fmt.Sprintf("a=%.3f b=%.3f n=%d", a, b, len(known))
		}
		out = append(out, rc)
	}
	return out
}

// fitRecalibration fits P(yes) = logistic(a + b*logit(price)) by Newton's
// method with a ridge penalty pulling (a, b) toward (0, 1), the identity
// map. With little data the result stays close to the raw market price.
func fitRecalibration(pts []trainPoint) (float64, float64) {
	const lambda = 2.0
	a, b := 0.0, 1.0
	for iter := 0; iter < 50; iter++ {
		ga, gb := lambda*a, lambda*(b-1)
		haa, hab, hbb := lambda, 0.0, lambda
		for _, t := range pts {
			x := logit(t.price)
			p := logistic(a + b*x)
			r := p - float64(t.outcome)
			w := p * (1 - p)
			ga += r
			gb += r * x
			haa += w
			hab += w * x
			hbb += w * x * x
		}
		det := haa*hbb - hab*hab
		if det <= 1e-12 {
			break
		}
		da := (hbb*ga - hab*gb) / det
		db := (haa*gb - hab*ga) / det
		a, b = a-da, b-db
		if math.Abs(da)+math.Abs(db) < 1e-9 {
			break
		}
	}
	return a, b
}

// ─── scoring ───────────────────────────────────────────────────────

type scoredRec struct {
	item    int
	cluster string
	cutoff  time.Time
	outcome int
	p       map[string]float64
}

func meanOf(recs []scoredRec, idx []int, f func(scoredRec) float64) float64 {
	if len(idx) == 0 {
		return math.NaN()
	}
	s := 0.0
	for _, i := range idx {
		s += f(recs[i])
	}
	return s / float64(len(idx))
}

// clusterBootstrap resamples whole clusters (linked questions move
// together) and returns the 2.5% and 97.5% quantiles of stat.
func clusterBootstrap(recs []scoredRec, B int, seed int64, stat func(idx []int) float64) (float64, float64) {
	if len(recs) < 2 || B < 50 {
		return math.NaN(), math.NaN()
	}
	byCluster := map[string][]int{}
	keys := []string{}
	for i, r := range recs {
		if _, ok := byCluster[r.cluster]; !ok {
			keys = append(keys, r.cluster)
		}
		byCluster[r.cluster] = append(byCluster[r.cluster], i)
	}
	if len(keys) < 5 {
		// Resampling two or three clusters gives a misleadingly narrow
		// interval; report none.
		return math.NaN(), math.NaN()
	}
	sort.Strings(keys)
	rng := rand.New(rand.NewSource(seed))
	vals := make([]float64, 0, B)
	idx := make([]int, 0, len(recs))
	for b := 0; b < B; b++ {
		idx = idx[:0]
		for range keys {
			idx = append(idx, byCluster[keys[rng.Intn(len(keys))]]...)
		}
		if v := stat(idx); !math.IsNaN(v) {
			vals = append(vals, v)
		}
	}
	if len(vals) < 10 {
		return math.NaN(), math.NaN()
	}
	sort.Float64s(vals)
	q := func(f float64) float64 { return vals[int(math.Min(float64(len(vals)-1), math.Floor(f*float64(len(vals)))))] }
	return q(0.025), q(0.975)
}

func allIdx(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

type ci struct {
	Mean float64 `json:"mean"`
	Lo   float64 `json:"lo"`
	Hi   float64 `json:"hi"`
}

// MarshalJSON writes an undefined bound or mean (too few clusters or no
// data) as null; encoding/json rejects NaN, which would void the report.
func (c ci) MarshalJSON() ([]byte, error) {
	f := func(v float64) any {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		return v
	}
	return json.Marshal(map[string]any{"mean": f(c.Mean), "lo": f(c.Lo), "hi": f(c.Hi)})
}

func mkCI(recs []scoredRec, B int, seed int64, f func(scoredRec) float64) ci {
	mean := meanOf(recs, allIdx(len(recs)), f)
	lo, hi := clusterBootstrap(recs, B, seed, func(idx []int) float64 { return meanOf(recs, idx, f) })
	return ci{Mean: round4(mean), Lo: round4(lo), Hi: round4(hi)}
}

func calBins(recs []scoredRec, method string) ([]map[string]any, float64) {
	type bin struct {
		n         int
		sumP, out float64
	}
	bins := make([]bin, 10)
	for _, r := range recs {
		p := r.p[method]
		i := min(int(p*10), 9)
		bins[i].n++
		bins[i].sumP += p
		bins[i].out += float64(r.outcome)
	}
	out := []map[string]any{}
	ece := 0.0
	for i, b := range bins {
		if b.n == 0 {
			continue
		}
		mp, or := b.sumP/float64(b.n), b.out/float64(b.n)
		ece += float64(b.n) / float64(len(recs)) * math.Abs(mp-or)
		out = append(out, map[string]any{"range": fmt.Sprintf("%.1f-%.1f", float64(i)/10, float64(i+1)/10), "n": b.n,
			"mean_forecast": round3(mp), "observed_rate": round3(or)})
	}
	return out, round4(ece)
}

func verdictFor(n int, d ci, minN int) string {
	switch {
	case n < minN:
		return fmt.Sprintf("insufficient sample (n=%d, need at least %d)", n, minN)
	case math.IsNaN(d.Lo) || math.IsNaN(d.Hi):
		return "no interval (too few clusters)"
	case d.Hi < 0:
		return "better than the market (interval below 0)"
	case d.Lo > 0:
		return "worse than the market (interval above 0)"
	default:
		return "no detectable difference from the market (interval includes 0)"
	}
}

// summarizeHorizon scores the statistical methods at one horizon.
func summarizeHorizon(items []evalItem, preds []evalPred, h float64, p evalParams) map[string]any {
	byItem := map[int]map[string]evalPred{}
	status := map[string]map[string]int{}
	reasons := map[string]map[string]int{}
	for _, pr := range preds {
		if pr.Horizon != h {
			continue
		}
		if byItem[pr.Item] == nil {
			byItem[pr.Item] = map[string]evalPred{}
		}
		byItem[pr.Item][pr.Method] = pr
		if status[pr.Method] == nil {
			status[pr.Method], reasons[pr.Method] = map[string]int{}, map[string]int{}
		}
		status[pr.Method][pr.Status]++
		if pr.Status != "ok" {
			reasons[pr.Method][reasonKey(pr.Detail)]++
		}
	}
	// Common set: every statistical method produced a forecast.
	recs := []scoredRec{}
	for _, it := range items {
		ps := byItem[it.Idx]
		r := scoredRec{item: it.Idx, cluster: it.Cluster, outcome: it.Outcome, p: map[string]float64{}}
		ok := true
		for _, m := range statMethods {
			pr, has := ps[m]
			if !has || pr.Status != "ok" || pr.P == nil {
				ok = false
				break
			}
			r.p[m], r.cutoff = *pr.P, pr.Cutoff
		}
		if ok {
			recs = append(recs, r)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].cutoff.Before(recs[j].cutoff) })
	methods := map[string]any{}
	cal := map[string]any{}
	for mi, m := range statMethods {
		m := m
		seed := p.Seed + int64(mi)*1000 + int64(h)
		entry := map[string]any{"status": status[m], "not_ok_reasons": reasons[m]}
		if len(recs) > 0 {
			entry["brier"] = mkCI(recs, p.Bootstrap, seed, func(r scoredRec) float64 { return brier(r.p[m], r.outcome) })
			entry["log_loss"] = mkCI(recs, p.Bootstrap, seed+1, func(r scoredRec) float64 { return logLoss(r.p[m], r.outcome) })
			bins, ece := calBins(recs, m)
			entry["ece"] = ece
			cal[m] = bins
			if m != "market" {
				d := mkCI(recs, p.Bootstrap, seed+2, func(r scoredRec) float64 { return brier(r.p[m], r.outcome) - brier(r.p["market"], r.outcome) })
				entry["brier_minus_market"] = d
				entry["verdict_vs_market"] = verdictFor(len(recs), d, 30)
			}
		}
		methods[m] = entry
	}
	// Chronological blocks of the common set.
	blocks := []map[string]any{}
	if len(recs) > 0 {
		k := max(1, min(p.Blocks, len(recs)))
		for b := 0; b < k; b++ {
			lo, hi := b*len(recs)/k, (b+1)*len(recs)/k
			if lo >= hi {
				continue
			}
			sub := recs[lo:hi]
			entry := map[string]any{"block": b + 1, "n": len(sub), "from": ts(sub[0].cutoff), "to": ts(sub[len(sub)-1].cutoff)}
			for _, m := range statMethods {
				entry["brier_"+m] = round4(meanOf(sub, allIdx(len(sub)), func(r scoredRec) float64 { return brier(r.p[m], r.outcome) }))
			}
			blocks = append(blocks, entry)
		}
	}
	yes := 0
	for _, r := range recs {
		yes += r.outcome
	}
	clusters := map[string]bool{}
	for _, r := range recs {
		clusters[r.cluster] = true
	}
	return map[string]any{"horizon_hours": h, "n_questions": len(items), "n_scored": len(recs), "n_clusters": len(clusters), "yes_rate": rateOf(yes, len(recs)),
		"methods": methods, "calibration": cal, "blocks": blocks}
}

var digitsRe = regexp.MustCompile(`\d+`)

// reasonKey groups failure details that differ only in numbers.
func reasonKey(s string) string { return digitsRe.ReplaceAllString(s, "N") }

func rateOf(a, n int) any {
	if n == 0 {
		return nil
	}
	return round3(float64(a) / float64(n))
}

// ─── AI-assisted method ────────────────────────────────────────────

type aiDetail struct {
	Evidence   []evidenceBrief `json:"evidence"`
	Shift      float64         `json:"shift"`
	Direction  string          `json:"direction,omitempty"`
	Cited      []int64         `json:"cited,omitempty"`
	Rationale  string          `json:"rationale,omitempty"`
	QueryError string          `json:"query_error,omitempty"`
}

// aiPredictions runs the live model's evidence shift on historical
// questions. Evidence is GDELT articles first seen before the cutoff.
func aiPredictions(env *ingestEnv, items []evalItem, p evalParams, now time.Time, progress func(done, total int)) []evalPred {
	knowledge := *parseTS(p.AIKnowledgeCutoff)
	dur := time.Duration(p.AIHorizon * float64(time.Hour))
	gdeltFloor := now.AddDate(0, 0, -88) // DOC API only searches about three months back
	type cand struct {
		it     evalItem
		cutoff time.Time
		price  float64
	}
	out := []evalPred{}
	cands := []cand{}
	for _, it := range items {
		c := it.end().Add(-dur)
		pr := evalPred{Item: it.Idx, Horizon: p.AIHorizon, Method: "ai_shift", Cutoff: c}
		price, _, ok := it.priceAt(c)
		switch {
		case !c.After(it.OpenedAt):
			pr.Status, pr.Detail = "excluded", "question opened after the forecast time"
		case !ok:
			pr.Status, pr.Detail = "abstain", "no trade before the forecast time"
		case !it.ResolvedAt.After(knowledge):
			pr.Status, pr.Detail = "excluded", "resolved before the model's knowledge cutoff (possible training-data leak)"
		case c.Add(-time.Duration(p.AIEvidenceDays) * 24 * time.Hour).Before(gdeltFloor):
			pr.Status, pr.Detail = "excluded", "evidence window older than GDELT's three-month search range"
		default:
			cands = append(cands, cand{it, c, price})
			continue
		}
		out = append(out, pr)
	}
	// Deterministic sample when there are more eligible questions than the
	// budget allows.
	rng := rand.New(rand.NewSource(p.Seed))
	rng.Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
	for i, c := range cands {
		pr := evalPred{Item: c.it.Idx, Horizon: p.AIHorizon, Method: "ai_shift", Cutoff: c.cutoff}
		if i >= p.AIMaxItems {
			pr.Status, pr.Detail = "excluded", fmt.Sprintf("outside the AI sample (ai_max_items=%d)", p.AIMaxItems)
			out = append(out, pr)
			continue
		}
		v, det, err := aiForecastAt(env, c.it, c.cutoff, c.price, p.AIEvidenceDays)
		b, _ := json.Marshal(det)
		pr.Detail = string(b)
		if err != nil {
			pr.Status = "error"
			pr.Detail = err.Error() + " " + pr.Detail
		} else {
			pr.P, pr.Status = &v, "ok"
		}
		out = append(out, pr)
		if progress != nil {
			progress(min(i+1, p.AIMaxItems), min(len(cands), p.AIMaxItems))
		}
	}
	return out
}

func aiForecastAt(env *ingestEnv, it evalItem, cutoff time.Time, price float64, evidenceDays int) (float64, aiDetail, error) {
	det := aiDetail{Evidence: []evidenceBrief{}}
	topics := questionTopics(it.Text)
	if len(topics) > 0 {
		terms := []string{}
		for _, t := range topics[:min(len(topics), 4)] {
			if strings.Contains(t, " ") {
				t = `"` + t + `"`
			}
			terms = append(terms, t)
		}
		query := terms[0]
		if len(terms) > 1 {
			query = "(" + strings.Join(terms, " OR ") + ")"
		}
		raw, ok := env.sc.call("gdelt", "doc_search", map[string]any{"query": query + " sourcelang:english", "maxrecords": "10",
			"startdatetime": ts(cutoff.Add(-time.Duration(evidenceDays) * 24 * time.Hour)), "enddatetime": ts(cutoff)})
		if !ok {
			det.QueryError = orStr(lastSourceError("gdelt"), "news search failed")
		} else {
			for _, n := range parseNews(raw, "gdelt") {
				seen := parseTS(n.Date)
				// Availability rule: GDELT's first-seen time must be before
				// the cutoff; undated items are dropped.
				if seen == nil || seen.After(cutoff) {
					continue
				}
				det.Evidence = append(det.Evidence, evidenceBrief{ID: int64(len(det.Evidence) + 1), Title: n.Title, Source: n.Source, Published: ts(*seen)})
			}
		}
	}
	if det.QueryError != "" {
		return 0, det, errors.New("evidence search failed: " + det.QueryError)
	}
	if len(det.Evidence) == 0 || env.llm == nil {
		return round3(price), det, nil
	}
	q := &Question{Text: it.Text, ResolutionRule: "Resolves by the venue's own rules (" + it.Venue + " " + fallback(it.URL, it.ExternalID) + ").", Deadline: it.ClosesAt}
	adj, err := proposeAdjustment(env.llm, q, price, det.Evidence, cutoff)
	if err != nil {
		return 0, det, err
	}
	ids := make([]int64, len(det.Evidence))
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	shift, valid := evidenceShift(adj, ids)
	det.Shift, det.Direction, det.Cited, det.Rationale = round3(shift), adj.Direction, valid, truncate(strings.TrimSpace(adj.Rationale), 400)
	return round3(logistic(logit(price) + shift)), det, nil
}

func summarizeAI(items []evalItem, preds []evalPred, p evalParams) map[string]any {
	byIdx := map[int]evalItem{}
	for _, it := range items {
		byIdx[it.Idx] = it
	}
	status, reasons := map[string]int{}, map[string]int{}
	recs := []scoredRec{}
	shifted := 0
	examples := []map[string]any{}
	for _, pr := range preds {
		if pr.Method != "ai_shift" {
			continue
		}
		status[pr.Status]++
		if pr.Status != "ok" {
			k := pr.Detail
			if i := strings.Index(k, " {"); i > 0 {
				k = k[:i]
			}
			reasons[reasonKey(k)]++
			continue
		}
		it := byIdx[pr.Item]
		market, _, _ := it.priceAt(pr.Cutoff)
		recs = append(recs, scoredRec{item: it.Idx, cluster: it.Cluster, cutoff: pr.Cutoff, outcome: it.Outcome, p: map[string]float64{"ai_shift": *pr.P, "market": market}})
		var det aiDetail
		_ = json.Unmarshal([]byte(pr.Detail), &det)
		if det.Shift != 0 {
			shifted++
			if len(examples) < 8 {
				examples = append(examples, map[string]any{"question": it.Text, "cutoff": ts(pr.Cutoff), "market": round3(market), "ai": *pr.P,
					"outcome": it.Outcome, "rationale": det.Rationale, "evidence_used": len(det.Evidence)})
			}
		}
	}
	out := map[string]any{"horizon_hours": p.AIHorizon, "knowledge_cutoff": p.AIKnowledgeCutoff, "model": p.AIModel, "status": status, "not_ok_reasons": reasons,
		"n_scored": len(recs), "n_shifted": shifted, "examples_shifted": examples}
	if len(recs) > 0 {
		seed := p.Seed + 77
		out["brier_ai"] = mkCI(recs, p.Bootstrap, seed, func(r scoredRec) float64 { return brier(r.p["ai_shift"], r.outcome) })
		out["brier_market"] = mkCI(recs, p.Bootstrap, seed+1, func(r scoredRec) float64 { return brier(r.p["market"], r.outcome) })
		d := mkCI(recs, p.Bootstrap, seed+2, func(r scoredRec) float64 {
			return brier(r.p["ai_shift"], r.outcome) - brier(r.p["market"], r.outcome)
		})
		out["brier_ai_minus_market"] = d
		out["log_loss_ai_minus_market"] = mkCI(recs, p.Bootstrap, seed+3, func(r scoredRec) float64 {
			return logLoss(r.p["ai_shift"], r.outcome) - logLoss(r.p["market"], r.outcome)
		})
		out["verdict"] = verdictFor(len(recs), d, 30)
	} else {
		out["verdict"] = "not evaluated: no AI forecasts were scored"
	}
	return out
}

// ─── run orchestration ─────────────────────────────────────────────

func createEvalRun(db *sql.DB, pid string, p evalParams, now time.Time) (int64, error) {
	var id int64
	err := db.QueryRow(`INSERT INTO intel_eval_runs (project_id, dataset_id, status, params, code_version, created_at) VALUES (?,?,?,?,?,?) RETURNING id`,
		pid, p.DatasetID, "running", mustJSON(p), evalCodeVersion(), ts(now)).Scan(&id)
	return id, err
}

func storeEvalPredictions(db *sql.DB, runID int64, preds []evalPred) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, pr := range preds {
		var p any
		if pr.P != nil {
			p = *pr.P
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO intel_eval_predictions (run_id, item_idx, horizon_hours, method, cutoff, probability, status, detail) VALUES (?,?,?,?,?,?,?,?)`,
			runID, pr.Item, pr.Horizon, pr.Method, tsMilli(pr.Cutoff), p, pr.Status, pr.Detail); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// executeEvalRun computes everything for a run and stores the report. It
// is deterministic for the statistical methods given the dataset, params
// and code version.
func executeEvalRun(env *ingestEnv, runID int64, ds evalDataset, items []evalItem, p evalParams) (map[string]any, error) {
	if got := datasetHash(items); got != ds.ContentHash {
		return nil, fmt.Errorf("dataset %d content hash mismatch (stored %s, loaded %s)", ds.ID, ds.ContentHash, got)
	}
	preds := []evalPred{}
	horizons := []any{}
	for _, h := range p.Horizons {
		hp := statPredictions(items, h, p.MinTrain)
		preds = append(preds, hp...)
		horizons = append(horizons, summarizeHorizon(items, hp, h, p))
	}
	report := map[string]any{"run_id": runID, "code_version": evalCodeVersion(), "params": p,
		"dataset": map[string]any{"id": ds.ID, "name": ds.Name, "content_hash": ds.ContentHash, "n_items": ds.NItems, "fetched_at": ds.FetchedAt, "params": ds.Params, "skipped": ds.Skipped},
		"backtest": map[string]any{"horizons": horizons}}
	if p.AI {
		if env.llm == nil {
			report["ai"] = map[string]any{"verdict": "not evaluated: no LLM configured (llm_model is none or the llm app is not bound)"}
		} else {
			_, _ = env.db.Exec(`UPDATE intel_eval_runs SET error = ? WHERE id = ?`, "running AI forecasts", runID)
			ap := aiPredictions(env, items, p, env.now, func(done, total int) {
				_, _ = env.db.Exec(`UPDATE intel_eval_runs SET error = ? WHERE id = ?`, fmt.Sprintf("AI forecasts %d/%d", done, total), runID)
			})
			preds = append(preds, ap...)
			report["ai"] = summarizeAI(items, ap, p)
		}
	} else {
		report["ai"] = map[string]any{"verdict": "not run (ai=false)"}
	}
	report["leakage_controls"] = evalLeakageControls(p)
	report["limitations"] = evalLimitations()
	if err := storeEvalPredictions(env.db, runID, preds); err != nil {
		return nil, err
	}
	return report, nil
}

func evalCodeVersion() string {
	v := "unknown"
	if m := regexp.MustCompile(`(?m)^version:\s*(\S+)`).FindStringSubmatch(manifestYAML); len(m) == 2 {
		v = m[1]
	}
	return "intelligence " + v + " / " + evalMethodVersion + " / " + forecastModel + " " + forecastModelVersion
}

func evalLeakageControls(p evalParams) []string {
	c := []string{
		"Forecast time is h hours before the earlier of close and resolution; questions created after it are excluded.",
		"Market price is the last individual trade at or before the forecast time (trade timestamps from the venue), never a later or final price.",
		"Trained methods (base_rate, market_recal) use only questions whose outcome was known strictly before each forecast time (expanding window, per question).",
		"The dataset is frozen with a content hash; every run verifies it before scoring.",
	}
	if p.AI {
		c = append(c,
			"AI evidence is GDELT articles whose first-seen time is before the forecast time; undated articles are dropped.",
			"AI forecasts are scored only on questions resolving after the stated model knowledge cutoff ("+p.AIKnowledgeCutoff+").")
	}
	return c
}

func evalLimitations() []string {
	return []string{
		"Selection uses later information: questions had to resolve YES/NO, and trader counts are final counts. Results describe liquid, cleanly resolved questions only.",
		"Question text is the venue's current text; edits made after the forecast time cannot be detected.",
		"Prediction-market prices already include public information, so beating the market is a high bar; the market baseline is not an independent method.",
		"The AI method's knowledge cutoff is stated by the operator, not verified; a wrong value would let outcome knowledge leak into AI forecasts.",
		"GDELT returns articles as indexed today; articles removed since the forecast time cannot be recovered.",
		"Historical backtests are not prospective evidence. Only forecasts recorded before outcomes (the live ledger) count as prospective.",
	}
}
