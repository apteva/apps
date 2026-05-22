package main

// Unit tests for the pure computation + the store. These never touch the
// network — the Yahoo client is exercised in integration runs, not here.

import (
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(filepath.Join("migrations", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
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
	if len(m.Provides.MCPTools) != 11 {
		t.Fatalf("want 11 mcp tools, got %d", len(m.Provides.MCPTools))
	}
	if m.DB == nil || m.DB.Migrations != "migrations/" {
		t.Fatalf("db block missing or wrong: %+v", m.DB)
	}
}

func TestSeedUniverseLoaded(t *testing.T) {
	st := newTestStore(t)
	rows, err := st.listUniverse(listFilter{Sort: "name", Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 1000 {
		t.Fatalf("expected the S&P 1500 seed (~1500), got %d", len(rows))
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

	// Name substring match, case-insensitive (the universe has several
	// Coca-Cola entities now, so just assert KO is among the hits).
	rows, err = st.searchUniverse("coca", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSym(rows, "KO") {
		t.Fatalf("coca search: want KO among results, got %+v", rows)
	}
}

func TestSectorFilter(t *testing.T) {
	st := newTestStore(t)
	rows, err := st.listUniverse(listFilter{Sector: "Energy", Sort: "name", Limit: 0})
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
	if err := st.updateSnapshot("KO", 60, 1.2, &y, nil); err != nil {
		t.Fatal(err)
	}
	// KO now has a 3.5% yield; a 3.0 floor keeps it, a 4.0 floor drops it.
	rows, err := st.listUniverse(listFilter{Sort: "yield", MinYield: floatPtr(3.0)})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSym(rows, "KO") {
		t.Fatal("min_yield=3.0 should include KO at 3.5%")
	}
	rows, _ = st.listUniverse(listFilter{Sort: "yield", MinYield: floatPtr(4.0)})
	if containsSym(rows, "KO") {
		t.Fatal("min_yield=4.0 should exclude KO at 3.5%")
	}
}

func TestStats(t *testing.T) {
	st := newTestStore(t)
	s0, err := st.stats(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if s0.Universe < 1000 || s0.Warmed != 0 || s0.WithPrice != 0 {
		t.Fatalf("fresh seed: want big universe, 0 warmed; got %+v", s0)
	}
	y := 3.0
	if err := st.updateSnapshot("KO", 60, 1.0, &y, nil); err != nil {
		t.Fatal(err)
	}
	pe := 12.0
	_ = st.updateFundamentals("KO", &pe, nil)
	s1, _ := st.stats(time.Hour)
	if s1.Warmed != 1 || s1.WithPrice != 1 || s1.WithYield != 1 || s1.WithFundamentals != 1 || s1.Fresh != 1 {
		t.Fatalf("after warming KO: %+v", s1)
	}
	if s1.NewestRefresh == nil {
		t.Fatal("expected a newest_refresh timestamp")
	}
}

func TestPayoutAndPEFilters(t *testing.T) {
	st := newTestStore(t)
	pe, payout := 12.0, 40.0
	if err := st.updateFundamentals("KO", &pe, &payout); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.listUniverse(listFilter{MaxPayout: floatPtr(50)}); !containsSym(rows, "KO") {
		t.Fatal("max_payout=50 should include KO at 40%")
	}
	if rows, _ := st.listUniverse(listFilter{MaxPayout: floatPtr(30)}); containsSym(rows, "KO") {
		t.Fatal("max_payout=30 should exclude KO at 40%")
	}
	if rows, _ := st.listUniverse(listFilter{MaxPE: floatPtr(15)}); !containsSym(rows, "KO") {
		t.Fatal("max_pe=15 should include KO at 12")
	}
	if rows, _ := st.listUniverse(listFilter{MaxPE: floatPtr(10)}); containsSym(rows, "KO") {
		t.Fatal("max_pe=10 should exclude KO at 12")
	}
}

func TestDividendCAGR5(t *testing.T) {
	// base window (~5y ago) totals 1.0; trailing 12mo totals 2.0
	// → CAGR = 2^(1/5) - 1 ≈ 14.87%.
	divs := []Dividend{
		{ExDate: daysAgo(30), Amount: 1.0}, {ExDate: daysAgo(120), Amount: 1.0},
		{ExDate: daysAgo(5*365 + 30), Amount: 0.5}, {ExDate: daysAgo(5*365 + 120), Amount: 0.5},
	}
	g := dividendCAGR5(divs)
	if g == nil {
		t.Fatal("expected a CAGR")
	}
	if math.Abs(*g-14.87) > 0.5 {
		t.Fatalf("cagr = %v, want ~14.87", *g)
	}
	if dividendCAGR5(nil) != nil {
		t.Fatal("no history → nil")
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

func TestDayChangeFallsBackToBars(t *testing.T) {
	// previousClose=0 (Yahoo's daily-range behavior) → use the prior bar,
	// NOT a 1-year-ago price. Bars: prior close 100, latest 110; price 110.
	res := &chartResult{
		Meta: quoteMeta{Price: 110},
		Bars: []Bar{{C: 90}, {C: 100}, {C: 110}},
	}
	prev, pct := dayChange(res)
	if prev != 100 {
		t.Fatalf("prevClose = %v, want 100 (second-to-last bar)", prev)
	}
	if math.Abs(pct-10) > 1e-9 {
		t.Fatalf("change = %v%%, want 10%% — not a multi-month return", pct)
	}
	// When Yahoo does give an intraday previousClose, prefer it.
	res.Meta.PreviousClose = 108
	if prev, _ := dayChange(res); prev != 108 {
		t.Fatalf("prevClose = %v, want 108 (meta wins)", prev)
	}
}

func TestPlausiblePriceRejectsBadReads(t *testing.T) {
	bars := []Bar{{C: 230}, {C: 231}}
	if !plausiblePrice(&chartResult{Meta: quoteMeta{Price: 231.73}, Bars: bars}) {
		t.Fatal("231.73 vs last 231 should be plausible")
	}
	if plausiblePrice(&chartResult{Meta: quoteMeta{Price: 0.005}, Bars: bars}) {
		t.Fatal("0.005 vs last 231 should be rejected (the JNJ-outlier class)")
	}
	if plausiblePrice(&chartResult{Meta: quoteMeta{Price: 0}, Bars: bars}) {
		t.Fatal("zero price should be rejected")
	}
}

func TestTrailingYieldGuardsAgainstBadPrice(t *testing.T) {
	divs := []Dividend{{ExDate: daysAgo(30), Amount: 1.3}}
	// A transient near-zero price would make 1.3/0.005 ≈ 26000% — must be
	// reported as unknown, not stored.
	if y := trailingYield(divs, 0.005); y != nil {
		t.Fatalf("implausible yield should be nil, got %v", *y)
	}
	if y := trailingYield(divs, 50); y == nil {
		t.Fatal("a sane 2.6%% yield should be returned")
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

// TestLiveFundamentals exercises the real cookie+consent+crumb handshake
// against Yahoo. Skipped by default (network); run with STOCKS_LIVE=1.
func TestLiveFundamentals(t *testing.T) {
	if os.Getenv("STOCKS_LIVE") != "1" {
		t.Skip("set STOCKS_LIVE=1 to run the live Yahoo handshake")
	}
	deref := func(p *float64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	y := newYahoo()
	pe, payout, err := y.fundamentals("JNJ", false)
	t.Logf("JNJ pe=%v payout=%v err=%v", deref(pe), deref(payout), err)
	if err != nil {
		t.Fatalf("fundamentals handshake failed: %v", err)
	}
	if pe == nil && payout == nil {
		t.Fatal("handshake ok but no P/E or payout parsed")
	}
}

func TestWatchlistMembership(t *testing.T) {
	st := newTestStore(t)
	a := &App{st: st}
	// Warm KO (3.5% yield) and PEP (2.0%) so rules can evaluate.
	yKO := 3.5
	if err := st.updateSnapshot("KO", 60, 0, &yKO, nil); err != nil {
		t.Fatal(err)
	}
	yPEP := 2.0
	_ = st.updateSnapshot("PEP", 150, 0, &yPEP, nil)

	// Dynamic list: yield ≥ 3 → KO matches, PEP doesn't.
	id, err := st.watchlistCreate("proj1", "High yield", `{"min_yield":3}`)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := st.watchlistByID("proj1", id)
	rows, err := a.resolveWatchlist(w, "")
	if err != nil {
		t.Fatal(err)
	}
	if !containsSym(rows, "KO") || containsSym(rows, "PEP") {
		t.Fatal("rule yield≥3 should include KO and exclude PEP")
	}

	// Include-pin PEP → present and flagged Pinned.
	if err := st.setPin(id, "PEP", "include"); err != nil {
		t.Fatal(err)
	}
	rows, _ = a.resolveWatchlist(w, "")
	if !containsSym(rows, "PEP") {
		t.Fatal("include pin should add PEP")
	}
	for _, r := range rows {
		if r.Symbol == "PEP" && !r.Pinned {
			t.Fatal("PEP should be flagged Pinned")
		}
	}

	// Exclude-pin KO → dropped despite matching the rule.
	_ = st.setPin(id, "KO", "exclude")
	rows, _ = a.resolveWatchlist(w, "")
	if containsSym(rows, "KO") {
		t.Fatal("exclude pin should drop KO")
	}

	// Project isolation: another project can't load it.
	if other, _ := st.watchlistByID("proj2", id); other != nil {
		t.Fatal("watchlist must be project-scoped")
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
