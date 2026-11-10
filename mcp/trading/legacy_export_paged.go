package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Dependency order lets an importer commit bounded pages without disabling
// foreign keys. The source's legacy research is immutable after the split.
var legacyTableOrder = []string{"strategies", "strategy_versions", "portfolio_strategy_assignments", "strategy_scorecard_policies", "backtest_runs", "backtest_events", "backtest_snapshots", "backtest_market_bars", "backtest_simulations", "backtest_simulation_outputs", "backtest_agent_decisions", "strategy_scorecard_evaluations", "strategy_run_events", "strategy_rebalances", "validation_suites", "validation_cases"}

// A legacy replay bar may reference a historical security that is absent from
// every current portfolio observation. Carry those exact parents with the bars.
func legacyBarSecurities(db researchQueryer, bars []map[string]any) ([]map[string]any, error) {
	ids := map[string]bool{}
	for _, bar := range bars {
		if id := strArg(bar, "security_id"); id != "" {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return readResearchRows(db, `SELECT * FROM securities WHERE id IN (SELECT value FROM json_each(?))`, observationSetJSON(ids))
}

func exportLegacyPage(ctx *sdk.AppCtx, pid string, install int64, queries map[string]string, args map[string]any) (*researchSnapshot, error) {
	out := &researchSnapshot{Version: 2, SourceInstallID: install, ProjectID: pid, Tables: map[string][]map[string]any{}}
	switch strArg(args, "phase") {
	case "manifest":
		out.TableNames = legacyTableOrder
		out.RowCounts = map[string]int64{}
		for _, table := range legacyTableOrder {
			var count int64
			if err := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM ("+queries[table]+")", pid).Scan(&count); err != nil {
				return nil, err
			}
			out.RowCounts[table] = count
		}
		rows, err := ctx.AppDB().Query("SELECT id FROM portfolios WHERE project_id=? ORDER BY id", pid)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				return nil, err
			}
			out.PortfolioIDs = append(out.PortfolioIDs, id)
		}
		return out, rows.Err()
	case "table":
		table := strArg(args, "table")
		query, ok := queries[table]
		if !ok {
			return nil, errors.New("unknown legacy research table")
		}
		cursor := int64Arg(args, "after_row_id", 0)
		limit := intArg(args, "limit", 2000)
		if cursor < 0 || limit < 1 || limit > 2000 {
			return nil, errors.New("invalid legacy export cursor or page size")
		}
		// Force a rowid range scan: choosing the run_id index sorts the entire
		// multi-run archive for every page and defeats keyset pagination.
		query = strings.Replace(query, "FROM "+table+" WHERE", "FROM "+table+" NOT INDEXED WHERE", 1)
		query = strings.Replace(query, "SELECT * FROM ", "SELECT rowid AS _migration_rowid, * FROM ", 1) + " AND rowid>? ORDER BY rowid LIMIT ?"
		rows, err := readResearchRows(ctx.AppDB(), query, pid, cursor, limit)
		if err != nil {
			return nil, err
		}
		refs := map[string]map[string]any{}
		if table == "backtest_market_bars" {
			securities, err := legacyBarSecurities(ctx.AppDB(), rows)
			if err != nil {
				return nil, err
			}
			for _, security := range securities {
				refs[strArg(security, "id")] = security
			}
		}
		selected := []map[string]any{}
		size := 0
		out.NextRowID = cursor
		out.Done = len(rows) < limit
		for _, row := range rows {
			next := int64Arg(row, "_migration_rowid", 0)
			delete(row, "_migration_rowid")
			b, err := json.Marshal(row)
			if err != nil {
				return nil, err
			}
			id := strArg(row, "security_id")
			security := refs[id]
			rowBytes := len(b) + 1
			if security != nil {
				encoded, err := json.Marshal(security)
				if err != nil {
					return nil, err
				}
				rowBytes += len(encoded) + 1
			}
			const budget = (4 << 20) - 2048
			if rowBytes > budget {
				return nil, fmt.Errorf("legacy %s row %d exceeds 4 MiB transfer limit", table, next)
			}
			if size+rowBytes > budget {
				out.Done = false
				break
			}
			if security != nil {
				out.LegacySecurities = append(out.LegacySecurities, security)
				delete(refs, id)
			}
			size += rowBytes
			selected = append(selected, row)
			out.NextRowID = next
		}
		out.Tables[table] = selected
		return out, nil
	default:
		return nil, errors.New("legacy export phase must be manifest or table")
	}
}
