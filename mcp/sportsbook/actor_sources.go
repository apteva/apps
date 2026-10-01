package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var actorOperationID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type actorSportSource struct {
	ID, Name, Sport, CompetitionID, Operation string
	ActorID                                   int64
	Input, FieldMap                           map[string]any
	Enabled, ReadOnly                         int
}

var defaultActorFields = map[string][]string{
	"external_id": {"external_id", "id", "event_id", "eventKey", "fixture.id", "match_id"}, "sport": {"sport", "sport_id"}, "competition": {"competition", "league", "tournament", "competition_name", "league.name"}, "home": {"home", "home_team", "homeTeam", "teams.home.name", "participants.home.name"}, "away": {"away", "away_team", "awayTeam", "teams.away.name", "participants.away.name"}, "starts_at": {"starts_at", "start", "start_time", "startsAt", "commence_time", "fixture.date", "date"}, "status": {"status", "event_status", "fixture.status.short"}, "home_score": {"home_score", "homeScore", "scores.home", "score.home", "goals.home"}, "away_score": {"away_score", "awayScore", "scores.away", "score.away", "goals.away"},
}

func actorProvider(app *sdk.AppCtx) (*sdk.BoundIntegration, error) {
	if app == nil {
		return nil, fail("actors_unavailable", 409, "Actors app is not bound")
	}
	for _, b := range app.IntegrationsFor("sports_scraper") {
		if b.Kind == "app" && (b.AppName == "" || b.AppName == "actors") {
			return b, nil
		}
	}
	return nil, fail("actors_unavailable", 409, "Bind the Actors app to the Sportsbook scrape-source role")
}
func actorMap(v any) map[string]any   { m, _ := v.(map[string]any); return m }
func mapFromAny(v any) map[string]any { m, _ := v.(map[string]any); return m }
func validateActorMap(raw any) (map[string]any, error) {
	m := actorMap(raw)
	if m == nil {
		return nil, fail("invalid_field_map", 400, "field_map must be an object")
	}
	out := map[string]any{}
	for k, v := range m {
		if _, ok := defaultActorFields[k]; !ok {
			return nil, fail("invalid_field_map", 400, "unknown canonical field: "+k)
		}
		switch x := v.(type) {
		case string:
			if strings.TrimSpace(x) == "" || len(x) > 200 {
				return nil, fail("invalid_field_map", 400, "field paths must be non-empty strings")
			}
			out[k] = x
		case []any:
			if len(x) == 0 || len(x) > 12 {
				return nil, fail("invalid_field_map", 400, "field path alternatives must contain 1–12 paths")
			}
			for _, p := range x {
				if s, ok := p.(string); !ok || strings.TrimSpace(s) == "" || len(s) > 200 {
					return nil, fail("invalid_field_map", 400, "field paths must be strings")
				}
			}
			out[k] = x
		default:
			return nil, fail("invalid_field_map", 400, "field_map values must be paths or path arrays")
		}
	}
	return out, nil
}
func parseJSONMap(raw string, fallback map[string]any) (map[string]any, error) {
	if raw == "" {
		return fallback, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fail("invalid_source_config", 400, "input must be valid JSON")
	}
	return m, nil
}
func (a *App) actorSourceList(project string) (any, error) {
	rows, err := objects(a.db, `SELECT id,name,sport,competition_id,actor_id,operation,input_json,field_map_json,enabled,read_only,created_at FROM actor_sport_sources WHERE project_id=? ORDER BY created_at DESC`, project)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		for _, k := range []string{"input_json", "field_map_json"} {
			if raw, ok := r[k].(string); ok {
				var v any
				if json.Unmarshal([]byte(raw), &v) == nil {
					r[strings.TrimSuffix(k, "_json")] = v
				}
				delete(r, k)
			}
		}
	}
	return map[string]any{"sources": rows, "read_only": true}, nil
}
func (a *App) actorSourceSet(app *sdk.AppCtx, project, sourceID string, args map[string]any) (any, error) {
	if _, err := actorProvider(app); err != nil {
		return nil, err
	}
	sport := textArg(args, "sport")
	if err := sportEnabled(a.db, project, sport); err != nil {
		return nil, err
	}
	name, operation := textArg(args, "name"), textArg(args, "operation")
	if name == "" || len(name) > 120 || !actorOperationID.MatchString(operation) {
		return nil, fail("invalid_source_config", 400, "Provide a name and valid Actors operation")
	}
	actorID := intArg(args, "actor_id")
	if actorID <= 0 {
		return nil, fail("invalid_source_config", 400, "actor_id must be positive")
	}
	input := mapFromAny(args["input"])
	if input == nil {
		input = map[string]any{}
	}
	if len(jsonText(input)) > 64*1024 {
		return nil, fail("invalid_source_config", 400, "input is too large")
	}
	fm, err := validateActorMap(args["field_map"])
	if err != nil {
		return nil, err
	}
	comp := textArg(args, "competition_id")
	if comp != "" {
		var s string
		if e := a.db.QueryRow(`SELECT sport FROM competitions WHERE project_id=? AND id=?`, project, comp).Scan(&s); e != nil || s != sport {
			return nil, fail("competition_mismatch", 400, "competition_id must belong to sport")
		}
	}
	enabled := 1
	if v, ok := args["enabled"].(bool); ok && !v {
		enabled = 0
	} else if _, ok := args["enabled"].(bool); !ok {
		return nil, fail("invalid_enabled", 400, "enabled must be boolean")
	}
	id := sourceID
	if id == "" {
		id = newID()
	}
	if len(id) > 128 {
		return nil, fail("invalid_source_config", 400, "source id is too long")
	}
	_, err = a.db.Exec(`INSERT INTO actor_sport_sources(project_id,id,name,sport,competition_id,actor_id,operation,input_json,field_map_json,enabled,read_only,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,1,?) ON CONFLICT(project_id,id) DO UPDATE SET name=excluded.name,sport=excluded.sport,competition_id=excluded.competition_id,actor_id=excluded.actor_id,operation=excluded.operation,input_json=excluded.input_json,field_map_json=excluded.field_map_json,enabled=excluded.enabled`, project, id, name, sport, comp, actorID, operation, jsonText(input), jsonText(fm), enabled, a.clock())
	if err != nil {
		return nil, err
	}
	return a.actorSourceList(project)
}
func sourceFromRow(a *App, project, id string) (actorSportSource, error) {
	var s actorSportSource
	var input, fields string
	err := a.db.QueryRow(`SELECT id,name,sport,competition_id,actor_id,operation,input_json,field_map_json,enabled,read_only FROM actor_sport_sources WHERE project_id=? AND id=?`, project, id).Scan(&s.ID, &s.Name, &s.Sport, &s.CompetitionID, &s.ActorID, &s.Operation, &input, &fields, &s.Enabled, &s.ReadOnly)
	if err == sql.ErrNoRows {
		return s, fail("not_found", 404, "Scrape source not found")
	}
	if err != nil {
		return s, err
	}
	s.Input, err = parseJSONMap(input, nil)
	if err != nil {
		return s, err
	}
	s.FieldMap, err = parseJSONMap(fields, nil)
	return s, err
}
func renderActorInput(v any, vars map[string]string) any {
	switch x := v.(type) {
	case string:
		for k, val := range vars {
			x = strings.ReplaceAll(x, "${"+k+"}", val)
		}
		return x
	case map[string]any:
		o := map[string]any{}
		for k, v := range x {
			o[k] = renderActorInput(v, vars)
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, v := range x {
			o[i] = renderActorInput(v, vars)
		}
		return o
	default:
		return v
	}
}
func actorCall(ctx context.Context, app *sdk.AppCtx, tool string, args map[string]any, out any) error {
	return sdk.CallAppResultContext(ctx, app.PlatformAPI(), "actors", tool, args, out)
}
func numberValue(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case json.Number:
		n, e := x.Float64()
		return n, e == nil
	case string:
		n, e := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(x), ",", "."), 64)
		return n, e == nil
	}
	return 0, false
}
func pathValue(item map[string]any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	var cur any = item
	for _, part := range strings.Split(path, ".") {
		if m, ok := cur.(map[string]any); ok {
			var yes bool
			cur, yes = m[part]
			if !yes {
				return nil, false
			}
		} else if list, ok := cur.([]any); ok {
			n, e := strconv.Atoi(part)
			if e != nil || n < 0 || n >= len(list) {
				return nil, false
			}
			cur = list[n]
		} else {
			return nil, false
		}
	}
	return cur, true
}
func mappedValue(item map[string]any, fields map[string]any, key string) any {
	paths := defaultActorFields[key]
	if v, ok := fields[key]; ok {
		switch x := v.(type) {
		case string:
			paths = []string{x}
		case []any:
			paths = nil
			for _, p := range x {
				if s, ok := p.(string); ok {
					paths = append(paths, s)
				}
			}
		}
	}
	for _, p := range paths {
		if v, ok := pathValue(item, p); ok && v != nil && strings.TrimSpace(fmt.Sprint(v)) != "" {
			return v
		}
	}
	return nil
}
func actorText(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
func parseActorTime(v any) (int64, bool) {
	if n, ok := numberValue(v); ok {
		if n > 1e12 {
			n /= 1000
		}
		if n > 1e9 {
			return int64(n), true
		}
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, e := time.Parse(layout, s); e == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}
func normalizeActorEvent(item map[string]any, s actorSportSource, date time.Time) (FeedEvent, error) {
	e := FeedEvent{ExternalID: actorText(mappedValue(item, s.FieldMap, "external_id")), Sport: s.Sport, Status: "scheduled", CompetitionID: s.CompetitionID}
	if e.ExternalID == "" {
		return e, fmt.Errorf("missing external_id")
	}
	if v := mappedValue(item, s.FieldMap, "sport"); v != nil {
		e.Sport = strings.ToLower(actorText(v))
		if e.Sport != strings.ToLower(s.Sport) {
			return e, fmt.Errorf("sport mismatch")
		}
	}
	if v := mappedValue(item, s.FieldMap, "competition"); v != nil {
		e.Competition = actorText(v)
	}
	if e.Competition == "" {
		e.Competition = s.CompetitionID
	}
	if e.Competition == "" {
		return e, fmt.Errorf("missing competition")
	}
	if v := mappedValue(item, s.FieldMap, "home"); v != nil {
		e.Home = actorText(v)
	}
	if v := mappedValue(item, s.FieldMap, "away"); v != nil {
		e.Away = actorText(v)
	}
	if v := mappedValue(item, s.FieldMap, "starts_at"); v != nil {
		if n, ok := parseActorTime(v); ok {
			e.Start = n
		}
	}
	if e.Start == 0 {
		return e, fmt.Errorf("missing starts_at")
	}
	if v := mappedValue(item, s.FieldMap, "status"); v != nil {
		switch strings.ToLower(actorText(v)) {
		case "finished", "final", "ft", "completed", "complete":
			e.Status = "finished"
		case "live", "in_play", "in progress":
			e.Status = "live"
		case "cancelled", "canceled", "postponed":
			e.Status = "cancelled"
		}
	}
	if v := mappedValue(item, s.FieldMap, "home_score"); v != nil {
		if n, ok := numberValue(v); ok {
			z := int(n)
			e.HomeScore = &z
		}
	}
	if v := mappedValue(item, s.FieldMap, "away_score"); v != nil {
		if n, ok := numberValue(v); ok {
			z := int(n)
			e.AwayScore = &z
		}
	}
	if e.Sport == "" {
		e.Sport = s.Sport
	}
	if e.Start < date.Unix() || e.Start >= date.AddDate(0, 0, 1).Unix() {
		return e, fmt.Errorf("outside requested date")
	}
	return e, nil
}
func (a *App) actorSync(ctx context.Context, app *sdk.AppCtx, project, actorName string, args map[string]any) (any, error) {
	s, err := sourceFromRow(a, project, textArg(args, "source_id"))
	if err != nil {
		return nil, err
	}
	if s.ReadOnly != 1 {
		return nil, fail("source_not_read_only", 409, "Actors source must be read-only")
	}
	if s.Enabled == 0 {
		return nil, fail("source_disabled", 409, "Actors source is disabled")
	}
	if err := sportEnabled(a.db, project, s.Sport); err != nil {
		return nil, err
	}
	date, err := time.Parse("2006-01-02", textArg(args, "date"))
	if err != nil {
		return nil, fail("invalid_date", 400, "date must be YYYY-MM-DD")
	}
	if _, err := actorProvider(app); err != nil {
		return nil, err
	}
	input := renderActorInput(s.Input, map[string]string{"date": date.Format("2006-01-02"), "sport": s.Sport, "competition": s.CompetitionID})
	runArgs := map[string]any{"actor_id": s.ActorID, "operation": s.Operation, "input": input, "idempotency_key": fmt.Sprintf("sportsbook:%s:%s", s.ID, date.Format("20060102"))}
	var queued map[string]any
	if err = actorCall(ctx, app, "actors_run", runArgs, &queued); err != nil {
		return nil, fail("actors_run_failed", 502, "Actors refused the read-only scrape run")
	}
	runID := int64FromAny(queued["run_id"])
	if runID == 0 {
		runID = int64FromAny(queued["id"])
	}
	if runID == 0 {
		return nil, fail("actors_run_failed", 502, "Actors returned no run id")
	}
	var run map[string]any
	deadline := time.Now().Add(45 * time.Second)
	for {
		var out map[string]any
		if err = actorCall(ctx, app, "actors_run_get", map[string]any{"id": runID}, &out); err != nil {
			return nil, fail("actors_run_failed", 502, "Actors run status could not be read")
		}
		run = actorRunObject(out)
		status := strings.ToLower(fmt.Sprint(run["status"]))
		if status == "succeeded" || status == "completed" {
			break
		}
		if status == "failed" || status == "cancelled" || status == "canceled" {
			return nil, fail("actors_run_failed", 502, "Actors scrape run did not complete")
		}
		if time.Now().After(deadline) {
			return nil, fail("actors_run_timeout", 504, "Actors scrape run exceeded the 45 second read window")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	items := []map[string]any{}
	after := int64(0)
	for {
		var page map[string]any
		if err = actorCall(ctx, app, "actors_dataset_read", map[string]any{"run_id": runID, "after": after, "limit": 200}, &page); err != nil {
			return nil, fail("actors_dataset_failed", 502, "Actors dataset could not be read")
		}
		raw, _ := page["items"].([]any)
		for _, v := range raw {
			if item := actorMap(v); item != nil {
				items = append(items, item)
			}
		}
		next := int64FromAny(page["next_cursor"])
		more, _ := page["has_more"].(bool)
		if !more || next <= after {
			break
		}
		after = next
		if len(items) > 5000 {
			return nil, fail("actors_dataset_too_large", 413, "Actors dataset exceeds the import limit")
		}
	}
	events := []FeedEvent{}
	skipped := 0
	competitionName := ""
	if s.CompetitionID != "" {
		_ = a.db.QueryRow("SELECT name FROM competitions WHERE project_id=? AND id=?", project, s.CompetitionID).Scan(&competitionName)
	}
	for _, item := range items {
		ev, er := normalizeActorEvent(item, s, date)
		if er != nil {
			skipped++
			continue
		}
		if competitionName != "" && ev.Competition == s.CompetitionID {
			ev.Competition = competitionName
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		return map[string]any{"source_id": s.ID, "run_id": runID, "imported": 0, "skipped": skipped, "read_only": true}, nil
	}
	imported, err := a.importBatch(project, actorName, Provider{ID: s.ActorID, Slug: "actors"}, "sports_data", s.Sport, date, events, "actors:"+s.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"source_id": s.ID, "run_id": runID, "imported": imported, "skipped": skipped, "read_only": true}, nil
}
func actorRunObject(v map[string]any) map[string]any {
	if r, ok := v["run"].(map[string]any); ok {
		return r
	}
	return v
}
func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case int:
		return int64(x)
	}
	return 0
}
