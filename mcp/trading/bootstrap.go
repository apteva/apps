package main

// Auto-fill on first install. Idempotent: runs only when the project
// has zero portfolios. Safe across restarts and reinstalls.
//
// What it creates:
//   - one paper portfolio with starting cash from config
//   - the bootstrap watchlist (default: BTC-USD, ETH-USD, SOL-USD,
//     POLY:btc-100k-2026 — covers crypto + a polymarket so the
//     operator sees both UI surfaces immediately)
//   - one welcome journal entry
//
// Gated by the `bootstrap_demo` config flag (default true). Set to
// "false" to keep installs empty.

import (
	"errors"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	bootstrapPortfolioName = "Demo Crypto"
	bootstrapMandate       = "Long-only crypto. Max 25% per coin. Paper book — connect a real exchange account from Settings to add reconciliation."
)

// bootstrapIfEmpty — call from OnMount after migrations + provider
// init. Logs but never fails the boot; the app should keep working
// even if bootstrap can't run.
func bootstrapIfEmpty(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return errors.New("bootstrap: AppCtx not initialised")
	}
	if ctx.Config().Get("bootstrap_demo") == "false" {
		return nil
	}
	pid := projectIDFromEnvOnly()
	if pid == "" {
		// Global-scope install — agent must self-bootstrap. The
		// auto-fill UX is a project-scope feature.
		return nil
	}

	// Idempotency: if any portfolio exists in this project, do
	// nothing. Restarts and re-installs converge cleanly.
	pfs, err := dbListPortfolios(ctx.AppDB(), pid)
	if err != nil {
		return err
	}
	if len(pfs) > 0 {
		return nil
	}

	cash := parseFloatDefault(ctx.Config().Get("starting_cash"), 100_000)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
		ProjectID:      pid,
		Name:           bootstrapPortfolioName,
		Mandate:        bootstrapMandate,
		AllowedClasses: []string{"crypto", "polymarket"},
		StartingCash:   cash,
	})
	if err != nil {
		return err
	}

	// Watchlist seed — comma-separated, whitespace-tolerant.
	added := 0
	for _, sym := range parseCSV(ctx.Config().Get("bootstrap_watchlist")) {
		if _, err := dbWatchlistAdd(ctx.AppDB(), pid, id, sym); err != nil {
			ctx.Logger().Warn("bootstrap watchlist add failed", "symbol", sym, "err", err)
			continue
		}
		added++
	}

	// Welcome journal entry. The body deliberately mentions both
	// what's automatic ("paper book seeded on install") and what's
	// future ("connect a real exchange account from Settings"), so
	// the operator's first-look isn't a black box.
	_, _ = dbInsertJournal(ctx.AppDB(), pid, id, "note",
		"Welcome — paper book seeded on install. Live crypto + polymarket prices via public APIs (no credentials needed). Connect a Binance account from Settings later for real-account reconciliation.",
		map[string]any{"source": "bootstrap", "watchlist_seeded": added})

	ctx.Logger().Info("bootstrap complete",
		"portfolio_id", id, "watchlist", added, "starting_cash", cash)
	return nil
}

// parseCSV — splits on commas, trims each part, drops empties.
func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseFloatDefault(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && v > 0 {
		return v
	}
	return def
}

// projectIDFromEnvOnly — bootstrap deliberately does NOT honour
// args-supplied project_id. Forces project-scope-only installs to
// auto-bootstrap, which is the safe path: a global-scope install
// would ambiguously bootstrap into "which project?".
func projectIDFromEnvOnly() string {
	return strings.TrimSpace(getEnvOnce("APTEVA_PROJECT_ID"))
}

// getEnvOnce — narrow indirection so tests can inject. Production
// reads os.Getenv on every call (cheap).
var getEnvOnce = func(key string) string {
	return osGetenv(key)
}

// osGetenv split out so the test file can stub it without touching os.
func osGetenv(key string) string {
	v, _ := lookupEnv(key)
	return v
}

// lookupEnv — package-level indirection for tests. Defaults to os.LookupEnv.
var lookupEnv = func(key string) (string, bool) {
	return osLookupEnv(key)
}
