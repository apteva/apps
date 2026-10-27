package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

var legacyImportTables = []string{"strategies", "strategy_versions", "portfolio_strategy_assignments", "strategy_scorecard_policies", "backtest_runs", "backtest_events", "backtest_snapshots", "backtest_market_bars", "backtest_simulations", "backtest_simulation_outputs", "backtest_agent_decisions", "strategy_scorecard_evaluations", "strategy_run_events", "strategy_rebalances", "validation_suites", "validation_cases"}

func (a *App) importLegacyResearchPaged(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	legacyImportMu.Lock()
	defer legacyImportMu.Unlock()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	pages := intArg(args, "max_pages", 8)
	if pages < 1 || pages > 32 {
		return nil, errors.New("max_pages must be 1..32")
	}
	var manifest researchSnapshot
	var raw string
	err = ctx.AppDB().QueryRow("SELECT manifest_json FROM legacy_import_progress WHERE project_id=?", pid).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == sql.ErrNoRows {
		if err = callTrading(ctx, "research_legacy_export", map[string]any{"phase": "manifest"}, &manifest); err != nil {
			return nil, err
		}
		if manifest.Version != 2 || manifest.ProjectID != pid || manifest.SourceInstallID <= 0 {
			return nil, errors.New("invalid paged legacy manifest")
		}
		var imported int
		if err = ctx.AppDB().QueryRow("SELECT COUNT(*) FROM research_imports WHERE project_id=? AND source_install_id=?", pid, manifest.SourceInstallID).Scan(&imported); err != nil {
			return nil, err
		}
		if imported > 0 {
			return map[string]any{"status": "already_imported"}, nil
		}
		if len(manifest.TableNames) != len(legacyImportTables) {
			return nil, errors.New("unexpected legacy table set")
		}
		for i, t := range legacyImportTables {
			if manifest.TableNames[i] != t {
				return nil, errors.New("unexpected legacy table order")
			}
			var n int
			if err = ctx.AppDB().QueryRow("SELECT COUNT(*) FROM " + t).Scan(&n); err != nil {
				return nil, err
			}
			if n > 0 {
				return nil, errors.New("legacy import requires empty research tables")
			}
		}
		for _, id := range manifest.PortfolioIDs {
			if err = syncTradingPortfolio(ctx, pid, id); err != nil {
				return nil, err
			}
		}
		b, _ := json.Marshal(manifest)
		raw = string(b)
		if _, err = ctx.AppDB().Exec("INSERT INTO legacy_import_progress(source_install_id,project_id,manifest_json)VALUES(?,?,?)", manifest.SourceInstallID, pid, raw); err != nil {
			return nil, err
		}
	} else if err = json.Unmarshal([]byte(raw), &manifest); err != nil {
		return nil, err
	}
	var imported int
	if err = ctx.AppDB().QueryRow("SELECT COUNT(*) FROM research_imports WHERE project_id=? AND source_install_id=?", pid, manifest.SourceInstallID).Scan(&imported); err != nil {
		return nil, err
	}
	if imported > 0 {
		return map[string]any{"status": "already_imported"}, nil
	}
	for i := 0; i < pages; i++ {
		var index int
		var cursor, total int64
		if err = ctx.AppDB().QueryRow("SELECT table_index,row_cursor,rows_imported FROM legacy_import_progress WHERE source_install_id=? AND project_id=?", manifest.SourceInstallID, pid).Scan(&index, &cursor, &total); err != nil {
			return nil, err
		}
		if index == len(legacyImportTables) {
			for _, table := range legacyImportTables {
				var n int64
				if err = ctx.AppDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
					return nil, err
				}
				if n != manifest.RowCounts[table] {
					return nil, fmt.Errorf("legacy %s count mismatch: got %d want %d", table, n, manifest.RowCounts[table])
				}
			}
			if _, err = ctx.AppDB().Exec("INSERT INTO research_imports(source_install_id,project_id)VALUES(?,?)", manifest.SourceInstallID, pid); err != nil {
				return nil, err
			}
			return map[string]any{"status": "imported", "rows_imported": total, "assignments": "paused"}, nil
		}
		table := legacyImportTables[index]
		var page researchSnapshot
		if err = callTrading(ctx, "research_legacy_export", map[string]any{"phase": "table", "table": table, "after_row_id": cursor, "limit": 2000}, &page); err != nil {
			return nil, err
		}
		rows, hasTable := page.Tables[table]
		if !hasTable || page.Version != 2 || page.ProjectID != pid || page.SourceInstallID != manifest.SourceInstallID || page.NextRowID < cursor || (!page.Done && page.NextRowID == cursor) || len(page.Tables) != 1 {
			return nil, errors.New("invalid or nonadvancing legacy page")
		}
		for _, row := range rows {
			if table == "portfolio_strategy_assignments" {
				row["status"] = "paused"
			}
			if (table == "backtest_runs" || table == "validation_suites") && strArg(row, "status") == "running" {
				row["status"] = "paused"
			}
		}
		nextIndex, nextCursor := index, page.NextRowID
		if page.Done {
			nextIndex++
			nextCursor = 0
		}
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			return nil, err
		}
		if err = writeResearchRows(tx, table, rows); err == nil {
			_, err = tx.Exec("UPDATE legacy_import_progress SET table_index=?,row_cursor=?,rows_imported=rows_imported+? WHERE source_install_id=? AND project_id=?", nextIndex, nextCursor, len(rows), manifest.SourceInstallID, pid)
		}
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	var index int
	var total int64
	if err = ctx.AppDB().QueryRow("SELECT table_index,rows_imported FROM legacy_import_progress WHERE source_install_id=? AND project_id=?", manifest.SourceInstallID, pid).Scan(&index, &total); err != nil {
		return nil, err
	}
	return map[string]any{"status": "importing", "tables_completed": index, "tables_total": len(legacyImportTables), "rows_imported": total, "assignments": "paused"}, nil
}
