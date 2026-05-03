package main

// Tier 1 tests for the auto-fill on first install. Cover both the
// "no portfolios → bootstrap" path and the "already populated →
// no-op" idempotency guard.

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// stubEnv installs a temporary lookupEnv override + restores on cleanup.
func stubEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := lookupEnv
	lookupEnv = func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
	t.Cleanup(func() { lookupEnv = prev })
}

func TestBootstrap_CreatesDemoOnEmptyProject(t *testing.T) {
	stubEnv(t, map[string]string{"APTEVA_PROJECT_ID": "test-proj"})
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"bootstrap_demo":      "true",
			"bootstrap_watchlist": "BTC-USD,ETH-USD,POLY:btc-100k-2026",
			"starting_cash":       "50000",
		}),
	)
	if err := bootstrapIfEmpty(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	pfs, err := dbListPortfolios(ctx.AppDB(), "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(pfs) != 1 {
		t.Fatalf("expected 1 portfolio, got %d", len(pfs))
	}
	p := pfs[0]
	if p.Name != bootstrapPortfolioName {
		t.Errorf("name=%q, want %q", p.Name, bootstrapPortfolioName)
	}
	if p.StartingCash != 50_000 {
		t.Errorf("starting_cash=%v, want 50000", p.StartingCash)
	}
	if !contains(p.AllowedClasses, "crypto") {
		t.Errorf("expected allowed_classes to include crypto, got %v", p.AllowedClasses)
	}

	// Watchlist seeded.
	wl, _ := dbWatchlist(ctx.AppDB(), p.ID)
	if len(wl) != 3 {
		t.Errorf("watchlist size=%d, want 3 (got %v)", len(wl), wl)
	}
	hasBTC := false
	for _, s := range wl {
		if s == "BTC-USD" {
			hasBTC = true
		}
	}
	if !hasBTC {
		t.Errorf("watchlist missing BTC-USD: %v", wl)
	}

	// Welcome journal entry.
	entries, _ := dbReadJournal(ctx.AppDB(), p.ID, "note", "", 10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 note journal entry, got %d", len(entries))
	}
	if !strings.Contains(strings.ToLower(entries[0].Body), "welcome") {
		t.Errorf("welcome note body unexpected: %q", entries[0].Body)
	}
	if entries[0].Metadata["source"] != "bootstrap" {
		t.Errorf("welcome metadata.source=%v, want bootstrap", entries[0].Metadata["source"])
	}
}

func TestBootstrap_NoOpWhenPortfoliosExist(t *testing.T) {
	stubEnv(t, map[string]string{"APTEVA_PROJECT_ID": "test-proj"})
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{"bootstrap_demo": "true"}),
	)
	// Pre-create one portfolio.
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
		ProjectID: "test-proj", Name: "Pre-existing",
		AllowedClasses: []string{"equity"}, StartingCash: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := bootstrapIfEmpty(ctx); err != nil {
		t.Fatal(err)
	}

	pfs, _ := dbListPortfolios(ctx.AppDB(), "test-proj")
	if len(pfs) != 1 {
		t.Errorf("expected 1 portfolio (no bootstrap), got %d", len(pfs))
	}
	if pfs[0].ID != id {
		t.Errorf("bootstrap clobbered the existing portfolio")
	}
	// No welcome note.
	entries, _ := dbReadJournal(ctx.AppDB(), id, "note", "", 10)
	if len(entries) != 0 {
		t.Errorf("expected 0 journal entries (bootstrap should have skipped), got %d", len(entries))
	}
}

func TestBootstrap_NoOpWhenFlagOff(t *testing.T) {
	stubEnv(t, map[string]string{"APTEVA_PROJECT_ID": "test-proj"})
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{"bootstrap_demo": "false"}),
	)
	if err := bootstrapIfEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	pfs, _ := dbListPortfolios(ctx.AppDB(), "test-proj")
	if len(pfs) != 0 {
		t.Errorf("expected 0 portfolios (flag off), got %d", len(pfs))
	}
}

func TestBootstrap_NoOpWhenNoProjectID(t *testing.T) {
	stubEnv(t, map[string]string{}) // no APTEVA_PROJECT_ID
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithConfig(map[string]string{"bootstrap_demo": "true"}),
	)
	if err := bootstrapIfEmpty(ctx); err != nil {
		t.Fatalf("bootstrap should not error in global scope: %v", err)
	}
	// No way to verify "no portfolios in any project" cleanly without
	// a project_id, so we just confirm we didn't panic + no error.
}

func TestBootstrap_FallsBackToDefaultStartingCashOnBadConfig(t *testing.T) {
	stubEnv(t, map[string]string{"APTEVA_PROJECT_ID": "test-proj"})
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"bootstrap_demo": "true",
			"starting_cash":  "not-a-number",
		}),
	)
	if err := bootstrapIfEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	pfs, _ := dbListPortfolios(ctx.AppDB(), "test-proj")
	if len(pfs) != 1 {
		t.Fatalf("want 1 portfolio, got %d", len(pfs))
	}
	if pfs[0].StartingCash != 100_000 {
		t.Errorf("starting_cash=%v, want default 100000", pfs[0].StartingCash)
	}
}
