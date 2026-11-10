package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPortfolioObservationScopesSharedCatalog(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	id := mustCreatePortfolio(t, ctx, "Observed", []string{"equity"})
	other := mustCreatePortfolio(t, ctx, "Other", []string{"equity"})
	run := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a production-sized catalog without adding any portfolio exposure.
	run(`WITH RECURSIVE catalog(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM catalog WHERE n<100000)
		INSERT INTO securities(id,asset_class,source) SELECT 'unrelated-'||n,'equity','fixture' FROM catalog`)
	run(`INSERT INTO security_listings(security_id,venue,symbol,source)
		SELECT id,'US',id,'fixture' FROM securities`)
	for _, symbol := range []string{"AAPL", "MSFT", "NVDA", "OUTSIDE"} {
		run(`INSERT INTO securities(id,asset_class,source) VALUES (?,'equity','fixture')`, symbol)
		run(`INSERT INTO security_listings(security_id,venue,symbol,source) VALUES (?,'US',?,'fixture')`, symbol, symbol)
		run(`INSERT INTO security_identifiers(security_id,identifier_type,identifier_value,source) VALUES (?,'ticker',?,'fixture')`, symbol, symbol)
		mark := &Mark{Symbol: symbol, AssetClass: "equity", Price: 100, Source: "fixture", MarkedAt: time.Now().UTC().Format(time.RFC3339)}
		if err := dbUpsertMark(db, mark); err != nil {
			t.Fatal(err)
		}
		run(`INSERT INTO universe_memberships(universe_id,security_id,valid_from,source) VALUES ('relevant',?,'2024-01-01','fixture'),('other',?,'2024-01-01','fixture')`, symbol, symbol)
	}
	insertPosition(t, ctx, id, "MSFT", 2, 100)
	insertPosition(t, ctx, other, "OUTSIDE", 2, 100)
	insertObservationOrder(t, ctx.AppDB(), id, "o-observed", "NVDA", "An observed order")
	insertObservationOrder(t, ctx.AppDB(), other, "o-outside", "OUTSIDE", "An unrelated order")
	run(`INSERT INTO portfolio_universe_policies(portfolio_id,project_id,selection_mode,reference_universe_id)
		VALUES (?,'test-proj','reference_universe','relevant')`, id)
	for _, venue := range []string{"US_EQUITY", "UNRELATED"} {
		run(`INSERT INTO exchange_sessions(venue,session_date,source) VALUES (?,?, 'fixture'),(?,'2006-01-01','fixture')`,
			venue, time.Now().UTC().Format(time.DateOnly), venue)
	}
	for _, profile := range [][2]string{{"simulation", "*"}, {"simulation", "AAPL"}, {"simulation", "OUTSIDE"}, {"alpaca-trading", "AAPL"}} {
		run(`INSERT INTO venue_execution_profiles(venue_slug,asset_class,symbol) VALUES (?,'equity',?)`, profile[0], profile[1])
	}

	raw, err := exportPortfolioObservation(context.Background(), ctx, map[string]any{"portfolio_id": id, "symbols": []string{"aapl"}, "paged": true}, 11)
	if err != nil {
		t.Fatal(err)
	}
	page := decodeObservationTestPage(t, raw)
	if !page.Done || len(raw) >= 32<<10 {
		t.Fatalf("small scoped observation took %d bytes, done=%v", len(raw), page.Done)
	}
	for table, want := range map[string]int{
		"portfolios": 1, "positions": 1, "orders": 1, "marks": 3, "instruments": 3, "securities": 3,
		"security_identifiers": 3, "security_listings": 3, "universe_memberships": 3, "exchange_sessions": 1, "venue_execution_profiles": 2,
	} {
		if got := len(page.Tables[table]); got != want {
			t.Errorf("%s: got %d rows, want %d", table, got, want)
		}
	}
	if strings.Contains(string(raw), "OUTSIDE") || strings.Contains(string(raw), "unrelated-") {
		t.Fatal("observation included unrelated reference rows")
	}
	historical, err := exportPortfolioObservation(context.Background(), ctx, map[string]any{
		"portfolio_id": id, "session_start": "2006-01-01", "session_end": "2006-01-02", "paged": true,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if sessions := decodeObservationTestPage(t, historical).Tables["exchange_sessions"]; len(sessions) != 1 || !strings.Contains(string(sessions[0]), "2006-01-01") {
		t.Fatalf("historical observation missed its requested exchange session: %s", sessions)
	}
}

func TestPortfolioObservationPagesAreBoundedAndImmutable(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Large observation", []string{"equity"})
	// Two rows like this cannot share a page. Quotes also stress JSON escaping
	// in the gateway's MCP envelope.
	rationale := strings.Repeat(`"`, 1100000)
	for i := 1; i <= 3; i++ {
		insertObservationOrder(t, ctx.AppDB(), id, fmt.Sprintf("o-%d", i), "AAPL", rationale)
	}
	capture := func(args map[string]any, caller int64) (json.RawMessage, error) {
		return exportPortfolioObservation(context.Background(), ctx, args, caller)
	}
	if _, err := capture(map[string]any{"portfolio_id": id}, 11); err == nil {
		t.Fatal("unpaged caller received a partial observation")
	}
	first, err := capture(map[string]any{"portfolio_id": id, "paged": true}, 11)
	if err != nil {
		t.Fatal(err)
	}
	page := decodeObservationTestPage(t, first)
	if page.Version != portfolioObservationVersion || page.Page != 1 || page.Revision == "" || page.Done {
		t.Fatal("expected a revision with a continuation")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE orders SET rationale='changed after capture' WHERE portfolio_id=?`, id); err != nil {
		t.Fatal(err)
	}
	count := 0
	for {
		for _, row := range page.Tables["orders"] {
			var order struct {
				Rationale string `json:"rationale"`
			}
			if err := json.Unmarshal(row, &order); err != nil || order.Rationale != rationale {
				t.Fatal("a page did not contain the original revision")
			}
			count++
		}
		if page.Done {
			break
		}
		args := map[string]any{"portfolio_id": id, "revision": page.Revision, "page": page.Page + 1}
		if _, err := capture(args, 12); err == nil {
			t.Fatal("another caller could read the captured observation")
		}
		raw, err := capture(args, 11)
		if err != nil {
			t.Fatal(err)
		}
		next := decodeObservationTestPage(t, raw)
		if next.Revision != page.Revision || next.Page != page.Page+1 {
			t.Fatal("continuation changed revision or page number")
		}
		page = next
	}
	if count != 3 {
		t.Fatalf("got %d of 3 orders", count)
	}
}

func insertObservationOrder(t *testing.T, db *sql.DB, pf int64, id, symbol, rationale string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	o := &Order{ID: id, PortfolioID: pf, Symbol: symbol, AssetClass: "equity", Side: "buy", Type: "market", Qty: 1, TIF: "gtc", Status: "working", Rationale: rationale, Source: "fixture"}
	if err := dbInsertOrder(tx, o, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func decodeObservationTestPage(t *testing.T, raw json.RawMessage) portfolioObservationPage {
	t.Helper()
	if len(raw) > 4<<20 {
		t.Fatalf("page exceeds 4 MiB: %d", len(raw))
	}
	wrapped, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": string(raw)}}})
	if err != nil || len(wrapped) >= 16<<20 {
		t.Fatalf("MCP envelope exceeds gateway limit: %d, %v", len(wrapped), err)
	}
	var page portfolioObservationPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page
}
