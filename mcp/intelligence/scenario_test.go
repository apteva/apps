package main

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func scenarioFixture(t *testing.T) (db *sql.DB, ids map[string]int64, ev int64) {
	t.Helper()
	db = openTestDB(t)
	ids = map[string]int64{}
	for _, n := range []string{"Port", "Plant", "Maker", "Bank"} {
		e, _, err := upsertWorldEntity(db, "p", WorldEntity{Name: n, Kind: "organization"})
		if err != nil {
			t.Fatal(err)
		}
		ids[n] = e.ID
	}
	row, _ := recordEvidence(db, Evidence{Title: "Plant ships through Port", Source: "s"}, "p")
	return db, ids, row["id"].(int64)
}

func TestScenarioWeakLinksOnlyWithIncludeWeak(t *testing.T) {
	db, ids, ev := scenarioFixture(t)
	mustAssert := func(r Relationship) {
		t.Helper()
		if _, err := assertRelationship(db, "p", r); err != nil {
			t.Fatal(err)
		}
	}
	mustAssert(Relationship{From: ids["Plant"], Type: "depends_on", To: ids["Port"], Status: "reported", EvidenceIDs: []int64{ev}})                                // ungraded
	mustAssert(Relationship{From: ids["Port"], Type: "affects", To: ids["Bank"], Status: "reported", EvidenceIDs: []int64{ev}, CausalBasis: "unsupported"}) // never
	shock := []Shock{{EntityID: ids["Port"], Description: "closed"}}

	res, err := runScenario(db, "p", scenarioInput{Shocks: shock})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res["exposed_entities"].([]exposure)); n != 0 {
		t.Fatalf("ungraded and unsupported links must not carry a shock by default, got %d", n)
	}
	if len(res["unsupported_shocks"].([]map[string]any)) != 1 {
		t.Fatal("a shock with no usable link must be reported")
	}
	prof := res["evidence_profile"].(map[string]any)
	if prof["weak_links_not_used"] != 1 || prof["unsupported_links_not_used"] != 1 {
		t.Fatalf("profile: %v", prof)
	}

	res, _ = runScenario(db, "p", scenarioInput{Shocks: shock, IncludeWeak: true})
	exp := res["exposed_entities"].([]exposure)
	// 1 * 0.8 (depends_on) * 0.8 (reported) * 0.3 (ungraded) = 0.192; Bank stays out.
	if len(exp) != 1 || exp[0].Entity != "Plant" || exp[0].Impact != 0.192 || !exp[0].WeakLinks {
		t.Fatalf("include_weak: %+v", exp)
	}
	if _, err := assertRelationship(db, "p", Relationship{From: ids["Plant"], Type: "uses", To: ids["Bank"], Status: "reported", EvidenceIDs: []int64{ev}, CausalBasis: "assumed"}); err == nil {
		t.Fatal("causal_basis assumed is reserved for status assumed")
	}
}

func TestScenarioMergesDuplicateLinksAndReplays(t *testing.T) {
	db, ids, ev := scenarioFixture(t)
	row, _ := recordEvidence(db, Evidence{Title: "Port outage stops Plant deliveries", Source: "t"}, "p")
	ev2 := row["id"].(int64)
	for _, e := range []int64{ev, ev2} {
		if _, err := assertRelationship(db, "p", Relationship{From: ids["Plant"], Type: "depends_on", To: ids["Port"], Status: "reported", EvidenceIDs: []int64{e}, CausalBasis: "mechanism"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := runScenario(db, "p", scenarioInput{Name: "demo", Shocks: []Shock{{Entity: "Port", Description: "closed"}}})
	if err != nil {
		t.Fatal(err)
	}
	exp := res["exposed_entities"].([]exposure)
	if len(exp) != 1 || exp[0].Chain[1].NSources != 2 || len(exp[0].Chain[1].RelIDs) != 2 || exp[0].SingleSource {
		t.Fatalf("duplicate links must merge into one edge with two sources: %+v", exp)
	}
	id := res["id"].(int64)
	rep, err := replayScenario(db, "p", id)
	if err != nil || rep["reproduced"] != true {
		t.Fatalf("replay: %v %v", rep, err)
	}

	// A link recorded after known_at is invisible to the replay.
	late, _ := assertRelationship(db, "p", Relationship{From: ids["Port"], Type: "supplies", To: ids["Maker"], Status: "reported", EvidenceIDs: []int64{ev}, CausalBasis: "mechanism"})
	if _, err := db.Exec(`UPDATE intel_relationships SET created_at = ? WHERE id = ?`, ts(nowUTC().Add(time.Hour)), late.ID); err != nil {
		t.Fatal(err)
	}
	if rep, _ = replayScenario(db, "p", id); rep["reproduced"] != true {
		t.Fatalf("later link leaked into the replay: %v", rep)
	}
	// Regrading a link the run used breaks reproduction, and says so.
	if _, err := db.Exec(`UPDATE intel_relationships SET causal_basis = 'unsupported' WHERE rel_type = 'depends_on'`); err != nil {
		t.Fatal(err)
	}
	if rep, _ = replayScenario(db, "p", id); rep["reproduced"] != false || rep["same_graph"] != false || rep["note"] == nil {
		t.Fatalf("regrade must show as a changed graph: %v", rep)
	}
}

func TestScenarioPastAsOfDefaultsKnownAtToAsOf(t *testing.T) {
	db, ids, ev := scenarioFixture(t)
	if _, err := assertRelationship(db, "p", Relationship{From: ids["Plant"], Type: "depends_on", To: ids["Port"], Status: "reported", EvidenceIDs: []int64{ev}, CausalBasis: "mechanism"}); err != nil {
		t.Fatal(err)
	}
	res, err := runScenario(db, "p", scenarioInput{Shocks: []Shock{{EntityID: ids["Port"]}}, AsOf: nowUTC().Add(-48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res["exposed_entities"].([]exposure)) != 0 {
		t.Fatal("a past as_of must not see links recorded after it")
	}
	if res["known_at"] != res["as_of"] {
		t.Fatalf("known_at should default to as_of: %v %v", res["known_at"], res["as_of"])
	}
}

func TestSensitivityReportIsSeededAndFlagsFragilePaths(t *testing.T) {
	db, ids, ev := scenarioFixture(t)
	_, _ = assertRelationship(db, "p", Relationship{From: ids["Plant"], Type: "depends_on", To: ids["Port"], Status: "reported", EvidenceIDs: []int64{ev}, CausalBasis: "mechanism"})
	_, _ = assertRelationship(db, "p", Relationship{From: ids["Plant"], Type: "supplies", To: ids["Maker"], Status: "assumed"})
	res, _ := runScenario(db, "p", scenarioInput{Shocks: []Shock{{EntityID: ids["Port"]}}})
	id := res["id"].(int64)
	a, err := runSensitivity(db, "p", id, 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := runSensitivity(db, "p", id, 100, 7)
	ja, _ := json.Marshal(a["ranges"])
	jb, _ := json.Marshal(b["ranges"])
	if string(ja) != string(jb) {
		t.Fatal("same seed must give the same ranges")
	}
	frag := a["fragility"].([]fragRow)
	if len(frag) != 2 || frag[0].Classification != "fragile" || len(frag[0].CriticalLinks) != 1 {
		t.Fatalf("single-source single path must be fragile: %+v", frag)
	}
	oat := a["one_at_a_time"].([]oatRow)
	if len(oat) == 0 || oat[0].Source == "" {
		t.Fatal("one-at-a-time table missing")
	}
	if a["graph_matches_run"] != true {
		t.Fatal("report must use the run's graph")
	}
	stored, _ := loadScenarioReport(db, "p", id)
	if stored == nil {
		t.Fatal("report must be stored on the scenario")
	}
}

func TestHistoryTestGroupsQuestionsByModelledExposure(t *testing.T) {
	db, ids, ev := scenarioFixture(t)
	now := nowUTC().Truncate(time.Hour)
	old := now.AddDate(0, 0, -20)
	tEvent := now.AddDate(0, 0, -5)
	rel, _ := assertRelationship(db, "p", Relationship{From: ids["Port"], Type: "supplies", To: ids["Maker"], Status: "reported", EvidenceIDs: []int64{ev}, CausalBasis: "mechanism"})
	_, _ = db.Exec(`UPDATE intel_relationships SET created_at = ? WHERE id = ?`, ts(old), rel.ID)

	question := func(name string, entity int64, after float64) int64 {
		q, _, err := insertQuestion(db, "p", Question{Origin: "market", Venue: "v", ExternalID: name, Text: name, Deadline: now.AddDate(0, 1, 0), Topics: []string{}, EntityIDs: []int64{}})
		if err != nil {
			t.Fatal(err)
		}
		row, _ := recordEvidence(db, Evidence{Title: "about " + name, Source: "s"}, "p")
		e := row["id"].(int64)
		cid, err := insertClaim(db, "p", Claim{EvidenceID: e, SubjectID: &entity, Subject: name, Predicate: "x", Extractor: "test"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = db.Exec(`UPDATE intel_claims SET created_at = ? WHERE id = ?`, ts(old), cid)
		_, _ = db.Exec(`INSERT INTO intel_question_evidence (question_id, evidence_id, relevance, linked_at) VALUES (?,?,?,?)`, q.ID, e, 0.9, ts(old))
		for _, p := range []struct {
			at time.Time
			v  float64
		}{{tEvent.Add(-30 * time.Hour), 0.5}, {tEvent.Add(-time.Hour), 0.5}, {tEvent.Add(23 * time.Hour), after}} {
			_, _ = db.Exec(`INSERT INTO intel_question_prices (question_id, price, observed_at, source) VALUES (?,?,?,?)`, q.ID, p.v, ts(p.at), "test")
		}
		return q.ID
	}
	question("maker-q", ids["Maker"], 0.7)
	question("bank-q", ids["Bank"], 0.5)
	question("port-q", ids["Port"], 0.6)
	portID := ids["Port"]
	cid, _ := insertClaim(db, "p", Claim{EvidenceID: ev, SubjectID: &portID, Subject: "Port", Predicate: "closes", Extractor: "test"})
	_, _ = db.Exec(`UPDATE intel_claims SET created_at = ? WHERE id = ?`, ts(tEvent), cid)

	rep, err := runHistoryTest(db, "p", historyParams{WindowHours: 24}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep["events_used"] != 1 {
		t.Fatalf("one event expected: %v", rep["events_used"])
	}
	g := rep["groups"].(map[string]any)
	ex, ot, di := g["exposed"].(map[string]any), g["other"].(map[string]any), g["direct"].(map[string]any)
	if ex["n"] != 1 || ex["mean_abs_move_after"] != 0.2 || ot["n"] != 1 || ot["mean_abs_move_after"] != 0.0 || di["n"] != 1 {
		t.Fatalf("groups: %v", g)
	}
	if rep["verdict"].(map[string]any)["status"] != "insufficient" {
		t.Fatalf("one event cannot give a verdict: %v", rep["verdict"])
	}
	if latest, err := latestHistoryTest(db, "p"); err != nil || latest == nil {
		t.Fatalf("history test must be stored: %v", err)
	}
}

func TestGradeRelationshipsAppliesGradeToEveryRowOfALink(t *testing.T) {
	db, ids, ev := scenarioFixture(t)
	for i := 0; i < 2; i++ {
		_, _ = assertRelationship(db, "p", Relationship{From: ids["Port"], Type: "affects", To: ids["Bank"], Status: "reported", EvidenceIDs: []int64{ev}})
	}
	_, _ = assertRelationship(db, "p", Relationship{From: ids["Plant"], Type: "uses", To: ids["Port"], Status: "reported", EvidenceIDs: []int64{ev}})
	llm := &fakeLLM{reply: `{"grades":[{"n":1,"basis":"co_occurrence","note":"only mentioned together"},{"n":2,"basis":"mechanism","note":"ships through"}]}`}
	out, err := gradeRelationships(db, "p", llm, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out["links_graded"] != 2 || len(llm.prompts) != 1 {
		t.Fatalf("grading: %v", out)
	}
	prof, _ := causalProfile(db, "p")
	if prof["co_occurrence"] != 2 || prof["mechanism"] != 1 || prof["ungraded"] != 0 {
		t.Fatalf("profile: %v", prof)
	}
}
