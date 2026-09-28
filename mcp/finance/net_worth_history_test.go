package main

import (
	"strings"
	"testing"
	"time"
)

func TestNetWorthHistoryReconstructsClosedHoldings(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	account := mustCreateAccount(t, app, ctx, "Broker", "brokerage", "EUR", 10000)
	_, err := ctx.AppDB().Exec(`UPDATE accounts SET opening_at=? WHERE id=?`, "2026-01-01T00:00:00Z", account.ID)
	if err != nil {
		t.Fatal(err)
	}
	instrument := mustCreateInstrument(t, app, ctx, "stock", "ACME", "Acme", "EUR")
	if _, err := app.toolTxnsBuy(ctx, map[string]any{
		"account_id": float64(account.ID), "instrument_id": float64(instrument.ID),
		"quantity": float64(1), "amount": float64(5000), "posted_at": "2026-01-10T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPricesSet(ctx, map[string]any{
		"instrument_id": float64(instrument.ID), "price": float64(6000), "as_of": "2026-02-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolTxnsSell(ctx, map[string]any{
		"account_id": float64(account.ID), "instrument_id": float64(instrument.ID),
		"quantity": float64(1), "amount": float64(7000), "posted_at": "2026-02-20T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := app.toolReportsNetWorth(ctx, map[string]any{
		"series": "daily", "from": "2026-01-09", "to": "2026-02-21",
	})
	if err != nil {
		t.Fatal(err)
	}
	points := result.(map[string]any)["points"].([]map[string]any)
	want := map[string]int64{
		"2026-01-09": 10000, // before the buy
		"2026-01-10": 10000, // trade price fills the missing price history
		"2026-02-10": 11000, // recorded price applies while held
		"2026-02-21": 12000, // closed holding no longer contributes
	}
	for _, point := range points {
		date := point["as_of"].(string)[:10]
		if expected, ok := want[date]; ok && point["total"].(int64) != expected {
			t.Errorf("%s net worth = %d, want %d", date, point["total"].(int64), expected)
		}
	}

	all, err := app.toolReportsNetWorth(ctx, map[string]any{
		"series": "monthly", "from": "all", "to": "2026-02-28",
	})
	if err != nil {
		t.Fatal(err)
	}
	series := all.(map[string]any)
	if !strings.HasPrefix(series["from"].(string), "2026-01-01") {
		t.Errorf("full history started at %s", series["from"])
	}
	monthly := series["points"].([]map[string]any)
	if len(monthly) != 2 || monthly[0]["total"].(int64) != 10000 || monthly[1]["total"].(int64) != 12000 {
		t.Errorf("monthly history = %#v", monthly)
	}
}

func TestNetWorthHistoryLimitsDailyRange(t *testing.T) {
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(2, 0, 0)
	if _, err := netWorthSampleTimes(from, to, "daily"); err == nil || !strings.Contains(err.Error(), "coarser") {
		t.Fatalf("expected a resolution hint, got %v", err)
	}
}

func TestNetWorthWeeklySamplesEndOnSundayAndIncludeRangeEnd(t *testing.T) {
	from := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC) // Monday
	to := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	points, err := netWorthSampleTimes(from, to, "weekly")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Format("2006-01-02") != "2026-10-04" || !points[1].Equal(to) {
		t.Fatalf("weekly samples = %v", points)
	}
}
