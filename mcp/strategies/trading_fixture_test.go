package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/google/uuid"
	"strings"
)

// This fixture models the Trading boundary for deterministic scheduler tests.
// Real cross-process execution and caller attribution are covered separately.
type fixtureTrading struct {
	tk.BasePlatformClient
	ctx           *sdk.AppCtx
	orderRequests []map[string]any
}

func (p *fixtureTrading) CallAppResult(app, tool string, args map[string]any, out any) error {
	if app != "trading" {
		return fmt.Errorf("unexpected dependency %s", app)
	}
	switch tool {
	case "portfolio_research_snapshot":
		pid, err := resolveProjectFromArgs(args)
		if err != nil {
			return err
		}
		id := int64Arg(args, "portfolio_id", 0)
		snap := researchSnapshot{Version: 1, SourceInstallID: 11, ProjectID: pid, Tables: map[string][]map[string]any{}}
		for _, table := range []string{"portfolios", "positions", "orders", "fills", "portfolio_risk_policies", "portfolio_risk_state", "portfolio_universe_policies", "watchlist", "day_baselines"} {
			query := "SELECT * FROM " + table + " WHERE portfolio_id=?"
			if table == "portfolios" {
				query = "SELECT * FROM portfolios WHERE id=?"
			}
			rows, err := readResearchRows(p.ctx.AppDB(), query, id)
			if err != nil {
				return err
			}
			snap.Tables[table] = rows
		}
		for _, table := range []string{"marks", "instruments", "venue_execution_profiles", "securities", "security_identifiers", "security_listings", "exchange_sessions", "universe_memberships"} {
			rows, err := readResearchRows(p.ctx.AppDB(), "SELECT * FROM "+table)
			if err != nil {
				return err
			}
			snap.Tables[table] = rows
		}
		return decodeResearchValue(snap, out)
	case "order_place":
		captured := map[string]any{}
		for k, v := range args {
			captured[k] = v
		}
		p.orderRequests = append(p.orderRequests, captured)
		pid, err := resolveProjectFromArgs(args)
		if err != nil {
			return err
		}
		order := &Order{ID: "fixture-" + uuid.NewString(), PortfolioID: int64Arg(args, "portfolio_id", 0), Symbol: strArg(args, "symbol"), AssetClass: inferAssetClass(strArg(args, "symbol")), Side: strArg(args, "side"), Type: strArg(args, "type"), Qty: floatArg(args, "qty", 0), TIF: "day", Status: "working", Rationale: strArg(args, "rationale"), Source: "strategy"}
		tx, err := p.ctx.AppDB().Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = dbInsertOrder(tx, order, pid); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE orders SET strategy_id=?,strategy_version=?,origin_app='strategies',origin_install_id=10,external_strategy_id=?,external_strategy_version=? WHERE id=?`, int64Arg(args, "external_strategy_id", 0), intArg(args, "external_strategy_version", 1), int64Arg(args, "external_strategy_id", 0), intArg(args, "external_strategy_version", 1), order.ID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if err = tryFill(globalEngine, order); err != nil && !strings.Contains(err.Error(), "cash") {
			return err
		}
		stored, err := dbGetOrder(p.ctx.AppDB(), pid, order.ID)
		if err != nil {
			return err
		}
		return decodeResearchValue(map[string]any{"order_id": stored.ID, "status": stored.Status}, out)
	default:
		return fmt.Errorf("unexpected Trading tool %s", tool)
	}
}
