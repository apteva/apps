package main

import (
	"database/sql"
	"encoding/json"
	"math"
	"sort"
)

func (a *App) predict(project, actor, market string) (any, error) {
	now := a.clock()
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sport, home, away, status, competition string
	var start int64
	var example int
	err = tx.QueryRow(`SELECT e.sport,e.home,e.away,e.status,e.starts_at,e.example,e.competition FROM markets m JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE m.project_id=? AND m.id=?`, project, market).Scan(&sport, &home, &away, &status, &start, &example, &competition)
	if err == sql.ErrNoRows {
		return nil, fail("not_found", 404, "Market not found")
	}
	if err != nil {
		return nil, err
	}
	if status != "scheduled" || start <= now {
		return nil, fail("event_closed", 409, "Predictions require a future pre-match event")
	}
	if sport != "football" && sport != "tennis" {
		return nil, fail("unsupported_model", 409, "No prediction model for this sport")
	}
	history, err := objects(tx, `SELECT * FROM (SELECT id,home,away,home_score,away_score,starts_at,received_at,source,competition FROM events WHERE project_id=? AND example=? AND sport=? AND (?='tennis' OR competition=?) AND status='finished' AND home_score IS NOT NULL AND away_score IS NOT NULL AND starts_at<? AND received_at<=? ORDER BY starts_at DESC,id DESC LIMIT 5000) ORDER BY starts_at,id`, project, example, sport, sport, competition, now, now)
	if err != nil {
		return nil, err
	}
	ratings := map[string]float64{}
	counts := map[string]int{}
	draws := 0
	training := []map[string]any{}
	for _, e := range history {
		h := identity(e["home"].(string))
		a := identity(e["away"].(string))
		hs, as := e["home_score"].(int64), e["away_score"].(int64)
		if sport == "tennis" && hs == as {
			continue
		}
		rh, ok := ratings[h]
		if !ok {
			rh = 1500
		}
		ra, ok := ratings[a]
		if !ok {
			ra = 1500
		}
		advantage := 0.0
		if sport == "football" {
			advantage = 60
		}
		expected := 1 / (1 + math.Pow(10, (ra-rh-advantage)/400))
		result := 0.5
		if hs > as {
			result = 1
		} else if hs < as {
			result = 0
		} else {
			draws++
		}
		ratings[h] = rh + 24*(result-expected)
		ratings[a] = ra - 24*(result-expected)
		counts[h]++
		counts[a]++
		training = append(training, e)
	}
	probabilities := map[string]float64{}
	model := "elo-v1-experimental"
	features := map[string]any{"sport": sport, "home": home, "away": away, "training_events": training, "home_samples": counts[identity(home)], "away_samples": counts[identity(away)], "k": 24, "calibrated": false, "note": "Experimental historical Elo; no claimed predictive advantage. Football uses a smoothed historical draw rate; tennis is not surface-adjusted."}
	expires := now + quoteTTL
	if start < expires {
		expires = start
	}
	if counts[identity(home)] >= 5 && counts[identity(away)] >= 5 {
		rh, ra := ratings[identity(home)], ratings[identity(away)]
		advantage := 0.0
		if sport == "football" {
			advantage = 60
		}
		win := 1 / (1 + math.Pow(10, (ra-rh-advantage)/400))
		features["home_rating"] = rh
		features["away_rating"] = ra
		features["home_advantage"] = advantage
		if sport == "football" {
			draw := math.Max(.10, math.Min(.40, float64(draws+5)/float64(len(training)+20)))
			probabilities["draw"] = draw
			probabilities["home"] = (1 - draw) * win
			probabilities["away"] = (1 - draw) * (1 - win)
		} else {
			probabilities["home"] = win
			probabilities["away"] = 1 - win
		}
	} else {
		quotes, err := objects(tx, currentQuotesSQL+` WHERE o.project_id=? AND o.market_id=? AND o.observed_at>=? AND o.observed_at<=? ORDER BY o.received_at DESC,o.id DESC`, project, market, now-quoteTTL, now+30)
		if err != nil {
			return nil, err
		}
		probabilities, err = consensus(quotes, sport)
		if err != nil {
			return nil, err
		}
		model = "bookmaker-baseline-v1"
		features["quotes"] = quotes
		features["note"] = "Insufficient historical samples: this is margin-adjusted bookmaker consensus, not an independent AI forecast."
		// A baseline cannot outlive the quotes used to produce it.
		for _, q := range quotes {
			if end := q["observed_at"].(int64) + quoteTTL; end < expires {
				expires = end
			}
		}
	}
	id := newID()
	_, err = tx.Exec(`INSERT INTO predictions VALUES(?,?,?,?,?,?,?,?)`, project, id, market, model, jsonText(probabilities), jsonText(features), now, expires)
	if err != nil {
		return nil, err
	}
	if err = audit(tx, project, actor, "prediction.created", id, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "market_id": market, "model": model, "probabilities": probabilities, "features": features, "created_at": now, "expires_at": expires}, nil
}
func consensus(quotes []map[string]any, sport string) (map[string]float64, error) {
	groups := map[string]map[string]float64{}
	book := map[string]string{}
	order := []string{}
	for _, q := range quotes {
		group := jsonText([]any{q["source"], q["connection_id"], q["bookmaker"], q["snapshot_id"]})
		if groups[group] == nil {
			groups[group] = map[string]float64{}
			order = append(order, group)
			book[group] = q["bookmaker"].(string)
		}
		groups[group][q["selection"].(string)] = 1000000 / float64(q["odds_micros"].(int64))
	}
	used := map[string]bool{}
	values := map[string][]float64{}
	required := []string{"home", "away"}
	if sport == "football" {
		required = append(required, "draw")
	}
	for _, key := range order {
		g := groups[key]
		if used[book[key]] {
			continue
		}
		sum := 0.0
		valid := true
		for _, s := range required {
			if g[s] <= 0 {
				valid = false
			}
			sum += g[s]
		}
		if !valid {
			continue
		}
		used[book[key]] = true
		for _, s := range required {
			values[s] = append(values[s], g[s]/sum)
		}
	}
	if len(used) == 0 {
		return nil, fail("insufficient_evidence", 409, "Import at least five historical results per participant, or fresh complete bookmaker odds")
	}
	out := map[string]float64{}
	sum := 0.0
	for _, s := range required {
		v := values[s]
		sort.Float64s(v)
		median := v[len(v)/2]
		if len(v)%2 == 0 {
			median = (v[len(v)/2-1] + median) / 2
		}
		out[s] = median
		sum += median
	}
	for s := range out {
		out[s] /= sum
	}
	return out, nil
}
func decodeProbabilities(raw string) (map[string]float64, error) {
	out := map[string]float64{}
	err := json.Unmarshal([]byte(raw), &out)
	return out, err
}
