package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// App-only reads for apps that automate trading on top of Trading, such as
// Strategies. Each read is scoped by the calling app's identity and project:
// an app may read portfolios in its project and fills of orders it
// originated. Nothing here knows about strategies.

const (
	portfolioStateVersion    = 1
	maxStateSymbols          = 512
	maxReferenceSymbols      = 2048
	referencePageRows        = 5000
	defaultFillsFeedLimit    = 500
	maxFillsFeedLimit        = 5000
	portfolioStateReadTrials = 3
)

// appReadCaller resolves the calling app and the project it may read.
func appReadCaller(call context.Context, args map[string]any) (*sdk.Caller, string, error) {
	caller := sdk.CallerFrom(call)
	if caller == nil || caller.AgentID != 0 || caller.AppName == "" || caller.AppInstallID == 0 {
		return nil, "", errors.New("authenticated app caller required")
	}
	if caller.ProjectID != "" {
		args["_project_id"] = caller.ProjectID
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, "", err
	}
	// A project-scoped Trading install serves only its own project.
	if caller.ProjectID != "" && caller.ProjectID != pid {
		return nil, "", errors.New("caller project does not match this Trading installation")
	}
	return caller, pid, nil
}

func readSymbols(args map[string]any, max int) ([]string, error) {
	raw, exists := args["symbols"]
	if !exists || raw == nil {
		return nil, nil
	}
	values, ok := stringSliceValue(raw)
	if !ok || len(values) > max {
		return nil, fmt.Errorf("symbols must be an array of at most %d symbols", max)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if len(value) > 256 {
			return nil, errors.New("symbol exceeds 256 bytes")
		}
		if symbol := canonicalSymbol(value); symbol != "" && !seen[symbol] {
			seen[symbol] = true
			out = append(out, symbol)
		}
	}
	sort.Strings(out)
	return out, nil
}

// PortfolioState is the account view an automating app sizes orders from.
// It has no order or fill history; its size is bounded by positions plus the
// requested symbols.
type PortfolioState struct {
	Version           int                              `json:"version"`
	SourceInstallID   int64                            `json:"source_install_id"`
	Revision          int64                            `json:"revision"`
	ObservedAt        string                           `json:"observed_at"`
	Portfolio         *Portfolio                       `json:"portfolio"`
	Config            map[string]any                   `json:"config"`
	ExecutionSettings PortfolioExecutionSettings       `json:"execution_settings"`
	Equity            float64                          `json:"equity"`
	DayBaseline       *float64                         `json:"day_baseline,omitempty"`
	Positions         []*Position                      `json:"positions"`
	WorkingOrders     []*Order                         `json:"working_orders"`
	Marks             map[string]*Mark                 `json:"marks"`
	Instruments       map[string]*Instrument           `json:"instruments"`
	Profiles          map[string]VenueExecutionProfile `json:"profiles"`
	RiskPolicy        *PortfolioRiskPolicy             `json:"risk_policy"`
	UniversePolicy    *PortfolioUniversePolicy         `json:"universe_policy"`
	Violations        map[string]*UniverseViolation    `json:"violations"`
}

func toolPortfolioState(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, pid, err := appReadCaller(call, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	if id <= 0 {
		return nil, errors.New("portfolio_id required")
	}
	symbols, err := readSymbols(args, maxStateSymbols)
	if err != nil {
		return nil, err
	}
	return readPortfolioState(call, ctx.AppReadDB(), pid, id, symbols)
}

// readPortfolioState composes the state from Trading's own helpers and
// accepts it only if the account revision did not move while reading. Cash,
// positions and order placement or resolution all bump the revision.
func readPortfolioState(call context.Context, db *sql.DB, pid string, id int64, symbols []string) (*PortfolioState, error) {
	for trial := 0; trial < portfolioStateReadTrials; trial++ {
		if err := call.Err(); err != nil {
			return nil, err
		}
		before, err := accountRevision(db, id)
		if err != nil {
			return nil, err
		}
		state, err := composePortfolioState(db, pid, id, symbols)
		if err != nil {
			return nil, err
		}
		after, err := accountRevision(db, id)
		if err != nil {
			return nil, err
		}
		if before == after {
			state.Revision = after
			state.SourceInstallID, _ = strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
			return state, nil
		}
	}
	return nil, errors.New("stale_state: portfolio kept changing while it was read; retry")
}

func composePortfolioState(db *sql.DB, pid string, id int64, symbols []string) (*PortfolioState, error) {
	pf, err := dbGetPortfolio(db, pid, id)
	if err != nil {
		return nil, err
	}
	state := &PortfolioState{
		Version: portfolioStateVersion, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Portfolio: pf,
		Marks: map[string]*Mark{}, Instruments: map[string]*Instrument{},
		Profiles: map[string]VenueExecutionProfile{}, Violations: map[string]*UniverseViolation{},
	}
	if state.Config, err = dbPortfolioConfig(db, id); err != nil {
		return nil, err
	}
	state.ExecutionSettings = dbPortfolioExecutionSettings(db, id)
	if state.Equity, err = computeEquity(db, pf); err != nil {
		return nil, err
	}
	if baseline, ok, err := dbGetDayBaseline(db, id, time.Now().UTC().Format("2006-01-02")); err != nil {
		return nil, err
	} else if ok {
		state.DayBaseline = &baseline
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
	for _, symbol := range symbols {
		all[symbol] = true
	}
	for _, p := range state.Positions {
		all[canonicalSymbol(p.Symbol)] = true
	}
	for _, o := range state.WorkingOrders {
		all[canonicalSymbol(o.Symbol)] = true
	}
	for symbol := range all {
		if symbol == "" {
			continue
		}
		if mark, err := dbGetMark(db, symbol); err == nil && mark != nil {
			state.Marks[symbol] = mark
		}
		if instrument, err := dbGetInstrument(db, symbol); err == nil && instrument != nil {
			state.Instruments[symbol] = instrument
		}
		state.Profiles[symbol] = resolveVenueProfile(db, pf, symbol, inferAssetClass(symbol))
		violation, err := universeViolation(db, state.UniversePolicy, symbol)
		if err != nil {
			return nil, err
		}
		if violation != nil {
			state.Violations[symbol] = violation
		}
	}
	if state.Positions == nil {
		state.Positions = []*Position{}
	}
	if state.WorkingOrders == nil {
		state.WorkingOrders = []*Order{}
	}
	return state, nil
}

func toolPortfolioPolicyCheck(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, pid, err := appReadCaller(call, args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppReadDB()
	pf, err := dbGetPortfolio(db, pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, err
	}
	symbols, err := readSymbols(args, maxReferenceSymbols)
	if err != nil {
		return nil, err
	}
	at := time.Now().UTC()
	if raw := strings.TrimSpace(strArg(args, "as_of")); raw != "" {
		if at, err = time.Parse(time.RFC3339, raw); err != nil {
			return nil, errors.New("as_of must be RFC3339")
		}
	}
	policy, err := dbGetPortfolioUniversePolicy(db, pf)
	if err != nil {
		return nil, err
	}
	violations := map[string]*UniverseViolation{}
	for _, symbol := range symbols {
		violation, err := universeViolationAt(db, policy, symbol, at.UTC())
		if err != nil {
			return nil, err
		}
		if violation != nil {
			violations[symbol] = violation
		}
	}
	return map[string]any{"portfolio_id": pf.ID, "as_of": at.UTC().Format(time.RFC3339), "universe_policy": policy, "violations": violations}, nil
}

// research_reference pages through reference data for a backtest window in
// a fixed table order. The cursor is "<table index>:<rowid>".
var referenceTables = []struct {
	name, query string
}{
	{"exchange_sessions", `SELECT rowid AS _row,venue,session_date,session_type,open_at,close_at,status,source FROM exchange_sessions
		WHERE session_date BETWEEN ? AND ? AND rowid>? ORDER BY rowid LIMIT ?`},
	{"corporate_actions", `SELECT rowid AS _row,provider,provider_event_id,revision,action_type,status,security_id,symbol,new_symbol,ex_date,effective_date,process_date FROM corporate_actions
		WHERE COALESCE(NULLIF(effective_date,''),NULLIF(ex_date,''),process_date) BETWEEN ? AND ? AND (symbol IN (SELECT value FROM json_each(?)) OR new_symbol IN (SELECT value FROM json_each(?))) AND rowid>? ORDER BY rowid LIMIT ?`},
	{"security_listings", `SELECT rowid AS _row,security_id,venue,symbol,currency,valid_from,valid_to,active,source,updated_at FROM security_listings
		WHERE symbol IN (SELECT value FROM json_each(?)) AND rowid>? ORDER BY rowid LIMIT ?`},
	{"universe_memberships", `SELECT rowid AS _row,universe_id,security_id,valid_from,valid_to,source FROM universe_memberships
		WHERE security_id IN (SELECT security_id FROM security_listings WHERE symbol IN (SELECT value FROM json_each(?))) AND rowid>? ORDER BY rowid LIMIT ?`},
}

func toolResearchReference(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, _, err := appReadCaller(call, args); err != nil {
		return nil, err
	}
	symbols, err := readSymbols(args, maxReferenceSymbols)
	if err != nil {
		return nil, err
	}
	start, end := strings.TrimSpace(strArg(args, "start")), strings.TrimSpace(strArg(args, "end"))
	for _, day := range []string{start, end} {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return nil, errors.New("start and end must be YYYY-MM-DD dates")
		}
	}
	table, after := 0, int64(0)
	if cursor := strings.TrimSpace(strArg(args, "cursor")); cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d:%d", &table, &after); err != nil || table < 0 || table >= len(referenceTables) {
			return nil, errors.New("invalid research_reference cursor")
		}
	}
	symbolSet := observationSetJSON(stringSet(symbols))
	db := ctx.AppReadDB()
	out := map[string]any{"start": start, "end": end, "adjustment": strArg(args, "adjustment")}
	tables := map[string][]map[string]any{}
	if table == 0 && after == 0 {
		out["status"] = referenceDataStatus(db)
		ids := map[string]string{}
		for _, symbol := range symbols {
			var security string
			if db.QueryRowContext(call, `SELECT security_id FROM security_listings WHERE symbol=? ORDER BY active DESC,updated_at DESC LIMIT 1`, symbol).Scan(&security) == nil {
				ids[symbol] = security
			}
		}
		out["security_ids"] = ids
	}
	remaining := referencePageRows
	for ; table < len(referenceTables) && remaining > 0; table, after = table+1, 0 {
		spec := referenceTables[table]
		var params []any
		switch spec.name {
		case "exchange_sessions":
			params = []any{start, end}
		case "corporate_actions":
			params = []any{start, end, symbolSet, symbolSet}
		default:
			params = []any{symbolSet}
		}
		rows, err := db.QueryContext(call, spec.query, append(params, after, remaining+1)...)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", spec.name, err)
		}
		var page []map[string]any
		if err := scanResearchRows(rows, func(row map[string]any) error {
			page = append(page, row)
			return nil
		}); err != nil {
			return nil, err
		}
		if len(page) > remaining {
			page = page[:remaining]
			last := page[len(page)-1]
			after = int64Arg(last, "_row", 0)
			tables[spec.name] = stripRowIDs(page)
			out["tables"], out["next_cursor"] = tables, fmt.Sprintf("%d:%d", table, after)
			return out, nil
		}
		remaining -= len(page)
		tables[spec.name] = stripRowIDs(page)
	}
	out["tables"] = tables
	if table < len(referenceTables) {
		out["next_cursor"] = fmt.Sprintf("%d:%d", table, 0)
	} else {
		out["next_cursor"] = ""
	}
	return out, nil
}

func stripRowIDs(rows []map[string]any) []map[string]any {
	for _, row := range rows {
		delete(row, "_row")
	}
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// FeedFill is one fill of an order the calling app originated.
type FeedFill struct {
	ID                      int64   `json:"id"`
	OrderID                 string  `json:"order_id"`
	PortfolioID             int64   `json:"portfolio_id"`
	Symbol                  string  `json:"symbol"`
	AssetClass              string  `json:"asset_class"`
	Side                    string  `json:"side"`
	Outcome                 string  `json:"outcome"`
	Qty                     float64 `json:"qty"`
	Price                   float64 `json:"price"`
	Fee                     float64 `json:"fee"`
	SpreadCost              float64 `json:"spread_cost"`
	SlippageCost            float64 `json:"slippage_cost"`
	FilledAt                string  `json:"filled_at"`
	DecisionRef             string  `json:"decision_ref"`
	ExternalStrategyID      int64   `json:"external_strategy_id"`
	ExternalStrategyVersion int64   `json:"external_strategy_version"`
}

func toolFillsFeed(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	caller, pid, err := appReadCaller(call, args)
	if err != nil {
		return nil, err
	}
	// Orders Trading's own strategy engine placed before the split carry
	// strategy_id and no origin_app. Strategies imported those strategies, so
	// it may ask for their fills too; no other app can.
	legacy, _ := args["include_legacy"].(bool)
	if legacy {
		if err := requireStrategiesCaller(call); err != nil {
			return nil, err
		}
	}
	after := int64Arg(args, "after_fill_id", 0)
	limit := intArg(args, "limit", defaultFillsFeedLimit)
	if limit <= 0 || limit > maxFillsFeedLimit {
		limit = defaultFillsFeedLimit
	}
	rows, err := ctx.AppReadDB().QueryContext(call, `
		SELECT f.id,f.order_id,f.portfolio_id,o.symbol,o.asset_class,o.side,COALESCE(o.outcome,''),f.qty,f.price,f.fee,
		       COALESCE(f.spread_cost,0),COALESCE(f.slippage_cost,0),COALESCE(f.filled_at,''),
		       COALESCE(o.decision_ref,''),
		       CASE WHEN o.origin_app='' THEN COALESCE(o.strategy_id,0) ELSE COALESCE(o.external_strategy_id,0) END,
		       CASE WHEN o.origin_app='' THEN COALESCE(o.strategy_version,0) ELSE COALESCE(o.external_strategy_version,0) END
		FROM fills f JOIN orders o ON o.id=f.order_id
		WHERE f.project_id=? AND f.id>? AND (o.origin_app=? OR (? AND o.origin_app='' AND COALESCE(o.strategy_id,0)>0))
		ORDER BY f.id LIMIT ?`, pid, after, caller.AppName, legacy, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fills := []FeedFill{}
	for rows.Next() {
		var f FeedFill
		if err := rows.Scan(&f.ID, &f.OrderID, &f.PortfolioID, &f.Symbol, &f.AssetClass, &f.Side, &f.Outcome, &f.Qty, &f.Price, &f.Fee,
			&f.SpreadCost, &f.SlippageCost, &f.FilledAt, &f.DecisionRef, &f.ExternalStrategyID, &f.ExternalStrategyVersion); err != nil {
			return nil, err
		}
		fills = append(fills, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
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

func (a *App) appReadTools() []sdk.Tool {
	symbols := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return []sdk.Tool{
		{Name: "portfolio_state", Exposure: sdk.ToolExposureAppOnly,
			Description: "Read one portfolio's current account state for automated sizing: cash, equity, positions, working orders, marks, venue profiles, policies and the account revision. No history.",
			InputSchema: schemaObject(map[string]any{"portfolio_id": map[string]any{"type": "integer"}, "symbols": symbols}, []string{"portfolio_id"}),
			HandlerCtx:  toolPortfolioState},
		{Name: "portfolio_policy_check", Exposure: sdk.ToolExposureAppOnly,
			Description: "Check symbols against a portfolio's universe and listing policy at a point in time.",
			InputSchema: schemaObject(map[string]any{"portfolio_id": map[string]any{"type": "integer"}, "symbols": symbols, "as_of": map[string]any{"type": "string"}}, []string{"portfolio_id", "symbols"}),
			HandlerCtx:  toolPortfolioPolicyCheck},
		{Name: "research_reference", Exposure: sdk.ToolExposureAppOnly,
			Description: "Page through exchange sessions, corporate actions, listings and universe memberships for a research window. Continue with next_cursor until it is empty.",
			InputSchema: schemaObject(map[string]any{"symbols": symbols, "start": map[string]any{"type": "string"}, "end": map[string]any{"type": "string"}, "adjustment": map[string]any{"type": "string"}, "cursor": map[string]any{"type": "string"}}, []string{"symbols", "start", "end"}),
			HandlerCtx:  toolResearchReference},
		{Name: "fills_feed", Exposure: sdk.ToolExposureAppOnly,
			Description: "Page through fills of orders the calling app placed, oldest first. Continue with after_fill_id=next_fill_id while has_more.",
			InputSchema: schemaObject(map[string]any{"after_fill_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}, "include_legacy": map[string]any{"type": "boolean"}}, nil),
			HandlerCtx:  toolFillsFeed},
	}
}
