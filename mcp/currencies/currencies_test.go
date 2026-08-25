package main

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

func testCtx(t *testing.T, opts ...testkit.Option) *sdk.AppCtx {
	t.Helper()
	all := []testkit.Option{testkit.WithProjectID("project-test"), testkit.WithConfig(map[string]string{
		"provider_priority": "alpaca-market-data,saltedge,alpha-vantage,manual",
		"pivot_currencies":  "EUR,USD", "default_max_age_seconds": "259200",
	})}
	all = append(all, opts...)
	ctx := testkit.NewAppCtx(t, "apteva.yaml", all...)
	if err := seedCurrencyDefinitions(ctx.AppDB()); err != nil {
		t.Fatalf("seed definitions: %v", err)
	}
	return ctx
}

func mustObservation(t *testing.T, ctx *sdk.AppCtx, base, quote, rate, provider, effective string) RateObservation {
	t.Helper()
	at, err := parseFlexibleTime(effective)
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := insertObservation(ctx.AppDB(), "project-test", ObservationInput{
		Base: base, Quote: quote, Rate: rate, RateKind: map[bool]string{true: "manual", false: "reference"}[provider == "manual"],
		EffectiveAt: at, EffectiveDate: at.Format("2006-01-02"), Granularity: "day", ObservedAt: at,
		ProviderSlug: provider, ProviderRef: effective, OriginalBase: base, OriginalQuote: quote,
		PayloadHash: provider + effective + rate, AdapterVersion: "test-v1",
	})
	if err != nil {
		t.Fatalf("insert observation: %v", err)
	}
	return o
}

func baseRequest(base, quote, asOf string) SelectionRequest {
	t, _ := parseFlexibleTime(asOf)
	return SelectionRequest{ProjectID: "project-test", Base: base, Quote: quote, AsOf: t,
		Selection: "latest_on_or_before", MaxAge: 72 * time.Hour, AllowInverse: true,
		AllowTriangulation: true, Fetch: false}
}

func TestSeedContainsCurrentISODefinitions(t *testing.T) {
	ctx := testCtx(t)
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM currency_definitions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 178 {
		t.Fatalf("seeded definitions=%d, want 178", count)
	}
	for code, wantMinor := range map[string]*int{"EUR": ptr(2), "JPY": ptr(0), "XCG": ptr(2), "XAU": nil} {
		got, err := getCurrency(ctx.AppDB(), code)
		if err != nil {
			t.Fatalf("get %s: %v", code, err)
		}
		if got.DataVersion != "2026-01-01" {
			t.Errorf("%s seed version=%q", code, got.DataVersion)
		}
		if wantMinor == nil && got.MinorUnits != nil || wantMinor != nil && (got.MinorUnits == nil || *got.MinorUnits != *wantMinor) {
			t.Errorf("%s minor units=%v, want %v", code, got.MinorUnits, wantMinor)
		}
	}
	if _, err := getCurrency(ctx.AppDB(), "BGN"); err == nil {
		t.Fatal("BGN must be historical after Bulgaria's 2026 euro adoption")
	}
}

func TestDirectInverseAndTriangulatedRates(t *testing.T) {
	ctx := testCtx(t)
	mustObservation(t, ctx, "EUR", "USD", "1.25", "manual", "2026-01-01")
	app := &App{}

	direct, err := app.selectRate(ctx, baseRequest("EUR", "USD", "2026-01-02"))
	if err != nil || direct.Rate != "1.25" || direct.Derived {
		t.Fatalf("direct=%+v err=%v", direct, err)
	}
	inverse, err := app.selectRate(ctx, baseRequest("USD", "EUR", "2026-01-02"))
	if err != nil || inverse.Rate != "0.8" || !inverse.Derived || !inverse.Path[0].Inverted {
		t.Fatalf("inverse=%+v err=%v", inverse, err)
	}

	mustObservation(t, ctx, "GBP", "EUR", "1.2", "manual", "2026-01-01")
	mustObservation(t, ctx, "EUR", "JPY", "160", "manual", "2026-01-01")
	cross, err := app.selectRate(ctx, baseRequest("GBP", "JPY", "2026-01-02"))
	if err != nil || cross.Rate != "192" || len(cross.Path) != 2 || !cross.Derived {
		t.Fatalf("cross=%+v err=%v", cross, err)
	}
}

func TestConversionUsesMinorUnitsAndExplicitRounding(t *testing.T) {
	ctx := testCtx(t)
	mustObservation(t, ctx, "USD", "JPY", "150", "manual", "2026-01-01")
	app := &App{}
	q, err := app.selectRate(ctx, baseRequest("USD", "JPY", "2026-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := convertWithQuote(ctx.AppDB(), 1, "USD", "JPY", "half_even", q)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["converted_amount_minor"]; got != int64(2) { // USD 0.01 * 150 = JPY 1.5
		t.Fatalf("converted=%v, want 2", got)
	}
	if out["rounding_occurred"] != true {
		t.Fatal("expected rounding flag")
	}
	if got, _ := out["conversion_id"].(string); len(got) < 8 || got[:4] != "fxc_" {
		t.Fatalf("conversion_id=%q", got)
	}

	identity, err := app.selectRate(ctx, baseRequest("EUR", "EUR", "2026-01-02"))
	if err != nil || identity.Rate != "1" || !identity.Identity || len(identity.Path) != 0 {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
}

func TestProviderObservationIsIdempotentAcrossEnvelopeChanges(t *testing.T) {
	ctx := testCtx(t)
	at := mustTime(t, "2026-08-25T10:00:00Z")
	in := ObservationInput{Base: "EUR", Quote: "USD", Rate: "1.17", RateKind: "spot", EffectiveAt: at,
		EffectiveDate: "2026-08-25", Granularity: "instant", ObservedAt: at, ProviderSlug: "alpaca-market-data",
		ConnectionID: 7, ProviderRef: "EURUSD@2026-08-25T10:00:00Z", OriginalBase: "EUR", OriginalQuote: "USD", PayloadHash: "first"}
	first, created, err := insertObservation(ctx.AppDB(), "project-test", in)
	if err != nil || !created {
		t.Fatalf("first insert created=%v err=%v", created, err)
	}
	in.PayloadHash = "changed-envelope"
	second, created, err := insertObservation(ctx.AppDB(), "project-test", in)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second=%+v created=%v err=%v", second, created, err)
	}
}

func TestMissingAndStaleNeverFallBackToParity(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	_, err := app.selectRate(ctx, baseRequest("EUR", "CAD", "2026-01-02"))
	if !errors.Is(err, errRateMissing) {
		t.Fatalf("missing err=%v", err)
	}
	mustObservation(t, ctx, "EUR", "CAD", "1.5", "manual", "2025-01-01")
	req := baseRequest("EUR", "CAD", "2026-01-02")
	_, err = app.selectRate(ctx, req)
	if !errors.Is(err, errRateMissing) {
		t.Fatalf("stale err=%v", err)
	}
	req.AllowStale = true
	q, err := app.selectRate(ctx, req)
	if err != nil || !q.Stale || q.Rate != "1.5" || len(q.Warnings) == 0 {
		t.Fatalf("stale quote=%+v err=%v", q, err)
	}
}

func TestManualRateToolIsAppendOnlyAndAudited(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	out, err := app.toolRateSetManual(ctx, map[string]any{
		"base": "eur", "quote": "usd", "rate": "1.1000", "effective_at": "2026-04-01", "reason": "contract rate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["created"] != true {
		t.Fatal("first insert should be created")
	}
	var audits int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM manual_rate_audit`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audits=%d", audits)
	}
	var rate string
	_ = ctx.AppDB().QueryRow(`SELECT rate_text FROM rate_observations`).Scan(&rate)
	if rate != "1.1" {
		t.Fatalf("canonical rate=%q", rate)
	}
}

type fxPlatform struct {
	testkit.BasePlatformClient
	now time.Time
}

func (p *fxPlatform) ListConnections(filter sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	if filter.AppSlug == "alpaca-market-data" {
		return []sdk.PlatformConnection{{ID: 7, AppSlug: "alpaca-market-data", Name: "Alpaca", Status: "active", ProjectID: filter.ProjectID}}, nil
	}
	return nil, nil
}

func (p *fxPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if id != 7 || tool != "forex_latest_rates" || input["currency_pairs"] != "EURUSD" {
		return nil, errors.New("unexpected provider call")
	}
	body, _ := json.Marshal(map[string]any{"rates": map[string]any{"EURUSD": map[string]any{"rate": "1.175", "timestamp": p.now.Format(time.RFC3339)}}})
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: body}, nil
}

func TestRateLookupLazilyFetchesThroughBoundConnection(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	platform := &fxPlatform{now: now}
	ctx := testCtx(t, testkit.WithPlatform(platform))
	req := baseRequest("EUR", "USD", now.Format(time.RFC3339))
	req.Fetch = true
	q, err := (&App{}).selectRate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if q.Rate != "1.175" || len(q.Path) != 1 || q.Path[0].Provider != "alpaca-market-data" {
		t.Fatalf("quote=%+v", q)
	}
}

func TestProviderAdaptersNormalizeFixtures(t *testing.T) {
	conn := sdk.PlatformConnection{ID: 9, AppSlug: "alpha-vantage"}
	alpha := []byte(`{"Time Series FX (Daily)":{"2026-08-24":{"1. open":"1.10","2. high":"1.20","3. low":"1.05","4. close":"1.15"}}}`)
	rows, err := parseAlphaVantage(alpha, conn, "EUR", "USD", mustTime(t, "2026-08-25"), "hash")
	if err != nil || len(rows) != 4 {
		t.Fatalf("alpha rows=%+v err=%v", rows, err)
	}
	foundClose := false
	for _, row := range rows {
		foundClose = foundClose || row.RateKind == "close" && row.Rate == "1.15"
	}
	if !foundClose {
		t.Fatal("Alpha Vantage close was not normalized")
	}

	conn.AppSlug = "alpaca-market-data"
	alpaca := []byte(`{"rates":{"EURUSD":{"bp":"1.10","ap":"1.20","t":"2026-08-25T10:00:00Z"}}}`)
	rows, err = parseAlpaca(alpaca, conn, "EUR", "USD", mustTime(t, "2026-08-25T11:00:00Z"), "hash")
	if err != nil || len(rows) != 1 || rows[0].Rate != "1.15" || rows[0].RateKind != "spot" {
		t.Fatalf("alpaca rows=%+v err=%v", rows, err)
	}

	conn.AppSlug = "saltedge"
	salt := []byte(`{"data":[{"currency_code":"EUR","rate":1.17,"fail":false},{"currency_code":"JPY","rate":0.0073125,"fail":true}],"meta":{"issued_on":"2026-08-25"}}`)
	rows, err = parseSaltEdge(salt, conn, "EUR", "JPY", mustTime(t, "2026-08-25T23:00:00Z"), "hash")
	if err != nil || len(rows) != 1 || rows[0].Rate != "160" || rows[0].RateKind != "reference" {
		t.Fatalf("saltedge rows=%+v err=%v", rows, err)
	}
	if got := rows[0].QualityFlags; len(got) != 2 || got[0] != "normalized_from_usd_reference_rates" || got[1] != "provider_previous_available_date" {
		t.Fatalf("saltedge quality flags=%v", got)
	}
	if rows[0].EffectiveDate != "2026-08-25" || rows[0].AdapterVersion != "saltedge-v2" {
		t.Fatalf("saltedge provenance=%+v", rows[0])
	}
}

func TestInt64ArgRejectsInvalidFloatValues(t *testing.T) {
	for _, value := range []float64{1.5, math.NaN(), math.Inf(1), 9223372036854775808.0} {
		if _, err := int64Arg(map[string]any{"amount": value}, "amount"); err == nil {
			t.Fatalf("int64Arg accepted %v", value)
		}
	}
	got, err := int64Arg(map[string]any{"amount": -9223372036854775808.0}, "amount")
	if err != nil || got != math.MinInt64 {
		t.Fatalf("int64Arg minimum got=%d err=%v", got, err)
	}
}

func TestDateOnlyAsOfIncludesWholeEffectiveDate(t *testing.T) {
	ctx := testCtx(t)
	mustObservation(t, ctx, "EUR", "USD", "1.2", "manual", "2026-06-01T12:00:00Z")
	req, err := selectionRequest(ctx, map[string]any{"base": "EUR", "quote": "USD", "as_of": "2026-06-01", "fetch": false}, "base", "quote")
	if err != nil {
		t.Fatal(err)
	}
	q, err := (&App{}).selectRate(ctx, req)
	if err != nil || q.Rate != "1.2" {
		t.Fatalf("quote=%+v err=%v", q, err)
	}
}

func ptr(v int) *int { return &v }

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	got, err := parseFlexibleTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
