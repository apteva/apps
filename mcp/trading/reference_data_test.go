package main

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestParseAlpacaCorporateActions_NormalizesTypedArrays(t *testing.T) {
	raw := json.RawMessage(`{
		"corporate_actions":{
			"forward_splits":[{"id":"split-1","symbol":"NVDA","new_rate":"10","old_rate":"1","ex_date":"2024-06-10","process_date":"2024-06-10"}],
			"cash_dividends":[{"id":"div-1","symbol":"AAPL","rate":"0.25","currency":"USD","ex_date":"2026-08-10","payable_date":"2026-08-14"}],
			"name_changes":[{"id":"name-1","symbol":"OLD","new_symbol":"NEW","effective_date":"2026-07-01"}],
			"worthless_removals":[{"id":"remove-1","symbol":"FAIL","effective_date":"2026-06-01"}]
		},"next_page_token":"next-1"}`)
	actions, next, err := parseAlpacaCorporateActions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if next != "next-1" {
		t.Fatalf("next=%q", next)
	}
	if len(actions) != 4 {
		t.Fatalf("actions=%d", len(actions))
	}
	byID := map[string]*CorporateAction{}
	for _, action := range actions {
		byID[action.ProviderEventID] = action
	}
	if got := byID["split-1"]; got.ActionType != "forward_split" || got.RatioNumerator != 10 || got.RatioDenominator != 1 {
		t.Fatalf("split=%+v", got)
	}
	if got := byID["div-1"]; got.ActionType != "cash_dividend" || got.CashAmount != 0.25 || got.Currency != "USD" {
		t.Fatalf("dividend=%+v", got)
	}
	if got := byID["name-1"]; got.ActionType != "name_change" || got.NewSymbol != "NEW" {
		t.Fatalf("name=%+v", got)
	}
}

func TestCorporateActionLedger_IsIdempotentAndRevisioned(t *testing.T) {
	ctx := newTestCtx(t)
	action := testCorporateAction("evt-1", "forward_split", "AAPL")
	action.RatioNumerator = 2
	action.RatioDenominator = 1
	inserted, corrected, err := dbUpsertCorporateAction(ctx.AppDB(), action)
	if err != nil {
		t.Fatal(err)
	}
	if !inserted || corrected || action.Revision != 1 {
		t.Fatalf("first insert=%v corrected=%v revision=%d", inserted, corrected, action.Revision)
	}
	inserted, corrected, err = dbUpsertCorporateAction(ctx.AppDB(), action)
	if err != nil {
		t.Fatal(err)
	}
	if inserted || corrected {
		t.Fatalf("duplicate insert=%v corrected=%v", inserted, corrected)
	}
	action.RawJSON = `{"corrected":true}`
	action.PayloadSHA256 = "changed"
	inserted, corrected, err = dbUpsertCorporateAction(ctx.AppDB(), action)
	if err != nil {
		t.Fatal(err)
	}
	if !inserted || !corrected || action.Revision != 2 {
		t.Fatalf("revision insert=%v corrected=%v revision=%d", inserted, corrected, action.Revision)
	}
}

func TestAlpacaCorporateActionSSE_NormalizesAndPersistsMutation(t *testing.T) {
	ctx := newTestCtx(t)
	payload := []byte(`{"event_type":"forward_split_corporateaction_event","action":"insert","ca":{"id":"sse-split-1","symbol":"AAPL","new_rate":"2","old_rate":"1","effective_date":"2099-01-01"}}`)
	if err := applyAlpacaCorporateActionEvent(ctx, "01TESTEVENT", payload); err != nil {
		t.Fatal(err)
	}
	actions, err := dbListCorporateActions(ctx.AppDB(), "AAPL", "forward_split", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].ProviderEventID != "sse-split-1" || actions[0].RatioNumerator != 2 {
		t.Fatalf("actions=%+v", actions)
	}
}

func TestCorporateActionProjector_AppliesSplitExactlyOnce(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Split book", []string{"equity"})
	insertPosition(t, ctx, portfolioID, "AAPL", 10, 200)
	_, _ = ctx.AppDB().Exec(`UPDATE position_history SET observed_at='2019-12-31T00:00:00Z' WHERE portfolio_id=?`, portfolioID)
	action := testCorporateAction("split-1", "forward_split", "AAPL")
	action.RatioNumerator = 4
	action.RatioDenominator = 1
	action.EffectiveDate = "2020-01-01"
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), mustPortfolio(t, ctx, portfolioID), action, "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), mustPortfolio(t, ctx, portfolioID), action, "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	positions, _ := dbListPositions(ctx.AppDB(), portfolioID)
	if len(positions) != 1 || positions[0].Qty != 40 || positions[0].AvgCost != 50 {
		t.Fatalf("positions=%+v", positions)
	}
	postings, _ := dbListCorporateActionPostings(ctx.AppDB(), portfolioID, 10)
	if len(postings) != 1 {
		t.Fatalf("postings=%d", len(postings))
	}
}

func TestCorporateActionProjector_CreditsDividendEntitlementOnce(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Dividend book", []string{"equity"})
	insertPosition(t, ctx, portfolioID, "AAPL", 8, 100)
	_, _ = ctx.AppDB().Exec(`UPDATE position_history SET observed_at='2026-01-01T00:00:00Z' WHERE portfolio_id=?`, portfolioID)
	portfolio := mustPortfolio(t, ctx, portfolioID)
	action := testCorporateAction("div-1", "cash_dividend", "AAPL")
	action.CashAmount = 0.25
	action.Currency = "USD"
	action.ExDate = "2026-01-02"
	action.PayableDate = "2026-01-05"
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), portfolio, action, "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), portfolio, action, "2026-01-05"); err != nil {
		t.Fatal(err)
	}
	after := mustPortfolio(t, ctx, portfolioID)
	if math.Abs(after.Cash-100002) > 1e-9 {
		t.Fatalf("cash=%v", after.Cash)
	}
}

func TestCorporateActionProjector_MigratesSymbolAndWatchlist(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Rename book", []string{"equity"})
	insertPosition(t, ctx, portfolioID, "OLD", 3, 12)
	_, _ = ctx.AppDB().Exec(`INSERT INTO watchlist(project_id,portfolio_id,symbol) VALUES('test-proj',?,'OLD')`, portfolioID)
	action := testCorporateAction("rename-1", "name_change", "OLD")
	action.NewSymbol = "NEW"
	action.EffectiveDate = "2026-01-01"
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), mustPortfolio(t, ctx, portfolioID), action, "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	positions, _ := dbListPositions(ctx.AppDB(), portfolioID)
	if len(positions) != 1 || positions[0].Symbol != "NEW" {
		t.Fatalf("positions=%+v", positions)
	}
	watchlist, _ := dbWatchlist(ctx.AppDB(), portfolioID)
	if len(watchlist) != 1 || watchlist[0] != "NEW" {
		t.Fatalf("watchlist=%v", watchlist)
	}
}

func TestMarketCalendar_PrefersStoredAuthoritativeSession(t *testing.T) {
	ctx := newTestCtx(t)
	_, err := ctx.AppDB().Exec(`INSERT INTO exchange_sessions(venue,session_date,session_type,open_at,close_at,status,source) VALUES('US_EQUITY','2026-07-03','regular','2026-07-03T14:00:00Z','2026-07-03T16:00:00Z','open','alpaca')`)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 3, 15, 0, 0, 0, time.UTC)
	session, err := marketSessionAt(calendarUSEquity, at)
	if err != nil {
		t.Fatal(err)
	}
	if !session.IsOpen || session.OpenAt != "2026-07-03T14:00:00Z" || session.Reason != "authoritative_session" {
		t.Fatalf("session=%+v", session)
	}
}

func TestReferenceManifest_ReportsForwardOnlyUniverseCoverage(t *testing.T) {
	ctx := newTestCtx(t)
	security := &Security{ID: "sec-test", AssetClass: "equity", Name: "Test", Status: "active", Source: "fixture"}
	if err := dbUpsertSecurity(ctx.AppDB(), security, &SecurityListing{Venue: "NYSE", Symbol: "TEST", Active: true, ValidFrom: "2026-01-01", Source: "fixture"}, nil); err != nil {
		t.Fatal(err)
	}
	_, _ = ctx.AppDB().Exec(`INSERT INTO universe_memberships(universe_id,security_id,valid_from,source) VALUES(?,?,?,?)`, referenceUniverseAlpaca, security.ID, "2026-01-01", "fixture")
	before := referenceManifest(ctx.AppDB(), []string{"TEST"}, "2025-01-01", "2025-12-31", "total_return")
	if before["survivorship_safe"] != false {
		t.Fatalf("before=%v", before)
	}
	after := referenceManifest(ctx.AppDB(), []string{"TEST"}, "2026-02-01", "2026-03-01", "total_return")
	if after["survivorship_safe"] != false {
		t.Fatalf("after=%v", after)
	}
}

func TestSecurityMaster_PreservesIdentityAcrossSymbolListings(t *testing.T) {
	ctx := newTestCtx(t)
	securityID, err := resolveSecurityID(ctx.AppDB(), "fixture", "asset-1", "", "037833100", "NASDAQ", "OLD")
	if err != nil {
		t.Fatal(err)
	}
	security := &Security{ID: securityID, AssetClass: "equity", Name: "Example", Status: "active", Source: "fixture"}
	if err = dbUpsertSecurity(ctx.AppDB(), security, &SecurityListing{ProviderAssetID: "asset-1", Venue: "NASDAQ", Symbol: "OLD", ValidFrom: "2020-01-01", ValidTo: "2025-12-31", Source: "fixture"}, map[string]string{"cusip": "037833100"}); err != nil {
		t.Fatal(err)
	}
	if err = dbUpsertSecurity(ctx.AppDB(), security, &SecurityListing{ProviderAssetID: "asset-1", Venue: "NASDAQ", Symbol: "NEW", ValidFrom: "2026-01-01", Active: true, Source: "fixture"}, map[string]string{"cusip": "037833100"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveSecurityID(ctx.AppDB(), "fixture", "", "", "037833100", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != securityID {
		t.Fatalf("identity changed: got %s want %s", resolved, securityID)
	}
	rows, err := dbListSecurities(ctx.AppDB(), "", "2026-02-01", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("listings=%v err=%v", rows, err)
	}
	gotSecurity, securityOK := rows[0]["security"].(Security)
	gotListing, listingOK := rows[0]["listing"].(SecurityListing)
	if !securityOK || !listingOK || gotSecurity.ID != securityID || gotListing.Symbol != "NEW" || gotListing.SecurityID != securityID {
		t.Fatalf("identity=%+v listing=%+v", gotSecurity, gotListing)
	}
}

func TestCorporateActionProjector_RemovesDelistedPositionExactlyOnce(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Delisting book", []string{"equity"})
	insertPosition(t, ctx, portfolioID, "FAIL", 7, 11)
	action := testCorporateAction("remove-1", "worthless_removal", "FAIL")
	action.EffectiveDate = "2026-01-01"
	portfolio := mustPortfolio(t, ctx, portfolioID)
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), portfolio, action, "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), portfolio, action, "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	positions, _ := dbListPositions(ctx.AppDB(), portfolioID)
	postings, _ := dbListCorporateActionPostings(ctx.AppDB(), portfolioID, 10)
	if len(positions) != 0 || len(postings) != 1 || postings[0].CostBasisDelta != -77 {
		t.Fatalf("positions=%+v postings=%+v", positions, postings)
	}
}

func TestCorporateActionProjector_RefusesCorrectedAppliedRevision(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Correction book", []string{"equity"})
	insertPosition(t, ctx, portfolioID, "AAPL", 10, 20)
	_, _ = ctx.AppDB().Exec(`UPDATE position_history SET observed_at='2025-12-31T00:00:00Z' WHERE portfolio_id=?`, portfolioID)
	first := testCorporateAction("split-corrected", "forward_split", "AAPL")
	first.RatioNumerator, first.RatioDenominator = 2, 1
	first.EffectiveDate = "2026-01-01"
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), mustPortfolio(t, ctx, portfolioID), first, "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	corrected := *first
	corrected.Revision = 2
	corrected.RatioNumerator = 3
	if err := applyCorporateActionToPortfolio(ctx.AppDB(), mustPortfolio(t, ctx, portfolioID), &corrected, "2026-01-01"); err == nil {
		t.Fatal("expected manual-reconciliation error for a corrected applied action")
	}
	positions, _ := dbListPositions(ctx.AppDB(), portfolioID)
	if len(positions) != 1 || positions[0].Qty != 20 {
		t.Fatalf("corrected revision was applied: %+v", positions)
	}
}

func TestReferenceHTTP_ExposesNormalizedLatestRevision(t *testing.T) {
	ctx := newTestCtx(t)
	action := testCorporateAction("http-action", "cash_dividend", "AAPL")
	action.CashAmount, action.ExDate = 0.25, "2026-08-10"
	if _, _, err := dbUpsertCorporateAction(ctx.AppDB(), action); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/reference/corporate-actions?symbol=AAPL", nil)
	recorder := httptest.NewRecorder()
	(&App{}).handleHTTPReferenceData(recorder, req)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		CorporateActions []*CorporateAction `json:"corporate_actions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.CorporateActions) != 1 || response.CorporateActions[0].ProviderEventID != "http-action" {
		t.Fatalf("response=%+v", response.CorporateActions)
	}
}

func TestReferenceManifest_FlagsRawPriceDiscontinuity(t *testing.T) {
	ctx := newTestCtx(t)
	manifest := referenceManifest(ctx.AppDB(), []string{"AAPL"}, "2026-01-01", "2026-12-31", "raw")
	if manifest["economic_continuity"] != false || manifest["warning"] == "" {
		t.Fatalf("manifest=%v", manifest)
	}
}

func testCorporateAction(id, typ, symbol string) *CorporateAction {
	return &CorporateAction{Provider: "fixture", ProviderEventID: id, Revision: 1, ActionType: typ, Status: "confirmed", Symbol: symbol, DataQuality: "complete", RawJSON: `{}`, PayloadSHA256: id}
}
func insertPosition(t *testing.T, ctx *sdk.AppCtx, portfolioID int64, symbol string, qty, avg float64) {
	t.Helper()
	_, err := ctx.AppDB().Exec(`INSERT INTO positions(project_id,portfolio_id,symbol,asset_class,qty,avg_cost) VALUES('test-proj',?,?, 'equity',?,?)`, portfolioID, symbol, qty, avg)
	if err != nil {
		t.Fatal(err)
	}
}
func mustPortfolio(t *testing.T, ctx *sdk.AppCtx, id int64) *Portfolio {
	t.Helper()
	portfolio, err := dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	return portfolio
}
