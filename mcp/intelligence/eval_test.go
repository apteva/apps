package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

var evalT0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// mkEvalItem makes a question open for 20 days that resolves at day d with
// the given outcome and two trades: p1 at day d-5 and p2 one hour before
// resolution.
func mkEvalItem(idx int, d float64, outcome int, p1, p2 float64) evalItem {
	res := evalT0.Add(time.Duration(d * 24 * float64(time.Hour)))
	return evalItem{Idx: idx, Venue: "manifold", ExternalID: "m" + string(rune('a'+idx%26)), Cluster: "c" + string(rune('a'+idx%26)) + string(rune('0'+idx/26)),
		Text: "Will the test event happen?", OpenedAt: res.AddDate(0, 0, -20), ClosesAt: res, ResolvedAt: res, Outcome: outcome,
		Prices: [][2]float64{{float64(res.AddDate(0, 0, -5).UnixMilli()), p1}, {float64(res.Add(-time.Hour).UnixMilli()), p2}}}
}

func predOf(preds []evalPred, item int, method string) evalPred {
	for _, p := range preds {
		if p.Item == item && p.Method == method {
			return p
		}
	}
	return evalPred{}
}

func TestPriceAtIgnoresLaterTrades(t *testing.T) {
	it := mkEvalItem(0, 30, 1, 0.3, 0.97)
	p, at, ok := it.priceAt(it.ResolvedAt.Add(-24 * time.Hour))
	if !ok || p != 0.3 {
		t.Fatalf("24h before resolution the last trade is 0.3, got %v ok=%v", p, ok)
	}
	if at.After(it.ResolvedAt.Add(-24 * time.Hour)) {
		t.Fatalf("trade time %v is after the cutoff", at)
	}
	if _, _, ok := it.priceAt(it.OpenedAt); ok {
		t.Fatal("no trade exists at open; priceAt must report ok=false")
	}
	// A trade exactly at the cutoff is available.
	exact := time.UnixMilli(int64(it.Prices[1][0])).UTC()
	if p, _, _ := it.priceAt(exact); p != 0.97 {
		t.Fatalf("a trade at the cutoff instant counts, got %v", p)
	}
}

// Trained methods may only use outcomes known before each forecast time.
func TestStatPredictionsTrainOnlyOnEarlierResolutions(t *testing.T) {
	items := []evalItem{}
	// 40 early questions, all NO, resolving days 1-40.
	for i := 0; i < 40; i++ {
		items = append(items, mkEvalItem(i, float64(i+1), 0, 0.2, 0.05))
	}
	// Then 40 later questions, all YES. If the base rate for an early
	// question saw these, it would rise above 0.5.
	for i := 40; i < 80; i++ {
		items = append(items, mkEvalItem(i, float64(i+1), 1, 0.8, 0.95))
	}
	preds := statPredictions(items, 24, 30)

	// Question 35 (day 36, cutoff day 35): 34 known, all NO.
	br := predOf(preds, 35, "base_rate")
	if br.Status != "ok" || br.P == nil {
		t.Fatalf("base_rate for item 35: %+v", br)
	}
	if want := 1.0 / 36; math.Abs(*br.P-want) > 1e-9 {
		t.Fatalf("base_rate must use only the 34 earlier NO outcomes: want %.4f got %.4f", want, *br.P)
	}
	// Question 20: only 19 known, below min_train.
	if p := predOf(preds, 20, "base_rate"); p.Status != "abstain" {
		t.Fatalf("too little history must abstain, got %+v", p)
	}
	if p := predOf(preds, 20, "market_recal"); p.Status != "abstain" {
		t.Fatalf("recalibration with too little history must abstain, got %+v", p)
	}
	// Item 79's cutoff (day 79) is item 78's resolution instant: that
	// outcome is not known strictly before it, so 78 outcomes (38 YES).
	last := predOf(preds, 79, "base_rate")
	if want := (38.0 + 1) / (78 + 2); math.Abs(*last.P-want) > 1e-9 {
		t.Fatalf("item 79 must see exactly the 78 outcomes resolved before its cutoff: want %.4f got %.4f", want, *last.P)
	}
	// Market uses the 24h-earlier trade, not the final one.
	if m := predOf(preds, 50, "market"); m.P == nil || *m.P != 0.8 {
		t.Fatalf("market price 24h before must be 0.8, got %+v", m)
	}
}

func TestStatPredictionsExcludeQuestionsCreatedAfterCutoff(t *testing.T) {
	it := mkEvalItem(0, 10, 1, 0.4, 0.9)
	it.OpenedAt = it.ResolvedAt.Add(-12 * time.Hour)
	for _, p := range statPredictions([]evalItem{it}, 24, 30) {
		if p.Status != "excluded" {
			t.Fatalf("%s must be excluded when the question did not exist at the forecast time, got %s", p.Method, p.Status)
		}
	}
}

func TestRecalibrationStaysNearIdentityOnCalibratedData(t *testing.T) {
	pts := []trainPoint{}
	// Prices that are calibrated on average: at 0.2 one in five is YES, at
	// 0.8 four in five.
	for i := 0; i < 50; i++ {
		pts = append(pts, trainPoint{price: 0.2, outcome: btoi(i%5 == 0)}, trainPoint{price: 0.8, outcome: btoi(i%5 != 0)})
	}
	a, b := fitRecalibration(pts)
	if math.Abs(a) > 0.05 || math.Abs(b-1) > 0.05 {
		t.Fatalf("calibrated data should give about the identity map, got a=%.3f b=%.3f", a, b)
	}
	// Overconfident prices (0.9 that is right only 60% of the time) get
	// pulled toward 0.5.
	pts = pts[:0]
	for i := 0; i < 100; i++ {
		pts = append(pts, trainPoint{price: 0.9, outcome: btoi(i%5 < 3)}, trainPoint{price: 0.1, outcome: btoi(i%5 >= 3)})
	}
	a, b = fitRecalibration(pts)
	if got := logistic(a + b*logit(0.9)); got > 0.75 || got < 0.5 {
		t.Fatalf("overconfident 0.9 should recalibrate toward 0.6, got %.3f", got)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestClusterBootstrapIsDeterministicAndResamplesClusters(t *testing.T) {
	recs := []scoredRec{}
	for i := 0; i < 60; i++ {
		recs = append(recs, scoredRec{item: i, cluster: "c" + string(rune('a'+i%20)), outcome: i % 2, p: map[string]float64{"m": 0.4}})
	}
	f := func(r scoredRec) float64 { return brier(r.p["m"], r.outcome) }
	a, b := mkCI(recs, 500, 7, f), mkCI(recs, 500, 7, f)
	if a != b {
		t.Fatalf("same seed must reproduce the interval: %+v vs %+v", a, b)
	}
	if !(a.Lo <= a.Mean && a.Mean <= a.Hi) {
		t.Fatalf("mean outside its interval: %+v", a)
	}
	// One cluster: no interval, encoded as null, verdict says so.
	one := []scoredRec{{cluster: "x", p: map[string]float64{"m": 0.5}}, {cluster: "x", p: map[string]float64{"m": 0.5}, outcome: 1}}
	c := mkCI(one, 500, 1, f)
	raw, err := json.Marshal(map[string]any{"ci": c})
	if err != nil {
		t.Fatalf("NaN interval must still encode: %v", err)
	}
	if !strings.Contains(string(raw), `"lo":null`) {
		t.Fatalf("undefined bound should be null, got %s", raw)
	}
	if v := verdictFor(40, c, 30); !strings.Contains(v, "no interval") {
		t.Fatalf("verdict without an interval: %q", v)
	}
	if v := verdictFor(10, ci{Mean: -0.1, Lo: -0.2, Hi: -0.05}, 30); !strings.Contains(v, "insufficient sample") {
		t.Fatalf("n below 30 must not produce a verdict: %q", v)
	}
}

func TestEvalDatasetRoundTripAndRunReport(t *testing.T) {
	db := openTestDB(t)
	items := []evalItem{}
	for i := 0; i < 70; i++ {
		items = append(items, mkEvalItem(i, float64(i+1), i%3/2, 0.3+0.4*float64(i%3/2), 0.5))
	}
	p := datasetParams{Venues: []string{"manifold"}}
	id, err := createEvalDataset(db, "p", p, evalT0)
	if err != nil {
		t.Fatal(err)
	}
	if err := finishEvalDataset(db, id, items, skipCounter{"too few traders": 3}, evalT0, nil); err != nil {
		t.Fatal(err)
	}
	dss, err := listEvalDatasets(db, "p", id)
	if err != nil || len(dss) != 1 || dss[0].Status != "ready" {
		t.Fatalf("dataset: %+v %v", dss, err)
	}
	loaded, err := loadEvalItems(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := datasetHash(loaded); got != dss[0].ContentHash {
		t.Fatalf("hash changed through storage: stored %s loaded %s", dss[0].ContentHash, got)
	}

	env := &ingestEnv{db: db, pid: "p", now: evalT0.AddDate(0, 3, 0)}
	ep := evalParams{DatasetID: id, Horizons: []float64{24}, MinTrain: 30, Blocks: 2, Bootstrap: 200, Seed: 1}
	runID, err := createEvalRun(db, "p", ep, env.now)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := executeEvalRun(env, runID, dss[0], loaded, ep)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Fatalf("report must encode: %v", err)
	}
	rep2, _ := executeEvalRun(env, runID, dss[0], loaded, ep)
	a, _ := json.Marshal(rep["backtest"])
	b, _ := json.Marshal(rep2["backtest"])
	if string(a) != string(b) {
		t.Fatal("statistical backtest must be reproducible")
	}
	// Tampering with the frozen data is detected.
	loaded[0].Outcome = 1 - loaded[0].Outcome
	if _, err := executeEvalRun(env, runID, dss[0], loaded, ep); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("modified dataset must fail the hash check, got %v", err)
	}
}

func TestAIPredictionsRespectKnowledgeAndAvailability(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	early := mkEvalItem(0, 0, 1, 0.4, 0.9) // resolves 1 Sep, before the knowledge cutoff
	early.ResolvedAt, early.ClosesAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	early.OpenedAt = early.ResolvedAt.AddDate(0, 0, -20)
	early.Prices = [][2]float64{{float64(early.ResolvedAt.AddDate(0, 0, -5).UnixMilli()), 0.4}}
	late := early
	late.Idx, late.Text = 1, "Will the Federal Reserve cut rates in November?"
	late.ResolvedAt, late.ClosesAt = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	late.OpenedAt = late.ResolvedAt.AddDate(0, 0, -20)
	late.Prices = [][2]float64{{float64(late.ResolvedAt.AddDate(0, 0, -5).UnixMilli()), 0.4}}

	cutoff := late.ResolvedAt.Add(-24 * time.Hour)
	before := cutoff.Add(-2 * time.Hour).Format("20060102T150405Z")
	after := cutoff.Add(2 * time.Hour).Format("20060102T150405Z")
	sc := &fakeSources{responses: map[string]string{
		"gdelt/doc_search": `{"articles":[{"title":"Fed officials signal a cut","url":"https://n.example/a","domain":"n.example","seendate":"` + before + `"},` +
			`{"title":"Fed cuts rates","url":"https://n.example/b","domain":"n.example","seendate":"` + after + `"},` +
			`{"title":"Undated item","url":"https://n.example/c","domain":"n.example"}]}`}}
	llm := &fakeLLM{reply: `{"direction":"yes","strength":2,"cited_evidence_ids":[1],"rationale":"Officials signalled a cut."}`}
	env := &ingestEnv{db: openTestDB(t), pid: "p", sc: sc, llm: llm, now: now}
	p := evalParams{AI: true, AIHorizon: 24, AIMaxItems: 30, AIEvidenceDays: 14, AIKnowledgeCutoff: "2026-10-15T00:00:00Z", Seed: 1}

	preds := aiPredictions(env, []evalItem{early, late}, p, now, nil)
	if e := predOf(preds, 0, "ai_shift"); e.Status != "excluded" || !strings.Contains(e.Detail, "knowledge cutoff") {
		t.Fatalf("pre-knowledge-cutoff question must be excluded, got %+v", e)
	}
	l := predOf(preds, 1, "ai_shift")
	if l.Status != "ok" || l.P == nil || *l.P <= 0.4 {
		t.Fatalf("AI forecast should shift up from the market, got %+v", l)
	}
	if len(llm.prompts) != 1 {
		t.Fatalf("one LLM call expected, got %d", len(llm.prompts))
	}
	if strings.Contains(llm.prompts[0], "Fed cuts rates") || strings.Contains(llm.prompts[0], "Undated item") {
		t.Fatal("articles first seen after the cutoff, or undated, must not reach the model")
	}
	if !strings.Contains(llm.prompts[0], "Fed officials signal a cut") {
		t.Fatal("the article seen before the cutoff should reach the model")
	}
	sum := summarizeAI([]evalItem{early, late}, preds, evalParams{AIHorizon: 24, Bootstrap: 200, Seed: 1})
	if sum["n_scored"] != 1 || !strings.Contains(sum["verdict"].(string), "insufficient sample") {
		t.Fatalf("one scored question cannot support a verdict: %+v", sum)
	}
	if _, err := json.Marshal(sum); err != nil {
		t.Fatalf("AI summary must encode: %v", err)
	}
}

func TestKalshiSeriesClustersRecurringMarkets(t *testing.T) {
	if kalshiSeries("KXHIGHNY-26NOV24") != kalshiSeries("KXHIGHNY-26NOV25") {
		t.Fatal("daily markets of one series must share a cluster")
	}
	if kalshiSeries("PLAIN") != "PLAIN" {
		t.Fatal("ticker without a date part is its own series")
	}
}
