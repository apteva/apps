package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

type fakeSources struct {
	responses map[string]string // "slug/tool" → JSON
	bound     map[string]bool
	calls     []string
}

func (f *fakeSources) call(slug, tool string, args map[string]any) (json.RawMessage, bool) {
	key := slug + "/" + tool
	f.calls = append(f.calls, key)
	if id := strArgM(args, "slug"); id != "" {
		if r, ok := f.responses[key+"/"+id]; ok {
			return json.RawMessage(r), true
		}
	}
	if id := strArgM(args, "ticker"); id != "" {
		if r, ok := f.responses[key+"/"+id]; ok {
			return json.RawMessage(r), true
		}
	}
	r, ok := f.responses[key]
	return json.RawMessage(r), ok
}
func (f *fakeSources) boundSlugs(c []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range c {
		out[s] = f.bound[s]
	}
	return out
}

type fakeLLM struct {
	reply   string
	prompts []string
}

func (f *fakeLLM) Complete(system, user string, maxTokens int) (string, error) {
	f.prompts = append(f.prompts, user)
	return f.reply, nil
}
func (f *fakeLLM) Model() string { return "test/model" }

func TestEvidenceDedupIgnoresObservationTime(t *testing.T) {
	db := openTestDB(t)
	a, b := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	e := Evidence{Kind: "news", Title: "Port strike announced", Source: "example.com", SourceRef: "https://example.com/a"}
	e.ObservedAt = &a
	_, first, err := recordEvidenceDetailed(db, e, "p")
	if err != nil || !first {
		t.Fatalf("first insert: %v %v", first, err)
	}
	e.ObservedAt = &b
	row, second, err := recordEvidenceDetailed(db, e, "p")
	if err != nil {
		t.Fatal(err)
	}
	if second || row["duplicate"] != true {
		t.Fatalf("re-fetch with a new observation time must be a duplicate, got inserted=%v", second)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM intelligence_evidence`).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}
}

func TestIngestedKnowledgeCannotBeBackdated(t *testing.T) {
	db := openTestDB(t)
	backdated := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := recordEvidence(db, Evidence{Title: "Late arrival", Source: "s", ObservedAt: &backdated}, "p"); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	observed, _ := searchEvidence(db, EvidenceQuery{ProjectID: "p", AsOf: &cutoff})
	ingested, _ := searchEvidence(db, EvidenceQuery{ProjectID: "p", AsOf: &cutoff, Knowledge: "ingested"})
	if len(observed) != 1 || len(ingested) != 0 {
		t.Fatalf("observed clock should trust the caller (1), ingested clock should not (0): got %d, %d", len(observed), len(ingested))
	}
}

func TestEntityPolicyAndMerging(t *testing.T) {
	db := openTestDB(t)
	if _, _, err := upsertWorldEntity(db, "p", WorldEntity{Name: "Jane Doe", Kind: "person"}); err == nil || !strings.Contains(err.Error(), "out of scope") {
		t.Fatalf("person entities must be refused, got %v", err)
	}
	role, _, err := upsertWorldEntity(db, "p", WorldEntity{Name: "Fed Chair", Kind: "public_role"})
	if err != nil || role.Kind != "public_role" {
		t.Fatalf("public roles allowed: %v", err)
	}
	a, created, err := upsertWorldEntity(db, "p", WorldEntity{Name: "Federal Reserve", Kind: "institution", Aliases: []string{"Fed"}, ExternalIDs: map[string]string{"wikidata": "Q53536"}})
	if err != nil || !created {
		t.Fatal(err)
	}
	b, created, err := upsertWorldEntity(db, "p", WorldEntity{Name: "fed", Kind: "concept"})
	if err != nil || created || b.ID != a.ID {
		t.Fatalf("alias should resolve to existing entity: created=%v id=%d want %d err=%v", created, b.ID, a.ID, err)
	}
	c, _, _ := upsertWorldEntity(db, "p", WorldEntity{Name: "US central bank", Kind: "institution", ExternalIDs: map[string]string{"wikidata": "Q53536"}})
	if c.ID != a.ID || !containsFold(c.Aliases, "US central bank") {
		t.Fatalf("external id match should merge alias: %+v", c)
	}
}

func TestRelationshipStatusNeedsEvidence(t *testing.T) {
	db := openTestDB(t)
	x, _, _ := upsertWorldEntity(db, "p", WorldEntity{Name: "Plant A", Kind: "facility"})
	y, _, _ := upsertWorldEntity(db, "p", WorldEntity{Name: "Chip B", Kind: "product"})
	if _, err := assertRelationship(db, "p", Relationship{From: x.ID, To: y.ID, Type: "produces", Status: "observed"}); err == nil {
		t.Fatal("observed relationship without evidence must be refused")
	}
	if _, err := assertRelationship(db, "p", Relationship{From: x.ID, To: y.ID, Type: "produces", Status: "assumed"}); err != nil {
		t.Fatalf("assumed relationship allowed: %v", err)
	}
}

func marketFixtures(now time.Time) *fakeSources {
	soon := now.Add(72 * time.Hour).Format(time.RFC3339)
	tooSoon := now.Add(2 * time.Hour).Format(time.RFC3339)
	poly := fmt.Sprintf(`[
	 {"question":"Will the Federal Reserve cut rates in December?","slug":"fed-cut-dec","description":"Resolves YES if the FOMC lowers the target range.","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.62\",\"0.38\"]","volumeNum":500000,"endDate":%q,"closed":false},
	 {"question":"Thin market","slug":"thin","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.5\",\"0.5\"]","volumeNum":10,"endDate":%q},
	 {"question":"Closes too soon","slug":"too-soon","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.5\",\"0.5\"]","volumeNum":900000,"endDate":%q},
	 {"question":"Who wins?","slug":"multi","outcomes":"[\"A\",\"B\"]","outcomePrices":"[\"0.5\",\"0.5\"]","volumeNum":900000,"endDate":%q},
	 {"question":"Will a CEO post 300-319 tweets this week?","slug":"tweets","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.4\",\"0.6\"]","volumeNum":900000,"endDate":%q},
	 {"question":"Will the IPO happen by Friday?","slug":"near-certain","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.995\",\"0.005\"]","volumeNum":900000,"endDate":%q}]`, soon, soon, tooSoon, soon, soon, soon)
	kalshi := fmt.Sprintf(`{"markets":[{"ticker":"KXCPI-26NOV","title":"Will CPI exceed 3%%?","yes_sub_title":"Above 3.0%%","rules_primary":"Resolves on BLS CPI release.","market_type":"binary","yes_bid_dollars":"0.4000","yes_ask_dollars":"0.4400","volume_fp":"900.00","status":"active","close_time":%q}]}`, soon)
	manifold := fmt.Sprintf(`[{"id":"mf1","question":"Will SpaceX launch Starship again?","url":"https://manifold.markets/x/starship","probability":0.7,"volume":5000,"outcomeType":"BINARY","isResolved":false,"closeTime":%d}]`, now.Add(96*time.Hour).UnixMilli())
	return &fakeSources{responses: map[string]string{
		"polymarket/markets_window":       poly,
		"kalshi/list_markets":             kalshi,
		"manifold-markets/search_markets": manifold,
		"gdelt/doc_search":                `{"articles":[{"title":"Federal Reserve officials signal December cut","url":"https://news.example/fed","domain":"news.example","seendate":"20261118T120000Z"},{"title":"Unrelated sports result","url":"https://news.example/sport","domain":"news.example"}]}`,
		"bls/get_series_single":           `{"Results":{"series":[{"data":[{"year":"2026","period":"M10","value":"321.5"},{"year":"2026","period":"M09","value":"320.9"},{"year":"2026","period":"M13","value":"1"}]}]}}`,
	}}
}

func setupSources(t *testing.T, env *ingestEnv) {
	t.Helper()
	for _, s := range defaultSources() {
		if err := ensureSource(env.db, env.pid, s); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPredictionMarketImportPricesAndResolution(t *testing.T) {
	db := openTestDB(t)
	now := nowUTC()
	fs := marketFixtures(now)
	env := &ingestEnv{db: db, pid: "p", sc: fs, now: now}
	setupSources(t, env)
	runs, err := runDueSources(env, true, "prediction-markets")
	if err != nil {
		t.Fatal(err)
	}
	if runs[0]["error"] != nil {
		t.Fatalf("run error: %v", runs[0]["error"])
	}
	qs, _ := listQuestions(db, "p", "open", 100)
	if len(qs) != 3 {
		var names []string
		for _, q := range qs {
			names = append(names, q.ExternalID)
		}
		t.Fatalf("want fed-cut-dec, KXCPI and mf1 (thin/too-soon/multi/personal/near-certain filtered), got %v", names)
	}
	var fed Question
	for _, q := range qs {
		if q.ExternalID == "fed-cut-dec" {
			fed = q
		}
	}
	if fed.ResolutionRule == "" || len(fed.Topics) == 0 || len(fed.EntityIDs) != 0 {
		t.Fatalf("question should carry rule and topics; topics must not become entities: %+v", fed)
	}
	var ents int
	_ = db.QueryRow(`SELECT COUNT(*) FROM entities`).Scan(&ents)
	if ents != 0 {
		t.Fatalf("market import must not create entities from question text, got %d", ents)
	}
	if p, _ := priceAt(db, fed.ID, now); p == nil || *p != 0.62 {
		t.Fatalf("price not recorded: %v", p)
	}
	var kalshiPriceVal *float64
	for _, q := range qs {
		if q.Venue == "kalshi" {
			kalshiPriceVal, _ = priceAt(db, q.ID, now)
		}
	}
	if kalshiPriceVal == nil || *kalshiPriceVal != 0.42 {
		t.Fatalf("kalshi mid price want 0.42, got %v", kalshiPriceVal)
	}

	// Second run: the Fed market no longer lists (closed) and resolves YES.
	fs.responses["polymarket/markets_window"] = `[]`
	fs.responses["polymarket/market_price/fed-cut-dec"] = `[{"question":"Will the Federal Reserve cut rates in December?","slug":"fed-cut-dec","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"1\",\"0\"]","closed":true,"umaResolutionStatus":"resolved"}]`
	env.now = now.Add(time.Minute)
	if _, err := runDueSources(env, true, "prediction-markets"); err != nil {
		t.Fatal(err)
	}
	q, _ := getQuestion(db, "p", fed.ID)
	if q.Status != "resolved" || q.Outcome == nil || *q.Outcome != 1 {
		t.Fatalf("expected resolved YES, got %s %v", q.Status, q.Outcome)
	}
}

func TestNewsLinksEvidenceToQuestions(t *testing.T) {
	db := openTestDB(t)
	now := nowUTC()
	fs := marketFixtures(now)
	env := &ingestEnv{db: db, pid: "p", sc: fs, now: now}
	setupSources(t, env)
	if _, err := runDueSources(env, true, "prediction-markets"); err != nil {
		t.Fatal(err)
	}
	runs, err := runDueSources(env, true, "news")
	if err != nil || runs[0]["error"] != nil {
		t.Fatalf("news run: %v %v", err, runs)
	}
	qs, _ := listQuestions(db, "p", "open", 100)
	var fed Question
	for _, q := range qs {
		if q.ExternalID == "fed-cut-dec" {
			fed = q
		}
	}
	ids, _ := questionEvidenceIDs(db, "p", fed.ID, nowUTC().Add(time.Second), 10)
	if len(ids) == 0 {
		t.Fatal("expected evidence linked to the Fed question")
	}
	ev, _ := searchEvidence(db, EvidenceQuery{ProjectID: "p", IDs: ids[:1], Limit: 1})
	if !strings.Contains(ev[0]["title"].(string), "Federal Reserve") || ev[0]["published_time"] != "2026-11-18T12:00:00Z" {
		t.Fatalf("most relevant evidence should be the Fed headline with GDELT date, got %v", ev[0])
	}
	gdelt := 0
	for _, c := range fs.calls {
		if c == "gdelt/doc_search" {
			gdelt++
		}
	}
	if gdelt != 1 {
		t.Fatalf("questions should share one batched GDELT query, got %d calls", gdelt)
	}
	// Re-running finds the same articles: duplicates, not new rows.
	runs, _ = runDueSources(env, true, "news")
	if runs[0]["inserted"].(int) != 0 || runs[0]["duplicates"].(int) == 0 {
		t.Fatalf("second news run should only see duplicates: %v", runs[0])
	}
}

func TestBLSSeriesBecomeStateValues(t *testing.T) {
	db := openTestDB(t)
	env := &ingestEnv{db: db, pid: "p", sc: marketFixtures(nowUTC()), now: nowUTC()}
	setupSources(t, env)
	runs, err := runDueSources(env, true, "bls")
	if err != nil || runs[0]["error"] != nil {
		t.Fatalf("%v %v", err, runs)
	}
	ents, _ := searchWorldEntities(db, "p", "CPI", "indicator", 5)
	if len(ents) != 1 || ents[0].ExternalIDs["bls"] != "CUUR0000SA0" {
		t.Fatalf("indicator entity: %+v", ents)
	}
	series, _ := stateSeries(db, "p", ents[0].ID, "value", 10)
	if len(series) != 2 || series[0]["value"].(float64) != 321.5 || series[0]["valid_at"] != "2026-10-01T00:00:00Z" {
		t.Fatalf("state series (M13 annual average excluded): %v", series)
	}
}

func seedQuestion(t *testing.T, db interface{}, env *ingestEnv, price *float64) *Question {
	t.Helper()
	q, _, err := insertQuestion(env.db, env.pid, Question{Origin: "manual", Text: "Will Acme Corp announce a recall?", ResolutionRule: "YES if Acme publishes a recall notice.",
		ResolutionSource: "acme.example/notices", Deadline: env.now.Add(5 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if price != nil {
		if err := addQuestionPrice(env.db, q.ID, *price, env.now.Add(-time.Minute), "test"); err != nil {
			t.Fatal(err)
		}
	}
	return q
}

func TestForecastAbstainsWithoutInputs(t *testing.T) {
	db := openTestDB(t)
	env := &ingestEnv{db: db, pid: "p", now: nowUTC()}
	q := seedQuestion(t, nil, env, nil)
	f, err := forecastQuestion(env, q, "initial")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Abstained || f.Probability != nil {
		t.Fatalf("expected abstention, got %+v", f)
	}
}

func TestForecastShiftRequiresValidCitations(t *testing.T) {
	db := openTestDB(t)
	env := &ingestEnv{db: db, pid: "p", now: nowUTC()}
	price := 0.30
	q := seedQuestion(t, nil, env, &price)
	row, _, err := recordEvidenceDetailed(db, Evidence{Kind: "news", Title: "Acme Corp recalls 1m units", Source: "news.example"}, "p")
	if err != nil {
		t.Fatal(err)
	}
	evID := row["id"].(int64)
	if _, err := linkQuestionEvidence(db, q.ID, evID, 1); err != nil {
		t.Fatal(err)
	}
	env.now = nowUTC().Add(time.Second)

	env.llm = &fakeLLM{reply: `{"direction":"yes","strength":2,"cited_evidence_ids":[999],"rationale":"made up"}`}
	f, err := forecastQuestion(env, q, "initial")
	if err != nil {
		t.Fatal(err)
	}
	if f.Adjustment != 0 || *f.Probability != 0.3 {
		t.Fatalf("uncited shift must be ignored: adj=%v p=%v", f.Adjustment, *f.Probability)
	}

	env.llm = &fakeLLM{reply: "Here you go:\n```json\n" + fmt.Sprintf(`{"direction":"yes","strength":2,"cited_evidence_ids":[%d],"rationale":"Recall reported."}`, evID) + "\n```"}
	f2, err := forecastQuestion(env, q, "new_evidence")
	if err != nil {
		t.Fatal(err)
	}
	want := round3(logistic(logit(0.3) + 0.4))
	if f2.Revision != 2 || f2.Adjustment != 0.4 || *f2.Probability != want || !containsID(f2.EvidenceIDs, evID) {
		t.Fatalf("expected revision 2 with +0.4 log-odds (p=%v), got rev=%d adj=%v p=%v", want, f2.Revision, f2.Adjustment, *f2.Probability)
	}
	if !strings.Contains(f2.Explanation, "Recall reported") {
		t.Fatalf("explanation should carry the rationale: %s", f2.Explanation)
	}

	// The recorded inputs replay and verify.
	rep, err := replayForecast(db, "p", f2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rep["snapshot_verified"] != true || rep["evidence_set_reproduced"] != true || rep["market_price_reproduced"] != true {
		t.Fatalf("replay should verify: %v", rep)
	}
	// Evidence arriving after the cutoff must not change the replay.
	late, _, _ := recordEvidenceDetailed(db, Evidence{Kind: "news", Title: "Acme recall widened", Source: "later.example"}, "p")
	// Timestamps are second-precision; place its arrival clearly after the cutoff.
	later := ts(f2.EvidenceCutoff.Add(time.Hour))
	_, _ = db.Exec(`UPDATE intelligence_evidence SET ingested_at = ? WHERE id = ?`, later, late["id"].(int64))
	_, _ = linkQuestionEvidence(db, q.ID, late["id"].(int64), 1)
	_, _ = db.Exec(`UPDATE intel_question_evidence SET linked_at = ? WHERE evidence_id = ?`, later, late["id"].(int64))
	rep, _ = replayForecast(db, "p", f2.ID)
	if rep["snapshot_verified"] != true || rep["evidence_set_reproduced"] != true {
		t.Fatalf("late evidence leaked into replay: %v", rep)
	}
}

func TestUpdateForecastsTriggers(t *testing.T) {
	db := openTestDB(t)
	env := &ingestEnv{db: db, pid: "p", now: nowUTC()}
	price := 0.5
	q := seedQuestion(t, nil, env, &price)
	counts, err := updateForecasts(env, 0)
	if err != nil || counts["initial"] != 1 {
		t.Fatalf("initial: %v %v", counts, err)
	}
	counts, _ = updateForecasts(env, 0)
	if counts["initial"]+counts["market_move"]+counts["new_evidence"] != 0 {
		t.Fatalf("no reason to revise: %v", counts)
	}
	_ = addQuestionPrice(db, q.ID, 0.58, env.now, "test")
	env.now = env.now.Add(time.Second)
	counts, _ = updateForecasts(env, 0)
	if counts["market_move"] != 1 {
		t.Fatalf("market move should revise: %v", counts)
	}
}

func TestLedgerScoresOneForecastPerQuestionWithoutLookahead(t *testing.T) {
	db := openTestDB(t)
	deadline := nowUTC().Add(-time.Hour)
	q, _, err := insertQuestion(db, "p", Question{Origin: "manual", Text: "Will X happen?", ResolutionRule: "r", ResolutionSource: "s", Deadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(p, market float64, at time.Time) {
		f := &Forecast{QuestionID: q.ID, Probability: &p, MarketPrice: &market, Prior: &market, EvidenceCutoff: at, SnapshotHash: "h", Model: "m", ModelVersion: "v", Trigger: "t", CreatedAt: at}
		if err := insertForecast(db, "p", f); err != nil {
			t.Fatal(err)
		}
	}
	mk(0.6, 0.5, deadline.Add(-72*time.Hour)) // early
	mk(0.8, 0.7, deadline.Add(-30*time.Hour)) // latest locked (>=24h before deadline) → scored
	mk(0.95, 0.9, deadline.Add(-2*time.Hour)) // inside lock window
	one := 1
	if err := resolveQuestionRow(db, "p", q.ID, "resolved", &one, "observed", deadline); err != nil {
		t.Fatal(err)
	}
	mk(0.99, 0.99, deadline.Add(30*time.Minute)) // after resolution: never scored

	l, err := buildLedger(db, "p", 24)
	if err != nil {
		t.Fatal(err)
	}
	scored := l["scored"].([]scoredForecast)
	if len(scored) != 1 || scored[0].Probability != 0.8 || scored[0].Revision != 2 {
		t.Fatalf("want revision 2 (p=0.8) scored once, got %+v", scored)
	}
	s := l["summary"].(map[string]any)
	if math.Abs(s["brier_model"].(float64)-0.04) > 1e-9 || math.Abs(s["brier_market"].(float64)-0.09) > 1e-9 || s["skill_vs_market"].(float64) <= 0 {
		t.Fatalf("scores: %v", s)
	}
}

func TestScenarioPropagatesWithAssumptionChain(t *testing.T) {
	db := openTestDB(t)
	port, _, _ := upsertWorldEntity(db, "p", WorldEntity{Name: "Port of Rotterdam", Kind: "infrastructure"})
	plant, _, _ := upsertWorldEntity(db, "p", WorldEntity{Name: "Chip plant", Kind: "facility"})
	maker, _, _ := upsertWorldEntity(db, "p", WorldEntity{Name: "Carmaker", Kind: "organization"})
	ev, _ := recordEvidence(db, Evidence{Title: "Plant ships via Rotterdam", Source: "s"}, "p")
	if _, err := assertRelationship(db, "p", Relationship{From: plant.ID, Type: "depends_on", To: port.ID, Status: "reported", EvidenceIDs: []int64{ev["id"].(int64)}, CausalBasis: "mechanism"}); err != nil {
		t.Fatal(err)
	}
	if _, err := assertRelationship(db, "p", Relationship{From: plant.ID, Type: "supplies", To: maker.ID, Status: "assumed"}); err != nil {
		t.Fatal(err)
	}
	q, _, _ := insertQuestion(db, "p", Question{Origin: "manual", Text: "Will Carmaker cut output?", ResolutionRule: "r", ResolutionSource: "s", Deadline: nowUTC().Add(48 * time.Hour), EntityIDs: []int64{maker.ID}, Topics: []string{}})
	res, err := runScenario(db, "p", scenarioInput{Name: "port closure", Shocks: []Shock{{Entity: "Port of Rotterdam", Description: "closed 5 days", Severity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	exp := res["exposed_entities"].([]exposure)
	if len(exp) == 2 && (exp[0].Chain[1].CausalBasis != "mechanism" || exp[0].Chain[1].NSources != 1 || exp[1].Chain[2].CausalBasis != "assumed") {
		t.Fatalf("chain must carry causal basis and sources: %+v", exp)
	}
	if len(exp) != 2 || exp[0].Entity != "Chip plant" || exp[1].Entity != "Carmaker" || !exp[1].Assumed || len(exp[1].Chain) != 3 {
		t.Fatalf("propagation: %+v", exp)
	}
	// plant: 1 * 0.8 (depends_on) * 0.8 (reported) * 1 (mechanism) = 0.64; maker: 0.64 * 0.7 * 0.5 (assumed) = 0.224
	if exp[0].Impact != 0.64 || exp[1].Impact != 0.224 {
		t.Fatalf("impacts: %v %v", exp[0].Impact, exp[1].Impact)
	}
	qs := res["exposed_questions"].([]map[string]any)
	if len(qs) != 1 || qs[0]["question_id"] != q.ID || res["hypothetical"] != true {
		t.Fatalf("exposed questions: %v", qs)
	}
	res2, _ := runScenario(db, "p", scenarioInput{Shocks: []Shock{{EntityID: port.ID, Description: "x"}}, SkipAssumed: true})
	if len(res2["exposed_entities"].([]exposure)) != 1 {
		t.Fatal("skip_assumed should stop at the assumed link")
	}
}

func TestQuestionTopics(t *testing.T) {
	got := questionTopics("Will the Federal Reserve cut rates in December 2026?")
	if len(got) == 0 || got[0] != "Federal Reserve" {
		t.Fatalf("topics: %v", got)
	}
	got = questionTopics("will bitcoin close above 100k?")
	if len(got) == 0 || got[0] != "bitcoin" {
		t.Fatalf("fallback topics: %v", got)
	}
}

func TestExtractionRecordsClaimsAndReportedLinks(t *testing.T) {
	db := openTestDB(t)
	env := &ingestEnv{db: db, pid: "p", now: nowUTC()}
	setupSources(t, env)
	_ = ensureSource(db, "p", IntelSource{Slug: "extraction", Connector: "extraction", Enabled: true, IntervalSec: 300})
	row, _, _ := recordEvidenceDetailed(db, Evidence{Kind: "news", Title: "TSMC supplies Apple with 2nm chips; CEO Jane Doe comments", Source: "n"}, "p")
	id := row["id"].(int64)
	env.llm = &fakeLLM{reply: fmt.Sprintf(`{"items":[{"evidence_id":%d,"entities":[{"name":"TSMC","kind":"organization"},{"name":"Apple","kind":"organization"},{"name":"Jane Doe","kind":"person"}],
	 "claims":[{"subject":"TSMC","predicate":"supplies","object":"Apple"}],"relationships":[{"from":"TSMC","type":"supplies","to":"Apple"},{"from":"Jane Doe","type":"operates","to":"TSMC"}]}]}`, id)}
	counts, err := extractClaims(env, 1)
	if err != nil {
		t.Fatal(err)
	}
	if counts["entities"] != 2 || counts["claims"] != 1 || counts["relationships"] != 1 {
		t.Fatalf("person must be dropped; got %v", counts)
	}
	tsmc, _ := findWorldEntity(db, "p", "TSMC", nil)
	rels, _ := relationshipsAt(db, "p", []int64{tsmc.ID}, nil)
	if len(rels) != 1 || rels[0].Status != "reported" || rels[0].EvidenceIDs[0] != id {
		t.Fatalf("relationship should be reported with evidence: %+v", rels)
	}
	// Cursor advanced: a second pass has nothing to do.
	env.llm.(*fakeLLM).prompts = nil
	_, _ = extractClaims(env, 1)
	if len(env.llm.(*fakeLLM).prompts) != 0 {
		t.Fatal("extraction should not re-process evidence")
	}
}

func TestMarketEventContextCapAndWalletExclusion(t *testing.T) {
	db := openTestDB(t)
	now := nowUTC()
	soon := ts(now.Add(72 * time.Hour))
	mk := func(slug, q string) string {
		return fmt.Sprintf(`{"question":%q,"slug":%q,"outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.4\",\"0.6\"]","volumeNum":900000,"endDate":%q,"events":[{"slug":"bra-arg","title":"Brazil vs. Argentina"}]}`, q, slug, soon)
	}
	poly := "[" + mk("s1", "Exact Score: 1-0?") + "," + mk("s2", "Exact Score: 2-1?") + "," + mk("s3", "Exact Score: Any Other Score?") + "," +
		fmt.Sprintf(`{"question":"Will 0xAbCdEf1234 sell before Friday?","slug":"wallet","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.4\",\"0.6\"]","volumeNum":900000,"endDate":%q}`, soon) + "]"
	kalshi := fmt.Sprintf(`{"markets":[{"ticker":"KXF1-HAM","event_ticker":"KXF1-26LV","title":"Lewis Hamilton wins","market_type":"binary","yes_bid_dollars":"0.10","yes_ask_dollars":"0.12","volume_fp":"900","status":"active","close_time":%q}]}`, soon)
	fs := &fakeSources{responses: map[string]string{
		"polymarket/markets_window": poly,
		"kalshi/list_markets":       kalshi,
		"kalshi/get_event":          `{"event":{"title":"Las Vegas Grand Prix winner"}}`,
	}}
	env := &ingestEnv{db: db, pid: "p", sc: fs, now: now}
	setupSources(t, env)
	if _, err := runDueSources(env, true, "prediction-markets"); err != nil {
		t.Fatal(err)
	}
	qs, _ := listQuestions(db, "p", "open", 100)
	texts := []string{}
	perEvent := 0
	for _, q := range qs {
		texts = append(texts, q.Text)
		if q.EventKey == "bra-arg" {
			perEvent++
			if !strings.HasPrefix(q.Text, "Brazil vs. Argentina: Exact Score: ") {
				t.Fatalf("polymarket question should carry its event title: %q", q.Text)
			}
		}
		if strings.Contains(q.Text, "0x") {
			t.Fatalf("wallet-address market should be excluded: %q", q.Text)
		}
	}
	if perEvent != 2 {
		t.Fatalf("max_per_event 2 should cap the event, got %d: %v", perEvent, texts)
	}
	if !containsString(texts, "Las Vegas Grand Prix winner: Lewis Hamilton wins") {
		t.Fatalf("kalshi question should carry its event title: %v", texts)
	}
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestWithEventTitle(t *testing.T) {
	cases := map[[2]string]string{
		{"Exact Score: Any Other Score?", "Lazio vs. Lecce - Exact Score"}:                            "Lazio vs. Lecce - Exact Score: Any Other Score?",
		{"Will Bitcoin reach $70,000 November 16-22?", "What price will Bitcoin hit November 16-22?"}: "Will Bitcoin reach $70,000 November 16-22?",
		{"Will Puma Shen win the next Taipei Mayor election?", "Taipei Mayor Election Winner"}:        "Will Puma Shen win the next Taipei Mayor election?",
		{"Lewis Hamilton wins", "Las Vegas Grand Prix winner"}:                                        "Las Vegas Grand Prix winner: Lewis Hamilton wins",
		{"Main Race: Lando Norris wins", "Las Vegas Grand Prix Main Race Winner"}:                     "Las Vegas Grand Prix Main Race Winner: Lando Norris wins",
		{"Chicago wins", "Chicago at Denver"}:                                                         "Chicago at Denver: Chicago wins",
	}
	for in, want := range cases {
		if got := withEventTitle(in[0], in[1]); got != want {
			t.Errorf("withEventTitle(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	if questionTemplate("Bitcoin above $68,000?") != questionTemplate("Bitcoin above $70,000?") {
		t.Error("ladder markets should share a template")
	}
}

// Evidence stored during a cycle must be inside the cutoff of the forecast
// it triggers in that same cycle.
func TestForecastAfterIngestSeesThisCyclesEvidence(t *testing.T) {
	db := openTestDB(t)
	start := nowUTC().Truncate(time.Second)
	clock := start
	prev := nowUTC
	nowUTC = func() time.Time { return clock }
	defer func() { nowUTC = prev }()

	env := &ingestEnv{db: db, pid: "p", now: start}
	price := 0.3
	q := seedQuestion(t, nil, env, &price)
	if counts, err := updateForecasts(env, 0); err != nil || counts["initial"] != 1 {
		t.Fatalf("initial: %v %v", counts, err)
	}

	// The cycle starts, then ingestion takes a while before news arrives.
	env.now = start.Add(time.Minute)
	clock = env.now.Add(13 * time.Second)
	row, _, err := recordEvidenceDetailed(db, Evidence{Kind: "news", Title: "Acme announces recall", Source: "news.example", Basis: basisObserved, Access: accessOpen}, "p")
	if err != nil {
		t.Fatal(err)
	}
	evID := row["id"].(int64)
	if _, err := linkQuestionEvidence(db, q.ID, evID, 1); err != nil {
		t.Fatal(err)
	}
	env.llm = &fakeLLM{reply: fmt.Sprintf(`{"direction":"yes","strength":1,"cited_evidence_ids":[%d],"rationale":"Recall announced."}`, evID)}

	counts, err := forecastAfterIngest(env, 5)
	if err != nil || counts["new_evidence"] != 1 {
		t.Fatalf("new evidence should revise: %v %v", counts, err)
	}
	fs, err := forecastsFor(db, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := fs[len(fs)-1]
	if !containsID(last.EvidenceIDs, evID) || last.Adjustment == 0 {
		t.Fatalf("forecast should use the evidence that triggered it: ids=%v adj=%v cutoff=%s", last.EvidenceIDs, last.Adjustment, last.EvidenceCutoff)
	}
}
