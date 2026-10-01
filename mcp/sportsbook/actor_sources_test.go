package main

import (
	"testing"
	"time"
)

func TestNormalizeActorEventUsesExplicitNestedMap(t *testing.T) {
	s := actorSportSource{Sport: "football", CompetitionID: "premier-league", FieldMap: map[string]any{
		"external_id": "fixture.id", "competition": "league.name", "home": "teams.home.name", "away": "teams.away.name",
		"starts_at": "fixture.date", "status": "fixture.status.short", "home_score": "goals.home", "away_score": "goals.away",
	}}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	e, err := normalizeActorEvent(map[string]any{
		"fixture": map[string]any{"id": float64(42), "date": "2026-10-01T18:00:00Z", "status": map[string]any{"short": "FT"}},
		"league":  map[string]any{"name": "Cup"}, "teams": map[string]any{"home": map[string]any{"name": "A"}, "away": map[string]any{"name": "B"}},
		"goals": map[string]any{"home": float64(2), "away": float64(1)},
	}, s, day)
	if err != nil || e.ExternalID != "42" || e.Competition != "Cup" || e.Home != "A" || e.Away != "B" || e.Status != "finished" || e.HomeScore == nil || *e.HomeScore != 2 {
		t.Fatalf("unexpected normalized event: %+v, %v", e, err)
	}
}

func TestNormalizeActorEventSkipsMalformedRows(t *testing.T) {
	s := actorSportSource{Sport: "football", CompetitionID: "cup", FieldMap: map[string]any{"external_id": "id", "starts_at": "start"}}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if _, err := normalizeActorEvent(map[string]any{"start": "2026-10-01T18:00:00Z"}, s, day); err == nil {
		t.Fatal("missing external id should be rejected")
	}
	if _, err := normalizeActorEvent(map[string]any{"id": "x", "start": "2026-10-02T18:00:00Z"}, s, day); err == nil {
		t.Fatal("out-of-date row should be rejected")
	}
}

func TestActorSourceListIsProjectScoped(t *testing.T) {
	a := testApp(t)
	if err := a.initCatalog("p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO actor_sport_sources(project_id,id,name,sport,actor_id,operation,input_json,field_map_json,enabled,read_only,created_at) VALUES('p1','one','One','football',1,'fixtures','{}','{}',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	out, err := a.actorSourceList("p2")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.(map[string]any)["sources"].([]map[string]any)) != 0 {
		t.Fatal("source crossed project boundary")
	}
}
