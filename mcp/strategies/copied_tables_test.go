package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The tables Strategies used to copy from Trading. Since 0.2.0 every Trading
// fact is read through Trading's tools, so no production SQL may name them;
// 0.2.1 drops them.
var copiedTradingTables = []string{
	"portfolios", "securities", "security_identifiers", "security_listings",
	"exchange_sessions", "universe_memberships", "marks", "instruments",
	"venue_execution_profiles", "portfolio_risk_policies", "portfolio_risk_state",
	"portfolio_universe_policies", "positions", "orders", "fills",
	"execution_costs", "position_accounting", "watchlist", "day_baselines",
}

func TestCopiedTradingTablesAreUnused(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range copiedTradingTables {
		sqlUse := regexp.MustCompile(`(?i)\b(FROM|JOIN|INTO|UPDATE|TABLE)\s+"?` + table + `\b`)
		literal := regexp.MustCompile(`"` + table + `"`)
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(src), "\n") {
				if sqlUse.MatchString(line) || (literal.MatchString(line) && strings.Contains(line, "writeResearchRows")) {
					t.Errorf("%s:%d still reads or writes copied Trading table %s", file, i+1, table)
				}
			}
		}
	}
}

func TestMigrationsDropTradingCopies(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	for _, table := range append(append([]string{}, copiedTradingTables...), "journal", "trading_observation_sources", "trading_observation_failures") {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("Strategies schema still has Trading table %s", table)
		}
	}
	var dangling int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master m, pragma_foreign_key_list(m.name) p
		WHERE m.type='table' AND p."table" NOT IN (SELECT name FROM sqlite_master WHERE type='table')`).Scan(&dangling); err != nil {
		t.Fatal(err)
	}
	if dangling != 0 {
		t.Fatalf("%d foreign keys point at dropped tables", dangling)
	}
	pid := mustCreatePortfolio(t, ctx, "Migrated", []string{"equity"})
	run, err := dbCreateBacktestRun(db, &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, StrategyID: mustCreateStrategy(t, ctx, "Migrated"), StrategyVersion: 1,
		RunKind: "strategy", Symbols: []string{"AAPL"}, Interval: "1h", StartingCash: 1000, TotalSteps: 1, Name: "migrated"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backtest_market_bars(run_id,step,symbol,asset_class,t,o,h,l,c,source,security_id) VALUES(?,0,'AAPL','equity',1,1,1,1,1,'test','sec-aapl')`, run); err != nil {
		t.Fatalf("frozen bar with a Trading security id: %v", err)
	}
}
