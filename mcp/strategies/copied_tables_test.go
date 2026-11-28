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
