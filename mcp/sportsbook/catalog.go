package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"

	sdk "github.com/apteva/app-sdk"
)

var catalogID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var providerKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,159}$`)

type MarketConfig struct {
	Profile   string `json:"outcome_profile"`
	Rules     string `json:"rules"`
	Model     string `json:"prediction_model"`
	Scope     string `json:"history_scope"`
	Advantage int64  `json:"home_advantage"`
	Enabled   int    `json:"enabled"`
}

func (c MarketConfig) outcomes() []string {
	if c.Profile == "three_way" {
		return []string{"home", "draw", "away"}
	}
	return []string{"home", "away"}
}
func ensureCatalog(tx *sql.Tx, project string) error {
	for _, s := range []struct {
		id, name, profile, rules, scope string
		advantage                       int
	}{
		{"football", "Football", "three_way", "regulation", "competition", 60},
		{"tennis", "Tennis", "two_way", "match_completed", "sport", 0},
	} {
		if _, err := tx.Exec(`INSERT INTO sports VALUES(?,?,?,1) ON CONFLICT DO NOTHING`, project, s.id, s.name); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO sport_market_types VALUES(?,?,'match_winner',?,?,'elo',?,?,1) ON CONFLICT DO NOTHING`, project, s.id, s.profile, s.rules, s.scope, s.advantage); err != nil {
			return err
		}
	}
	for _, m := range []struct{ sport, role, slug, key string }{
		{"football", "sports_data", "the-sports-db", "Soccer"},
		{"football", "odds", "the-odds-api", "soccer_epl"},
	} {
		if _, err := tx.Exec(`INSERT INTO provider_sport_mappings VALUES(?,?,'',?,?,?,1) ON CONFLICT DO NOTHING`, project, m.sport, m.role, m.slug, m.key); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) initCatalog(project string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = ensureCatalog(tx, project); err != nil {
		return err
	}
	// Backfill v0.1 events without changing their identity or market semantics.
	rows, err := objects(tx, `SELECT DISTINCT sport,competition FROM events WHERE project_id=? AND competition_id=''`, project)
	if err != nil {
		return err
	}
	for _, r := range rows {
		id, err := ensureCompetition(tx, project, r["sport"].(string), r["competition"].(string))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE events SET competition_id=? WHERE project_id=? AND sport=? AND competition=? AND competition_id=''`, id, project, r["sport"], r["competition"]); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func ensureCompetition(tx *sql.Tx, project, sport, name string) (string, error) {
	id := ""
	err := tx.QueryRow(`SELECT id FROM competitions WHERE project_id=? AND sport=? AND name=? COLLATE NOCASE`, project, sport, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	hash := sha256.Sum256([]byte(sport + ":" + identity(name)))
	id = fmt.Sprintf("auto-%x", hash[:12])
	_, err = tx.Exec(`INSERT INTO competitions VALUES(?,?,?,?,1)`, project, id, sport, name)
	return id, err
}
func sportEnabled(q queryer, project, sport string) error {
	var enabled int
	err := q.QueryRow(`SELECT enabled FROM sports WHERE project_id=? AND id=?`, project, sport).Scan(&enabled)
	if err == sql.ErrNoRows {
		return fail("unknown_sport", 400, "Add this sport to the catalog first")
	}
	if err != nil {
		return err
	}
	if enabled == 0 {
		return fail("sport_disabled", 409, "Enable this sport in the catalog")
	}
	return nil
}
func marketConfig(q queryer, project, sport string) (MarketConfig, error) {
	var c MarketConfig
	err := q.QueryRow(`SELECT outcome_profile,rules,prediction_model,history_scope,home_advantage,enabled FROM sport_market_types WHERE project_id=? AND sport=? AND type='match_winner'`, project, sport).Scan(&c.Profile, &c.Rules, &c.Model, &c.Scope, &c.Advantage, &c.Enabled)
	if err == sql.ErrNoRows {
		return c, fail("market_unconfigured", 409, "Configure a match-winner market for this sport")
	}
	return c, err
}
func (a *App) catalog(project string) (any, error) {
	if err := a.initCatalog(project); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, table := range []string{"sports", "competitions", "sport_market_types", "provider_sport_mappings"} {
		rows, err := objects(a.db, "SELECT * FROM "+table+" WHERE project_id=? ORDER BY rowid", project)
		if err != nil {
			return nil, err
		}
		out[table] = rows
	}
	out["supported_market_types"] = []string{"match_winner"}
	out["supported_outcome_profiles"] = []string{"two_way", "three_way"}
	return out, nil
}
func (a *App) catalogWrite(app *sdk.AppCtx, project, actor, tool string, args map[string]any) (any, error) {
	if err := a.initCatalog(project); err != nil {
		return nil, err
	}
	sport := textArg(args, "sport")
	if !catalogID.MatchString(sport) {
		return nil, fail("invalid_sport", 400, "Sport ID must use 1–64 lowercase letters, digits, underscores or hyphens")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, ok := args["enabled"].(bool); !ok {
		return nil, fail("invalid_enabled", 400, "Provide enabled as a boolean")
	}
	enabled := 1
	if v, ok := args["enabled"].(bool); ok && !v {
		enabled = 0
	}
	entity := sport
	if tool != "sport_upsert" {
		var exists int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM sports WHERE project_id=? AND id=?`, project, sport).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			return nil, fail("unknown_sport", 400, "Add the sport first")
		}
	}
	switch tool {
	case "sport_upsert":
		name := textArg(args, "name")
		if name == "" || len(name) > 120 {
			return nil, fail("invalid_name", 400, "Provide a name of 1–120 characters")
		}
		_, err = tx.Exec(`INSERT INTO sports VALUES(?,?,?,?) ON CONFLICT(project_id,id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled`, project, sport, name, enabled)
		if err == nil && textArg(args, "outcome_profile") != "" {
			v, ok := args["market_enabled"].(bool)
			if !ok {
				return nil, fail("invalid_enabled", 400, "Provide market_enabled when saving market settings")
			}
			marketEnabled := 0
			if v {
				marketEnabled = 1
			}
			err = saveMarketConfig(tx, project, sport, args, marketEnabled)
		}
	case "competition_upsert":
		id, name := textArg(args, "competition_id"), textArg(args, "name")
		if !catalogID.MatchString(id) || name == "" || len(name) > 200 {
			return nil, fail("invalid_competition", 400, "Provide a stable competition ID and name")
		}
		var oldSport string
		e := tx.QueryRow(`SELECT sport FROM competitions WHERE project_id=? AND id=?`, project, id).Scan(&oldSport)
		if e != nil && e != sql.ErrNoRows {
			return nil, e
		}
		if oldSport != "" && oldSport != sport {
			return nil, fail("sport_mismatch", 409, "A competition cannot be moved to another sport")
		}
		_, err = tx.Exec(`INSERT INTO competitions VALUES(?,?,?,?,?) ON CONFLICT(project_id,id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled`, project, id, sport, name, enabled)
		entity = id
	case "sport_market_set":
		err = saveMarketConfig(tx, project, sport, args, enabled)
	case "provider_sport_mapping_set":
		role, slug, key, comp := textArg(args, "role"), textArg(args, "provider_slug"), textArg(args, "external_key"), textArg(args, "competition_id")
		if (role != "sports_data" && role != "odds") || !providerKey.MatchString(key) {
			return nil, fail("invalid_mapping", 400, "Provide a data/odds role and valid external key")
		}
		if !((slug == "the-odds-api") || (slug == "the-sports-db" && role == "sports_data") || (slug == "api-sports" && role == "sports_data" && sport == "football") || (slug == "api-tennis" && sport == "tennis")) {
			return nil, fail("unsupported_adapter", 409, "This adapter cannot map the selected sport and role")
		}
		if slug == "the-sports-db" && comp != "" {
			return nil, fail("unsupported_mapping", 400, "TheSportsDB uses sport-level names; filter competitions after import")
		}
		if slug == "the-odds-api" && !catalogID.MatchString(key) {
			return nil, fail("invalid_mapping", 400, "Use a provider sport key such as basketball_nba")
		}
		if (slug == "api-sports" || slug == "api-tennis") && comp == "" {
			return nil, fail("invalid_mapping", 400, "Fixed-sport adapters use numeric competition mappings")
		}
		if slug == "api-sports" || slug == "api-tennis" {
			if !regexp.MustCompile(`^[1-9][0-9]{0,11}$`).MatchString(key) {
				return nil, fail("invalid_mapping", 400, "Use a positive numeric league or tournament ID")
			}
		}
		if comp != "" {
			var s string
			if e := tx.QueryRow(`SELECT sport FROM competitions WHERE project_id=? AND id=?`, project, comp).Scan(&s); e != nil || s != sport {
				return nil, fail("competition_mismatch", 400, "Select a competition from this sport")
			}
		}
		_, err = tx.Exec(`INSERT INTO provider_sport_mappings VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id,sport,competition_id,role,provider_slug) DO UPDATE SET external_key=excluded.external_key,enabled=excluded.enabled`, project, sport, comp, role, slug, key, enabled)
	default:
		return nil, fail("unknown_tool", 404, "Unknown catalog operation")
	}
	if err != nil {
		return nil, err
	}
	if err = audit(tx, project, actor, tool, entity, a.clock()); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return a.catalog(project)
}
func (a *App) mapping(project, sport, comp, role, slug string) (string, error) {
	var key string
	err := a.db.QueryRow(`SELECT external_key FROM provider_sport_mappings WHERE project_id=? AND sport=? AND competition_id=? AND role=? AND provider_slug=? AND enabled=1`, project, sport, comp, role, slug).Scan(&key)
	if err == sql.ErrNoRows {
		return "", fail("mapping_required", 409, "Configure an enabled provider mapping for this sport and competition")
	}
	return key, err
}

// Adapter capabilities describe implemented parsers. Catalog mappings extend the
// generic adapters; they never turn fixed-sport adapters into arbitrary parsers.
func (a *App) availableProviders(app *sdk.AppCtx, project, role string) ([]Provider, error) {
	if err := a.initCatalog(project); err != nil {
		return nil, err
	}
	ps, err := a.bound(app, role)
	if err != nil {
		return nil, err
	}
	for i, p := range ps {
		if p.Slug != "the-odds-api" && !(p.Slug == "the-sports-db" && role == "sports_data") {
			continue
		}
		rows, err := objects(a.db, `SELECT DISTINCT m.sport FROM provider_sport_mappings m JOIN sports s ON s.project_id=m.project_id AND s.id=m.sport WHERE m.project_id=? AND m.role=? AND m.provider_slug=? AND m.enabled=1 AND s.enabled=1`, project, role, p.Slug)
		if err != nil {
			return nil, err
		}
		ps[i].Sports = append([]string{}, p.Sports...)
		seen := map[string]bool{}
		for _, s := range p.Sports {
			seen[s] = true
		}
		for _, r := range rows {
			s := r["sport"].(string)
			if !seen[s] {
				ps[i].Sports = append(ps[i].Sports, s)
				seen[s] = true
			}
		}
		if p.Slug == "the-odds-api" && role == "sports_data" {
			ps[i].Capabilities = []string{"events", "recent_results"}
		}
	}
	return ps, nil
}

// Disabling a catalog entry stops new exposure while settlements remain possible.
func marketEnabled(q queryer, project, market string) error {
	var s, c, m int
	err := q.QueryRow(`SELECT s.enabled,c.enabled,t.enabled FROM markets m JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id JOIN sports s ON s.project_id=e.project_id AND s.id=e.sport JOIN competitions c ON c.project_id=e.project_id AND c.id=e.competition_id JOIN sport_market_types t ON t.project_id=e.project_id AND t.sport=e.sport AND t.type=m.type WHERE m.project_id=? AND m.id=?`, project, market).Scan(&s, &c, &m)
	if err != nil {
		return err
	}
	if s == 0 || c == 0 || m == 0 {
		return fail("catalog_disabled", 409, "The sport, competition or market is disabled; new bets are blocked")
	}
	return nil
}

func saveMarketConfig(tx *sql.Tx, project, sport string, args map[string]any, enabled int) error {
	profile, rules, model, scope := textArg(args, "outcome_profile"), textArg(args, "rules"), textArg(args, "prediction_model"), textArg(args, "history_scope")
	advantage := intArg(args, "home_advantage")
	if raw, ok := args["home_advantage"]; ok {
		valid := true
		switch v := raw.(type) {
		case int, int64:
		case float64:
			valid = !math.IsNaN(v) && !math.IsInf(v, 0) && v == math.Trunc(v) && v >= 0 && v <= 200
		case json.Number:
			n, e := v.Int64()
			valid = e == nil && n >= 0 && n <= 200
		default:
			valid = false
		}
		if !valid {
			return fail("invalid_market_config", 400, "Home advantage must be an integer from 0 to 200")
		}
	}
	if (profile != "two_way" && profile != "three_way") || (rules != "regulation" && rules != "match_completed") || (model != "elo" && model != "baseline" && model != "none") || (scope != "sport" && scope != "competition") || advantage < 0 || advantage > 200 {
		return fail("invalid_market_config", 400, "Choose supported outcome, settlement, model and history settings")
	}
	_, err := tx.Exec(`INSERT INTO sport_market_types VALUES(?,?,'match_winner',?,?,?,?,?,?) ON CONFLICT(project_id,sport,type) DO UPDATE SET outcome_profile=excluded.outcome_profile,rules=excluded.rules,prediction_model=excluded.prediction_model,history_scope=excluded.history_scope,home_advantage=excluded.home_advantage,enabled=excluded.enabled`, project, sport, profile, rules, model, scope, advantage, enabled)
	return err
}
