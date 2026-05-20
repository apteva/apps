package main

import (
	"encoding/json"
	"testing"
)

// mockSource returns canned JSON per (slug, tool) and reports a fixed
// bound set. Lets us exercise the gateway end-to-end with no live
// integrations.
type mockSource struct {
	responses map[string]json.RawMessage // "slug:tool" → data
	bound     map[string]bool
}

func (m *mockSource) call(slug, tool string, _ map[string]any) (json.RawMessage, bool) {
	if r, ok := m.responses[slug+":"+tool]; ok {
		return r, true
	}
	return nil, false
}
func (m *mockSource) boundSlugs(cands []string) map[string]bool {
	out := map[string]bool{}
	for _, c := range cands {
		out[c] = m.bound[c]
	}
	return out
}

func TestGwProbability_OddsDevig(t *testing.T) {
	// the-odds-api response: Alcaraz vs Sinner, three books, h2h decimal.
	oddsResp := `[{
		"home_team":"Carlos Alcaraz","away_team":"Jannik Sinner",
		"bookmakers":[
			{"key":"pinnacle","markets":[{"key":"h2h","outcomes":[
				{"name":"Carlos Alcaraz","price":1.85},{"name":"Jannik Sinner","price":2.05}]}]},
			{"key":"betfair","markets":[{"key":"h2h","outcomes":[
				{"name":"Carlos Alcaraz","price":1.83},{"name":"Jannik Sinner","price":2.08}]}]}
		]}]`
	sc := &mockSource{responses: map[string]json.RawMessage{
		"the-odds-api:get_odds": json.RawMessage(oddsResp),
	}}

	res := gwProbability(sc, map[string]any{"entity": "Alcaraz", "domain": "tennis", "sport": "tennis_atp"})
	if res.Method != "odds_devig" {
		t.Fatalf("method = %q, want odds_devig", res.Method)
	}
	if res.Books != 2 {
		t.Errorf("books = %d, want 2", res.Books)
	}
	// Alcaraz is the favorite (lower decimal odds) → fair prob > 0.5.
	if res.FairProb <= 0.50 || res.FairProb >= 0.60 {
		t.Errorf("fair prob out of expected band: %.4f", res.FairProb)
	}
}

func TestGwProbability_Unavailable(t *testing.T) {
	// No sources bound → graceful "unavailable", not an error.
	sc := &mockSource{responses: map[string]json.RawMessage{}}
	res := gwProbability(sc, map[string]any{"entity": "Alcaraz", "domain": "tennis"})
	if res.Method != "unavailable" {
		t.Errorf("expected unavailable, got %q", res.Method)
	}
}

func TestGwContext_MergesAndDedups(t *testing.T) {
	// Two news sources, one overlapping headline → deduped to one.
	tavily := `{"results":[
		{"title":"Alcaraz reaches French Open final","url":"http://a"},
		{"title":"Sinner battles into final","url":"http://b"}]}`
	newsapi := `{"articles":[
		{"title":"Alcaraz reaches French Open final","url":"http://a2","source":{"name":"Reuters"}},
		{"title":"Clay court preview: Alcaraz vs Sinner","url":"http://c","source":{"name":"ESPN"}}]}`
	sc := &mockSource{responses: map[string]json.RawMessage{
		"tavily:search":      json.RawMessage(tavily),
		"newsapi:everything": json.RawMessage(newsapi),
	}}

	res := gwContext(sc, "Alcaraz Sinner French Open")
	// 3 unique titles (one dup removed across the two sources).
	if len(res.Items) != 3 {
		t.Errorf("expected 3 deduped items, got %d: %+v", len(res.Items), res.Items)
	}
	if len(res.Sources) != 2 {
		t.Errorf("expected 2 contributing sources, got %d", len(res.Sources))
	}
}

func TestGwStats_FansAndRecordsProvenance(t *testing.T) {
	sc := &mockSource{responses: map[string]json.RawMessage{
		"api-sports:tennis_rankings":   json.RawMessage(`{"Alcaraz":{"rank":2}}`),
		"tennis-abstract:recent_form":  json.RawMessage(`{"win_rate":0.9}`),
	}}
	res := gwStats(sc, "Carlos Alcaraz", "tennis")
	if _, ok := res.Data["ranking"]; !ok {
		t.Error("expected ranking data")
	}
	if _, ok := res.Data["form"]; !ok {
		t.Error("expected form data")
	}
	if len(res.Sources) == 0 {
		t.Error("expected provenance sources recorded")
	}
}

func TestGwSourcesStatus(t *testing.T) {
	sc := &mockSource{bound: map[string]bool{"the-odds-api": true, "fred": true}}
	out := gwSourcesStatus(sc)
	rows, _ := out["sources"].([]map[string]any)
	if len(rows) == 0 {
		t.Fatal("expected source rows")
	}
	boundCount := 0
	for _, r := range rows {
		if r["bound"].(bool) {
			boundCount++
		}
	}
	if boundCount != 2 {
		t.Errorf("expected 2 bound sources, got %d", boundCount)
	}
}
