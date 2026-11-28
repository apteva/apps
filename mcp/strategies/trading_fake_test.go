package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/google/uuid"
)

// fakeTrading plays the Trading app for Strategies tests. It keeps Trading's
// account and reference tables in its own database, separate from the
// Strategies database, and serves the app-only reads Strategies calls. Paper
// fills happen synchronously inside order_place, which real Trading defers to
// its next engine tick.

//go:embed testdata/trading_fixture_schema.sql
var tradingFixtureSchema string

type fixtureTrading struct {
	tk.BasePlatformClient
	ctx                *sdk.AppCtx
	db                 *sql.DB
	engine             *engine
	orderRequests      []map[string]any
	observedPortfolios []int64
	stateErr           error
	toolCalls          []string
}

func newFixtureTrading(t *testing.T) *fixtureTrading {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/trading.db?_pragma=foreign_keys(on)&_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(tradingFixtureSchema); err != nil {
		t.Fatalf("trading fixture schema: %v", err)
	}
	return &fixtureTrading{db: db}
}

// tradingDB is the fake Trading's database for the test's AppCtx.
func tradingDB(ctx *sdk.AppCtx) *sql.DB { return ctx.PlatformAPI().(tradingFixtureOwner).fixture().db }

// tradingEngine fills paper orders inside the fake Trading.
func tradingEngine(ctx *sdk.AppCtx) *engine {
	return ctx.PlatformAPI().(tradingFixtureOwner).fixture().engine
}

type tradingFixtureOwner interface{ fixture() *fixtureTrading }

func (p *fixtureTrading) fixture() *fixtureTrading { return p }

func (p *fixtureTrading) project(args map[string]any) (string, error) {
	return resolveProjectFromArgs(args)
}

func (p *fixtureTrading) CallAppResult(app, tool string, args map[string]any, out any) error {
	if app != "trading" {
		return fmt.Errorf("unexpected dependency %s", app)
	}
	p.toolCalls = append(p.toolCalls, tool)
	var result any
	var err error
	switch tool {
	case "portfolio_state":
		result, err = p.portfolioState(args)
	case "portfolio_policy_check":
		result, err = p.policyCheck(args)
	case "research_reference":
		result, err = p.researchReference(args)
	case "fills_feed":
		result, err = p.fillsFeed(args)
	case "portfolio_list":
		result, err = p.portfolioList(args)
	case "order_place":
		result, err = p.orderPlace(args)
	default:
		return fmt.Errorf("unexpected Trading tool %s", tool)
	}
	if err != nil {
		return err
	}
	return decodeResearchValue(result, out)
}

func fixtureSymbols(args map[string]any) []string {
	values, _ := stringSliceValue(args["symbols"])
	return sortedSymbols(values)
}

func (p *fixtureTrading) portfolioState(args map[string]any) (any, error) {
	pid, err := p.project(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	p.observedPortfolios = append(p.observedPortfolios, id)
	if p.stateErr != nil {
		return nil, p.stateErr
	}
	db := p.db
	pf, err := dbGetPortfolio(db, pid, id)
	if err != nil {
		return nil, err
	}
	state := &TradingState{Version: 1, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Portfolio: pf,
		Marks: map[string]*Mark{}, Instruments: map[string]*Instrument{}, Profiles: map[string]VenueExecutionProfile{}, Violations: map[string]*UniverseViolation{}}
	if err = db.QueryRow(`SELECT COALESCE((SELECT revision FROM portfolio_revisions WHERE portfolio_id=?),0)`, id).Scan(&state.Revision); err != nil {
		return nil, err
	}
	if state.Config, err = dbPortfolioConfig(db, id); err != nil {
		return nil, err
	}
	state.ExecutionSettings = dbPortfolioExecutionSettings(db, id)
	if state.Equity, err = computeEquity(db, pf); err != nil {
		return nil, err
	}
	if state.Positions, err = dbListPositions(db, id); err != nil {
		return nil, err
	}
	if state.WorkingOrders, err = dbListOrdersInternal(db, id, "working", -1); err != nil {
		return nil, err
	}
	if state.RiskPolicy, err = dbGetPortfolioRiskPolicy(db, pf); err != nil {
		return nil, err
	}
	if state.UniversePolicy, err = dbGetPortfolioUniversePolicy(db, pf); err != nil {
		return nil, err
	}
	all := map[string]bool{}
	for _, symbol := range fixtureSymbols(args) {
		all[symbol] = true
	}
	for _, position := range state.Positions {
		all[canonicalSymbol(position.Symbol)] = true
	}
	for symbol := range all {
		if mark, err := dbGetMark(db, symbol); err == nil && mark != nil {
			state.Marks[symbol] = mark
		}
		if instrument, err := dbGetInstrument(db, symbol); err == nil && instrument != nil {
			state.Instruments[symbol] = instrument
		}
		state.Profiles[symbol] = resolveVenueProfile(db, pf, symbol, inferAssetClass(symbol))
		violation, err := portfolioUniverseViolation(db, pf, symbol)
		if err != nil {
			return nil, err
		}
		if violation != nil {
			state.Violations[symbol] = violation
		}
	}
	return state, nil
}

func (p *fixtureTrading) policyCheck(args map[string]any) (any, error) {
	pid, err := p.project(args)
	if err != nil {
		return nil, err
	}
	pf, err := dbGetPortfolio(p.db, pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, err
	}
	violations := map[string]*UniverseViolation{}
	for _, symbol := range fixtureSymbols(args) {
		violation, err := portfolioUniverseViolation(p.db, pf, symbol)
		if err != nil {
			return nil, err
		}
		if violation != nil {
			violations[symbol] = violation
		}
	}
	return map[string]any{"violations": violations}, nil
}

func (p *fixtureTrading) researchReference(args map[string]any) (any, error) {
	symbols := fixtureSymbols(args)
	set := observationSetJSON(stringSet(symbols))
	start, end := strArg(args, "start"), strArg(args, "end")
	tables := map[string][]map[string]any{}
	queries := map[string]struct {
		query  string
		params []any
	}{
		"exchange_sessions":    {`SELECT venue,session_date,session_type,open_at,close_at,status,source FROM exchange_sessions WHERE session_date BETWEEN ? AND ? ORDER BY rowid`, []any{start, end}},
		"corporate_actions":    {`SELECT provider,provider_event_id,action_type,symbol,new_symbol,ex_date,effective_date FROM corporate_actions WHERE COALESCE(NULLIF(effective_date,''),NULLIF(ex_date,''),process_date) BETWEEN ? AND ? AND symbol IN (SELECT value FROM json_each(?)) ORDER BY rowid`, []any{start, end, set}},
		"security_listings":    {`SELECT security_id,venue,symbol,valid_from,valid_to,active,source FROM security_listings WHERE symbol IN (SELECT value FROM json_each(?)) ORDER BY rowid`, []any{set}},
		"universe_memberships": {`SELECT universe_id,security_id,valid_from,valid_to,source FROM universe_memberships WHERE security_id IN (SELECT security_id FROM security_listings WHERE symbol IN (SELECT value FROM json_each(?))) ORDER BY rowid`, []any{set}},
	}
	for name, q := range queries {
		rows, err := readResearchRows(p.db, q.query, q.params...)
		if err != nil {
			return nil, err
		}
		tables[name] = rows
	}
	ids := map[string]string{}
	for _, symbol := range symbols {
		var id string
		if p.db.QueryRow(`SELECT security_id FROM security_listings WHERE symbol=? ORDER BY active DESC,updated_at DESC LIMIT 1`, symbol).Scan(&id) == nil {
			ids[symbol] = id
		}
	}
	return map[string]any{"start": start, "end": end, "adjustment": strArg(args, "adjustment"), "status": referenceDataStatus(p.db), "security_ids": ids, "tables": tables, "next_cursor": ""}, nil
}

func (p *fixtureTrading) fillsFeed(args map[string]any) (any, error) {
	pid, err := p.project(args)
	if err != nil {
		return nil, err
	}
	after, limit := int64Arg(args, "after_fill_id", 0), intArg(args, "limit", 500)
	legacy := 0
	if v, _ := args["include_legacy"].(bool); v {
		legacy = 1
	}
	rows, err := p.db.Query(`SELECT f.id,f.order_id,f.portfolio_id,o.symbol,o.asset_class,o.side,COALESCE(o.outcome,''),f.qty,f.price,f.fee,
		COALESCE(f.spread_cost,0),COALESCE(f.slippage_cost,0),COALESCE(f.filled_at,''),COALESCE(o.decision_ref,''),
		CASE WHEN o.origin_app='' THEN o.strategy_id ELSE o.external_strategy_id END,
		CASE WHEN o.origin_app='' THEN o.strategy_version ELSE o.external_strategy_version END
		FROM fills f JOIN orders o ON o.id=f.order_id WHERE f.project_id=? AND f.id>?
		AND (o.origin_app='strategies' OR (?=1 AND o.origin_app='' AND o.strategy_id>0)) ORDER BY f.id LIMIT ?`, pid, after, legacy, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fills := []TradingFill{}
	for rows.Next() {
		var f TradingFill
		if err := rows.Scan(&f.ID, &f.OrderID, &f.PortfolioID, &f.Symbol, &f.AssetClass, &f.Side, &f.Outcome, &f.Qty, &f.Price, &f.Fee,
			&f.SpreadCost, &f.SlippageCost, &f.FilledAt, &f.DecisionRef, &f.ExternalStrategyID, &f.ExternalStrategyVersion); err != nil {
			return nil, err
		}
		fills = append(fills, f)
	}
	more := len(fills) > limit
	if more {
		fills = fills[:limit]
	}
	next := after
	if len(fills) > 0 {
		next = fills[len(fills)-1].ID
	}
	return map[string]any{"fills": fills, "next_fill_id": next, "has_more": more}, nil
}

func (p *fixtureTrading) portfolioList(args map[string]any) (any, error) {
	pid, err := p.project(args)
	if err != nil {
		return nil, err
	}
	portfolios, err := dbListPortfolios(p.db, pid)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, pf := range portfolios {
		out = append(out, map[string]any{"id": pf.ID, "name": pf.Name, "mode": pf.Mode, "status": pf.Status, "execution_environment": pf.ExecutionEnvironment, "live_armed": pf.LiveArmed})
	}
	return map[string]any{"portfolios": out}, nil
}

func (p *fixtureTrading) orderPlace(args map[string]any) (any, error) {
	captured := map[string]any{}
	for k, v := range args {
		captured[k] = v
	}
	p.orderRequests = append(p.orderRequests, captured)
	pid, err := p.project(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	key := strArg(args, "idempotency_key")
	if key != "" {
		var existing string
		if p.db.QueryRow(`SELECT order_id FROM order_requests WHERE project_id=? AND portfolio_id=? AND request_key=?`, pid, id, key).Scan(&existing) == nil {
			stored, err := dbGetOrder(p.db, pid, existing)
			if err != nil {
				return nil, err
			}
			return map[string]any{"order_id": stored.ID, "status": stored.Status}, nil
		}
	}
	if raw, ok := args["if_revision"]; ok && raw != nil {
		var current int64
		if err := p.db.QueryRow(`SELECT COALESCE((SELECT revision FROM portfolio_revisions WHERE portfolio_id=?),0)`, id).Scan(&current); err != nil {
			return nil, err
		}
		if current != int64Arg(args, "if_revision", -1) {
			return map[string]any{"status": "rejected", "code": "stale_state", "detail": fmt.Sprintf("account revision is %d", current)}, nil
		}
	}
	order := &Order{ID: "fixture-" + uuid.NewString(), PortfolioID: id, Symbol: strArg(args, "symbol"), AssetClass: inferAssetClass(strArg(args, "symbol")), Side: strArg(args, "side"), Type: strArg(args, "type"), Qty: floatArg(args, "qty", 0), TIF: "day", Status: "working", Rationale: strArg(args, "rationale"), Source: "app:strategies"}
	tx, err := p.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = dbInsertOrder(tx, order, pid); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE orders SET origin_app='strategies',origin_install_id=10,decision_ref=?,external_strategy_id=?,external_strategy_version=? WHERE id=?`, strArg(args, "decision_ref"), int64Arg(args, "external_strategy_id", 0), intArg(args, "external_strategy_version", 1), order.ID); err != nil {
		return nil, err
	}
	if key != "" {
		if _, err = tx.Exec(`INSERT INTO order_requests(project_id,portfolio_id,request_key,intent_hash,order_id) VALUES(?,?,?,?,?)`, pid, id, key, "fixture", order.ID); err != nil {
			return nil, err
		}
	}
	var revision int64
	if err = tx.QueryRow(`SELECT COALESCE((SELECT revision FROM portfolio_revisions WHERE portfolio_id=?),0)`, id).Scan(&revision); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if err = tryFill(p.engine, order); err != nil && !strings.Contains(err.Error(), "cash") {
		return nil, err
	}
	stored, err := dbGetOrder(p.db, pid, order.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"order_id": stored.ID, "status": stored.Status, "revision": revision}, nil
}

var errFixtureUnavailable = errors.New("Trading timed out")

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func observationSetJSON(set map[string]bool) string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

func stringSliceValue(raw any) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		return values, true
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			s, ok := value.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case nil:
		return nil, true
	}
	return nil, false
}
