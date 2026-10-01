package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func provider(id int64, role, slug string, defaultProvider bool) Provider {
	s, c := capabilities(role, slug)
	return Provider{ID: id, Slug: slug, Kind: "integration", Default: defaultProvider, Sports: s, Capabilities: c}
}
func TestProviderRolesAndSportOverrides(t *testing.T) {
	a := testApp(t)
	a.providers = func(_ *sdk.AppCtx, role string) ([]Provider, error) {
		switch role {
		case "sports_data":
			return []Provider{provider(1, role, "api-sports", true), provider(2, role, "api-tennis", false)}, nil
		case "odds":
			return []Provider{provider(3, role, "the-odds-api", true), provider(4, role, "api-tennis", false)}, nil
		}
		return []Provider{}, nil
	}
	ps, err := a.choose(nil, "p1", "sports_data", "tennis", 0, false)
	if err != nil || ps[0].ID != 2 {
		t.Fatalf("sport routing %v %v", ps, err)
	}
	_, err = a.choose(nil, "p1", "sports_data", "football", 3, false)
	code(t, err, "unbound_provider")
	_, err = a.choose(nil, "p1", "sports_data", "football", 2, false)
	code(t, err, "unsupported_capability")
	ps, err = a.choose(nil, "p1", "odds", "tennis", 0, true)
	if err != nil || len(ps) != 2 {
		t.Fatal("multiple odds sources")
	}
	if _, err = a.setRoute(nil, "p1", "test", map[string]any{"role": "odds", "sport": "tennis", "connection_id": int64(4)}); err != nil {
		t.Fatal(err)
	}
	ps, err = a.choose(nil, "p1", "odds", "tennis", 0, false)
	if err != nil || ps[0].ID != 4 {
		t.Fatal("override ignored")
	}
	_, err = a.choose(nil, "p1", "execution", "tennis", 0, false)
	code(t, err, "provider_unavailable")
}

func TestWildcardRouteCoverageAndRemovedProvider(t *testing.T) {
	a := testApp(t)
	ps := []Provider{provider(1, "sports_data", "api-sports", true), provider(2, "sports_data", "api-tennis", false)}
	a.providers = func(_ *sdk.AppCtx, _ string) ([]Provider, error) { return ps, nil }
	if _, err := a.setRoute(nil, "p1", "test", map[string]any{"role": "sports_data", "sport": "*", "connection_id": int64(1)}); err != nil {
		t.Fatal(err)
	}
	selected, err := a.choose(nil, "p1", "sports_data", "tennis", 0, false)
	if err != nil || selected[0].ID != 2 {
		t.Fatalf("wildcard preference should fall back outside coverage: %v %v", selected, err)
	}
	ps = ps[1:]
	_, err = a.choose(nil, "p1", "sports_data", "tennis", 0, false)
	code(t, err, "route_unavailable")
}
func TestProviderResponseContracts(t *testing.T) {
	t.Run("football", func(t *testing.T) {
		raw := json.RawMessage(`{"errors":[],"response":[{"fixture":{"id":42,"date":"2026-10-01T18:00:00Z","status":{"short":"AET"}},"league":{"name":"Cup"},"teams":{"home":{"name":"A"},"away":{"name":"B"}},"goals":{"home":3,"away":2},"score":{"fulltime":{"home":1,"away":1}}}]}`)
		events, err := parseFootball(raw)
		if err != nil || len(events) != 1 || *events[0].HomeScore != 1 {
			t.Fatal("regulation score must exclude extra time")
		}
		_, err = parseFootball(json.RawMessage(`{"errors":{"token":"invalid"},"response":[]}`))
		if err == nil {
			t.Fatal("semantic errors ignored")
		}
	})
	t.Run("sportsdb", func(t *testing.T) {
		events, err := parseSportsDB(json.RawMessage(`{"events":[{"idEvent":"a","strLeague":"League","strHomeTeam":"A","strAwayTeam":"B","strTimestamp":"2026-10-01T18:00:00Z","strStatus":"2H","intHomeScore":"2","intAwayScore":"0"}]}`))
		if err != nil || events[0].Status != "live" {
			t.Fatal("live score misclassified as final")
		}
		for _, status := range []string{"AET", "AP"} {
			raw := json.RawMessage(`{"events":[{"idEvent":"a","strTimestamp":"2026-10-01T18:00:00Z","strStatus":"` + status + `","intHomeScore":"3","intAwayScore":"2"}]}`)
			events, err = parseSportsDB(raw)
			if err != nil || events[0].Status != "finished" || events[0].HomeScore != nil || events[0].AwayScore != nil {
				t.Fatal("extra-time scores must not train regulation markets")
			}
		}
	})
	t.Run("tennis", func(t *testing.T) {
		events, err := parseTennis(json.RawMessage(`{"success":1,"result":[{"event_key":2,"event_date":"2026-10-01","event_time":"18:00","event_first_player":"A","event_second_player":"B","tournament_name":"Cup","event_status":"Finished","event_final_result":"2 - 1"}]}`))
		if err != nil || *events[0].HomeScore != 2 {
			t.Fatal(err)
		}
		events, err = addTennisOdds(events, json.RawMessage(`{"success":1,"result":{"2":{"Home/Away":{"Home":{"Book":"1.90"},"Away":{"Book":"2.05"}}}}}`), 10)
		if err != nil || len(events[0].Quotes) != 2 {
			t.Fatal("tennis odds contract")
		}
		if _, err = parseTennis(json.RawMessage(`{"success":0,"error":"invalid key"}`)); err == nil {
			t.Fatal("semantic error ignored")
		}
	})
	t.Run("odds", func(t *testing.T) {
		raw := json.RawMessage(`[{"id":"1","sport_title":"League","commence_time":"2026-10-01T18:00:00Z","home_team":"A","away_team":"B","bookmakers":[{"key":"Book","last_update":"2026-10-01T12:00:00Z","markets":[{"key":"h2h","outcomes":[{"name":"A","price":2.1},{"name":"B","price":2.2}]}]}]}]`)
		e, err := parseOddsAPI(raw, "football")
		if err != nil || len(e[0].Quotes) != 0 {
			t.Fatal("incomplete three-way odds accepted")
		}
		e, err = parseOddsAPI(raw, "tennis")
		if err != nil || len(e[0].Quotes) != 2 {
			t.Fatal("two-way tennis odds rejected")
		}
	})
}
func TestEmptySnapshotInvalidatesRemovedQuotes(t *testing.T) {
	a := testApp(t)
	now := a.clock()
	p := provider(7, "odds", "the-odds-api", true)
	date := time.Unix(now, 0).UTC().Truncate(24 * time.Hour)
	e := FeedEvent{ExternalID: "x", Sport: "tennis", Competition: "Cup", Home: "A", Away: "B", Start: now + 3600, Status: "scheduled", Quotes: []Quote{{"home", "book", 2000000, now}, {"away", "book", 2000000, now}}}
	if _, err := a.importBatch("p1", "test", p, "odds", "tennis", date, []FeedEvent{e}); err != nil {
		t.Fatal(err)
	}
	rows, err := objects(a.db, currentQuotesSQL+" WHERE o.project_id='p1'")
	if err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	if _, err = a.importBatch("p1", "test", p, "odds", "tennis", date, nil); err != nil {
		t.Fatal(err)
	}
	rows, err = objects(a.db, currentQuotesSQL+" WHERE o.project_id='p1'")
	if err != nil || len(rows) != 0 {
		t.Fatal("removed quotes remained active")
	}
	var count int
	a.db.QueryRow("SELECT COUNT(*) FROM odds_observations").Scan(&count)
	if count != 2 {
		t.Fatal("history erased")
	}
}
func TestCrossProviderIdentityAndFinishedStatus(t *testing.T) {
	a := testApp(t)
	day := time.Unix(a.clock(), 0).UTC().Truncate(24 * time.Hour)
	e := FeedEvent{ExternalID: "x", Sport: "football", Competition: "League", Home: "A", Away: "B", Start: a.clock() - 100, Status: "finished"}
	if _, err := a.importBatch("p1", "test", Provider{ID: 1, Slug: "data"}, "sports_data", "football", day, []FeedEvent{e}); err != nil {
		t.Fatal(err)
	}
	e.ExternalID = "y"
	e.Status = "scheduled"
	if _, err := a.importBatch("p1", "test", Provider{ID: 2, Slug: "odds"}, "odds", "football", day, []FeedEvent{e}); err != nil {
		t.Fatal(err)
	}
	var count int
	var status string
	a.db.QueryRow("SELECT COUNT(*),status FROM events").Scan(&count, &status)
	if count != 1 || status != "finished" {
		t.Fatalf("identity/final state %d %s", count, status)
	}
}
func TestLLMAdaptersCannotChangeForecast(t *testing.T) {
	for _, slug := range []string{"openai-api", "anthropic-api"} {
		t.Run(slug, func(t *testing.T) {
			a := testApp(t)
			_, _, _, prediction := seed(t, a)
			a.providers = func(_ *sdk.AppCtx, role string) ([]Provider, error) {
				if role == "llm" {
					return []Provider{provider(20, role, slug, true)}, nil
				}
				return nil, nil
			}
			var before string
			a.db.QueryRow("SELECT probabilities FROM predictions WHERE id=?", prediction).Scan(&before)
			a.call = func(_ context.Context, _ *sdk.AppCtx, p Provider, tool string, args map[string]any) (json.RawMessage, error) {
				if args["model"] != "test-model" {
					t.Fatal("model routing")
				}
				if slug == "openai-api" {
					if tool != "chat_completion" {
						t.Fatal(tool)
					}
					return json.RawMessage(`{"choices":[{"message":{"content":"Experimental estimate with limited evidence."}}]}`), nil
				}
				if tool != "create_message" {
					t.Fatal(tool)
				}
				return json.RawMessage(`{"content":[{"type":"text","text":"Experimental estimate with limited evidence."}]}`), nil
			}
			if _, err := a.explain(context.Background(), nil, "p1", map[string]any{"prediction_id": prediction, "model": "test-model"}); err != nil {
				t.Fatal(err)
			}
			var after string
			a.db.QueryRow("SELECT probabilities FROM predictions WHERE id=?", prediction).Scan(&after)
			if before != after {
				t.Fatal("LLM changed forecast")
			}
		})
	}
}
