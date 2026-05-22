package main

// Unit tests for the pure computation + the store. These never touch the
// network — the Yahoo client is exercised in integration runs, not here.

import (
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	body, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return &store{db: db}
}

func daysAgo(n int) int64 { return time.Now().AddDate(0, 0, -n).Unix() }

// The embedded manifest (main.go) is only validated at sdk.Run boot;
// parse it here so a typo fails CI instead of the running sidecar.
func TestEmbeddedManifest(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "stocks" {
		t.Fatalf("name = %q, want stocks", m.Name)
	}
	if len(m.Provides.MCPTools) != 5 {
		t.Fatalf("want 5 mcp tools, got %d", len(m.Provides.MCPTools))
	}
	if m.DB == nil || m.DB.Migrations != "migrations/" {
		t.Fatalf("db block missing or wrong: %+v", m.DB)
	}
}

func TestSeedUniverseLoaded(t *testing.T) {
	st := newTestStore(t)
	rows, err := st.listUniverse("", "name", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 20 {
		t.Fatalf("expected the seed universe (~27), got %d", len(rows))
	}
}

func TestSearchUniverse(t *testing.T) {
	st := newTestStore(t)

	// Exact symbol match should rank first.
	rows, err := st.searchUniverse("AAPL", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || rows[0].Symbol != "AAPL" {
		t.Fatalf("AAPL search: want AAPL first, got %+v", rows)
	}

	// Name substring match, case-insensitive.
	rows, err = st.searchUniverse("coca", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || rows[0].Symbol != "KO" {
		t.Fatalf("coca search: want KO, got %+v", rows)
	}
}

func TestSectorFilter(t *testing.T) {
	st := newTestStore(t)
	rows, err := st.listUniverse("Energy", "name", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("expected Energy stocks (XOM, CVX)")
	}
	for _, r := range rows {
		if r.Sector != "Energy" {
			t.Fatalf("sector filter leaked %s (%s)", r.Symbol, r.Sector)
		}
	}
}

func TestSnapshotAndYieldFilter(t *testing.T) {
	st := newTestStore(t)
	y := 3.5
	if err := st.updateSnapshot("KO", 60, 1.2, &y); err != nil {
		t.Fatal(err)
	}
	// KO now has a 3.5% yield; a 3.0 floor keeps it, a 4.0 floor drops it.
	rows, err := st.listUniverse("", "yield", floatPtr(3.0), 100)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSym(rows, "KO") {
		t.Fatal("min_yield=3.0 should include KO at 3.5%")
	}
	rows, _ = st.listUniverse("", "yield", floatPtr(4.0), 100)
	if containsSym(rows, "KO") {
		t.Fatal("min_yield=4.0 should exclude KO at 3.5%")
	}
}

func TestDividendsRoundTrip(t *testing.T) {
	st := newTestStore(t)
	divs := []Dividend{
		{ExDate: daysAgo(300), Amount: 0.46},
		{ExDate: daysAgo(200), Amount: 0.46},
		{ExDate: daysAgo(100), Amount: 0.48},
		{ExDate: daysAgo(10), Amount: 0.48},
	}
	if err := st.saveDividends("KO", divs); err != nil {
		t.Fatal(err)
	}
	// Idempotent re-save must not duplicate.
	if err := st.saveDividends("KO", divs); err != nil {
		t.Fatal(err)
	}
	got, err := st.loadDividends("KO")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 dividends, got %d", len(got))
	}
	if got[0].ExDate < got[1].ExDate {
		t.Fatal("loadDividends should be newest-first")
	}
}

func TestTrailingYield(t *testing.T) {
	divs := []Dividend{
		{ExDate: daysAgo(400), Amount: 1.0}, // outside the 12mo window
		{ExDate: daysAgo(300), Amount: 0.5},
		{ExDate: daysAgo(100), Amount: 0.5},
	}
	got := trailingYield(divs, 50)
	if got == nil {
		t.Fatal("expected a yield")
	}
	// 1.0 trailing / 50 = 2.0%
	if math.Abs(*got-2.0) > 1e-9 {
		t.Fatalf("yield = %v, want 2.0", *got)
	}
	if trailingYield(divs, 0) != nil {
		t.Fatal("zero price → unknown yield")
	}
	if trailingYield(nil, 50) != nil {
		t.Fatal("no dividends → unknown yield")
	}
}

func TestDividendFrequency(t *testing.T) {
	quarterly := []Dividend{
		{ExDate: daysAgo(280), Amount: 1}, {ExDate: daysAgo(190), Amount: 1},
		{ExDate: daysAgo(100), Amount: 1}, {ExDate: daysAgo(10), Amount: 1},
	}
	if f := dividendFrequency(quarterly); f != "quarterly" {
		t.Fatalf("want quarterly, got %s", f)
	}
	if f := dividendFrequency(nil); f != "none" {
		t.Fatalf("want none, got %s", f)
	}
}

func TestNormalization(t *testing.T) {
	if normalizeRange("bogus") != "1y" {
		t.Fatal("bad range should default to 1y")
	}
	if normalizeRange("5Y") != "5y" {
		t.Fatal("range should lowercase")
	}
	if normalizeInterval("") != "1d" {
		t.Fatal("empty interval should default to 1d")
	}
}

func TestLooksLikeTicker(t *testing.T) {
	for _, ok := range []string{"AAPL", "BRK-B", "nvda", "T"} {
		if !looksLikeTicker(ok) {
			t.Fatalf("%q should look like a ticker", ok)
		}
	}
	for _, no := range []string{"", "the coca cola company", "a b c", "TOOLONGX"} {
		if looksLikeTicker(no) {
			t.Fatalf("%q should not look like a ticker", no)
		}
	}
}

func floatPtr(f float64) *float64 { return &f }

func containsSym(rows []instrumentRow, sym string) bool {
	for _, r := range rows {
		if r.Symbol == sym {
			return true
		}
	}
	return false
}
