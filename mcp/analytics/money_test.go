package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func moneyTestTime(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UnixMilli()
}

func TestSumMoneyUsesHistoricalRatesAndLeavesEventsUntouched(t *testing.T) {
	db := testDashboardDB(t)
	aug1 := moneyTestTime(t, "2026-08-01")
	aug15 := moneyTestTime(t, "2026-08-15")
	if _, err := upsertFXRate(db, "h-sites", FXRate{BaseCurrency: "USD", QuoteCurrency: "EUR", AsOf: aug1, Rate: 0.9, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertFXRate(db, "h-sites", FXRate{BaseCurrency: "USD", QuoteCurrency: "EUR", AsOf: aug15, Rate: 0.8, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		date, props string
	}{
		{"2026-08-10", `{"total_cents":10000,"currency":"USD","accounting_date":"2026-08-10"}`},
		{"2026-08-20", `{"total_cents":10000,"currency":"USD","accounting_date":"2026-08-20"}`},
		{"2026-08-20", `{"total_cents":5000,"currency":"EUR","accounting_date":"2026-08-20"}`},
	}
	for _, fixture := range fixtures {
		if _, err := insertEvent(db, EventInsert{TS: moneyTestTime(t, fixture.date), App: "billing", Topic: "invoice.paid", ProjectID: "h-sites", Source: "track", Props: fixture.props}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := insertEvent(db, EventInsert{TS: moneyTestTime(t, "2026-08-20"), App: "billing", Topic: "invoice.paid", ProjectID: "other", Source: "track", Props: `{"total_cents":999999,"currency":"EUR"}`}); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"app": "billing", "topic": "invoice.paid", "window": "all",
		"aggregation": "sum_money", "value": "props.total_cents",
		"currency_field": "props.currency", "reporting_currency": "EUR",
		"amount_unit": "minor", "rate_date_field": "props.accounting_date",
	}
	result, err := evaluateWidget(db, "h-sites", DashboardWidget{Type: "stat", Config: cfg}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := result["value"].(float64); got != 220 {
		t.Fatalf("sum_money value=%v want 220 EUR", got)
	}
	if result["currency"] != "EUR" || result["amount_unit"] != "major" || result["count"] != int64(3) {
		t.Fatalf("sum_money metadata=%#v", result)
	}
	breakdown := result["breakdown"].([]moneyBreakdown)
	if len(breakdown) != 2 || breakdown[0].Currency != "EUR" || breakdown[0].ConvertedValue != 50 || breakdown[1].Currency != "USD" || breakdown[1].ConvertedValue != 170 {
		t.Fatalf("breakdown=%#v", breakdown)
	}
	var stored string
	if err := db.QueryRow(`SELECT props FROM events WHERE project_id='h-sites' ORDER BY id LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(stored), &props); err != nil {
		t.Fatal(err)
	}
	if len(props) != 3 || props["total_cents"] != float64(10000) || props["currency"] != "USD" {
		t.Fatalf("source event changed: %s", stored)
	}
}

func TestSumMoneyTimeseriesAndPreviousPeriodComparison(t *testing.T) {
	db := testDashboardDB(t)
	now := time.Now().UTC()
	if _, err := upsertFXRate(db, "p1", FXRate{BaseCurrency: "USD", QuoteCurrency: "EUR", AsOf: now.Add(-72 * time.Hour).UnixMilli(), Rate: 0.5, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		when  time.Time
		cents int
	}{{now.Add(-36 * time.Hour), 1000}, {now.Add(-12 * time.Hour), 2000}} {
		body, _ := json.Marshal(map[string]any{"amount_cents": fixture.cents, "currency": "USD"})
		if _, err := insertEvent(db, EventInsert{TS: fixture.when.UnixMilli(), App: "billing", Topic: "paid", ProjectID: "p1", Source: "track", Props: string(body)}); err != nil {
			t.Fatal(err)
		}
	}
	base := map[string]any{
		"app": "billing", "topic": "paid", "aggregation": "sum_money",
		"value": "props.amount_cents", "currency_field": "props.currency",
		"reporting_currency": "EUR", "amount_unit": "minor",
	}
	seriesCfg := map[string]any{}
	for k, v := range base {
		seriesCfg[k] = v
	}
	seriesCfg["window"] = "7d"
	seriesCfg["interval"] = "day"
	series, err := evaluateWidget(db, "p1", DashboardWidget{Type: "timeseries", Config: seriesCfg}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := series["series"].([]map[string]any)
	observed := []map[string]any{}
	for _, row := range rows {
		if count, _ := row["count"].(int64); count > 0 {
			observed = append(observed, row)
		}
	}
	if len(observed) != 2 || observed[0]["value"] != float64(5) || observed[1]["value"] != float64(10) {
		t.Fatalf("series=%#v", rows)
	}
	statCfg := map[string]any{}
	for k, v := range base {
		statCfg[k] = v
	}
	statCfg["window"] = "24h"
	statCfg["comparison"] = "previous_period"
	stat, err := evaluateWidget(db, "p1", DashboardWidget{Type: "stat", Config: statCfg}, nil)
	if err != nil {
		t.Fatal(err)
	}
	comparison := stat["comparison"].(map[string]any)
	if stat["value"] != float64(10) || comparison["previous_value"] != float64(5) || comparison["change_percent"] != float64(100) {
		t.Fatalf("stat=%#v", stat)
	}
}

func TestSumMoneyRejectsMissingRatesAndSupportsInverse(t *testing.T) {
	db := testDashboardDB(t)
	date := moneyTestTime(t, "2026-08-20")
	if _, err := insertEvent(db, EventInsert{TS: date, App: "billing", Topic: "paid", ProjectID: "p1", Source: "track", Props: `{"amount":10,"currency":"USD"}`}); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"aggregation": "sum_money", "value": "props.amount", "currency_field": "props.currency", "reporting_currency": "EUR", "amount_unit": "major", "window": "all"}
	if _, err := evaluateWidget(db, "p1", DashboardWidget{Type: "stat", Config: cfg}, nil); err == nil || !strings.Contains(err.Error(), "missing FX rate USD/EUR") {
		t.Fatalf("missing-rate error=%v", err)
	}
	if _, err := upsertFXRate(db, "p1", FXRate{BaseCurrency: "EUR", QuoteCurrency: "USD", AsOf: moneyTestTime(t, "2026-08-01"), Rate: 2, Source: "inverse-test"}); err != nil {
		t.Fatal(err)
	}
	result, err := evaluateWidget(db, "p1", DashboardWidget{Type: "stat", Config: cfg}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result["value"].(float64)-5) > 1e-9 {
		t.Fatalf("inverse value=%v want 5", result["value"])
	}
}
