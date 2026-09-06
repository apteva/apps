package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func financialFixture(t *testing.T, app *sdk.AppCtx, project string, money bool) *Objective {
	t.Helper()
	in := objectiveFixture("Financial", "sum")
	in.Targets[0].Query.App = ""
	in.Targets[0].Query.Topic = ""
	in.Targets[0].Currency = "EUR"
	in.Targets[0].Query.Value = "props.amount"
	in.Targets[0].PeriodStart = time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	in.Targets[0].PeriodEnd = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	if money {
		in.Targets[0].Query.Aggregation = "sum_money"
		in.Targets[0].Query.CurrencyField = "props.currency"
		in.Targets[0].Query.ReportingCurrency = "EUR"
		in.Targets[0].Query.AmountUnit = "major"
	}
	o, err := createObjective(app.AppDB(), project, in)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func financialPayment(t *testing.T, db *sql.DB, project string, at int64, amount int, currency, key string) int64 {
	t.Helper()
	e, err := insertEvent(db, EventInsert{App: "billing", Topic: "payment_received", ProjectID: project, TS: at, Props: fmt.Sprintf(`{"amount":%d,"currency":%q}`, amount, currency), Source: "track", UpsertKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func runFinancial(t *testing.T, app *sdk.AppCtx) {
	t.Helper()
	if err := (&App{}).financialRefreshWorker(context.Background(), app); err != nil {
		t.Fatal(err)
	}
}
func financialProgress(t *testing.T, app *sdk.AppCtx, o *Objective, want float64) *TargetProgress {
	t.Helper()
	p, err := cachedTargetProgress(app.AppDB(), o.Targets[0])
	if err != nil || p == nil || p.ActualValue == nil || *p.ActualValue != want {
		t.Fatalf("progress=%+v err=%v want=%v", p, err, want)
	}
	return p
}
func TestFinancialWorkerPaymentCorrectionDeleteAndScope(t *testing.T) {
	rec := tk.NewEmitRecorder()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithEmitter(rec))
	o := financialFixture(t, app, "p1", true)
	other := financialFixture(t, app, "p2", true)
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC).UnixMilli()
	id := financialPayment(t, app.AppDB(), "p1", at, 10, "EUR", "payment")
	financialPayment(t, app.AppDB(), "p2", at, 99, "EUR", "")
	runFinancial(t, app)
	p := financialProgress(t, app, o, 10)
	if p.Status != "ok" {
		t.Fatal(p)
	}
	if otherP, _ := cachedTargetProgress(app.AppDB(), other.Targets[0]); otherP != nil {
		t.Fatal("cross-project refresh", otherP)
	}
	if _, err := app.AppDB().Exec(`UPDATE events SET props='{"amount":7,"currency":"EUR"}' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	runFinancial(t, app)
	financialProgress(t, app, o, 7)
	if _, err := app.AppDB().Exec(`DELETE FROM events WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	runFinancial(t, app)
	p = financialProgress(t, app, o, 0)
	if p.Freshness != "unverified" {
		t.Fatal("empty capture is not confirmed zero", p)
	}
	var requests int
	app.AppDB().QueryRow(`SELECT COUNT(*) FROM financial_fx_requests`).Scan(&requests)
	if requests != 0 {
		t.Fatal("identity conversion requested FX")
	}
	events := rec.Events()
	if len(events) != 3 {
		t.Fatalf("expected one notification per persisted target: %+v", events)
	}
	for _, e := range events {
		if e.ProjectID != "p1" || e.Topic != "objective.progress.updated" {
			t.Fatal(e)
		}
	}
}
func TestFinancialFXFailureRecoveryAndUnchangedImports(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	o := financialFixture(t, app, "p1", true)
	at := time.Date(2025, 12, 12, 18, 0, 0, 0, time.UTC).UnixMilli()
	financialPayment(t, app.AppDB(), "p1", at, 10, "GBP", "")
	old := financialFetch
	defer func() { financialFetch = old }()
	calls := 0
	financialFetch = func(context.Context, *sdk.AppCtx, financialFXNeed) ([]financialObservation, error) {
		calls++
		return nil, errors.New("provider offline")
	}
	runFinancial(t, app)
	p, _ := cachedTargetProgress(app.AppDB(), o.Targets[0])
	if p == nil || p.Status != "error" || p.ActualValue != nil {
		t.Fatal(p)
	}
	runFinancial(t, app)
	if calls != 1 {
		t.Fatal("ignored provider backoff", calls)
	}
	obs := []financialObservation{{Base: "EUR", Quote: "GBP", Rate: "0.8", EffectiveAt: "2025-12-12T15:00:00Z", Provider: "ecb-reference-rates"}}
	financialFetch = func(context.Context, *sdk.AppCtx, financialFXNeed) ([]financialObservation, error) { return obs, nil }
	app.AppDB().Exec(`UPDATE financial_fx_requests SET next_retry=0`)
	runFinancial(t, app)
	p = financialProgress(t, app, o, 12.5)
	if p.Status != "ok" {
		t.Fatal(p)
	}
	rev, _ := financialRevision(app.AppDB(), "p1")
	need := financialFXNeed{Base: "GBP", Quote: "EUR", Day: "2025-12-12"}
	if err := importFinancialRates(context.Background(), app.AppDB(), "p1", need, obs); err != nil {
		t.Fatal(err)
	}
	after, _ := financialRevision(app.AppDB(), "p1")
	if after != rev {
		t.Fatal("identical import invalidated project")
	}
	upsertFXRate(app.AppDB(), "p1", FXRate{BaseCurrency: "EUR", QuoteCurrency: "GBP", AsOf: time.Date(2025, 12, 12, 15, 0, 0, 0, time.UTC).UnixMilli(), Rate: 0.5, Source: "manual"})
	if err := importFinancialRates(context.Background(), app.AppDB(), "p1", need, obs); err != nil {
		t.Fatal(err)
	}
	runFinancial(t, app)
	financialProgress(t, app, o, 20)
}
func TestFinancialLastSuccessPreservedOnFailure(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	o := financialFixture(t, app, "p1", false)
	id := financialPayment(t, app.AppDB(), "p1", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).UnixMilli(), 11, "EUR", "")
	runFinancial(t, app)
	before := financialProgress(t, app, o, 11)
	app.AppDB().Exec(`UPDATE events SET props='{"amount":"bad"}' WHERE id=?`, id)
	runFinancial(t, app)
	after := financialProgress(t, app, o, 11)
	if after.Status != "error" || *after.MeasuredAt != *before.MeasuredAt {
		t.Fatalf("failed calculation refreshed last success: %+v -> %+v", before, after)
	}
}
func TestFinancialLeaseAndRestart(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	o := financialFixture(t, app, "p1", false)
	financialPayment(t, app.AppDB(), "p1", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).UnixMilli(), 11, "EUR", "")
	app.AppDB().Exec(`UPDATE financial_projects SET lease_token='other-worker',lease_until=? WHERE project_id='p1'`, time.Now().Add(time.Minute).UnixMilli())
	runFinancial(t, app)
	p, _ := cachedTargetProgress(app.AppDB(), o.Targets[0])
	if p != nil {
		t.Fatal("overlapped lease")
	}
	app.AppDB().Exec(`UPDATE financial_projects SET lease_until=1 WHERE project_id='p1'`)
	runFinancial(t, app)
	financialProgress(t, app, o, 11)
	runFinancial(t, app.WithProject(""))
	var count int
	app.AppDB().QueryRow(`SELECT COUNT(*) FROM financial_projects WHERE project_id=''`).Scan(&count)
	if count != 0 {
		t.Fatal("empty worker scope")
	}
}
func TestFinancialInvalidationIsTransactionalAndLateResultRejected(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	o := financialFixture(t, app, "p1", false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	id := financialPayment(t, app.AppDB(), "p1", at, 11, "EUR", "")
	runFinancial(t, app)
	revision, _ := financialRevision(app.AppDB(), "p1")
	tx, _ := app.AppDB().Begin()
	tx.Exec(`UPDATE events SET props='{"amount":99}' WHERE id=?`, id)
	tx.Rollback()
	after, _ := financialRevision(app.AppDB(), "p1")
	if revision != after {
		t.Fatal("rolled back event left dirty work")
	}
	old := measureObjectiveTarget(app.AppDB(), "p1", o.Targets[0], false)
	started := time.Now().UnixMilli()
	app.AppDB().Exec(`UPDATE events SET props='{"amount":22}' WHERE id=?`, id)
	runFinancial(t, app)
	if err := persistObjectiveMeasurementAtRevision(app.AppDB(), o.Targets[0], old, time.Now().UnixMilli(), "p1", revision, started); err != nil {
		t.Fatal(err)
	}
	financialProgress(t, app, o, 22)
	target := o.Targets[0]
	app.AppDB().Exec(`UPDATE objective_targets SET updated_at=updated_at+1 WHERE id=?`, target.ID)
	if err := persistObjectiveMeasurement(app.AppDB(), target, old, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	financialProgress(t, app, o, 22)
}
func TestFinancialECBPublicationCalendar(t *testing.T) {
	for _, tc := range []struct{ at, want string }{{"2026-09-06T12:00:00Z", "2026-09-04"}, {"2026-04-06T17:00:00Z", "2026-04-02"}, {"2026-03-30T13:59:00Z", "2026-03-27"}, {"2026-03-30T14:00:00Z", "2026-03-30"}, {"2025-12-26T17:00:00Z", "2025-12-24"}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if got := expectedECBPublication(at).Format("2006-01-02"); got != tc.want {
			t.Errorf("%s = %s want %s", tc.at, got, tc.want)
		}
	}
}
func TestFinancialSharingAdoptsExistingComponentAndRevokes(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("source"))
	source := financialFixture(t, app, "source", false)
	dest := financialFixture(t, app, "management", false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	financialPayment(t, app.AppDB(), "source", at, 11, "EUR", "")
	component := financialComponent(t, app.AppDB(), "management", at, 3)
	grant, err := grantFinancialShare(app.AppDB(), "source", source.Targets[0].ID, "management", "revenue", "editor", "receipts")
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := createFinancialMapping(context.Background(), app.AppDB(), "management", grant, dest.Targets[0].ID, component, "editor")
	if err != nil {
		t.Fatal(err)
	}
	runFinancial(t, app.WithProject("management"))
	p, _ := cachedTargetProgress(app.AppDB(), dest.Targets[0])
	if p == nil || p.Status != "error" {
		t.Fatal("unavailable source treated as zero", p)
	}
	runFinancial(t, app)
	runFinancial(t, app.WithProject("management"))
	financialProgress(t, app, dest, 11)
	var raw string
	app.AppDB().QueryRow(`SELECT props FROM events WHERE id=?`, component).Scan(&raw)
	runFinancial(t, app.WithProject("management"))
	var repeated string
	app.AppDB().QueryRow(`SELECT props FROM events WHERE id=?`, component).Scan(&repeated)
	if raw != repeated {
		t.Fatal("repeat changed component")
	}
	var n int
	app.AppDB().QueryRow(`SELECT COUNT(*) FROM events WHERE project_id='management'`).Scan(&n)
	if n != 1 {
		t.Fatal("duplicated adopted component", n)
	}
	if err = revokeFinancialShare(app.AppDB(), "source", grant); err != nil {
		t.Fatal(err)
	}
	runFinancial(t, app.WithProject("management"))
	p = financialProgress(t, app, dest, 11)
	if p.Status != "error" {
		t.Fatal("revoked share marked current", p)
	}
	var msg string
	app.AppDB().QueryRow(`SELECT last_error FROM financial_mappings WHERE id=?`, mapping).Scan(&msg)
	if !strings.Contains(msg, "revoked") {
		t.Fatal(msg)
	}
}
func TestFinancialSharingRejectsUnauthorizedAndSelf(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("source"))
	source := financialFixture(t, app, "source", false)
	dest := financialFixture(t, app, "management", false)
	if _, err := grantFinancialShare(app.AppDB(), "intruder", source.Targets[0].ID, "management", "revenue", "u", "receipts"); err == nil {
		t.Fatal("foreign target shared")
	}
	if _, err := grantFinancialShare(app.AppDB(), "source", source.Targets[0].ID, "source", "revenue", "u", "receipts"); err == nil {
		t.Fatal("self sharing")
	}
	grant, _ := grantFinancialShare(app.AppDB(), "source", source.Targets[0].ID, "management", "revenue", "u", "receipts")
	if _, err := createFinancialMapping(context.Background(), app.AppDB(), "intruder", grant, dest.Targets[0].ID, 1, "u"); err == nil {
		t.Fatal("grant leaked across destination")
	}
	prior := globalCtx
	globalCtx = app
	defer func() { globalCtx = prior }()
	req := httptest.NewRequest("POST", "/financial-shares", strings.NewReader(`{}`))
	req.Header.Set("X-User-ID", "owner")
	req.Header.Set(trustedProjectHeader, "source")
	req.Header.Set("X-Apteva-Bound-Caller-Install-ID", "99")
	w := httptest.NewRecorder()
	(&App{}).handleFinancialShares(w, req)
	if w.Code != 403 {
		t.Fatal("app used owner's identity to grant sharing", w.Code)
	}
}
func TestFinancialConfirmedZeroNeedsScopedReconciliation(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	o := financialFixture(t, app, "p1", true)
	runFinancial(t, app)
	financialProgress(t, app, o, 0)
	old := globalCtx
	globalCtx = app
	defer func() { globalCtx = old }()
	r := httptest.NewRequest("POST", "/financial-refresh", strings.NewReader(fmt.Sprintf(`{"verify_target":%d,"verified_through":%d}`, o.Targets[0].ID, time.Now().UnixMilli())))
	r.Header.Set("X-User-ID", "reconciler")
	r.Header.Set(trustedProjectHeader, "p1")
	w := httptest.NewRecorder()
	(&App{}).handleFinancialRefresh(w, r)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	runFinancial(t, app)
	p := financialProgress(t, app, o, 0)
	if p.Freshness != "confirmed_zero" || !p.DataVerified {
		t.Fatal("reconciled zero not recognized", p)
	}
	var actor string
	if err := app.AppDB().QueryRow(`SELECT verified_by FROM financial_targets WHERE target_id=?`, o.Targets[0].ID).Scan(&actor); err != nil || actor != "reconciler" {
		t.Fatal("attestation audit", actor, err)
	}
	financialPayment(t, app.AppDB(), "p1", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).UnixMilli(), 1, "EUR", "")
	runFinancial(t, app)
	p = financialProgress(t, app, o, 1)
	if p.DataVerified {
		t.Fatal("input change retained old completeness assertion")
	}
}
func TestFinancialFailedPersistenceEmitsNothing(t *testing.T) {
	rec := tk.NewEmitRecorder()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithEmitter(rec))
	financialFixture(t, app, "p1", false)
	financialPayment(t, app.AppDB(), "p1", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).UnixMilli(), 11, "EUR", "")
	_, err := app.AppDB().Exec(`CREATE TRIGGER reject_progress BEFORE INSERT ON objective_progress BEGIN SELECT RAISE(ABORT,'storage failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	runFinancial(t, app)
	if len(rec.Events()) != 0 {
		t.Fatal("announced unpersisted progress", rec.Events())
	}
}
func TestFinancialCrossRatesAndIntradayCoverage(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	obs := []financialObservation{{Base: "EUR", Quote: "GBP", Rate: "0.8", EffectiveAt: "2025-12-12T15:00:00Z", Provider: "ecb-reference-rates"}, {Base: "EUR", Quote: "USD", Rate: "1.2", EffectiveAt: "2025-12-12T15:00:00Z", Provider: "ecb-reference-rates"}}
	err := importFinancialRates(context.Background(), app.AppDB(), "p1", financialFXNeed{Base: "GBP", Quote: "USD", Day: "2025-12-12"}, obs)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := loadFXRateIndex(app.AppDB(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	rate, _, err := idx.resolve("GBP", "USD", time.Date(2025, 12, 12, 17, 0, 0, 0, time.UTC).UnixMilli())
	if err != nil || math.Abs(rate-1.5) > 1e-12 {
		t.Fatal("cross FX", rate, err)
	}
	o := financialFixture(t, app, "p1", true)
	financialPayment(t, app.AppDB(), "p1", time.Date(2025, 12, 12, 9, 0, 0, 0, time.UTC).UnixMilli(), 1, "GBP", "")
	financialPayment(t, app.AppDB(), "p1", time.Date(2025, 12, 12, 18, 0, 0, 0, time.UTC).UnixMilli(), 1, "GBP", "")
	upsertFXRate(app.AppDB(), "p1", FXRate{BaseCurrency: "GBP", QuoteCurrency: "EUR", AsOf: time.Date(2025, 12, 11, 15, 0, 0, 0, time.UTC).UnixMilli(), Rate: 1.2})
	if err = financialFXCoverage(app.AppDB(), "p1", o.Targets[0]); err == nil {
		t.Fatal("early payment hid missing later publication")
	}
}
func TestFinancialMappingCycleRenewalAndSavedPeriod(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("a"))
	a := financialFixture(t, app, "a", false)
	b := financialFixture(t, app, "b", false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	ea := financialComponent(t, app.AppDB(), "a", at, 1)
	eb := financialComponent(t, app.AppDB(), "b", at, 1)
	grant, _ := grantFinancialShare(app.AppDB(), "a", a.Targets[0].ID, "b", "revenue", "u", "receipts")
	id, err := createFinancialMapping(context.Background(), app.AppDB(), "b", grant, b.Targets[0].ID, eb, "u")
	if err != nil {
		t.Fatal(err)
	}
	reverse, _ := grantFinancialShare(app.AppDB(), "b", b.Targets[0].ID, "a", "revenue", "u", "receipts")
	if _, err = createFinancialMapping(context.Background(), app.AppDB(), "a", reverse, a.Targets[0].ID, ea, "u"); err == nil {
		t.Fatal("cycle accepted")
	}
	app.AppDB().Exec(`UPDATE objective_targets SET updated_at=updated_at+1 WHERE id=?`, a.Targets[0].ID)
	if _, err = createFinancialMapping(context.Background(), app.AppDB(), "b", grant, b.Targets[0].ID, eb, "u"); err == nil {
		t.Fatal("old source consent survived edit")
	}
	renewed, _ := grantFinancialShare(app.AppDB(), "a", a.Targets[0].ID, "b", "revenue", "u", "receipts")
	same, err := createFinancialMapping(context.Background(), app.AppDB(), "b", renewed, b.Targets[0].ID, eb, "u")
	if err != nil || same != id {
		t.Fatal("renewal duplicated mapping", same, err)
	}
	app.AppDB().Exec(`UPDATE objective_targets SET period_end=period_end+1,updated_at=updated_at+1 WHERE id=?`, b.Targets[0].ID)
	if _, err = createFinancialMapping(context.Background(), app.AppDB(), "b", renewed, b.Targets[0].ID, eb, "u"); err == nil {
		t.Fatal("saved period mismatch accepted")
	}
}
func TestFinancialMissingProviderDoesNotBlockEURGoal(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	fx := financialFixture(t, app, "p1", true)
	eurIn := objectiveFixture("Local EUR", "sum")
	eurIn.Targets[0] = fx.Targets[0]
	eurIn.Targets[0].ID = 0
	eurIn.Targets[0].Query.Where = map[string]any{"props.currency": "EUR"}
	eur, err := createObjective(app.AppDB(), "p1", eurIn)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 18, 0, 0, 0, time.UTC).UnixMilli()
	financialPayment(t, app.AppDB(), "p1", at, 10, "GBP", "")
	financialPayment(t, app.AppDB(), "p1", at, 5, "EUR", "")
	old := financialFetch
	financialFetch = func(context.Context, *sdk.AppCtx, financialFXNeed) ([]financialObservation, error) {
		return nil, errors.New("Currencies is not installed")
	}
	defer func() { financialFetch = old }()
	runFinancial(t, app)
	p := financialProgress(t, app, eur, 5)
	if p.Status != "ok" {
		t.Fatal("optional dependency blocked EUR", p)
	}
	p, _ = cachedTargetProgress(app.AppDB(), fx.Targets[0])
	if p == nil || p.Status != "error" {
		t.Fatal("missing FX silently succeeded", p)
	}
}

func financialComponent(t *testing.T, db *sql.DB, project string, at int64, amount int) int64 {
	t.Helper()
	id, err := insertEvent(db, EventInsert{App: "financial-components", Topic: "component", ProjectID: project, Source: "track", TS: at, Props: fmt.Sprintf(`{"amount":%d,"currency":"EUR"}`, amount)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestFinancialSavedMonthUsesMadridDSTBounds(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, 0)
	if end.Sub(start) != 31*24*time.Hour+time.Hour {
		t.Fatal("fixture omitted DST")
	}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	in := objectiveFixture("October", "sum")
	in.Targets[0].Query.Value = "props.amount"
	in.Targets[0].PeriodStart = start.UnixMilli()
	in.Targets[0].PeriodEnd = end.UnixMilli()
	in.Targets[0].Timezone = "Europe/Madrid"
	o, err := createObjective(app.AppDB(), "p1", in)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		ts     int64
		amount int
	}{{start.UnixMilli() - 1, 99}, {start.UnixMilli(), 1}, {end.UnixMilli() - 1, 2}, {end.UnixMilli(), 99}} {
		financialPayment(t, app.AppDB(), "p1", row.ts, row.amount, "EUR", "")
	}
	runFinancial(t, app)
	financialProgress(t, app, o, 3)
}
func TestFinancialIncomeStreamCannotCountRevenueAndSettlement(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("source"))
	revenue := financialFixture(t, app, "source", false)
	settlement := financialFixture(t, app, "source", false)
	dest := financialFixture(t, app, "management", false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	one := financialComponent(t, app.AppDB(), "management", at, 1)
	two := financialComponent(t, app.AppDB(), "management", at, 1)
	first, err := grantFinancialShare(app.AppDB(), "source", revenue.Targets[0].ID, "management", "revenue", "u", "subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createFinancialMapping(context.Background(), app.AppDB(), "management", first, dest.Targets[0].ID, one, "u"); err != nil {
		t.Fatal(err)
	}
	second, err := grantFinancialShare(app.AppDB(), "source", settlement.Targets[0].ID, "management", "other", "u", "subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createFinancialMapping(context.Background(), app.AppDB(), "management", second, dest.Targets[0].ID, two, "u"); err == nil {
		t.Fatal("same income stream double-counted")
	}
	// Another project remains a separate component even with the same local key.
	other := financialFixture(t, app, "other-source", false)
	third, err := grantFinancialShare(app.AppDB(), "other-source", other.Targets[0].ID, "management", "revenue", "u", "subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createFinancialMapping(context.Background(), app.AppDB(), "management", third, dest.Targets[0].ID, two, "u"); err != nil {
		t.Fatal("different project collapsed", err)
	}
}
func TestFinancialFXCorrectionsUseLatestObservationAndImmutableProvenance(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	need := financialFXNeed{Base: "GBP", Quote: "EUR", Day: "2025-12-12"}
	old := financialObservation{RateID: 1, Base: "EUR", Quote: "GBP", Rate: "0.8", EffectiveAt: "2025-12-12T15:00:00Z", Provider: "ecb-reference-rates", ProviderRef: "https://data.ecb.europa.eu/", ObservedAt: "2026-09-01T12:00:00Z"}
	corrected := old
	corrected.RateID = 2
	corrected.Rate = "0.9"
	corrected.ObservedAt = "2026-09-02T12:00:00Z"
	for _, list := range [][]financialObservation{{old}, {corrected, old}, {old, corrected}} {
		if err := importFinancialRates(context.Background(), app.AppDB(), "p1", need, list); err != nil {
			t.Fatal(err)
		}
	}
	var rate float64
	var revisions, provenance int
	app.AppDB().QueryRow(`SELECT rate FROM fx_rates WHERE project_id='p1'`).Scan(&rate)
	app.AppDB().QueryRow(`SELECT COUNT(*) FROM fx_rate_revisions WHERE project_id='p1'`).Scan(&revisions)
	app.AppDB().QueryRow(`SELECT COUNT(*) FROM financial_fx_provenance WHERE project_id='p1'`).Scan(&provenance)
	if rate != 0.9 || revisions != 2 || provenance != 2 {
		t.Fatal(rate, revisions, provenance)
	}
	if _, err := app.AppDB().Exec(`UPDATE financial_fx_provenance SET observations='[]'`); err == nil {
		t.Fatal("provenance can be rewritten")
	}
}
