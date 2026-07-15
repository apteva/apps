package main

import (
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

func TestExpiredPolymarketMigrationOnlyCleansBootstrapState(t *testing.T) {
	ctx := newTestCtx(t)
	bootstrapID := mustCreatePortfolio(t, ctx, "Demo Crypto", []string{"polymarket"})
	userID := mustCreatePortfolio(t, ctx, "User markets", []string{"polymarket"})
	if _, err := dbInsertJournal(ctx.AppDB(), "test-proj", bootstrapID, "note", "bootstrap", map[string]any{"source": "bootstrap"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbWatchlistAdd(ctx.AppDB(), "test-proj", bootstrapID, "POLY:btc-100k-2026"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbWatchlistAdd(ctx.AppDB(), "test-proj", userID, "POLY:fed-cut-march"); err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"POLY:btc-100k-2026", "POLY:fed-cut-march"} {
		no := 0.5
		if err := dbUpsertMark(ctx.AppDB(), &Mark{
			Symbol: symbol, AssetClass: "polymarket", Price: 0.5, NoPrice: &no,
			MarkedAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}

	migration, err := os.ReadFile("migrations/011_remove_expired_polymarket_bootstrap.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	bootstrapWatchlist, _ := dbWatchlist(ctx.AppDB(), bootstrapID)
	if len(bootstrapWatchlist) != 0 {
		t.Fatalf("bootstrap watchlist = %v", bootstrapWatchlist)
	}
	if _, err := dbGetMark(ctx.AppDB(), "POLY:btc-100k-2026"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("orphaned bootstrap mark was not removed: %v", err)
	}
	userWatchlist, _ := dbWatchlist(ctx.AppDB(), userID)
	if len(userWatchlist) != 1 || userWatchlist[0] != "POLY:fed-cut-march" {
		t.Fatalf("user watchlist changed: %v", userWatchlist)
	}
	if _, err := dbGetMark(ctx.AppDB(), "POLY:fed-cut-march"); err != nil {
		t.Fatalf("user mark removed: %v", err)
	}
}
