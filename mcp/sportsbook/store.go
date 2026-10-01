package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const quoteTTL int64 = 900

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func textArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}
func intArg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || math.Abs(v) > 9007199254740991 {
			return 0
		}
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
func boolArg(args map[string]any, key string) bool { b, _ := args[key].(bool); return b }
func jsonText(v any) string                        { b, _ := json.Marshal(v); return string(b) }
func objects(q queryer, stmt string, args ...any) ([]map[string]any, error) {
	rows, err := q.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vs := make([]any, len(cols))
		ps := make([]any, len(cols))
		for i := range vs {
			ps[i] = &vs[i]
		}
		if err = rows.Scan(ps...); err != nil {
			return nil, err
		}
		obj := map[string]any{}
		for i, c := range cols {
			v := vs[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if c == "probabilities" || c == "features" {
				if s, ok := v.(string); ok {
					var decoded any
					if json.Unmarshal([]byte(s), &decoded) == nil {
						v = decoded
					}
				}
			}
			obj[c] = v
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}
func audit(tx *sql.Tx, project, actor, action, id string, now int64) error {
	_, err := tx.Exec("INSERT INTO audit_events(project_id,actor,action,entity_id,created_at) VALUES(?,?,?,?,?)", project, actor, action, id, now)
	return err
}
func ledger(tx *sql.Tx, project, bankroll, transaction string, entries map[string]int64, now int64) error {
	var total int64
	for _, v := range entries {
		total += v
	}
	if total != 0 {
		return fmt.Errorf("unbalanced ledger transaction")
	}
	for account, amount := range entries {
		if amount == 0 {
			continue
		}
		if _, err := tx.Exec("INSERT INTO ledger_entries(project_id,bankroll_id,transaction_id,account,amount_minor,created_at) VALUES(?,?,?,?,?,?)", project, bankroll, transaction, account, amount, now); err != nil {
			return err
		}
	}
	return nil
}
func balances(q queryer, project, bankroll string) (cash, locked int64, err error) {
	err = q.QueryRow("SELECT COALESCE(SUM(CASE WHEN account='cash' THEN amount_minor ELSE 0 END),0), COALESCE(SUM(CASE WHEN account='locked' THEN amount_minor ELSE 0 END),0) FROM ledger_entries WHERE project_id=? AND bankroll_id=?", project, bankroll).Scan(&cash, &locked)
	return
}

const currentQuotesSQL = `SELECT o.* FROM odds_observations o JOIN quote_heads h ON h.project_id=o.project_id AND h.market_id=o.market_id AND h.source=o.source AND h.connection_id=o.connection_id AND h.snapshot_id=o.snapshot_id`

func (a *App) workspace(project string, example bool, sport string) (any, error) {
	if err := a.initCatalog(project); err != nil {
		return nil, err
	}
	now := a.clock()
	ex := 0
	if example {
		ex = 1
	}
	events, err := objects(a.db, `SELECT * FROM events WHERE project_id=? AND example=? AND (?='' OR sport=?) ORDER BY CASE status WHEN 'scheduled' THEN 0 WHEN 'live' THEN 1 ELSE 2 END,starts_at DESC LIMIT 250`, project, ex, sport, sport)
	if err != nil {
		return nil, err
	}
	quotes, err := objects(a.db, currentQuotesSQL+` JOIN markets m ON m.project_id=o.project_id AND m.id=o.market_id JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE o.project_id=? AND e.example=? AND (?='' OR e.sport=?) AND o.observed_at>=? AND o.observed_at<=? AND e.status='scheduled' AND e.starts_at>? ORDER BY o.observed_at DESC LIMIT 2000`, project, ex, sport, sport, now-quoteTTL, now+30, now)
	if err != nil {
		return nil, err
	}
	markets, err := objects(a.db, `SELECT m.* FROM markets m JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE m.project_id=? AND e.example=? AND (?='' OR e.sport=?) LIMIT 1000`, project, ex, sport, sport)
	if err != nil {
		return nil, err
	}
	predictions, err := objects(a.db, `SELECT p.project_id,p.id,p.market_id,p.model,p.probabilities,json_remove(p.features,'$.training_events','$.quotes') AS features,p.created_at,p.expires_at FROM predictions p JOIN markets m ON m.project_id=p.project_id AND m.id=p.market_id JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE p.project_id=? AND e.example=? ORDER BY p.created_at DESC,p.rowid DESC LIMIT 150`, project, ex)
	if err != nil {
		return nil, err
	}
	bankrolls, err := objects(a.db, `SELECT b.*,COALESCE(SUM(CASE WHEN l.account='cash' THEN l.amount_minor ELSE 0 END),0) AS cash_minor,COALESCE(SUM(CASE WHEN l.account='locked' THEN l.amount_minor ELSE 0 END),0) AS locked_minor,COALESCE(-SUM(CASE WHEN l.account='pnl' THEN l.amount_minor ELSE 0 END),0) AS pnl_minor FROM bankrolls b LEFT JOIN ledger_entries l ON l.project_id=b.project_id AND l.bankroll_id=b.id WHERE b.project_id=? AND b.example=? GROUP BY b.id ORDER BY b.created_at LIMIT 50`, project, ex)
	if err != nil {
		return nil, err
	}
	proposals, err := objects(a.db, `SELECT p.*,q.selection,q.bookmaker,q.odds_micros,m.event_id FROM proposals p JOIN bankrolls b ON b.project_id=p.project_id AND b.id=p.bankroll_id JOIN odds_observations q ON q.id=p.quote_id AND q.project_id=p.project_id JOIN markets m ON m.project_id=q.project_id AND m.id=q.market_id WHERE p.project_id=? AND b.example=? ORDER BY p.created_at DESC,p.rowid DESC LIMIT 100`, project, ex)
	if err != nil {
		return nil, err
	}
	bets, err := objects(a.db, `SELECT b.*,q.selection,q.bookmaker,m.event_id FROM bets b JOIN bankrolls k ON k.project_id=b.project_id AND k.id=b.bankroll_id JOIN proposals p ON p.project_id=b.project_id AND p.id=b.proposal_id JOIN odds_observations q ON q.id=p.quote_id AND q.project_id=p.project_id JOIN markets m ON m.project_id=q.project_id AND m.id=q.market_id WHERE b.project_id=? AND k.example=? ORDER BY b.accepted_at DESC LIMIT 100`, project, ex)
	if err != nil {
		return nil, err
	}
	explanations, err := objects(a.db, `SELECT x.* FROM explanations x JOIN predictions p ON p.project_id=x.project_id AND p.id=x.prediction_id JOIN markets m ON m.project_id=p.project_id AND m.id=p.market_id JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE x.project_id=? AND e.example=? ORDER BY x.created_at DESC LIMIT 100`, project, ex)
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": events, "markets": markets, "quotes": quotes, "predictions": predictions, "bankrolls": bankrolls, "proposals": proposals, "bets": bets, "explanations": explanations, "server_time": now, "example": example, "mode": "paper", "quote_ttl_seconds": quoteTTL}, nil
}
func (a *App) createBankroll(project, actor string, args map[string]any) (any, error) {
	name := textArg(args, "name")
	currency := textArg(args, "currency")
	initial := intArg(args, "initial_minor")
	stake := intArg(args, "max_stake_bps")
	if stake == 0 {
		stake = 200
	}
	exposure := intArg(args, "max_exposure_bps")
	if exposure == 0 {
		exposure = 1000
	}
	if name == "" || len(name) > 120 || initial < 100 || initial > 100000000000 || !(currency == "EUR" || currency == "USD" || currency == "GBP") || stake < 1 || stake > 1000 || exposure < stake || exposure > 5000 {
		return nil, fail("invalid_bankroll", 400, "Provide a name, currency (EUR/USD/GBP), positive minor-unit balance and valid risk limits")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id := newID()
	ex := 0
	if boolArg(args, "example") {
		ex = 1
	}
	now := a.clock()
	_, err = tx.Exec(`INSERT INTO bankrolls VALUES(?,?,?,?,?,?,?,?,?)`, project, id, name, currency, initial, stake, exposure, ex, now)
	if err != nil {
		return nil, err
	}
	if err = ledger(tx, project, id, newID(), map[string]int64{"cash": initial, "equity": -initial}, now); err != nil {
		return nil, err
	}
	if err = audit(tx, project, actor, "bankroll.created", id, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}
func (a *App) perform(ctx context.Context, app *sdk.AppCtx, project, actor, tool string, args map[string]any) (any, error) {
	switch tool {
	case "catalog_get":
		return a.catalog(project)
	case "sport_upsert", "competition_upsert", "sport_market_set", "provider_sport_mapping_set":
		return a.catalogWrite(app, project, actor, tool, args)
	case "provider_sports_list":
		return a.discoverSports(ctx, app, project, intArg(args, "connection_id"))
	case "workspace_get":
		return a.workspace(project, boolArg(args, "example"), textArg(args, "sport"))
	case "integrations_list":
		return a.integrationStatus(app, project)
	case "sports_sources_list":
		return a.actorSourceList(project)
	case "sports_scrape_source_set":
		return a.actorSourceSet(app, project, textArg(args, "source_id"), args)
	case "sports_scrape_sync":
		return a.actorSync(ctx, app, project, actor, args)
	case "provider_route_set":
		return a.setRoute(app, project, actor, args)
	case "sports_sync", "odds_sync":
		return a.sync(ctx, app, project, actor, tool, args)
	case "demo_load":
		return a.loadDemo(project, actor)
	case "bankroll_create":
		return a.createBankroll(project, actor, args)
	case "prediction_run":
		return a.predict(project, actor, textArg(args, "market_id"))
	case "prediction_explain":
		return a.explain(ctx, app, project, args)
	case "bet_propose":
		return a.propose(project, actor, args)
	case "proposal_accept":
		return a.accept(project, actor, textArg(args, "proposal_id"))
	case "bet_settle":
		return a.settle(project, actor, args)
	case "odds_history":
		return objects(a.db, `SELECT o.* FROM odds_observations o JOIN markets m ON m.project_id=o.project_id AND m.id=o.market_id WHERE o.project_id=? AND m.event_id=? ORDER BY o.observed_at DESC,o.id DESC LIMIT 500`, project, textArg(args, "event_id"))
	case "bet_submit":
		return nil, fail("live_execution_unavailable", 409, "Connected execution is unavailable in v0.2. Use proposal_accept for paper bets.")
	}
	return nil, fail("unknown_tool", 404, "Unknown operation")
}
func toolSchema(name string) map[string]any {
	props := map[string]any{}
	required := []string{}
	str := func(key string) { props[key] = map[string]any{"type": "string", "maxLength": 500} }
	integer := func(key string) { props[key] = map[string]any{"type": "integer", "minimum": 1} }
	for _, key := range []string{"sport", "sport_key", "competition_id", "provider_slug", "external_key", "outcome_profile", "rules", "prediction_model", "history_scope", "market_id", "prediction_id", "bankroll_id", "proposal_id", "bet_id", "event_id", "role", "date", "name", "currency", "rationale", "outcome", "note", "model", "source_id", "operation"} {
		str(key)
	}
	for _, key := range []string{"connection_id", "quote_id", "stake_minor", "initial_minor", "max_stake_bps", "max_exposure_bps", "home_advantage", "actor_id"} {
		integer(key)
	}
	props["home_advantage"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 200}
	props["market_enabled"] = map[string]any{"type": "boolean"}
	props["enabled"] = map[string]any{"type": "boolean"}
	props["example"] = map[string]any{"type": "boolean"}
	props["all_sources"] = map[string]any{"type": "boolean"}
	props["input"] = map[string]any{"type": "object", "additionalProperties": true}
	props["field_map"] = map[string]any{"type": "object", "additionalProperties": true}
	switch name {
	case "sport_upsert":
		required = []string{"sport", "name", "enabled"}
	case "competition_upsert":
		required = []string{"sport", "competition_id", "name", "enabled"}
	case "sport_market_set":
		required = []string{"sport", "outcome_profile", "rules", "prediction_model", "history_scope", "enabled"}
	case "provider_sport_mapping_set":
		required = []string{"sport", "role", "provider_slug", "external_key", "enabled"}
	case "provider_sports_list":
		required = []string{"connection_id"}
	case "provider_route_set":
		required = []string{"role", "connection_id"}
	case "prediction_run":
		required = []string{"market_id"}
	case "prediction_explain":
		required = []string{"prediction_id", "model"}
	case "bet_propose":
		required = []string{"bankroll_id", "prediction_id", "quote_id", "stake_minor", "rationale"}
	case "proposal_accept":
		required = []string{"proposal_id"}
	case "bet_settle":
		required = []string{"bet_id", "outcome", "note"}
	case "bankroll_create":
		required = []string{"name", "currency", "initial_minor"}
	case "odds_history":
		required = []string{"event_id"}
	case "sports_sync", "odds_sync":
		required = []string{"sport", "date"}
	case "sports_scrape_source_set":
		required = []string{"name", "sport", "actor_id", "operation", "field_map", "enabled"}
	case "sports_scrape_sync":
		required = []string{"source_id", "date"}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func parseOdds(s string) (int64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 1 || v > 1000 {
		return 0, fmt.Errorf("invalid odds")
	}
	return int64(math.Round(v * 1000000)), nil
}
