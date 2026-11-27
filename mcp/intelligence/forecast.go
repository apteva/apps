package main

// Forecasting, revision, extraction and scoring.
//
// Model v1 ("market-prior+evidence-shift"):
//   prior       = market price at the evidence cutoff (else 0.5, recorded
//                 as an assumption)
//   adjustment  = deterministic, capped log-odds shift whose direction and
//                 strength an LLM proposes from the frozen evidence set,
//                 and only when it cites evidence ids from that set
//   probability = logistic(logit(prior) + adjustment)
// The model abstains when there is neither a market price nor evidence.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	forecastModel        = "market-prior+evidence-shift"
	forecastModelVersion = "v1"
	shiftPerStrength     = 0.2 // log-odds per strength point
	maxShift             = 0.6
	marketMoveTrigger    = 0.05
)

func logit(p float64) float64 {
	p = math.Min(math.Max(p, 0.001), 0.999)
	return math.Log(p / (1 - p))
}
func logistic(x float64) float64 { return 1 / (1 + math.Exp(-x)) }
func round3(v float64) float64   { return math.Round(v*1000) / 1000 }

type evidenceBrief struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	Published string `json:"published,omitempty"`
	Hash      string `json:"-"`
}

func loadEvidenceBriefs(db *sql.DB, pid string, ids []int64) ([]evidenceBrief, error) {
	if len(ids) == 0 {
		return []evidenceBrief{}, nil
	}
	rows, err := searchEvidence(db, EvidenceQuery{ProjectID: pid, IDs: ids, Limit: len(ids)})
	if err != nil {
		return nil, err
	}
	byID := map[int64]evidenceBrief{}
	for _, r := range rows {
		id, _ := r["id"].(int64)
		b := evidenceBrief{ID: id, Title: fmt.Sprint(r["title"]), Source: fmt.Sprint(r["source"]), Hash: fmt.Sprint(r["content_hash"])}
		if p, ok := r["published_time"].(string); ok {
			b.Published = p
		}
		byID[id] = b
	}
	out := []evidenceBrief{}
	for _, id := range ids { // keep relevance order
		if b, ok := byID[id]; ok {
			out = append(out, b)
		}
	}
	return out, nil
}

// snapshotHash fingerprints exactly what the forecast could see, so the
// inputs can be re-assembled and verified later.
func snapshotHash(qid int64, cutoff time.Time, prior *float64, briefs []evidenceBrief) string {
	h := sha256.New()
	fmt.Fprintf(h, "q=%d\x00cutoff=%s\x00prior=%v", qid, ts(cutoff), fptr(prior))
	for _, b := range briefs {
		fmt.Fprintf(h, "\x00%d:%s", b.ID, b.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type llmAdjustment struct {
	Direction string  `json:"direction"` // yes | no | none
	Strength  float64 `json:"strength"`  // 0..3
	Cited     []int64 `json:"cited_evidence_ids"`
	Rationale string  `json:"rationale"`
}

const adjustSystemPrompt = `You assess whether recent evidence should move a probability away from a prediction-market price.
The market price already reflects public information; most evidence should NOT move it. Only propose a shift when specific evidence
in the list materially changes the likelihood of the question resolving YES under its stated rules, and the market may not yet reflect it.
Reply with JSON only: {"direction":"yes"|"no"|"none","strength":0-3,"cited_evidence_ids":[ids from the list],"rationale":"one or two sentences naming what changed"}.
strength: 0 none, 1 slight, 2 clear, 3 decisive. Cite only ids from the list. Never invent facts.`

func proposeAdjustment(llm llmClient, q *Question, prior float64, briefs []evidenceBrief, now time.Time) (*llmAdjustment, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\nResolution rule: %s\nDeadline: %s\nNow: %s\nMarket/prior probability of YES: %.3f\n\nEvidence (newest relevant first):\n",
		q.Text, truncate(q.ResolutionRule, 1500), ts(q.Deadline), ts(now), prior)
	for _, e := range briefs {
		fmt.Fprintf(&b, "- [%d] %s (%s%s)\n", e.ID, e.Title, e.Source, map[bool]string{true: ", " + e.Published, false: ""}[e.Published != ""])
	}
	reply, err := llm.Complete(adjustSystemPrompt, b.String(), 400)
	if err != nil {
		return nil, err
	}
	var adj llmAdjustment
	if err := decodeLLMJSON(reply, &adj); err != nil {
		return nil, err
	}
	return &adj, nil
}

// evidenceShift turns a proposed adjustment into the capped log-odds shift
// the model applies. Only citations of evidence the forecast could see
// count; without one, or with direction none or strength 0, the shift is 0.
// Live forecasts and backtests both use this rule.
func evidenceShift(adj *llmAdjustment, allowed []int64) (float64, []int64) {
	valid := []int64{}
	for _, id := range adj.Cited {
		if containsID(allowed, id) {
			valid = append(valid, id)
		}
	}
	strength := math.Max(0, math.Min(3, math.Round(adj.Strength)))
	if (adj.Direction != "yes" && adj.Direction != "no") || strength == 0 || len(valid) == 0 {
		return 0, valid
	}
	shift := math.Min(maxShift, strength*shiftPerStrength)
	if adj.Direction == "no" {
		shift = -shift
	}
	return shift, valid
}

// forecastQuestion records a new immutable forecast revision. The evidence
// cutoff is now; only evidence ingested by then is considered.
func forecastQuestion(env *ingestEnv, q *Question, trigger string) (*Forecast, error) {
	cutoff := env.now
	f := &Forecast{QuestionID: q.ID, EvidenceCutoff: cutoff, Model: forecastModel, ModelVersion: forecastModelVersion, Trigger: trigger, CreatedAt: env.now}
	market, err := priceAt(env.db, q.ID, cutoff)
	if err != nil {
		return nil, err
	}
	f.MarketPrice = market
	ids, err := questionEvidenceIDs(env.db, env.pid, q.ID, cutoff, 12)
	if err != nil {
		return nil, err
	}
	briefs, err := loadEvidenceBriefs(env.db, env.pid, ids)
	if err != nil {
		return nil, err
	}
	f.EvidenceIDs = ids
	prior := market
	if prior == nil {
		if len(briefs) == 0 {
			f.Abstained, f.AbstainReason = true, "no market price and no linked evidence at cutoff"
			f.SnapshotHash = snapshotHash(q.ID, cutoff, nil, briefs)
			return f, insertForecast(env.db, env.pid, f)
		}
		base := 0.5
		prior = &base
		f.Assumptions = append(f.Assumptions, "no market price: uninformative 0.5 base rate used as prior")
	}
	f.Prior = prior
	f.SnapshotHash = snapshotHash(q.ID, cutoff, prior, briefs)
	p := *prior
	explanation := fmt.Sprintf("Prior %.1f%% from %s.", p*100, map[bool]string{true: "market price at cutoff", false: "base rate"}[market != nil])
	switch {
	case len(briefs) == 0:
		explanation += " No linked evidence at cutoff; forecast equals prior."
	case env.llm == nil:
		explanation += fmt.Sprintf(" %d evidence items linked; evidence assessment unavailable (no LLM configured), so no adjustment.", len(briefs))
	default:
		adj, err := proposeAdjustment(env.llm, q, p, briefs, env.now)
		if err != nil {
			explanation += " Evidence assessment failed (" + err.Error() + "); no adjustment."
			break
		}
		shift, valid := evidenceShift(adj, ids)
		if shift == 0 {
			explanation += fmt.Sprintf(" Reviewed %d evidence items: no material change. %s", len(briefs), strings.TrimSpace(adj.Rationale))
			break
		}
		f.Adjustment = round3(shift)
		f.Model = forecastModel + " (" + env.llm.Model() + ")"
		explanation += fmt.Sprintf(" Shifted %+.2f log-odds toward %s citing evidence %v: %s", shift, strings.ToUpper(adj.Direction), valid, strings.TrimSpace(adj.Rationale))
	}
	prob := round3(logistic(logit(p) + f.Adjustment))
	f.Probability = &prob
	f.Explanation = explanation
	return f, insertForecast(env.db, env.pid, f)
}

// updateForecasts creates initial forecasts and revisions for open
// questions before their deadline. A revision needs a reason: new evidence
// linked since the last cutoff, or a market move beyond the threshold.
func updateForecasts(env *ingestEnv, maxLLM int) (map[string]int, error) {
	open, err := listQuestions(env.db, env.pid, "open", 5000)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"initial": 0, "new_evidence": 0, "market_move": 0, "abstained": 0}
	llmUsed := 0
	for i := range open {
		q := &open[i]
		if !q.Deadline.After(env.now) {
			continue
		}
		last, err := latestForecast(env.db, q.ID)
		if err != nil {
			return counts, err
		}
		trigger := ""
		switch {
		case last == nil:
			trigger = "initial"
		case q.LastEvidenceAt != nil && q.LastEvidenceAt.After(last.EvidenceCutoff):
			trigger = "new_evidence"
		default:
			if m, _ := priceAt(env.db, q.ID, env.now); m != nil && (last.MarketPrice == nil || math.Abs(*m-*last.MarketPrice) >= marketMoveTrigger) {
				trigger = "market_move"
			}
		}
		if trigger == "" {
			continue
		}
		sub := *env
		if llmUsed >= maxLLM {
			sub.llm = nil
		}
		f, err := forecastQuestion(&sub, q, trigger)
		if err != nil {
			return counts, err
		}
		if sub.llm != nil && len(f.EvidenceIDs) > 0 {
			llmUsed++
		}
		counts[trigger]++
		if f.Abstained {
			counts["abstained"]++
		}
	}
	return counts, nil
}

// ─── Claim extraction ──────────────────────────────────────────────

const extractSystemPrompt = `Extract structured facts from news headlines for a world model of organizations, places, markets, technologies, commodities, infrastructure and events.
Rules: never create entities for private individuals. People may appear only as public roles (e.g. "US President", "Fed Chair") with kind public_role.
Only state what the headline itself reports. Reply with JSON only:
{"items":[{"evidence_id":id,"entities":[{"name":"...","kind":"organization|facility|place|region|commodity|product|technology|market|institution|infrastructure|event|indicator|public_role|concept"}],
"claims":[{"subject":"entity name","predicate":"short verb phrase","object":"entity name or text","value":number|null,"unit":""}],
"relationships":[{"from":"entity name","type":"supplies|depends_on|located_in|part_of|operates|regulates|affects|produces|uses|competes_with|owns","to":"entity name","basis":"mechanism|asserted|co_occurrence"}]}]}
Relationship basis: mechanism = the headline says how one affects the other (e.g. "port closure halts X's exports to Y"); asserted = it states the relation without how; co_occurrence = the two only appear together and no relation is stated. Prefer co_occurrence when unsure.`

// extractClaims runs the LLM over evidence not yet extracted (cursor =
// last evidence id). Extracted relationships are recorded as "reported",
// each citing its evidence; nothing becomes accepted fact automatically.
func extractClaims(env *ingestEnv, batches int) (map[string]int, error) {
	counts := map[string]int{"evidence": 0, "entities": 0, "claims": 0, "relationships": 0}
	if env.llm == nil {
		return counts, nil
	}
	var cursor int64
	_ = env.db.QueryRow(`SELECT CAST(cursor AS INTEGER) FROM intel_sources WHERE project_id = ? AND slug = 'extraction'`, env.pid).Scan(&cursor)
	for b := 0; b < batches; b++ {
		rows, err := env.db.Query(`SELECT id, title, source FROM intelligence_evidence WHERE project_id = ? AND id > ? AND kind = 'news'
			AND basis <> 'synthetic' AND access <> 'restricted' AND content_expired_at IS NULL ORDER BY id LIMIT 10`, env.pid, cursor)
		if err != nil {
			return counts, err
		}
		type item struct {
			id            int64
			title, source string
		}
		items := []item{}
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.title, &it.source); err != nil {
				rows.Close()
				return counts, err
			}
			items = append(items, it)
		}
		rows.Close()
		if len(items) == 0 {
			break
		}
		var prompt strings.Builder
		for _, it := range items {
			fmt.Fprintf(&prompt, "[%d] %s (%s)\n", it.id, it.title, it.source)
		}
		reply, err := env.llm.Complete(extractSystemPrompt, prompt.String(), 2500)
		if err != nil {
			return counts, err
		}
		var parsed struct {
			Items []struct {
				EvidenceID int64 `json:"evidence_id"`
				Entities   []struct{ Name, Kind string }
				Claims     []struct {
					Subject, Predicate, Object, Unit string
					Value                            *float64
				}
				Relationships []struct{ From, Type, To, Basis string }
			} `json:"items"`
		}
		if err := decodeLLMJSON(reply, &parsed); err != nil {
			return counts, fmt.Errorf("extraction reply: %w", err)
		}
		valid := map[int64]bool{}
		for _, it := range items {
			valid[it.id] = true
		}
		extractor := "llm:" + env.llm.Model()
		for _, it := range parsed.Items {
			if !valid[it.EvidenceID] {
				continue
			}
			counts["evidence"]++
			names := map[string]int64{}
			for _, e := range it.Entities {
				ent, created, err := upsertWorldEntity(env.db, env.pid, WorldEntity{Name: e.Name, Kind: e.Kind, Basis: basisInferred})
				if err != nil {
					continue // includes refused person entities
				}
				names[strings.ToLower(e.Name)] = ent.ID
				if created {
					counts["entities"]++
				}
			}
			idFor := func(name string) *int64 {
				if id, ok := names[strings.ToLower(strings.TrimSpace(name))]; ok {
					return &id
				}
				return nil
			}
			for _, c := range it.Claims {
				conf := 0.5
				if _, err := insertClaim(env.db, env.pid, Claim{EvidenceID: it.EvidenceID, SubjectID: idFor(c.Subject), Subject: c.Subject, Predicate: c.Predicate,
					ObjectID: idFor(c.Object), Object: c.Object, Value: c.Value, Unit: c.Unit, Confidence: &conf, Extractor: extractor}); err == nil {
					counts["claims"]++
				}
			}
			for _, r := range it.Relationships {
				from, to := idFor(r.From), idFor(r.To)
				if from == nil || to == nil || *from == *to {
					continue
				}
				conf := 0.5
				cb := strings.ToLower(strings.TrimSpace(r.Basis))
				if cb != cbMechanism && cb != cbAsserted && cb != cbCooccurrence {
					cb = "" // left for relationships_grade
				}
				gradedBy := ""
				if cb != "" {
					gradedBy = extractor
				}
				if _, err := assertRelationship(env.db, env.pid, Relationship{From: *from, To: *to, Type: r.Type, Status: "reported", Confidence: &conf,
					EvidenceIDs: []int64{it.EvidenceID}, Note: extractor, CausalBasis: cb, GradedBy: gradedBy}); err == nil {
					counts["relationships"]++
				}
			}
			linkEvidenceEntitiesToQuestions(env.db, env.pid, it.EvidenceID, names)
		}
		cursor = items[len(items)-1].id
		_, _ = env.db.Exec(`UPDATE intel_sources SET cursor = ?, last_run_at = ?, last_success_at = ?, calls_total = calls_total + 1 WHERE project_id = ? AND slug = 'extraction'`,
			fmt.Sprint(cursor), ts(env.now), ts(env.now), env.pid)
	}
	return counts, nil
}

// linkEvidenceEntitiesToQuestions attaches entities found in question
// evidence to that question (bounded), connecting forecasts to the graph.
func linkEvidenceEntitiesToQuestions(db *sql.DB, pid string, evidenceID int64, names map[string]int64) {
	if len(names) == 0 {
		return
	}
	rows, err := db.Query(`SELECT question_id FROM intel_question_evidence WHERE evidence_id = ? AND relevance >= 0.5`, evidenceID)
	if err != nil {
		return
	}
	qids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			qids = append(qids, id)
		}
	}
	rows.Close()
	for _, qid := range qids {
		q, err := getQuestion(db, pid, qid)
		if err != nil {
			continue
		}
		ids := append([]int64{}, q.EntityIDs...)
		for _, id := range names {
			if len(ids) >= 12 {
				break
			}
			if !containsID(ids, id) {
				ids = append(ids, id)
			}
		}
		if len(ids) != len(q.EntityIDs) {
			_ = setQuestionEntities(db, pid, qid, ids)
		}
	}
}

// ─── Scoring ───────────────────────────────────────────────────────

type scoredForecast struct {
	QuestionID   int64    `json:"question_id"`
	Question     string   `json:"question"`
	Venue        string   `json:"venue,omitempty"`
	Category     string   `json:"category,omitempty"`
	Outcome      int      `json:"outcome"`
	ForecastID   int64    `json:"forecast_id"`
	Revision     int      `json:"revision"`
	Probability  float64  `json:"probability"`
	Market       *float64 `json:"market_price"`
	HorizonHours float64  `json:"horizon_hours"`
	Bucket       string   `json:"horizon_bucket"`
	BrierModel   float64  `json:"brier_model"`
	BrierMarket  *float64 `json:"brier_market,omitempty"`
	BrierUniform float64  `json:"brier_uniform"`
	LogModel     float64  `json:"log_loss_model"`
	LogMarket    *float64 `json:"log_loss_market,omitempty"`
}

func brier(p float64, o int) float64 { d := p - float64(o); return d * d }
func logLoss(p float64, o int) float64 {
	p = math.Min(math.Max(p, 0.01), 0.99)
	if o == 1 {
		return -math.Log(p)
	}
	return -math.Log(1 - p)
}

// pickScoredForecast chooses one forecast per question so revisions do
// not inflate the sample: the latest non-abstained forecast made at least
// lockHours before the deadline, else the latest made before resolution.
// Forecasts recorded after the outcome was known are never scored.
func pickScoredForecast(fs []Forecast, q Question, lockHours float64) *Forecast {
	lock := q.Deadline.Add(-time.Duration(lockHours * float64(time.Hour)))
	end := q.Deadline
	if q.ResolvedAt != nil && q.ResolvedAt.Before(end) {
		end = *q.ResolvedAt
	}
	var locked, late *Forecast
	for i := range fs {
		f := &fs[i]
		if f.Abstained || f.Probability == nil || f.CreatedAt.After(end) {
			continue
		}
		if !f.CreatedAt.After(lock) {
			locked = f
		}
		late = f
	}
	if locked != nil {
		return locked
	}
	return late
}

func horizonBucket(h float64) string {
	switch {
	case h < 24:
		return "<24h"
	case h < 72:
		return "1-3d"
	case h < 168:
		return "3-7d"
	default:
		return "7d+"
	}
}

func buildLedger(db *sql.DB, pid string, lockHours float64) (map[string]any, error) {
	resolved, err := listQuestions(db, pid, "resolved", 5000)
	if err != nil {
		return nil, err
	}
	scored := []scoredForecast{}
	unscored := 0
	for _, q := range resolved {
		if q.Outcome == nil {
			continue
		}
		fs, err := forecastsFor(db, q.ID)
		if err != nil {
			return nil, err
		}
		f := pickScoredForecast(fs, q, lockHours)
		if f == nil {
			unscored++
			continue
		}
		o := *q.Outcome
		h := q.Deadline.Sub(f.CreatedAt).Hours()
		s := scoredForecast{QuestionID: q.ID, Question: q.Text, Venue: q.Venue, Category: q.Category, Outcome: o, ForecastID: f.ID, Revision: f.Revision,
			Probability: *f.Probability, Market: f.MarketPrice, HorizonHours: math.Round(h*10) / 10, Bucket: horizonBucket(h),
			BrierModel: round4(brier(*f.Probability, o)), BrierUniform: 0.25, LogModel: round4(logLoss(*f.Probability, o))}
		if f.MarketPrice != nil {
			bm, lm := round4(brier(*f.MarketPrice, o)), round4(logLoss(*f.MarketPrice, o))
			s.BrierMarket, s.LogMarket = &bm, &lm
		}
		scored = append(scored, s)
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].QuestionID > scored[j].QuestionID })
	summary := summarize(scored)
	byBucket := map[string]any{}
	byVenue := map[string]any{}
	groups := map[string][]scoredForecast{}
	vgroups := map[string][]scoredForecast{}
	for _, s := range scored {
		groups[s.Bucket] = append(groups[s.Bucket], s)
		vgroups[fallback(s.Venue, "manual")] = append(vgroups[fallback(s.Venue, "manual")], s)
	}
	for k, v := range groups {
		byBucket[k] = summarize(v)
	}
	for k, v := range vgroups {
		byVenue[k] = summarize(v)
	}
	var open, voided, abstained int
	_ = db.QueryRow(`SELECT COUNT(*) FROM intel_questions WHERE project_id = ? AND status = 'open'`, pid).Scan(&open)
	_ = db.QueryRow(`SELECT COUNT(*) FROM intel_questions WHERE project_id = ? AND status = 'void'`, pid).Scan(&voided)
	_ = db.QueryRow(`SELECT COUNT(DISTINCT question_id) FROM intel_forecasts f WHERE project_id = ? AND abstained = 1
		AND NOT EXISTS (SELECT 1 FROM intel_forecasts g WHERE g.question_id = f.question_id AND g.abstained = 0)`, pid).Scan(&abstained)
	coverage := 0.0
	if len(resolved) > 0 {
		coverage = math.Round(float64(len(scored))/float64(len(resolved))*1000) / 1000
	}
	return map[string]any{
		"summary": summary, "by_horizon": byBucket, "by_venue": byVenue, "calibration": calibration(scored),
		"counts":      map[string]int{"open": open, "resolved": len(resolved), "scored": len(scored), "unscored_resolved": unscored, "void": voided, "abstained_only": abstained},
		"coverage":    coverage,
		"scored":      scored,
		"method":      fmt.Sprintf("One forecast per question: latest made >= %.0fh before deadline, else latest before resolution. Baselines: market price at the same cutoff, and uniform 0.5. Lower Brier/log loss is better; skill_vs_market > 0 means the model beat the market.", lockHours),
		"preliminary": len(scored) < 100,
	}, nil
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

func summarize(s []scoredForecast) map[string]any {
	if len(s) == 0 {
		return map[string]any{"n": 0}
	}
	var bm, bu, lm float64
	var bmk, lmk float64
	nm := 0
	for _, x := range s {
		bm += x.BrierModel
		bu += x.BrierUniform
		lm += x.LogModel
		if x.BrierMarket != nil {
			bmk += *x.BrierMarket
			lmk += *x.LogMarket
			nm++
		}
	}
	n := float64(len(s))
	out := map[string]any{"n": len(s), "brier_model": round4(bm / n), "brier_uniform": round4(bu / n), "log_loss_model": round4(lm / n)}
	if nm > 0 {
		// Compare on the subset that has a market baseline.
		var bmSub float64
		for _, x := range s {
			if x.BrierMarket != nil {
				bmSub += x.BrierModel
			}
		}
		out["n_with_market"] = nm
		out["brier_market"] = round4(bmk / float64(nm))
		out["log_loss_market"] = round4(lmk / float64(nm))
		if bmk > 0 {
			out["skill_vs_market"] = round4(1 - bmSub/bmk)
		}
	}
	return out
}

func calibration(s []scoredForecast) []map[string]any {
	type bin struct {
		n      int
		sumP   float64
		sumOut int
		lo, hi float64
	}
	bins := make([]bin, 10)
	for i := range bins {
		bins[i].lo, bins[i].hi = float64(i)/10, float64(i+1)/10
	}
	for _, x := range s {
		i := int(x.Probability * 10)
		if i > 9 {
			i = 9
		}
		bins[i].n++
		bins[i].sumP += x.Probability
		bins[i].sumOut += x.Outcome
	}
	out := []map[string]any{}
	for _, b := range bins {
		if b.n == 0 {
			continue
		}
		out = append(out, map[string]any{"range": fmt.Sprintf("%.1f-%.1f", b.lo, b.hi), "n": b.n,
			"mean_forecast": round3(b.sumP / float64(b.n)), "observed_rate": round3(float64(b.sumOut) / float64(b.n))})
	}
	return out
}
