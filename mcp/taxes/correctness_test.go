package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type appCall struct {
	app   string
	tool  string
	input map[string]any
}

type taxesPlatformStub struct {
	tk.BasePlatformClient
	calls     []appCall
	responder func(app, tool string, input map[string]any) (map[string]any, error)
}

func (s *taxesPlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.calls = append(s.calls, appCall{app: app, tool: tool, input: cloneMap(input)})
	if s.responder == nil {
		return errors.New("unexpected app call")
	}
	response, err := s.responder(app, tool, input)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func testCtx(t *testing.T, dbProject string, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	db := openTestDB(t)
	manifest := (&App{}).Manifest()
	return sdk.NewAppCtxForTest(&manifest, db, nil, platform, nil).WithProject(dbProject)
}

func insertTestProfile(t *testing.T, ctx *sdk.AppCtx, profile Profile) Profile {
	t.Helper()
	if profile.ProjectID == "" {
		profile.ProjectID = ctx.CurrentProject()
	}
	if profile.Name == "" {
		profile.Name = "Test business"
	}
	if profile.Country == "" {
		profile.Country = "ES"
	}
	if profile.Structure == "" {
		profile.Structure = "ES_SL"
	}
	if profile.FiscalYearStart == "" {
		profile.FiscalYearStart = "01-01"
	}
	if profile.FiscalYearEnd == "" {
		profile.FiscalYearEnd = "12-31"
	}
	if profile.FilingCadence == "" {
		profile.FilingCadence = "quarterly"
	}
	if profile.AccountingBasis == "" {
		profile.AccountingBasis = "accrual"
	}
	if profile.Currency == "" {
		profile.Currency = "EUR"
	}
	result, err := (&App{}).toolProfilesCreate(ctx, map[string]any{
		"name":              profile.Name,
		"country":           profile.Country,
		"structure":         profile.Structure,
		"fiscal_year_start": profile.FiscalYearStart,
		"fiscal_year_end":   profile.FiscalYearEnd,
		"vat_registered":    profile.VATRegistered,
		"filing_cadence":    profile.FilingCadence,
		"accounting_basis":  profile.AccountingBasis,
		"currency":          profile.Currency,
		"config":            profile.Config,
		"auto_open_periods": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.(map[string]any)["profile"].(Profile)
}

func TestProjectIDUsesDispatchContext(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := testCtx(t, "project-from-dispatch", nil)
	if got := projectID(ctx, map[string]any{"_project_id": "wrong-project"}); got != "project-from-dispatch" {
		t.Fatalf("projectID=%q, want dispatch project", got)
	}
}

func TestSyncBillingUsesContractAndDoesNotDoubleCount(t *testing.T) {
	stub := &taxesPlatformStub{}
	stub.responder = func(app, tool string, input map[string]any) (map[string]any, error) {
		if app != "billing" || tool != "invoices_search" {
			return nil, fmt.Errorf("unexpected %s:%s", app, tool)
		}
		status := stringFromAny(input["status"])
		amount := int64(10000)
		if status == "paid" {
			amount = 20000
		}
		return map[string]any{"invoices": []any{map[string]any{
			"subtotal_cents": amount,
			"tax_cents":      amount * 21 / 100,
			"total_cents":    amount * 121 / 100,
			"status":         status,
		}}}, nil
	}
	ctx := testCtx(t, "p-sync", stub)
	summary := syncBilling(ctx, Profile{
		ProjectID: "p-sync", Currency: "EUR", AccountingBasis: "accrual", VATRegistered: true,
	}, "2026-01-01", "2026-03-31")
	if summary.RevenueCents != 30000 || summary.OutputTaxCents != 6300 {
		t.Fatalf("summary=%+v", summary)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("calls=%d, want 2", len(stub.calls))
	}
	for _, call := range stub.calls {
		if call.input["since"] != "2026-01-01" || call.input["until"] != "2026-04-01" {
			t.Fatalf("wrong date filters: %#v", call.input)
		}
		if call.input["_project_id"] != "p-sync" || call.input["currency"] != "EUR" || call.input["limit"] != 200 {
			t.Fatalf("missing scoped contract args: %#v", call.input)
		}
		if _, exists := call.input["date_from"]; exists {
			t.Fatalf("legacy date_from leaked into call: %#v", call.input)
		}
	}
}

func TestSyncBillsPaginatesAndUsesSubtotalOnly(t *testing.T) {
	stub := &taxesPlatformStub{}
	stub.responder = func(app, tool string, input map[string]any) (map[string]any, error) {
		if app != "bills" || tool != "bills_search" {
			return nil, fmt.Errorf("unexpected %s:%s", app, tool)
		}
		if input["status"] != "received" {
			return map[string]any{"bills": []any{}, "has_more": false}, nil
		}
		offset := intFromAny(input["offset"])
		if offset == 0 {
			return map[string]any{
				"bills":    []any{map[string]any{"subtotal_cents": 10000, "tax_cents": 2100, "total_cents": 12100}},
				"has_more": true, "next_offset": 1,
			}, nil
		}
		return map[string]any{
			"bills":    []any{map[string]any{"subtotal_cents": 5000, "tax_cents": 1050, "total_cents": 6050}},
			"has_more": false,
		}, nil
	}
	ctx := testCtx(t, "p-bills", stub)
	summary := syncBills(ctx, Profile{
		ProjectID: "p-bills", Currency: "EUR", AccountingBasis: "accrual", VATRegistered: true,
	}, "2026-01-01", "2026-03-31")
	if summary.ExpensesCents != 15000 || summary.InputTaxCents != 3150 || summary.Items != 2 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestExplicitZeroVATIsPreserved(t *testing.T) {
	db := openTestDB(t)
	if err := seedTaxRules(db); err != nil {
		t.Fatal(err)
	}
	rule, err := findRule(db, "ES", "ES_SL", "vat", 2026)
	if err != nil {
		t.Fatal(err)
	}
	out := calculateOutputs("vat", rule, map[string]any{
		"revenue_cents":    int64(100000),
		"expenses_cents":   int64(20000),
		"output_tax_cents": int64(0),
		"input_tax_cents":  int64(0),
	}, "2026-01-01", "2026-03-31")
	if payable := int64FromAny(out["estimated_payable_cents"]); payable != 0 {
		t.Fatalf("payable=%d, want explicit zero", payable)
	}
}

func TestGeneratedPeriodsRespectVATAndDeadlines(t *testing.T) {
	noVAT := Profile{
		Country: "ES", Structure: "ES_AUTONOMO", FilingCadence: "quarterly",
		FiscalYearStart: "01-01", FiscalYearEnd: "12-31",
	}
	periods := inferPeriods(noVAT, 2026)
	for _, period := range periods {
		if period.TaxType == "vat" {
			t.Fatal("VAT period generated for non-VAT profile")
		}
		if period.TaxType == "income_tax" && period.PeriodEnd == "2026-12-31" && period.DueDate != "2027-02-01" {
			t.Fatalf("Q4 income deadline=%q", period.DueDate)
		}
		if period.TaxType == "social_contributions" && period.PeriodEnd == "2026-02-28" && period.DueDate != "2026-02-28" {
			t.Fatalf("February social deadline=%q", period.DueDate)
		}
	}
	fr := Profile{
		Country: "FR", Structure: "FR_SAS", VATRegistered: true, FilingCadence: "monthly",
		FiscalYearStart: "01-01", FiscalYearEnd: "12-31",
	}
	for _, period := range inferPeriods(fr, 2026) {
		if period.DueDate != "" || period.DeadlineState != "requires_confirmation" {
			t.Fatalf("French deadline should require confirmation: %+v", period)
		}
	}
}

func TestSpanishCorporateDeadlineUsesClampedCalendarMonths(t *testing.T) {
	profile := Profile{
		Country: "ES", Structure: "ES_SL",
		FiscalYearStart: "07-01", FiscalYearEnd: "06-30",
	}
	period := annualPeriodForProfile(profile, 2026, "corporate_tax")
	if period.PeriodEnd != "2027-06-30" || period.DueDate != "2028-01-24" {
		t.Fatalf("period=%+v", period)
	}

	calendarYear := annualPeriodForProfile(Profile{
		Country: "ES", Structure: "ES_SL",
		FiscalYearStart: "01-01", FiscalYearEnd: "12-31",
	}, 2025, "corporate_tax")
	if calendarYear.DueDate != "2026-07-27" {
		t.Fatalf("calendar-year due date=%q", calendarYear.DueDate)
	}
}

func TestEURLApplicableTaxesFollowConfiguredRegime(t *testing.T) {
	income := inferredTaxTypes(Profile{Country: "FR", Structure: "FR_EURL", VATRegistered: true})
	if !containsString(income, "income_tax") || containsString(income, "corporate_tax") {
		t.Fatalf("income regime types=%v", income)
	}
	corporate := inferredTaxTypes(Profile{
		Country: "FR", Structure: "FR_EURL", VATRegistered: true,
		Config: map[string]any{"tax_regime": "corporate_tax"},
	})
	if !containsString(corporate, "corporate_tax") || containsString(corporate, "income_tax") {
		t.Fatalf("corporate regime types=%v", corporate)
	}
}

func TestLinkBillDoesNotRecordUnverifiedPayment(t *testing.T) {
	stub := &taxesPlatformStub{}
	stub.responder = func(app, tool string, input map[string]any) (map[string]any, error) {
		return map[string]any{
			"found": true,
			"bill": map[string]any{
				"id": 77, "currency": "EUR", "status": "received", "payments": []any{},
			},
		}, nil
	}
	ctx := testCtx(t, "p-pay", stub)
	profile := insertTestProfile(t, ctx, Profile{VATRegistered: true})
	obligation, err := insertObligation(ctx.AppDB(), profile, 0, 0, "vat", "VAT", 10000, "EUR", "2026-04-20", "AEAT", "payable", "filed", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolPaymentsLinkBill(ctx, map[string]any{"obligation_id": obligation.ID, "bills_bill_id": 77})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["payment_recorded"] != false {
		t.Fatalf("unexpected payment result: %#v", out)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM tax_payments WHERE obligation_id=?`, obligation.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("payments=%d, want 0", count)
	}
	updated, err := getObligation(ctx.AppDB(), profile.ProjectID, obligation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "filed" || int64FromAny(updated.Metadata["bills_bill_id"]) != 77 {
		t.Fatalf("updated obligation=%+v", updated)
	}
}

func TestVerifiedBillsPaymentIsIdempotentAndMarksPaid(t *testing.T) {
	stub := &taxesPlatformStub{}
	stub.responder = func(app, tool string, input map[string]any) (map[string]any, error) {
		return map[string]any{
			"found": true,
			"bill": map[string]any{
				"id": 77, "currency": "EUR", "status": "paid",
				"payments": []any{map[string]any{
					"id": 88, "amount_cents": 10000, "currency": "EUR",
					"sent_at": "2026-04-18", "method": "wire", "external_id": "bank-1",
				}},
			},
		}, nil
	}
	ctx := testCtx(t, "p-verified", stub)
	profile := insertTestProfile(t, ctx, Profile{VATRegistered: true})
	obligation, err := insertObligation(ctx.AppDB(), profile, 0, 0, "vat", "VAT", 10000, "EUR", "2026-04-20", "AEAT", "payable", "filed", nil)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"obligation_id": obligation.ID, "bills_bill_id": 77, "bills_payment_id": 88}
	if _, err := (&App{}).toolPaymentsLinkBill(ctx, args); err != nil {
		t.Fatal(err)
	}
	updated, err := getObligation(ctx.AppDB(), profile.ProjectID, obligation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "paid" {
		t.Fatalf("status=%q, want paid", updated.Status)
	}
	if _, err := (&App{}).toolPaymentsLinkBill(ctx, args); err == nil {
		t.Fatal("duplicate Bills payment should fail")
	}
}

func TestRefundEstimateCreatesReceivable(t *testing.T) {
	ctx := testCtx(t, "p-refund", nil)
	profile := insertTestProfile(t, ctx, Profile{VATRegistered: true})
	obligation, err := upsertEstimatedObligation(ctx.AppDB(), profile, 0, 0, "vat", map[string]any{
		"estimated_payable_cents": int64(-4500),
	}, "2026-04-20")
	if err != nil {
		t.Fatal(err)
	}
	if obligation.Direction != "receivable" || obligation.AmountCents != 4500 {
		t.Fatalf("obligation=%+v", obligation)
	}
}

func TestReportIsScopedToSelectedPeriod(t *testing.T) {
	ctx := testCtx(t, "p-report", nil)
	profile := insertTestProfile(t, ctx, Profile{VATRegistered: true})
	first, err := (&App{}).toolPeriodsOpen(ctx, map[string]any{
		"profile_id": profile.ID, "tax_type": "vat",
		"period_start": "2026-01-01", "period_end": "2026-03-31", "due_date": "2026-04-20",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := (&App{}).toolPeriodsOpen(ctx, map[string]any{
		"profile_id": profile.ID, "tax_type": "vat",
		"period_start": "2026-04-01", "period_end": "2026-06-30", "due_date": "2026-07-20",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstID := int64FromAny(first.(map[string]any)["period"].(map[string]any)["id"])
	secondID := int64FromAny(second.(map[string]any)["period"].(map[string]any)["id"])
	if _, err := insertObligation(ctx.AppDB(), profile, firstID, 0, "vat", "Q1", 1000, "EUR", "2026-04-20", "AEAT", "payable", "filed", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := insertObligation(ctx.AppDB(), profile, secondID, 0, "vat", "Q2", 2000, "EUR", "2026-07-20", "AEAT", "payable", "filed", nil); err != nil {
		t.Fatal(err)
	}
	result, err := (&App{}).toolReportGenerate(ctx, map[string]any{"profile_id": profile.ID, "period_id": firstID})
	if err != nil {
		t.Fatal(err)
	}
	report := result.(map[string]any)["report"].(map[string]any)
	obligations := report["obligations"].([]Obligation)
	if len(obligations) != 1 || obligations[0].Title != "Q1" {
		t.Fatalf("report obligations=%+v", obligations)
	}
}

func TestProfileRejectsCountryStructureMismatch(t *testing.T) {
	ctx := testCtx(t, "p-profile", nil)
	_, err := (&App{}).toolProfilesCreate(ctx, map[string]any{
		"name": "Wrong", "country": "ES", "structure": "FR_SAS",
	})
	if err == nil {
		t.Fatal("country/structure mismatch should fail")
	}
}

func TestEstimateAllClosesPeriodRowsBeforeWriting(t *testing.T) {
	ctx := testCtx(t, "p-all", nil)
	if err := seedTaxRules(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	profile := insertTestProfile(t, ctx, Profile{VATRegistered: true})
	if _, err := (&App{}).toolPeriodsOpen(ctx, map[string]any{
		"profile_id": profile.ID, "tax_type": "vat",
		"period_start": "2026-01-01", "period_end": "2026-03-31", "due_date": "2026-04-20",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := (&App{}).toolEstimateAll(ctx, map[string]any{
		"profile_id": profile.ID, "period_start": "2026-01-01", "period_end": "2026-03-31",
		"sync_sources": false, "create_obligation": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	estimates := result.(map[string]any)["estimates"].([]any)
	if len(estimates) != 1 {
		t.Fatalf("estimates=%d, want 1", len(estimates))
	}
}

func TestAdjustmentRejectsMismatchedPeriod(t *testing.T) {
	ctx := testCtx(t, "p-adjust", nil)
	profile := insertTestProfile(t, ctx, Profile{VATRegistered: true})
	periodResult, err := (&App{}).toolPeriodsOpen(ctx, map[string]any{
		"profile_id": profile.ID, "tax_type": "vat",
		"period_start": "2026-01-01", "period_end": "2026-03-31", "due_date": "2026-04-20",
	})
	if err != nil {
		t.Fatal(err)
	}
	periodID := int64FromAny(periodResult.(map[string]any)["period"].(map[string]any)["id"])
	_, err = (&App{}).toolAdjustmentsCreate(ctx, map[string]any{
		"profile_id": profile.ID, "period_id": periodID, "tax_type": "corporate_tax",
		"kind": "manual", "amount_cents": 1000, "currency": "EUR", "reason": "Correction",
	})
	if err == nil {
		t.Fatal("adjustment with a mismatched period should fail")
	}
}

func TestFailedEstimateDoesNotPersistCalculation(t *testing.T) {
	ctx := testCtx(t, "p-no-partial", nil)
	if err := seedTaxRules(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	profile := insertTestProfile(t, ctx, Profile{
		Country: "FR", Structure: "FR_EURL",
	})
	periodResult, err := (&App{}).toolPeriodsOpen(ctx, map[string]any{
		"profile_id": profile.ID, "tax_type": "social_contributions",
		"period_start": "2026-01-01", "period_end": "2026-01-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	periodID := int64FromAny(periodResult.(map[string]any)["period"].(map[string]any)["id"])
	_, err = (&App{}).toolEstimateSocial(ctx, map[string]any{
		"profile_id": profile.ID, "period_id": periodID, "create_obligation": true,
	})
	if err == nil {
		t.Fatal("missing due date and social amount should fail")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM tax_calculations WHERE project_id=?`, profile.ProjectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("calculations=%d, want 0 after failed estimate", count)
	}
}

func TestSocialEstimateDoesNotCallBillingOrBills(t *testing.T) {
	stub := &taxesPlatformStub{}
	stub.responder = func(app, tool string, input map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("unexpected %s:%s", app, tool)
	}
	ctx := testCtx(t, "p-social-source", stub)
	if err := seedTaxRules(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	profile := insertTestProfile(t, ctx, Profile{
		Structure: "ES_AUTONOMO", VATRegistered: true,
	})
	_, err := (&App{}).toolEstimateSocial(ctx, map[string]any{
		"profile_id":   profile.ID,
		"period_start": "2026-01-01", "period_end": "2026-01-31",
		"social_contribution_cents": 32000, "create_obligation": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("social estimate made %d cross-app calls", len(stub.calls))
	}
}
