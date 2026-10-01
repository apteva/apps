package main

import (
	"context"
	"database/sql"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"path/filepath"
	"testing"
	"time"
)

func configureSport(t *testing.T, a *App, project, sport, profile, model string) {
	t.Helper()
	if _, err := a.catalogWrite(nil, project, "test", "sport_upsert", map[string]any{"sport": sport, "name": sport, "enabled": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.catalogWrite(nil, project, "test", "sport_market_set", map[string]any{"sport": sport, "outcome_profile": profile, "rules": "match_completed", "prediction_model": model, "history_scope": "competition", "home_advantage": int64(0), "enabled": true}); err != nil {
		t.Fatal(err)
	}
}
func TestConfigurableSportFromMappingThroughPaperSettlement(t *testing.T) {
	a := testApp(t)
	configureSport(t, a, "p1", "basketball", "two_way", "baseline")
	for _, tool := range []string{"competition_upsert", "provider_sport_mapping_set"} {
		args := map[string]any{"sport": "basketball", "competition_id": "nba", "name": "NBA", "role": "odds", "provider_slug": "the-odds-api", "external_key": "basketball_nba", "enabled": true}
		if _, err := a.catalogWrite(nil, "p1", "test", tool, args); err != nil {
			t.Fatal(err)
		}
	}
	a.providers = func(_ *sdk.AppCtx, role string) ([]Provider, error) {
		return []Provider{provider(4, role, "the-odds-api", true)}, nil
	}
	called := false
	a.call = func(_ context.Context, _ *sdk.AppCtx, p Provider, tool string, args map[string]any) (json.RawMessage, error) {
		called = true
		if tool != "get_odds" || args["sport"] != "basketball_nba" {
			t.Fatalf("wrong provider call: %s %v", tool, args)
		}
		return json.RawMessage(`[{"id":"nba-1","sport_key":"basketball_nba","sport_title":"Provider NBA name","commence_time":"2026-10-01T18:00:00Z","home_team":"A","away_team":"B","bookmakers":[{"key":"Book","last_update":"2026-10-01T12:00:00Z","markets":[{"key":"h2h","outcomes":[{"name":"A","price":2.1},{"name":"B","price":1.9}]}]}]}]`), nil
	}
	out, err := a.sync(context.Background(), nil, "p1", "test", "odds_sync", map[string]any{"sport": "basketball", "competition_id": "nba", "date": "2026-10-01"})
	if err != nil {
		t.Fatal(err)
	}
	if !called || !out.(map[string]any)["sources"].([]map[string]any)[0]["success"].(bool) {
		t.Fatalf("sync failed: %v", out)
	}
	var market, comp string
	var quote int64
	if err = a.db.QueryRow(`SELECT m.id,e.competition_id FROM markets m JOIN events e ON e.id=m.event_id AND e.project_id=m.project_id WHERE m.project_id='p1'`).Scan(&market, &comp); err != nil {
		t.Fatal(err)
	}
	if comp != "nba" {
		t.Fatal("competition not normalized")
	}
	prediction, err := a.predict("p1", "test", market)
	if err != nil {
		t.Fatal(err)
	}
	if prediction.(map[string]any)["model"] != "bookmaker-baseline-v1" {
		t.Fatal("wrong model")
	}
	b, err := a.createBankroll("p1", "test", map[string]any{"name": "Paper", "currency": "EUR", "initial_minor": int64(100000)})
	if err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow(`SELECT id FROM odds_observations WHERE selection='home'`).Scan(&quote)
	proposalID := proposal(t, a, b.(map[string]any)["id"].(string), quote, prediction.(map[string]any)["id"].(string), 1000)
	bet, err := a.accept("p1", "test", proposalID)
	if err != nil {
		t.Fatal(err)
	}
	// Catalog disables new exposure, but existing obligations still settle.
	if _, err = a.catalogWrite(nil, "p1", "test", "sport_upsert", map[string]any{"sport": "basketball", "name": "Basketball", "enabled": false}); err != nil {
		t.Fatal(err)
	}
	_, err = a.predict("p1", "test", market)
	code(t, err, "sport_disabled")
	_, err = a.propose("p1", "test", map[string]any{"bankroll_id": b.(map[string]any)["id"], "prediction_id": prediction.(map[string]any)["id"], "quote_id": quote, "stake_minor": int64(1000), "rationale": "A valid research rationale"})
	code(t, err, "catalog_disabled")
	if _, err = a.settle("p1", "test", map[string]any{"bet_id": bet.(map[string]any)["id"], "outcome": "won", "note": "Manual winning settlement"}); err != nil {
		t.Fatal(err)
	}
	cash, locked, err := balances(a.db, "p1", b.(map[string]any)["id"].(string))
	if err != nil || cash != 101100 || locked != 0 {
		t.Fatalf("ledger %d %d %v", cash, locked, err)
	}
	if _, err = a.catalog("p2"); err != nil {
		t.Fatal(err)
	}
	var count int
	a.db.QueryRow(`SELECT COUNT(*) FROM sports WHERE project_id='p2' AND id='basketball'`).Scan(&count)
	if count != 0 {
		t.Fatal("catalog leaked across projects")
	}
}
func TestMarketProfilesAreFrozenAndDisabledEntriesBlockAcceptance(t *testing.T) {
	a := testApp(t)
	bank, market, quote, pred := seed(t, a)
	proposalID := proposal(t, a, bank, quote, pred, 1000)
	if _, err := a.catalogWrite(nil, "p1", "test", "sport_market_set", map[string]any{"sport": "football", "outcome_profile": "two_way", "rules": "match_completed", "prediction_model": "baseline", "history_scope": "sport", "enabled": true}); err != nil {
		t.Fatal(err)
	}
	var profile, rules, model string
	a.db.QueryRow(`SELECT outcome_profile,rules,prediction_model FROM markets WHERE id=?`, market).Scan(&profile, &rules, &model)
	if profile != "three_way" || rules != "regulation" || model != "elo" {
		t.Fatal("existing market settings changed")
	}
	p, err := a.predict("p1", "test", market)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(map[string]any)["probabilities"].(map[string]float64)["draw"]; !ok {
		t.Fatal("snapshot semantics lost")
	}
	if _, err = a.catalogWrite(nil, "p1", "test", "sport_upsert", map[string]any{"sport": "football", "name": "Football", "enabled": false}); err != nil {
		t.Fatal(err)
	}
	_, err = a.accept("p1", "test", proposalID)
	code(t, err, "catalog_disabled")
	// Lazy initialization must never re-enable an explicitly disabled entry.
	if err = a.initCatalog("p1"); err != nil {
		t.Fatal(err)
	}
	_, err = a.sync(context.Background(), nil, "p1", "test", "sports_sync", map[string]any{"sport": "football", "date": "2026-10-01"})
	code(t, err, "sport_disabled")
}
func TestCompetitionSnapshotIsolationAndOutcomeValidation(t *testing.T) {
	a := testApp(t)
	configureSport(t, a, "p1", "hockey", "three_way", "baseline")
	day := time.Unix(a.clock(), 0).UTC().Truncate(24 * time.Hour)
	p := Provider{ID: 9, Slug: "the-odds-api"}
	for _, league := range []string{"east", "west"} {
		e := FeedEvent{ExternalID: league, Sport: "hockey", Competition: league, Home: "A", Away: "B", Status: "scheduled", Start: a.clock() + 3600, Quotes: []Quote{{"home", "book", 2500000, a.clock()}, {"draw", "book", 3000000, a.clock()}, {"away", "book", 2500000, a.clock()}}}
		if _, err := a.importBatch("p1", "test", p, "odds", "hockey", day, []FeedEvent{e}, league); err != nil {
			t.Fatal(err)
		}
	}
	var events int
	a.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events)
	if events != 2 {
		t.Fatal("different competitions merged")
	}
	if _, err := a.importBatch("p1", "test", p, "odds", "hockey", day, nil, "east"); err != nil {
		t.Fatal(err)
	}
	rows, err := objects(a.db, currentQuotesSQL+` WHERE o.project_id='p1'`)
	if err != nil || len(rows) != 3 {
		t.Fatalf("other competition quotes invalidated: %v %d", err, len(rows))
	}
	raw := json.RawMessage(`[{"id":"x","commence_time":"2026-10-01T18:00:00Z","home_team":"A","away_team":"B","bookmakers":[{"key":"book","last_update":"2026-10-01T12:00:00Z","markets":[{"key":"h2h","outcomes":[{"name":"A","price":2.1},{"name":"Draw","price":3},{"name":"B","price":2.2}]}]}]}]`)
	parsed, err := parseOddsAPIProfile(raw, "hockey", "two_way")
	if err != nil || len(parsed[0].Quotes) != 0 {
		t.Fatal("three-way prices silently converted to two-way")
	}
	parsed, err = parseOddsAPIProfile(raw, "hockey", "three_way")
	if err != nil || len(parsed[0].Quotes) != 3 {
		t.Fatal("generic three-way quotes rejected")
	}
}
func TestCatalogAdapterAndProjectConstraints(t *testing.T) {
	a := testApp(t)
	configureSport(t, a, "p1", "basketball", "two_way", "baseline")
	_, err := a.catalogWrite(nil, "p1", "test", "provider_sport_mapping_set", map[string]any{"sport": "basketball", "role": "sports_data", "provider_slug": "api-sports", "external_key": "12", "enabled": true})
	code(t, err, "unsupported_adapter")
	_, err = a.catalogWrite(nil, "p2", "test", "competition_upsert", map[string]any{"sport": "basketball", "competition_id": "nba", "name": "NBA", "enabled": true})
	code(t, err, "unknown_sport")
	_, err = a.catalogWrite(nil, "p1", "test", "sport_upsert", map[string]any{"sport": "Invalid Sport", "name": "X", "enabled": true})
	code(t, err, "invalid_sport")
	_, err = a.catalogWrite(nil, "p1", "test", "sport_upsert", map[string]any{"sport": "basketball", "name": "X"})
	code(t, err, "invalid_enabled")
	raw := json.RawMessage(`[{"id":"x","commence_time":"2026-10-01T10:00:00Z","home_team":"A","away_team":"B","completed":true,"scores":[{"name":"A","score":"102"},{"name":"B","score":"99"}]}]`)
	events, err := parseOddsScores(raw, "basketball", "regulation")
	if err != nil || events[0].HomeScore != nil {
		t.Fatal("final totals used as regulation scores")
	}
	events, err = parseOddsScores(raw, "basketball", "match_completed")
	if err != nil || *events[0].HomeScore != 102 {
		t.Fatal("completed-match scores missing")
	}
}
func TestV01MigrationPreservesExistingPaperBet(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	raw, _ := embedded.ReadFile("migrations/001_init.sql")
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO events VALUES('p1','e','football','Legacy league','A','B',1790884800,'scheduled',NULL,NULL,'old','x',1,1790856000,0)`,
		`INSERT INTO markets VALUES('p1','m','e','match_winner','regulation')`,
		`INSERT INTO odds_observations VALUES(7,'p1','m','home','Book',2000000,'old',1,1790856000,1790856000,'s')`,
		`INSERT INTO quote_heads VALUES('p1','m','old',1,'s')`,
		`INSERT INTO predictions VALUES('p1','pred','m','old','{"home":0.4,"draw":0.3,"away":0.3}','{}',1790856000,1790856900)`,
		`INSERT INTO bankrolls VALUES('p1','b','Legacy','EUR',100000,200,1000,0,1790856000)`,
		`INSERT INTO proposals VALUES('p1','p','b','pred',7,1000,0.4,-0.2,'Legacy rationale','accepted',1790856000,1790856900)`,
		`INSERT INTO bets VALUES('p1','bet','p','b',1000,2000000,'open',1790856000,NULL,'')`,
		`INSERT INTO ledger_entries(project_id,bankroll_id,transaction_id,account,amount_minor,created_at) VALUES('p1','b','seed','cash',99000,1790856000),('p1','b','seed','locked',1000,1790856000),('p1','b','seed','equity',-100000,1790856000)`,
	}
	for _, s := range statements {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ = embedded.ReadFile("migrations/002_catalog.sql")
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	a := &App{db: db}
	if err = a.initCatalog("p1"); err != nil {
		t.Fatal(err)
	}
	var profile, comp string
	if err = db.QueryRow(`SELECT m.outcome_profile,e.competition_id FROM markets m JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id`).Scan(&profile, &comp); err != nil {
		t.Fatal(err)
	}
	if profile != "three_way" || comp == "" {
		t.Fatal("legacy catalog migration failed")
	}
	if _, err = a.settle("p1", "test", map[string]any{"bet_id": "bet", "outcome": "won", "note": "Settled after upgrade"}); err != nil {
		t.Fatal(err)
	}
	cash, locked, err := balances(db, "p1", "b")
	if err != nil || cash != 101000 || locked != 0 {
		t.Fatal("legacy accounting changed")
	}
}

func TestAtomicSportSaveAndProviderDiscovery(t *testing.T) {
	a := testApp(t)
	args := map[string]any{"sport": "baseball", "name": "Baseball", "enabled": true, "outcome_profile": "two_way", "rules": "match_completed", "prediction_model": "baseline", "history_scope": "competition", "home_advantage": 1.5, "market_enabled": true}
	_, err := a.catalogWrite(nil, "p1", "test", "sport_upsert", args)
	code(t, err, "invalid_market_config")
	var count int
	a.db.QueryRow(`SELECT COUNT(*) FROM sports WHERE project_id='p1' AND id='baseball'`).Scan(&count)
	if count != 0 {
		t.Fatal("invalid market settings left a partially created sport")
	}
	args["home_advantage"] = float64(0)
	if _, err = a.catalogWrite(nil, "p1", "test", "sport_upsert", args); err != nil {
		t.Fatal(err)
	}
	if _, err = marketConfig(a.db, "p1", "baseball"); err != nil {
		t.Fatal(err)
	}
	a.providers = func(_ *sdk.AppCtx, role string) ([]Provider, error) {
		return []Provider{provider(8, role, "the-odds-api", true)}, nil
	}
	a.call = func(_ context.Context, _ *sdk.AppCtx, p Provider, tool string, args map[string]any) (json.RawMessage, error) {
		if tool != "list_sports" || args["all"] != true {
			t.Fatal("incorrect discovery request")
		}
		return json.RawMessage(`[{"key":"baseball_mlb","title":"MLB","group":"Baseball","active":true}]`), nil
	}
	rows, err := a.discoverSports(context.Background(), nil, "p1", 8)
	if err != nil || len(rows.([]map[string]any)) != 1 {
		t.Fatal(err)
	}
	_, err = a.discoverSports(context.Background(), nil, "p1", 9)
	code(t, err, "unbound_provider")
	a.call = func(context.Context, *sdk.AppCtx, Provider, string, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"error":"No access"}`), nil
	}
	_, err = a.discoverSports(context.Background(), nil, "p1", 8)
	code(t, err, "invalid_provider_catalog")
}
func TestMappedTennisCompetitionUsesFilteredFixtures(t *testing.T) {
	a := testApp(t)
	a.call = func(_ context.Context, _ *sdk.AppCtx, p Provider, tool string, args map[string]any) (json.RawMessage, error) {
		if tool == "list_fixtures" {
			if args["tournament_key"] != int64(12) {
				t.Fatalf("missing tournament filter: %v", args)
			}
			return json.RawMessage(`{"success":1,"result":[{"event_key":2,"event_date":"2026-10-01","event_time":"18:00","event_first_player":"A","event_second_player":"B","tournament_name":"Cup","event_status":"Scheduled"}]}`), nil
		}
		if tool != "get_odds" {
			t.Fatal(tool)
		}
		return json.RawMessage(`{"success":1,"result":{"2":{"Home/Away":{"Home":{"Book":"1.90"},"Away":{"Book":"2.05"}}}}}`), nil
	}
	cfg := MarketConfig{Profile: "two_way", Rules: "match_completed"}
	events, err := a.fetch(context.Background(), nil, Provider{ID: 1, Slug: "api-tennis"}, "odds", "tennis", time.Unix(a.clock(), 0).UTC().Truncate(24*time.Hour), "12", cfg)
	if err != nil || len(events) != 1 || len(events[0].Quotes) != 2 {
		t.Fatal(err)
	}
}
