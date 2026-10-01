package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Quote struct {
	Selection string
	Bookmaker string
	Odds      int64
	Observed  int64
}
type FeedEvent struct {
	ExternalID, Sport, Competition, Home, Away, Status string
	Start                                              int64
	HomeScore, AwayScore                               *int
	Quotes                                             []Quote
	Example                                            bool
}

func parseTime(s string) (int64, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("invalid event timestamp")
}
func score(s string) *int {
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return nil
	}
	return &v
}
func parseSportsDB(raw json.RawMessage) ([]FeedEvent, error) {
	var body struct {
		Events []struct {
			ID        string  `json:"idEvent"`
			League    string  `json:"strLeague"`
			Home      string  `json:"strHomeTeam"`
			Away      string  `json:"strAwayTeam"`
			Timestamp string  `json:"strTimestamp"`
			Date      string  `json:"dateEvent"`
			Time      string  `json:"strTime"`
			Status    string  `json:"strStatus"`
			HomeScore *string `json:"intHomeScore"`
			AwayScore *string `json:"intAwayScore"`
		} `json:"events"`
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || keys["events"] == nil {
		return nil, malformed()
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil, malformed()
	}
	out := []FeedEvent{}
	for _, r := range body.Events {
		stamp := r.Timestamp
		if stamp == "" {
			stamp = r.Date + " " + r.Time
		}
		start, err := parseTime(stamp)
		if err != nil {
			continue
		}
		e := FeedEvent{ExternalID: r.ID, Sport: "football", Competition: r.League, Home: r.Home, Away: r.Away, Start: start, Status: "scheduled"}
		// Do not settle from non-null scores alone: live fixtures also have scores.
		switch strings.ToLower(r.Status) {
		case "match finished", "ft", "aet", "ap":
			e.Status = "finished"
		case "match postponed", "cancelled", "canceled":
			e.Status = "cancelled"
		case "1h", "2h", "ht", "live", "in progress":
			e.Status = "live"
		}
		if r.HomeScore != nil {
			e.HomeScore = score(*r.HomeScore)
		}
		if r.AwayScore != nil {
			e.AwayScore = score(*r.AwayScore)
		}
		// This feed does not expose separate regulation scores for extra-time
		// or shootout results, so exclude them from regulation-market training.
		if status := strings.ToLower(r.Status); status == "aet" || status == "ap" {
			e.HomeScore, e.AwayScore = nil, nil
		}
		out = append(out, e)
	}
	return out, nil
}
func parseFootball(raw json.RawMessage) ([]FeedEvent, error) {
	var body struct {
		Errors   json.RawMessage `json:"errors"`
		Response []struct {
			Fixture struct {
				ID     int64  `json:"id"`
				Date   string `json:"date"`
				Status struct {
					Short string `json:"short"`
				} `json:"status"`
			} `json:"fixture"`
			League struct {
				Name string `json:"name"`
			} `json:"league"`
			Teams struct {
				Home struct {
					Name string `json:"name"`
				} `json:"home"`
				Away struct {
					Name string `json:"name"`
				} `json:"away"`
			} `json:"teams"`
			Goals struct {
				Home *int `json:"home"`
				Away *int `json:"away"`
			} `json:"goals"`
			Score struct {
				Fulltime struct {
					Home *int `json:"home"`
					Away *int `json:"away"`
				} `json:"fulltime"`
			} `json:"score"`
		} `json:"response"`
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || keys["response"] == nil {
		return nil, malformed()
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil, malformed()
	}
	if string(body.Errors) != "" && string(body.Errors) != "[]" && string(body.Errors) != "{}" && string(body.Errors) != "null" {
		return nil, malformed()
	}
	out := []FeedEvent{}
	for _, r := range body.Response {
		start, err := parseTime(r.Fixture.Date)
		if err != nil {
			continue
		}
		e := FeedEvent{ExternalID: strconv.FormatInt(r.Fixture.ID, 10), Sport: "football", Competition: r.League.Name, Home: r.Teams.Home.Name, Away: r.Teams.Away.Name, Start: start, Status: "scheduled", HomeScore: r.Goals.Home, AwayScore: r.Goals.Away}
		switch r.Fixture.Status.Short {
		case "FT", "AET", "PEN":
			e.Status = "finished"
			e.HomeScore = r.Score.Fulltime.Home
			e.AwayScore = r.Score.Fulltime.Away
		case "CANC", "PST", "ABD", "AWD", "WO":
			e.Status = "cancelled"
		case "1H", "HT", "2H", "ET", "BT", "P", "LIVE", "SUSP", "INT":
			e.Status = "live"
		}
		out = append(out, e)
	}
	return out, nil
}
func parseTennis(raw json.RawMessage) ([]FeedEvent, error) {
	var body struct {
		Success int `json:"success"`
		Result  []struct {
			ID          int64  `json:"event_key"`
			Date        string `json:"event_date"`
			Time        string `json:"event_time"`
			Home        string `json:"event_first_player"`
			Away        string `json:"event_second_player"`
			Competition string `json:"tournament_name"`
			Status      string `json:"event_status"`
			Final       string `json:"event_final_result"`
			Live        string `json:"event_live"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Success != 1 {
		return nil, malformed()
	}
	out := []FeedEvent{}
	for _, r := range body.Result {
		start, err := parseTime(r.Date + " " + r.Time)
		if err != nil {
			continue
		}
		e := FeedEvent{ExternalID: strconv.FormatInt(r.ID, 10), Sport: "tennis", Competition: r.Competition, Home: r.Home, Away: r.Away, Start: start, Status: "scheduled"}
		if r.Live == "1" {
			e.Status = "live"
		}
		switch strings.ToLower(r.Status) {
		case "finished":
			e.Status = "finished"
		case "cancelled", "canceled", "retired", "walkover":
			e.Status = "cancelled"
		}
		parts := strings.Split(r.Final, "-")
		if len(parts) == 2 {
			e.HomeScore = score(strings.TrimSpace(parts[0]))
			e.AwayScore = score(strings.TrimSpace(parts[1]))
		}
		out = append(out, e)
	}
	return out, nil
}
func parseOddsAPI(raw json.RawMessage, sport string) ([]FeedEvent, error) {
	var body []struct {
		ID         string `json:"id"`
		SportTitle string `json:"sport_title"`
		Start      string `json:"commence_time"`
		Home       string `json:"home_team"`
		Away       string `json:"away_team"`
		Books      []struct {
			Key     string `json:"key"`
			Updated string `json:"last_update"`
			Markets []struct {
				Key      string `json:"key"`
				Updated  string `json:"last_update"`
				Outcomes []struct {
					Name  string  `json:"name"`
					Price float64 `json:"price"`
				} `json:"outcomes"`
			} `json:"markets"`
		} `json:"bookmakers"`
	}
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &body) != nil {
		return nil, malformed()
	}
	out := []FeedEvent{}
	for _, r := range body {
		start, err := parseTime(r.Start)
		if err != nil {
			continue
		}
		e := FeedEvent{ExternalID: r.ID, Sport: sport, Competition: r.SportTitle, Home: r.Home, Away: r.Away, Start: start, Status: "scheduled"}
		for _, b := range r.Books {
			for _, m := range b.Markets {
				if m.Key != "h2h" {
					continue
				}
				stamp := m.Updated
				if stamp == "" {
					stamp = b.Updated
				}
				observed, err := parseTime(stamp)
				if err != nil {
					continue
				}
				batch := []Quote{}
				for _, o := range m.Outcomes {
					selection := ""
					switch o.Name {
					case r.Home:
						selection = "home"
					case r.Away:
						selection = "away"
					case "Draw":
						if sport == "football" {
							selection = "draw"
						}
					}
					odds, err := parseOdds(strconv.FormatFloat(o.Price, 'f', 6, 64))
					if err != nil || selection == "" {
						continue
					}
					batch = append(batch, Quote{selection, b.Key, odds, observed})
				}
				if completeQuotes(batch, sport) {
					e.Quotes = append(e.Quotes, batch...)
				}
			}
		}
		out = append(out, e)
	}
	return out, nil
}
func addTennisOdds(events []FeedEvent, raw json.RawMessage, received int64) ([]FeedEvent, error) {
	var body struct {
		Success int                                                `json:"success"`
		Result  map[string]map[string]map[string]map[string]string `json:"result"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Success != 1 {
		return nil, malformed()
	}
	for i := range events {
		books := map[string][]Quote{}
		market := body.Result[events[i].ExternalID]["Home/Away"]
		for outcome, prices := range market {
			selection := ""
			switch outcome {
			case "Home":
				selection = "home"
			case "Away":
				selection = "away"
			}
			if selection == "" {
				continue
			}
			for book, price := range prices {
				odds, err := parseOdds(price)
				if err == nil {
					books[book] = append(books[book], Quote{selection, book, odds, received})
				}
			}
		}
		for _, qs := range books {
			if completeQuotes(qs, "tennis") {
				events[i].Quotes = append(events[i].Quotes, qs...)
			}
		}
	}
	return events, nil
}
func completeQuotes(qs []Quote, sport string) bool {
	seen := map[string]bool{}
	for _, q := range qs {
		if seen[q.Selection] {
			return false
		}
		seen[q.Selection] = true
	}
	return seen["home"] && seen["away"] && (sport != "football" || seen["draw"])
}
func identity(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
func upsertEvent(tx *sql.Tx, project string, p Provider, e FeedEvent, now int64) (string, string, error) {
	if e.ExternalID == "" || len(e.ExternalID) > 200 || e.Home == "" || e.Away == "" || e.Home == e.Away || len(e.Home) > 200 || len(e.Away) > 200 || len(e.Competition) > 200 || e.Start <= 0 {
		return "", "", fmt.Errorf("invalid event")
	}
	if e.Status != "scheduled" && e.Status != "live" && e.Status != "finished" && e.Status != "cancelled" {
		return "", "", fmt.Errorf("invalid event status")
	}
	ex := 0
	if e.Example {
		ex = 1
	}
	id := ""
	err := tx.QueryRow(`SELECT event_id FROM event_aliases WHERE project_id=? AND source=? AND connection_id=? AND external_id=?`, project, p.Slug, p.ID, e.ExternalID).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return "", "", err
	}
	if id == "" {
		// Conservative identity matching: exact normalized participants, same
		// sport, same minute, unique candidate. Ambiguity stays separate.
		candidates, err := objects(tx, `SELECT id,home,away FROM events WHERE project_id=? AND example=? AND sport=? AND starts_at BETWEEN ? AND ?`, project, ex, e.Sport, e.Start-60, e.Start+60)
		if err != nil {
			return "", "", err
		}
		matches := []string{}
		for _, c := range candidates {
			if identity(c["home"].(string)) == identity(e.Home) && identity(c["away"].(string)) == identity(e.Away) {
				matches = append(matches, c["id"].(string))
			}
		}
		if len(matches) == 1 {
			id = matches[0]
		} else {
			id = newID()
		}
	}
	_, err = tx.Exec(`INSERT INTO events(project_id,id,sport,competition,home,away,starts_at,status,home_score,away_score,source,external_id,connection_id,received_at,example) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,id) DO UPDATE SET starts_at=excluded.starts_at,status=CASE WHEN events.status IN ('finished','cancelled') AND excluded.status IN ('scheduled','live') THEN events.status ELSE excluded.status END,home_score=CASE WHEN events.status IN ('finished','cancelled') AND excluded.status IN ('scheduled','live') THEN events.home_score WHEN excluded.status='finished' THEN excluded.home_score ELSE COALESCE(excluded.home_score,events.home_score) END,away_score=CASE WHEN events.status IN ('finished','cancelled') AND excluded.status IN ('scheduled','live') THEN events.away_score WHEN excluded.status='finished' THEN excluded.away_score ELSE COALESCE(excluded.away_score,events.away_score) END,received_at=excluded.received_at`, project, id, e.Sport, e.Competition, e.Home, e.Away, e.Start, e.Status, e.HomeScore, e.AwayScore, p.Slug, e.ExternalID, p.ID, now, ex)
	if err != nil {
		return "", "", err
	}
	_, err = tx.Exec(`INSERT INTO event_aliases VALUES(?,?,?,?,?) ON CONFLICT(project_id,source,connection_id,external_id) DO NOTHING`, project, p.Slug, p.ID, e.ExternalID, id)
	if err != nil {
		return "", "", err
	}
	rules := "regulation"
	if e.Sport == "tennis" {
		rules = "match_completed"
	}
	market := ""
	err = tx.QueryRow(`SELECT id FROM markets WHERE project_id=? AND event_id=? AND type='match_winner' AND rules=?`, project, id, rules).Scan(&market)
	if err == sql.ErrNoRows {
		market = newID()
		_, err = tx.Exec(`INSERT INTO markets VALUES(?,?,?,?,?)`, project, market, id, "match_winner", rules)
	}
	return id, market, err
}
func (a *App) importBatch(project, actor string, p Provider, role, sport string, date time.Time, events []FeedEvent) (int, error) {
	if len(events) > 2000 {
		return 0, fmt.Errorf("too many events")
	}
	now := a.clock()
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if role == "odds" {
		// A successful complete date snapshot invalidates omitted bookmaker quotes.
		_, err = tx.Exec(`UPDATE quote_heads SET snapshot_id='' WHERE project_id=? AND source=? AND connection_id=? AND market_id IN (SELECT m.id FROM markets m JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE m.project_id=? AND e.example=0 AND e.sport=? AND e.starts_at>=? AND e.starts_at<?)`, project, p.Slug, p.ID, project, sport, date.Unix(), date.AddDate(0, 0, 1).Unix())
		if err != nil {
			return 0, err
		}
	}
	for _, e := range events {
		_, market, err := upsertEvent(tx, project, p, e, now)
		if err != nil {
			return 0, err
		}
		if role == "odds" || e.Example {
			snapshot := newID()
			for _, q := range e.Quotes {
				if q.Observed > now+30 || q.Observed <= 0 {
					return 0, fmt.Errorf("invalid quote timestamp")
				}
				_, err = tx.Exec(`INSERT INTO odds_observations(project_id,market_id,selection,bookmaker,odds_micros,source,connection_id,observed_at,received_at,snapshot_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, project, market, q.Selection, q.Bookmaker, q.Odds, p.Slug, p.ID, q.Observed, now, snapshot)
				if err != nil {
					return 0, err
				}
			}
			_, err = tx.Exec(`INSERT INTO quote_heads VALUES(?,?,?,?,?) ON CONFLICT(project_id,market_id,source,connection_id) DO UPDATE SET snapshot_id=excluded.snapshot_id`, project, market, p.Slug, p.ID, snapshot)
			if err != nil {
				return 0, err
			}
		}
	}
	if err = audit(tx, project, actor, role+".imported", p.Slug, now); err != nil {
		return 0, err
	}
	return len(events), tx.Commit()
}
